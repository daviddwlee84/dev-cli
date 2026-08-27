package gitx_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestDiscover(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()

	repo, err := gitx.Discover(ctx, r.Root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if repo.Root != resolve(t, r.Root) {
		t.Errorf("Root = %q, want %q", repo.Root, r.Root)
	}
	if repo.IsLinkedWorktree {
		t.Error("main checkout should not report as a linked worktree")
	}
	if repo.Name != "repo" {
		t.Errorf("Name = %q, want repo", repo.Name)
	}
}

func TestDiscoverNotARepo(t *testing.T) {
	if _, err := gitx.Discover(gittest.Ctx(), t.TempDir()); !errors.Is(err, gitx.ErrNotARepo) {
		t.Errorf("want ErrNotARepo, got %v", err)
	}
}

func TestLinkedWorktreeSharesRepoKey(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	wtPath := filepath.Join(t.TempDir(), "feat-auth")

	if err := gitx.AddWorktree(ctx, r.Root, wtPath, "feat/auth", "main"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	main, err := gitx.Discover(ctx, r.Root)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := gitx.Discover(ctx, wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if main.Key() != linked.Key() {
		t.Errorf("worktrees of one clone must share a key: %q vs %q", main.Key(), linked.Key())
	}
	if !linked.IsLinkedWorktree {
		t.Error("linked worktree should report IsLinkedWorktree")
	}
	if linked.Name != "repo" {
		t.Errorf("linked worktree Name = %q, want the repo name not the worktree dir", linked.Name)
	}
	if linked.MainRoot != main.Root {
		t.Errorf("MainRoot = %q, want %q", linked.MainRoot, main.Root)
	}
}

func TestWorktreesListing(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	wtPath := filepath.Join(t.TempDir(), "feat-auth")
	if err := gitx.AddWorktree(ctx, r.Root, wtPath, "feat/auth", "main"); err != nil {
		t.Fatal(err)
	}

	list, err := gitx.Worktrees(ctx, r.Root)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 worktrees, got %d: %+v", len(list), list)
	}
	if !list[0].Main {
		t.Error("first entry should be the main checkout")
	}
	if list[1].Branch != "feat/auth" {
		t.Errorf("branch = %q, want feat/auth (refs/heads/ prefix must be stripped)", list[1].Branch)
	}

	got, ok, err := gitx.WorktreeFor(ctx, r.Root, "feat/auth")
	if err != nil || !ok {
		t.Fatalf("WorktreeFor: ok=%v err=%v", ok, err)
	}
	if got.Path != resolve(t, wtPath) {
		t.Errorf("path = %q, want %q", got.Path, wtPath)
	}
	if _, ok, _ := gitx.WorktreeFor(ctx, r.Root, "nope"); ok {
		t.Error("WorktreeFor should not match a nonexistent branch")
	}
}

// AddWorktree must branch from the explicit base, not from wherever HEAD
// happens to be — the failure mode called out in the design notes.
func TestAddWorktreeUsesExplicitBase(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	r.Branch("feature/A")
	r.Commit("a.txt", "a\n", "feat: work on A")
	// Standing on feature/A, create a worktree explicitly based on main.
	wtPath := filepath.Join(t.TempDir(), "fix-x")
	if err := gitx.AddWorktree(ctx, r.Root, wtPath, "fix/x", "main"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if _, err := gitx.Run(ctx, wtPath, "merge-base", "--is-ancestor", "fix/x", "main"); err != nil {
		t.Error("fix/x should be based on main, but it carries feature/A commits")
	}
}

func TestAddWorktreeChecksOutExistingBranch(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	r.Branch("feat/existing")
	r.Commit("e.txt", "e\n", "feat: existing")
	r.Git("switch", "main")

	wtPath := filepath.Join(t.TempDir(), "existing")
	if err := gitx.AddWorktree(ctx, r.Root, wtPath, "feat/existing", "main"); err != nil {
		t.Fatalf("AddWorktree on an existing branch: %v", err)
	}
	// The pre-existing commit must still be there: the branch was checked out,
	// not recreated from base.
	if _, err := gitx.Run(ctx, wtPath, "cat-file", "-e", "HEAD:e.txt"); err != nil {
		t.Error("existing branch was recreated from base instead of checked out")
	}
}

func TestRemoveWorktree(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	wtPath := filepath.Join(t.TempDir(), "tmp")
	if err := gitx.AddWorktree(ctx, r.Root, wtPath, "tmp/branch", "main"); err != nil {
		t.Fatal(err)
	}
	if err := gitx.RemoveWorktree(ctx, r.Root, wtPath, false); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	// Removing a checkout must never delete the branch.
	if !gitx.BranchExists(ctx, r.Root, "tmp/branch") {
		t.Error("worktree remove must not delete the branch")
	}
}

func TestRemoveDirtyWorktreeNeedsForce(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	wtPath := filepath.Join(t.TempDir(), "dirty")
	if err := gitx.AddWorktree(ctx, r.Root, wtPath, "dirty/branch", "main"); err != nil {
		t.Fatal(err)
	}
	r.GitIn(wtPath, "config", "user.email", "d@e.test")
	if err := writeFile(filepath.Join(wtPath, "scratch.txt"), "wip\n"); err != nil {
		t.Fatal(err)
	}
	if err := gitx.RemoveWorktree(ctx, r.Root, wtPath, false); err == nil {
		t.Fatal("removing a dirty worktree without --force must fail")
	}
	if err := gitx.RemoveWorktree(ctx, r.Root, wtPath, true); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
}

func TestStatus(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()

	st, err := gitx.StatusOf(ctx, r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || st.Dirty() || st.Published() {
		t.Errorf("fresh repo: %+v", st)
	}
	if st.Summary() != "local" {
		t.Errorf("Summary of an unpublished clean branch = %q, want local", st.Summary())
	}

	r.Write("new.txt", "x\n")
	r.Write("staged.txt", "y\n")
	r.Git("add", "staged.txt")
	st, _ = gitx.StatusOf(ctx, r.Root)
	if st.Untracked != 1 || st.Staged != 1 || st.Changed != 2 {
		t.Errorf("want 2 unique paths: 1 untracked + 1 staged, got %+v", st)
	}
	if st.Summary() != "+1 ?1" {
		t.Errorf("rich Summary = %q", st.Summary())
	}
	if !st.Dirty() {
		t.Error("should be dirty")
	}
}

func TestStatusAheadBehind(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	r.WithRemote()

	st, _ := gitx.StatusOf(ctx, r.Root)
	if !st.Published() || !st.Synced() || st.Summary() != "clean" {
		t.Errorf("just-pushed branch: %+v summary=%q", st, st.Summary())
	}

	r.Commit("ahead.txt", "a\n", "feat: local only")
	r.Commit("ahead2.txt", "a\n", "feat: local only 2")
	st, _ = gitx.StatusOf(ctx, r.Root)
	if st.Ahead != 2 || st.Behind != 0 {
		t.Errorf("want ahead=2, got %+v", st)
	}
	if st.Summary() != "⇡2" {
		t.Errorf("Summary = %q, want ⇡2", st.Summary())
	}
}

func TestWipCommit(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()

	// Clean tree: nothing to check point, and that is not an error.
	made, err := gitx.WipCommit(ctx, r.Root, "wip: checkpoint")
	if err != nil || made {
		t.Fatalf("clean tree: made=%v err=%v", made, err)
	}

	r.Write("draft.txt", "half done\n")
	made, err = gitx.WipCommit(ctx, r.Root, "wip: checkpoint token refresh")
	if err != nil || !made {
		t.Fatalf("dirty tree: made=%v err=%v", made, err)
	}
	st, _ := gitx.StatusOf(ctx, r.Root)
	if st.Dirty() {
		t.Error("tree should be clean after a wip commit, including untracked files")
	}
	_, subject, _ := gitx.LastCommit(ctx, r.Root)
	if subject != "wip: checkpoint token refresh" {
		t.Errorf("subject = %q", subject)
	}
}

func TestDefaultBranch(t *testing.T) {
	r := gittest.New(t)
	if got := gitx.DefaultBranch(gittest.Ctx(), r.Root); got != "main" {
		t.Errorf("DefaultBranch = %q, want main", got)
	}
}

func TestBranchAndRefExists(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	if !gitx.BranchExists(ctx, r.Root, "main") {
		t.Error("main should exist")
	}
	if gitx.BranchExists(ctx, r.Root, "nope") {
		t.Error("nope should not exist")
	}
	if gitx.BranchExists(ctx, r.Root, "") {
		t.Error("empty branch name should never report as existing")
	}
	if !gitx.RefExists(ctx, r.Root, "HEAD") {
		t.Error("HEAD should resolve")
	}
	if gitx.RefExists(ctx, r.Root, "refs/heads/absent") {
		t.Error("absent ref should not resolve")
	}
}

func TestStatusRichChangeCounts(t *testing.T) {
	r := gittest.New(t)
	ctx := gittest.Ctx()
	r.Commit("modify.txt", "old\n", "chore: add modified fixture")
	r.Commit("delete.txt", "gone soon\n", "chore: add delete fixture")
	r.Commit("rename.txt", "move me\n", "chore: add rename fixture")

	r.Write("modify.txt", "new\n")
	if err := os.Remove(filepath.Join(r.Root, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	r.Git("mv", "rename.txt", "renamed.txt")
	r.Write("untracked.txt", "new\n")

	st, err := gitx.StatusOf(ctx, r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 4 {
		t.Errorf("Changed = %d, want 4 unique paths: %+v", st.Changed, st)
	}
	if st.Staged != 1 || st.Unstaged != 2 || st.Untracked != 1 {
		t.Errorf("stage categories wrong: %+v", st)
	}
	if st.Modified != 1 || st.Deleted != 1 || st.Renamed != 1 {
		t.Errorf("change types wrong: %+v", st)
	}
	if got := st.Summary(); got != "+1 !2 ?1" {
		t.Errorf("Summary = %q", got)
	}
	if got := st.Breakdown(); got != "4 changed paths (+1 staged, !2 unstaged, ?1 untracked)" {
		t.Errorf("Breakdown = %q", got)
	}
	if st.LatestChange.IsZero() {
		t.Error("dirty files should contribute their latest edit time")
	}
}

func TestStatusDoesNotDoubleCountPathStagedAndUnstaged(t *testing.T) {
	r := gittest.New(t)
	r.Commit("both.txt", "base\n", "chore: add fixture")
	r.Write("both.txt", "staged\n")
	r.Git("add", "both.txt")
	r.Write("both.txt", "changed again\n")

	st, err := gitx.StatusOf(gittest.Ctx(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 1 || st.Staged != 1 || st.Unstaged != 1 {
		t.Errorf("one path with two states should be unique once: %+v", st)
	}
	if st.Summary() != "+1 !1" {
		t.Errorf("Summary = %q", st.Summary())
	}
}

func TestStatusSummaryDivergenceAndConflicts(t *testing.T) {
	st := gitx.Status{Ahead: 3, Behind: 2, Changed: 2, Conflicted: 1, Untracked: 1}
	if got := st.Summary(); got != "⇕⇡3⇣2 =1 ?1" {
		t.Errorf("Summary = %q", got)
	}
	if got := (gitx.Status{}).Breakdown(); got != "0 changed paths" {
		t.Errorf("clean Breakdown = %q", got)
	}
}

func TestStatusLatestChangeHandlesSpacesInPath(t *testing.T) {
	r := gittest.New(t)
	r.Commit("file with spaces.txt", "old\n", "chore: spaced fixture")
	r.Write("file with spaces.txt", "new\n")
	r.Write("another new file.txt", "new\n")

	st, err := gitx.StatusOf(gittest.Ctx(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 2 || st.LatestChange.IsZero() {
		t.Errorf("space-containing paths should be parsed intact: %+v", st)
	}
}

func TestRemoteFromConfig(t *testing.T) {
	r := gittest.New(t)
	r.Git("remote", "add", "origin", "git@github.com:owner/repo.git")
	repo, err := gitx.Discover(gittest.Ctx(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitx.RemoteFromConfig(repo.GitCommonDir, "origin"); got != "git@github.com:owner/repo.git" {
		t.Errorf("RemoteFromConfig = %q", got)
	}
	if got := gitx.RemoteFromConfig(repo.GitCommonDir, "missing"); got != "" {
		t.Errorf("missing remote = %q", got)
	}
}
