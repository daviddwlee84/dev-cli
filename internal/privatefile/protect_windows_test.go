//go:build windows

package privatefile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProtectCreatedSetsCurrentOwnerAndProtectedACL(t *testing.T) {
	for _, directory := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "private")
		mode := os.FileMode(0o600)
		var err error
		if directory {
			mode = 0o700
			err = os.Mkdir(path, mode)
		} else {
			err = os.WriteFile(path, nil, mode)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = ProtectCreated(path, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = Check(path, info, directory); err != nil {
			t.Fatal(err)
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil {
			t.Fatal(err)
		}
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil || !owner.Equals(user.User.Sid) {
			t.Fatal("owner was not pinned to current user")
		}
	}
}

func TestProtectCreatedRejectsHardlinkedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+"-link"); err != nil {
		t.Fatal(err)
	}
	if err := ProtectCreated(path, 0o600); err == nil {
		t.Fatal("hardlink accepted")
	}
}

func TestProtectCreatedFileRejectsReplacedCreationIdentity(t *testing.T) {
	first, err := os.CreateTemp(t.TempDir(), "first-*")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(second, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var expected windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(first.Fd()), &expected); err != nil {
		t.Fatal(err)
	}
	if err := protectCreated(second, 0o600, &expected); err == nil {
		t.Fatal("protected a substituted name instead of the retained creation identity")
	}
	if err := ProtectCreatedFile(first); err != nil {
		t.Fatal(err)
	}
	info, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(first.Name(), info, false); err != nil {
		t.Fatal(err)
	}
}
