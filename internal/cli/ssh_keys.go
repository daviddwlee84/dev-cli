package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/daviddwlee84/dev-cli/internal/picker"
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
	cmd := &cobra.Command{Use: "key", Short: "Inspect local SSH keys and agent identities", Args: cobra.NoArgs}
	var jsonOut, noAgent bool
	var alias string
	list := &cobra.Command{
		Use: "list", Short: "List local public keys and SSH agent identities",
		Long: `List public-key metadata under ~/.ssh and identities from the current SSH agent.
Keys are deduplicated by fingerprint. No private key contents are read or printed.
Only --alias evaluates OpenSSH configuration (ssh -G) and its configured agent.
Listing never repairs permissions, generates keys, or authenticates remotely.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), sshShowTimeout)
			defer cancel()
			service, err := app.sshHosts()
			document := sshKeyListDocument{SchemaVersion: sshCLISchemaVersion, Kind: "ssh_key_list", Status: "ready", Alias: alias}
			if err == nil {
				document.KeyCatalog, err = service.Catalog(ctx, sshhost.KeyCatalogRequest{LocalOnly: alias == "", Alias: alias, NoAgent: noAgent})
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
	cmd.AddCommand(list)
	cmd.AddCommand(newSSHKeyDoctorCmd(app))
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

func localSSHKeyCatalog(ctx context.Context, service *sshhost.Service) (sshhost.KeyCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, sshShowTimeout)
	defer cancel()
	return service.Catalog(ctx, sshhost.KeyCatalogRequest{LocalOnly: true})
}

func selectSSHWizardKey(ctx context.Context, app *App, service *sshhost.Service, alias string, options sshSetupOptions) (sshSetupOptions, error) {
	for {
		catalog, err := localSSHKeyCatalog(ctx, service)
		if err != nil {
			return options, err
		}
		renderSSHKeyDiagnostics(app, catalog.Diagnostics)
		items := make([]picker.Item, 0, len(catalog.Candidates)+1)
		for _, key := range catalog.Candidates {
			items = append(items, picker.Item{Value: key.Fingerprint, Label: sshKeyLabel(service.Paths().Home, key), Description: strings.Join([]string{key.Comment, key.Algorithm, key.Fingerprint, sshKeySources(key), sshKeySigner(key)}, " · ")})
		}
		items = append(items, picker.Item{Value: "manual", Label: "Enter a key path…", Description: "Use a custom path or a private key missing its .pub companion"})
		selected, err := sshPick(ctx, app, "SSH key for "+alias, items, false)
		if errors.Is(err, picker.ErrCanceled) {
			err = errPromptCanceled
		}
		if err != nil {
			return options, err
		}
		options.key, options.keyCandidate, options.keyPlan = "", nil, nil
		if selected[0].Value == "manual" {
			options.key, err = newPrompter(app).line("Key or public key path", filepath.Join(service.Paths().Home, ".ssh", "id_ed25519"))
			if err != nil {
				return options, err
			}
		} else {
			for _, key := range catalog.Candidates {
				if key.Fingerprint == selected[0].Value {
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

func prepareSSHWizardKey(ctx context.Context, app *App, service *sshhost.Service, options *sshSetupOptions) error {
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
