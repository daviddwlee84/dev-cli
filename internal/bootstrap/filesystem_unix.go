//go:build !windows

package bootstrap

import (
	"os"
	"syscall"
)

// sameFilesystem reports whether two existing paths live on the same device,
// which is the prerequisite for an atomic rename.
func sameFilesystem(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	as, aok := ai.Sys().(*syscall.Stat_t)
	bs, bok := bi.Sys().(*syscall.Stat_t)
	return aok && bok && as.Dev == bs.Dev
}
