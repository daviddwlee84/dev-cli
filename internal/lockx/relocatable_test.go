package lockx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
			expected, err := os.Stat(directory)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := lease.lock.file.Name()
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
