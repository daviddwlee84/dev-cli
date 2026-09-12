//go:build windows

package privatefile

import (
	"errors"
	"io/fs"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CreatorOwner is the owner Windows assigns to objects created by this token.
// Elevated processes may default to Administrators rather than TokenUser.
func CreatorOwner() (*windows.SID, error) {
	var size uint32
	token := windows.GetCurrentProcessToken()
	err := windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size < uint32(unsafe.Sizeof(uintptr(0))) {
		return nil, errors.New("creator owner unavailable")
	}
	data := make([]byte, size)
	if err = windows.GetTokenInformation(token, windows.TokenOwner, &data[0], size, &size); err != nil {
		return nil, err
	}
	sid := *(**windows.SID)(unsafe.Pointer(&data[0]))
	copy, err := sid.Copy()
	runtime.KeepAlive(data)
	return copy, err
}

// ProtectCreated sets owner and DACL on an exclusively created private object,
// before sensitive bytes are written. Callers must retain their creation/source
// proof; this is not an ownership repair API for existing foreign files.
func ProtectCreated(path string, mode fs.FileMode) error {
	dir := mode.Perm() == 0o700
	if !dir && mode.Perm() != 0o600 {
		return errors.New("invalid private mode")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || dir != (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) || !dir && info.NumberOfLinks != 1 {
		return errors.New("unsafe created private object")
	}
	before, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := before.Owner()
	if err != nil || owner == nil {
		return errors.New("created object owner unavailable")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	creator, err := CreatorOwner()
	if err != nil || !owner.Equals(user.User.Sid) && !owner.Equals(creator) {
		return errors.New("created private object has foreign owner")
	}
	descriptor, err := Descriptor(dir)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("private DACL unavailable")
	}
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, user.User.Sid, nil, dacl, nil)
	runtime.KeepAlive(descriptor)
	return err
}
