//go:build windows

package configedit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"golang.org/x/sys/windows"
)

// Metadata retains the source descriptor; it stays in private recovery only.
type Metadata struct {
	Present    bool   `json:"present"`
	Descriptor string `json:"descriptor,omitempty"`
}

func metadataHandle(path string) (windows.Handle, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, info, err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, info, err
	}
	if err = windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return 0, info, errors.New("unsafe Windows file identity")
	}
	return h, info, nil
}
func trustedSID(sid string) bool {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && (sid == user.User.Sid.String() || sid == "S-1-5-18" || sid == "S-1-5-32-544")
}
func validateDescriptor(sd *windows.SECURITY_DESCRIPTOR, parent bool) error {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !trustedSID(owner.String()) {
		return errors.New("untrusted Windows file owner")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("Windows file requires an explicit DACL")
	}
	// Ancestors may permit directory creation (the Windows volume root does).
	// The immediate publication directory must prohibit writes by other users.
	mask := windows.ACCESS_MASK(windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | 0x40)
	if parent {
		mask |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA
	}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported Windows access-control entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !trustedSID(sid) && ace.Mask&(mask|windows.GENERIC_ALL|windows.GENERIC_WRITE) != 0 {
			return errors.New("Windows file permits foreign writes")
		}
	}
	return nil
}
func securityAt(path string) (*windows.SECURITY_DESCRIPTOR, windows.ByHandleFileInformation, error) {
	h, info, err := metadataHandle(path)
	if err != nil {
		return nil, info, err
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	return sd, info, err
}
func checkAncestor(path string, _ fs.FileInfo) error {
	sd, _, err := securityAt(path)
	if err != nil {
		return err
	}
	return validateDescriptor(sd, false)
}
func checkDirectory(path string, _ fs.FileInfo) error {
	sd, _, err := securityAt(path)
	if err != nil {
		return err
	}
	return validateDescriptor(sd, true)
}
func captureMetadata(path string, expected fs.FileInfo) (Metadata, error) {
	sd, info, err := securityAt(path)
	if err != nil {
		return Metadata{}, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(expected, current) || info.NumberOfLinks != 1 {
		return Metadata{}, ErrStale
	}
	if err = validateDescriptor(sd, true); err != nil {
		return Metadata{}, err
	}
	return Metadata{Present: true, Descriptor: sd.String()}, nil
}
func (m Metadata) prepare(file *os.File) error {
	var sd *windows.SECURITY_DESCRIPTOR
	var err error
	if m.Present {
		sd, err = windows.SecurityDescriptorFromString(m.Descriptor)
	} else {
		sd, err = privatefile.Descriptor(false)
	}
	if err != nil {
		return err
	}
	if err = validateDescriptor(sd, true); err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	group, _, err := sd.Group()
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.OWNER_SECURITY_INFORMATION)
	if group != nil {
		flags |= windows.GROUP_SECURITY_INFORMATION
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		flags |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		flags |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	original := windows.Handle(file.Fd())
	var originalInfo windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(original, &originalInfo); err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(file.Name())
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var stageInfo windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &stageInfo) != nil || stageInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || stageInfo.NumberOfLinks != 1 || stageInfo.VolumeSerialNumber != originalInfo.VolumeSerialNumber || stageInfo.FileIndexHigh != originalInfo.FileIndexHigh || stageInfo.FileIndexLow != originalInfo.FileIndexLow {
		return ErrStale
	}
	if err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, flags, owner, group, acl, nil); err != nil {
		return err
	}
	actual, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if m.Present && actual.String() != sd.String() {
		return errors.New("Windows security descriptor did not round-trip")
	}
	if !m.Present {
		ao, _, e := actual.Owner()
		if e != nil || !ao.Equals(owner) {
			return errors.New("private stage owner mismatch")
		}
		ac, _, e := actual.DACL()
		if e != nil || ac == nil || ac.AceCount != acl.AceCount {
			return errors.New("private stage ACL mismatch")
		}
		for i := uint16(0); i < acl.AceCount; i++ {
			var aa, wa *windows.ACCESS_ALLOWED_ACE
			if windows.GetAce(ac, uint32(i), &aa) != nil || windows.GetAce(acl, uint32(i), &wa) != nil {
				return errors.New("private stage ACL unavailable")
			}
			if aa.Header != wa.Header || aa.Mask != wa.Mask || !(*windows.SID)(unsafe.Pointer(&aa.SidStart)).Equals((*windows.SID)(unsafe.Pointer(&wa.SidStart))) {
				return errors.New("private stage ACL mismatch")
			}
		}

	}
	return nil
}
func sameDevice(a, b string, _, _ fs.FileInfo) bool {
	ha, ia, ea := metadataHandle(a)
	if ea != nil {
		return false
	}
	defer windows.CloseHandle(ha)
	hb, ib, eb := metadataHandle(b)
	if eb != nil {
		return false
	}
	defer windows.CloseHandle(hb)
	return ia.VolumeSerialNumber == ib.VolumeSerialNumber
}
func fileIdentity(path string, _ fs.FileInfo) string {
	h, i, err := metadataHandle(path)
	if err != nil {
		return "unavailable"
	}
	defer windows.CloseHandle(h)
	return fmt.Sprintf("%d:%d:%d", i.VolumeSerialNumber, i.FileIndexHigh, i.FileIndexLow)
}
func makeDirectory(path string) error { return privatefile.MakeDir(path) }
func privateRecoveryMode(path string, info fs.FileInfo) bool {
	return privatefile.Check(path, info, true) == nil
}
