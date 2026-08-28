//go:build linux

package experiment

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameExclusive(source, destination string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	// Older kernels and filesystems may not implement renameat2. Retain the
	// collision check on that compatibility path; supported systems stay atomic.
	if err := rejectExisting(destination); err != nil {
		return err
	}
	return os.Rename(source, destination)
}
