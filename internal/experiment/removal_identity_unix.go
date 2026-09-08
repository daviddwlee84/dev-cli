//go:build darwin

package experiment

import (
	"fmt"
	"os"
	"syscall"
)

func removalIdentity(path string) (string, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", fmt.Errorf("filesystem identity unavailable")
	}
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec), fmt.Sprint(stat.Dev), nil
}
