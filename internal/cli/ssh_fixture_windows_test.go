//go:build windows

package cli

import (
	"os"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/privatefile"
)

func protectSSHFixture(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o600)
	if info.IsDir() {
		mode = 0o700
	}
	if err = privatefile.ProtectCreated(path, mode); err != nil {
		t.Fatal(err)
	}
}
