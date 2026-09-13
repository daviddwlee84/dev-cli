//go:build unix

package machineregistry

import (
	"io/fs"
	"os"
	"syscall"
)

func setPrivateMode(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode.Perm())
}

func checkPrivate(path string, info fs.FileInfo, want fs.FileMode) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != want.Perm() || info.Mode().IsRegular() && stat.Nlink != 1 {
		diagnostic := pathDiagnostic(path, "requires current-user ownership, private permissions and unlinked files", info, want, false)
		if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().IsRegular() && stat.Nlink != 1 {
			diagnostic.Repairable = false
		}
		return diagnostic
	}
	return nil
}

func checkAuxiliary(path string, info fs.FileInfo) error {
	return checkPrivate(path, info, 0o600)
}

func checkAncestor(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() && stat.Uid != 0 {
		return pathDiagnostic(path, "ancestor has an untrusted owner", info, 0, true)
	}
	if info.Mode().Perm()&0o022 != 0 && !(info.Mode()&os.ModeSticky != 0 && stat.Uid == 0) {
		return pathDiagnostic(path, "ancestor is writable by other users", info, info.Mode().Perm()&^0o022, true)
	}
	return nil
}
