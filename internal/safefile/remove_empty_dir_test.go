package safefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupRegressionEmptyDirectoryRemoval(t *testing.T) {
	for _, kind := range []string{"empty", "nonempty", "file", "replaced-directory", "symlink", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "child")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			root, _, err := OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			expected, err := root.Lstat("child")
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch kind {
			case "nonempty":
				if err := os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "file", "replaced-directory", "symlink":
				if err := os.Rename(path, filepath.Join(base, "original")); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "file":
					err = os.WriteFile(path, []byte("keep"), 0600)
				case "replaced-directory":
					err = os.Mkdir(path, 0700)
				case "symlink":
					err = os.Symlink(t.TempDir(), path)
					if err != nil {
						t.Skipf("symlink creation unavailable: %v", err)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err = RemoveEmptyChildDir(ctx, root, "child", expected)
			if kind == "empty" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("directory remains: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("accepted %s", kind)
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("removed protected path: %v", err)
				}
			}
		})
	}
}

func TestCleanupRegressionEmptyDirectoryPortableNames(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	root, _, err := OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	expected, err := root.Lstat("child")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "child/..", `child\..`, "C:child", "child:stream", "NUL", "child."} {
		if err := RemoveEmptyChildDir(t.Context(), root, name, expected); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if err := RemoveEmptyChildDir(t.Context(), root, "child", nil); err == nil {
		t.Fatal("missing expected identity accepted")
	}
	if err := RemoveEmptyChildDir(t.Context(), nil, "child", expected); err == nil {
		t.Fatal("nil parent accepted")
	}
	if _, err := os.Stat(filepath.Join(base, "child")); err != nil {
		t.Fatal(err)
	}
}

// The backend's final context check provides a deterministic external-writer
// interleaving immediately after identity checks and before native deletion.
// Value intentionally does not expose an embedded cancelCtx to context.Cause.
type directoryMutationContext struct {
	context.Context
	mutate func()
}

func (c *directoryMutationContext) Value(any) any { return nil }
func (c *directoryMutationContext) Err() error {
	if c.mutate != nil {
		mutate := c.mutate
		c.mutate = nil
		mutate()
	}
	return nil
}

func TestCleanupRegressionEmptyDirectoryLateSubstitution(t *testing.T) {
	for _, kind := range []string{"file", "nonempty"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "child")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			root, _, err := OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			expected, err := root.Lstat("child")
			if err != nil {
				t.Fatal(err)
			}
			mutated := false
			ctx := &directoryMutationContext{Context: context.Background(), mutate: func() {
				mutated = true
				if kind == "file" {
					if err := os.Rename(path, filepath.Join(base, "reviewed")); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}}
			err = removeEmptyChildDirNative(ctx, root, "child", expected)
			if !mutated {
				t.Fatalf("native deletion did not reach the race boundary: %v", err)
			}
			file := path
			if kind == "nonempty" {
				file = filepath.Join(path, "keep")
				if err == nil {
					t.Fatal("removed nonempty directory")
				}
			}
			data, readErr := os.ReadFile(file)
			if readErr != nil || string(data) != "keep" {
				t.Fatalf("late file was removed: %q %v (delete: %v)", data, readErr, err)
			}
		})
	}
}

func TestCleanupRegressionEmptyDirectoryExpectedIdentityCannotBeAFile(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	root, _, err := OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	info, err := root.Lstat("file")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveEmptyChildDir(t.Context(), root, "file", info); !errors.Is(err, ErrUnsafeType) {
		t.Fatal(err)
	}
}
