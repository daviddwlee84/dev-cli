package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sshTUITestApp(t *testing.T) (*App, *sshCLIFixture) {
	t.Helper()
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.appendRootConfig("Host first second\n HostName 100.64.0.2\n User remote-user\n Port 22\n")
	a := &App{Cfg: config.Default(), In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, remotesPath: f.remotesPath, sshHostRunner: f.runner, interactiveCheck: func() bool { return true }}
	return a, f
}

func TestSSHTUILoadIsPassiveAndDoesNotCreateRegistry(t *testing.T) {
	app, fixture := sshTUITestApp(t)
	fixture.runner.resetCalls()
	actions := sshTUIActions(newTUIAppState(app))
	inv, err := actions.Load(t.Context())
	if err != nil || len(inv.Machines) != 2 {
		t.Fatalf("inventory=%+v error=%v", inv, err)
	}
	for _, call := range fixture.runner.callSnapshot() {
		if call.Name != "herdr" || strings.Join(call.Args, " ") != "machine list --json" {
			t.Fatalf("passive load invoked %s %v", call.Name, call.Args)
		}
	}
	if _, err := os.Stat(filepath.Join(app.Cfg.StateDir(), "machines", "registry.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passive load created registry: %v", err)
	}
}

func TestSSHTUIProfileChoiceAndNativeKeylessConnection(t *testing.T) {
	app, fixture := sshTUITestApp(t)
	inv, err := loadSSHMachineInventory(t.Context(), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	request := machineregistry.Request{Action: "adopt", Label: "shared machine"}
	for _, row := range inv.Machines {
		for _, ref := range row.References {
			request.Bindings = append(request.Bindings, sshflow.ReferenceBinding(ref))
		}
	}
	plan, err := app.machineStore().Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.machineStore().Apply(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	inv, err = loadSSHMachineInventory(t.Context(), app, false, true)
	if err != nil || len(inv.Machines) != 1 || len(inv.Machines[0].Profiles) != 2 {
		t.Fatalf("inventory=%+v error=%v", inv, err)
	}
	picked := false
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if request.Prompt != "Choose the exact SSH profile" || request.Multi || len(request.Items) != 2 {
			t.Fatalf("unexpected picker %+v", request)
		}
		picked = true
		return picker.Result{Item: request.Items[1]}, nil
	}
	fixture.runner.resetCalls()
	workflow, err := sshTUIActions(newTUIAppState(app)).Workflow(t.Context(), tui.SSHWorkflowRequest{Action: "connect", Selected: inv.Machines[0]})
	if err != nil {
		t.Fatal(err)
	}
	workflow.SetStdin(strings.NewReader(""))
	workflow.SetStdout(io.Discard)
	workflow.SetStderr(io.Discard)
	if err := workflow.Run(); err != nil {
		t.Fatal(err)
	}
	if !picked {
		t.Fatal("multiple aliases bypassed endpoint picker")
	}
	connections := 0
	for _, call := range fixture.runner.callSnapshot() {
		if call.Name == "ssh" && call.Interactive && !hasSSHArg(call.Args, "-E") {
			connections++
			if call.Args[len(call.Args)-1] != "second" || !hasSSHArg(call.Args, "ForwardAgent=no") || len(call.Stdin) != 0 {
				t.Fatalf("non-native connection %+v", call)
			}
		}
		if call.Name == "ssh-keygen" || call.Name == "tailscale" {
			t.Fatalf("keyless connect invoked %s", call.Name)
		}
	}
	if connections != 1 || workflow.Result().MembershipChanged {
		t.Fatalf("connections=%d result=%+v", connections, workflow.Result())
	}
}

func TestSSHTUIRejectsChangedSourceBeforeNativeConnection(t *testing.T) {
	app, fixture := sshTUITestApp(t)
	inv, err := loadSSHMachineInventory(t.Context(), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	selected := inv.Machines[0]
	fixture.appendRootConfig("# source changed after selection\n")
	fixture.runner.resetCalls()
	workflow, err := sshTUIActions(newTUIAppState(app)).Workflow(t.Context(), tui.SSHWorkflowRequest{Action: "connect", Selected: selected})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Run(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("error=%v", err)
	}
	for _, call := range fixture.runner.callSnapshot() {
		if call.Name == "ssh" {
			t.Fatal("stale profile started SSH")
		}
	}
}

func TestSSHTUIRevalidationUsesExactAliasFingerprintAndSource(t *testing.T) {
	app, _ := sshTUITestApp(t)
	inv, err := loadSSHMachineInventory(t.Context(), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	profile := inv.Machines[0].Profiles[0]
	if err := revalidateSSHTUIProfile(t.Context(), app, profile); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*sshflow.ConnectionProfile){func(p *sshflow.ConnectionProfile) { p.Fingerprint = "changed" }, func(p *sshflow.ConnectionProfile) { p.Alias = "different" }, func(p *sshflow.ConnectionProfile) { p.Source = sshhost.Location{Path: "foreign"} }, func(p *sshflow.ConnectionProfile) { p.User = "other-user" }} {
		changed := profile
		change(&changed)
		if err := revalidateSSHTUIProfile(t.Context(), app, changed); err == nil {
			t.Fatal("changed connection profile was accepted")
		}
	}
}

func TestSSHTUIProfileMatchingRejectsMovedMapping(t *testing.T) {
	p := sshflow.ConnectionProfile{ID: "source", Alias: "box", Fingerprint: "fingerprint"}
	selected := sshflow.MachineRow{ID: "one", MachineID: "one", Profiles: []sshflow.ConnectionProfile{p}}
	current := sshflow.MachineInventory{Machines: []sshflow.MachineRow{{ID: "two", MachineID: "two", Profiles: []sshflow.ConnectionProfile{p}}}}
	if _, err := matchSSHTUIProfile(selected, current, p); err == nil {
		t.Fatal("moved mapping was silently followed")
	}
}

func TestSSHTUIMappingUnlinkUsesRegistryAndRetainsSSHConfig(t *testing.T) {
	app, fixture := sshTUITestApp(t)
	inv, err := loadSSHMachineInventory(t.Context(), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	ref := inv.Machines[0].References[0]
	plan, err := app.machineStore().Plan(t.Context(), machineregistry.Request{Action: "adopt", Label: "machine", Bindings: []machineregistry.Binding{sshflow.ReferenceBinding(ref)}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.machineStore().Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fixture.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	app.In = strings.NewReader("y\n")
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if request.Prompt == "Machine identity mapping" {
			return picker.Result{Item: picker.Item{Value: "unlink"}}, nil
		}
		if !request.Multi || len(request.Items) != 1 {
			t.Fatalf("unexpected source choice: %+v", request)
		}
		return picker.Result{Items: request.Items}, nil
	}
	if err := runSSHTUIMappings(t.Context(), app, sshflow.MachineRow{MachineID: result.MachineID}); err != nil {
		t.Fatal(err)
	}
	current, err := app.machineStore().Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	binding, found := current.LookupBinding(ref.Provider, ref.Scope, ref.NativeID)
	if !found || !binding.Suppressed || binding.MachineID != "" {
		t.Fatalf("binding=%+v", binding)
	}
	after, err := os.ReadFile(fixture.rootConfigPath())
	if err != nil || string(before) != string(after) {
		t.Fatal("mapping changed source SSH configuration")
	}
}
