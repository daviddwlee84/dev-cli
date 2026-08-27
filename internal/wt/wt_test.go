package wt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

// cfgFor builds a config whose worktree root is a scratch directory.
func cfgFor(t *testing.T) config.Config {
	t.Helper()
	c := config.Default()
	c.Paths.WorktreeRoot = filepath.Join(t.TempDir(), "Worktrees")
	c.Paths.WorktreePath = "{{worktree_root}}/{{repo}}/{{branch|slug}}"
	c.Worktree.Include = nil
	c.Worktree.PostCreate = config.PostCreate{} // no commands in tests
	return c
}

func TestCreateUsesPathTemplate(t *testing.T) {
	r := gittest.New(t)
	cfg := cfgFor(t)
	m := &wt.Manager{Cfg: cfg}

	res, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/auth", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := filepath.Join(config.Expand(cfg.Paths.WorktreeRoot), "repo", "feat-auth")
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
	if !res.BranchCreated {
		t.Error("BranchCreated should be true for a new branch")
	}
	if _, err := os.Stat(filepath.Join(res.Path, "README.md")); err != nil {
		t.Errorf("checkout should contain the repo's tracked files: %v", err)
	}
}

func TestCreateRefusesDuplicateBranch(t *testing.T) {
	r := gittest.New(t)
	m := &wt.Manager{Cfg: cfgFor(t)}
	req := wt.CreateRequest{RepoPath: r.Root, Branch: "feat/dup", Base: "main", NoRuntime: true}

	if _, err := m.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	_, err := m.Create(context.Background(), req)
	var exists *wt.ErrExists
	if err == nil {
		t.Fatal("creating a second worktree for one branch must fail")
	}
	if !asErr(err, &exists) {
		t.Fatalf("want ErrExists, got %T: %v", err, err)
	}
	if exists.Path == "" {
		t.Error("ErrExists should report where the branch is already checked out")
	}
}

func TestCreateRefusesNestedWorktree(t *testing.T) {
	r := gittest.New(t)
	cfg := cfgFor(t)
	// A template that would place the worktree inside the repo itself.
	cfg.Paths.WorktreePath = "{{repo_path}}/.worktrees/{{branch|slug}}"
	m := &wt.Manager{Cfg: cfg}

	_, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/nested", Base: "main", NoRuntime: true,
	})
	if err == nil {
		t.Fatal("a worktree inside the repository should be refused")
	}
	if !strings.Contains(err.Error(), "inside the repository") {
		t.Errorf("error should explain the nesting problem, got %v", err)
	}
}

func TestCreateRefusesNonEmptyTarget(t *testing.T) {
	r := gittest.New(t)
	cfg := cfgFor(t)
	m := &wt.Manager{Cfg: cfg}
	target := filepath.Join(t.TempDir(), "occupied")
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "important.txt"), []byte("do not clobber\n"), 0o644)

	_, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/x", Base: "main", Path: target, NoRuntime: true,
	})
	if err == nil {
		t.Fatal("a non-empty target must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(target, "important.txt")); statErr != nil {
		t.Error("the existing file must be untouched")
	}
}

func TestCreateRejectsUnknownBase(t *testing.T) {
	r := gittest.New(t)
	m := &wt.Manager{Cfg: cfgFor(t)}
	_, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/x", Base: "origin/nope", NoRuntime: true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want a clear missing-base error, got %v", err)
	}
}

func TestRemoveKeepsBranchAndRefusesDirty(t *testing.T) {
	r := gittest.New(t)
	m := &wt.Manager{Cfg: cfgFor(t)}
	ctx := context.Background()

	res, err := m.Create(ctx, wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/rm", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(res.Path, "scratch.txt"), []byte("wip\n"), 0o644)

	dirty, _, err := wt.DirtyCheck(ctx, res.Path)
	if err != nil || !dirty {
		t.Fatalf("DirtyCheck: dirty=%v err=%v", dirty, err)
	}
	if err := m.Remove(ctx, wt.RemoveRequest{RepoPath: r.Root, Path: res.Path}); err == nil {
		t.Fatal("removing a dirty worktree without force must fail")
	}
	if err := m.Remove(ctx, wt.RemoveRequest{RepoPath: r.Root, Path: res.Path, Force: true}); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
	if !gitx.BranchExists(ctx, r.Root, "feat/rm") {
		t.Error("removing a worktree must never delete the branch")
	}
}

func TestRemoveMissingDirectoryPrunes(t *testing.T) {
	r := gittest.New(t)
	m := &wt.Manager{Cfg: cfgFor(t)}
	ctx := context.Background()
	res, err := m.Create(ctx, wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/gone", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the user deleting the directory behind git's back.
	os.RemoveAll(res.Path)

	if err := m.Remove(ctx, wt.RemoveRequest{RepoPath: r.Root, Path: res.Path}); err != nil {
		t.Fatalf("removing an already-deleted worktree should prune, not fail: %v", err)
	}
	list, _ := gitx.Worktrees(ctx, r.Root)
	if len(list) != 1 {
		t.Errorf("stale administrative entry should be pruned, got %d worktrees", len(list))
	}
}

// Only gitignored files may be carried into a new worktree: a tracked file is
// already there on the correct branch, and copying it would overwrite it.
func TestProvisionCopiesOnlyGitignoredFiles(t *testing.T) {
	r := gittest.New(t)
	r.Commit(".gitignore", ".env\nsecrets/\n", "chore: ignore env")
	r.Commit("tracked.json", `{"tracked":true}`, "chore: add tracked config")
	r.Write(".env", "TOKEN=abc\n")
	r.Write("secrets/key.txt", "shh\n")

	cfg := cfgFor(t)
	cfg.Worktree.Include = []string{".env", "secrets/key.txt", "tracked.json"}
	m := &wt.Manager{Cfg: cfg}

	res, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/env", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(res.Path, ".env")); err != nil || string(got) != "TOKEN=abc\n" {
		t.Errorf(".env should have been copied: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "secrets/key.txt")); err != nil {
		t.Errorf("nested ignored file should have been copied: %v", err)
	}
	// tracked.json is in the checkout because git put it there, and it must
	// not appear in the copied list.
	for _, c := range res.Provision.Copied {
		if c == "tracked.json" {
			t.Error("a tracked file must never be copied over the checkout's own version")
		}
	}
}

func TestProvisionLinksOptInDirectories(t *testing.T) {
	r := gittest.New(t)
	r.Commit(".gitignore", "node_modules/\n", "chore: ignore deps")
	os.MkdirAll(filepath.Join(r.Root, "node_modules", "pkg"), 0o755)

	cfg := cfgFor(t)
	cfg.Worktree.Link = []string{"node_modules"}
	m := &wt.Manager{Cfg: cfg}

	res, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/link", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(res.Path, "node_modules"))
	if err != nil {
		t.Fatalf("node_modules should exist in the worktree: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("linked directories should be symlinks, not copies")
	}
	if len(res.Provision.Linked) != 1 {
		t.Errorf("Linked = %v", res.Provision.Linked)
	}
}

func TestProvisionRunsPostCreateCommands(t *testing.T) {
	r := gittest.New(t)
	cfg := cfgFor(t)
	cfg.Worktree.PostCreate = config.PostCreate{Commands: []string{"echo provisioned > marker.txt"}}
	m := &wt.Manager{Cfg: cfg}

	res, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/post", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(res.Path, "marker.txt"))
	if err != nil || strings.TrimSpace(string(got)) != "provisioned" {
		t.Errorf("post_create should run in the new worktree: %q %v", got, err)
	}
	if len(res.Provision.Ran) != 1 {
		t.Errorf("Ran = %v", res.Provision.Ran)
	}
}

// A failing post-create command must leave a usable checkout behind: the user
// can fix it by hand, and rolling the worktree back would lose nothing but
// cost them the branch.
func TestProvisionFailureIsNotFatal(t *testing.T) {
	r := gittest.New(t)
	cfg := cfgFor(t)
	cfg.Worktree.PostCreate = config.PostCreate{Commands: []string{"exit 3"}}
	m := &wt.Manager{Cfg: cfg}

	res, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/fail", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatalf("a failed post_create must not fail the create: %v", err)
	}
	if len(res.Provision.Failures) != 1 {
		t.Errorf("the failure should be reported, got %v", res.Provision.Failures)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "README.md")); err != nil {
		t.Error("the checkout should still exist and be usable")
	}
}

func TestRepoOverrideWins(t *testing.T) {
	r := gittest.New(t)
	r.Commit(".dev.toml", "[worktree]\npost_create = [\"echo from-repo > who.txt\"]\n", "chore: add dev override")

	cfg := cfgFor(t)
	cfg.Worktree.PostCreate = config.PostCreate{Commands: []string{"echo from-global > who.txt"}}
	m := &wt.Manager{Cfg: cfg}

	res, err := m.Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/override", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(res.Path, "who.txt"))
	if strings.TrimSpace(string(got)) != "from-repo" {
		t.Errorf("the repo's .dev.toml should win, got %q", got)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	got := wt.Detect(dir)
	if len(got) != 1 || got[0] != "go mod download" {
		t.Errorf("Detect = %v, want [go mod download]", got)
	}

	// Two JS lockfiles: only one package manager should be chosen.
	js := t.TempDir()
	os.WriteFile(filepath.Join(js, "pnpm-lock.yaml"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(js, "package-lock.json"), []byte("{}"), 0o644)
	got = wt.Detect(js)
	if len(got) > 1 {
		t.Errorf("only one JS package manager should run, got %v", got)
	}

	if got := wt.Detect(t.TempDir()); len(got) != 0 {
		t.Errorf("an empty directory needs no provisioning, got %v", got)
	}
}

// asErr is errors.As without importing errors into every call site.
func asErr[T error](err error, target *T) bool {
	for err != nil {
		if t, ok := err.(T); ok {
			*target = t
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
