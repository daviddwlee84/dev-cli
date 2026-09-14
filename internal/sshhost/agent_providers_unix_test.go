//go:build unix

package sshhost

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPlatformAgentSocketRejectsRegularFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformAgentSocket(path, info); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("regular file accepted as agent socket: %v", err)
	}
}

type socketMetadataFixture struct {
	fs.FileInfo
	owner uint32
}

func (socketMetadataFixture) Mode() fs.FileMode { return fs.ModeSocket | 0o600 }
func (f socketMetadataFixture) Sys() any        { return &syscall.Stat_t{Uid: f.owner} }

func TestPlatformAgentSocketRequiresCurrentOwner(t *testing.T) {
	path := t.TempDir()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	owned := socketMetadataFixture{FileInfo: info, owner: uint32(os.Geteuid())}
	if err := platformAgentSocket(path, owned); err != nil {
		t.Fatal(err)
	}
	owned.owner++
	if err := platformAgentSocket(path, owned); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("foreign-owned socket accepted: %v", err)
	}
}

func TestExplicitAgentRejectsSymlinkedParentsBeforeInventory(t *testing.T) {
	f := newAgentKeyFixture(t)
	real := filepath.Join(f.service.paths.Home, "real-agent")
	writeFixture(t, filepath.Join(real, "agent.sock"), "")
	link := filepath.Join(f.service.paths.Home, "linked-agent")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ref := AgentSocketRef{Provider: AgentProviderCustom, Socket: filepath.Join(link, "agent.sock")}
	if _, err := f.service.ResolveAgentSocket("darwin", ref.Socket); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("custom path through symlink accepted: %v", err)
	}
	if _, err := f.service.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, Agents: []AgentSocketRef{ref}}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("explicit catalog path through symlink accepted: %v", err)
	}
	if _, err := f.service.SelectAgentKey(t.Context(), ref, f.fingerprint); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("exact key selection through symlink accepted: %v", err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("unsafe explicit socket was queried: %+v", f.requests)
	}
}
