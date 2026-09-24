package lockx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRelocatableLeaseMovesWithTreeAndStillExcludes(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "checkout")
	directory := filepath.Join(parent, "lease")
	movedParent := parent + "-moved"
	moved := filepath.Join(movedParent, "lease")
	for _, ordinaryContender := range []bool{false, true} {
		if err := WithDirRelocatable(t.Context(), directory, "move", func() error {
			if err := os.Rename(parent, movedParent); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			contender := WithDirRelocatable
			if ordinaryContender {
				contender = WithDir
			}
			called := false
			err := contender(ctx, moved, "contender", func() error { called = true; return nil })
			if !errors.Is(err, context.DeadlineExceeded) || called {
				t.Fatalf("moved lease did not exclude contender: called=%t err=%v", called, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := WithDir(t.Context(), moved, "after release", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(movedParent, parent); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRelocatableLeaseRejectsReplacedDirectoryOrNamedFile(t *testing.T) {
	for _, replaceDirectory := range []bool{false, true} {
		t.Run(map[bool]string{false: "lock-file", true: "directory"}[replaceDirectory], func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "lease")
			lease, err := acquireDir(t.Context(), directory, "fixture", true)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			expected, err := directoryIdentity(directory)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(filepath.Dir(directory), "."+filepath.Base(directory)+".lock")
			if lease.lock.file == nil && !replaceDirectory {
				// Native Windows relocation has no descendant lock-file handle.
				// Forging that obsolete path must not bypass the retained mutex.
				if err := os.WriteFile(lockPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
				defer cancel()
				if err := WithDir(ctx, directory, "forged file contender", func() error { return errors.New("bypassed kernel lease") }); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("forged file bypass: %v", err)
				}
				return
			}
			if replaceDirectory {
				if err := os.Rename(directory, directory+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(lockPath, lockPath+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(lockPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateRelocatableLock(directory, expected, lockPath, lease.lock.file); err == nil {
				t.Fatal("replaced authority accepted")
			}
		})
	}
}

func TestRelocatableLeaseCrossProcessExcludesAfterRename(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "checkout")
	directory := filepath.Join(parent, "lease")
	moved := parent + "-moved"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(path, want string) {
		t.Helper()
		if runtime.GOOS == "windows" {
			path = strings.ToUpper(path)
		}
		cmd := exec.Command(executable, "-test.run=^TestDirectoryLeaseProcessHelper$")
		cmd.Env = append(os.Environ(), "DEV_LOCKX_HELPER_PATH="+path, "DEV_LOCKX_HELPER_EXPECT="+want)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child lease: %v\n%s", err, out)
		}
	}
	if err := WithDirRelocatable(t.Context(), directory, "parent", func() error {
		if err := os.Rename(parent, moved); err != nil {
			return err
		}
		run(filepath.Join(moved, "lease"), "blocked")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run(filepath.Join(moved, "lease"), "acquired")
}

func TestDirectoryLeaseProcessHelper(t *testing.T) {
	path := os.Getenv("DEV_LOCKX_HELPER_PATH")
	if path == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	called := false
	err := WithDir(ctx, path, "child", func() error { called = true; return nil })
	want := os.Getenv("DEV_LOCKX_HELPER_EXPECT")
	if want == "blocked" && (!errors.Is(err, context.DeadlineExceeded) || called) {
		t.Fatalf("wanted blocked, called=%t err=%v", called, err)
	}
	if want == "acquired" && (err != nil || !called) {
		t.Fatalf("wanted acquired, called=%t err=%v", called, err)
	}
	if want != "blocked" && want != "acquired" {
		t.Fatal("unexpected helper mode")
	}
}
