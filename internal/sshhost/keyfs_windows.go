//go:build windows

package sshhost

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func platformOpenNoFollowReadWrite(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("%s is a reparse point: %w", path, ErrUnsafePath)
	}
	return os.NewFile(uintptr(handle), path), nil
}

func platformValidateCatalogDirectory(path string, info fs.FileInfo) error {
	return platformValidatePrivateDirectory(path, info)
}

func platformValidatePublicFile(path string, file *os.File, info fs.FileInfo) error {
	// Public material is not secret, but it controls authorization. Requiring the
	// same protected owner/DACL and single-link shape as local configuration is a
	// conservative validation rule and generated public files satisfy it.
	return platformValidatePrivateFile(path, file, info)
}
