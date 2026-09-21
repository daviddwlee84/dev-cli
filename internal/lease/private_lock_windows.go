//go:build windows

package lease

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// Empty lock files from older builds may inherit a private DACL from an already
// protected authority parent. Seal only that existing effective policy, while
// holding the actual lock and proving the named security handle is the same file.
// Never repair a broad/foreign ACL or change object contents or ownership.
func protectPrivateLock(file *os.File) error {
	if err := privateLockParent(file.Name()); err != nil {
		return err
	}
	var held windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &held); err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(file.Name())
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var named windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &named); err != nil {
		return err
	}
	if named.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || named.NumberOfLinks != 1 || named.FileSizeHigh != 0 || named.FileSizeLow != 0 || held.VolumeSerialNumber != named.VolumeSerialNumber || held.FileIndexHigh != named.FileIndexHigh || held.FileIndexLow != named.FileIndexLow {
		return errors.New("private lock identity or contents changed")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	private, err := privateDescriptorMatches(descriptor, false)
	if err != nil {
		return err
	}
	if !private {
		return errors.New("private lock has a foreign owner or non-private effective DACL")
	}
	protected, err := privateDescriptorMatches(descriptor, true)
	if err != nil {
		return err
	}
	if protected {
		return nil
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("private lock DACL unavailable")
	}
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return err
	}
	after, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	protected, err = privateDescriptorMatches(after, true)
	if err != nil {
		return err
	}
	if !protected {
		return errors.New("private lock DACL was not protected")
	}
	return nil
}
