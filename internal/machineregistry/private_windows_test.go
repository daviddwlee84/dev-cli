//go:build windows

package machineregistry

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRegistryRejectsPublicWindowsDACL(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
	defer setPrivateMode(store.Path, 0o600)
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(store.Path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(descriptor)
	if _, err := store.Read(t.Context()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("public registry DACL read = %v", err)
	}
}

func TestSQLiteJournalMayInheritPrivateWindowsDirectoryACL(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
	path := store.Path + "-journal"
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAuxiliary(path, info); err != nil {
		t.Fatalf("private inherited journal ACL = %v", err)
	}
}
