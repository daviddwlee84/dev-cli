package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

type sshFleetFixtureRunner struct {
	source          sshremote.Resolved
	calls           []string
	resolutions     int
	changeOnRecheck bool
}

func newSSHFleetFixtureRunner() *sshFleetFixtureRunner {
	origin := sshremote.Origin{MachineID: "11111111-1111-4111-8111-111111111111", Platform: "linux", User: "remote-owner", Root: "/home/remote-owner/.ssh/config"}
	origin.ID = sshremote.OriginID(origin.MachineID, origin.User, origin.Root)
	profile := sshremote.Profile{ID: sshremote.ProfileID(origin.ID, "database"), Alias: "database", Fingerprint: strings.Repeat("a", 64), State: "active", Source: sshhost.Location{Path: origin.Root, Line: 1}, HostName: "10.81.0.8", User: "operator", Port: 2222}
	hop := sshhost.RouteHop{Alias: "database", Reference: "database", HostName: profile.HostName, User: profile.User, Port: profile.Port, RemoteOS: sshhost.RemoteOSUnknown, AdminState: sshhost.AdminUnknown, Target: true}
	return &sshFleetFixtureRunner{source: sshremote.Resolved{Header: sshremote.NewHeader(), Kind: "ssh_remote_resolved", Origin: origin, Profile: profile, ObservedAt: time.Now(), Effective: sshhost.EffectiveConfig{Alias: profile.Alias, HostName: profile.HostName, User: profile.User, Port: profile.Port, IdentityFiles: []string{"/remote/private/key"}}, Route: sshhost.Route{Alias: profile.Alias, Hops: []sshhost.RouteHop{hop}, TargetRemoteOS: sshhost.RemoteOSUnknown}, RouteProfiles: []sshremote.RouteProfile{{OriginID: origin.ID, ProfileID: profile.ID, Hop: hop}}, Importable: true}}
}
func (r *sshFleetFixtureRunner) RunWithOptions(ctx context.Context, host fleet.Host, args []string, stdin []byte, options fleet.RunOptions) fleet.Result {
	if len(args) != 2 {
		return fleet.Result{ExitCode: 1}
	}
	r.calls = append(r.calls, args[1])
	var response any
	switch args[1] {
	case "_ssh-capability":
		response = sshremote.Capability{Header: sshremote.NewHeader(), Kind: "ssh_remote_capability", Origin: r.source.Origin, Supported: true, Operations: []string{"inventory", "resolve", "keys", "connect"}}
	case "_ssh-inventory":
		response = sshremote.Inventory{Header: sshremote.NewHeader(), Kind: "ssh_remote_inventory", Origin: r.source.Origin, Complete: true, ObservedAt: time.Now(), Profiles: []sshremote.Profile{r.source.Profile}}
	case "_ssh-resolve":
		r.resolutions++
		response = r.source
		if r.changeOnRecheck && r.resolutions > 1 {
			response = sshremote.ProtocolError(sshremote.ErrSourceChanged)
		}
	default:
		return fleet.Result{ExitCode: 1}
	}
	data, _ := json.Marshal(response)
	return fleet.Result{Stdout: data}
}
func runSSHFleetFixture(t *testing.T, f *sshCLIFixture, runner *sshFleetFixtureRunner, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &stderr, sshHostRunner: f.runner, sshRemoteRunner: runner, interactiveCheck: func() bool { return false }}
	root := newRootCommand(app)
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, args...))
	err := root.Execute()
	if err != nil {
		t.Logf("stderr %s", stderr.String())
	}
	return out.String(), err
}
func setupSSHFleetFixture(t *testing.T) (*sshCLIFixture, *sshFleetFixtureRunner) {
	t.Helper()
	f := newSSHCLIFixture(t)
	canonical, e := filepath.EvalSymlinks(f.home)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(canonical, ".cache"))
	if e := os.MkdirAll(filepath.Dir(f.remotesPath), 0700); e != nil {
		t.Fatal(e)
	}
	data := "schema_version = 1\n[[hosts]]\nname = 'gateway'\nhostname = 'gateway.example'\nuser = 'remote-owner'\nremote_os = 'posix'\n"
	if e := os.WriteFile(f.remotesPath, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	return f, newSSHFleetFixtureRunner()
}
func TestSSHFleetImportConfigOnlyReimportAndCache(t *testing.T) {
	f, runner := setupSSHFleetFixture(t)
	args := []string{"ssh", "setup", "database-local", "--from", "fleet:gateway/database", "--config-only", "--yes", "--json"}
	out, err := runSSHFleetFixture(t, f, runner, args...)
	if err != nil {
		t.Fatalf("import %v %s", err, out)
	}
	config, err := os.ReadFile(f.managedPath("database-local"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "/remote/private") || !strings.Contains(string(config), "ProxyJump via-") {
		t.Fatalf("imported config %s", config)
	}
	first, err := registryForFixture(f).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Imports) != 2 {
		t.Fatalf("provenance %#v", first.Imports)
	}
	beforeFiles, _ := filepath.Glob(filepath.Join(f.home, ".ssh", "dev.d", "*.conf"))
	out, err = runSSHFleetFixture(t, f, runner, args...)
	if err != nil {
		t.Fatalf("reimport %v %s", err, out)
	}
	afterFiles, _ := filepath.Glob(filepath.Join(f.home, ".ssh", "dev.d", "*.conf"))
	if len(beforeFiles) != len(afterFiles) {
		t.Fatal("reimport duplicated profiles")
	}
	beforeCalls := len(runner.calls)
	localCalls := len(f.runner.callSnapshot())
	out, err = runSSHFleetFixture(t, f, runner, "ssh", "setup", "database-local", "--from", "fleet:gateway/database", "--config-only", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("cached plan %v %s", err, out)
	}
	if len(runner.calls) != beforeCalls || len(f.runner.callSnapshot()) != localCalls {
		t.Fatal("read-only cached import ran a process")
	}
	out, err = runSSHFleetFixture(t, f, runner, "ssh", "list", "--fleet", "--json")
	if err != nil || !strings.Contains(out, "fleet_ssh_sources") {
		t.Fatalf("fleet list %v %s", err, out)
	}
	if len(runner.calls) != beforeCalls {
		t.Fatal("passive list queried remote")
	}
}
func TestSSHFleetImportRemoteChangeStopsBeforeInitialization(t *testing.T) {
	f, runner := setupSSHFleetFixture(t)
	runner.changeOnRecheck = true
	_, err := runSSHFleetFixture(t, f, runner, "ssh", "setup", "database-local", "--from", "fleet:gateway/database", "--config-only", "--yes", "--json")
	if err == nil {
		t.Fatal("changed remote accepted")
	}
	if _, err := os.Stat(f.rootConfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source initialized config: %v", err)
	}
	assertReviewRegistryUnchanged(t, f)
}
func TestSSHConfigOnlyRejectsCycleBeforeMutation(t *testing.T) {
	f := newSSHCLIFixture(t)
	_, _, err := f.run("ssh", "setup", "loop", "--hostname", "127.0.0.1", "--user", "tester", "--proxy-jump", "loop", "--config-only", "--yes", "--json")
	if err == nil {
		t.Fatal("self cycle accepted")
	}
	if _, err := os.Stat(f.managedPath("loop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cycle wrote fragment %v", err)
	}
}

func TestSSHFleetWizardSelectsSourcesAndProfilesBeforeWriting(t *testing.T) {
	f, runner := setupSSHFleetFixture(t)
	output := &sshOnboardWizardOutput{beforeConfirm: func() {
		if _, e := os.Stat(f.rootConfigPath()); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("wizard wrote config before review")
		}
	}}
	var stderr bytes.Buffer
	app := &App{In: strings.NewReader("database-local\n\n\ny\n"), Out: output, Err: &stderr, sshHostRunner: f.runner, sshRemoteRunner: runner, interactiveCheck: func() bool { return true }}
	app.pickerSelect = func(_ context.Context, r picker.Request) (picker.Result, error) {
		if r.Multi {
			return picker.Result{Items: []picker.Item{r.Items[0]}}, nil
		}
		value := "fleet"
		if strings.HasPrefix(r.Prompt, "Authentication") {
			value = "config"
		}
		for _, item := range r.Items {
			if item.Value == value {
				return picker.Result{Item: item}, nil
			}
		}
		return picker.Result{}, errors.New("unexpected wizard picker " + r.Prompt)
	}
	root := newRootCommand(app)
	root.SetArgs([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never", "ssh", "setup"})
	if e := root.Execute(); e != nil {
		t.Fatalf("wizard %v\n%s\n%s", e, output.String(), stderr.String())
	}
	if output.confirmations != 1 {
		t.Fatalf("confirmations=%d", output.confirmations)
	}
	if _, e := os.Stat(f.managedPath("database-local")); e != nil {
		t.Fatal(e)
	}
	for _, call := range f.runner.callSnapshot() {
		if call.Name == "ssh" && !hasSSHArg(call.Args, "-G") {
			t.Fatal("configuration-only import attempted login")
		}
		if call.Name == "ssh-keygen" {
			t.Fatal("configuration-only import touched keys")
		}
	}
}
