package desktop

import (
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeTrashRoundTrip(t *testing.T) {
	if os.Getenv("DEV_TEST_NATIVE_TRASH") != "1" {
		t.Skip("opt-in native desktop smoke test")
	}
	for _, backend := range []string{"preferred", "foundation"} {
		t.Run(backend, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "dev-trash-test-"+uuid.NewString()+" 中文 ' literal")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "keep"), []byte("test bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			// The test source is on the home filesystem. Find and restore only its
			// exact file identity, never a similarly named user item.
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			trashDir := filepath.Join(home, ".Trash")
			t.Cleanup(func() {
				entries, err := os.ReadDir(trashDir)
				if err != nil {
					t.Errorf("inspect test Trash: %v", err)
					return
				}
				for _, entry := range entries {
					info, err := entry.Info()
					if err != nil || !os.SameFile(before, info) {
						continue
					}
					if err := os.Rename(filepath.Join(trashDir, entry.Name()), source); err != nil {
						t.Errorf("restore test directory: %v", err)
					}
					return
				}
			})
			if backend == "preferred" {
				err = trash(t.Context(), source)
			} else {
				_, err = exec.CommandContext(t.Context(), "/usr/bin/osascript", "-l", "JavaScript", "-e", trashScript, source).CombinedOutput()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatalf("source still present: %v", err)
			}
			entries, err := os.ReadDir(trashDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil || !os.SameFile(before, info) {
					continue
				}
				body, err := os.ReadFile(filepath.Join(trashDir, entry.Name(), "keep"))
				if err != nil || string(body) != "test bytes" {
					t.Fatalf("Trash bytes=%q: %v", body, err)
				}
				return
			}
			t.Fatal("test item was not found in native Trash")
		})
	}
}
