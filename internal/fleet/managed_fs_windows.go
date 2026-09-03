//go:build windows

package fleet

import (
	"fmt"
	"io/fs"
)

// Windows mode bits do not express DACL ownership. Common validation still
// rejects non-regular files and reported symlinks/reparse links; sshhost must
// inject its handle-based DACL/reparse-aware backend for mutation.
func validateManagedPermissions(_ string, _ fs.FileInfo, _ fs.FileMode) error { return nil }

func writeManagedFragmentOS(request ManagedFragmentWriteRequest) error {
	return fmt.Errorf("write %s: %w", request.Path, ErrManagedSecurityBackend)
}

func removeManagedFragmentOS(request ManagedFragmentRemoveRequest) error {
	return fmt.Errorf("remove %s: %w", request.Path, ErrManagedSecurityBackend)
}
