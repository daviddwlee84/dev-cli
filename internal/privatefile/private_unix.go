//go:build !windows

package privatefile

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func MakeDir(path string) error { return os.Mkdir(path, 0o700) }
func Check(_ string, info fs.FileInfo, dir bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return errors.New("private artifacts must be owned by the current user and private")
	}
	if !dir && (!info.Mode().IsRegular() || stat.Nlink != 1) {
		return errors.New("private artifact is not a single-link regular file")
	}
	return nil
}
