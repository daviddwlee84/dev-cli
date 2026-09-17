//go:build windows

package safefile

import (
	"context"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func removeEmptyChildDirNative(ctx context.Context, parent *os.Root, name string, expected fs.FileInfo) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	held, err := dir.Stat()
	if err != nil {
		return err
	}
	root, err := parent.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(held, root) || unsafeLink(held) || !held.IsDir() {
		return ErrChanged
	}
	childName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(dir.Fd()), ObjectName: childName,
		Attributes: windows.OBJ_CASE_INSENSITIVE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	// No delete-on-close: nothing may be deleted before type, identity and
	// emptiness are verified. DIRECTORY_FILE rejects a replaced ordinary file;
	// OPEN_REPARSE_POINT opens (and below rejects) rather than follows a link.
	if err := windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.DELETE,
		&attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0, 0); err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if unsafeLink(info) || !info.IsDir() || !os.SameFile(info, expected) {
		return ErrChanged
	}
	if err := verifyEmptyDirectory(file); err != nil {
		return err
	}
	if err := VerifyChildRoot(parent, name, expected); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	// FileDispositionInfo deletes this verified handle, not a newly resolved
	// path. The kernel refuses a directory that became nonempty.
	remove := byte(1)
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &remove, 1)
}
