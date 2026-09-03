package safefile_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestOpenRootRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := safefile.OpenRoot(link); !errors.Is(err, safefile.ErrUnsafeType) {
		t.Fatalf("OpenRoot symlink error = %v", err)
	}
}

func TestHeldChildRootDetectsNameReplacementWithoutFollowingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming an open directory is not portable to Windows")
	}
	rootPath := t.TempDir()
	childPath := filepath.Join(rootPath, "child")
	movedPath := filepath.Join(rootPath, "moved")
	outside := t.TempDir()
	if err := os.Mkdir(childPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "inside.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	child, held, err := safefile.OpenChildRoot(root, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if err := os.Rename(childPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, childPath); err != nil {
		t.Fatal(err)
	}

	data, _, err := safefile.ReadStableRegular(t.Context(), child, "inside.txt", nil, 1024)
	if err != nil || string(data) != "inside\n" {
		t.Fatalf("held read = %q, %v", data, err)
	}
	if err := safefile.VerifyChildRoot(root, "child", held); !errors.Is(err, safefile.ErrChanged) {
		t.Fatalf("replacement verification error = %v", err)
	}
}

func TestReadStableRegularRejectsReplacementAndLimits(t *testing.T) {
	rootPath := t.TempDir()
	path := filepath.Join(rootPath, "value")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	expected, err := root.Lstat("value")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := safefile.ReadStableRegular(t.Context(), root, "value", expected, 4); !errors.Is(err, safefile.ErrFileLimit) {
		t.Fatalf("bounded read error = %v", err)
	}
	if err := os.WriteFile(path, []byte("mutated in place"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := safefile.ReadStableRegular(t.Context(), root, "value", expected, 1024); !errors.Is(err, safefile.ErrChanged) {
		t.Fatalf("in-place mutation error = %v", err)
	}

	replacement := filepath.Join(rootPath, "replacement")
	if err := os.WriteFile(replacement, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := safefile.ReadStableRegular(t.Context(), root, "value", expected, 1024); !errors.Is(err, safefile.ErrChanged) {
		t.Fatalf("stale read error = %v", err)
	}
}

func TestReadStableRegularDetectsSameSizeRewriteWithRestoredModTime(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("change-time comparison is implemented on Darwin and Linux")
	}
	rootPath := t.TempDir()
	path := filepath.Join(rootPath, "value")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	expected, err := root.Lstat("value")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, expected.ModTime(), expected.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := safefile.ReadStableRegular(t.Context(), root, "value", expected, 1024); !errors.Is(err, safefile.ErrChanged) {
		t.Fatalf("restored-mtime rewrite error = %v", err)
	}
	if _, err := safefile.AtomicReplacePrivate(t.Context(), root, "value", expected, []byte("unsafe"), false); !errors.Is(err, safefile.ErrReplaceTarget) {
		t.Fatalf("restored-mtime replacement error = %v", err)
	}
	assertFile(t, path, "after!", 0o600)
}

func TestReadStableRegularRejectsLinksAndNonRegularEntries(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "regular"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular", filepath.Join(rootPath, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"directory", "link"} {
		if _, _, err := safefile.ReadStableRegular(t.Context(), root, name, nil, 1024); !errors.Is(err, safefile.ErrUnsafeType) {
			t.Errorf("ReadStableRegular(%q) error = %v", name, err)
		}
	}
}

func TestAtomicCreateNoClobberAndPrivateReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	created, err := safefile.CreatePrivateNoClobber(t.Context(), root, "secret.env", []byte("old\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if created.Mode().Perm() != 0o600 {
		t.Fatalf("created mode = %o", created.Mode().Perm())
	}
	if _, err := safefile.CreatePrivateNoClobber(t.Context(), root, "secret.env", []byte("clobber\n"), false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create error = %v, want fs.ErrExist", err)
	}
	assertFile(t, filepath.Join(rootPath, "secret.env"), "old\n", 0o600)

	replaced, err := safefile.AtomicReplacePrivate(t.Context(), root, "secret.env", created, []byte("new\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Mode().Perm() != 0o700 {
		t.Fatalf("replacement mode = %o", replaced.Mode().Perm())
	}
	assertFile(t, filepath.Join(rootPath, "secret.env"), "new\n", 0o700)
	if _, err := safefile.AtomicReplacePrivate(t.Context(), root, "secret.env", created, []byte("stale\n"), false); !errors.Is(err, safefile.ErrReplaceTarget) {
		t.Fatalf("stale replacement error = %v", err)
	}

	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dev-safefile-") {
			t.Errorf("staging file was not removed: %s", entry.Name())
		}
	}
}

func TestAtomicReplaceRejectsSymlinkAndSpecialTarget(t *testing.T) {
	rootPath := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := safefile.CreatePrivateNoClobber(t.Context(), root, "link", []byte("bad"), false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("create over symlink error = %v", err)
	}
	for _, name := range []string{"link", "directory"} {
		expected, err := root.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := safefile.AtomicReplacePrivate(t.Context(), root, name, expected, []byte("bad"), false); !errors.Is(err, safefile.ErrReplaceTarget) {
			t.Errorf("AtomicReplacePrivate(%q) error = %v", name, err)
		}
	}
	body, err := os.ReadFile(outside)
	if err != nil || string(body) != "outside\n" {
		t.Fatalf("outside target changed: %q, %v", body, err)
	}
}

func TestFilePrimitivesRejectNonPortableNames(t *testing.T) {
	rootPath := t.TempDir()
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{".git", "NUL.txt", "bad\\name", "control\x1b", "bidi" + string(rune(0x202e))} {
		if _, err := safefile.CreatePrivateNoClobber(t.Context(), root, name, []byte("bad"), false); err == nil {
			t.Errorf("CreatePrivateNoClobber(%q) unexpectedly succeeded", name)
		}
		if _, _, err := safefile.ReadStableRegular(t.Context(), root, name, nil, 1024); err == nil {
			t.Errorf("ReadStableRegular(%q) unexpectedly succeeded", name)
		}
	}
}

func TestPrivateWritesEnforceCompiledFileLimit(t *testing.T) {
	root, _, err := safefile.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data := make([]byte, safefile.CompiledMaxFileBytes+1)
	if _, err := safefile.CreatePrivateNoClobber(t.Context(), root, "large", data, false); !errors.Is(err, safefile.ErrFileLimit) {
		t.Fatalf("oversized private create error = %v", err)
	}
}

func TestCreateNoClobberPreservesExplicitNonSecretMode(t *testing.T) {
	rootPath := t.TempDir()
	root, _, err := safefile.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	info, err := safefile.CreateNoClobber(t.Context(), root, "template.sh", []byte("#!/bin/sh\n"), 0o751)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func assertFile(t *testing.T, path, want string, mode fs.FileMode) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != want {
		t.Fatalf("%s body = %q, want %q", path, body, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}
