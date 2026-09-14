//go:build darwin

package sshvault

import (
	"crypto/sha256"
	"encoding/binary"
)

// getattrlist(2) documents an attrreference to kauth_filesec. sys/kauth.h
// defines its 44-byte prefix, 24-byte ACEs, magic, count limit, tags and vnode
// rights. Current supported Darwin targets use little-endian native encoding.
// XNU attr_pack_common sets the unused owner/group GUIDs to kauth_null_guid.
func parseDarwinNativeACL(buffer []byte) (nativeACLObservation, error) {
	const (
		prefixSize   = 12 // length + attrreference_t
		filesecSize  = 44
		aceSize      = 24
		filesecMagic = 0x012cc16d
		noACL        = ^uint32(0)
		aclNoInherit = 1 << 17
		aceDeny      = 2
		aceInherited = 1 << 4
		vnodeRights  = 0x3ffe // KAUTH_VNODE_READ_DATA through TAKE_OWNERSHIP
	)
	if len(buffer) < prefixSize {
		return nativeACLObservation{}, ErrNativeContext
	}
	length := binary.LittleEndian.Uint32(buffer[:4])
	offset := binary.LittleEndian.Uint32(buffer[4:8])
	dataLength := binary.LittleEndian.Uint32(buffer[8:12])
	if length < prefixSize || length > 8192 || uint64(length) > uint64(len(buffer)) {
		return nativeACLObservation{}, ErrNativeContext
	}
	if dataLength == 0 {
		if length != prefixSize || offset != 0 && offset != 8 {
			return nativeACLObservation{}, ErrNativeContext
		}
		return nativeACLObservation{safe: true, digest: sha256.Sum256(buffer[:length])}, nil
	}
	// Only this one variable attribute was requested. Reject negative/unknown
	// offsets, gaps, overlap and truncated or extra payload rather than guessing.
	if offset != 8 || dataLength != length-prefixSize || dataLength < filesecSize {
		return nativeACLObservation{}, ErrNativeContext
	}
	data := buffer[prefixSize:length]
	if binary.LittleEndian.Uint32(data[:4]) != filesecMagic {
		return nativeACLObservation{}, ErrNativeContext
	}
	for _, b := range data[4:36] {
		if b != 0 {
			return nativeACLObservation{}, ErrNativeContext
		}
	}
	count := binary.LittleEndian.Uint32(data[36:40])
	flags := binary.LittleEndian.Uint32(data[40:44])
	// NO_INHERIT prevents ACL replacement; other filesystem-private/deferred
	// flags have unsupported semantics and remain fail-closed.
	if flags & ^uint32(aclNoInherit) != 0 {
		return nativeACLObservation{}, ErrNativeContext
	}
	if count == noACL {
		if len(data) != filesecSize || flags != 0 {
			return nativeACLObservation{}, ErrNativeContext
		}
	} else {
		if count > 128 || len(data) != filesecSize+int(count)*aceSize {
			return nativeACLObservation{}, ErrNativeContext
		}
		for i := 0; i < int(count); i++ {
			entry := data[filesecSize+i*aceSize : filesecSize+(i+1)*aceSize]
			nonzero := false
			for _, b := range entry[:16] {
				nonzero = nonzero || b != 0
			}
			entryFlags := binary.LittleEndian.Uint32(entry[16:20])
			rights := binary.LittleEndian.Uint32(entry[20:24])
			// Deny and an optional INHERITED marker cannot grant beyond the
			// already checked POSIX modes. Inheritance-control, audit/alarm,
			// generic and Windows-interoperability rights are not interpreted.
			if !nonzero || entryFlags&0xf != aceDeny || entryFlags & ^uint32(aceDeny|aceInherited) != 0 || rights == 0 || rights & ^uint32(vnodeRights) != 0 {
				return nativeACLObservation{}, ErrNativeContext
			}
		}
	}
	return nativeACLObservation{safe: true, digest: sha256.Sum256(buffer[:length])}, nil
}
