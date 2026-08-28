package gitx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestAnalyzeFinishSeparatesCommitRelationAndDirtyContent(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	r.Commit("same.txt", "old\n", "chore: add same fixture")
	r.Commit("staged.txt", "old\n", "chore: add staged fixture")
	r.Commit("unique.txt", "old\n", "chore: add unique fixture")
	r.Git("branch", "feat/done-analysis")

	worktree := filepath.Join(t.TempDir(), "feature")
	r.Git("worktree", "add", worktree, "feat/done-analysis")
	r.Commit("same.txt", "from main\n", "feat: update same fixture")
	r.Commit("from-main.txt", "shared\n", "feat: add shared fixture")

	if err := os.WriteFile(filepath.Join(worktree, "same.txt"), []byte("from main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "unique.txt"), []byte("unique work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.GitIn(worktree, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "from-main.txt"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	indexBefore := r.GitIn(worktree, "write-tree")
	objectsBefore := r.GitIn(worktree, "count-objects", "-v")
	analysis, err := gitx.AnalyzeFinish(ctx, worktree, "main", "feat/done-analysis")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Relation.BaseOnly != 2 || analysis.Relation.BranchOnly != 0 || !analysis.Relation.Contained() {
		t.Fatalf("relation = %+v", analysis.Relation)
	}
	if got := r.GitIn(worktree, "write-tree"); got != indexBefore {
		t.Fatalf("analysis changed the real index: before=%s after=%s", indexBefore, got)
	}
	if got := r.GitIn(worktree, "count-objects", "-v"); got != objectsBefore {
		t.Fatalf("analysis wrote repository objects:\nbefore:\n%s\nafter:\n%s", objectsBefore, got)
	}

	byPath := map[string]gitx.DirtyPath{}
	for _, change := range analysis.Changes {
		byPath[change.Path] = change
	}
	for _, path := range []string{"same.txt", "from-main.txt"} {
		if !byPath[path].BaseEquivalent {
			t.Errorf("%s should match main: %+v", path, byPath[path])
		}
	}
	for _, path := range []string{"staged.txt", "unique.txt", "new.txt"} {
		if byPath[path].BaseEquivalent {
			t.Errorf("%s should contain unique content: %+v", path, byPath[path])
		}
	}
	if analysis.EquivalentDirty() != 2 || analysis.UniqueDirty() != 3 {
		t.Fatalf("dirty relation = equivalent %d unique %d", analysis.EquivalentDirty(), analysis.UniqueDirty())
	}

	if err := os.WriteFile(filepath.Join(worktree, "unique.txt"), []byte("changed again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next, err := gitx.AnalyzeFinish(ctx, worktree, "main", "feat/done-analysis")
	if err != nil {
		t.Fatal(err)
	}
	if next.Fingerprint == analysis.Fingerprint {
		t.Fatal("fingerprint did not change with checkout content")
	}
}

func TestCommitAllChangesStagesEverythingAndRunsHooks(t *testing.T) {
	r := gittest.New(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(r.Root, ".git", "hooks", "pre-commit")
	r.Git("config", "core.hooksPath", filepath.Join(r.Root, ".git", "hooks"))
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf ran > \"$DEV_TEST_HOOK_MARKER\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_TEST_HOOK_MARKER", marker)
	r.Write("tracked.txt", "tracked\n")
	r.Git("add", "tracked.txt")
	r.Write("tracked.txt", "tracked again\n")
	r.Write("untracked.txt", "new\n")

	if err := gitx.CommitAllChanges(gittest.Ctx(), r.Root, "chore: finalize fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pre-commit hook did not run: %v", err)
	}
	status, err := gitx.StatusOf(gittest.Ctx(), r.Root)
	if err != nil || status.Dirty() {
		t.Fatalf("checkout after commit = %+v, %v", status, err)
	}
	if got := r.Git("show", "HEAD:tracked.txt"); got != "tracked again" {
		t.Fatalf("tracked content = %q", got)
	}
	if got := r.Git("show", "HEAD:untracked.txt"); got != "new" {
		t.Fatalf("untracked content = %q", got)
	}
}

func TestDiscardAllChangesKeepsIgnoredFiles(t *testing.T) {
	r := gittest.New(t)
	r.Commit(".gitignore", "ignored.txt\n", "chore: add ignore fixture")
	r.Commit("tracked.txt", "base\n", "chore: add tracked fixture")
	r.Write("tracked.txt", "staged\n")
	r.Git("add", "tracked.txt")
	r.Write("tracked.txt", "unstaged\n")
	r.Write("untracked.txt", "remove\n")
	r.Write("ignored.txt", "keep\n")

	if err := gitx.DiscardAllChanges(gittest.Ctx(), r.Root); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(r.Root, "tracked.txt")); err != nil || string(got) != "base\n" {
		t.Fatalf("tracked file = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(r.Root, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file survived discard: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(r.Root, "ignored.txt")); err != nil || string(got) != "keep\n" {
		t.Fatalf("ignored file = %q, %v", got, err)
	}
}
