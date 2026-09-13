//go:build windows

package machineregistry

import (
	"errors"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"io/fs"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func setPrivateMode(path string, want fs.FileMode) error {
	return privatefile.ProtectCreated(path, want)
}

func checkAncestor(path string, info fs.FileInfo) error {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return &PathError{Path: path, Reason: "unknown or reparse metadata", Owner: "unknown", ExpectedOwner: "current user; real non-reparse path", Mode: info.Mode()}
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

func checkWindowsPrivate(path string, info fs.FileInfo, requireProtected bool) (resultErr error) {
	defer func() {
		var diagnostic *PathError
		if errors.Is(resultErr, ErrUnsafePath) && !errors.As(resultErr, &diagnostic) {
			resultErr = pathDiagnostic(path, resultErr.Error(), info, 0, false)
		}
	}()
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
	ownerMatches := err == nil && owner != nil && owner.String() == current
	if !requireProtected && err == nil && owner != nil {
		// SQLite creates its own journals. A verified private parent restricts the
		// inherited DACL; the token's native creator owner may be Administrators.
		creator, e := privatefile.CreatorOwner()
		ownerMatches = ownerMatches || e == nil && owner.Equals(creator) && allowed[owner.String()]
	}
	if !ownerMatches {
		actual := "unknown"
		if owner != nil {
			actual = owner.String()
		}
		return &PathError{Path: path, Reason: "owner is not the current user", Owner: actual, ExpectedOwner: current, Mode: info.Mode()}
	}
	control, _, err := descriptor.Control()
	if err != nil || requireProtected && control&windows.SE_DACL_PROTECTED == 0 {
		return &PathError{Path: path, Reason: "DACL is not protected", Owner: current, ExpectedOwner: current + "; private protected DACL", Mode: info.Mode()}
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
			return &PathError{Path: path, Reason: "DACL grants another user access: " + sid, Owner: current, ExpectedOwner: current + "; only current user, SYSTEM and Administrators", Mode: info.Mode()}
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

func pathDiagnostic(path, reason string, info fs.FileInfo, want fs.FileMode, ancestor bool) *PathError {
	expected := "current user; private protected ACL"
	if ancestor {
		expected = "trusted real non-reparse directory"
	}
	return &PathError{Path: path, Reason: reason, Owner: "unknown", ExpectedOwner: expected, Mode: info.Mode(), ExpectedMode: want}
}
