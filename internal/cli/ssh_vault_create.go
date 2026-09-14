package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshvault"
	"github.com/spf13/cobra"
)

type sshVaultCreateOptions struct {
	request                           sshflow.VaultKeyRequest
	dryRun, json                      bool
	yes                               bool
	forPicker                         bool
	experimentalSet, nativeContextSet bool
}

type sshVaultCreateDocument struct {
	SchemaVersion int                      `json:"schema_version"`
	Kind          string                   `json:"kind"`
	Status        string                   `json:"status"`
	DryRun        bool                     `json:"dry_run"`
	Preview       *sshflow.VaultKeyPreview `json:"preview,omitempty"`
	Result        *sshflow.VaultKeyResult  `json:"result,omitempty"`
	ErrorCode     string                   `json:"error_code,omitempty"`
}

func newSSHKeyCreateCmd(app *App) *cobra.Command {
	var options sshVaultCreateOptions
	cmd := &cobra.Command{Use: "create", Short: "Create a new SSH key in an explicitly reviewed vault", Args: cobra.NoArgs,
		Long: `Create a new SSH key in 1Password, or explicitly opt into experimental
Bitwarden in-memory generation under a reviewed native profile. Bitwarden needs
both --experimental and --native-context; --yes never substitutes for either.
--desktop is a separate interactive Bitwarden GUI handoff: it reports newly
visible agent keys, not vault item creation. No existing private key is imported
and no item is deleted. No SSH configuration or remote key installation occurs.
--dry-run emits unobserved intent only and never invokes op, bw, node or agents.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.experimentalSet = cmd.Flags().Changed("experimental")
			options.nativeContextSet = cmd.Flags().Changed("native-context")
			ctx := cmd.Context()
			if options.json || !app.interactive() {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
			}
			_, err := runSSHVaultCreation(ctx, app, options)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.request.Provider, "provider", "", "vault provider: 1password or bitwarden (omit for the interactive wizard)")
	flags.StringVar(&options.request.AccountID, "account", "", "exact native account/user ID (required with --yes; blank otherwise reviews the current account)")
	flags.StringVar(&options.request.VaultID, "vault", "", "exact 1Password vault ID, or personal for Bitwarden")
	flags.StringVar(&options.request.Title, "title", "SSH key", "new vault item title")
	flags.BoolVar(&options.request.Experimental, "experimental", false, "explicitly approve experimental Bitwarden in-memory key generation")
	flags.BoolVar(&options.request.NativeContextApproved, "native-context", false, "explicitly delegate Bitwarden endpoints to the reviewed native profile; endpoints remain unverified")
	flags.BoolVar(&options.request.Desktop, "desktop", false, "use an interactive Bitwarden desktop handoff instead of an RPC creation")
	flags.BoolVar(&options.dryRun, "dry-run", false, "show unobserved intent only; do not query providers or agents and do not create a key")
	flags.BoolVar(&options.yes, "yes", false, "approve creation in the exact account/vault after planning (does not approve experimental or native-context mode)")
	flags.BoolVar(&options.json, "json", false, "emit one public-only plan/result document without a complete public key line")
	registerFlagCompletion(cmd, "provider", fixedCompletions("1password", "bitwarden"))
	return cmd
}

func collectSSHVaultRequest(ctx context.Context, app *App, options sshVaultCreateOptions) (sshflow.VaultKeyRequest, error) {
	request := options.request
	interactive := app.interactive() && !options.json
	wizard := request.Provider == ""
	if wizard {
		if !interactive {
			return request, errors.New("--provider is required outside the interactive vault wizard")
		}
		service, err := app.sshHosts()
		if err != nil {
			return request, err
		}
		var choices []picker.Item
		for _, choice := range sshVaultKeyChoices(service, false) {
			value := choice.VaultAction
			if choice.UnavailableReason != "" {
				value = "unavailable:" + value
			}
			choices = append(choices, picker.Item{Value: value, Label: choice.Label, Description: choice.Description})
		}
		for {
			selected, err := sshPick(ctx, app, "Choose SSH vault key creation", choices, false)
			if err != nil {
				return request, err
			}
			if strings.HasPrefix(selected[0].Value, "unavailable:") {
				fmt.Fprintln(app.Err, selected[0].Description)
				continue
			}
			choice, err := sshVaultRequestForAction(selected[0].Value, request.Title)
			if err != nil {
				return request, err
			}
			request.Provider, request.Desktop = choice.Provider, choice.Desktop
			break
		}
	}
	if request.Desktop {
		return request, nil
	}
	prompt := newPrompter(app)
	if interactive && !options.yes {
		var err error
		if request.AccountID == "" {
			request.AccountID, err = prompt.line("Exact native account/user ID (blank reviews the current account)", "")
			if err != nil {
				return request, err
			}
		}
		if request.Provider == "1password" && request.VaultID == "" {
			request.VaultID, err = prompt.line("Exact 1Password vault ID (not its display name)", "")
			if err != nil {
				return request, err
			}
		}
		if wizard || options.forPicker {
			request.Title, err = prompt.line("New vault SSH key title", request.Title)
			if err != nil {
				return request, err
			}
		}
	}
	if !options.dryRun && (options.yes || !interactive) && request.AccountID == "" {
		return request, errors.New("an exact --account ID is required for unattended/--yes vault creation; use the interactive wizard to review the current account")
	}
	if request.Provider == "bitwarden" {
		if !request.Experimental {
			if !interactive || options.yes || options.experimentalSet {
				return request, sshvault.ErrExperimental
			}
			approved, err := prompt.confirm("Approve experimental in-memory key generation? Private bytes briefly enter process memory/native input; no guarantee covers swap, core dumps, native persistence or perfect erasure", false)
			if err != nil {
				return request, err
			}
			if !approved {
				return request, errPromptCanceled
			}
			request.Experimental = true
		}
		if !request.NativeContextApproved {
			if !interactive || options.yes || options.nativeContextSet {
				return request, sshvault.ErrUnsupportedContext
			}
			approved, err := prompt.confirm("Separately approve delegating endpoints/configuration to the reviewed Bitwarden native profile? Endpoint status stays unverified and destination binding stays unknown", false)
			if err != nil {
				return request, err
			}
			if !approved {
				return request, errPromptCanceled
			}
			request.NativeContextApproved = true
		}
	}
	return request, nil
}

func runSSHVaultCreation(ctx context.Context, app *App, options sshVaultCreateOptions) (sshflow.VaultKeyResult, error) {
	if _, native := app.In.(fileDescriptor); !native {
		app.In = bufio.NewReader(app.In)
	}
	document := sshVaultCreateDocument{SchemaVersion: 1, Kind: "ssh_key_create", Status: "not_started", DryRun: options.dryRun}
	finish := func(result sshflow.VaultKeyResult, err error) (sshflow.VaultKeyResult, error) {
		if err != nil {
			document.ErrorCode = sshVaultErrorCode(err)
		}
		if options.json {
			if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
				return result, encodeErr
			}
		} else if document.Result != nil {
			fmt.Fprintln(app.Out, strings.Join(document.Result.Lines(), "\n"))
		} else if document.Preview != nil {
			fmt.Fprintln(app.Out, "Vault action: "+document.Status)
		}
		return result, err
	}
	request, err := collectSSHVaultRequest(ctx, app, options)
	if err != nil {
		document.Status = "blocked"
		return finish(sshflow.VaultKeyResult{}, err)
	}
	request, _, err = normalizeSSHVaultRequest(request)
	if err != nil {
		document.Status = "blocked"
		return finish(sshflow.VaultKeyResult{}, publicSSHVaultError(err))
	}
	if options.dryRun {
		preview := sshflow.VaultKeyPreview{Request: request, IntentOnly: true, Notes: []string{"Account, vault, native context and agent are UNOBSERVED. No provider/client/agent was queried. Actual creation requires a fresh service-bound native account/vault plan and separate approval; no readiness or creation is implied."}}
		if request.Provider == "bitwarden" && !request.Desktop {
			preview.EndpointStatus = sshvault.EndpointUnverified
			preview.Notes = append(preview.Notes, "Experimental native-profile mode never attests endpoints or promises complete memory erasure.")
		}
		document.Status, document.Preview = "intent_only", &preview
		if !options.json {
			fmt.Fprintln(app.Out, strings.Join(preview.Lines(), "\n"))
		}
		return finish(sshflow.VaultKeyResult{Status: "intent_only", Context: preview, AgentStatus: "unobserved"}, nil)
	}
	if request.Desktop && (!app.interactive() || options.json) {
		document.Status = "blocked"
		return finish(sshflow.VaultKeyResult{}, errors.New("Bitwarden desktop handoff requires an interactive terminal and explicit fingerprint selection"))
	}
	prepared, err := prepareSSHVaultKey(ctx, app, request)
	if err != nil {
		document.Status = "blocked"
		return finish(sshflow.VaultKeyResult{}, err)
	}
	preview := prepared.Preview()
	document.Preview = &preview
	if !options.json {
		fmt.Fprintln(app.Out, strings.Join(preview.Lines(), "\n"))
	}
	if request.Desktop || !options.yes {
		if !app.interactive() || options.json {
			document.Status = "confirmation_required"
			return finish(sshflow.VaultKeyResult{}, errors.New("--yes is required to create a vault item without an interactive confirmation"))
		}
		question := "Create this new vault SSH key now? The item remains if later SSH setup is canceled"
		if request.Desktop {
			question = "Have you completed the native Bitwarden desktop steps and want to refresh this captured agent now?"
		}
		approved, err := newPrompter(app).confirm(question, false)
		if err != nil || !approved {
			document.Status = "canceled"
			if err == nil {
				err = errPromptCanceled
			}
			return finish(sshflow.VaultKeyResult{}, err)
		}
	}
	result, err := applySSHVaultKey(ctx, prepared)
	if err == nil && request.Desktop && !options.forPicker && len(result.Keys) > 0 {
		selected, selectionErr := pickSSHVaultAgentKey(ctx, app, result)
		if selectionErr != nil {
			err = selectionErr
		} else if selected != nil {
			result.Keys = []sshflow.VaultAgentKey{*selected}
			result.Notes = append(result.Notes, "Explicitly selected advertised fingerprint: "+selected.Fingerprint+"; no SSH changes were made.")
		} else {
			result.AgentStatus = "not_selected"
		}
	}
	document.Status, document.Result = result.Status, &result
	return finish(result, err)
}

func sshVaultErrorCode(err error) string {
	for _, entry := range []struct {
		err  error
		code string
	}{
		{sshvault.ErrExperimental, "experimental_approval_required"}, {sshvault.ErrUnsupportedContext, "native_context_approval_required"},
		{sshvault.ErrLocked, "vault_locked"}, {sshvault.ErrUnsupportedVersion, "unsupported_version"}, {sshvault.ErrNativeContext, "native_context_unavailable"},
		{sshvault.ErrEntrypoint, "native_entrypoint_unsupported"}, {sshvault.ErrStale, "source_changed"}, {sshvault.ErrPlanUsed, "creation_already_attempted"},
		{sshvault.ErrUnknown, "creation_unknown"}, {sshvault.ErrPublicKey, "created_public_key_unverified"}, {sshvault.ErrUnsafe, "invalid_vault_request"},
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}, {errPromptCanceled, "canceled"},
	} {
		if errors.Is(err, entry.err) {
			return entry.code
		}
	}
	return "vault_operation_incomplete"
}
