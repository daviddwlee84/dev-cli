//go:build unix

package machineregistry

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func setPrivateMode(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode.Perm())
}

func checkPrivate(_ string, info fs.FileInfo, want fs.FileMode) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != want.Perm() || info.Mode().IsRegular() && stat.Nlink != 1 {
		return fmt.Errorf("registry requires current-user ownership, private permissions and unlinked files: %w", ErrUnsafePath)
	}
	return nil
}

func checkAuxiliary(path string, info fs.FileInfo) error {
	return checkPrivate(path, info, 0o600)
}

func checkAncestor(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() && stat.Uid != 0 {
		return fmt.Errorf("registry ancestor has an untrusted owner: %w", ErrUnsafePath)
	}
	if info.Mode().Perm()&0o022 != 0 && !(info.Mode()&os.ModeSticky != 0 && stat.Uid == 0) {
		return fmt.Errorf("registry ancestor is writable by other users: %w", ErrUnsafePath)
	}
	return nil
}
