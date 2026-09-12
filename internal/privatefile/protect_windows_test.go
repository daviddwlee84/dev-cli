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
