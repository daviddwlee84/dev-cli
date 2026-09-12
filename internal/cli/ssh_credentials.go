package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func (a *App) sshCredentials() sshcredential.Manager {
	if a.sshCredentialManager != nil {
		return *a.sshCredentialManager
	}
	return sshcredential.Manager{Store: sshcredential.NewStore(""), Providers: map[string]sshcredential.Provider{"system": sshcredential.SystemProvider(), "bitwarden": sshcredential.Bitwarden{}}}
}
func validateSSHPasswordStore(provider string) error {
	if provider != "" && provider != "system" && provider != "bitwarden" {
		return errors.New("--password-store must be system or bitwarden")
	}
	return nil
}

// The source revision and authentication key are deliberately excluded from
// identity. A route/account change produces a different credential context.
func sshPasswordContexts(root string, route sshhost.Route, registry machineregistry.Snapshot) ([]sshcredential.Context, error) {
	var result []sshcredential.Context
	var prefix []sshcredential.Context
	for _, hop := range route.Hops {
		c := sshcredential.Context{Origin: "local:" + root, Profile: strings.ToLower(hop.Alias), Host: strings.ToLower(strings.TrimSuffix(hop.HostName, ".")), User: hop.User, Port: hop.Port, Kind: "password"}
		if imported, found := registry.LookupImport(hop.Alias); found {
			c.Origin, c.Profile = imported.OriginID, imported.ProfileID
		}
		if address, e := netip.ParseAddr(c.Host); e == nil {
			c.Host = address.Unmap().String()
		}
		prefix = append(prefix, c)
		data, _ := json.Marshal(prefix)
		digest := sha256.Sum256(data)
		c.Route = hex.EncodeToString(digest[:])
		if e := c.Validate(); e != nil {
			return nil, e
		}
		result = append(result, c)
	}
	return result, nil
}
func readSSHReusablePassword(app *App, label string) ([]byte, error) {
	if app.sshPasswordPrompt != nil {
		return app.sshPasswordPrompt(label)
	}
	input, ok := app.In.(fileDescriptor)
	if !ok || !term.IsTerminal(input.Fd()) {
		return nil, sshhost.ErrInteractionRequired
	}
	fmt.Fprintf(app.Err, "Reusable SSH login password for %s (hidden; not OTP or key passphrase): ", label)
	value, err := term.ReadPassword(input.Fd())
	fmt.Fprintln(app.Err)
	return value, err
}

// Only dev-owned password input can be saved. Native host-key, passphrase and
// keyboard-interactive prompts are never intercepted or inferred as passwords.
func prepareSSHAuthentication(ctx context.Context, app *App, s *sshhost.Service, alias, provider string, skipTarget bool, sessionOptions ...sshhost.ConnectionOptions) (*sshhost.AuthenticationOperation, error) {
	if provider == "" {
		provider = "system"
	}
	if err := validateSSHPasswordStore(provider); err != nil {
		return nil, err
	}
	op, err := s.PrepareAuthentication(ctx, alias)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = op.Close()
		}
	}()
	registry, err := app.machineStore().Read(ctx)
	if err != nil {
		return nil, err
	}
	contexts, err := sshPasswordContexts(s.Paths().RootConfig, op.Route, registry)
	if err != nil {
		return nil, err
	}
	manager := app.sshCredentials()
	for index, hop := range op.Route.Hops {
		if skipTarget && hop.Target {
			break
		}
		probe, e := op.ProbeHop(ctx, index)
		if e != nil {
			return nil, e
		}
		if probe.Ready {
			continue
		}
		if !probe.HostTrusted {
			if !app.interactive() {
				break
			}
			probe, e = op.TrustHop(ctx, index)
			if e != nil {
				return nil, e
			}
		}
		if !probe.HostTrusted || !probe.AuthenticationRequired || !probe.PasswordAvailable {
			break
		}
		if len(sessionOptions) > 0 {
			if e := op.ValidateSession(sessionOptions[0]); e != nil {
				return nil, e
			}
		}
		c := contexts[index]
		lookupCtx, lookupCancel := context.WithTimeout(ctx, 30*time.Second)
		secret, e := manager.Get(lookupCtx, c)
		lookupCancel()
		saved := e == nil && len(secret) > 0
		if e != nil && !errors.Is(e, sshcredential.ErrNotFound) {
			app.warnf("SSH credential lookup unavailable for %s; native login remains available", hop.Alias)
		}
		if !saved {
			if !app.interactive() {
				break
			}
			secret, e = readSSHReusablePassword(app, hop.User+"@"+hop.HostName)
			if e != nil {
				sshcredential.Wipe(secret)
				return nil, e
			}
		}
		proof, e := op.ProvePassword(ctx, index, secret)
		if e != nil || !proof.Verified {
			sshcredential.Wipe(secret)
			return nil, errors.New("reusable SSH password was not verified; no password was saved")
		}
		if !saved {
			offerSSHPasswordSave(ctx, app, manager, c, secret, provider)
		}
		sshcredential.Wipe(secret)
	}
	keep = true
	return op, nil
}

func offerSSHPasswordSave(ctx context.Context, app *App, manager sshcredential.Manager, c sshcredential.Context, secret []byte, provider string) {
	if !app.interactive() {
		return
	}
	if manager.Store == nil {
		app.warnf("SSH login succeeded; credential storage is unavailable")
		return
	}
	record, _, err := manager.Store.Lookup(ctx, c)
	if err != nil {
		app.warnf("SSH login succeeded; credential preference could not be read")
		return
	}
	if record.Policy == sshcredential.PolicyNever {
		return
	}
	selected, err := sshPick(ctx, app, "Save verified reusable password for "+c.User+"@"+c.Host+"?", []picker.Item{{Value: "no", Label: "No", Description: "Keep only for this operation"}, {Value: "yes", Label: "Yes", Description: "Save in " + provider}, {Value: "never", Label: "Never", Description: "Do not offer saving for this account and route"}}, false)
	if err != nil {
		app.warnf("SSH login succeeded; password was not saved")
		return
	}
	saveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := manager.Save(saveCtx, c, secret, sshcredential.SaveDecision(selected[0].Value), provider)
	if err != nil {
		app.warnf("SSH login succeeded; credential save %s (review native provider before retrying)", result.Status)
		return
	}
	if result.Status == "saved" {
		fmt.Fprintf(app.Out, "Saved SSH login password in %s.\n", provider)
	}
}
