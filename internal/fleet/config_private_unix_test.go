//go:build unix

package fleet

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixCheckPrivateModeRequiresPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.toml")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := configWithPlainPassword()
	if err := CheckPrivateMode(path, cfg); err != nil {
		t.Fatalf("mode 0600 rejected: %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateMode(path, cfg); err != nil {
		t.Fatalf("more restrictive mode 0400 rejected: %v", err)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateMode(path, cfg); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Fatalf("mode %04o error = %v", mode, err)
		}
	}
}

func TestUnixWritePrivateConfigFileIsAtomicAndRejectsForeignLinks(t *testing.T) {
	t.Run("create and overwrite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "remotes.toml")
		if err := WritePrivateConfigFile(path, []byte("first"), false); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("created mode = %v, err %v", info.Mode().Perm(), err)
		}
		if err := WritePrivateConfigFile(path, []byte("second"), true); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, []byte("second")) {
			t.Fatalf("overwritten bytes = %q, err %v", got, err)
		}
	})

	t.Run("symlink preserves foreign bytes", func(t *testing.T) {
		root := t.TempDir()
		foreign := filepath.Join(root, "foreign")
		path := filepath.Join(root, "remotes.toml")
		if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(foreign, path); err != nil {
			t.Fatal(err)
		}
		if err := WritePrivateConfigFile(path, []byte("replacement"), true); err == nil {
			t.Fatal("force overwrite accepted symlink")
		}
		got, err := os.ReadFile(foreign)
		if err != nil || !bytes.Equal(got, []byte("foreign")) {
			t.Fatalf("foreign symlink target changed: %q, err %v", got, err)
		}
	})

	t.Run("hard link preserves foreign bytes", func(t *testing.T) {
		root := t.TempDir()
		foreign := filepath.Join(root, "foreign")
		path := filepath.Join(root, "remotes.toml")
		if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(foreign, path); err != nil {
			t.Fatal(err)
		}
		if err := WritePrivateConfigFile(path, []byte("replacement"), true); err == nil {
			t.Fatal("force overwrite accepted hard link")
		}
		got, err := os.ReadFile(foreign)
		if err != nil || !bytes.Equal(got, []byte("foreign")) {
			t.Fatalf("foreign hard-link target changed: %q, err %v", got, err)
		}
	})

	t.Run("concurrent replacement wins", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "remotes.toml")
		if err := WritePrivateConfigFile(path, []byte("initial"), false); err != nil {
			t.Fatal(err)
		}
		concurrent := filepath.Join(root, "concurrent")
		if err := os.WriteFile(concurrent, []byte("concurrent"), 0o600); err != nil {
			t.Fatal(err)
		}
		privateConfigUnixBeforePublish = func(string) {
			if err := os.Rename(concurrent, path); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { privateConfigUnixBeforePublish = nil })
		err := WritePrivateConfigFile(path, []byte("replacement"), true)
		privateConfigUnixBeforePublish = nil
		if err == nil {
			t.Fatal("force overwrite replaced a concurrent object")
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, []byte("concurrent")) {
			t.Fatalf("concurrent target changed: %q, err %v (write err %v)", got, readErr, err)
		}
		entries, readDirErr := os.ReadDir(root)
		if readDirErr != nil {
			t.Fatal(readDirErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".dev-remotes-") {
				t.Fatalf("private stage leaked after conflict: %s", entry.Name())
			}
		}
	})
}

func configWithPlainPassword() Config {
	cfg := DefaultConfig()
	cfg.Hosts = []Host{{
		Name: "lab", SSHAlias: "lab",
		SSHLoginPasswordSource: PasswordSource{Type: "plain", Value: "secret"},
	}}
	return cfg
}
