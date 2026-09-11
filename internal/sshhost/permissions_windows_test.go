//go:build windows

package sshhost

import (
	"context"
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPermissionWindowsInspectsNativeACLAndDoesNotRepairIt(t *testing.T) {
	paths, err := NewPaths(fixtureHome(t))
	if err != nil {
		t.Fatal(err)
	}
	makeFixturePrivateDirectory(t, paths.SSHDir)
	if err := os.WriteFile(paths.RootConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	protectFixtureFile(t, paths.RootConfig)
	service, err := NewService(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil || !plan.Ready() || plan.NeedsRepair() {
		t.Fatalf("private native ACL: %#v %v", plan, err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(paths.RootConfig, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(descriptor)
	defer protectFixtureFile(t, paths.RootConfig)
	plan, err = service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil || plan.Ready() || len(plan.Changes) != 0 || len(plan.Diagnostics) == 0 {
		t.Fatalf("unsupported ACL offered chmod: %#v %v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err == nil {
		t.Fatal("native ACL was implicitly repaired")
	}
}
