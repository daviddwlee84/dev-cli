package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
	"github.com/spf13/cobra"
)

func sshFleetOn(value string) (string, error) {
	if !strings.HasPrefix(value, "fleet:") {
		return "", errors.New("--on requires fleet:HOST")
	}
	name, err := url.PathUnescape(strings.TrimPrefix(value, "fleet:"))
	if err != nil || strings.TrimSpace(name) == "" {
		return "", errors.New("--on requires an encoded nonempty fleet host name")
	}
	return name, nil
}

func runSSHRemoteKeys(ctx context.Context, app *App, on, alias string, noAgent, jsonOut bool) error {
	name, err := sshFleetOn(on)
	if err != nil {
		return asUsageError(err)
	}
	host, err := sshFleetHost(app, name)
	if err != nil {
		return err
	}
	keys, err := app.sshRemotes().Keys(ctx, host, alias, noAgent)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeSSHJSON(app, keys)
	}
	fmt.Fprintf(app.Out, "SSH keys on %s (remote paths; no key transfer):\n", name)
	renderSSHKeyCatalog(app, "", keys.Catalog)
	return nil
}

func newSSHConnectCmd(app *App) *cobra.Command {
	var on, keyID, passwordStore string
	cmd := &cobra.Command{Use: "connect ALIAS", Short: "Open a local or explicitly remote-native SSH session", Long: `Connect using an exact SSH alias. --on fleet:HOST runs that host's native SSH
client with its own configuration and keys. --key-id selects one fingerprint in
the executing host's key catalog; no private key is transferred. This command
opens an interactive session only, preserves child exit status, disables agent
forwarding, and never retries a started session.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !app.interactive() {
			return asUsageError(errors.New("ssh connect requires an interactive terminal"))
		}
		return runSSHConnectWithStore(cmd.Context(), app, args[0], on, keyID, passwordStore)
	}}
	cmd.Flags().StringVar(&passwordStore, "password-store", "system", "provider offered after verified reusable password login: system or bitwarden")
	cmd.Flags().StringVar(&on, "on", "", "Execute using the selected fleet:HOST's own SSH client and keys")
	cmd.Flags().StringVar(&keyID, "key-id", "", "Exact SHA256 fingerprint from the executing host's key list")
	return cmd
}

func runSSHConnect(ctx context.Context, app *App, alias, on, keyID string) error {
	return runSSHConnectWithStore(ctx, app, alias, on, keyID, "system")
}
func runSSHConnectWithStore(ctx context.Context, app *App, alias, on, keyID, passwordStore string) error {
	if err := validateSSHPasswordStore(passwordStore); err != nil {
		return asUsageError(err)
	}
	if on != "" {
		name, err := sshFleetOn(on)
		if err != nil {
			return asUsageError(err)
		}
		host, err := sshFleetHost(app, name)
		if err != nil {
			return err
		}
		client := app.sshRemotes()
		inv, err := client.Inventory(ctx, host)
		if err != nil {
			return err
		}
		profile, found := inv.Find(alias)
		if !found {
			return sshremote.ErrNotFound
		}
		fmt.Fprintf(app.Out, "Connect on %s using remote alias %s (keys stay on %s).\n", name, profile.Alias, name)
		return client.Connect(ctx, host, profile.Selection(inv.Origin), keyID, false)
	}
	s, err := app.sshHosts()
	if err != nil {
		return err
	}
	var candidate *sshhost.KeyCandidate
	if keyID != "" {
		catalog, err := s.Catalog(ctx, sshhost.KeyCatalogRequest{Alias: alias})
		if err != nil {
			return err
		}
		for _, key := range catalog.Candidates {
			if key.Fingerprint == keyID {
				candidate = &key
				break
			}
		}
		if candidate == nil {
			return errors.New("selected key fingerprint is not available in this alias's local context")
		}
	}
	sessionOptions := sshhost.ConnectionOptions{Interactive: true, ForwardAgentNo: true}
	connection, err := prepareSSHAuthentication(ctx, app, s, alias, passwordStore, candidate != nil, sessionOptions)
	if errors.Is(err, sshhost.ErrUnsupportedRoute) {
		app.warnf("using native SSH for this profile; its settings do not support managed password saving")
		native, e := s.PrepareConnection(ctx, alias, candidate)
		if e != nil {
			return e
		}
		defer native.Close()
		result, e := native.Run(ctx, sessionOptions)
		if e != nil {
			return e
		}
		if result.ExitCode != 0 {
			return sshremote.ExitError{Code: result.ExitCode}
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer connection.Close()
	var result sshhost.RunResult
	if !connection.UsesPassword() {
		_ = connection.Close()
		native, e := s.PrepareConnection(ctx, alias, candidate)
		if e != nil {
			return e
		}
		defer native.Close()
		result, err = native.Run(ctx, sessionOptions)
	} else if candidate != nil {
		result, err = connection.RunSelected(ctx, candidate, sessionOptions)
	} else {
		result, err = connection.Run(ctx, sessionOptions)
	}
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return sshremote.ExitError{Code: result.ExitCode}
	}
	return nil
}
