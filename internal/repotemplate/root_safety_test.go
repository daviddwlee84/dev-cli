package repotemplate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHeldSourceDirectoryDoesNotFollowReplacementSymlink(t *testing.T) {
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

	root, _, err := openNamedRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	child, held, err := openChildRoot(root, "child")
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

	snapshot := Snapshot{}
	if err := snapshotWholeRoot(context.Background(), child, "", &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 1 || snapshot.Files[0].Path != "inside.txt" || string(snapshot.Files[0].Data) != "inside\n" {
		t.Fatalf("held snapshot escaped to replacement: %+v", snapshot.Files)
	}
	if err := verifyChildRoot(root, "child", held); err == nil {
		t.Fatal("replacement symlink was not detected after held-root traversal")
	}
}

func TestHeldDestinationDirectoryDoesNotWriteThroughReplacementSymlink(t *testing.T) {
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

	root, _, err := openNamedRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	child, held, err := openChildRoot(root, "child")
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

	file := File{Path: "created.txt", Mode: 0o640, Data: []byte("held destination\n")}
	if err := writeRootFile(child, filepath.Base(file.Path), file); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(movedPath, file.Path)); err != nil || string(body) != "held destination\n" {
		t.Fatalf("held destination = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(outside, file.Path)); !os.IsNotExist(err) {
		t.Fatalf("replacement symlink received a write: %v", err)
	}
	if err := verifyChildRoot(root, "child", held); err == nil {
		t.Fatal("replacement symlink was not detected after held-root write")
	}
}
