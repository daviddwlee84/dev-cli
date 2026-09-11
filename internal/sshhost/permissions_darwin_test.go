//go:build darwin

package sshhost

import (
	"context"
	"os/exec"
	"testing"
)

func TestPermissionDarwinACLRequiresManualRemediation(t *testing.T) {
	service, paths := permissionFixture(t)
	// This changes only a test fixture. Native ACLs need a separate query:
	// listxattr is not sufficient to establish that chmod is permission-only.
	command := exec.Command("/bin/chmod", "+a", "everyone allow read", paths.RootConfig)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare fixture ACL: %v %s", err, output)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil || plan.Ready() || plan.Status != "blocked" {
		t.Fatalf("ACL plan=%#v err=%v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err == nil {
		t.Fatal("ACL was implicitly changed")
	}
	if permissionMode(t, paths.RootConfig) != 0o644 || permissionMode(t, paths.SSHDir) != 0o755 {
		t.Fatal("blocked ACL plan changed modes")
	}
}
