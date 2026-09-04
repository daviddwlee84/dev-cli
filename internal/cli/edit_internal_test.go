package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestPrepareTUICapabilityEditRequiresExistingRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("VISUAL", "cmd.exe /c exit 0")
	} else {
		t.Setenv("VISUAL", "true")
	}

	edit, err := prepareTUICapabilityEdit(path)
	if err != nil {
		t.Fatal(err)
	}
	if runErr := edit.Command.Run(); edit.Complete == nil || edit.Complete(runErr) != nil {
		t.Fatalf("capability editor run/complete failed: %v", runErr)
	}
	literalDir := filepath.Join(root, "$CLIENT")
	if err := os.Mkdir(literalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	literalPath := filepath.Join(literalDir, "config.json")
	if err := os.WriteFile(literalPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIENT", "expanded-elsewhere")
	literalEdit, err := prepareTUICapabilityEdit(literalPath)
	if err != nil {
		t.Fatalf("literal-dollar editor path: %v", err)
	}
	if err := literalEdit.Complete(nil); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "config-link")
		if err := os.Symlink(literalPath, link); err != nil {
			t.Fatal(err)
		}
		linkedEdit, err := prepareTUICapabilityEdit(link)
		if err != nil {
			t.Fatalf("symlink editor target: %v", err)
		}
		if err := linkedEdit.Complete(nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := prepareTUICapabilityEdit(root); !errors.Is(err, safefile.ErrUnsafeType) {
		t.Fatalf("directory editor error = %v", err)
	}
	if _, err := prepareTUICapabilityEdit(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing editor error = %v", err)
	}
}

func TestPrepareTUICapabilityEditPreservesWorkingCopyOnRunError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("VISUAL", "cmd.exe /c exit 0")
	} else {
		t.Setenv("VISUAL", "true")
	}
	edit, err := prepareTUICapabilityEdit(path)
	if err != nil {
		t.Fatal(err)
	}
	err = edit.Complete(errors.New("terminal restore failed"))
	if err == nil || !strings.Contains(err.Error(), "working copy preserved at") {
		t.Fatalf("run error = %v", err)
	}
	marker := "working copy preserved at "
	remainder := strings.SplitN(err.Error(), marker, 2)[1]
	recovery, _, found := strings.Cut(remainder, ": ")
	if !found {
		t.Fatalf("recovery path missing: %v", err)
	}
	if _, statErr := os.Stat(recovery); statErr != nil {
		t.Fatalf("working copy was not preserved: %v", statErr)
	}
	_ = os.Remove(recovery)
}

func TestPrepareTUICapabilityEditReplacesOnlyUnchangedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(root, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'edited\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)

	edit, err := prepareTUICapabilityEdit(path)
	if err != nil {
		t.Fatal(err)
	}
	runErr := edit.Command.Run()
	if err := edit.Complete(runErr); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "edited\n" {
		t.Fatalf("edited source = %q, %v", body, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("edited source mode = %v", info.Mode().Perm())
	}

	edit, err = prepareTUICapabilityEdit(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'user-edited\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runErr = edit.Command.Run()
	err = edit.Complete(runErr)
	if err == nil || !strings.Contains(err.Error(), "working copy preserved at") {
		t.Fatalf("conflicting edit = %v", err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || string(body) != "external\n" {
		t.Fatalf("conflict overwrote external source = %q, %v", body, readErr)
	}
	if remainder, ok := strings.CutPrefix(err.Error(), "replace changed capability file; working copy preserved at "); ok {
		if recovery, _, found := strings.Cut(remainder, ": "); found {
			_ = os.Remove(recovery)
		}
	}

	first := filepath.Join(root, "first.json")
	second := filepath.Join(root, "second.json")
	link := filepath.Join(root, "selected.json")
	if err := os.WriteFile(first, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	edit, err = prepareTUICapabilityEdit(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	runErr = edit.Command.Run()
	err = edit.Complete(runErr)
	if err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Fatalf("retargeted capability edit = %v", err)
	}
	if got, _ := os.ReadFile(first); string(got) != "first\n" {
		t.Fatalf("retarget conflict changed original target: %q", got)
	}
	if got, _ := os.ReadFile(second); string(got) != "second\n" {
		t.Fatalf("retarget conflict changed new target: %q", got)
	}
	if marker := "working copy preserved at "; strings.Contains(err.Error(), marker) {
		remainder := strings.SplitN(err.Error(), marker, 2)[1]
		if recovery, _, found := strings.Cut(remainder, ": "); found {
			_ = os.Remove(recovery)
		}
	}
}

func TestReadTUICapabilityFileIsBoundedAndRegular(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("raw-local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readTUICapabilityFile(context.Background(), path)
	if err != nil || strings.TrimSpace(got) != "raw-local-secret" {
		t.Fatalf("read capability file = %q, %v", got, err)
	}
	literalDir := filepath.Join(root, "$CLIENT")
	if err := os.Mkdir(literalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	literalPath := filepath.Join(literalDir, "config.json")
	if err := os.WriteFile(literalPath, []byte("literal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIENT", "expanded-elsewhere")
	if got, err := readTUICapabilityFile(context.Background(), literalPath); err != nil || strings.TrimSpace(got) != "literal" {
		t.Fatalf("literal-dollar capability path = %q, %v", got, err)
	}

	tooLarge := filepath.Join(root, "large.json")
	if err := os.WriteFile(tooLarge, make([]byte, tuiCapabilityFileMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTUICapabilityFile(context.Background(), tooLarge); !errors.Is(err, safefile.ErrFileLimit) {
		t.Fatalf("large capability file error = %v", err)
	}
	if _, err := readTUICapabilityFile(context.Background(), root); !errors.Is(err, safefile.ErrUnsafeType) {
		t.Fatalf("directory capability file error = %v", err)
	}
}
