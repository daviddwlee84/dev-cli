//go:build darwin

package sshvault

import (
	"encoding/binary"
	"testing"
)

func darwinACLBuffer(flags, rights, aclFlags uint32) []byte {
	buffer := make([]byte, 12+44+24)
	binary.LittleEndian.PutUint32(buffer[:4], uint32(len(buffer)))
	binary.LittleEndian.PutUint32(buffer[4:8], 8)
	binary.LittleEndian.PutUint32(buffer[8:12], uint32(len(buffer)-12))
	binary.LittleEndian.PutUint32(buffer[12:16], 0x012cc16d)
	binary.LittleEndian.PutUint32(buffer[48:52], 1)
	binary.LittleEndian.PutUint32(buffer[52:56], aclFlags)
	buffer[56] = 1 // synthetic, non-null principal GUID
	binary.LittleEndian.PutUint32(buffer[72:76], flags)
	binary.LittleEndian.PutUint32(buffer[76:80], rights)
	return buffer
}

func TestDarwinACLParserAcceptsOnlyKnownNonGrantingForms(t *testing.T) {
	for _, flags := range []uint32{2, 2 | (1 << 4)} {
		for _, aclFlags := range []uint32{0, 1 << 17} {
			for _, rights := range []uint32{1 << 4, 0x3ffe} {
				observation, err := parseDarwinNativeACL(darwinACLBuffer(flags, rights, aclFlags))
				if err != nil || !observation.safe {
					t.Fatal("documented deny-only form rejected")
				}
			}
		}
	}
	for _, offset := range []uint32{0, 8} {
		buffer := make([]byte, 12)
		binary.LittleEndian.PutUint32(buffer[:4], 12)
		binary.LittleEndian.PutUint32(buffer[4:8], offset)
		if observation, err := parseDarwinNativeACL(buffer); err != nil || !observation.safe {
			t.Fatal("well-formed absent ACL rejected")
		}
	}
	for _, count := range []uint32{0, ^uint32(0)} {
		buffer := darwinACLBuffer(2, 1<<4, 0)[:56]
		binary.LittleEndian.PutUint32(buffer[:4], 56)
		binary.LittleEndian.PutUint32(buffer[8:12], 44)
		binary.LittleEndian.PutUint32(buffer[48:52], count)
		if observation, err := parseDarwinNativeACL(buffer); err != nil || !observation.safe {
			t.Fatal("documented empty/no-ACL form rejected")
		}
	}
}

func TestDarwinACLParserRejectsMalformedOrGrantingMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"short-header", func(b []byte) []byte { return b[:8] }},
		{"invalid-total", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[:4], 8); return b }},
		{"truncated-total", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[:4], 100); return b }},
		{"negative-offset", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 0xfffffffc); return b }},
		{"overlap", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 4); return b }},
		{"truncated-payload", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:12], 69); return b }},
		{"unknown-magic", func(b []byte) []byte { b[12] = 0; return b }},
		{"unknown-owner", func(b []byte) []byte { b[16] = 1; return b }},
		{"too-many-entries", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[48:52], 129); return b }},
		{"count-mismatch", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[48:52], 2); return b }},
		{"private-flags", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[52:56], 1); return b }},
		{"deferred-inheritance", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[52:56], 1<<16); return b }},
		{"allow", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[72:76], 1); return b }},
		{"audit", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[72:76], 3); return b }},
		{"inherit-control", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[72:76], 2|(1<<5)); return b }},
		{"generic-right", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[76:80], 1<<21); return b }},
		{"interop-right", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[76:80], 1<<20); return b }},
		{"unknown-right", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[76:80], 1); return b }},
		{"zero-rights", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[76:80], 0); return b }},
		{"null-principal", func(b []byte) []byte { b[56] = 0; return b }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if observation, err := parseDarwinNativeACL(test.change(darwinACLBuffer(2, 1<<4, 0))); err == nil || observation.safe {
				t.Fatal("unknown or granting ACL metadata accepted")
			}
		})
	}
}

func TestDarwinACLObservationBindsAllAcceptedMetadata(t *testing.T) {
	before, err := parseDarwinNativeACL(darwinACLBuffer(2, 1<<4, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]byte){
		func(b []byte) { b[57] = 2 },
		func(b []byte) { binary.LittleEndian.PutUint32(b[72:76], 2|(1<<4)) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[76:80], (1<<4)|(1<<2)) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[52:56], 1<<17) },
	} {
		body := darwinACLBuffer(2, 1<<4, 0)
		change(body)
		after, err := parseDarwinNativeACL(body)
		if err != nil || before == after {
			t.Fatal("accepted ACL metadata change was not bound")
		}
	}
}
