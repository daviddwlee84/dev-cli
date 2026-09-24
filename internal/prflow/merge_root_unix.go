//go:build !windows

package prflow

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func checkMergeStateOwner(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("application state directory must be current-user-owned and not writable by others")
	}
	return nil
}
