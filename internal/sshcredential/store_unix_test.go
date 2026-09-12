//go:build unix

package sshcredential

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyRejectsLinksBroadModesAndUnexpectedSecretFields(t *testing.T) {
	for _, mode := range []string{"link", "hardlink", "broad", "secret_field", "parent_link"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t)
			c := testContext()
			if _, e := s.SavePreference(ctx, c, PolicyNever); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "link":
				moved := s.Path + ".old"
				if e := os.Rename(s.Path, moved); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(moved, s.Path); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(s.Path, s.Path+".link"); e != nil {
					t.Fatal(e)
				}
			case "broad":
				if e := os.Chmod(s.Path, 0o644); e != nil {
					t.Fatal(e)
				}
			case "secret_field":
				if e := os.WriteFile(s.Path, []byte("schema_version=1\nsecret='must reject'\n"), 0o600); e != nil {
					t.Fatal(e)
				}
			case "parent_link":
				dir := filepath.Dir(s.Path)
				if e := os.Rename(dir, dir+".old"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(dir+".old", dir); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.Read(ctx); !errors.Is(e, ErrUnsafe) {
				t.Fatal("unsafe policy accepted", e)
			}
		})
	}
}
func TestBrokerUsesPrivateSocketPath(t *testing.T) {
	b, e := NewBroker(context.Background(), []PasswordAnswer{{PasswordContext{"one", "user", []string{"host"}, 22}, []byte("secret")}})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	for path, want := range map[string]os.FileMode{filepath.Dir(b.endpoint): 0o700, b.endpoint: 0o600} {
		info, e := os.Lstat(path)
		if e != nil || info.Mode().Perm() != want {
			t.Fatalf("broker permissions %s %v", path, e)
		}
	}
}
