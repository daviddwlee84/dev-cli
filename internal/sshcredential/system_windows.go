//go:build windows

package sshcredential

import (
	"context"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

type nativeSystemProvider struct{}

func (nativeSystemProvider) ID() string { return "system" }

var credDLL = windows.NewLazySystemDLL("advapi32.dll")
var credRead = credDLL.NewProc("CredReadW")
var credWrite = credDLL.NewProc("CredWriteW")
var credDelete = credDLL.NewProc("CredDeleteW")
var credFree = credDLL.NewProc("CredFree")

type winCredential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	CredentialBlobSize      uint32
	CredentialBlob          *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, UserName   *uint16
}

func (nativeSystemProvider) Available(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if credRead.Find() != nil || credWrite.Find() != nil || credDelete.Find() != nil {
		return ErrUnavailable
	}
	return nil
}
func (p nativeSystemProvider) Get(ctx context.Context, c Context, ref Reference) ([]byte, error) {
	if validateNativeReference(c, ref) != nil {
		return nil, ErrUnsafe
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	target, _ := windows.UTF16PtrFromString(providerOwner + ":" + c.ID())
	var value *winCredential
	r, _, e := credRead.Call(uintptr(unsafe.Pointer(target)), 1, 0, uintptr(unsafe.Pointer(&value)))
	if r == 0 {
		if e == windows.ERROR_NOT_FOUND {
			return nil, ErrNotFound
		}
		return nil, ErrLocked
	}
	defer credFree.Call(uintptr(unsafe.Pointer(value)))
	if value == nil || value.CredentialBlobSize > MaxSecretBytes || windows.UTF16PtrToString(value.Comment) != providerOwner {
		return nil, ErrUnsafe
	}
	secret := append([]byte(nil), unsafe.Slice(value.CredentialBlob, value.CredentialBlobSize)...)
	if validateSecret(secret) != nil {
		Wipe(secret)
		return nil, ErrUnsafe
	}
	return secret, nil
}
func (p nativeSystemProvider) Put(ctx context.Context, c Context, old *Reference, secret []byte) (Reference, error) {
	if c.Validate() != nil || validateSecret(secret) != nil || len(secret) > 2560 || old != nil && validateNativeReference(c, *old) != nil {
		return Reference{}, ErrUnsafe
	}
	ref := Reference{p.ID(), c.ID()}
	existing, e := p.Get(ctx, c, ref)
	Wipe(existing)
	if e == nil && old == nil {
		return Reference{}, ErrUnsafe
	}
	if e != nil && e != ErrNotFound {
		return Reference{}, e
	}
	if e == ErrNotFound && old != nil {
		return Reference{}, ErrNotFound
	}
	target, _ := windows.UTF16PtrFromString(providerOwner + ":" + c.ID())
	comment, _ := windows.UTF16PtrFromString(providerOwner)
	user, _ := windows.UTF16PtrFromString(c.User)
	value := winCredential{Type: 1, TargetName: target, Comment: comment, CredentialBlobSize: uint32(len(secret)), CredentialBlob: &secret[0], Persist: 2, UserName: user}
	r, _, _ := credWrite.Call(uintptr(unsafe.Pointer(&value)), 0)
	runtime.KeepAlive(secret)
	runtime.KeepAlive(value)
	if r == 0 {
		return Reference{}, ErrUnknown
	}
	return ref, nil
}
func (p nativeSystemProvider) Delete(ctx context.Context, c Context, ref Reference) error {
	secret, e := p.Get(ctx, c, ref)
	Wipe(secret)
	if e != nil {
		return e
	}
	target, _ := windows.UTF16PtrFromString(providerOwner + ":" + c.ID())
	r, _, _ := credDelete.Call(uintptr(unsafe.Pointer(target)), 1, 0)
	if r == 0 {
		return ErrUnknown
	}
	return nil
}
