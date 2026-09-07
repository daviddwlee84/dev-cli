//go:build !windows

package gitx

import (
	"fmt"
	"os"
	"syscall"
)

func submoduleDirectoryIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe submodule directory %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("directory identity unavailable for %s", path)
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
