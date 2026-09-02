//go:build unix

package sshhost

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func platformOpenNoFollowReadWrite(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func platformValidateCatalogDirectory(path string, info fs.FileInfo) error {
	return platformValidatePrivateDirectory(path, info)
}

func platformValidatePublicFile(path string, file *os.File, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no Unix ownership metadata", path)
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the current user: %w", path, ErrUnsafePath)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("%s has %d hard links, want 1: %w", path, stat.Nlink, ErrUnsafePath)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group/world writable: %w", path, ErrUnsafePath)
	}
	return nil
}
