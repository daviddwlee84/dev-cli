//go:build darwin

package sshvault

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func changeDarwinACLFixture(t *testing.T, operation, entry, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "/bin/chmod", operation, entry, path).Run() != nil {
		t.Fatal("change native ACL only on owned temporary fixture")
	}
}

func protectiveACLFixture(t *testing.T, path, entry string) {
	t.Helper()
	changeDarwinACLFixture(t, "+a", entry, path)
	t.Cleanup(func() {
		// Remove only the exact deny ACE this test added to its own fixture,
		// allowing normal temporary-directory cleanup. Runtime never repairs ACLs.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if exec.CommandContext(ctx, "/bin/chmod", "-a", entry, path).Run() != nil {
			t.Error("remove exact deny ACE from owned temporary fixture")
		}
	})
}

func TestDarwinProtectiveAncestorDenyOnlyACLAccepted(t *testing.T) {
	fixture := newNativeFixture(t, false)
	protectiveACLFixture(t, fixture.root, "everyone deny delete")
	service, generated := countedNativeService(nil)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatalf("documented protective ancestor ACL was refused: %v", err)
	}
	result, err := service.Apply(context.Background(), plan)
	if err != nil || result.Status != StatusCreated || *generated != 1 || !CanSelectAgentKey(plan, result) {
		t.Fatalf("protective ACL did not retain guarded native behavior: %v", err)
	}
}

func TestDarwinProtectiveACLDoesNotExemptAdditionalGrant(t *testing.T) {
	fixture := newNativeFixture(t, false)
	protectiveACLFixture(t, fixture.root, "everyone deny delete")
	service, generated := countedNativeService(nil)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	changeDarwinACLFixture(t, "+a", "everyone allow write", fixture.root)
	result, err := service.Apply(context.Background(), plan)
	if err == nil || result.Status != StatusNotStarted || *generated != 0 {
		t.Fatal("protective deny entry hid an additional allow entry")
	}
	fixture.assertNoCreate(t)
}

func TestDarwinAcceptedACLOnlyChangeInvalidatesPlan(t *testing.T) {
	for _, target := range []string{"ancestor", "data"} {
		t.Run(target, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			path := fixture.root
			second := "everyone deny delete_child"
			if target == "data" {
				path = filepath.Join(fixture.profile, "data.json")
				second = "everyone deny write"
			}
			protectiveACLFixture(t, path, "everyone deny delete")
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal("stat own ACL fixture")
			}
			protectiveACLFixture(t, path, second)
			after, err := os.Lstat(path)
			if err != nil || before.Mode() != after.Mode() || !os.SameFile(before, after) {
				t.Fatal("fixture did not isolate an ACL metadata change")
			}
			result, err := service.Apply(context.Background(), plan)
			if !errors.Is(err, ErrStale) || result.Status != StatusNotStarted || *generated != 0 {
				t.Fatal("changed accepted deny-only ACL escaped full metadata binding")
			}
			fixture.assertNoCreate(t)
		})
	}
}

func TestDarwinPostWriteDenyOnlyChangeRetainsReceipt(t *testing.T) {
	fixture := newNativeFixture(t, false)
	runner := &nativeHookRunner{}
	service, _ := countedNativeService(runner)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	runner.after = func(args []string) {
		if nativeCreateArgs(args) {
			protectiveACLFixture(t, filepath.Join(fixture.profile, "data.json"), "everyone deny delete")
		}
	}
	result, err := service.Apply(context.Background(), plan)
	if !errors.Is(err, ErrUnknown) || result.Status != StatusCreated || result.Receipt == nil || result.Receipt.Fingerprint == "" || result.NativeContextStatus != NativeUnknown || CanSelectAgentKey(plan, result) {
		t.Fatal("post-write deny-only metadata change lost receipt or authorized selection")
	}
}

func addNativeACLFixture(t *testing.T, path string) {
	t.Helper()
	// The grants leave mode bits unchanged; only an ACL-aware guard catches them.
	changeDarwinACLFixture(t, "+a", "everyone allow read,write,execute", path)
}
