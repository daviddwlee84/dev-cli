// Package lockx provides a cross-platform advisory lock over a directory.
//
// It is a leaf package on purpose. Both the catalog store and Git transaction
// code need to serialize mutations, and neither should have to depend on the
// other to get a lock.
package lockx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WithDir runs operation while holding an exclusive lock for dir. The lock file
// lives beside dir rather than inside it, so locking never adds a file to the
// directory being protected. label names the caller in wrapped errors.
func WithDir(ctx context.Context, dir, label string, operation func() error) (err error) {
	if operation == nil {
		return fmt.Errorf("%s lock requires an operation", label)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s directory for lock: %w", label, err)
	}
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("make %s directory absolute for lock: %w", label, err)
	}
	canonicalDir, err := filepath.EvalSymlinks(absoluteDir)
	if err != nil {
		return fmt.Errorf("canonicalize %s directory for lock: %w", label, err)
	}
	lockPath := filepath.Join(filepath.Dir(canonicalDir), "."+filepath.Base(canonicalDir)+".lock")
	lock, err := acquire(ctx, lockPath)
	if err != nil {
		return fmt.Errorf("acquire %s lock: %w", label, err)
	}
	defer func() {
		if releaseErr := lock.Close(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release %s lock: %w", label, releaseErr))
		}
	}()
	return operation()
}
