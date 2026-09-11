package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/dotfile"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

// LoadHerdr observes the controller's native client catalog once for the whole
// table. It never starts a server, resolves SSH options, or contacts a target.
func (b *tuiFleetBackend) LoadHerdr(ctx context.Context) (tui.FleetHerdrCatalog, error) {
	catalog := tui.FleetHerdrCatalog{Status: "unavailable", Profiles: []tui.FleetHerdrProfile{}, InHerdr: os.Getenv("HERDR_ENV") == "1"}
	if err := ctx.Err(); err != nil {
		return catalog, err
	}
	if b.current().noRuntime {
		catalog.Status, catalog.Detail = "disabled", "The dashboard was started with --no-runtime"
		catalog.ObservedAt = time.Now().UTC()
		return catalog, nil
	}
	runner := b.current().sshHostRunner
	if runner == nil {
		runner = sshhost.ExecRunner{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	help, helpErr := runner.Run(probeCtx, sshhost.RunRequest{Name: "herdr", Args: []string{"--help"}, Display: "Herdr command capabilities"})
	cancel()
	catalog.RemoteAvailable = helpErr == nil && help.ExitCode == 0 && !help.StdoutTruncated && strings.Contains(string(help.Stdout), "--remote") && strings.Contains(string(help.Stdout), "--session")
	if err := ctx.Err(); err != nil {
		return catalog, err
	}
	inventory, err := (herdrremote.Service{Runner: runner}).List(ctx)
	catalog.Status, catalog.ObservedAt = inventory.Status, time.Now().UTC()
	if err != nil {
		catalog.Detail = err.Error()
		return catalog, err
	}
	for _, profile := range inventory.Profiles {
		catalog.Profiles = append(catalog.Profiles, tui.FleetHerdrProfile{ID: profile.ID, Label: profile.Label, Target: profile.Target, Session: profile.Session, Enabled: profile.Enabled})
	}
	return catalog, nil
}

// ListActions joins a shared, fresh catalog without another native read. The UI
// groups profile actions by ProfileID before offering one profile's operations.
func (b *tuiFleetBackend) ListActions(ctx context.Context, descriptor tui.FleetHostDescriptor, catalog tui.FleetHerdrCatalog) ([]tui.FleetHostAction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actions := []tui.FleetHostAction{{ID: "dotfile-status", Label: "Dotfile status", Description: "Read this host's native configuration and source revision"}}
	if descriptor.Local {
		if descriptor.Key != localFleetDescriptor().Key {
			return nil, errors.New("local host selection changed")
		}
		return append(actions, tui.FleetHostAction{ID: "local-info", Label: descriptor.Name + " · " + goruntime.GOOS + "/" + goruntime.GOARCH, Description: "This machine · dev " + versionFromBuild(), Disabled: true}), nil
	}
	host, err := b.host(descriptor)
	if err != nil {
		return nil, err
	}
	actions = append([]tui.FleetHostAction{
		{ID: "ssh", Label: "Open SSH shell", Description: "Connect to the configured host; remote dev is not required"},
		{ID: "authenticated-refresh", Label: "Refresh with authentication", Description: "Refresh only this host in a terminal; configured credentials may be requested"},
	}, actions...)
	if _, err := exec.LookPath("ssh"); err != nil {
		actions[0].Disabled, actions[0].Description = true, "ssh is not installed"
	}
	if b.current().noRuntime || catalog.Status == "disabled" {
		return append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr disabled", Description: "The dashboard was started with --no-runtime", Disabled: true}), nil
	}
	var profiles []herdrremote.Profile
	if catalog.Status == "ready" && host.SSHAlias != "" {
		for _, p := range catalog.Profiles {
			if p.Target == host.SSHAlias {
				profiles = append(profiles, herdrremote.Profile{ID: p.ID, Label: p.Label, Target: p.Target, Session: p.Session, Enabled: p.Enabled})
			}
		}
	}
	reason := fleetHerdrTargetReason(host)
	if catalog.Status == "ready" {
		if len(profiles) == 0 && reason == "" {
			actions = append(actions, tui.FleetHostAction{ID: "herdr-add", Label: "Add to Herdr", Description: "Prepare session default and save on this machine; remote setup approvals remain native"})
		}
		for _, profile := range profiles {
			if profile.Enabled {
				actions = append(actions, fleetHerdrProfileOption("herdr-disable", "Disable in Herdr", profile, "Detach this machine from local clients; remote sessions keep running"))
			} else {
				enable := fleetHerdrProfileOption("herdr-enable", "Enable in Herdr", profile, "Open local clients reconnect through SSH")
				if reason != "" {
					enable.Disabled, enable.Description = true, reason
				}
				actions = append(actions, enable)
			}
			actions = append(actions, fleetHerdrProfileOption("herdr-remove", "Remove from Herdr", profile, "Remove this saved profile and detach local clients; remote sessions keep running"))
		}
	} else {
		detail := catalog.Detail
		if detail == "" {
			detail = "Requires a compatible native machine CLI; no authoritative catalog is available"
		}
		actions = append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr saved machines unavailable", Description: detail, Disabled: true})
	}
	if !catalog.InHerdr && catalog.RemoteAvailable && reason == "" {
		if len(profiles) == 0 {
			actions = append(actions, tui.FleetHostAction{ID: "herdr-connect", Label: "Connect with Herdr", Description: "Attach through SSH to session default; native setup approvals remain interactive"})
		} else {
			for _, profile := range profiles {
				actions = append(actions, fleetHerdrProfileOption("herdr-connect", "Connect with Herdr", profile, "Attach through SSH to this explicit session"))
			}
		}
	}
	if reason != "" {
		actions = append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr connection unavailable", Description: reason, Disabled: true})
	} else if !catalog.InHerdr && !catalog.RemoteAvailable {
		actions = append(actions, tui.FleetHostAction{ID: "herdr-connect-unavailable", Label: "Herdr connection unavailable", Description: "Install a Herdr binary with native --remote support", Disabled: true})
	}
	return actions, nil
}

func fleetHerdrProfileOption(verb, label string, profile herdrremote.Profile, description string) tui.FleetHostAction {
	return tui.FleetHostAction{ID: tuiFleetProfileAction(verb, profile), Label: label, Description: description, ProfileID: profile.ID, ProfileLabel: profile.Label, ProfileSession: profile.Session}
}

func tuiFleetProfileAction(verb string, profile herdrremote.Profile) string {
	profile.Selected = false // Client focus is not saved-machine authority.
	data, _ := json.Marshal(profile)
	return fmt.Sprintf("%s:%s:%x", verb, profile.ID, sha256.Sum256(data))
}

func fleetHerdrTargetReason(host fleet.Host) string {
	if host.EffectiveRemoteOS() == fleet.RemoteOSWindows {
		return "Herdr remote servers require Linux or macOS"
	}
	if host.SSHAlias == "" || host.User != "" || host.Port != 0 || host.IdentityFile != "" {
		return "Use an SSH alias containing all connection settings for Herdr"
	}
	if host.PasswordKind() != "none" {
		return "Herdr uses OpenSSH authentication; fleet password sources are not forwarded"
	}
	return ""
}

// The process payload contains only identities and selected intent. The child
// reloads the configuration before connecting, so a handoff cannot silently
// follow an edited host name to a different endpoint.
type tuiFleetHostRequest struct {
	Host       string `json:"host"`
	EndpointID string `json:"endpoint_id,omitempty"`
	Local      bool   `json:"local,omitempty"`
	Action     string `json:"action"`
}

func tuiFleetHostActionProcess(ctx context.Context, app *App, descriptor tui.FleetHostDescriptor, action string) (*exec.Cmd, error) {
	b := newTUIFleetBackend(func() *App { return app })
	if descriptor.Local {
		if descriptor.Key != localFleetDescriptor().Key || action != "dotfile-status" {
			return nil, errors.New("invalid local host action")
		}
	} else {
		host, err := b.host(descriptor)
		if err != nil {
			return nil, err
		}
		if err := validateTUIFleetHostAction(app, host, action); err != nil {
			return nil, err
		}
	}
	payload, err := json.Marshal(tuiFleetHostRequest{Host: descriptor.Name, EndpointID: descriptor.EndpointID, Local: descriptor.Local, Action: action})
	if err != nil {
		return nil, err
	}
	return tuiFleetProcess(ctx, app, "tui", "_fleet-host", "--request", base64.RawURLEncoding.EncodeToString(payload))
}

func validateTUIFleetHostAction(app *App, host fleet.Host, action string) error {
	switch action {
	case "ssh", "authenticated-refresh", "dotfile-status":
		return nil
	}
	if app.noRuntime {
		return errors.New("Herdr is disabled by --no-runtime")
	}
	verb := action
	if action != "herdr-add" && action != "herdr-connect" {
		var err error
		verb, _, err = parseTUIFleetProfileAction(action)
		if err != nil {
			return err
		}
	}
	if host.SSHAlias == "" {
		return errors.New("Herdr profile matching requires an exact SSH alias")
	}
	if verb != "herdr-disable" && verb != "herdr-remove" {
		if reason := fleetHerdrTargetReason(host); reason != "" {
			return errors.New(reason)
		}
	}
	if verb == "herdr-connect" && os.Getenv("HERDR_ENV") == "1" {
		return errors.New("select the saved machine in Herdr's sidebar; an embedded client is not opened")
	}
	return nil
}

func parseTUIFleetProfileAction(action string) (string, string, error) {
	parts := strings.Split(action, ":")
	if len(parts) != 3 || len(parts[1]) != 32 || len(parts[2]) != 64 {
		return "", "", errors.New("invalid Herdr profile action")
	}
	switch parts[0] {
	case "herdr-connect", "herdr-enable", "herdr-disable", "herdr-remove":
	default:
		return "", "", errors.New("unknown Herdr profile action")
	}
	if _, err := hex.DecodeString(parts[1] + parts[2]); err != nil {
		return "", "", errors.New("invalid Herdr profile identity")
	}
	return parts[0], parts[1], nil
}

func tuiFleetProcess(ctx context.Context, app *App, args ...string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	global := []string{}
	if app.configPath != "" {
		global = append(global, "--config", app.configPath)
	}
	if app.remotesPath != "" {
		global = append(global, "--remotes", app.remotesPath)
	}
	if app.noRuntime {
		global = append(global, "--no-runtime")
	}
	return exec.CommandContext(ctx, executable, append(global, args...)...), nil
}

func newTUIFleetHostCmd(app *App) *cobra.Command {
	var encoded string
	cmd := &cobra.Command{Use: "_fleet-host", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(encoded) > 8192 {
				return errors.New("invalid fleet host action")
			}
			data, err := base64.RawURLEncoding.DecodeString(encoded)
			var request tuiFleetHostRequest
			if err != nil || json.Unmarshal(data, &request) != nil {
				return errors.New("invalid fleet host action")
			}
			b := newTUIFleetBackend(func() *App { return app })
			descriptor := tui.FleetHostDescriptor{Key: "remote:" + request.EndpointID, Name: request.Host, EndpointID: request.EndpointID, Local: request.Local}
			if request.Local {
				descriptor.Key = "local:" + request.Host
				if descriptor.Key != localFleetDescriptor().Key || request.Action != "dotfile-status" {
					return errors.New("invalid local host action")
				}
			}
			var host fleet.Host
			if !request.Local {
				host, err = b.host(descriptor)
				if err != nil {
					return err
				}
			}
			err = runTUIFleetHostAction(cmd.Context(), app, b, descriptor, host, request.Action)
			// Read-only reports and native registration messages must stay visible
			// before Bubble Tea re-enters its alternate screen.
			if request.Action != "ssh" && !strings.HasPrefix(request.Action, "herdr-connect") && app.interactive() {
				if err != nil {
					fmt.Fprintln(app.Err, err)
				}
				_, _ = newPrompter(app).readLine("Press Enter to return to FLEET: ")
			}
			return err
		}}
	cmd.Flags().StringVar(&encoded, "request", "", "exact host action")
	return cmd
}

func runTUIFleetHostAction(ctx context.Context, app *App, b *tuiFleetBackend, descriptor tui.FleetHostDescriptor, host fleet.Host, action string) error {
	switch action {
	case "ssh":
		return runTUIFleetSSH(ctx, app, host)
	case "authenticated-refresh":
		if !app.interactive() {
			return errors.New("authenticated refresh requires an interactive terminal")
		}
		b.run = func(ctx context.Context, host fleet.Host, args []string, options fleet.RunOptions) fleet.Result {
			return (fleet.Transport{Err: app.Err}).RunWithOptions(ctx, host, args, nil, options)
		}
		result, err := b.loadHost(ctx, descriptor, fleet.RetryAuthentication)
		if err != nil {
			return err
		}
		renderFleetList(app, []fleet.HostResult{result}, "")
		if result.StrictFailure() {
			return fmt.Errorf("host %s: %s", result.Name, result.Error)
		}
		return nil
	case "dotfile-status":
		if descriptor.Local {
			return renderDotfileStatus(app.Out, dotfile.Observe(ctx, dotfile.Options{}), false)
		}
		// The outer handoff already verified this exact host. Keep that immutable
		// endpoint through the final transport instead of resolving its name in
		// another dev process, where a concurrent config edit could retarget it.
		run := b.run
		if run == nil {
			run = func(ctx context.Context, host fleet.Host, args []string, options fleet.RunOptions) fleet.Result {
				return (fleet.Transport{Err: app.Err, StdoutLimit: dotfile.MaxStatusBytes}).RunWithOptions(ctx, host, args, nil, options)
			}
		}
		result := collectFleetDotfileHost(ctx, host, dotfileFleetRunFunc(run))
		return renderFleetDotfileResults(app, []fleetDotfileResult{result}, false)
	}
	if err := validateTUIFleetHostAction(app, host, action); err != nil {
		return err
	}
	if action == "herdr-add" {
		manage := newSSHManageCmd(app)
		manage.SetContext(ctx)
		manage.SetArgs([]string{"--action", "register", "--to", "herdr", "--alias", host.SSHAlias, "--herdr-label", host.Name, "--herdr-session", "default", "--apply"})
		return manage.Execute()
	}
	session := "default"
	if action != "herdr-connect" {
		verb, id, err := parseTUIFleetProfileAction(action)
		if err != nil {
			return err
		}
		service := herdrremote.Service{Runner: app.sshHostRunner}
		inv, err := service.List(ctx)
		if err != nil {
			return err
		}
		found := false
		var selected herdrremote.Profile
		for _, profile := range inv.Profiles {
			if profile.ID == id && profile.Target == host.SSHAlias && action == tuiFleetProfileAction(verb, profile) {
				session, found = profile.Session, true
				selected = profile
			}
		}
		if !found {
			return errors.New("selected Herdr profile changed; reopen host actions")
		}
		if verb != "herdr-connect" {
			plan, err := service.Plan(inv, herdrremote.Request{Action: strings.TrimPrefix(verb, "herdr-"), ProfileID: id})
			if err != nil {
				return err
			}
			return applyTUIFleetHerdrPlan(ctx, app, b, descriptor, selected, service, plan)
		}
	}
	if os.Getenv("HERDR_ENV") == "1" {
		return errors.New("select the saved machine in Herdr's sidebar; an embedded client is not opened")
	}
	process := exec.CommandContext(ctx, "herdr", "--remote", host.SSHAlias, "--session", session)
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	return process.Run()
}

func applyTUIFleetHerdrPlan(ctx context.Context, app *App, backend *tuiFleetBackend, descriptor tui.FleetHostDescriptor, profile herdrremote.Profile, service herdrremote.Service, plan herdrremote.Plan) error {
	fmt.Fprintf(app.Out, "Herdr profile: %s\nSSH target: %s\nSession: %s\nProfile ID: %s\nAction: %s\n", profile.Label, profile.Target, profile.Session, profile.ID, plan.Request.Action)
	for _, effect := range plan.Effects {
		fmt.Fprintln(app.Out, "  "+effect)
	}
	if !app.interactive() {
		return errors.New("Herdr profile changes require confirmation in an interactive terminal")
	}
	confirmed, err := newPrompter(app).confirm("Apply this Herdr profile change?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errPromptCanceled
	}
	if _, err := backend.host(descriptor); err != nil {
		return err
	}
	result, err := service.Apply(ctx, plan, true)
	fmt.Fprintf(app.Out, "Herdr %s: %s\n", plan.Request.Action, result.Status)
	return err
}

func runTUIFleetSSH(ctx context.Context, app *App, host fleet.Host) error {
	args := []string{"-t"}
	if host.ConnectTimeout.Duration > 0 {
		seconds := int(host.ConnectTimeout.Duration.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		args = append(args, "-o", "ConnectTimeout="+strconv.Itoa(seconds))
	}
	if host.SSHAlias != "" && host.User != "" {
		args = append(args, "-l", host.User)
	}
	if host.Port > 0 {
		args = append(args, "-p", strconv.Itoa(host.Port))
	}
	if host.IdentityFile != "" {
		args = append(args, "-i", config.Expand(host.IdentityFile))
	}
	args = append(args, "--", host.Destination())
	process := exec.CommandContext(ctx, "ssh", args...)
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	return process.Run()
}
