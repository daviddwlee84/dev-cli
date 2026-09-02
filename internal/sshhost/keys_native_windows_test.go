//go:build windows

package sshhost

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsNativeSSHKeygenPublishesProtectedPair(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen.exe"); err != nil {
		t.Skipf("ssh-keygen.exe unavailable: %v", err)
	}
	service, paths := windowsFixtureService(t)
	initPlan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyInit(context.Background(), initPlan); err != nil {
		t.Fatal(err)
	}
	service.runner = ExecRunner{}
	destination := filepath.Join(paths.SSHDir, "id_windows_native")
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: destination,
		Comment: "dev-cli-windows-test", NoPassphrase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyKey(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Retained {
		t.Fatalf("result = %#v", result)
	}
	for _, path := range []string{destination, destination + ".pub"} {
		if _, err := readSecureFile(path, false); err != nil {
			t.Fatalf("generated file does not have protected DACL %s: %v", path, err)
		}
	}
}
