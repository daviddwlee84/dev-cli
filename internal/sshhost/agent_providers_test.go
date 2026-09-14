package sshhost

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Temporary directories are too long for real Unix sockets on macOS, so provider
// tests accept regular files as sockets; platformAgentSocket is tested separately.
func acceptFileAgentSockets(s *Service) {
	s.agentSocketCheck = func(_ string, info fs.FileInfo) error {
		if !info.Mode().IsRegular() {
			return ErrUnsafePath
		}
		return nil
	}
}

func TestObserveAgentProvidersStatsKnownSocketsPerOS(t *testing.T) {
	paths := fixturePaths(t)
	s := newFixtureService(t, paths, DiscoverOptions{})
	acceptFileAgentSockets(s)
	s.agentSystemRoot = t.TempDir()
	if got := s.ObserveAgentProviders("darwin"); len(got) != 0 {
		t.Fatalf("empty home observed providers: %+v", got)
	}
	bitwarden := filepath.Join(paths.Home, ".bitwarden-ssh-agent.sock")
	writeFixture(t, bitwarden, "")
	onePassword := filepath.Join(paths.Home, "Library", "Group Containers", "2BUA8C4S2C.com.1password", "t", "agent.sock")
	writeFixture(t, onePassword, "")
	if err := os.MkdirAll(filepath.Join(s.agentSystemRoot, "Applications", "Secretive.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []AgentProviderObservation{
		{Provider: AgentProviderBitwarden, Label: "Bitwarden", Socket: bitwarden, SocketPresent: true},
		{Provider: AgentProvider1Password, Label: "1Password", Socket: onePassword, SocketPresent: true},
		{Provider: AgentProviderSecretive, Label: "Secretive", Socket: filepath.Join(paths.Home, "Library", "Containers", "com.maxgoedjen.Secretive.SecretAgent", "Data", "socket.ssh"), AppDetected: true},
	}
	if got := s.ObserveAgentProviders("darwin"); !reflect.DeepEqual(got, want) {
		t.Fatalf("darwin observations=%+v", got)
	}
	if got := s.ObserveAgentProviders("linux"); len(got) != 1 || got[0].Provider != AgentProviderBitwarden || !got[0].SocketPresent {
		t.Fatalf("linux observations=%+v", got)
	}
	if observations := s.ObserveAgentProviders("windows"); len(observations) != 0 {
		t.Fatalf("unattested Windows pipes must not be offered: %+v", observations)
	}

	ref, err := s.ResolveAgentSocket("darwin", "1Password")
	if err != nil || ref != (AgentSocketRef{Provider: AgentProvider1Password, Socket: onePassword}) {
		t.Fatalf("ref=%+v err=%v", ref, err)
	}
	if _, err := s.ResolveAgentSocket("darwin", "secretive"); !errors.Is(err, ErrAgentProviderUnavailable) {
		t.Fatalf("installed provider without a socket resolved: %v", err)
	}
	if custom, err := s.ResolveAgentSocket("darwin", bitwarden); err != nil || custom.Provider != AgentProviderCustom {
		t.Fatalf("custom socket=%+v err=%v", custom, err)
	}
	for _, unsafe := range []string{"relative.sock", "~/agent.sock", "/tmp/%d/agent.sock", "/tmp/../agent.sock"} {
		if _, err := s.ResolveAgentSocket("darwin", unsafe); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("%s accepted: %v", unsafe, err)
		}
	}

	target := filepath.Join(paths.Home, "real-1password")
	writeFixture(t, filepath.Join(target, "agent.sock"), "")
	linked := filepath.Join(paths.Home, ".1password")
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Remove(bitwarden); err != nil {
		t.Fatal(err)
	}
	if got := s.ObserveAgentProviders("linux"); len(got) != 0 {
		t.Fatalf("socket below a symlinked directory was trusted: %+v", got)
	}
}

func TestCatalogRequestedAgentPrecedenceAndPublicationWithoutPrivateBytes(t *testing.T) {
	paths := fixturePaths(t)
	ambient := filepath.Join(paths.Home, "ambient.sock")
	bitwarden := filepath.Join(paths.Home, "Library", "Group Containers", "bw agent.sock")
	t.Setenv("SSH_AUTH_SOCK", ambient)
	shared, vault, ambientOnly := testPublicLine(0xa1, "shared"), testPublicLine(0xa2, "vault"), testPublicLine(0xa3, "ambient")
	runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Name != "ssh-add" {
			t.Fatalf("catalog or publication executed %s", r.Name)
		}
		switch r.Env[len(r.Env)-1] {
		case "SSH_AUTH_SOCK=" + ambient:
			return RunResult{Stdout: []byte(string(shared) + "\n" + string(ambientOnly) + "\n")}, nil
		case "SSH_AUTH_SOCK=" + bitwarden:
			return RunResult{Stdout: []byte(string(shared) + "\n" + string(vault) + "\n")}, nil
		}
		return RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}, nil
	})
	s, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	acceptFileAgentSockets(s)
	writeFixture(t, bitwarden, "")
	ref := AgentSocketRef{Provider: AgentProviderBitwarden, Socket: bitwarden}
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, Agents: []AgentSocketRef{ref}})
	if err != nil || len(catalog.Candidates) != 3 || !hasDiagnostic(catalog.Diagnostics, "agent_key_duplicate") || !catalog.Complete {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	byComment := map[string]KeyCandidate{}
	for _, candidate := range catalog.Candidates {
		byComment[candidate.Comment] = candidate
	}
	if a := byComment["shared"].Agent; a == nil || *a != ref {
		t.Fatalf("requested agent did not outrank SSH_AUTH_SOCK: %+v", byComment["shared"])
	}
	if byComment["vault"].Agent == nil || byComment["ambient"].Agent != nil {
		t.Fatalf("agent attribution=%+v", catalog.Candidates)
	}
	if _, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, Agents: []AgentSocketRef{{Socket: "relative.sock"}}}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("unsafe requested socket accepted: %v", err)
	}

	destination := s.DefaultAgentPublicPath(ref.Provider, "lab")
	if destination != filepath.Join(paths.SSHDir, "dev_agent_bitwarden_lab.pub") {
		t.Fatal(destination)
	}
	if _, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyPublishAgent, Candidate: byComment["ambient"], PublicDestination: destination}); err == nil {
		t.Fatal("ambient agent key was published without an explicit agent")
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyPublishAgent, Candidate: byComment["vault"], PublicDestination: destination})
	if err != nil || plan.Action != ActionCreate || plan.Agent == nil || *plan.Agent != ref || plan.IdentityFile != destination {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if err := s.RevalidateKeySelection(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplyKey(t.Context(), plan)
	if err != nil || !result.Created || result.Candidate.PublicPath != destination || result.Candidate.state.agent == nil || result.Candidate.state.agent.socket != bitwarden {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if metadata, err := ParsePublicKey(data); err != nil || metadata.Fingerprint != byComment["vault"].Fingerprint {
		t.Fatalf("published public key=%q err=%v", data, err)
	}
	again, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyPublishAgent, Candidate: byComment["vault"], PublicDestination: destination})
	if err != nil || again.Action != ActionNoop {
		t.Fatalf("identical publication is not a no-op: %+v %v", again, err)
	}
	writeFixture(t, destination, string(shared)+"\n")
	collision, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyPublishAgent, Candidate: byComment["vault"], PublicDestination: destination})
	if err != nil || collision.Action != ActionBlocked || !hasDiagnostic(collision.Diagnostics, "key_collision") {
		t.Fatalf("different public key was overwritten: %+v %v", collision, err)
	}
}

func TestManagedV2OnlyForAgentDirectivesAndVerifiesEffectiveAgent(t *testing.T) {
	yes := true
	v1 := ManagedDefinition{Alias: "lab", HostName: "lab.example", IdentityFile: "/home/u/.ssh/id", IdentitiesOnly: &yes}
	plain, err := RenderManaged(v1)
	if err != nil || !strings.HasPrefix(string(plain), ManagedHeader+"\n") {
		t.Fatalf("v1 rendering changed: %q %v", plain, err)
	}
	v2 := v1
	v2.IdentityAgent = filepath.Join(fixturePaths(t).Home, "Library", "Group Containers", "agent.sock")
	v2.SecurityKeyProvider = "internal"
	data, err := RenderManaged(v2)
	want := ManagedHeaderV2 + "\nHost lab\n    HostName lab.example\n    IdentityFile /home/u/.ssh/id\n    IdentitiesOnly yes\n    IdentityAgent " + quoteConfigValue(v2.IdentityAgent) + "\n    SecurityKeyProvider internal\n"
	if err != nil || string(data) != want {
		t.Fatalf("v2 rendering=%q err=%v", data, err)
	}
	if parsed, err := ParseManaged(data); err != nil || !reflect.DeepEqual(parsed, v2) {
		t.Fatalf("v2 round trip=%+v err=%v", parsed, err)
	}
	for _, drift := range []string{
		strings.Replace(string(data), ManagedHeaderV2, ManagedHeader, 1),
		strings.Replace(string(plain), ManagedHeader, ManagedHeaderV2, 1),
	} {
		if _, err := ParseManaged([]byte(drift)); !errors.Is(err, ErrNotManaged) {
			t.Fatalf("non-canonical header accepted: %q %v", drift, err)
		}
	}
	for _, agent := range []string{"~/agent.sock", "relative.sock", "/tmp/%d/agent.sock", "none"} {
		invalid := v1
		invalid.IdentityAgent = agent
		if ValidateManagedDefinition(invalid) == nil {
			t.Fatalf("IdentityAgent %q accepted", agent)
		}
	}
	effective := EffectiveConfig{Alias: "lab", HostName: "lab.example", IdentityFiles: []string{"/home/u/.ssh/id"}, IdentitiesOnly: &yes,
		Values: map[string][]string{"identityagent": {v2.IdentityAgent}, "securitykeyprovider": {"internal"}}}
	if err := VerifyManagedEffective(v2, effective); err != nil {
		t.Fatal(err)
	}
	effective.Values["identityagent"] = []string{"/other/agent.sock"}
	if err := VerifyManagedEffective(v2, effective); err == nil || !strings.Contains(err.Error(), "IdentityAgent") {
		t.Fatalf("different effective agent accepted: %v", err)
	}
}

func TestPublishedNamedAgentKeyBootstrapsWhenAliasUsesThatAgent(t *testing.T) {
	paths := fixturePaths(t)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(paths.Home, "different-ambient.sock"))
	socket := filepath.Join(paths.Home, "bitwarden agent.sock")
	line := testPublicLine(0xa4, "vault only")
	destination := filepath.Join(paths.SSHDir, "dev_agent_bitwarden_target.pub")
	runner := keyCatalogRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Name == "ssh-add" {
			if r.Env[len(r.Env)-1] == "SSH_AUTH_SOCK="+socket {
				return RunResult{Stdout: append(append([]byte{}, line...), '\n')}, nil
			}
			return RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}, nil
		}
		if r.Display == "ssh selected-key-only authentication proof" {
			data, err := os.ReadFile(sshArgForCatalogTest(r.Args, "-F"))
			if err != nil {
				t.Fatal(err)
			}
			if !proofDirectiveEquals(data, "IdentityAgent", socket) || !strings.Contains(string(data), destination) {
				t.Fatalf("proof did not select the published agent key: %s", data)
			}
			if err := os.WriteFile(sshArgForCatalogTest(r.Args, "-E"), []byte("Authenticated to target using \"publickey\".\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return RunResult{}, nil
	})
	s, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	acceptFileAgentSockets(s)
	writeFixture(t, socket, "")
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, Agents: []AgentSocketRef{{Provider: AgentProviderBitwarden, Socket: socket}}})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyPublishAgent, Candidate: catalog.Candidates[0], PublicDestination: destination})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.ApplyKey(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	route.state.hops[0].effective.Values["identityagent"] = []string{socket}
	result, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Route: route, Key: key})
	if err != nil || !result.Ready {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("published public key was not retained: %v", err)
	}
}
