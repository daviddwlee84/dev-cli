//go:build unix

package sshvault

import (
	"io/fs"
	"os"
	"syscall"
)

func nativeOwned(info fs.FileInfo, private bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return false
	}
	if private {
		return int(stat.Uid) == os.Geteuid() && info.Mode().Perm()&0o077 == 0 && (info.IsDir() || stat.Nlink == 1)
	}
	if int(stat.Uid) != os.Geteuid() && stat.Uid != 0 {
		return false
	}
	return info.Mode().Perm()&0o022 == 0 || info.IsDir() && stat.Uid == 0 && info.Mode()&os.ModeSticky != 0
}

func nativeSameOwner(a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Uid == right.Uid && left.Gid == right.Gid
}

func nativeLinkOwned(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (int(stat.Uid) == os.Geteuid() || stat.Uid == 0)
}
