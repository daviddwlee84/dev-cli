//go:build darwin

package sshhost

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func permissionSameChangeTime(a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Ctimespec == right.Ctimespec
}

// Darwin ACLs are not reliably exposed as listxattr entries. Query the opened
// inode's ATTR_CMN_EXTENDED_SECURITY through fgetattrlist and reject any ACL
// blob. attr.h specifies a uint32 length followed by an attrreference_t here;
// an absent ACL is represented by a zero-length reference. Unknown responses
// fail closed, and this query never accesses file contents.
func permissionCheckACL(file *os.File) error {
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
		return fmt.Errorf("inspect native ACL before permission repair: %w", errno)
	}
	length := binary.LittleEndian.Uint32(buffer[:4])
	if length < 12 || length > uint32(len(buffer)) {
		return fmt.Errorf("unsupported native ACL response: %w", ErrManualRemediation)
	}
	if binary.LittleEndian.Uint32(buffer[8:12]) != 0 {
		return fmt.Errorf("native ACL requires manual remediation: %w", ErrManualRemediation)
	}
	return nil
}
