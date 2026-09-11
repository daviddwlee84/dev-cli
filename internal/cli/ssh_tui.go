package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sshTUIActions(state *tuiAppState) tui.SSHActions {
	return tui.SSHActions{
		Load: func(ctx context.Context) (tui.SSHInventory, error) {
			active := *state.Current()
			return loadSSHMachineInventory(ctx, &active, false, true)
		},
		Workflow: func(ctx context.Context, request tui.SSHWorkflowRequest) (tui.SSHWorkflow, error) {
			return &sshTUIWorkflow{ctx: ctx, app: *state.Current(), request: request}, nil
		},
	}
}

// Each workflow runs after Bubble Tea restores the foreground terminal. SSH
// and Herdr keep native credential/installation interaction; no local runtime
// needs to be resolved to administer or connect to machines.
type sshTUIWorkflow struct {
	ctx     context.Context
	app     App
	request tui.SSHWorkflowRequest
	result  tui.SSHWorkflowResult
}

func (w *sshTUIWorkflow) SetStdin(in io.Reader)         { w.app.In = in }
func (w *sshTUIWorkflow) SetStdout(out io.Writer)       { w.app.Out = out }
func (w *sshTUIWorkflow) SetStderr(out io.Writer)       { w.app.Err = out }
func (w *sshTUIWorkflow) Result() tui.SSHWorkflowResult { return w.result }
func (w *sshTUIWorkflow) Run() error {
	err := w.run()
	if errors.Is(err, errPromptCanceled) {
		w.result.Status = "SSH action canceled"
		return nil
	}
	if err == nil && w.result.Status == "" {
		w.result.Status = "Returned from SSH " + w.request.Action
	}
	return err
}

func (w *sshTUIWorkflow) run() error {
	app, ctx := &w.app, w.ctx
	switch w.request.Action {
	case "setup":
		w.result.MembershipChanged = true // interrupted setup can retain partial registrations
		return runSSHOnboarding(ctx, app, nil, sshSetupOptions{})
	case "discover":
		selected, err := sshPick(ctx, app, "Discover hosts", []picker.Item{{Value: "tailscale", Label: "Refresh Tailscale peers", Description: "Read the local Tailscale daemon"}, {Value: "lan", Label: "Scan a selected LAN range", Description: "Choose on-link IPv4 scope and SSH ports; no authentication"}}, false)
		if err != nil {
			return err
		}
		request := sshdiscovery.LANRequest{Ports: []int{22}}
		if selected[0].Value == "lan" {
			value, e := newPrompter(app).line("SSH ports (comma separated, maximum 16)", "22")
			if e != nil {
				return e
			}
			request.Ports = nil
			for _, part := range strings.Split(value, ",") {
				port, e := strconv.Atoi(strings.TrimSpace(part))
				if e != nil || port < 1 || port > 65535 {
					return errors.New("SSH ports must be between 1 and 65535")
				}
				request.Ports = append(request.Ports, port)
			}
			if len(request.Ports) > 16 {
				return errors.New("select at most 16 SSH ports")
			}
		}
		report, err := runSSHDiscovery(ctx, app, selected[0].Value, request, true, false)
		renderSSHDiscovery(app, report)
		return err
	case "mappings":
		return runSSHTUIMappings(ctx, app, w.request.Selected)
	case "connect", "probe", "diagnose", "register":
	default:
		return fmt.Errorf("unsupported SSH dashboard action %q", w.request.Action)
	}
	profile, err := chooseSSHTUIProfile(ctx, app, w.request.Selected)
	if err != nil {
		return err
	}
	if err := revalidateSSHTUIProfile(ctx, app, profile); err != nil {
		return err
	}
	switch w.request.Action {
	case "connect":
		runner := app.sshHostRunner
		if runner == nil {
			runner = sshhost.ExecRunner{}
		}
		result, err := runner.Run(ctx, sshhost.RunRequest{Name: "ssh", Args: []string{profile.Alias}, Interactive: true, Display: "SSH interactive connection"})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("SSH connection exited %d", result.ExitCode)
		}
		return nil
	case "probe":
		return runSSHProbe(ctx, app, profile.Alias, false)
	case "diagnose":
		return runSSHDiagnose(ctx, app, sshhost.DiagnoseRequest{Target: profile.Alias}, false)
	case "register":
		options, err := sshTUIRegistrationOptions(ctx, app, w.request.Selected, profile.Alias)
		if err != nil {
			return err
		}
		if err := revalidateSSHTUIProfile(ctx, app, profile); err != nil {
			return err
		}
		w.result.MembershipChanged = true
		return runSSHOnboarding(ctx, app, []string{profile.Alias}, options)
	}
	return nil
}

func chooseSSHTUIProfile(ctx context.Context, app *App, selected sshflow.MachineRow) (sshflow.ConnectionProfile, error) {
	if selected.ID == "" || len(selected.Profiles) == 0 {
		return sshflow.ConnectionProfile{}, errors.New("select a machine with an SSH profile; n sets up a connection")
	}
	profile := selected.Profiles[0]
	if len(selected.Profiles) > 1 {
		items := []picker.Item{}
		for _, p := range selected.Profiles {
			endpoint, user, port := p.HostName, p.User, "native"
			if endpoint == "" {
				endpoint = "resolved by OpenSSH"
			}
			if user == "" {
				user = "native"
			}
			if p.Port != 0 {
				port = fmt.Sprint(p.Port)
			}
			items = append(items, picker.Item{Value: p.ID, Label: p.Alias, Description: fmt.Sprintf("%s user=%s port=%s · %s", endpoint, user, port, p.State)})
		}
		picked, err := sshPick(ctx, app, "Choose the exact SSH profile", items, false)
		if err != nil {
			return profile, err
		}
		found := false
		for _, p := range selected.Profiles {
			if p.ID == picked[0].Value {
				profile = p
				found = true
			}
		}
		if !found {
			return profile, errors.New("selected SSH profile is no longer available")
		}
	}
	current, err := loadSSHMachineInventory(ctx, app, false, true)
	if err != nil {
		return profile, err
	}
	return matchSSHTUIProfile(selected, current, profile)
}

func matchSSHTUIProfile(selected sshflow.MachineRow, current sshflow.MachineInventory, profile sshflow.ConnectionProfile) (sshflow.ConnectionProfile, error) {
	matches := 0
	for _, row := range current.Machines {
		if row.ID != selected.ID || row.MachineID != selected.MachineID {
			continue
		}
		for _, candidate := range row.Profiles {
			if candidate.ID == profile.ID && candidate == profile {
				matches++
			}
		}
	}
	if profile.ID == "" || profile.Alias == "" || profile.Fingerprint == "" || matches != 1 {
		return sshflow.ConnectionProfile{}, errors.New("selected SSH profile or machine mapping changed; refresh and select it again")
	}
	return profile, nil
}

func revalidateSSHTUIProfile(ctx context.Context, app *App, expected sshflow.ConnectionProfile) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	inv, err := service.Discover(ctx)
	if err != nil {
		return err
	}
	if err := requireStaticProbeSelection(expected.Alias, inv); err != nil {
		return err
	}
	for _, hint := range inv.ConnectionHints {
		if hint.Alias == expected.Alias && hint.Fingerprint == expected.Fingerprint && hint.Source == expected.Source && hint.HostName == expected.HostName && hint.User == expected.User && hint.Port == expected.Port && hint.State == expected.State {
			return nil
		}
	}
	return errors.New("SSH configuration changed after profile selection; refresh and select it again")
}

func sshTUIRegistrationOptions(ctx context.Context, app *App, row sshflow.MachineRow, alias string) (sshSetupOptions, error) {
	options := sshSetupOptions{auth: "existing"}
	to, err := sshPick(ctx, app, "Register this SSH profile with", []picker.Item{{Value: "fleet", Label: "Fleet"}, {Value: "herdr", Label: "Herdr"}, {Value: "both", Label: "Fleet and Herdr"}}, false)
	if err != nil {
		return options, err
	}
	options.to = to[0].Value
	defaultOS := "posix"
	for _, candidate := range append(append([]sshdiscovery.Candidate{}, row.Tailscale...), row.LAN...) {
		if strings.EqualFold(candidate.OS, "windows") {
			defaultOS = "windows"
		}
	}
	options.targetOS, err = newPrompter(app).choice("Target OS", defaultOS, "posix, windows", map[string]string{"posix": "posix", "windows": "windows"})
	if err != nil {
		return options, err
	}
	if options.to != "herdr" {
		options.fleetName, err = newPrompter(app).line("Fleet profile name", alias)
		if err != nil {
			return options, err
		}
	}
	if options.to != "fleet" {
		options.herdrLabel, err = newPrompter(app).line("Herdr label", alias)
		if err != nil {
			return options, err
		}
		options.herdrSession, err = newPrompter(app).line("Herdr session", "default")
		if err != nil {
			return options, err
		}
	}
	return options, nil
}

func runSSHTUIMappings(ctx context.Context, app *App, selected sshflow.MachineRow) error {
	choice, err := sshPick(ctx, app, "Machine identity mapping", []picker.Item{{Value: "adopt", Label: "Enroll candidate groups"}, {Value: "link", Label: "Link source references to a machine"}, {Value: "unlink", Label: "Unlink source references"}, {Value: "merge", Label: "Merge two canonical machine identities"}}, false)
	if err != nil {
		return err
	}
	action := choice[0].Value
	if action == "adopt" {
		return runSSHAdoptWizard(ctx, app)
	}
	snapshot, err := app.machineStore().Read(ctx)
	if err != nil {
		return err
	}
	chooseMachine := func(prompt, exclude string) (string, error) {
		items := []picker.Item{}
		for _, machine := range snapshot.Machines {
			if machine.MergedInto == "" && machine.ID != exclude {
				items = append(items, picker.Item{Value: machine.ID, Label: machine.Label, Description: machine.ID})
			}
		}
		if len(items) == 0 {
			return "", errors.New("no eligible canonical machine; enroll candidates first")
		}
		picked, err := sshPick(ctx, app, prompt, items, false)
		if err != nil {
			return "", err
		}
		return picked[0].Value, nil
	}
	machineID := selected.MachineID
	if machineID != "" {
		if machine, ok := snapshot.Find(machineID); !ok || machine.ID != machineID {
			return errors.New("selected machine identity changed; refresh before editing mappings")
		}
	}
	if machineID == "" {
		machineID, err = chooseMachine("Choose canonical machine", "")
		if err != nil {
			return err
		}
	}
	request := machineregistry.Request{Action: action, MachineID: machineID}
	if action == "merge" {
		request.Into, err = chooseMachine("Merge into this surviving identity", machineID)
		if err != nil {
			return err
		}
		return runSSHMachineChange(ctx, app, request, true, false, false)
	}
	bindings := map[string]machineregistry.Binding{}
	items := []picker.Item{}
	if action == "unlink" {
		for _, binding := range snapshot.Bindings {
			if binding.MachineID == machineID && !binding.Suppressed {
				id := sshflow.ReferenceID(binding.Provider, binding.Scope, binding.NativeID)
				bindings[id] = binding
				items = append(items, picker.Item{Value: id, Label: binding.Provider + " · " + binding.NativeID, Description: binding.Scope})
			}
		}
	} else {
		inventory, err := loadSSHMachineInventory(ctx, app, false, true)
		if err != nil {
			return err
		}
		for _, row := range inventory.Machines {
			for _, ref := range row.References {
				if ref.State == "unresolved" || ref.State == "stale" {
					continue
				}
				if _, duplicate := bindings[ref.ID]; duplicate {
					continue
				}
				bindings[ref.ID] = sshflow.ReferenceBinding(ref)
				items = append(items, picker.Item{Value: ref.ID, Label: ref.Provider + " · " + ref.NativeID, Description: row.Label + " · " + ref.State})
			}
		}
	}
	if len(items) == 0 {
		return errors.New("no eligible source references")
	}
	picked, err := sshPick(ctx, app, "Select exact source references to "+action, items, true)
	if err != nil {
		return err
	}
	for _, selection := range picked {
		binding, ok := bindings[selection.Value]
		if !ok {
			return errors.New("source selection changed")
		}
		request.Bindings = append(request.Bindings, binding)
	}
	return runSSHMachineChange(ctx, app, request, true, false, false)
}
