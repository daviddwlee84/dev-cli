//go:build linux

package sshvault

import (
	"encoding/binary"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func addNativeACLFixture(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal("stat owned ACL fixture")
	}
	mode := info.Mode().Perm()
	// Keep the mask equal to the existing group mode to isolate an ACL-only
	// change. Named entries remain unsupported even when they do not widen it.
	entries := []struct {
		tag, permissions uint16
		id               uint32
	}{
		{1, uint16(mode>>6) & 7, 0xffffffff},
		{2, 0, uint32(os.Geteuid() + 1)},
		{4, uint16(mode>>3) & 7, 0xffffffff},
		{16, uint16(mode>>3) & 7, 0xffffffff},
		{32, uint16(mode) & 7, 0xffffffff},
	}
	body := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(body[:4], 2)
	for i, entry := range entries {
		offset := 4 + 8*i
		binary.LittleEndian.PutUint16(body[offset:], entry.tag)
		binary.LittleEndian.PutUint16(body[offset+2:], entry.permissions)
		binary.LittleEndian.PutUint32(body[offset+4:], entry.id)
	}
	if err := unix.Setxattr(path, "system.posix_acl_access", body, 0); err != nil {
		t.Skip("filesystem cannot create the owned native ACL fixture")
	}
}
