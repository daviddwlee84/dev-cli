//go:build windows

package claudeworkflow

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func safeMetadataInfo(path string, info fs.FileInfo, wantDir bool) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	if wantDir {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}
