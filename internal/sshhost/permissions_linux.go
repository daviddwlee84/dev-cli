//go:build linux

package sshhost

import (
	"io/fs"
	"os"
	"syscall"
)

func permissionSameChangeTime(a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Ctim == right.Ctim
}

// Linux access/default ACLs are exposed as system.posix_acl_* xattrs, which the
// bounded metadata reader rejects before any chmod.
func permissionCheckACL(*os.File) error { return nil }

// FS_INDEX_FL is ext4's read-only directory layout marker. In-place chmod
// preserves it; it is not a user-controlled flag needing a replacement path.
func permissionFlagsRoundTrip(flags uint32, directory bool) error {
	if directory {
		flags &^= 0x00001000
	}
	return platformFlagsRoundTrip(flags)
}
