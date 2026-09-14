//go:build darwin || linux

package sshvault

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativePathChecksEveryIntermediateSymlinkTarget(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "executable"
		if directory {
			name = "profile"
		}
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal("resolve own fixture root")
			}
			protected, middle, safe := filepath.Join(root, "protected"), filepath.Join(root, "middle"), filepath.Join(root, "safe")
			for _, path := range []string{protected, middle, safe} {
				if os.Mkdir(path, 0o700) != nil {
					t.Fatal("create own path fixture")
				}
			}
			target := safe
			if !directory {
				target = filepath.Join(safe, "bw")
				if os.WriteFile(target, []byte("public fixture"), 0o700) != nil {
					t.Fatal("create own file fixture")
				}
			}
			hop, outer := filepath.Join(middle, "hop"), filepath.Join(protected, "outer")
			if os.Symlink(target, hop) != nil || os.Symlink(hop, outer) != nil {
				t.Fatal("create own indirect links")
			}
			before, err := captureNativePath(outer, directory, directory)
			if err != nil || before.canonical != target {
				t.Fatalf("safe indirect fixture refused: %v", err)
			}
			found := false
			for _, entry := range before.entries {
				if entry.path == middle {
					found = true
				}
			}
			if !found {
				t.Fatal("intermediate target directory was not captured")
			}
			if os.Chmod(middle, 0o777) != nil {
				t.Fatal("make only own intermediate directory unsafe")
			}
			if _, err := captureNativePath(outer, directory, directory); err == nil {
				t.Fatal("protected link hid an unsafe intermediate directory")
			}
		})
	}
}

func TestNativePathDoesNotCleanAwayUnsafeDotDotTraversal(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve own fixture root")
	}
	for _, name := range []string{"protected", "unsafe", "safe"} {
		if os.Mkdir(filepath.Join(root, name), 0o700) != nil {
			t.Fatal("create own traversal fixture")
		}
	}
	if _, err := captureNativePath(filepath.Join(root, "safe"), true, true); err != nil {
		t.Fatal("safe target could not be inspected")
	}
	if os.Chmod(filepath.Join(root, "unsafe"), 0o777) != nil {
		t.Fatal("change only own intermediate mode")
	}
	outer := filepath.Join(root, "protected", "outer")
	if os.Symlink("../unsafe/../safe", outer) != nil {
		t.Fatal("create own dot-dot fixture")
	}
	if _, err := captureNativePath(outer, true, true); err == nil {
		t.Fatal("target normalization erased an unsafe resolution component")
	}
}

func TestNativePathBindsIntermediateRetargetWithSameFinalIdentity(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve own fixture root")
	}
	for _, name := range []string{"protected", "middle", "other", "safe"} {
		if os.Mkdir(filepath.Join(root, name), 0o700) != nil {
			t.Fatal("create own binding fixture")
		}
	}
	target := filepath.Join(root, "safe")
	hop, other, outer := filepath.Join(root, "middle", "hop"), filepath.Join(root, "other", "hop"), filepath.Join(root, "protected", "outer")
	if os.Symlink(target, hop) != nil || os.Symlink(target, other) != nil || os.Symlink(hop, outer) != nil {
		t.Fatal("create own chained links")
	}
	before, err := captureNativePath(outer, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if os.Remove(hop) != nil || os.Symlink(other, hop) != nil {
		t.Fatal("retarget own intermediate link")
	}
	after, err := captureNativePath(outer, true, true)
	if err != nil || before.canonical != after.canonical {
		t.Fatal("safe final fixture identity changed unexpectedly")
	}
	if sameNativePath(before, after) {
		t.Fatal("same final target hid a changed intermediate link")
	}
}

func TestBitwardenPortableProfileRejectsUnsafeOrRetargetedIntermediate(t *testing.T) {
	for _, change := range []string{"unsafe-at-plan", "unsafe-at-apply", "retarget-same-final"} {
		t.Run(change, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			middle := filepath.Join(fixture.root, "middle")
			if os.Mkdir(middle, 0o700) != nil {
				t.Fatal("create own profile intermediate")
			}
			hop := filepath.Join(middle, "hop")
			if os.Symlink(fixture.profile, hop) != nil || os.Symlink(hop, filepath.Join(fixture.bin, "bw-data")) != nil {
				t.Fatal("create own indirect portable profile")
			}
			if _, err := captureNativePath(filepath.Join(fixture.bin, "bw-data"), true, true); err != nil {
				t.Fatal("safe portable fixture could not be inspected")
			}
			if change == "unsafe-at-plan" && os.Chmod(middle, 0o777) != nil {
				t.Fatal("change own intermediate mode")
			}
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if change == "unsafe-at-plan" {
				if err == nil || *generated != 0 {
					t.Fatal("unsafe portable profile acquired a plan")
				}
				if _, err := os.Stat(fixture.calls); !os.IsNotExist(err) {
					t.Fatal("provider ran before resolving the unsafe portable profile")
				}
				fixture.assertNoCreate(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if change == "unsafe-at-apply" {
				if os.Chmod(middle, 0o777) != nil {
					t.Fatal("change own intermediate mode")
				}
			} else {
				other := filepath.Join(fixture.root, "other-hop")
				if os.Symlink(fixture.profile, other) != nil || os.Remove(hop) != nil || os.Symlink(other, hop) != nil {
					t.Fatal("retarget own intermediate link to same profile")
				}
			}
			result, err := service.Apply(context.Background(), plan)
			if err == nil || result.Status != StatusNotStarted || *generated != 0 {
				t.Fatal("changed intermediate profile authority reached generation")
			}
			fixture.assertNoCreate(t)
		})
	}
}
