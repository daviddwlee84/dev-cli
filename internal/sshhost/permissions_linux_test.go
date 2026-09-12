//go:build linux

package sshhost

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPermissionLinuxACLRequiresManualRemediation(t *testing.T) {
	service, paths := permissionFixture(t)
	data := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(data[:4], 2)
	entries := []struct {
		tag, permissions uint16
		id               uint32
	}{
		{1, 6, 0xffffffff}, {2, 4, uint32(os.Geteuid() + 1)}, {4, 0, 0xffffffff}, {16, 4, 0xffffffff}, {32, 0, 0xffffffff},
	}
	for index, entry := range entries {
		offset := 4 + index*8
		binary.LittleEndian.PutUint16(data[offset:], entry.tag)
		binary.LittleEndian.PutUint16(data[offset+2:], entry.permissions)
		binary.LittleEndian.PutUint32(data[offset+4:], entry.id)
	}
	if err := unix.Setxattr(paths.RootConfig, "system.posix_acl_access", data, 0); err != nil {
		t.Skipf("filesystem cannot create fixture ACL: %v", err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil || plan.Ready() || plan.Status != "blocked" {
		t.Fatalf("ACL plan=%#v err=%v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err == nil {
		t.Fatal("ACL was implicitly changed")
	}
	if permissionMode(t, paths.SSHDir) != 0o755 {
		t.Fatal("blocked ACL plan changed modes")
	}
}
