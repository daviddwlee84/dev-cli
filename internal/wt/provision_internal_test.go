package wt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFileRejectsSourceReplacedAfterLstat(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "settings.local.json")
	outside := filepath.Join(dir, "outside.json")
	dest := filepath.Join(dir, "copied.json")
	if err := os.WriteFile(source, []byte(`{"backend":"expected"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(`{"secret":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}

	err = copyFile(source, dest, expected)
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("copyFile should reject the swapped source, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination should not be created after source swap: %v", err)
	}
}
