package submodule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestCleanupRegressionNestedPlaceholderIdentitySurvivesParentMove(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "same directory"
		if replace {
			name = "replacement must be rejected"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			parent := filepath.Join(base, "parent")
			placeholder := filepath.Join(parent, "leaf")
			if err := os.MkdirAll(placeholder, 0o755); err != nil {
				t.Fatal(err)
			}
			info, err := captureRemovalPlaceholder(placeholder)
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(base, "quarantine")
			if err := os.Rename(parent, moved); err != nil {
				t.Fatal(err)
			}
			if replace {
				// Retain the original inode/file ID to prevent an allocator from
				// reusing it for the replacement directory.
				if err := os.Rename(filepath.Join(moved, "leaf"), filepath.Join(moved, "retained")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(moved, "leaf"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyStagedPlaceholders(t.Context(), map[string]os.FileInfo{placeholder: info}, []moveRecord{{From: parent, To: moved}})
			if replace && !errors.Is(err, safefile.ErrChanged) {
				t.Fatalf("replacement identity = %v, want filesystem change", err)
			}
			if !replace && err != nil {
				t.Fatalf("same directory moved with its parent: %v", err)
			}
		})
	}
}
