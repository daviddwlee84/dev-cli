//go:build unix

package sshhost

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPlanInitBlocksUnsafeMetadataWithoutWrites(t *testing.T) {
	service, paths := secureFixtureService(t, "Host old\n")
	if err := os.Chmod(paths.RootConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionBlocked || !hasDiagnostic(plan.Diagnostics, "root_metadata_unsafe") {
		t.Fatalf("plan = %#v", plan)
	}
	for _, path := range []string{paths.ManagedDir, filepath.Join(paths.SSHDir, ".dev.d.lock")} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("planning wrote %s: %v", path, err)
		}
	}
}

func TestInitApplyPreservesBOMCRLFOutsideBytesAndXattr(t *testing.T) {
	service, paths := secureFixtureService(t, "placeholder")
	bom := []byte{0xef, 0xbb, 0xbf}
	original := append(append([]byte{}, bom...), []byte("# top\r\nCompression yes\r\nHost old\r\n  User me\r\n")...)
	if err := os.WriteFile(paths.RootConfig, original, 0o600); err != nil {
		t.Fatal(err)
	}
	attributeName := "user.dev-cli-sshhost-test"
	attributeValue := []byte("preserve-me")
	xattrSupported := unix.Setxattr(paths.RootConfig, attributeName, attributeValue, 0) == nil

	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionUpdate || plan.Newline != "\r\n" || !plan.BOM {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := service.ApplyInit(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Action != ActionUpdate {
		t.Fatalf("result = %#v", result)
	}
	got, err := os.ReadFile(paths.RootConfig)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, bom...), []byte("# top\r\nCompression yes\r\n"+rootIncludeLine()+"\r\nHost old\r\n  User me\r\n")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("root bytes:\n%q\nwant:\n%q", got, want)
	}
	info, err := os.Stat(paths.RootConfig)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("root mode = %v, err %v", info.Mode(), err)
	}
	if xattrSupported {
		size, err := unix.Getxattr(paths.RootConfig, attributeName, nil)
		if err != nil {
			t.Fatal(err)
		}
		value := make([]byte, size)
		read, err := unix.Getxattr(paths.RootConfig, attributeName, value)
		if err != nil || !bytes.Equal(value[:read], attributeValue) {
			t.Fatalf("xattr = %q, err %v", value[:read], err)
		}
	}
	entries, err := os.ReadDir(paths.ManagedDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("managed directory entries = %#v, err %v", entries, err)
	}
}

func TestInitCreatesMissingRootAndPrivateNamespace(t *testing.T) {
	service, paths := secureFixtureService(t, "")
	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionCreate {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := os.Lstat(paths.ManagedDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("planning created managed directory: %v", err)
	}
	result, err := service.ApplyInit(context.Background(), plan)
	if err != nil || !result.Changed {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	content, err := os.ReadFile(paths.RootConfig)
	if err != nil || string(content) != rootIncludeLine()+"\n" {
		t.Fatalf("root content = %q, err %v", content, err)
	}
	for path, mode := range map[string]fs.FileMode{paths.RootConfig: 0o600, paths.ManagedDir: 0o700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, err %v", path, info.Mode(), err)
		}
	}
}

func TestInitExistingIncludeIsNoopAndPreservesMtime(t *testing.T) {
	service, paths := secureFixtureService(t, rootIncludeLine()+"\nHost old\n")
	if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(paths.RootConfig)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanInit(context.Background())
	if err != nil || plan.Action != ActionNoop {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	result, err := service.ApplyInit(context.Background(), plan)
	if err != nil || result.Changed {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	after, err := os.Stat(paths.RootConfig)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v, err %v", before.ModTime(), after.ModTime(), err)
	}
}

func TestInitNoopRevalidatesManagedNamespace(t *testing.T) {
	service, paths := secureFixtureService(t, rootIncludeLine()+"\n")
	if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanInit(context.Background())
	if err != nil || plan.Action != ActionNoop {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	writeFixture(t, filepath.Join(paths.ManagedDir, "foreign.tmp"), "data")
	if _, err := service.ApplyInit(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("ApplyInit error = %v, want source changed", err)
	}
}

func TestInitExistingIncludeCreatesOnlyMissingNamespace(t *testing.T) {
	service, paths := secureFixtureService(t, rootIncludeLine()+"\n")
	plan, err := service.PlanInit(context.Background())
	if err != nil || plan.Action != ActionCreate {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	before, _ := os.Stat(paths.RootConfig)
	result, err := service.ApplyInit(context.Background(), plan)
	if err != nil || !result.Changed {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	after, _ := os.Stat(paths.RootConfig)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("root was replaced while only creating namespace")
	}
}

func TestInitRejectsChangedSourceBeforeAnyMutation(t *testing.T) {
	service, paths := secureFixtureService(t, "Host old\n")
	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.RootConfig, []byte("# changed\nHost old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyInit(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("ApplyInit error = %v", err)
	}
	if _, err := os.Lstat(paths.ManagedDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale apply created namespace: %v", err)
	}
}

func TestInitRevalidatesRootImmediatelyBeforeReplace(t *testing.T) {
	service, paths := secureFixtureService(t, "Host old\n")
	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("# concurrent\nHost old\n")
	service.beforeInitCommit = func() {
		if err := os.WriteFile(paths.RootConfig, changed, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ApplyInit(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("ApplyInit error = %v, want source changed", err)
	}
	got, err := os.ReadFile(paths.RootConfig)
	if err != nil || !bytes.Equal(got, changed) {
		t.Fatalf("concurrent root = %q, err %v", got, err)
	}
}

func TestInitAcceptsConcurrentSuccessfulInclude(t *testing.T) {
	service, paths := secureFixtureService(t, "Host old\n")
	plan, err := service.PlanInit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.RootConfig, []byte(rootIncludeLine()+"\nHost old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyInit(context.Background(), plan)
	if err != nil || result.Action != ActionNoop || result.Changed {
		t.Fatalf("result = %#v, err %v", result, err)
	}
}

func TestPlanInitBlocksUnsafeRootAndNamespace(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, paths Paths)
	}{
		{name: "root symlink", setup: func(t *testing.T, paths Paths) {
			if err := os.Remove(paths.RootConfig); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(paths.Home, "foreign")
			writeFixture(t, target, "Host old\n")
			if err := os.Symlink(target, paths.RootConfig); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "root hardlink", setup: func(t *testing.T, paths Paths) {
			if err := os.Link(paths.RootConfig, filepath.Join(paths.Home, "root-link")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "foreign namespace entry", setup: func(t *testing.T, paths Paths) {
			if err := os.Mkdir(paths.ManagedDir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(paths.ManagedDir, "leftover.tmp"), "data")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, paths := secureFixtureService(t, "Host old\n")
			test.setup(t, paths)
			plan, err := service.PlanInit(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Action != ActionBlocked {
				t.Fatalf("plan = %#v", plan)
			}
		})
	}
}

func TestPlanInitBlocksUnsupportedSecurityXattrWithoutWrites(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux security xattr policy test")
	}
	service, paths := secureFixtureService(t, "Host old\n")
	if err := unix.Setxattr(paths.RootConfig, "security.dev_cli_test", []byte("x"), 0); err != nil {
		t.Skipf("cannot create security xattr as current user: %v", err)
	}
	plan, err := service.PlanInit(context.Background())
	if err != nil || plan.Action != ActionBlocked {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	if _, err := os.Lstat(paths.ManagedDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("blocked planning wrote namespace: %v", err)
	}
}
