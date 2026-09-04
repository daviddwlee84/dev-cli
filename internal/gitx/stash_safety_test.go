package gitx_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestInspectStashSafetyBlocksNestedRepository(t *testing.T) {
	r := gittest.New(t)
	nested := filepath.Join(r.Root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	r.GitIn(nested, "init")
	if err := os.WriteFile(filepath.Join(nested, "content.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection, err := gitx.InspectStashSafety(context.Background(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Safe() || len(inspection.NestedRepositories) != 1 || inspection.NestedRepositories[0] != "nested" {
		t.Fatalf("inspection=%+v", inspection)
	}
}

func TestInspectStashSafetyBlocksDirtySubmodule(t *testing.T) {
	child := gittest.New(t)
	parent := gittest.New(t)
	parent.Git("-c", "protocol.file.allow=always", "submodule", "add", child.Root, "modules/child")
	parent.Git("commit", "-am", "test: add submodule")
	if err := os.WriteFile(filepath.Join(parent.Root, "modules", "child", "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection, err := gitx.InspectStashSafety(context.Background(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Safe() || inspection.DirtySubmodules == 0 {
		t.Fatalf("inspection=%+v", inspection)
	}
}
