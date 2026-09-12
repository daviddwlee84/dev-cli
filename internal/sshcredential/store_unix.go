//go:build unix

package sshcredential

import (
	"io/fs"
	"os"
	"syscall"
)

func credentialPrivate(_ string, info fs.FileInfo, mode fs.FileMode) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() || info.Mode().Perm() != mode || info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() && st.Nlink != 1 {
		return ErrUnsafe
	}
	return nil
}
func credentialSetPrivate(path string, mode fs.FileMode) error { return os.Chmod(path, mode) }
func credentialAncestor(_ string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() && st.Uid != 0 || info.Mode().Perm()&0o022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return ErrUnsafe
	}
	return nil
}
