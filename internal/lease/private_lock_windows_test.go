//go:build windows

package lease

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func privateLockFixture(t *testing.T) (*os.File, string) {
	t.Helper()
	parent := t.TempDir()
	if err := setPrivateMode(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "lease.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file, path
}

func TestPrivateLockSealsOnlyInheritedPrivateDACL(t *testing.T) {
	file, path := privateLockFixture(t)
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	private, err := privateModeMatches(path, info.Mode(), 0600)
	if err != nil || private {
		t.Fatalf("fixture must inherit its private parent ACL: protected=%v error=%v", private, err)
	}
	if err := protectPrivateLock(file); err != nil {
		t.Fatal(err)
	}
	private, err = privateModeMatches(path, info.Mode(), 0600)
	if err != nil || !private {
		t.Fatalf("lock did not receive protected private ACL: %v %v", private, err)
	}
	if err := protectPrivateLock(file); err != nil {
		t.Fatalf("protected lock not reusable: %v", err)
	}
}

func TestPrivateLockRejectsBroadExistingDACLWithoutRepair(t *testing.T) {
	file, path := privateLockFixture(t)
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(descriptor)
	if err := protectPrivateLock(file); err == nil {
		t.Fatal("broad existing lock was repaired or accepted")
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if private, err := privateModeMatches(path, info.Mode(), 0600); err != nil || private {
		t.Fatalf("rejected broad ACL was changed: %v %v", private, err)
	}
}

func TestPrivateLockRejectsDifferentNamedIdentity(t *testing.T) {
	parent := t.TempDir()
	if err := setPrivateMode(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "lease.lock")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateLock(file); err == nil {
		t.Fatal("substituted path was accepted as the held lock")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if private, err := privateModeMatches(path, info.Mode(), 0600); err != nil || private {
		t.Fatalf("substituted file policy was modified: %v %v", private, err)
	}
}

func TestPrivateLockRejectsPayloadAndHardlink(t *testing.T) {
	t.Run("payload", func(t *testing.T) {
		file, path := privateLockFixture(t)
		if _, err := file.WriteString("foreign payload"); err != nil {
			t.Fatal(err)
		}
		if err := protectPrivateLock(file); err == nil {
			t.Fatal("nonempty lock accepted")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "foreign payload" {
			t.Fatalf("payload changed: %q %v", data, err)
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		file, path := privateLockFixture(t)
		if err := os.Link(path, path+".alias"); err != nil {
			t.Fatal(err)
		}
		if err := protectPrivateLock(file); err == nil {
			t.Fatal("hardlinked lock accepted")
		}
	})
}
