package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupRegressionArtifactStoreMissingDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("not an empty intent store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		path    string
		missing bool
		bad     bool
	}{
		{name: "existing empty directory", path: root},
		{name: "missing directory", path: filepath.Join(root, "missing"), missing: true},
		{name: "missing parents", path: filepath.Join(root, "missing", "nested"), missing: true},
		{name: "ordinary file", path: file, bad: true},
		{name: "file ancestor", path: filepath.Join(file, "nested"), bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing, err := missingStoreDirectory(tc.path)
			if (err != nil) != tc.bad || missing != tc.missing {
				t.Fatalf("absence classification: missing=%v err=%v", missing, err)
			}
			intents, err := NewStore(tc.path).List()
			if (err != nil) != tc.bad || len(intents) != 0 {
				t.Fatalf("store list: %v, %v", intents, err)
			}
		})
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "not an empty intent store\n" {
		t.Fatal("store inspection changed the non-directory target", err)
	}
}
