//go:build windows

package sshcredential

import (
	"golang.org/x/sys/windows"
	"io/fs"
	"runtime"
	"syscall"
	"unsafe"
)

func credentialAncestor(_ string, info fs.FileInfo) error {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnsafe
	}
	return nil
}
func credentialSetPrivate(path string, mode fs.FileMode) error {
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	inherit := ""
	if mode == 0o700 {
		inherit = "OICI"
	}
	sd, e := windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";GA;;;" + u.User.Sid.String() + ")(A;" + inherit + ";GA;;;SY)(A;" + inherit + ";GA;;;BA)")
	if e != nil {
		return e
	}
	dacl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	e = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	runtime.KeepAlive(sd)
	return e
}
func credentialPrivate(path string, info fs.FileInfo, _ fs.FileMode) error {
	if credentialAncestor(path, info) != nil {
		return ErrUnsafe
	}
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	sd, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return e
	}
	owner, _, e := sd.Owner()
	if e != nil || owner.String() != u.User.Sid.String() {
		return ErrUnsafe
	}
	control, _, e := sd.Control()
	if e != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnsafe
	}
	dacl, _, e := sd.DACL()
	if e != nil || dacl == nil {
		return ErrUnsafe
	}
	seen := false
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, uint32(i), &ace) != nil {
			return ErrUnsafe
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnsafe
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != u.User.Sid.String() && sid != "S-1-5-18" && sid != "S-1-5-32-544" {
			return ErrUnsafe
		}
		seen = seen || sid == u.User.Sid.String()
	}
	if !seen {
		return ErrUnsafe
	}
	if info.Mode().IsRegular() {
		p, _ := windows.UTF16PtrFromString(path)
		h, e := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if e != nil {
			return e
		}
		defer windows.CloseHandle(h)
		var held windows.ByHandleFileInformation
		if windows.GetFileInformationByHandle(h, &held) != nil || held.NumberOfLinks != 1 || held.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ErrUnsafe
		}
	}
	return nil
}
