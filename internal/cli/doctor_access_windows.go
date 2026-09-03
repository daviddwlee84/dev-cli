//go:build windows

package cli

import "golang.org/x/sys/windows"

const windowsFileAddFile = 0x0002

// dirWritable requests directory-create access on a handle without creating a
// probe file. The kernel evaluates the current token against the directory DACL.
func dirWritable(dir string) bool {
	pointer, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(
		pointer,
		windowsFileAddFile,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(handle)
	return true
}
