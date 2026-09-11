package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

func (b *tuiFleetBackend) ListActions(ctx context.Context, descriptor tui.FleetHostDescriptor) ([]tui.FleetHostAction, error) {
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
		actions[0].Disabled = true
		actions[0].Description = "ssh is not installed"
	}
	if b.current().noRuntime {
		return append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr disabled", Description: "The dashboard was started with --no-runtime", Disabled: true}), nil
	}
	if reason := fleetHerdrTargetReason(host); reason != "" {
		return append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr unavailable", Description: reason, Disabled: true}), nil
	}
	runner := b.current().sshHostRunner
	if runner == nil {
		runner = sshhost.ExecRunner{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	help, helpErr := runner.Run(probeCtx, sshhost.RunRequest{Name: "herdr", Args: []string{"--help"}, Display: "Herdr command capabilities"})
	cancel()
	remoteAvailable := helpErr == nil && help.ExitCode == 0 && !help.StdoutTruncated && strings.Contains(string(help.Stdout), "--remote") && strings.Contains(string(help.Stdout), "--session")
	if !remoteAvailable {
		return append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr unavailable", Description: "Install a Herdr binary with native --remote support", Disabled: true}), nil
	}
	inv, listErr := (herdrremote.Service{Runner: runner}).List(ctx)
	var profiles []herdrremote.Profile
	if listErr == nil {
		for _, profile := range inv.Profiles {
			if profile.Target == host.SSHAlias {
				profiles = append(profiles, profile)
			}
		}
	}
	if os.Getenv("HERDR_ENV") != "1" {
		if len(profiles) == 0 {
			actions = append(actions, tui.FleetHostAction{ID: "herdr-connect", Label: "Connect with Herdr", Description: "Attach through SSH to session default; native setup approvals remain interactive"})
		} else {
			for _, profile := range profiles {
				actions = append(actions, tui.FleetHostAction{ID: tuiFleetProfileAction("herdr-connect", profile), Label: "Connect with Herdr · " + profile.Label, Description: "Session " + profile.Session + " · profile " + profile.ID})
			}
		}
		return actions, nil
	}
	if listErr != nil {
		return append(actions, tui.FleetHostAction{ID: "herdr-unavailable", Label: "Herdr saved machines unavailable", Description: "Requires a compatible machine CLI; use SSH or upgrade Herdr", Disabled: true}), nil
	}
	if len(profiles) == 0 {
		return append(actions, tui.FleetHostAction{ID: "herdr-add", Label: "Add to Herdr", Description: "Prepare session default and save on this machine; select it in Herdr's sidebar afterwards"}), nil
	}
	for _, profile := range profiles {
		action := tui.FleetHostAction{ID: tuiFleetProfileAction("herdr-enable", profile), Label: "Enable in Herdr · " + profile.Label, Description: "Session " + profile.Session + " · profile " + profile.ID}
		if profile.Enabled {
			action.Disabled = true
			action.Label = "Already in Herdr · " + profile.Label
			action.Description += "; select the machine in Herdr's native sidebar"
		}
		actions = append(actions, action)
	}
	return actions, nil
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
	actions, err := b.ListActions(ctx, descriptor)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, candidate := range actions {
		if candidate.ID == action && !candidate.Disabled {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("this host action is no longer available")
	}
	payload, err := json.Marshal(tuiFleetHostRequest{Host: descriptor.Name, EndpointID: descriptor.EndpointID, Local: descriptor.Local, Action: action})
	if err != nil {
		return nil, err
	}
	return tuiFleetProcess(ctx, app, "tui", "_fleet-host", "--request", base64.RawURLEncoding.EncodeToString(payload))
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
	if reason := fleetHerdrTargetReason(host); reason != "" {
		return errors.New(reason)
	}
	if app.noRuntime {
		return errors.New("Herdr is disabled by --no-runtime")
	}
	if action == "herdr-add" {
		manage := newSSHManageCmd(app)
		manage.SetContext(ctx)
		manage.SetArgs([]string{"--action", "register", "--to", "herdr", "--alias", host.SSHAlias, "--herdr-label", host.Name, "--herdr-session", "default", "--apply"})
		return manage.Execute()
	}
	session := "default"
	if action != "herdr-connect" {
		parts := strings.Split(action, ":")
		if len(parts) != 3 || (parts[0] != "herdr-connect" && parts[0] != "herdr-enable") {
			return errors.New("unknown fleet host action")
		}
		verb, id := parts[0], parts[1]
		inv, err := (herdrremote.Service{Runner: app.sshHostRunner}).List(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, profile := range inv.Profiles {
			if profile.ID == id && profile.Target == host.SSHAlias && action == tuiFleetProfileAction(verb, profile) {
				session, found = profile.Session, true
			}
		}
		if !found {
			return errors.New("selected Herdr profile changed; reopen host actions")
		}
		if verb == "herdr-enable" {
			manage := newSSHManageCmd(app)
			manage.SetContext(ctx)
			manage.SetArgs([]string{"--action", "enable", "--herdr-profile", id, "--apply"})
			return manage.Execute()
		}
	}
	if os.Getenv("HERDR_ENV") == "1" {
		return errors.New("select the saved machine in Herdr's sidebar; an embedded client is not opened")
	}
	process := exec.CommandContext(ctx, "herdr", "--remote", host.SSHAlias, "--session", session)
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	return process.Run()
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
