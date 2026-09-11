package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

type sshKeyDeriveDocument struct {
	SchemaVersion int                `json:"schema_version"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`
	Plan          sshhost.KeyPlan    `json:"plan"`
	Result        *sshhost.KeyResult `json:"result,omitempty"`
	ErrorCode     string             `json:"error_code,omitempty"`
}

func newSSHKeyDeriveCmd(app *App) *cobra.Command {
	var apply, yes, jsonOut bool
	cmd := &cobra.Command{
		Use: "derive PRIVATE_KEY", Short: "Preview or derive a missing SSH public-key companion",
		Long: `Preview creation of PRIVATE_KEY.pub without reading private contents or running
ssh-keygen. --apply invokes native ssh-keygen -y after confirmation and publishes
the public companion without overwriting an existing file. Native ssh-keygen
owns passphrase prompts; unattended encrypted keys require interactive execution.
Existing companions are reported without modification. Use key doctor for
permission repairs before deriving. Keys must be within the user's ~/.ssh tree.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if yes && !apply {
				return asUsageError(errors.New("--yes requires --apply"))
			}
			if strings.HasSuffix(strings.ToLower(args[0]), ".pub") {
				return asUsageError(errors.New("select a private identity path, not its .pub companion"))
			}
			return runSSHKeyDerive(cmd.Context(), app, args[0], apply, yes, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Derive and save the missing public companion after confirmation")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm public companion creation without prompting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print one versioned plan or result without key material")
	return cmd
}

func runSSHKeyDerive(ctx context.Context, app *App, path string, apply, yes, jsonOut bool) error {
	s, err := app.sshHosts()
	if err != nil {
		return err
	}
	interactive := app.interactive() && !jsonOut
	plan, err := s.PlanKey(ctx, sshhost.KeyRequest{Operation: sshhost.KeyUse, Path: path, AllowDerive: true, Interactive: interactive})
	doc := sshKeyDeriveDocument{SchemaVersion: sshCLISchemaVersion, Kind: "ssh_key_derive_plan", Status: "planned", Plan: plan}
	finish := func(err error) error {
		doc.ErrorCode = sshErrorCode(err)
		if jsonOut {
			if encodeErr := writeSSHJSON(app, doc); encodeErr != nil {
				return encodeErr
			}
		} else {
			fmt.Fprintf(app.Out, "SSH public companion: %s\n  %s\n", doc.Status, plan.PublicPath)
			renderSSHKeyDiagnostics(app, plan.Diagnostics)
		}
		return err
	}
	if err != nil {
		doc.Status = "failed"
		return finish(err)
	}
	if !plan.Ready() {
		doc.Status = "blocked"
		return finish(sshhost.ErrBlocked)
	}
	if plan.Operation != sshhost.KeyDerive {
		doc.Status = "exists"
		return finish(nil)
	}
	if !apply {
		return finish(nil)
	}
	if !yes {
		if !interactive {
			doc.Status = "confirmation_required"
			return finish(errors.New("--yes is required to derive a public companion outside an interactive terminal"))
		}
		fmt.Fprintf(app.Out, "Derive %s → %s\n", plan.IdentityFile, plan.PublicPath)
		confirmed, err := newPrompter(app).confirm("Create this missing public key companion?", false)
		if err != nil {
			return err
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	doc.Kind = "ssh_key_derive_result"
	err = withSSHOperationLock(ctx, app, func() error {
		if err := s.RevalidateKeySelection(ctx, plan); err != nil {
			return err
		}
		result, err := s.ApplyKey(ctx, plan)
		doc.Result = &result
		return err
	})
	doc.Status = "created"
	if err != nil {
		doc.Status = "failed"
	}
	return finish(err)
}
