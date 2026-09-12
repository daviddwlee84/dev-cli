//go:build windows

package sshhost

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Go's Windows FileInfo exposes last-write time but not FILE_BASIC_INFO's
// ChangeTime. Capture that timestamp through a no-follow handle without reading
// private contents, and prove the handle still identifies the reviewed path.
func selectedKeyNativeChangeTime(identity secureFileIdentity) (int64, error) {
	file, err := platformOpenNoFollow(identity.path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !stableFileInfo(identity.info, opened) {
		return 0, ErrSourceChanged
	}
	if err := platformValidatePrivateFile(identity.path, file, opened); err != nil {
		return 0, err
	}
	readChangeTime := func() (int64, error) {
		// FILE_BASIC_INFO: four LARGE_INTEGER timestamps and DWORD attributes,
		// padded to the native eight-byte structure alignment.
		var basic struct {
			CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
			FileAttributes, Padding                                 uint32
		}
		err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileBasicInfo,
			(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic)))
		return basic.ChangeTime, err
	}
	before, err := readChangeTime()
	if err != nil {
		return 0, err
	}
	afterInfo, err := file.Stat()
	if err != nil || !stableFileInfo(opened, afterInfo) {
		return 0, ErrSourceChanged
	}
	current, err := os.Lstat(identity.path)
	if err != nil || !stableFileInfo(afterInfo, current) {
		return 0, ErrSourceChanged
	}
	after, err := readChangeTime()
	if err != nil || before != after {
		return 0, ErrSourceChanged
	}
	return after, nil
}
