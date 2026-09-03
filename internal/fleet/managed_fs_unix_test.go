//go:build !windows

package fleet

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultManagedFragmentWriterModesIdempotencyAndRemoval(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	path, err := WriteManagedFragment(primary, host, nil)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("managed modes = directory %04o file %04o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	fixedMtime := time.Unix(1_700_000_100, 0)
	if err := os.Chtimes(path, fixedMtime, fixedMtime); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteManagedFragment(primary, host, nil); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.ModTime().Equal(fixedMtime) {
		t.Fatalf("idempotent write changed mtime from %v to %v", fixedMtime, unchanged.ModTime())
	}

	host.Name = "renamed-profile"
	if _, err := WriteManagedFragment(primary, host, nil); err != nil {
		t.Fatal(err)
	}
	parsed, err := ValidateManagedFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != host {
		t.Fatalf("updated managed host = %+v, want %+v", parsed, host)
	}
	if _, err := RemoveManagedFragment(primary, host.SSHAlias, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed fragment still exists: %v", err)
	}
}

func TestLoadConfigRejectsInsecureManagedModes(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		dir  bool
	}{
		{name: "file", mode: 0o644},
		{name: "directory", mode: 0o755, dir: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := filepath.Join(t.TempDir(), "remotes.toml")
			path := writeManagedFixture(t, primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX})
			insecurePath := path
			if test.dir {
				insecurePath = filepath.Dir(path)
			}
			if err := os.Chmod(insecurePath, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(primary); err == nil || !strings.Contains(err.Error(), "mode") {
				t.Fatalf("LoadConfig error = %v, want private mode rejection", err)
			}
		})
	}
}

func TestLoadConfigRejectsManagedSymlink(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	path := writeManagedFixture(t, primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX})
	target := filepath.Join(t.TempDir(), "target.toml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(primary); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadConfig error = %v, want symlink rejection", err)
	}
}
