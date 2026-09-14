//go:build linux

package sshvault

import (
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func nativeSameChangeTime(a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Ctim == right.Ctim
}

func captureNativeACL(path string, expected fs.FileInfo) (nativeACLObservation, error) {
	var file *os.File
	list := func(buffer []byte) (int, error) { return unix.Llistxattr(path, buffer) }
	if expected.Mode()&os.ModeSymlink == 0 {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nativeACLObservation{}, ErrNativeContext
		}
		file = os.NewFile(uintptr(fd), path)
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !nativePermissionIdentity(expected, opened) {
			return nativeACLObservation{}, ErrStale
		}
		list = func(buffer []byte) (int, error) { return unix.Flistxattr(fd, buffer) }
	}
	// Like SSH's bounded metadata policy, reject access/default/NFS ACLs and
	// other system/security policy attributes. Never read private file contents.
	// Unsupported metadata queries are unknown, not an empty ACL observation.
	size, err := list(nil)
	if err != nil || size < 0 || size > 64<<10 {
		return nativeACLObservation{}, ErrNativeContext
	}
	names := make([]byte, size)
	if size > 0 {
		n, err := list(names)
		if err != nil || n < 0 || n > len(names) {
			return nativeACLObservation{}, ErrNativeContext
		}
		names = names[:n]
	}
	for _, name := range strings.Split(string(names), "\x00") {
		if strings.HasPrefix(name, "system.") || strings.HasPrefix(name, "security.") {
			return nativeACLObservation{}, ErrNativeContext
		}
	}
	current, err := os.Lstat(path)
	if err != nil || !nativePermissionIdentity(expected, current) {
		return nativeACLObservation{}, ErrStale
	}
	if file != nil {
		after, err := file.Stat()
		if err != nil || !nativePermissionIdentity(expected, after) {
			return nativeACLObservation{}, ErrStale
		}
	}
	return nativeACLObservation{safe: true}, nil
}
