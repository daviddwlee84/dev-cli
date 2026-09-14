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
	s := newFixtureService(t, paths, DiscoverOptions{})
	path := filepath.Join(paths.Home, "not-a-pipe")
	writeFixture(t, path, "")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformAgentSocket(path, info); !errors.Is(err, ErrManualRemediation) {
		t.Fatalf("regular Windows file accepted as an agent: %v", err)
	}
	if _, err := s.ResolveAgentSocket("windows", path); !errors.Is(err, ErrManualRemediation) {
		t.Fatalf("unattested Windows path resolved: %v", err)
	}
	if _, err := s.SelectAgentKey(t.Context(), AgentSocketRef{Socket: windowsOpenSSHAgentPipe}, "SHA256:unknown"); !errors.Is(err, ErrManualRemediation) {
		t.Fatalf("unattested Windows pipe queried: %v", err)
	}
	if got := s.ObserveAgentProviders("windows"); len(got) != 0 {
		t.Fatalf("unattested Windows pipe offered: %+v", got)
	}
}
