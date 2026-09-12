//go:build linux || darwin

package sshremote

import (
	"io/fs"
	"os"
	"syscall"
)

func cachePlatformSupported() error { return nil }

func checkCacheAncestor(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid() {
		return ErrUnsafeCache
	}
	if info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return ErrUnsafeCache
	}
	return nil
}

func checkCacheDirectory(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return ErrUnsafeCache
	}
	return nil
}

func checkCacheFile(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || info.Mode().Perm() != 0o600 {
		return ErrUnsafeCache
	}
	return nil
}

// Mkdir and safefile's stage creation already set private Unix modes. Avoid
// path-based chmod here; it would introduce a link-following operation.
func setCachePrivate(string, fs.FileMode) error { return nil }
