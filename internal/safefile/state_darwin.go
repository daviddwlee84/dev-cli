//go:build darwin

package safefile

import (
	"io/fs"
	"syscall"
)

func samePlatformState(left, right fs.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	if !leftOK || !rightOK {
		return leftOK == rightOK
	}
	return leftStat.Ctimespec.Sec == rightStat.Ctimespec.Sec &&
		leftStat.Ctimespec.Nsec == rightStat.Ctimespec.Nsec
}
