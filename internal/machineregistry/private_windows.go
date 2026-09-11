//go:build windows

package machineregistry

import (
	"fmt"
	"io/fs"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func setPrivateMode(path string, want fs.FileMode) error {
	inheritance := ""
	if want.Perm() == 0o700 {
		inheritance = "OICI"
	} else if want.Perm() != 0o600 {
		return ErrUnsafePath
	}
	current, _, err := privateSIDs()
	if err != nil {
		return err
	}
	ace := func(sid string) string { return fmt.Sprintf("(A;%s;GA;;;%s)", inheritance, sid) }
	descriptor, err := windows.SecurityDescriptorFromString("D:P" + ace(current) + ace("S-1-5-18") + ace("S-1-5-32-544"))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("private registry DACL unavailable: %w", ErrUnsafePath)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	runtime.KeepAlive(descriptor)
	return err
}

func checkAncestor(_ string, info fs.FileInfo) error {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("registry path has unknown or reparse metadata: %w", ErrUnsafePath)
	}
	return nil
}

func checkPrivate(path string, info fs.FileInfo, _ fs.FileMode) error {
	return checkWindowsPrivate(path, info, true)
}

func checkAuxiliary(path string, info fs.FileInfo) error {
	// SQLite creates rollback journals itself. They inherit only the current
	// user/SYSTEM/Administrators grants from the protected registry directory.
	return checkWindowsPrivate(path, info, false)
}

func checkWindowsPrivate(path string, info fs.FileInfo, requireProtected bool) error {
	if err := checkAncestor(path, info); err != nil {
		return err
	}
	current, allowed, err := privateSIDs()
	if err != nil {
		return err
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || owner.String() != current {
		return fmt.Errorf("registry owner is not the current user: %w", ErrUnsafePath)
	}
	control, _, err := descriptor.Control()
	if err != nil || requireProtected && control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("registry DACL is not protected: %w", ErrUnsafePath)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return ErrUnsafePath
	}
	seenCurrent := false
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnsafePath
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !allowed[sid] {
			return fmt.Errorf("registry DACL grants another user access: %w", ErrUnsafePath)
		}
		seenCurrent = seenCurrent || sid == current
	}
	if !seenCurrent {
		return ErrUnsafePath
	}
	if info.Mode().IsRegular() {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(handle)
		var held windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &held); err != nil {
			return err
		}
		if held.NumberOfLinks != 1 || held.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ErrUnsafePath
		}
	}
	return nil
}

func privateSIDs() (string, map[string]bool, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", nil, err
	}
	current := user.User.Sid.String()
	return current, map[string]bool{current: true, "S-1-5-18": true, "S-1-5-32-544": true}, nil
}
