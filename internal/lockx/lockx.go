// Package lockx provides cross-platform advisory filesystem locks.
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
	"sync"
)

// Lease holds one acquired directory lock until Close. Close is idempotent.
type Lease struct {
	lock  *fileLock
	label string
	once  sync.Once
	err   error
}

// AcquireDir acquires the same cross-process lock used by WithDir. The lock file
// lives beside dir rather than inside it, so locking never adds a file to the
// directory being protected.
func AcquireDir(ctx context.Context, dir, label string) (*Lease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s directory for lock: %w", label, err)
	}
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("make %s directory absolute for lock: %w", label, err)
	}
	canonicalDir, err := filepath.EvalSymlinks(absoluteDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize %s directory for lock: %w", label, err)
	}
	lockPath := filepath.Join(filepath.Dir(canonicalDir), "."+filepath.Base(canonicalDir)+".lock")
	lock, err := acquire(ctx, lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire %s lock: %w", label, err)
	}
	return &Lease{lock: lock, label: label}, nil
}

// Close releases the acquired directory lock.
func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if err := l.lock.Close(); err != nil {
			l.err = fmt.Errorf("release %s lock: %w", l.label, err)
		}
	})
	return l.err
}

// WithDir runs operation while holding an exclusive lock for dir.
func WithDir(ctx context.Context, dir, label string, operation func() error) (err error) {
	if operation == nil {
		return fmt.Errorf("%s lock requires an operation", label)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lease, err := AcquireDir(ctx, dir, label)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

// WithFile runs operation while holding an explicit advisory lock file. Its
// parent must already exist; no directory is created as a side effect.
func WithFile(ctx context.Context, path, label string, operation func() error) (err error) {
	if operation == nil {
		return fmt.Errorf("%s lock requires an operation", label)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("make %s lock path absolute: %w", label, err)
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return fmt.Errorf("canonicalize %s lock parent: %w", label, err)
	}
	lockPath := filepath.Join(canonicalParent, filepath.Base(absolutePath))
	lock, err := acquire(ctx, lockPath)
	if err != nil {
		return fmt.Errorf("acquire %s lock: %w", label, err)
	}
	defer func() {
		if releaseErr := lock.Close(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release %s lock: %w", label, releaseErr))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}
