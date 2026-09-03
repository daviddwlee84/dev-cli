//go:build linux

package sshhost

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func platformNoReplace(source, destination string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	// Link is an atomic no-replace publication even on kernels without
	// renameat2. Removing the staging name leaves the destination at link count 1.
	if err := os.Link(source, destination); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}
