//go:build windows

package cli_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func externalCLITestHome(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"O:%sD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;%s)", user.User.Sid.String(), user.User.Sid.String(),
	))
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	pointer, err := windows.UTF16PtrFromString(home)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(pointer, &attributes); err != nil {
		t.Fatal(err)
	}
	return home
}
