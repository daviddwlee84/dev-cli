package repo_test

import (
	"context"
	"os"
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
