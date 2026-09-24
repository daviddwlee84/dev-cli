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
	return acquireDir(ctx, dir, label, false)
}

func acquireDir(ctx context.Context, dir, label string, movable bool) (*Lease, error) {
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
	var directoryIdentity os.FileInfo
	if movable {
		directoryIdentity, err = os.Stat(canonicalDir)
		if err != nil {
			return nil, err
		}
	}
	acquireFile := acquire
	if movable {
		acquireFile = acquireMovable
	}
	lock, err := acquireFile(ctx, lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire %s lock: %w", label, err)
	}
	lease := &Lease{lock: lock, label: label}
	if movable {
		if err := validateRelocatableLock(canonicalDir, directoryIdentity, lockPath, lock.file); err != nil {
			return nil, errors.Join(err, lease.Close())
		}
	}
	return lease, nil
}

func validateRelocatableLock(dir string, expected os.FileInfo, path string, held *os.File) error {
	current, err := os.Stat(dir)
	if err != nil || !os.SameFile(expected, current) {
		return errors.New("relocatable lock directory changed while acquiring its lease")
	}
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() {
		return errors.New("relocatable lock path is missing or no longer a regular file")
	}
	opened, err := held.Stat()
	if err != nil || !os.SameFile(named, opened) {
		return errors.New("relocatable lock file changed while acquiring its lease")
	}
	return nil
}

// WithDirRelocatable is an explicit lease for operations that may rename the
// protected directory's containing tree. Windows grants delete sharing only for
// this entry point; file locking still excludes other lease holders. A waiter
// whose directory or named lock file changed fails before its operation runs.
func WithDirRelocatable(ctx context.Context, dir, label string, operation func() error) (err error) {
	if operation == nil {
		return fmt.Errorf("%s lock requires an operation", label)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lease, err := acquireDir(ctx, dir, label, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
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
func WithDir(ctx context.Context, dir, label string, operation func() error) error {
	return WithDirChecked(ctx, dir, label, nil, operation)
}

// WithDirChecked validates the retained, locked file before running operation.
// Other lock consumers keep WithDir's existing behavior; private state owners
// can enforce their native identity and permission contract under the lock.
func WithDirChecked(ctx context.Context, dir, label string, check func(*os.File) error, operation func() error) (err error) {
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
	if check != nil {
		if err := check(lease.lock.file); err != nil {
			return fmt.Errorf("validate %s lock: %w", label, err)
		}
	}
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
