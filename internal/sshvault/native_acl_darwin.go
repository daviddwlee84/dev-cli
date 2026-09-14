//go:build darwin

package sshvault

import (
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func nativeSameChangeTime(a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Ctimespec == right.Ctimespec
}

func captureNativeACL(path string, expected fs.FileInfo) (nativeACLObservation, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW
	if expected.Mode()&os.ModeSymlink != 0 {
		// O_SYMLINK obtains the link's own inode rather than following it.
		flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_SYMLINK
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nativeACLObservation{}, ErrNativeContext
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !nativePermissionIdentity(expected, before) {
		return nativeACLObservation{}, ErrStale
	}
	observation, err := readStableNativeACL(func() (nativeACLObservation, error) { return readDarwinNativeACL(file) })
	if err != nil {
		return nativeACLObservation{}, err
	}
	after, err := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !nativePermissionIdentity(before, after) || !nativePermissionIdentity(after, current) {
		return nativeACLObservation{}, ErrStale
	}
	return observation, nil
}

func readDarwinNativeACL(file *os.File) (nativeACLObservation, error) {
	// Darwin ACLs are not reliably listed as xattrs. Use the same opened-inode
	// ATTR_CMN_EXTENDED_SECURITY query as sshhost permissionCheckACL, not file
	// contents or executable chmod/ACL text. Only documented deny-only metadata
	// is accepted beyond the existing absence-only policy.
	attributes := struct {
		BitmapCount                           uint16
		Reserved                              uint16
		Common, Volume, Directory, File, Fork uint32
	}{BitmapCount: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buffer [8192]byte
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, file.Fd(), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	runtime.KeepAlive(file)
	runtime.KeepAlive(attributes)
	if errno != 0 {
		return nativeACLObservation{}, ErrNativeContext
	}
	return parseDarwinNativeACL(buffer[:])
}
