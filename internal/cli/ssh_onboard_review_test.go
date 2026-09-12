package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshReviewPromptReader struct {
	reader     io.Reader
	once       sync.Once
	beforeRead func()
}

func (r *sshReviewPromptReader) Read(data []byte) (int, error) {
	r.once.Do(r.beforeRead)
	return r.reader.Read(data)
}

func runSSHReviewInteractive(t *testing.T, f *sshCLIFixture, runner sshhost.Runner, onConfirm func(), args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	app := &App{In: &sshReviewPromptReader{reader: strings.NewReader("y\n"), beforeRead: onConfirm}, Out: &out, Err: &stderr, sshHostRunner: runner, interactiveCheck: func() bool { return true }}
	root := newRootCommand(app)
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, args...))
	err := root.Execute()
	return out.String(), err
}

func assertReviewRegistryUnchanged(t *testing.T, f *sshCLIFixture) {
	t.Helper()
	snapshot, err := registryForFixture(f).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Machines) != 0 || len(snapshot.Bindings) != 0 {
		t.Fatalf("stale/mismatched input wrote registry: %#v", snapshot)
	}
}

func TestSSHReviewAdoptRevalidatesSSHSourceAfterConfirmation(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.createManaged("lab", "192.168.1.10")
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	selector := sshflow.ReferenceID("ssh", f.rootConfigPath(), "lab")
	mutated := false
	out, err := runSSHReviewInteractive(t, f, runner, func() {
		data, e := os.ReadFile(f.managedPath("lab"))
		if e != nil {
			t.Fatal(e)
		}
		data = bytes.ReplaceAll(data, []byte("192.168.1.10"), []byte("192.168.1.11"))
		if e = os.WriteFile(f.managedPath("lab"), data, 0o600); e != nil {
			t.Fatal(e)
		}
		mutated = true
	}, "ssh", "machine", "adopt", "--source", selector, "--label", "Lab", "--apply")
	if !mutated {
		t.Fatal("confirmation seam was not exercised")
	}
	if err == nil {
		t.Fatalf("source retargeted during confirmation was accepted: %s", out)
	}
	assertReviewRegistryUnchanged(t, f)
}

type sshReviewHerdrRunner struct {
	base   *sshCLIRunner
	target string
}

func (r *sshReviewHerdrRunner) Run(ctx context.Context, q sshhost.RunRequest) (sshhost.RunResult, error) {
	if q.Name == "herdr" {
		data, _ := json.Marshal([]map[string]any{{"id": strings.Repeat("a", 32), "label": "Remote", "target": r.target, "session": "dev", "enabled": true}})
		return sshhost.RunResult{Stdout: data}, nil
	}
	return r.base.Run(ctx, q)
}

func TestSSHReviewAdoptRevalidatesHerdrSourceAfterConfirmation(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	runner := &sshReviewHerdrRunner{base: f.runner, target: "original-host"}
	selector := sshflow.ReferenceID("herdr", filepath.Dir(f.rootConfigPath()), strings.Repeat("a", 32))
	mutated := false
	out, err := runSSHReviewInteractive(t, f, runner, func() { runner.target = "replacement-host"; mutated = true }, "ssh", "machine", "adopt", "--source", selector, "--label", "Remote", "--apply")
	if !mutated {
		t.Fatalf("did not reach confirmation: %v %s", err, out)
	}
	if err == nil {
		t.Fatalf("retargeted provider record was accepted: %s", out)
	}
	assertReviewRegistryUnchanged(t, f)
}

func TestSSHReviewSourceSetupRejectsMismatchedForeignAlias(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.appendRootConfig("\nHost production\n    HostName 192.168.1.99\n    User operator\n")
	before, err := os.ReadFile(f.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	out, err := runMachineCLI(t, f, runner, "ssh", "setup", "production", "--from", "tailscale:lab", "--config-only", "--yes", "--json")
	if err == nil {
		t.Fatalf("foreign production alias was assigned to unrelated tailnet node: %s", out)
	}
	after, e := os.ReadFile(f.rootConfigPath())
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign config changed")
	}
	assertReviewRegistryUnchanged(t, f)
}

func TestSSHReviewMissingExplicitMachineRejectedBeforeConfiguration(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	out, err := runMachineCLI(t, f, runner, "ssh", "setup", "lab", "--from", "tailscale:lab", "--user", "tester", "--machine", "11111111-1111-4111-8111-111111111111", "--config-only", "--yes", "--json")
	if err == nil {
		t.Fatalf("missing explicit machine accepted: %s", out)
	}
	if _, e := os.Lstat(f.rootConfigPath()); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("invalid machine selection created SSH configuration: %v", e)
	}
	assertReviewRegistryUnchanged(t, f)
}

func TestSSHReviewUnlinkAllowsUnresolvedRecordedReference(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	store := registryForFixture(f)
	binding := machineregistry.Binding{Provider: "ssh", Scope: f.rootConfigPath(), NativeID: "removed-alias", Fingerprint: "last-reviewed-source"}
	plan, err := store.Plan(context.Background(), machineregistry.Request{Action: "adopt", Label: "Saved", Bindings: []machineregistry.Binding{binding}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	out, err := runMachineCLI(t, f, runner, "ssh", "machine", "unlink", "--machine", plan.MachineID, "--source", sshflow.ReferenceID(binding.Provider, binding.Scope, binding.NativeID), "--apply", "--yes", "--json")
	if err != nil {
		t.Fatalf("cannot unlink disappeared source: %v %s", err, out)
	}
	snapshot, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Bindings) != 1 || !snapshot.Bindings[0].Suppressed || snapshot.Bindings[0].MachineID != "" {
		t.Fatalf("unlink did not retain suppression intent: %#v", snapshot)
	}
}

func TestSSHReviewMachineShowJSONHonorsMachineSelector(t *testing.T) {
	f := newSSHCLIFixture(t)
	store := registryForFixture(f)
	var selectedID string
	for _, label := range []string{"Selected", "Unrelated"} {
		plan, err := store.Plan(context.Background(), machineregistry.Request{Action: "adopt", Label: label, Bindings: []machineregistry.Binding{{Provider: "ssh", Scope: f.rootConfigPath(), NativeID: strings.ToLower(label), Fingerprint: "reviewed"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Apply(context.Background(), plan); err != nil {
			t.Fatal(err)
		}
		if selectedID == "" {
			selectedID = plan.MachineID
		}
	}
	imports := []machineregistry.SSHImport{}
	for _, alias := range []string{"selected", "unrelated"} {
		imports = append(imports, machineregistry.SSHImport{LocalAlias: alias, OriginID: "source-origin", ProfileID: alias, FleetHost: "gateway", RemoteAlias: alias, SourceFingerprint: "source", RouteFingerprint: "route", DefinitionFingerprint: "definition"})
	}
	importPlan, e := store.Plan(context.Background(), machineregistry.Request{Action: "record-imports", Imports: imports})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Apply(context.Background(), importPlan); e != nil {
		t.Fatal(e)
	}
	out, err := runMachineCLI(t, f, f.runner, "ssh", "machine", "show", selectedID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot machineregistry.Snapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("invalid single JSON snapshot: %v %s", err, out)
	}
	if len(snapshot.Machines) != 1 || snapshot.Machines[0].ID != selectedID {
		t.Fatalf("JSON ignored machine selector: %s", out)
	}
	if len(snapshot.Imports) != 1 || snapshot.Imports[0].LocalAlias != "selected" {
		t.Fatalf("JSON included unrelated import: %s", out)
	}
	if len(snapshot.Bindings) != 1 || snapshot.Bindings[0].MachineID != selectedID {
		t.Fatalf("JSON included unrelated bindings: %s", out)
	}
	if _, err := runMachineCLI(t, f, f.runner, "ssh", "machine", "show", "11111111-1111-4111-8111-111111111111", "--json"); !errors.Is(err, machineregistry.ErrNotFound) {
		t.Fatalf("missing selected machine did not fail: %v", err)
	}
}

func TestSSHReviewFullDiscoveryIDsAndStaleLANProvenance(t *testing.T) {
	f := newSSHCLIFixture(t)
	canonicalHome, err := filepath.EvalSymlinks(f.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(canonicalHome, ".cache"))
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	tailscale := sshdiscovery.NewService(runner)
	tailReport, err := tailscale.Tailscale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Out: io.Discard, Err: io.Discard, sshDiscoveryService: tailscale}
	selected, err := resolveSSHDiscoveryTarget(context.Background(), app, tailReport.Candidates[0].ID)
	if err != nil || selected.ID != tailReport.Candidates[0].ID {
		t.Fatalf("full Tailscale ID selection: %#v %v", selected, err)
	}

	scope := sshdiscovery.InterfaceScope{Interface: "home0", Index: 2, Address: "192.168.55.10", Prefix: "192.168.55.0/24"}
	service := sshdiscovery.NewService(nil, sshdiscovery.ServiceOptions{
		Interfaces: func() ([]sshdiscovery.InterfaceScope, error) { return []sshdiscovery.InterfaceScope{scope}, nil },
		LookupAddr: func(context.Context, string) ([]string, error) { return nil, errors.New("no PTR") },
		Now:        func() time.Time { return time.Now().Add(-sshdiscovery.CacheTTL - time.Minute) },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() { defer server.Close(); _, _ = io.WriteString(server, "SSH-2.0-OpenSSH\r\n") }()
			return client, nil
		},
	})
	report, err := service.LAN(context.Background(), sshdiscovery.LANRequest{Interface: scope.Interface, Ranges: []string{"192.168.55.20"}, Ports: []int{22}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sshdiscovery.WriteCache(context.Background(), sshDiscoveryCacheDir(), report); err != nil {
		t.Fatal(err)
	}
	app.sshDiscoveryService = service
	selected, err = resolveSSHDiscoveryTarget(context.Background(), app, report.Candidates[0].ID)
	if err != nil || selected.ID != report.Candidates[0].ID || selected.State != "stale_observation" {
		t.Fatalf("explicit stale source identity: %#v %v", selected, err)
	}
	selected, err = resolveSSHDiscoveryTarget(context.Background(), app, "lan:192.168.55.20:22")
	if err != nil || selected.Scope != "explicit-endpoint" || selected.State != "unobserved" {
		t.Fatalf("implicit stale cache identity reused: %#v %v", selected, err)
	}
	// Fresh metadata for an interface that has since changed is also only an
	// explicit endpoint suggestion unless its exact cached ID is selected.
	report.ObservedAt = time.Now()
	if err := sshdiscovery.WriteCache(context.Background(), sshDiscoveryCacheDir(), report); err != nil {
		t.Fatal(err)
	}
	scope.Interface, scope.Index = "office0", 3
	selected, err = resolveSSHDiscoveryTarget(context.Background(), app, "lan:192.168.55.20:22")
	if err != nil || selected.Scope != "explicit-endpoint" {
		t.Fatalf("wrong-interface cache identity reused: %#v %v", selected, err)
	}
}

func TestSSHReviewLANSetupDoesNotChooseFirstOfCompetingScopes(t *testing.T) {
	f := newSSHCLIFixture(t)
	canonicalHome, err := filepath.EvalSymlinks(f.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(canonicalHome, ".cache"))
	scopes := []sshdiscovery.InterfaceScope{
		{Interface: "home0", Index: 2, Address: "192.168.55.10", Prefix: "192.168.55.0/24"},
		{Interface: "office0", Index: 3, Address: "192.168.55.11", Prefix: "192.168.55.0/24"},
	}
	options := sshdiscovery.ServiceOptions{
		Interfaces: func() ([]sshdiscovery.InterfaceScope, error) { return scopes, nil },
		LookupAddr: func(context.Context, string) ([]string, error) { return nil, errors.New("no PTR") },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() { defer server.Close(); _, _ = io.WriteString(server, "SSH-2.0-OpenSSH\r\n") }()
			return client, nil
		},
	}
	service := sshdiscovery.NewService(nil, options)
	for _, scope := range scopes {
		report, err := service.LAN(context.Background(), sshdiscovery.LANRequest{Interface: scope.Interface, Ranges: []string{"192.168.55.20"}, Ports: []int{22}})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Candidates) != 1 {
			t.Fatal("fake endpoint was not discovered")
		}
		if err := sshdiscovery.WriteCache(context.Background(), sshDiscoveryCacheDir(), report); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{Out: io.Discard, Err: io.Discard, sshDiscoveryService: service}
	if candidate, err := resolveSSHDiscoveryTarget(context.Background(), app, "lan:192.168.55.20:22"); err == nil {
		t.Fatalf("ambiguous cached scope was selected implicitly: %#v", candidate)
	}
}
