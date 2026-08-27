package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/bootstrap"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func moveRepo(t *testing.T, r *gittest.Repo, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.Root, path); err != nil {
		t.Fatal(err)
	}
	r.Root = path
}

func byKind(rs []bootstrap.Repository, k bootstrap.Kind) []bootstrap.Repository {
	var out []bootstrap.Repository
	for _, r := range rs {
		if r.Kind == k {
			out = append(out, r)
		}
	}
	return out
}

func TestScanFindsReposAtArbitraryDepth(t *testing.T) {
	root := t.TempDir()
	a := gittest.New(t)
	b := gittest.New(t)
	moveRepo(t, a, filepath.Join(root, "flat"))
	moveRepo(t, b, filepath.Join(root, "host", "owner", "deep"))

	got, warnings := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 repos, got %+v", got)
	}
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths[a.Root] || !paths[b.Root] {
		t.Errorf("missing paths: %+v", paths)
	}
}

func TestScanRespectsDepth(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "a", "b", "c", "repo"))

	got, _ := bootstrap.Scan(context.Background(), []string{root}, bootstrap.Options{
		MaxDepth: 2, FollowSymlinkDirs: true, IncludeWorktrees: true,
	})
	if len(got) != 0 {
		t.Errorf("repo below the depth should not be found: %+v", got)
	}
	got, _ = bootstrap.Scan(context.Background(), []string{root}, bootstrap.Options{
		MaxDepth: 5, FollowSymlinkDirs: true, IncludeWorktrees: true,
	})
	if len(got) != 1 {
		t.Errorf("want the deep repo with a larger depth, got %+v", got)
	}
}

func TestScanClassifiesLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "repos", "demo"))
	wtPath := filepath.Join(root, "worktrees", "demo", "feat-x")
	if err := gitx.AddWorktree(context.Background(), r.Root, wtPath, "feat/x", "main"); err != nil {
		t.Fatal(err)
	}

	got, warnings := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	mains := byKind(got, bootstrap.Canonical)
	worktrees := byKind(got, bootstrap.Worktree)
	if len(mains) != 1 || len(worktrees) != 1 {
		t.Fatalf("want canonical + worktree, got %+v", got)
	}
	if worktrees[0].Branch != "feat/x" {
		t.Errorf("branch = %q", worktrees[0].Branch)
	}
	if mains[0].CloneKey() != worktrees[0].CloneKey() {
		t.Error("main and linked worktree should share the clone key")
	}
	wantMain, _ := filepath.EvalSymlinks(mains[0].Path)
	if worktrees[0].MainRoot != wantMain {
		t.Errorf("MainRoot = %q, want %q", worktrees[0].MainRoot, wantMain)
	}
}

func TestScanCanExcludeWorktrees(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "demo"))
	wtPath := filepath.Join(root, "wt")
	if err := gitx.AddWorktree(context.Background(), r.Root, wtPath, "feat/x", "main"); err != nil {
		t.Fatal(err)
	}

	got, _ := bootstrap.Scan(context.Background(), []string{root}, bootstrap.Options{
		MaxDepth: 8, FollowSymlinkDirs: true, IncludeWorktrees: false,
	})
	if len(got) != 1 || got[0].Kind != bootstrap.Canonical {
		t.Errorf("want only the canonical checkout, got %+v", got)
	}
}

func TestScanFoldsSymlinkIntoAlias(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "physical", "demo"))
	index := filepath.Join(root, "index")
	os.MkdirAll(index, 0o755)
	alias := filepath.Join(index, "demo-link")
	if err := os.Symlink(r.Root, alias); err != nil {
		t.Fatal(err)
	}

	got, warnings := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(got) != 1 {
		t.Fatalf("a symlink is an alias, not a second repo: %+v", got)
	}
	if len(got[0].Aliases) != 1 {
		t.Fatalf("want one alias, got %+v", got[0].Aliases)
	}
	if got[0].Aliases[0].Path != alias {
		t.Errorf("alias = %q, want %q", got[0].Aliases[0].Path, alias)
	}
}

func TestScanRootCanItselfBeSymlink(t *testing.T) {
	outside := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(outside, "demo"))
	root := t.TempDir()
	link := filepath.Join(root, "linked-repo")
	if err := os.Symlink(r.Root, link); err != nil {
		t.Fatal(err)
	}

	got, _ := bootstrap.Scan(context.Background(), []string{link}, bootstrap.DefaultOptions())
	if len(got) != 1 || !got[0].Symlink {
		t.Fatalf("a root symlink directly to a repo should be inspected: %+v", got)
	}
	wantTarget, _ := filepath.EvalSymlinks(r.Root)
	if got[0].SymlinkTarget != wantTarget {
		t.Errorf("target = %q, want %q", got[0].SymlinkTarget, wantTarget)
	}
}

func TestScanFollowsSymlinkContainerWithoutLooping(t *testing.T) {
	outside := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(outside, "group", "demo"))
	// A loop inside the followed tree: group/back -> outside.
	os.Symlink(outside, filepath.Join(outside, "group", "back"))

	root := t.TempDir()
	container := filepath.Join(root, "external")
	if err := os.Symlink(filepath.Join(outside, "group"), container); err != nil {
		t.Fatal(err)
	}

	got, warnings := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(got) != 1 {
		t.Fatalf("want one repo and no infinite loop, got %+v", got)
	}
}

func TestScanCanSkipSymlinkContainers(t *testing.T) {
	outside := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(outside, "demo"))
	root := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "external"))

	got, _ := bootstrap.Scan(context.Background(), []string{root}, bootstrap.Options{
		MaxDepth: 8, FollowSymlinkDirs: false, IncludeWorktrees: true,
	})
	if len(got) != 0 {
		t.Errorf("symlink container should not be followed, got %+v", got)
	}
}

func TestScanClassifiesBareRepo(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "demo.git")
	cmd := []string{"init", "--bare", "--initial-branch=main", bare}
	r := gittest.New(t)
	// Reuse the helper's configured execution environment to create the bare
	// repo from a directory outside either repository.
	r.GitIn(root, cmd...)

	got, _ := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	bareRows := byKind(got, bootstrap.Bare)
	if len(bareRows) != 1 {
		t.Fatalf("want one bare repo, got %+v", got)
	}
	if bareRows[0].Name != "demo" {
		t.Errorf("bare name = %q", bareRows[0].Name)
	}
}

func TestScanReportsMissingRootAsWarning(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	got, warnings := bootstrap.Scan(context.Background(), []string{missing}, bootstrap.DefaultOptions())
	if len(got) != 0 || len(warnings) != 1 {
		t.Errorf("got=%+v warnings=%v", got, warnings)
	}
}

func TestScanStopsInsideARepo(t *testing.T) {
	root := t.TempDir()
	outer := gittest.New(t)
	moveRepo(t, outer, filepath.Join(root, "outer"))
	// A nested .git entry should not be treated as a separate project; it is
	// source state of the outer repo (or a submodule/worktree artifact).
	os.MkdirAll(filepath.Join(outer.Root, "vendor", "nested", ".git"), 0o755)

	got, _ := bootstrap.Scan(context.Background(), []string{root}, bootstrap.DefaultOptions())
	if len(got) != 1 || got[0].Path != outer.Root {
		t.Errorf("want only the outer repo, got %+v", got)
	}
}

func TestEnrichAliasesDoesNotAddUnrelatedRepos(t *testing.T) {
	base := []bootstrap.Repository{
		{Path: "/physical/api", RealPath: "/physical/api", Name: "api"},
	}
	other := []bootstrap.Repository{
		{Path: "/index/api", RealPath: "/physical/api", Name: "api"},
		{Path: "/index/web", RealPath: "/physical/web", Name: "web"},
	}

	got := bootstrap.EnrichAliases(base, other)
	if len(got) != 1 {
		t.Fatalf("unrelated repositories must not become move candidates: %+v", got)
	}
	if len(got[0].Aliases) != 1 || got[0].Aliases[0].Path != "/index/api" {
		t.Errorf("matching alias not folded in: %+v", got[0].Aliases)
	}
}

func TestScanCanonicalRootFindsWorktreeOutsideEveryRoot(t *testing.T) {
	root := t.TempDir()
	r := gittest.New(t)
	moveRepo(t, r, filepath.Join(root, "demo"))
	outside := filepath.Join(t.TempDir(), "agent-worktree")
	if err := gitx.AddWorktree(context.Background(), r.Root, outside, "agent/parallel", "main"); err != nil {
		t.Fatal(err)
	}

	// Only the canonical repo is scanned. Git's registry, not recursive
	// descent, must contribute the worktree living elsewhere.
	got, warnings := bootstrap.Scan(context.Background(), []string{r.Root}, bootstrap.DefaultOptions())
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(byKind(got, bootstrap.Canonical)) != 1 || len(byKind(got, bootstrap.Worktree)) != 1 {
		t.Fatalf("want canonical + external worktree, got %+v", got)
	}
	wantOutside, _ := filepath.EvalSymlinks(outside)
	if byKind(got, bootstrap.Worktree)[0].Path != wantOutside {
		t.Errorf("worktree path = %q, want %q", byKind(got, bootstrap.Worktree)[0].Path, wantOutside)
	}
}
