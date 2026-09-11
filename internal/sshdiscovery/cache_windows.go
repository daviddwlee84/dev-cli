//go:build windows

package sshdiscovery

import (
	"fmt"
	"io/fs"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func cachePlatformSupported() error { return nil }

func checkCacheAncestor(_ string, info fs.FileInfo) error {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || !info.IsDir() || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnsafeCache
	}
	return nil
}

func checkCacheDirectory(path string, info fs.FileInfo) error {
	if err := checkCacheAncestor(path, info); err != nil {
		return err
	}
	return checkWindowsCachePrivate(path, true)
}

func checkCacheFile(path string, info fs.FileInfo) error {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || !info.Mode().IsRegular() || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnsafeCache
	}
	return checkWindowsCachePrivate(path, false)
}

func cacheWindowsHandle(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(name, access|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	var held windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &held); err != nil || held.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		if err != nil {
			return windows.InvalidHandle, err
		}
		return windows.InvalidHandle, ErrUnsafeCache
	}
	return handle, nil
}

func cachePrivateSIDs() (string, map[string]bool, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", nil, err
	}
	current := user.User.Sid.String()
	if current == "" {
		return "", nil, ErrUnsafeCache
	}
	return current, map[string]bool{current: true, "S-1-5-18": true, "S-1-5-32-544": true}, nil
}

// Windows permissions use a protected DACL, not os.FileMode's read-only bit.
// Only the current user, SYSTEM and Administrators receive access. Applying the
// DACL through an opened non-reparse handle avoids following a final link.
func setCachePrivate(path string, mode fs.FileMode) error {
	inheritance := ""
	if mode.Perm() == 0o700 {
		inheritance = "OICI"
	} else if mode.Perm() != 0o600 {
		return ErrUnsafeCache
	}
	current, _, err := cachePrivateSIDs()
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
		return ErrUnsafeCache
	}
	handle, err := cacheWindowsHandle(path, windows.WRITE_DAC|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	runtime.KeepAlive(descriptor)
	return err
}

func checkWindowsCachePrivate(path string, directory bool) error {
	current, allowed, err := cachePrivateSIDs()
	if err != nil {
		return err
	}
	handle, err := cacheWindowsHandle(path, windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var held windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &held); err != nil {
		return err
	}
	if directory != (held.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) || !directory && held.NumberOfLinks != 1 {
		return ErrUnsafeCache
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || owner.String() != current {
		return ErrUnsafeCache
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnsafeCache
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return ErrUnsafeCache
	}
	seenCurrent := false
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnsafeCache
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !allowed[sid] {
			return ErrUnsafeCache
		}
		seenCurrent = seenCurrent || sid == current
	}
	if !seenCurrent {
		return ErrUnsafeCache
	}
	return nil
}
