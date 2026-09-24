//go:build windows

package lockx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRelocatableDirectoryMutexRejectsUntrustedExistingObject(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"unexpected-principal", "unprotected"} {
		t.Run(policy, func(t *testing.T) {
			directory := t.TempDir()
			identity, err := directoryIdentity(directory)
			if err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(directoryMutexName(identity))
			if err != nil {
				t.Fatal(err)
			}
			sid := user.User.Sid.String()
			sddl := "O:" + sid + "D:P(A;;GA;;;WD)"
			if policy == "unprotected" {
				sddl = "O:" + sid + "D:(A;;GA;;;" + sid + ")(A;;GA;;;SY)"
			}
			descriptor, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
			handle, err := windows.CreateMutex(&attributes, false, name)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(handle)
			called := false
			err = WithDirRelocatable(t.Context(), directory, "untrusted", func() error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("untrusted mutex accepted: called=%t err=%v", called, err)
			}
		})
	}
}

func TestRelocatableDirectoryMutexAbandonedOwnerIsRecoverable(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "lease")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRelocatableDirectoryMutexOwnerProcessHelper$")
	cmd.Env = append(os.Environ(), "DEV_LOCKX_OWNER_PATH="+directory)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		t.Fatalf("child did not acquire: %q %v", line, err)
	}
	identity, err := directoryIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(directoryMutexName(identity))
	if err != nil {
		t.Fatal(err)
	}
	// Retain an observer handle so the object survives the owning process and
	// the next keeper must handle WAIT_ABANDONED, rather than create anew.
	retained, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		t.Fatal(err)
	}
	defer windows.CloseHandle(retained)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	called := false
	if err := WithDirRelocatable(ctx, directory, "recovered", func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("abandoned lease: called=%t err=%v", called, err)
	}
}

func TestRelocatableDirectoryMutexOwnerProcessHelper(t *testing.T) {
	directory := os.Getenv("DEV_LOCKX_OWNER_PATH")
	if directory == "" {
		return
	}
	if err := WithDirRelocatable(t.Context(), directory, "child owner", func() error {
		fmt.Fprintln(os.Stdout, "held")
		time.Sleep(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRelocatableDirectoryMutexWaiterRejectsReplacedDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "lease")
	owner, err := acquireDir(t.Context(), directory, "owner", true)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	identity, err := directoryIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		// This is the acquisition boundary after the caller captured identity.
		lock, err := acquireDirectory(t.Context(), "", identity, true)
		if err != nil {
			result <- err
			return
		}
		defer lock.Close()
		result <- validateRelocatableLock(directory, identity, "", lock.file)
	}()
	if err := os.Rename(directory, directory+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "directory changed") {
			t.Fatalf("replaced directory authorized: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter failed to finish")
	}
}

func TestRelocatableDirectoryMutexCancellationReleasesWaiter(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "lease")
	owner, err := acquireDir(t.Context(), directory, "owner", true)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, movable := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		lease, err := acquireDir(ctx, directory, "canceled waiter", movable)
		cancel()
		if err == nil {
			_ = lease.Close()
			t.Fatal("nested mutex acquisition bypassed current owner")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := WithDirRelocatable(ctx, directory, "after cancellation", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
