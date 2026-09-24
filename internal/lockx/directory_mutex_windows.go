//go:build windows

package lockx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The pinned keeper owns and releases the mutex on one OS thread. Callbacks
// remain free to migrate, and nested callers cannot exploit mutex recursion.
type directoryMutex struct {
	release chan struct{}
	done    chan error
	once    sync.Once
	err     error
}

func directoryMutexName(identity string) string {
	digest := sha256.Sum256([]byte("dev-cli-directory-lease-v1\x00" + identity))
	// No SID or path in the key: users/sessions must contend on one object.
	return `Global\dev-cli-directory-lease-v1-` + hex.EncodeToString(digest[:])
}

func acquireDirectoryMutex(ctx context.Context, identity string) (*directoryMutex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid.String()
	sddl := "O:" + sid + "D:P(A;;GA;;;" + sid + ")"
	if sid != "S-1-5-18" {
		sddl += "(A;;GA;;;SY)"
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	name, err := windows.UTF16PtrFromString(directoryMutexName(identity))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(&attributes, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	if handle == 0 {
		return nil, errors.New("directory lease mutex was not created")
	}
	if err := verifyDirectoryMutexSecurity(handle, user.User.Sid); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	lease := &directoryMutex{release: make(chan struct{}), done: make(chan error, 1)}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		for {
			if err := ctx.Err(); err != nil {
				ready <- errors.Join(err, windows.CloseHandle(handle))
				return
			}
			state, err := windows.WaitForSingleObject(handle, 10)
			if err != nil {
				ready <- errors.Join(err, windows.CloseHandle(handle))
				return
			}
			switch state {
			case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
				// acquireDir revalidates eager identity before every callback,
				// including when the preceding process abandoned the mutex.
				ready <- nil
				<-lease.release
				lease.done <- errors.Join(windows.ReleaseMutex(handle), windows.CloseHandle(handle))
				return
			case uint32(windows.WAIT_TIMEOUT):
			default:
				ready <- errors.Join(fmt.Errorf("unexpected directory mutex wait result %d", state), windows.CloseHandle(handle))
				return
			}
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return lease, nil
}

func (m *directoryMutex) Close() error {
	m.once.Do(func() { close(m.release); m.err = <-m.done })
	return m.err
}

func verifyDirectoryMutexSecurity(handle windows.Handle, current *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(current) {
		return errors.New("directory mutex has an unexpected owner")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("directory mutex requires a protected DACL")
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return errors.New("directory mutex has no trusted DACL")
	}
	allowed := map[string]bool{current.String(): true, "S-1-5-18": true}
	if int(acl.AceCount) != len(allowed) {
		return errors.New("directory mutex grants unexpected access")
	}
	seen := map[string]bool{}
	for index := uint16(0); index < acl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, uint32(index), &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return errors.New("directory mutex has unsupported access rules")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !allowed[sid] || seen[sid] {
			return errors.New("directory mutex grants unexpected access")
		}
		seen[sid] = true
	}
	return nil
}
