//go:build windows

package sshhost

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func windowsFixtureService(t *testing.T) (*Service, Paths) {
	t.Helper()
	paths, err := NewPaths(fixtureHome(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(paths, managedFixtureRunner{paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	return service, paths
}

func TestWindowsInitAndManagedApplyUseProtectedACLs(t *testing.T) {
	service, paths := windowsFixtureService(t)
	plan, err := service.PlanInit(context.Background())
	if err != nil || plan.Action != ActionCreate {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	result, err := service.ApplyInit(context.Background(), plan)
	if err != nil || !result.Changed {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	for _, path := range []string{paths.SSHDir, paths.ManagedDir} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := platformValidatePrivateDirectory(path, info); err != nil {
			t.Fatalf("directory ACL %s: %v", path, err)
		}
	}
	if _, err := readSecureFile(paths.RootConfig, true); err != nil {
		t.Fatalf("root ACL: %v", err)
	}
	managedPlan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "host"})
	if err != nil || !managedPlan.Ready() {
		t.Fatalf("managed plan = %#v, err %v", managedPlan, err)
	}
	managed, err := service.ApplyManaged(context.Background(), managedPlan)
	if err != nil || !managed.Changed {
		t.Fatalf("managed result = %#v, err %v", managed, err)
	}
	if _, err := readSecureFile(filepath.Join(paths.ManagedDir, "lab.conf"), false); err != nil {
		t.Fatalf("managed ACL: %v", err)
	}
}

func TestWindowsPlanInitBlocksInheritedRootDACLWithoutWrites(t *testing.T) {
	service, paths := windowsFixtureService(t)
	if err := platformMakePrivateDirectory(paths.SSHDir); err != nil {
		t.Fatal(err)
	}
	// The new file inherits safe principals but is not itself protected. Root
	// mutation must clone only metadata that satisfies the exact protected DACL
	// contract, rather than silently rewriting it during planning.
	if err := os.WriteFile(paths.RootConfig, []byte("Host old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionBlocked {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := os.Lstat(paths.ManagedDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("blocked plan created managed directory: %v", err)
	}
}

func TestWindowsRejectsReparseManagedComponent(t *testing.T) {
	service, paths := windowsFixtureService(t)
	if err := platformMakePrivateDirectory(paths.SSHDir); err != nil {
		t.Fatal(err)
	}
	realDirectory := filepath.Join(paths.Home, "real")
	if err := platformMakePrivateDirectory(realDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, paths.ManagedDir); err != nil {
		t.Skipf("Windows symlink privilege/developer mode unavailable: %v", err)
	}
	plan, err := service.PlanUpsert(context.Background(), ManagedDefinition{Alias: "lab", HostName: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionBlocked {
		t.Fatalf("reparse-point plan = %#v", plan)
	}
}
