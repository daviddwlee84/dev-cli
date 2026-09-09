//go:build darwin

package configedit

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func checkFlags(_ *os.File, info fs.FileInfo) error {
	if info.Sys().(*syscall.Stat_t).Flags != 0 {
		return errors.New("configuration file flags require manual preservation")
	}
	return nil
}
