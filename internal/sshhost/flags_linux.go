//go:build linux

package sshhost

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const (
	linuxFlagImmutable = 0x00000010
	linuxFlagAppend    = 0x00000020
	linuxFlagNoDump    = 0x00000040
	linuxFlagExtents   = 0x00080000
)

func platformFileFlags(file *os.File) (uint32, error) {
	flags, err := unix.IoctlGetInt(int(file.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return 0, fmt.Errorf("read Linux inode flags: %w", err)
	}
	return uint32(flags), nil
}

func platformFlagsRoundTrip(flags uint32) error {
	if blocked := flags & (linuxFlagImmutable | linuxFlagAppend); blocked != 0 {
		return fmt.Errorf("immutable or append-only Linux inode flags 0x%x prevent safe replacement", blocked)
	}
	const proven = linuxFlagNoDump | linuxFlagExtents
	if unsupported := flags &^ proven; unsupported != 0 {
		return fmt.Errorf("Linux inode flags 0x%x require manual preservation", unsupported)
	}
	return nil
}

func platformSetFileFlags(file *os.File, flags uint32) error {
	if err := platformFlagsRoundTrip(flags); err != nil {
		return err
	}
	current, err := platformFileFlags(file)
	if err != nil {
		return err
	}
	if current == flags {
		return nil
	}
	if err := unix.IoctlSetPointerInt(int(file.Fd()), unix.FS_IOC_SETFLAGS, int(flags)); err != nil {
		return fmt.Errorf("restore Linux inode flags 0x%x: %w", flags, err)
	}
	return nil
}
