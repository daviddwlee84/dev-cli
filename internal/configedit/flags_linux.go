//go:build linux

package configedit

import (
	"errors"
	"golang.org/x/sys/unix"
	"io/fs"
	"os"
)

func checkFlags(file *os.File, _ fs.FileInfo) error {
	flags, err := unix.IoctlGetInt(int(file.Fd()), unix.FS_IOC_GETFLAGS)
	if err == unix.ENOTTY || err == unix.EOPNOTSUPP {
		return nil
	}
	if err != nil {
		return err
	}
	// Extents are an ordinary filesystem implementation flag. User-controlled
	// flags (including immutable, append-only and nodump) need native handling.
	if flags & ^0x00080000 != 0 {
		return errors.New("configuration inode flags require manual preservation")
	}
	return nil
}
