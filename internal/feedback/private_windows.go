//go:build windows

package feedback

import (
	"errors"
	"golang.org/x/sys/windows"
	"io/fs"
	"unsafe"
)

func feedbackDescriptor(dir bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	flags := ""
	if dir {
		flags = "OICI"
	}
	sid := user.User.Sid.String()
	return windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;" + flags + ";FA;;;" + sid + ")(A;" + flags + ";FA;;;SY)")
}
func makePrivateDir(path string) error {
	descriptor, err := feedbackDescriptor(true)
	if err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	return windows.CreateDirectory(pointer, &attrs)
}
func checkPrivate(path string, _ fs.FileInfo, dir bool) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if dir {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(pointer, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return errors.New("feedback artifact identity unavailable")
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !dir && info.NumberOfLinks != 1 {
		return errors.New("unsafe feedback artifact identity")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !owner.Equals(current.User.Sid) {
		return errors.New("feedback artifact owner differs from current user")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("feedback artifacts require a protected DACL")
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return errors.New("feedback artifacts require current-user and SYSTEM access only")
	}
	seen := map[string]bool{}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, uint32(i), &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported feedback ACL")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != current.User.Sid.String() && sid != "S-1-5-18" {
			return errors.New("feedback ACL grants another identity")
		}
		seen[sid] = true
	}
	if len(seen) != 2 {
		return errors.New("feedback ACL is incomplete")
	}
	return nil
}
