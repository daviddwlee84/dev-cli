//go:build windows

package machineid

import (
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateModeUsesProtectedWindowsDACL(t *testing.T) {
	path := t.TempDir()
	if err := setPrivateMode(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	private, err := privateModeMatches(path, info.Mode(), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if !private {
		t.Fatal("protected private DACL was not recognized")
	}
	_, allowed, err := privateSIDs()
	if err != nil {
		t.Fatal(err)
	}
	world, err := windows.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatal(err)
	}
	if privateSIDAllowed(world, allowed) {
		t.Fatal("untrusted owner SID was accepted")
	}

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
	private, err = privateModeMatches(path, info.Mode(), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if private {
		t.Fatal("world-accessible DACL was accepted as private")
	}

	// Restore access before TempDir cleanup runs.
	if err := setPrivateMode(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
