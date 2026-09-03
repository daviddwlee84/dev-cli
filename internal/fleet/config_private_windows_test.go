//go:build windows

package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsCheckPrivateModeRequiresProtectedUserAndSystemDACL(t *testing.T) {
	cfg := configWithPlainPassword()
	root := t.TempDir()
	protectedPath := filepath.Join(root, "protected.toml")
	writeProtectedWindowsFile(t, protectedPath, []byte("secret"))
	if err := CheckPrivateMode(protectedPath, cfg); err != nil {
		t.Fatalf("protected current-user-and-SYSTEM DACL rejected: %v", err)
	}

	user, err := managedWindowsCurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	insecureDescriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"O:%sD:P(A;;FA;;;SY)(A;;FA;;;%s)(A;;FR;;;WD)",
		user.String(),
		user.String(),
	))
	if err != nil {
		t.Fatal(err)
	}
	insecurePath := filepath.Join(root, "insecure.toml")
	writeWindowsFileWithDescriptor(t, insecurePath, []byte("secret"), insecureDescriptor)
	if err := CheckPrivateMode(insecurePath, cfg); err == nil {
		t.Fatal("DACL granting Everyone read access was accepted")
	}
}

func TestWindowsWritePrivateConfigFileProtectsAndPreservesUnsafeTargets(t *testing.T) {
	t.Run("create and overwrite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "remotes.toml")
		if err := WritePrivateConfigFile(path, []byte("first"), false); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateMode(path, configWithPlainPassword()); err != nil {
			t.Fatalf("created primary config DACL: %v", err)
		}
		if err := WritePrivateConfigFile(path, []byte("second"), true); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, []byte("second")) {
			t.Fatalf("overwritten bytes = %q, err %v", got, err)
		}
		if err := CheckPrivateMode(path, configWithPlainPassword()); err != nil {
			t.Fatalf("overwritten primary config DACL: %v", err)
		}
	})

	t.Run("DACL failure preserves bytes", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "remotes.toml")
		user, err := managedWindowsCurrentUserSID()
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FR;;;WD)", user.String(), user.String()))
		if err != nil {
			t.Fatal(err)
		}
		writeWindowsFileWithDescriptor(t, path, []byte("foreign"), descriptor)
		if err := WritePrivateConfigFile(path, []byte("replacement"), true); err == nil {
			t.Fatal("force overwrite accepted unsafe DACL")
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, []byte("foreign")) {
			t.Fatalf("unsafe-DACL bytes changed: %q, err %v", got, err)
		}
	})

	t.Run("hard link preserves bytes", func(t *testing.T) {
		root := t.TempDir()
		foreign := filepath.Join(root, "foreign")
		path := filepath.Join(root, "remotes.toml")
		writeProtectedWindowsFile(t, foreign, []byte("foreign"))
		if err := os.Link(foreign, path); err != nil {
			t.Fatal(err)
		}
		if err := WritePrivateConfigFile(path, []byte("replacement"), true); err == nil {
			t.Fatal("force overwrite accepted hard link")
		}
		got, err := os.ReadFile(foreign)
		if err != nil || !bytes.Equal(got, []byte("foreign")) {
			t.Fatalf("hard-linked bytes changed: %q, err %v", got, err)
		}
	})

	t.Run("reparse target preserves bytes", func(t *testing.T) {
		root := t.TempDir()
		foreign := filepath.Join(root, "foreign")
		path := filepath.Join(root, "remotes.toml")
		writeProtectedWindowsFile(t, foreign, []byte("foreign"))
		if err := os.Symlink(foreign, path); err != nil {
			t.Skipf("Windows symlink privilege or developer mode unavailable: %v", err)
		}
		if err := WritePrivateConfigFile(path, []byte("replacement"), true); err == nil {
			t.Fatal("force overwrite accepted reparse target")
		}
		got, err := os.ReadFile(foreign)
		if err != nil || !bytes.Equal(got, []byte("foreign")) {
			t.Fatalf("reparse target bytes changed: %q, err %v", got, err)
		}
	})

	t.Run("concurrent replacement wins", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "remotes.toml")
		concurrent := filepath.Join(root, "concurrent.toml")
		if err := WritePrivateConfigFile(path, []byte("initial"), false); err != nil {
			t.Fatal(err)
		}
		writeProtectedWindowsFile(t, concurrent, []byte("concurrent"))
		var concurrentErr error
		privateConfigWindowsBeforePublish = func(string) {
			from, _ := windows.UTF16PtrFromString(concurrent)
			to, _ := windows.UTF16PtrFromString(path)
			concurrentErr = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		}
		t.Cleanup(func() { privateConfigWindowsBeforePublish = nil })
		err := WritePrivateConfigFile(path, []byte("replacement"), true)
		privateConfigWindowsBeforePublish = nil
		got, readErr := os.ReadFile(path)
		switch {
		case concurrentErr == nil:
			if err == nil {
				t.Fatal("force overwrite replaced a concurrent object")
			}
			if readErr != nil || !bytes.Equal(got, []byte("concurrent")) {
				t.Fatalf("concurrent bytes changed: %q, err %v (write err %v)", got, readErr, err)
			}
		case errors.Is(concurrentErr, windows.ERROR_ACCESS_DENIED), errors.Is(concurrentErr, windows.ERROR_SHARING_VIOLATION):
			if err != nil || readErr != nil || !bytes.Equal(got, []byte("replacement")) {
				t.Fatalf("held-handle replacement = %q, read err %v, write err %v, concurrent err %v", got, readErr, err, concurrentErr)
			}
		default:
			t.Fatalf("unexpected concurrent replacement error: %v", concurrentErr)
		}
	})
}

func configWithPlainPassword() Config {
	cfg := DefaultConfig()
	cfg.Hosts = []Host{{
		Name: "lab", SSHAlias: "lab",
		SSHLoginPasswordSource: PasswordSource{Type: "plain", Value: "secret"},
	}}
	return cfg
}

func writeProtectedWindowsFile(t *testing.T, path string, content []byte) {
	t.Helper()
	descriptor, err := managedWindowsProtectedDescriptor(false)
	if err != nil {
		t.Fatal(err)
	}
	writeWindowsFileWithDescriptor(t, path, content, descriptor)
}

func writeWindowsFileWithDescriptor(t *testing.T, path string, content []byte, descriptor *windows.SECURITY_DESCRIPTOR) {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		&attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("cannot wrap protected Windows test file")
	}
	written, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if written != len(content) {
		t.Fatalf("write protected Windows test file = %d bytes, want %d", written, len(content))
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}
