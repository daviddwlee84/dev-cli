//go:build unix

package machineregistry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsafeModesAndLinksAreRejectedWithoutRepair(t *testing.T) {
	for _, kind := range []string{"directory-mode", "database-mode", "database-link", "database-hardlink", "parent-link", "lock-link", "journal-link"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
			plan, err := store.Plan(t.Context(), Request{Action: "link", MachineID: firstID, Bindings: []Binding{binding("work")}})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory-mode":
				err = os.Chmod(filepath.Dir(store.Path), 0o755)
			case "database-mode":
				err = os.Chmod(store.Path, 0o644)
			case "database-link":
				if err = os.Rename(store.Path, store.Path+".real"); err == nil {
					err = os.Symlink(store.Path+".real", store.Path)
				}
			case "database-hardlink":
				err = os.Link(store.Path, store.Path+".other")
			case "parent-link":
				dir := filepath.Dir(store.Path)
				if err = os.Rename(dir, dir+".real"); err == nil {
					err = os.Symlink(dir+".real", dir)
				}
			case "lock-link":
				path := filepath.Join(filepath.Dir(store.Path), ".registry.lock")
				if err = os.Remove(path); err == nil {
					err = os.Symlink(store.Path, path)
				}
			case "journal-link":
				err = os.Symlink(store.Path, store.Path+"-journal")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Apply(t.Context(), plan); !errors.Is(err, ErrUnsafePath) && !errors.Is(err, ErrStale) {
				t.Fatalf("unsafe path accepted: %v", err)
			}
			if kind == "directory-mode" || kind == "database-mode" {
				path, mode := store.Path, os.FileMode(0o644)
				if kind == "directory-mode" {
					path, mode = filepath.Dir(store.Path), 0o755
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != mode {
					t.Fatal("unsafe preexisting permissions were silently changed")
				}
			}
		})
	}
}
