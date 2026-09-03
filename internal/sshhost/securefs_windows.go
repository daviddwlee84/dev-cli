//go:build windows

package sshhost

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileAllAccess windows.ACCESS_MASK = 0x001f01ff

var reOpenFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

type fileMetadata struct {
	sddl string
}

func platformRejectReparsePath(path string, allowMissingFinal bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return fmt.Errorf("Windows path has no volume: %w", ErrUnsafePath)
	}
	rest := strings.TrimPrefix(absolute, volume)
	current := volume
	if strings.HasPrefix(rest, `\`) || strings.HasPrefix(rest, `/`) {
		current += string(filepath.Separator)
		rest = strings.TrimLeft(rest, `\/`)
	}
	components := strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' })
	for index, component := range components {
		current = filepath.Join(current, component)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if allowMissingFinal && index == len(components)-1 && errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("%s is a reparse point: %w", current, ErrUnsafePath)
		}
	}
	return nil
}

func platformValidateHome(path string, info fs.FileInfo) error {
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if descriptor == nil {
		return errors.New("home has no security descriptor")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user) {
		return fmt.Errorf("%s is not owned by the current user: %w", path, ErrUnsafePath)
	}
	return nil
}

func platformValidatePrivateDirectory(path string, info fs.FileInfo) error {
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return validateProtectedDescriptor(descriptor, true)
}

func platformMakePrivateDirectory(path string) error {
	descriptor, err := protectedDescriptor(true)
	if err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor,
	}
	return windows.CreateDirectory(pointer, &attributes)
}

func platformOpenNoFollow(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
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

func platformValidatePrivateFile(path string, file *os.File, info fs.FileInfo) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s is a reparse point: %w", path, ErrUnsafePath)
	}
	if information.NumberOfLinks != 1 {
		return fmt.Errorf("%s has %d hard links, want 1: %w", path, information.NumberOfLinks, ErrUnsafePath)
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return validateProtectedDescriptor(descriptor, false)
}

func platformCaptureMetadata(path string, file *os.File, info fs.FileInfo) (fileMetadata, error) {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fileMetadata{}, err
	}
	if err := validateProtectedDescriptor(descriptor, false); err != nil {
		return fileMetadata{}, err
	}
	sddl := descriptor.String()
	if sddl == "" {
		return fileMetadata{}, errors.New("cannot serialize Windows security descriptor")
	}
	return fileMetadata{sddl: sddl}, nil
}

func platformMetadataRoundTrip(metadata fileMetadata) error {
	if metadata.sddl == "" {
		return errors.New("empty Windows security descriptor")
	}
	descriptor, err := windows.SecurityDescriptorFromString(metadata.sddl)
	if err != nil {
		return err
	}
	return validateProtectedDescriptor(descriptor, false)
}

func platformApplyPrivateFile(path string, file *os.File) error {
	descriptor, err := protectedDescriptor(false)
	if err != nil {
		return err
	}
	return setDescriptor(file, descriptor)
}

func platformApplyMetadata(path string, file *os.File, metadata fileMetadata) error {
	descriptor, err := windows.SecurityDescriptorFromString(metadata.sddl)
	if err != nil {
		return err
	}
	return setDescriptor(file, descriptor)
}

func platformVerifyMetadata(path string, file *os.File, expected fileMetadata) error {
	actual, err := platformCaptureMetadata(path, file, nil)
	if err != nil {
		return err
	}
	if actual.sddl != expected.sddl {
		return errors.New("staged Windows owner/DACL differs from source metadata")
	}
	return nil
}

// Windows has no portable directory fsync. MOVEFILE_WRITE_THROUGH flushes the
// publication operation before returning.
func platformSyncDirectory(string) error { return nil }

func protectedDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf("O:%sD:P(A;%s;FA;;;SY)(A;%s;FA;;;%s)", user.String(), inheritance, inheritance, user.String())
	return windows.SecurityDescriptorFromString(sddl)
}

func setDescriptor(file *os.File, descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil {
		return errors.New("missing owner in Windows security descriptor")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	handle, err := reopenFileForSecurity(windows.Handle(file.Fd()))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
}

func reopenFileForSecurity(original windows.Handle) (windows.Handle, error) {
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	handle, _, callErr := reOpenFileProc.Call(
		uintptr(original), uintptr(windows.WRITE_DAC|windows.WRITE_OWNER|windows.READ_CONTROL), uintptr(share), 0,
	)
	if windows.Handle(handle) == windows.InvalidHandle {
		if callErr == nil || callErr == syscall.Errno(0) {
			callErr = windows.ERROR_ACCESS_DENIED
		}
		return windows.InvalidHandle, callErr
	}
	return windows.Handle(handle), nil
}

func validateProtectedDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	if descriptor == nil {
		return errors.New("missing Windows security descriptor")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("DACL inherits from a parent: %w", ErrUnsafePath)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user) {
		return fmt.Errorf("owner is not the current user: %w", ErrUnsafePath)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		if err == nil {
			err = errors.New("missing DACL")
		}
		return err
	}
	if dacl.AceCount != 2 {
		return fmt.Errorf("DACL has %d entries, want current-user and SYSTEM only: %w", dacl.AceCount, ErrUnsafePath)
	}
	wantFlags := uint8(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	seenUser, seenSystem := false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != wantFlags || ace.Mask != windowsFileAllAccess {
			return fmt.Errorf("DACL entry %d is not an explicit full-control entry: %w", index, ErrUnsafePath)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(user):
			seenUser = true
		case sid.Equals(system):
			seenSystem = true
		default:
			return fmt.Errorf("DACL grants an unexpected SID %s: %w", sid.String(), ErrUnsafePath)
		}
	}
	if !seenUser || !seenSystem {
		return fmt.Errorf("DACL does not grant both current-user and SYSTEM: %w", ErrUnsafePath)
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}
