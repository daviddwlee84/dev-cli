package gitx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

// WithLifecycleLock acquires containing repository gates before a child gate.
// Absorbed submodules therefore cannot start a dev lifecycle transaction while
// their owning superproject is being retired. The task-store lock comes later.
func WithLifecycleLock(ctx context.Context, commonDir string, operation func() error) error {
	var dirs []string
	identities := map[string]os.FileInfo{}
	for p := filepath.Clean(commonDir); ; p = filepath.Dir(p) {
		if info, err := os.Stat(filepath.Join(p, "objects")); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "config")); err == nil {
				dirs = append(dirs, p)
				identity, err := os.Stat(p)
				if err != nil {
					return err
				}
				identities[p] = identity
			}
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if len(dirs) == 0 {
		dirs = append(dirs, commonDir)
	}
	var lock func(int) error
	lock = func(i int) error {
		if i < 0 {
			if _, err := os.Stat(filepath.Join(commonDir, "objects")); err != nil {
				return fmt.Errorf("Git common directory disappeared before mutation: %w", err)
			}
			return operation()
		}
		if expected := identities[dirs[i]]; expected != nil {
			current, err := os.Stat(dirs[i])
			if err != nil || !os.SameFile(expected, current) {
				return fmt.Errorf("Git directory identity changed before lock: %s", dirs[i])
			}
		}
		return lockx.WithDir(ctx, filepath.Join(dirs[i], "dev-taskflow"), "taskflow repository", func() error { return lock(i - 1) })
	}
	return lock(len(dirs) - 1)
}
