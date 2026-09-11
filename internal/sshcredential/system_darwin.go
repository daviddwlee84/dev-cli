//go:build darwin

package sshcredential

import (
	"context"
	"github.com/ebitengine/purego"
	"sync"
	"unsafe"
)

type nativeSystemProvider struct{}

func (nativeSystemProvider) ID() string { return "system" }

type macSecurity struct {
	cf, sec          uintptr
	err              error
	stringCreate     func(uintptr, string, uint32) uintptr
	dataCreate       func(uintptr, *byte, int64) uintptr
	dataLength       func(uintptr) int64
	dataBytes        func(uintptr) *byte
	dictionaryCreate func(uintptr, int64, uintptr, uintptr) uintptr
	dictionarySet    func(uintptr, uintptr, uintptr)
	release          func(uintptr)
	copyMatching     func(uintptr, *uintptr) int32
	add              func(uintptr, *uintptr) int32
	update           func(uintptr, uintptr) int32
	delete           func(uintptr) int32
	constants        map[string]uintptr
}

var macOnce sync.Once
var macAPI macSecurity

func loadMacSecurity() *macSecurity {
	macOnce.Do(func() {
		a := &macAPI
		defer func() {
			if recover() != nil {
				a.err = ErrUnavailable
			}
		}()
		var e error
		a.cf, e = purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if e != nil {
			a.err = ErrUnavailable
			return
		}
		a.sec, e = purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if e != nil {
			a.err = ErrUnavailable
			return
		}
		purego.RegisterLibFunc(&a.stringCreate, a.cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&a.dataCreate, a.cf, "CFDataCreate")
		purego.RegisterLibFunc(&a.dataLength, a.cf, "CFDataGetLength")
		purego.RegisterLibFunc(&a.dataBytes, a.cf, "CFDataGetBytePtr")
		purego.RegisterLibFunc(&a.dictionaryCreate, a.cf, "CFDictionaryCreateMutable")
		purego.RegisterLibFunc(&a.dictionarySet, a.cf, "CFDictionarySetValue")
		purego.RegisterLibFunc(&a.release, a.cf, "CFRelease")
		purego.RegisterLibFunc(&a.copyMatching, a.sec, "SecItemCopyMatching")
		purego.RegisterLibFunc(&a.add, a.sec, "SecItemAdd")
		purego.RegisterLibFunc(&a.update, a.sec, "SecItemUpdate")
		purego.RegisterLibFunc(&a.delete, a.sec, "SecItemDelete")
		// Ask native dlsym for a pointer-typed result. Converting its address
		// through Go uintptr would lose pointer provenance and fail checkptr.
		var symbolPointer func(uintptr, string) unsafe.Pointer
		purego.RegisterLibFunc(&symbolPointer, purego.RTLD_DEFAULT, "dlsym")
		a.constants = map[string]uintptr{}
		for _, name := range []string{"kSecClass", "kSecClassGenericPassword", "kSecAttrService", "kSecAttrAccount", "kSecAttrLabel", "kSecValueData", "kSecReturnData", "kSecMatchLimit", "kSecMatchLimitOne"} {
			symbol := symbolPointer(a.sec, name)
			if symbol == nil {
				a.err = ErrUnavailable
				return
			}
			a.constants[name] = *(*uintptr)(symbol)
		}
		symbol := symbolPointer(a.cf, "kCFBooleanTrue")
		if symbol == nil {
			a.err = ErrUnavailable
			return
		}
		a.constants["true"] = *(*uintptr)(symbol)
	})
	return &macAPI
}
func (a *macSecurity) query(c Context) (uintptr, func()) {
	dict := a.dictionaryCreate(0, 0, 0, 0)
	service := a.stringCreate(0, providerOwner, 0x08000100)
	account := a.stringCreate(0, c.ID(), 0x08000100)
	a.dictionarySet(dict, a.constants["kSecClass"], a.constants["kSecClassGenericPassword"])
	a.dictionarySet(dict, a.constants["kSecAttrService"], service)
	a.dictionarySet(dict, a.constants["kSecAttrAccount"], account)
	return dict, func() { a.release(dict); a.release(service); a.release(account) }
}
func macStatus(status int32) error {
	switch status {
	case 0:
		return nil
	case -25300:
		return ErrNotFound
	case -25293, -25308, -128:
		return ErrLocked
	default:
		return ErrUnavailable
	}
}
func (nativeSystemProvider) Available(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return loadMacSecurity().err
}
func (p nativeSystemProvider) Get(ctx context.Context, c Context, ref Reference) ([]byte, error) {
	if validateNativeReference(c, ref) != nil {
		return nil, ErrUnsafe
	}
	if e := p.Available(ctx); e != nil {
		return nil, e
	}
	a := loadMacSecurity()
	query, cleanup := a.query(c)
	defer cleanup()
	a.dictionarySet(query, a.constants["kSecReturnData"], a.constants["true"])
	a.dictionarySet(query, a.constants["kSecMatchLimit"], a.constants["kSecMatchLimitOne"])
	var value uintptr
	if e := macStatus(a.copyMatching(query, &value)); e != nil {
		return nil, e
	}
	if value == 0 {
		return nil, ErrUnavailable
	}
	defer a.release(value)
	n := a.dataLength(value)
	if n < 1 || n > MaxSecretBytes {
		return nil, ErrUnsafe
	}
	secret := append([]byte(nil), unsafe.Slice(a.dataBytes(value), n)...)
	if validateSecret(secret) != nil {
		Wipe(secret)
		return nil, ErrUnsafe
	}
	return secret, nil
}
func (p nativeSystemProvider) Put(ctx context.Context, c Context, old *Reference, secret []byte) (Reference, error) {
	if c.Validate() != nil || validateSecret(secret) != nil || old != nil && validateNativeReference(c, *old) != nil {
		return Reference{}, ErrUnsafe
	}
	if e := p.Available(ctx); e != nil {
		return Reference{}, e
	}
	ref := Reference{p.ID(), c.ID()}
	a := loadMacSecurity()
	query, cleanup := a.query(c)
	defer cleanup()
	data := a.dataCreate(0, &secret[0], int64(len(secret)))
	if data == 0 {
		return Reference{}, ErrUnavailable
	}
	defer a.release(data)
	if old == nil {
		a.dictionarySet(query, a.constants["kSecValueData"], data)
		label := a.stringCreate(0, "dev SSH "+c.ID()[:16], 0x08000100)
		defer a.release(label)
		a.dictionarySet(query, a.constants["kSecAttrLabel"], label)
		status := a.add(query, nil)
		if status == -25299 {
			return Reference{}, ErrUnsafe
		}
		if status != 0 {
			return Reference{}, ErrUnknown
		}
	} else {
		attrs := a.dictionaryCreate(0, 0, 0, 0)
		defer a.release(attrs)
		a.dictionarySet(attrs, a.constants["kSecValueData"], data)
		status := a.update(query, attrs)
		if status == -25300 {
			return Reference{}, ErrNotFound
		}
		if status != 0 {
			return Reference{}, ErrUnknown
		}
	}
	return ref, nil
}
func (p nativeSystemProvider) Delete(ctx context.Context, c Context, ref Reference) error {
	if validateNativeReference(c, ref) != nil {
		return ErrUnsafe
	}
	if e := p.Available(ctx); e != nil {
		return e
	}
	a := loadMacSecurity()
	query, cleanup := a.query(c)
	defer cleanup()
	if status := a.delete(query); status != 0 {
		return macStatus(status)
	}
	return nil
}
