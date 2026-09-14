//go:build darwin || linux

package sshvault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func nativeACLFixtureTarget(t *testing.T, fixture *nativeFixture, target string) (string, bool, bool) {
	t.Helper()
	switch target {
	case "executable":
		if os.Chmod(fixture.entry, 0o755) != nil {
			t.Fatal("set own executable fixture mode")
		}
		return fixture.entry, false, false
	case "tool-parent":
		return fixture.bin, true, false
	case "profile":
		return fixture.profile, true, true
	default:
		return filepath.Join(fixture.profile, "data.json"), false, true
	}
}

func TestNativeACLPolicyBlocksBeforeProviderExecution(t *testing.T) {
	for _, target := range []string{"executable", "tool-parent", "profile", "data"} {
		t.Run(target, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			path, directory, private := nativeACLFixtureTarget(t, fixture, target)
			if _, err := captureNativePath(path, directory, private); err != nil {
				t.Fatalf("ACL-free fixture was not inspectable: %v", err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal("stat own ACL fixture")
			}
			addNativeACLFixture(t, path)
			after, err := os.Lstat(path)
			if err != nil || before.Mode() != after.Mode() {
				t.Fatal("ACL fixture changed mode rather than only native ACL metadata")
			}
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err == nil || plan.state != nil || *generated != 0 {
				t.Fatal("ACL-bearing native path acquired creation authority")
			}
			if _, err := os.Stat(fixture.calls); !os.IsNotExist(err) {
				t.Fatal("provider executed before native ACL review completed")
			}
			fixture.assertNoCreate(t)
		})
	}
}

func TestNativeACLOnlyChangeInvalidatesReviewedPlan(t *testing.T) {
	for _, target := range []string{"executable", "tool-parent", "profile", "data"} {
		t.Run(target, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			path, _, _ := nativeACLFixtureTarget(t, fixture, target)
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal("stat own ACL fixture")
			}
			addNativeACLFixture(t, path)
			after, err := os.Lstat(path)
			if err != nil || before.Mode() != after.Mode() || !os.SameFile(before, after) {
				t.Fatal("fixture did not isolate an ACL-only change")
			}
			result, err := service.Apply(context.Background(), plan)
			if err == nil || result.Status != StatusNotStarted || *generated != 0 {
				t.Fatal("ACL-only source change reached private generation")
			}
			fixture.assertNoCreate(t)
		})
	}
}

func TestNativePostWriteACLChangeRetainsReceipt(t *testing.T) {
	fixture := newNativeFixture(t, false)
	runner := &nativeHookRunner{}
	service, _ := countedNativeService(runner)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	runner.after = func(args []string) {
		if nativeCreateArgs(args) {
			addNativeACLFixture(t, filepath.Join(fixture.profile, "data.json"))
		}
	}
	result, err := service.Apply(context.Background(), plan)
	if !errors.Is(err, ErrUnknown) || result.Status != StatusCreated || result.Receipt == nil || result.Receipt.Fingerprint == "" || result.NativeContextStatus != NativeUnknown || CanSelectAgentKey(plan, result) {
		t.Fatal("post-write ACL uncertainty lost its receipt or authorized agent selection")
	}
}
