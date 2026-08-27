package gitx_test

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(p, content string) error {
	return os.WriteFile(p, []byte(content), 0o644)
}

// resolve mirrors the symlink resolution git applies to paths (on macOS
// t.TempDir() lives under /var, a symlink to /private/var), so path
// comparisons in tests compare like with like.
func resolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
