package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

const sshKeyDoctorConfirmation = "Apply these SSH key permission repairs?"

type sshKeyDoctorDocument struct {
	SchemaVersion int                       `json:"schema_version"`
	Kind          string                    `json:"kind"`
	Status        string                    `json:"status"`
	Scope         string                    `json:"scope"`
	Complete      bool                      `json:"complete"`
	KeyPaths      []string                  `json:"key_paths"`
	Plan          sshhost.PermissionPlan    `json:"plan"`
	Result        *sshhost.PermissionResult `json:"result,omitempty"`
	Recheck       *sshhost.PermissionPlan   `json:"recheck,omitempty"`
	Diagnostics   []sshhost.Diagnostic      `json:"diagnostics,omitempty"`
	ErrorCode     string                    `json:"error_code,omitempty"`
}

func newSSHKeyDoctorCmd(app *App) *cobra.Command {
	var fix, yes, jsonOut bool
	var keys []string
	cmd := &cobra.Command{
		Use: "doctor", Short: "Check SSH key permissions and preview repairs",
		Long: `Inspect canonical SSH paths and discoverable key-file permissions without changes.
The default bounded scan finds public-key companions and standard identity filenames
under ~/.ssh. --key limits inspection to selected paths plus ~/.ssh, config and dev.d.
It does not read private-key contents, query agents, evaluate SSH configuration,
or contact remote hosts.
--fix previews exact permission tightening and asks before applying it; --yes skips
that confirmation. Noninteractive and JSON repairs require --yes.
Repairs never change contents, ownership or ACLs, and completed tightening is retained
on partial failure. Key format, passphrases and remote authentication are not checked.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if yes && !fix {
				return asUsageError(errors.New("--yes requires --fix"))
			}
			for _, key := range keys {
				if strings.TrimSpace(key) == "" {
					return asUsageError(errors.New("--key requires a nonempty path"))
				}
			}
			return runSSHKeyDoctor(cmd.Context(), app, keys, fix, yes, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Review and apply safe permission tightening")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the permission repair plan without prompting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print one versioned diagnosis or repair result")
	cmd.Flags().StringArrayVar(&keys, "key", nil, "Inspect only this key path and canonical SSH paths (repeatable)")
	return cmd
}

func planSSHKeyDoctor(ctx context.Context, service *sshhost.Service, keys []string) (sshKeyDoctorDocument, error) {
	ctx, cancel := context.WithTimeout(ctx, sshShowTimeout)
	defer cancel()
	document := sshKeyDoctorDocument{SchemaVersion: sshCLISchemaVersion, Kind: "ssh_key_doctor_plan", Status: "ready", Scope: "selected", Complete: true, KeyPaths: append([]string{}, keys...)}
	if len(keys) == 0 {
		document.Scope = "discovered"
		scan, err := service.ScanPermissionKeys(ctx)
		document.KeyPaths = append([]string{}, scan.KeyPaths...)
		document.Complete = scan.Complete
		document.Diagnostics = scan.Diagnostics
		if err != nil {
			return document, err
		}
	}
	plan, err := service.PlanPermissions(ctx, sshhost.PermissionRequest{KeyPaths: document.KeyPaths})
	document.Plan = plan
	if err != nil {
		return document, err
	}
	switch {
	case !document.Complete:
		document.Status = "incomplete"
	case !plan.Ready():
		document.Status = "blocked"
	case plan.NeedsRepair():
		document.Status = "repairable"
	}
	return document, nil
}

func runSSHKeyDoctor(ctx context.Context, app *App, keys []string, fix, yes, jsonOut bool) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	document, err := planSSHKeyDoctor(ctx, service, keys)
	if err != nil {
		document.Status = "failed"
		document.Complete = false
		document.ErrorCode = sshErrorCode(err)
		return finishSSHKeyDoctor(app, jsonOut, document, err)
	}
	if !document.Complete || !document.Plan.Ready() {
		document.ErrorCode = "blocked"
		return finishSSHKeyDoctor(app, jsonOut, document, fmt.Errorf("SSH key permission diagnosis needs manual attention; inspect the reported paths or limit inspection with --key: %w", sshhost.ErrBlocked))
	}
	if !fix || !document.Plan.NeedsRepair() {
		err := finishSSHKeyDoctor(app, jsonOut, document, nil)
		if !jsonOut && document.Plan.NeedsRepair() {
			fmt.Fprintln(app.Out, "Rerun with --fix to review and apply these repairs.")
		}
		return err
	}
	if !yes && (jsonOut || !app.interactive()) {
		document.Status = "confirmation_required"
		document.ErrorCode = "confirmation_required"
		return finishSSHKeyDoctor(app, jsonOut, document, errors.New("--yes is required to fix SSH key permissions without an interactive terminal"))
	}
	if !jsonOut {
		renderSSHKeyDoctor(app, document)
		fmt.Fprintln(app.Out, "Completed permission repairs are retained if a later step fails.")
	}
	if !yes {
		confirmed, err := newPrompter(app).confirm(sshKeyDoctorConfirmation, false)
		if err != nil {
			return err
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	result, err := service.ApplyPermissions(ctx, document.Plan)
	document.Kind = "ssh_key_doctor_result"
	document.Result = &result
	document.Status = result.Status
	if err != nil {
		document.ErrorCode = sshErrorCode(err)
		return finishSSHKeyDoctor(app, jsonOut, document, err)
	}
	// A fresh diagnosis reports newly discovered or remaining issues rather than
	// claiming that repairing the reviewed paths made every current key ready.
	after, err := planSSHKeyDoctor(ctx, service, keys)
	document.Recheck = &after.Plan
	document.Complete = after.Complete
	document.Diagnostics = after.Diagnostics
	if err != nil || !after.Complete || !after.Plan.Ready() || after.Plan.NeedsRepair() {
		if err != nil {
			document.Complete = false
		}
		document.Status = "partial"
		document.ErrorCode = "remaining_permissions"
		if err == nil {
			err = errors.New("reviewed repairs were applied; run dev ssh key doctor again to review remaining issues")
		}
		return finishSSHKeyDoctor(app, jsonOut, document, err)
	}
	document.Status = "applied"
	return finishSSHKeyDoctor(app, jsonOut, document, nil)
}

func finishSSHKeyDoctor(app *App, jsonOut bool, document sshKeyDoctorDocument, err error) error {
	if jsonOut {
		if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
			return encodeErr
		}
	} else {
		renderSSHKeyDoctor(app, document)
	}
	return err
}

func renderSSHKeyDoctor(app *App, document sshKeyDoctorDocument) {
	fmt.Fprintf(app.Out, "SSH key doctor: %s (%d key paths; %s scope)\n", document.Status, len(document.KeyPaths), document.Scope)
	if document.Result != nil {
		renderSSHPermissionOutcomes(app, *document.Result)
	} else {
		renderSSHPermissionChanges(app, document.Plan)
	}
	renderSSHKeyDiagnostics(app, document.Diagnostics)
	renderSSHKeyDiagnostics(app, document.Plan.Diagnostics)
	if document.Recheck != nil {
		renderSSHPermissionChanges(app, *document.Recheck)
		renderSSHKeyDiagnostics(app, document.Recheck.Diagnostics)
	}
}
