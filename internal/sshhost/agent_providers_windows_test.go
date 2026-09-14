//go:build windows

package sshhost

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitWindowsAgentsFailClosedWithoutPipeAttestation(t *testing.T) {
	paths := fixturePaths(t)
	s := newFixtureService(t, paths, DiscoverOptions{}) // Any runner invocation panics.
	path := filepath.Join(paths.Home, "not-a-pipe")
	writeFixture(t, path, "unchanged fixture")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformAgentSocket(path, info); !errors.Is(err, ErrManualRemediation) {
		t.Fatalf("regular Windows file accepted as an agent: %v", err)
	}

	// Use token-free native syntax to exercise attestation refusal itself. The
	// CI temporary path can contain RUNNER~1 and correctly fail syntax earlier.
	for _, socket := range []string{windowsOpenSSHAgentPipe, `C:\dev-agent-fixture\agent.sock`} {
		if err := ValidateAgentSocketPath(socket); err != nil {
			t.Fatalf("invalid test path %q: %v", socket, err)
		}
		if _, err := s.ResolveAgentSocket("windows", socket); !errors.Is(err, ErrManualRemediation) {
			t.Fatalf("unattested Windows path resolved: %v", err)
		}
		ref := AgentSocketRef{Provider: AgentProviderCustom, Socket: socket}
		if _, err := s.SelectAgentKey(t.Context(), ref, "SHA256:unknown"); !errors.Is(err, ErrManualRemediation) {
			t.Fatalf("unattested Windows agent queried: %v", err)
		}
		inventory, err := s.ObserveAgentKeys(t.Context(), ref)
		if !errors.Is(err, ErrManualRemediation) || inventory.Complete || inventory.state != nil {
			t.Fatalf("unattested Windows baseline=%+v err=%v", inventory, err)
		}
		catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, Agents: []AgentSocketRef{ref}})
		if !errors.Is(err, ErrManualRemediation) || catalog.Complete || len(catalog.Candidates) != 0 {
			t.Fatalf("unattested Windows catalog=%+v err=%v", catalog, err)
		}
		if _, err := s.RefreshAgentKeys(t.Context(), AgentInventory{Agent: ref, Complete: true}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("fabricated Windows baseline refreshed: %v", err)
		}
	}
	if got := s.ObserveAgentProviders("windows"); len(got) != 0 {
		t.Fatalf("unattested Windows pipe offered: %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "unchanged fixture" {
		t.Fatalf("agent refusal modified a file: %v", err)
	}
	entries, err := os.ReadDir(paths.SSHDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("agent refusal wrote SSH files: %v %v", entries, err)
	}
}

func TestWindowsAgentTokenPathsFailBeforeAttestation(t *testing.T) {
	s := newFixtureService(t, fixturePaths(t), DiscoverOptions{})
	for _, path := range []string{`C:\Users\RUNNER~1\agent.sock`, `C:\Users\%USERNAME%\agent.sock`, `C:\Users\${USER}\agent.sock`, `~\agent.sock`, "relative.sock"} {
		if err := ValidateAgentSocketPath(path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("unsafe agent syntax accepted: %q %v", path, err)
		}
		if _, err := s.ResolveAgentSocket("windows", path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("unsafe path reached attestation: %q %v", path, err)
		}
	}
}
