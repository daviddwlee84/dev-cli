package repo_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestValidateNavigationParent(t *testing.T) {
	r := gittest.New(t)
	expected := navigationExpected(t, r.Root)
	for name, observation := range map[string]repo.Repo{
		"complete":    expected,
		"candidate":   {Path: r.Root, RealPath: expected.RealPath, HasGit: true},
		"Git only":    {Path: r.Root, HasGit: true},
		"common only": {Path: r.Root, CommonDir: expected.CommonDir},
		"gitdir only": {Path: r.Root, GitDir: expected.GitDir},
		"main only":   {Path: r.Root, MainRoot: expected.MainRoot},
	} {
		t.Run(name, func(t *testing.T) {
			assertNavigation(t, t.Context(), observation, "", r.Root, "")
		})
	}

	for _, field := range []string{"target", "common", "gitdir", "main"} {
		t.Run("changed "+field, func(t *testing.T) {
			changed := expected
			other := t.TempDir()
			switch field {
			case "target":
				changed.RealPath = other
			case "common":
				changed.CommonDir = other
			case "gitdir":
				changed.GitDir = other
			case "main":
				changed.MainRoot = other
			}
			assertNavigation(t, t.Context(), changed, "", "", "changed")
		})
	}

	nested := filepath.Join(r.Root, "source")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	assertNavigation(t, t.Context(), repo.Repo{Path: nested, HasGit: true}, "", "", "Git root changed")
}

func TestValidateNavigationSymlink(t *testing.T) {
	r := gittest.New(t)
	expected := navigationExpected(t, r.Root)
	alias := filepath.Join(t.TempDir(), "bootstrap-alias")
	navigationSymlink(t, r.Root, alias)
	expected.Path, expected.Symlink = alias, true
	assertNavigation(t, t.Context(), expected, "", alias, "")

	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	navigationSymlink(t, t.TempDir(), alias)
	assertNavigation(t, t.Context(), expected, "", "", "repository target changed")
}

func TestValidateNavigationRequiresExistingDirectory(t *testing.T) {
	for _, kind := range []string{"missing", "file", "empty path"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "target")
			switch kind {
			case "missing":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty path":
				path = ""
			}
			assertNavigation(t, t.Context(), repo.Repo{Path: path, RealPath: path}, "", "", "navigation")
		})
	}
}

func TestValidateNavigationPlainDirectory(t *testing.T) {
	plain := t.TempDir()
	assertNavigation(t, t.Context(), repo.Repo{Path: plain}, "", plain, "")

	r := gittest.New(t)
	nested := filepath.Join(r.Root, "plain")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// HasGit=false describes the selected directory, not its containing repo.
	assertNavigation(t, t.Context(), repo.Repo{Path: nested}, "", nested, "")

	alias := filepath.Join(t.TempDir(), "plain-alias")
	navigationSymlink(t, plain, alias)
	assertNavigation(t, t.Context(), repo.Repo{Path: alias, RealPath: navigationCanonical(t, plain)}, "", alias, "")
}

func TestValidateNavigationFailedGitDoesNotFallback(t *testing.T) {
	r := gittest.New(t)
	expected := navigationExpected(t, r.Root)
	if err := os.RemoveAll(expected.GitDir); err != nil {
		t.Fatal(err)
	}
	for name, observation := range map[string]repo.Repo{
		"complete":    expected,
		"candidate":   {Path: r.Root, HasGit: true},
		"common only": {Path: r.Root, CommonDir: expected.CommonDir},
		"gitdir only": {Path: r.Root, GitDir: expected.GitDir},
		"main only":   {Path: r.Root, MainRoot: expected.MainRoot},
		"bare only":   {Path: r.Root, Bare: true},
	} {
		t.Run(name, func(t *testing.T) {
			assertNavigation(t, t.Context(), observation, "", "", "validate repository")
		})
	}
}

func TestValidateNavigationChild(t *testing.T) {
	r := gittest.New(t)
	expected := navigationExpected(t, r.Root)
	child := filepath.Join(t.TempDir(), "child")
	r.Git("worktree", "add", "--detach", child, "main")
	assertNavigation(t, t.Context(), expected, child, child, "")
	assertNavigation(t, t.Context(), repo.Repo{Path: r.Root, HasGit: true}, child, child, "")

	alias := filepath.Join(t.TempDir(), "child-alias")
	navigationSymlink(t, child, alias)
	assertNavigation(t, t.Context(), expected, alias, alias, "")

	nested := filepath.Join(child, "source")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	assertNavigation(t, t.Context(), expected, nested, "", "registered worktree not found")

	other := gittest.New(t)
	assertNavigation(t, t.Context(), expected, other.Root, "", "registered worktree not found")
	wrongParent := expected
	wrongParent.CommonDir = navigationExpected(t, other.Root).CommonDir
	assertNavigation(t, t.Context(), wrongParent, child, "", "Git common directory changed")
}

func TestValidateNavigationChildMissingOrPrunable(t *testing.T) {
	for _, state := range []string{"missing", "prunable", "replaced Git", "unregistered"} {
		t.Run(state, func(t *testing.T) {
			r := gittest.New(t)
			expected := navigationExpected(t, r.Root)
			child := filepath.Join(t.TempDir(), "child")
			r.Git("worktree", "add", "--detach", child, "main")
			selected := navigationExpected(t, child)
			ctx := gitx.WithObservations(t.Context())
			if _, err := gitx.Worktrees(ctx, r.Root); err != nil {
				t.Fatal(err)
			}
			wantError := ""
			switch state {
			case "missing":
				if err := os.RemoveAll(child); err != nil {
					t.Fatal(err)
				}
				wantError = "inspect navigation directory"
			case "prunable", "replaced Git":
				if err := os.Remove(filepath.Join(child, ".git")); err != nil {
					t.Fatal(err)
				}
				wantError = "prunable"
				if state == "replaced Git" {
					r.GitIn(child, "init", "--initial-branch=main")
					wantError = "checkout Git common directory changed"
				}
			case "unregistered":
				if err := os.RemoveAll(selected.GitDir); err != nil {
					t.Fatal(err)
				}
				wantError = "registered worktree not found"
			}
			assertNavigation(t, ctx, expected, child, "", wantError)
		})
	}
}

func TestValidateNavigationFreshRegistration(t *testing.T) {
	r := gittest.New(t)
	expected := navigationExpected(t, r.Root)
	ctx := gitx.WithObservations(t.Context())
	if _, err := gitx.Worktrees(ctx, r.Root); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(t.TempDir(), "new-child")
	r.Git("worktree", "add", "--detach", child, "main")
	assertNavigation(t, ctx, expected, child, child, "")
}

func TestValidateNavigationBare(t *testing.T) {
	r := gittest.New(t)
	bare := filepath.Join(t.TempDir(), "hub.git")
	r.Git("clone", "--bare", "--", r.Root, bare)
	physical := navigationCanonical(t, bare)
	expected := repo.Repo{Path: bare, RealPath: physical, GitDir: physical, CommonDir: physical, MainRoot: physical, HasGit: true, Bare: true}
	assertNavigation(t, t.Context(), expected, "", bare, "")
	assertNavigation(t, t.Context(), repo.Repo{Path: bare, HasGit: true}, "", bare, "")

	child := filepath.Join(t.TempDir(), "bare-child")
	r.GitIn(bare, "worktree", "add", "--detach", child, "main")
	assertNavigation(t, t.Context(), expected, child, child, "")

	alias := filepath.Join(t.TempDir(), "hub-alias")
	navigationSymlink(t, bare, alias)
	expected.Path = alias
	assertNavigation(t, t.Context(), expected, "", alias, "")
}

func TestValidateNavigationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, observation := range []repo.Repo{{Path: t.TempDir()}, {Path: t.TempDir(), HasGit: true}} {
		got, err := repo.ValidateNavigation(ctx, observation, "")
		if got != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("ValidateNavigation canceled = %q, %v", got, err)
		}
	}
}

func navigationExpected(t *testing.T, path string) repo.Repo {
	t.Helper()
	g, err := gitx.Discover(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Repo{Path: path, RealPath: navigationCanonical(t, path), GitDir: g.GitDir, CommonDir: g.GitCommonDir, MainRoot: g.MainRoot, HasGit: true, Bare: g.Bare}
}

func navigationCanonical(t *testing.T, path string) string {
	t.Helper()
	canonical, err := pathx.Canonical(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func navigationSymlink(t *testing.T, target, alias string) {
	t.Helper()
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func assertNavigation(t *testing.T, ctx context.Context, expected repo.Repo, checkout, want, wantError string) {
	t.Helper()
	got, err := repo.ValidateNavigation(ctx, expected, checkout)
	if wantError != "" {
		if got != "" || err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("ValidateNavigation(%q, %q) = %q, %v; want error containing %q", expected.Path, checkout, got, err, wantError)
		}
		return
	}
	if err != nil || got != want {
		t.Fatalf("ValidateNavigation(%q, %q) = %q, %v; want %q", expected.Path, checkout, got, err, want)
	}
}
