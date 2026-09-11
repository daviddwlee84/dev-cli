//go:build darwin || linux

package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
)

func herdrDirectoryIdentity(info fs.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("filesystem identity is unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
