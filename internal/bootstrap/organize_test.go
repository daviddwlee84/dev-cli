package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/bootstrap"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func scanRepos(t *testing.T, root string) []bootstrap.Repository {
	t.Helper()
	r, warnings := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	if len(warnings) > 0 {
		t.Fatalf("scan warnings: %v", warnings)
	}
	return r
}

func TestFlatIndexPlanAndApply(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "deep", "category", "demo"))
	index := filepath.Join(root, "index")

	plan := bootstrap.IndexPlan(scanRepos(t, root), index, bootstrap.Flat, false)
	ready := plan.Ready()
	if len(ready) != 1 {
		t.Fatalf("want one ready link, got %+v", plan.Actions)
	}
	if ready[0].Target != filepath.Join(index, "demo") {
		t.Errorf("flat target = %q", ready[0].Target)
	}
	created, err := bootstrap.ApplyIndex(plan)
	if err != nil || created != 1 {
		t.Fatalf("ApplyIndex: created=%d err=%v", created, err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(index, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(r.Root)
	if target != want {
		t.Errorf("link resolves to %q, want %q", target, want)
	}

	// Replanning is idempotent: the correct link is current, not ready.
	plan = bootstrap.IndexPlan(scanRepos(t, root), index, bootstrap.Flat, false)
	if len(plan.Ready()) != 0 || plan.Actions[0].State != bootstrap.Current {
		t.Errorf("existing correct link should be current: %+v", plan.Actions)
	}
}

func TestPreserveIndexMirrorsRelativePath(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "Quant", "signals", "demo"))
	index := filepath.Join(t.TempDir(), "index")

	plan := bootstrap.IndexPlan(scanRepos(t, root), index, bootstrap.Preserve, false)
	if len(plan.Ready()) != 1 {
		t.Fatalf("actions: %+v", plan.Actions)
	}
	want := filepath.Join(index, "Quant", "signals", "demo")
	if plan.Ready()[0].Target != want {
		t.Errorf("target = %q, want %q", plan.Ready()[0].Target, want)
	}
}

func TestRelativeIndexLink(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "repos", "demo"))
	index := filepath.Join(root, "index")

	plan := bootstrap.IndexPlan(scanRepos(t, filepath.Join(root, "repos")), index, bootstrap.Flat, true)
	if len(plan.Ready()) != 1 || plan.Ready()[0].RelativeTarget == "" {
		t.Fatalf("want a relative link payload: %+v", plan.Actions)
	}
	if filepath.IsAbs(plan.Ready()[0].RelativeTarget) {
		t.Errorf("payload should be relative: %q", plan.Ready()[0].RelativeTarget)
	}
	if _, err := bootstrap.ApplyIndex(plan); err != nil {
		t.Fatal(err)
	}
	payload, _ := os.Readlink(filepath.Join(index, "demo"))
	if filepath.IsAbs(payload) {
		t.Errorf("actual link payload should be relative: %q", payload)
	}
}

func TestIndexBlocksDuplicateNames(t *testing.T) {
	root := t.TempDir()
	a, b := gittest.New(t), gittest.New(t)
	moveRepo(t, a, filepath.Join(root, "work", "demo"))
	moveRepo(t, b, filepath.Join(root, "personal", "demo"))

	plan := bootstrap.IndexPlan(scanRepos(t, root), filepath.Join(root, "index"), bootstrap.Flat, false)
	if len(plan.Blocked()) != 2 || len(plan.Ready()) != 0 {
		t.Fatalf("both duplicate names should be blocked: %+v", plan.Actions)
	}
	if !strings.Contains(plan.Blocked()[0].Reason, "duplicate") {
		t.Errorf("reason = %q", plan.Blocked()[0].Reason)
	}

	// Preserve layout can represent both without inventing names.
	plan = bootstrap.IndexPlan(scanRepos(t, root), filepath.Join(root, "index"), bootstrap.Preserve, false)
	if len(plan.Ready()) != 2 {
		t.Errorf("preserve layout should have no collision: %+v", plan.Actions)
	}
}

func TestIndexNeverReplacesOccupiedTarget(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "repos", "demo"))
	index := filepath.Join(root, "index")
	os.MkdirAll(filepath.Join(index, "demo"), 0o755)
	os.WriteFile(filepath.Join(index, "demo", "important"), []byte("mine"), 0o644)

	plan := bootstrap.IndexPlan(scanRepos(t, filepath.Join(root, "repos")), index, bootstrap.Flat, false)
	if len(plan.Blocked()) != 1 {
		t.Fatalf("occupied target should block: %+v", plan.Actions)
	}
	if _, err := bootstrap.ApplyIndex(plan); err != nil {
		t.Fatalf("blocked actions are skipped, not fatal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(index, "demo", "important")); err != nil {
		t.Error("occupied target must be untouched")
	}
}

func TestIndexNeverIncludesLinkedWorktrees(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "demo"))
	if err := gitx.AddWorktree(context.Background(), r.Root,
		filepath.Join(root, "worktrees", "demo", "feat-x"), "feat/x", "main"); err != nil {
		t.Fatal(err)
	}

	plan := bootstrap.IndexPlan(scanRepos(t, root), filepath.Join(root, "index"), bootstrap.Flat, false)
	if len(plan.Actions) != 1 || plan.Actions[0].Repo.Kind != bootstrap.Canonical {
		t.Errorf("execution worktrees should not be project entries: %+v", plan.Actions)
	}
}

func TestMovePlanAndApply(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "old", "demo"))
	destRoot := filepath.Join(root, "new")

	plan := bootstrap.MovePlan(context.Background(), scanRepos(t, filepath.Join(root, "old")), destRoot, bootstrap.Flat)
	if len(plan.Ready()) != 1 {
		t.Fatalf("move should be ready: %+v", plan.Actions)
	}
	moved, err := bootstrap.ApplyMoves(plan)
	if err != nil || moved != 1 {
		t.Fatalf("ApplyMoves: moved=%d err=%v", moved, err)
	}
	dest := filepath.Join(destRoot, "demo")
	if _, err := gitx.Discover(context.Background(), dest); err != nil {
		t.Errorf("moved directory should still be a repo: %v", err)
	}
	if _, err := os.Stat(r.Root); !os.IsNotExist(err) {
		t.Error("source should have moved")
	}
}

func TestMoveBlocksDirtyRepo(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "old", "demo"))
	os.WriteFile(filepath.Join(r.Root, "dirty.txt"), []byte("wip"), 0o644)

	plan := bootstrap.MovePlan(context.Background(), scanRepos(t, filepath.Join(root, "old")),
		filepath.Join(root, "new"), bootstrap.Flat)
	if len(plan.Blocked()) != 1 || !strings.Contains(plan.Blocked()[0].Reason, "uncommitted") {
		t.Errorf("dirty repo should be blocked: %+v", plan.Actions)
	}
}

func TestMoveBlocksLinkedWorktreesEvenOutsideScan(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "repos", "demo"))
	// Outside the root being scanned: MovePlan must ask git, not rely on the
	// scanner having happened across it.
	if err := gitx.AddWorktree(context.Background(), r.Root,
		filepath.Join(t.TempDir(), "feat-x"), "feat/x", "main"); err != nil {
		t.Fatal(err)
	}

	plan := bootstrap.MovePlan(context.Background(), scanRepos(t, filepath.Join(root, "repos")),
		filepath.Join(root, "new"), bootstrap.Flat)
	if len(plan.Blocked()) != 1 || !strings.Contains(plan.Blocked()[0].Reason, "linked worktree") {
		t.Errorf("repo with a worktree should be blocked: %+v", plan.Actions)
	}
}

func TestMoveBlocksSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "repos", "demo"))
	os.MkdirAll(filepath.Join(root, "index"), 0o755)
	os.Symlink(r.Root, filepath.Join(root, "index", "demo"))

	plan := bootstrap.MovePlan(context.Background(), scanRepos(t, root),
		filepath.Join(root, "new"), bootstrap.Flat)
	if len(plan.Blocked()) != 1 || !strings.Contains(plan.Blocked()[0].Reason, "symlink aliases") {
		t.Errorf("aliases would break and should block the move: %+v", plan.Actions)
	}
}

func TestMoveBlocksOccupiedTarget(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "old", "demo"))
	dest := filepath.Join(root, "new", "demo")
	os.MkdirAll(dest, 0o755)

	plan := bootstrap.MovePlan(context.Background(), scanRepos(t, filepath.Join(root, "old")),
		filepath.Join(root, "new"), bootstrap.Flat)
	if len(plan.Blocked()) != 1 || !strings.Contains(plan.Blocked()[0].Reason, "target already exists") {
		t.Errorf("occupied target should block: %+v", plan.Actions)
	}
}

func TestParseLayout(t *testing.T) {
	for _, name := range []string{"flat", "preserve"} {
		if got, err := bootstrap.ParseLayout(name); err != nil || string(got) != name {
			t.Errorf("ParseLayout(%q) = %q, %v", name, got, err)
		}
	}
	if _, err := bootstrap.ParseLayout("magic"); err == nil {
		t.Error("unknown layout should error")
	}
}
