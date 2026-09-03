//go:build windows

package machineid

import (
	"fmt"
	"io/fs"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	localSystemSID    = "S-1-5-18"
	administratorsSID = "S-1-5-32-544"
)

// setPrivateMode installs a protected DACL. Windows os.FileMode permission
// bits only model the read-only attribute and cannot establish privacy.
func setPrivateMode(path string, want fs.FileMode) error {
	inheritance := ""
	switch want.Perm() {
	case 0o600:
	case 0o700:
		inheritance = "OICI"
	default:
		return fmt.Errorf("unsupported private Windows mode %04o", want.Perm())
	}
	current, _, err := privateSIDs()
	if err != nil {
		return err
	}
	ace := func(sid string) string {
		return fmt.Sprintf("(A;%s;GA;;;%s)", inheritance, sid)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P" + ace(current) + ace(localSystemSID) + ace(administratorsSID))
	if err != nil {
		return fmt.Errorf("build private Windows security descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private Windows DACL: %w", err)
	}
	if dacl == nil {
		return fmt.Errorf("private Windows security descriptor has no DACL")
	}
	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return fmt.Errorf("set private Windows DACL: %w", err)
	}
	return nil
}

func privateModeMatches(path string, _ fs.FileMode, _ fs.FileMode) (bool, error) {
	current, allowed, err := privateSIDs()
	if err != nil {
		return false, err
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read Windows owner and DACL: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return false, fmt.Errorf("read Windows owner: %w", err)
	}
	if !privateSIDAllowed(owner, allowed) {
		return false, nil
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return false, fmt.Errorf("read Windows security descriptor control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return false, nil
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return false, fmt.Errorf("read Windows DACL: %w", err)
	}
	if dacl == nil {
		return false, nil
	}
	seenCurrent := false
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return false, fmt.Errorf("read Windows DACL entry %d: %w", index, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return false, nil
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		value := sid.String()
		if _, ok := allowed[value]; !ok {
			return false, nil
		}
		if value == current {
			seenCurrent = true
		}
	}
	return seenCurrent, nil
}

func privateSIDAllowed(sid *windows.SID, allowed map[string]struct{}) bool {
	if sid == nil {
		return false
	}
	_, ok := allowed[sid.String()]
	return ok
}

func privateSIDs() (string, map[string]struct{}, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", nil, fmt.Errorf("read current Windows user: %w", err)
	}
	current := user.User.Sid.String()
	if current == "" {
		return "", nil, fmt.Errorf("current Windows user has an invalid SID")
	}
	return current, map[string]struct{}{
		current:           {},
		localSystemSID:    {},
		administratorsSID: {},
	}, nil
}

func replaceFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH durably publishes the replacement.
// Windows does not expose a portable directory fsync through os.File.
func syncDirectory(string) error { return nil }
