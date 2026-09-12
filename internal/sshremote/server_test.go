package sshremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type testSSHRunner struct {
	calls       []sshhost.RunRequest
	interactive int
	exitCode    int
	policy      string
}

func (r *testSSHRunner) Run(_ context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	r.calls = append(r.calls, request)
	if request.Name == "ssh" && len(request.Args) > 0 && request.Args[0] == "-G" {
		alias := request.Args[len(request.Args)-1]
		return sshhost.RunResult{Stdout: []byte(fmt.Sprintf("hostname %s.example\nuser remote\nport 22\nproxyjump none\nidentityfile ~/.ssh/id_ed25519\nidentitiesonly no\nstricthostkeychecking ask\nuserknownhostsfile /tmp/known_hosts\nglobalknownhostsfile /etc/ssh/ssh_known_hosts\n%s", alias, r.policy))}, nil
	}
	if request.Name == "ssh-add" {
		return sshhost.RunResult{ExitCode: 1}, nil
	}
	if request.Name == "ssh" && request.Interactive {
		r.interactive++
		return sshhost.RunResult{ExitCode: r.exitCode}, nil
	}
	return sshhost.RunResult{}, fmt.Errorf("unexpected subprocess %s", request.Name)
}

func testServer(t *testing.T, config string) (*Server, *testSSHRunner, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := sshhost.NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	runner := &testSSHRunner{}
	service, err := sshhost.NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(service)
	server.Identity = machineid.NewStore(filepath.Join(dir, "identity", "identity.json"))
	server.User = "test-user"
	server.Platform = "linux"
	return server, runner, path
}
func testCapability(t *testing.T, server *Server) Capability {
	t.Helper()
	capability, err := server.Capability(context.Background(), CapabilityRequest{Header: NewHeader()})
	if err != nil {
		t.Fatal(err)
	}
	return capability
}
func testInventory(t *testing.T, server *Server) Inventory {
	t.Helper()
	capability := testCapability(t, server)
	inventory, err := server.Inventory(context.Background(), NewRequest(capability.Origin))
	if err != nil {
		t.Fatal(err)
	}
	return inventory
}

func TestCapabilityIsOnlyIdentityInitializationPath(t *testing.T) {
	server, runner, _ := testServer(t, "Host target\n HostName target.example\n")
	wrong := CapabilityRequest{Header: Header{SchemaVersion: 1, ProtocolVersion: 99}}
	if _, err := server.Capability(context.Background(), wrong); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("wrong version: %v", err)
	}
	origin := Origin{MachineID: "11111111-1111-4111-8111-111111111111", Platform: "linux", User: server.User, Root: server.SSH.Paths().RootConfig}
	origin.ID = OriginID(origin.MachineID, origin.User, origin.Root)
	if _, err := server.Inventory(context.Background(), NewRequest(origin)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("inventory before capability: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(server.Identity.Path)); !os.IsNotExist(err) {
		t.Fatalf("read initialized identity directory: %v", err)
	}
	capability := testCapability(t, server)
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Identity.Load(); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("capability invoked SSH or agent")
	}
}

func TestInventoryStaticProjectionAndStableProfileID(t *testing.T) {
	server, runner, path := testServer(t, "Host target\n HostName target.example\nMatch exec \"never-export-this-command\"\n User altered\n")
	inventory := testInventory(t, server)
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "never-export") {
		t.Fatal("raw Match command escaped projection")
	}
	if len(runner.calls) != 0 {
		t.Fatal("inventory invoked a subprocess")
	}
	profile, ok := inventory.Find("target")
	if !ok {
		t.Fatal("target missing")
	}
	if err := os.WriteFile(path, []byte("Host target\n HostName replacement.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := testInventory(t, server)
	changed, _ := after.Find("target")
	if profile.ID != changed.ID || profile.Fingerprint == changed.Fingerprint {
		t.Fatalf("profile identity/revision conflated: %#v %#v", profile, changed)
	}
	other := inventory.Origin
	other.User = "different-user"
	other.ID = OriginID(other.MachineID, other.User, other.Root)
	if ProfileID(other.ID, "target") == profile.ID {
		t.Fatal("accounts share source profile identity")
	}
}

func TestResolveRevalidatesSourceAndDoesNotExportCommands(t *testing.T) {
	server, runner, path := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	profile, _ := inventory.Find("target")
	request := ResolveRequest{Request: NewRequest(inventory.Origin), Selection: profile.Selection(inventory.Origin)}
	runner.policy = "localcommand must-never-be-exported\n"
	resolved, err := server.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Importable || len(resolved.ImportDiagnostics) == 0 {
		t.Fatal("source-local command was importable")
	}
	encoded, _ := json.Marshal(resolved)
	if strings.Contains(string(encoded), "must-never") || resolved.Effective.Values != nil {
		t.Fatal("native command/config map was exported")
	}
	runner.calls = nil
	if err := os.WriteFile(path, []byte("Host target\n HostName changed.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Resolve(context.Background(), request); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("stale source resolve: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("stale selection ran SSH before refusal")
	}
}

func TestRemoteConnectRunsOnceWithoutExtraCommandOrAgentForwarding(t *testing.T) {
	server, runner, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	profile, _ := inventory.Find("target")
	runner.exitCode = 255
	request := ConnectRequest{Request: NewRequest(inventory.Origin), Selection: profile.Selection(inventory.Origin)}
	result, err := server.Connect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 255 || runner.interactive != 1 {
		t.Fatalf("connect result=%+v attempts=%d", result, runner.interactive)
	}
	last := runner.calls[len(runner.calls)-1]
	if last.Args[len(last.Args)-1] != "target" || len(last.Stdin) != 0 {
		t.Fatalf("connect has an extra command: %#v", last.Args)
	}
	for _, required := range []string{"ForwardAgent=no"} {
		found := false
		for _, arg := range last.Args {
			found = found || arg == required
		}
		if !found {
			t.Errorf("missing %s", required)
		}
	}
	for _, arg := range last.Args {
		if arg == "ClearAllForwardings=yes" || arg == "ForwardX11=no" || arg == "PermitLocalCommand=no" {
			t.Fatalf("ordinary inner connection overrides native policy through %s", arg)
		}
	}
}

func TestSelectedKeyMustBeResolvedOnRemote(t *testing.T) {
	server, runner, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	profile, _ := inventory.Find("target")
	request := ConnectRequest{Request: NewRequest(inventory.Origin), Selection: profile.Selection(inventory.Origin), KeyID: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if _, err := server.Connect(context.Background(), request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing remote key: %v", err)
	}
	if runner.interactive != 0 {
		t.Fatal("missing remote key connected")
	}
}

func TestOpaqueRemoteProfileConnectsNativelyButCannotResolveForImport(t *testing.T) {
	server, runner, _ := testServer(t, "Host target\n HostName target.example\n ProxyCommand route-tool %h %p\n")
	runner.policy = "proxycommand route-tool %h %p\n"
	inventory := testInventory(t, server)
	profile, found := inventory.Find("target")
	if !found {
		t.Fatal("opaque exact alias missing from static inventory")
	}
	selection := profile.Selection(inventory.Origin)
	if _, err := server.Resolve(context.Background(), ResolveRequest{Request: NewRequest(inventory.Origin), Selection: selection}); !errors.Is(err, sshhost.ErrUnsupportedRoute) {
		t.Fatalf("opaque route was reconstructed: %v", err)
	}
	result, err := server.Connect(context.Background(), ConnectRequest{Request: NewRequest(inventory.Origin), Selection: selection})
	if err != nil || result.ExitCode != 0 || runner.interactive != 1 {
		t.Fatalf("native-only connection: result=%+v err=%v attempts=%d", result, err, runner.interactive)
	}
}

func TestSelectorsAndConnectWireRejectAmbiguity(t *testing.T) {
	selector := EncodeSelector("lab / staging, one", "nested/alias:22")
	host, alias, err := ParseSelector(selector)
	if err != nil || host != "lab / staging, one" || alias != "nested/alias:22" {
		t.Fatalf("selector roundtrip %q %q %v", host, alias, err)
	}
	for _, bad := range []string{"fleet:host/a/b", "fleet:host/%xx", "fleet:/alias", "fleet:host/-oProxyCommand=bad"} {
		if _, _, err := ParseSelector(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	profile, _ := inventory.Find("target")
	request := ConnectRequest{Request: NewRequest(inventory.Origin), Selection: profile.Selection(inventory.Origin)}
	encoded, err := EncodeConnectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeConnectRequest(encoded)
	if err != nil || decoded != request {
		t.Fatalf("connect roundtrip %v", err)
	}
	request.SchemaVersion = 2
	if _, err := EncodeConnectRequest(request); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("accepted version: %v", err)
	}
}

func TestResolvedAllowsDistinctInvocationProfilesForSameAlias(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	profile, _ := inventory.Find("target")
	resolved := Resolved{Header: NewHeader(), Kind: "ssh_remote_resolved", Origin: inventory.Origin, Profile: profile, ObservedAt: time.Now(), Effective: sshhost.EffectiveConfig{Alias: "target", HostName: "target.example", User: "second", Port: 22}, Route: sshhost.Route{Alias: "target", Hops: []sshhost.RouteHop{{Alias: "target", Reference: "first@target", HostName: "target.example", User: "first", Port: 22}, {Alias: "target", Reference: "second@target", HostName: "target.example", User: "second", Port: 22, Target: true}}}, Importable: true}
	for _, hop := range resolved.Route.Hops {
		resolved.RouteProfiles = append(resolved.RouteProfiles, RouteProfile{OriginID: inventory.Origin.ID, ProfileID: profile.ID, Hop: hop})
	}
	if err := resolved.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved.Effective.HostName = "different.example"
	if err := resolved.Validate(); err == nil {
		t.Fatal("preview endpoint disagrees with executed route endpoint")
	}
	resolved.Effective.HostName = "target.example"
	resolved.Route.Hops[1].User = "first"
	resolved.Effective.User = "first"
	resolved.RouteProfiles[1].Hop = resolved.Route.Hops[1]
	if err := resolved.Validate(); err == nil {
		t.Fatal("duplicate invocation accepted")
	}
}

func TestRemoteKeyMetadataRejectsInjectedSourceText(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	capability := testCapability(t, server)
	keys := Keys{Header: NewHeader(), Kind: "ssh_remote_keys", Origin: capability.Origin, ObservedAt: time.Now(), Catalog: sshhost.KeyCatalog{Complete: true, Candidates: []sshhost.KeyCandidate{{Algorithm: "ssh-ed25519", Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Source: sshhost.KeySourceAgent}}}}
	if err := keys.Validate(); err != nil {
		t.Fatal(err)
	}
	keys.Catalog.Candidates[0].Sources = []sshhost.KeySource{"agent\x1b[2J"}
	if err := keys.Validate(); err == nil {
		t.Fatal("remote source label could inject terminal controls")
	}
}
