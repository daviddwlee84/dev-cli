package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

func (o sshSetupOptions) hasExistingKey() bool { return o.key != "" || o.keyCandidate != nil }

type sshKeyListDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Alias         string `json:"alias,omitempty"`
	sshhost.KeyCatalog
	ErrorCode string `json:"error_code,omitempty"`
}

func newSSHKeyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Inspect SSH keys and agents, or create a new vault key (install with dev ssh setup)", Args: cobra.NoArgs}
	var jsonOut, noAgent bool
	var alias, on string
	var agentValues []string
	list := &cobra.Command{
		Use: "list", Short: "List local public keys and SSH agent identities",
		Long: `List public-key metadata under ~/.ssh and identities from the current SSH agent.
Keys are deduplicated by fingerprint. No private key contents are read or printed.
For local listings, only --alias evaluates ssh -G and the alias's configured agent.
Local listing never repairs permissions, generates keys, or authenticates remotely.
--on fleet:HOST explicitly contacts that source for metadata from its own keys and agent.
--agent also lists a named agent (bitwarden, 1password, secretive) or an absolute socket.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if noAgent && len(agentValues) > 0 {
				return asUsageError(errors.New("--no-agent cannot be combined with --agent"))
			}
			if on != "" && len(agentValues) > 0 {
				return asUsageError(errors.New("--agent lists local agents and cannot be combined with --on"))
			}
			if on != "" {
				return runSSHRemoteKeys(cmd.Context(), app, on, alias, noAgent, jsonOut)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), sshShowTimeout)
			defer cancel()
			service, err := app.sshHosts()
			document := sshKeyListDocument{SchemaVersion: sshCLISchemaVersion, Kind: "ssh_key_list", Status: "ready", Alias: alias}
			if err == nil {
				var agents []sshhost.AgentSocketRef
				if agents, err = resolveSSHAgentRefs(service, agentValues); err == nil {
					document.KeyCatalog, err = service.Catalog(ctx, sshhost.KeyCatalogRequest{LocalOnly: alias == "", Alias: alias, NoAgent: noAgent, Agents: agents})
				}
			}
			if err != nil {
				document.Status = "failed"
				document.ErrorCode = sshErrorCode(err)
			} else if !document.Complete {
				document.Status = "partial"
			}
			if jsonOut {
				if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
					return encodeErr
				}
			} else if service != nil {
				renderSSHKeyCatalog(app, service.Paths().Home, document.KeyCatalog)
			}
			return err
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "Print key metadata and source diagnostics as JSON")
	list.Flags().BoolVar(&noAgent, "no-agent", false, "Skip SSH agent enumeration")
	list.Flags().StringVar(&alias, "alias", "", "Evaluate this alias's OpenSSH identity and agent settings")
	list.Flags().StringVar(&on, "on", "", "Run key inventory on the selected fleet:HOST using its local key context")
	list.Flags().StringArrayVar(&agentValues, "agent", nil, "Also list a named SSH agent: bitwarden, 1password, secretive or an absolute socket path (repeatable)")
	registerFlagCompletion(list, "agent", fixedCompletions(string(sshhost.AgentProviderBitwarden), string(sshhost.AgentProvider1Password), string(sshhost.AgentProviderSecretive)))
	cmd.AddCommand(list)
	cmd.AddCommand(newSSHKeyDoctorCmd(app))
	cmd.AddCommand(newSSHKeyDeriveCmd(app))
	cmd.AddCommand(newSSHKeyCreateCmd(app))
	return cmd
}

func renderSSHKeyCatalog(app *App, home string, catalog sshhost.KeyCatalog) {
	if len(catalog.Candidates) == 0 {
		if catalog.Complete {
			fmt.Fprintln(app.Out, "No local SSH keys found. Use a key path or generate a key in dev ssh setup.")
		} else {
			fmt.Fprintln(app.Out, "No key candidates available; some sources could not be inspected. See diagnostics below.")
		}
	} else {
		w := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "KEY\tALGORITHM\tSIGNER\tSOURCE\tFINGERPRINT\tCOMMENT")
		for _, key := range catalog.Candidates {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", sshKeyLabel(home, key), key.Algorithm, sshKeySigner(key), sshKeySources(key), key.Fingerprint, key.Comment)
		}
		_ = w.Flush()
	}
	renderSSHKeyDiagnostics(app, catalog.Diagnostics)
}

func renderSSHKeyDiagnostics(app *App, diagnostics []sshhost.Diagnostic) {
	for _, diagnostic := range diagnostics {
		message := diagnostic.Message
		if message == "" {
			message = strings.ReplaceAll(diagnostic.Code, "_", " ")
		}
		if diagnostic.Path != "" && !strings.Contains(message, diagnostic.Path) {
			message += " (" + diagnostic.Path + ")"
		}
		fmt.Fprintf(app.Err, "dev: SSH keys: %s\n", message)
	}
}

func sshKeyLabel(home string, key sshhost.KeyCandidate) string {
	if key.Agent != nil {
		return sshhost.AgentProviderLabel(key.Agent.Provider) + " agent"
	}
	path := key.IdentityFile
	if path == "" {
		path = key.PublicPath
	}
	if path == "" {
		return "SSH agent"
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

func sshKeySources(key sshhost.KeyCandidate) string {
	var sources []string
	for _, source := range key.Sources {
		sources = append(sources, string(source))
	}
	if len(sources) == 0 {
		sources = append(sources, string(key.Source))
	}
	return strings.Join(sources, "+")
}

func sshKeySigner(key sshhost.KeyCandidate) string {
	if key.NeedsPermissionRepair {
		return "permission repair needed"
	}
	var signers []string
	if key.Provenance.Private {
		signers = append(signers, "private file")
	}
	if key.Provenance.SecurityKeyStub {
		signers = append(signers, "security key")
	}
	if key.Provenance.Agent {
		signers = append(signers, "agent")
	}
	if len(signers) == 0 {
		return "public only; no signer found"
	}
	return strings.Join(signers, " + ")
}

func localSSHKeyCatalog(ctx context.Context, service *sshhost.Service, agents ...sshhost.AgentSocketRef) (sshhost.KeyCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, sshShowTimeout)
	defer cancel()
	return service.Catalog(ctx, sshhost.KeyCatalogRequest{LocalOnly: true, Agents: agents})
}

func resolveSSHAgentRefs(service *sshhost.Service, values []string) ([]sshhost.AgentSocketRef, error) {
	refs := make([]sshhost.AgentSocketRef, 0, len(values))
	for _, value := range values {
		ref, err := service.ResolveAgentSocket(runtime.GOOS, value)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// sshPickerAgents returns the explicit agent sockets a key picker lists: the
// flag-selected agent, or every provider whose socket is present. Installed
// providers without a socket get a hint on app.Err when app is non-nil.
func sshPickerAgents(app *App, service *sshhost.Service, options sshSetupOptions) []sshhost.AgentSocketRef {
	if options.fleetImportKeyPicker {
		return nil
	}
	if options.agentRef != nil {
		return []sshhost.AgentSocketRef{*options.agentRef}
	}
	var agents []sshhost.AgentSocketRef
	for _, observation := range service.ObserveAgentProviders(runtime.GOOS) {
		switch {
		case observation.Provider == sshhost.AgentProviderWindowsPipe:
		case observation.SocketPresent:
			agents = append(agents, sshhost.AgentSocketRef{Provider: observation.Provider, Socket: observation.Socket})
		case observation.AppDetected && app != nil:
			fmt.Fprintf(app.Err, "dev: %s is installed but its SSH agent socket was not found; %s to list its keys.\n", observation.Label, sshhost.AgentProviderEnableHint(observation.Provider))
		}
	}
	return agents
}

// selectSSHAgentKey finds key, a SHA256 fingerprint or a .pub path, in one agent.
func selectSSHAgentKey(ctx context.Context, service *sshhost.Service, ref sshhost.AgentSocketRef, key string) (sshhost.KeyCandidate, error) {
	fingerprint := key
	if !strings.HasPrefix(key, "SHA256:") {
		if !strings.HasSuffix(strings.ToLower(key), ".pub") {
			return sshhost.KeyCandidate{}, errors.New("--key with --identity-agent must be a SHA256 fingerprint or a .pub file")
		}
		plan, err := service.PlanKey(ctx, sshhost.KeyRequest{Operation: sshhost.KeyUse, Path: key})
		if err != nil {
			return sshhost.KeyCandidate{}, fmt.Errorf("read public key for --identity-agent: %w", err)
		}
		// Only public metadata selects the agent key. An unsafe private companion
		// does not grant private authority or prevent use of the separate agent.
		if plan.Fingerprint == "" {
			return sshhost.KeyCandidate{}, sshKeyPlanBlockedError(plan)
		}
		fingerprint = plan.Fingerprint
	}
	return service.SelectAgentKey(ctx, ref, fingerprint)
}

// sshAgentManualError explains the lines a user adds to a foreign alias that
// dev must not rewrite.
func sshAgentManualError(alias string, ref sshhost.AgentSocketRef, publicPath string) error {
	return fmt.Errorf("%s is a foreign SSH alias, so dev will not edit it. To use this %s agent key, save its public line to %s and add to that Host block:\n    IdentityAgent %q\n    IdentityFile %q\n    IdentitiesOnly yes\n%w",
		alias, sshhost.AgentProviderLabel(ref.Provider), publicPath, ref.Socket, publicPath, sshhost.ErrManualRemediation)
}

// chooseSSHKeyInteractive offers existing local keys, generation and a manual
// path in one picker, then prepares the selected key plan.
func chooseSSHKeyInteractive(ctx context.Context, app *App, service *sshhost.Service, alias string, options sshSetupOptions, generateDefault string) (sshSetupOptions, error) {
	agents := sshPickerAgents(app, service, options)
	for {
		catalog, err := localSSHKeyCatalog(ctx, service, agents...)
		if err != nil {
			return options, err
		}
		renderSSHKeyDiagnostics(app, catalog.Diagnostics)
		items := make([]picker.Item, 0, len(catalog.Candidates)+2)
		for _, key := range catalog.Candidates {
			if options.agentRef != nil && (key.Agent == nil || key.Agent.Socket != options.agentRef.Socket) {
				continue
			}
			items = append(items, picker.Item{Value: key.Fingerprint, Label: sshKeyLabel(service.Paths().Home, key), Description: strings.Join([]string{key.Comment, key.Algorithm, key.Fingerprint, sshKeySources(key), sshKeySigner(key)}, " · ")})
		}
		if options.agentRef == nil {
			for _, observation := range service.ObserveAgentProviders(runtime.GOOS) {
				if observation.Provider == sshhost.AgentProviderWindowsPipe || observation.SocketPresent && !options.fleetImportKeyPicker {
					continue
				}
				hint := sshhost.AgentProviderEnableHint(observation.Provider)
				if options.fleetImportKeyPicker {
					hint = errSSHFleetImportAgent.Error()
				}
				items = append(items, picker.Item{Value: "unavailable:" + string(observation.Provider), Label: observation.Label + " agent (unavailable)", Description: hint})
			}
			for _, choice := range sshVaultKeyChoices(service, options.fleetImportKeyPicker) {
				value := "vault:" + choice.VaultAction
				if choice.UnavailableReason != "" {
					value = "unavailable:" + value
				}
				items = append(items, picker.Item{Value: value, Label: choice.Label, Description: choice.Description})
			}
			hardwareChoices, err := sshSecurityKeyChoices(ctx, service, alias, options.fleetImportKeyPicker)
			if err != nil {
				return options, err
			}
			for i, choice := range hardwareChoices {
				value := "generate-sk"
				if choice.UnavailableReason != "" {
					value = fmt.Sprintf("unavailable:hardware-%d", i)
				}
				items = append(items, picker.Item{Value: value, Label: choice.Label, Description: choice.Description})
			}
			items = append(items,
				picker.Item{Value: "generate", Label: "+ Generate a new key", Description: "Ed25519 at " + generateDefault},
				picker.Item{Value: "manual", Label: "Enter a key path…", Description: "Use a custom path or a private key missing its .pub companion"})
		}
		if len(items) == 0 {
			return options, errors.New("the selected agent offers no keys; unlock the provider and make an SSH key available, then retry")
		}
		selected, err := sshPick(ctx, app, "SSH key for "+alias, items, false)
		if errors.Is(err, picker.ErrCanceled) {
			err = errPromptCanceled
		}
		if err != nil {
			return options, err
		}
		if strings.HasPrefix(selected[0].Value, "unavailable:") {
			fmt.Fprintln(app.Err, "dev: "+selected[0].Description)
			continue
		}
		options.key, options.keyCandidate, options.keyPlan, options.generateKey, options.keyPath = "", nil, nil, false, ""
		clearSSHGeneratedKeyOptions(&options)
		switch selected[0].Value {
		case "vault:1password", "vault:bitwarden-native", "vault:bitwarden-desktop":
			if options.fleetImportKeyPicker {
				return options, errors.New("new vault creation is not supported during fleet-source profile import; configure the local alias first")
			}
			request, err := sshVaultRequestForAction(strings.TrimPrefix(selected[0].Value, "vault:"), "SSH key for "+alias)
			if err != nil {
				return options, err
			}
			result, err := runSSHVaultCreation(ctx, app, sshVaultCreateOptions{request: request, forPicker: true})
			options.vaultReceipts = sshflow.RetainVaultKeyReceipt(options.vaultReceipts, result.Receipt)
			if err != nil {
				if errors.Is(err, errPromptCanceled) || errors.Is(err, picker.ErrCanceled) {
					continue
				}
				return options, err
			}
			key, err := pickSSHVaultAgentKey(ctx, app, result)
			if err != nil {
				if errors.Is(err, errPromptCanceled) || errors.Is(err, picker.ErrCanceled) {
					continue
				}
				return options, err
			}
			if key == nil {
				continue
			}
			candidate, err := service.SelectAgentKey(ctx, sshhost.AgentSocketRef{Provider: sshhost.AgentProviderID(key.Provider), Socket: key.Socket}, key.Fingerprint)
			if err != nil {
				return options, err
			}
			options.keyCandidate = &candidate
		case "generate", "generate-sk":
			options.generateKey = true
			defaultPath := generateDefault
			if selected[0].Value == "generate-sk" {
				defaultPath = filepath.Join(service.Paths().SSHDir, "id_ed25519_sk_dev_"+alias)
			}
			prompt := newPrompter(app)
			options.keyPath, err = prompt.line("New key path (a bare name is placed in ~/.ssh)", defaultPath)
			if err != nil {
				return options, err
			}
			options.keyPath = normalizeSSHKeyPromptPath(service.Paths().SSHDir, options.keyPath)
			options.comment, err = prompt.line("Key comment (blank = ssh-keygen default)", "")
			if err != nil {
				return options, err
			}
			options.comment = strings.TrimSpace(options.comment)
			if selected[0].Value == "generate-sk" {
				if err := promptSSHSecurityKeyOptions(prompt, &options); err != nil {
					return options, err
				}
			}
		case "manual":
			options.key, err = newPrompter(app).line("Key or public key path", filepath.Join(service.Paths().Home, ".ssh", "id_ed25519"))
			if err != nil {
				return options, err
			}
		default:
			for _, key := range catalog.Candidates {
				if key.Fingerprint == selected[0].Value {
					if key.Agent != nil {
						key, err = service.SelectAgentKey(ctx, *key.Agent, key.Fingerprint)
						if err != nil {
							return options, err
						}
					}
					options.keyCandidate = &key
					break
				}
			}
			if options.keyCandidate == nil {
				return options, errors.New("key picker returned an unknown fingerprint")
			}
		}
		if err := prepareSSHWizardKey(ctx, app, service, &options); err != nil {
			if errors.Is(err, errPromptCanceled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return options, err
			}
			fmt.Fprintf(app.Err, "dev: %v; select another key or enter its path.\n", err)
			continue
		}
		return options, nil
	}
}

// normalizeSSHKeyPromptPath places a bare prompted key name under the SSH
// directory; explicit paths and flags keep their exact meaning.
func normalizeSSHKeyPromptPath(sshDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\%$`) || strings.HasPrefix(value, "~") {
		return value
	}
	return filepath.Join(sshDir, value)
}

// sshKeyPlanBlockedError names the first blocking reason of a key plan.
func sshKeyPlanBlockedError(plan sshhost.KeyPlan) error {
	if len(plan.Diagnostics) > 0 {
		diagnostic := plan.Diagnostics[0]
		reason := diagnostic.Message
		if reason == "" {
			reason = strings.ReplaceAll(diagnostic.Code, "_", " ")
		}
		return fmt.Errorf("SSH key plan is blocked: %s: %w", reason, sshhost.ErrBlocked)
	}
	return fmt.Errorf("SSH key plan is blocked: %w", sshhost.ErrBlocked)
}

func prepareSSHWizardKey(ctx context.Context, app *App, service *sshhost.Service, options *sshSetupOptions) error {
	if err := validateSSHGenerationOptions(*options); err != nil {
		return err
	}
	if err := validateSSHAgentOptions(*options); err != nil {
		return err
	}
	if options.keyPlan != nil {
		return nil
	}
	path := options.key
	if options.generateKey {
		path = options.keyPath
	} else if options.keyCandidate != nil {
		path = options.keyCandidate.IdentityFile
		if path == "" {
			path = options.keyCandidate.PublicPath
		}
	}
	if path != "" {
		if err := preflightSSHWizardPermissions(ctx, app, service, path); err != nil {
			return err
		}
	}
	if candidate := options.keyCandidate; candidate != nil {
		if candidate.NeedsPermissionRepair {
			catalog, err := localSSHKeyCatalog(ctx, service)
			if err != nil {
				return err
			}
			options.keyCandidate = nil
			for _, key := range catalog.Candidates {
				if key.Fingerprint == candidate.Fingerprint && key.IdentityFile == candidate.IdentityFile && key.PublicPath == candidate.PublicPath {
					options.keyCandidate = &key
					break
				}
			}
			if options.keyCandidate == nil {
				return errors.New("selected key changed during permission repair")
			}
			candidate = options.keyCandidate
		}
		if !candidate.Provenance.Private && !candidate.Provenance.SecurityKeyStub && !candidate.Provenance.Agent {
			return errors.New("only the public key was found; load its signer into the SSH agent or choose a private key")
		}
	}
	plan, err := planSSHSetupKey(ctx, app, service, *options, true)
	if err != nil {
		return err
	}
	if !plan.Ready() {
		renderSSHKeyDiagnostics(app, plan.Diagnostics)
		return errors.New("selected key is not ready")
	}
	options.keyPlan = &plan
	return nil
}
