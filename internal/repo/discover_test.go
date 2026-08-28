package repo_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// tree builds a directory layout: keys are relative paths, a value of "git"
// makes the directory a repo root.
func tree(t *testing.T, layout map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, kind := range layout {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if kind == "git" {
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func names(rs []repo.Repo) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Display()
	}
	return out
}

func TestDiscoverFindsCategorisedAndFlatRepos(t *testing.T) {
	root := tree(t, map[string]string{
		"Quant/backtest-engine": "git",
		"Quant/orderbook":       "git",
		"Web/music-notes":       "git",
		"flat-repo":             "git",
		"Quant/not-a-repo":      "",
		"empty-dir":             "",
	})

	got, err := repo.Discover(context.Background(), []string{root}, repo.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Quant/backtest-engine", "flat-repo", "Web/music-notes", "Quant/orderbook"}
	if len(got) != len(want) {
		t.Fatalf("found %v, want %d repos", names(got), len(want))
	}
	byName := map[string]repo.Repo{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if byName["backtest-engine"].Category != "Quant" {
		t.Errorf("category = %q, want Quant", byName["backtest-engine"].Category)
	}
	if byName["flat-repo"].Category != "" {
		t.Errorf("a repo directly in the root should have no category, got %q", byName["flat-repo"].Category)
	}
	if !byName["flat-repo"].HasGit {
		t.Error("HasGit should be set")
	}
}

func TestDiscoverDoesNotDescendIntoRepos(t *testing.T) {
	// A repo containing a nested checkout (a submodule, or a worktree parked
	// inside the repo) must be reported once, not twice.
	root := tree(t, map[string]string{
		"Quant/engine":                     "git",
		"Quant/engine/vendor/dependency":   "git",
		"Quant/engine/.claude/worktrees/x": "git",
	})
	got, _ := repo.Discover(context.Background(), []string{root}, repo.DefaultOptions())
	if len(got) != 1 || got[0].Name != "engine" {
		t.Fatalf("want only the outer repo, got %v", names(got))
	}
}

func TestDiscoverSkipsHeavyAndHiddenDirs(t *testing.T) {
	root := tree(t, map[string]string{
		"node_modules/pkg": "git",
		".hidden/repo":     "git",
		"Web/real":         "git",
	})
	got, _ := repo.Discover(context.Background(), []string{root}, repo.DefaultOptions())
	if len(got) != 1 || got[0].Name != "real" {
		t.Errorf("want only Web/real, got %v", names(got))
	}
}

func TestDiscoverRespectsMaxDepth(t *testing.T) {
	root := tree(t, map[string]string{"a/b/c/d/deep": "git"})
	got, _ := repo.Discover(context.Background(), []string{root}, repo.Options{MaxDepth: 2})
	if len(got) != 0 {
		t.Errorf("repo below MaxDepth should not be found, got %v", names(got))
	}
	got, _ = repo.Discover(context.Background(), []string{root}, repo.Options{MaxDepth: 6})
	if len(got) != 1 {
		t.Errorf("want the deep repo with a larger MaxDepth, got %v", names(got))
	}
}

func TestDiscoverMissingRootIsNotAnError(t *testing.T) {
	got, err := repo.Discover(context.Background(),
		[]string{filepath.Join(t.TempDir(), "unmounted")}, repo.DefaultOptions())
	if err != nil {
		t.Fatalf("a missing scan root must be skipped, not fatal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want none, got %v", names(got))
	}
}

func TestDiscoverFindsBareRepo(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "hub", "project.git")
	for _, d := range []string{"objects", "refs"} {
		os.MkdirAll(filepath.Join(bare, d), 0o755)
	}
	os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)

	got, _ := repo.Discover(context.Background(), []string{root}, repo.DefaultOptions())
	if len(got) != 1 || !got[0].Bare {
		t.Fatalf("want one bare repo, got %+v", got)
	}
}

func TestResolve(t *testing.T) {
	root := tree(t, map[string]string{
		"Quant/backtest-engine": "git",
		"Web/music-notes":       "git",
		"Web/music-player":      "git",
	})
	roots := []string{root}
	ctx := context.Background()

	r, _, err := repo.Resolve(ctx, roots, "backtest-engine")
	if err != nil || r.Name != "backtest-engine" {
		t.Fatalf("exact name: %+v %v", r, err)
	}
	r, _, err = repo.Resolve(ctx, roots, "Quant/backtest-engine")
	if err != nil || r.Name != "backtest-engine" {
		t.Fatalf("display name: %+v %v", r, err)
	}
	if _, _, err := repo.Resolve(ctx, roots, "music"); err == nil {
		t.Error("an ambiguous prefix must error rather than pick one")
	} else if _, ok := err.(*repo.AmbiguousError); !ok {
		t.Errorf("want AmbiguousError, got %T", err)
	}
	if _, _, err := repo.Resolve(ctx, roots, "absent"); err == nil {
		t.Error("want NotFoundError")
	} else if _, ok := err.(*repo.NotFoundError); !ok {
		t.Errorf("want NotFoundError, got %T", err)
	}
}

// An exact name must beat a longer repo that merely contains it.
func TestResolveExactBeatsSubstring(t *testing.T) {
	root := tree(t, map[string]string{"a/web": "git", "a/web-frontend": "git"})
	r, _, err := repo.Resolve(context.Background(), []string{root}, "web")
	if err != nil {
		t.Fatalf("exact match should win outright: %v", err)
	}
	if r.Name != "web" {
		t.Errorf("resolved to %q, want web", r.Name)
	}
}

func TestDiscoverDirectSymlinkToRepo(t *testing.T) {
	root := t.TempDir()
	physical := tree(t, map[string]string{"demo": "git"})
	alias := filepath.Join(root, "renamed-demo")
	if err := os.Symlink(filepath.Join(physical, "demo"), alias); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Discover(context.Background(), []string{root}, repo.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want the symlinked repo, got %+v", got)
	}
	if got[0].Path != alias || !got[0].Symlink {
		t.Errorf("want the navigation alias retained, got %+v", got[0])
	}
	if got[0].Name != "renamed-demo" {
		t.Errorf("index name should be the display name, got %q", got[0].Name)
	}
}

func TestDiscoverDeduplicatesIndexAndPhysicalRoot(t *testing.T) {
	physical := tree(t, map[string]string{"demo": "git"})
	index := t.TempDir()
	if err := os.Symlink(filepath.Join(physical, "demo"), filepath.Join(index, "demo")); err != nil {
		t.Fatal(err)
	}

	// First root wins: put the curated index first and its alias is what the UI
	// uses, while scanning the physical root adds no duplicate.
	got, err := repo.Discover(context.Background(), []string{index, physical}, repo.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("one clone should appear once, got %+v", got)
	}
	if got[0].Path != filepath.Join(index, "demo") {
		t.Errorf("first scan root should win, got %q", got[0].Path)
	}

	fast, err := repo.Discover(context.Background(), []string{index, physical}, repo.CompletionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(fast) != 1 || fast[0].Path != filepath.Join(index, "demo") {
		t.Errorf("fast discovery should preserve alias deduplication: %+v", fast)
	}
}

func TestDiscoverSkipsLinkedWorktreeAsProject(t *testing.T) {
	root := tree(t, map[string]string{"demo": "git"})
	// The filesystem-only fake repo above cannot create a worktree, so build a
	// real one for this classification.
	realRoot := t.TempDir()
	cmd := exec.Command("git", "init", "--initial-branch=main", filepath.Join(realRoot, "repo"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	real := filepath.Join(realRoot, "repo")
	exec.Command("git", "-C", real, "config", "user.email", "dev@example.test").Run()
	exec.Command("git", "-C", real, "config", "user.name", "dev test").Run()
	os.WriteFile(filepath.Join(real, "README.md"), []byte("x"), 0o644)
	exec.Command("git", "-C", real, "add", ".").Run()
	exec.Command("git", "-C", real, "commit", "-m", "init").Run()
	wt := filepath.Join(realRoot, "wt")
	if out, err := exec.Command("git", "-C", real, "worktree", "add", "-b", "feat/x", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}
	_ = root

	got, err := repo.Discover(context.Background(), []string{realRoot}, repo.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != real {
		t.Errorf("linked worktree is execution state, not another project: %+v", got)
	}

	fast, err := repo.Discover(context.Background(), []string{realRoot}, repo.CompletionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(fast) != 1 || fast[0].Path != real {
		t.Errorf("fast discovery should skip linked worktrees: %+v", fast)
	}
}

func TestResolveExplicitSymlinkPreservesAlias(t *testing.T) {
	physical := tree(t, map[string]string{"demo": "git"})
	root := t.TempDir()
	alias := filepath.Join(root, "friendly-name")
	if err := os.Symlink(filepath.Join(physical, "demo"), alias); err != nil {
		t.Fatal(err)
	}

	got, _, err := repo.Resolve(context.Background(), []string{root}, alias)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != alias || !got.Symlink {
		t.Errorf("explicit alias should be retained: %+v", got)
	}
	if got.Name != "friendly-name" {
		t.Errorf("alias name = %q", got.Name)
	}
}
