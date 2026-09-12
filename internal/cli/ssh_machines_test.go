package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshMachineRunner struct {
	base        *sshCLIRunner
	status      []byte
	calls       []string
	unavailable bool
}

func (r *sshMachineRunner) Run(ctx context.Context, q sshhost.RunRequest) (sshhost.RunResult, error) {
	r.calls = append(r.calls, q.Name+" "+q.Display)
	if q.Name == "tailscale" {
		if r.unavailable {
			return sshhost.RunResult{}, &exec.Error{Name: "tailscale", Err: exec.ErrNotFound}
		}
		return sshhost.RunResult{Stdout: r.status}, nil
	}
	if q.Name == "herdr" {
		return sshhost.RunResult{Stdout: []byte("[]")}, nil
	}
	return r.base.Run(ctx, q)
}
func machineStatus(advertised bool) []byte {
	peer := map[string]any{"ID": "node1", "HostName": "lab", "DNSName": "lab.tailtest.ts.net.", "OS": "linux", "TailscaleIPs": []string{"100.77.43.16"}, "Online": true}
	if advertised {
		peer["sshHostKeys"] = []string{"ssh-ed25519 public"}
	}
	data, _ := json.Marshal(map[string]any{"Version": "1.102.3", "BackendState": "Running", "Self": map[string]any{"ID": "self"}, "CurrentTailnet": map[string]any{"MagicDNSSuffix": "tailtest.ts.net"}, "Peer": map[string]any{"rotating-key": peer}})
	return data
}
func runMachineCLI(t *testing.T, f *sshCLIFixture, runner sshhost.Runner, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &stderr, sshHostRunner: runner, interactiveCheck: func() bool { return false }}
	root := newRootCommand(app)
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, args...))
	err := root.Execute()
	if err != nil {
		t.Logf("stderr: %s", stderr.String())
	}
	return out.String(), err
}
func registryForFixture(f *sshCLIFixture) *machineregistry.Store {
	return machineregistry.NewStore(filepath.Join(f.home, ".local", "share", "dev", "machines", "registry.db"))
}

func TestSSHSourceSetupDryRunAndKeylessFleet(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(true)}
	args := []string{"ssh", "setup", "lab", "--from", "tailscale:lab", "--user", "tester", "--auth", "existing", "--to", "fleet", "--json"}
	out, err := runMachineCLI(t, f, runner, append(args, "--dry-run")...)
	if err != nil {
		t.Fatalf("plan: %v %s", err, out)
	}
	for _, call := range runner.calls {
		if !strings.HasPrefix(call, "tailscale ") {
			t.Fatalf("dry-run effect: %s", call)
		}
	}
	for _, path := range []string{f.rootConfigPath(), registryForFixture(f).Path, sshDiscoveryCacheDir()} {
		if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("dry-run created %s: %v", path, e)
		}
	}
	runner.calls = nil
	out, err = runMachineCLI(t, f, runner, append(args, "--yes")...)
	if err != nil {
		t.Fatalf("apply: %v %s", err, out)
	}
	var result struct{ Kind, Status string }
	if json.Unmarshal([]byte(out), &result) != nil || result.Kind != "ssh_onboarding_result" || result.Status != "ready" {
		t.Fatal(out)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "ssh-keygen") || strings.Contains(call, "selected-key") || strings.Contains(call, "installer") {
			t.Fatalf("keyless ran %s", call)
		}
	}
	data, err := os.ReadFile(f.managedPath("lab"))
	if err != nil || !bytes.Contains(data, []byte("100.77.43.16")) || bytes.Contains(data, []byte("IdentityFile")) {
		t.Fatalf("managed: %s %v", data, err)
	}
	snapshot, err := registryForFixture(f).Read(context.Background())
	if err != nil || len(snapshot.Machines) != 1 || len(snapshot.Bindings) != 2 {
		t.Fatalf("registry: %#v %v", snapshot, err)
	}
	id := snapshot.Machines[0].ID
	out, err = runMachineCLI(t, f, runner, append(args, "--yes")...)
	if err != nil {
		t.Fatalf("idempotent rerun: %v %s", err, out)
	}
	snapshot, _ = registryForFixture(f).Read(context.Background())
	if len(snapshot.Machines) != 1 || snapshot.Machines[0].ID != id {
		t.Fatal("rerun replaced canonical identity")
	}
	if _, e := os.Stat(filepath.Join(filepath.Dir(f.remotesPath), "remotes.d", "ssh-lab.toml")); e != nil {
		t.Fatal(e)
	}
}

func TestSSHOptionalDiscoveryListCompatibilityAndCacheClear(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.createManaged("lab", "100.77.43.16")
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	out, err := runMachineCLI(t, f, runner, "ssh", "list", "--json")
	if err != nil || len(runner.calls) != 0 {
		t.Fatalf("static list: %v %v", err, runner.calls)
	}
	if strings.Contains(out, "\"machines\"") {
		t.Fatal("default JSON contract changed")
	}
	out, err = runMachineCLI(t, f, runner, "ssh", "list", "--tailscale", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Aliases  []any
		Machines []struct {
			ID       string
			Profiles []struct{ Alias string }
		}
	}
	if err = json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Aliases) != 1 || len(doc.Machines) != 1 || len(doc.Machines[0].Profiles) != 1 {
		t.Fatal(out)
	}
	if _, e := os.Stat(registryForFixture(f).Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("list created registry")
	}
	store := registryForFixture(f)
	p, e := store.Plan(context.Background(), machineregistry.Request{Action: "adopt", Label: "keep"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Apply(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if _, err = runMachineCLI(t, f, runner, "cache", "clear", "all"); err != nil {
		t.Fatal(err)
	}
	snapshot, e := store.Read(context.Background())
	if e != nil || len(snapshot.Machines) != 1 {
		t.Fatal("cache clear removed identity")
	}
	runner.unavailable = true
	runner.calls = nil
	out, err = runMachineCLI(t, f, runner, "ssh", "list", "--tailscale", "--json")
	var partial struct {
		Complete bool
		Sources  map[string]string
	}
	decodeErr := json.Unmarshal([]byte(out), &partial)
	if err != nil || decodeErr != nil || partial.Complete || partial.Sources["tailscale"] != "unavailable" || !strings.Contains(out, "\"lab\"") {
		t.Fatalf("partial list: %v %s", err, out)
	}
}

func TestSSHAdvertisedKeyBootstrapRefusedWithoutConfiguration(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(true)}
	out, err := runMachineCLI(t, f, runner, "ssh", "setup", "lab", "--from", "tailscale:lab", "--user", "tester", "--generate-key", "--no-passphrase", "--yes", "--json")
	if err == nil {
		t.Fatal("accepted key bootstrap on Tailscale SSH")
	}
	var doc map[string]any
	if json.Unmarshal([]byte(out), &doc) != nil {
		t.Fatal("missing one safe error JSON:", out)
	}
	if _, e := os.Stat(f.rootConfigPath()); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("refused setup wrote config")
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "ssh-keygen") || strings.HasPrefix(call, "ssh ") {
			t.Fatal(call)
		}
	}
}

func TestSSHMachinesMultipleAliasesOnePeerAndDefaultUser(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	for _, pair := range [][2]string{{"lab", "tester"}, {"lab-root", "root"}} {
		out, err := runMachineCLI(t, f, runner, "ssh", "setup", pair[0], "--from", "tailscale:lab", "--user", pair[1], "--config-only", "--yes", "--json")
		if err != nil {
			t.Fatalf("%s: %v %s", pair[0], err, out)
		}
	}
	snapshot, err := registryForFixture(f).Read(context.Background())
	if err != nil || len(snapshot.Machines) != 1 {
		t.Fatalf("separate identities: %#v %v", snapshot, err)
	}
	for _, pair := range [][2]string{{"lab", "tester"}, {"lab-root", "root"}} {
		data, e := os.ReadFile(f.managedPath(pair[0]))
		if e != nil || !bytes.Contains(data, []byte(pair[1])) {
			t.Fatalf("lost user: %s %v", data, e)
		}
	}
}
