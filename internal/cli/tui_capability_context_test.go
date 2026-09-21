package cli

import (
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestTUICapabilityTargetsPreferExactStartupCheckoutInRepository(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locals := []tui.RepoRow{
		{Repo: repo.Repo{Name: "api", Path: filepath.Join(root, "repos", "api"), MainRoot: filepath.Join(root, "repos", "api"), CommonDir: filepath.Join(root, "repos", "api", ".git"), HasGit: true}},
		{Repo: repo.Repo{Name: "web", Path: filepath.Join(root, "repos", "web"), MainRoot: filepath.Join(root, "repos", "web"), CommonDir: filepath.Join(root, "repos", "web", ".git"), HasGit: true}},
	}
	current := agenttarget.Target{
		RepoName: "api", RepoPath: filepath.Join(root, "repos", "api"), CheckoutRoot: filepath.Join(root, "worktrees", "api-feature"), CommonDir: filepath.Join(root, "repos", "api", ".git"),
	}

	targets := tuiCapabilityTargets(locals, current, tui.CapabilityStartupContext)
	if len(targets) != 1 || targets[0].CheckoutRoot != current.CheckoutRoot || targets[0].RepoName != "api" {
		t.Fatalf("repository context targets = %+v", targets)
	}
}

func TestTUICapabilityTargetsIncludeRepositoriesAndStartupCheckoutInAllScope(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locals := []tui.RepoRow{
		{Repo: repo.Repo{Name: "api", Path: filepath.Join(root, "repos", "api"), MainRoot: filepath.Join(root, "repos", "api"), CommonDir: filepath.Join(root, "repos", "api", ".git"), HasGit: true}},
		{Repo: repo.Repo{Name: "web", Path: filepath.Join(root, "repos", "web"), MainRoot: filepath.Join(root, "repos", "web"), CommonDir: filepath.Join(root, "repos", "web", ".git"), HasGit: true}},
	}
	current := agenttarget.Target{
		RepoName: "api", RepoPath: filepath.Join(root, "repos", "api"), CheckoutRoot: filepath.Join(root, "worktrees", "api-feature"), CommonDir: filepath.Join(root, "repos", "api", ".git"),
	}

	targets := tuiCapabilityTargets(locals, current, tui.CapabilityAllRepositories)
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target.CheckoutRoot] = true
	}
	for _, expected := range []string{filepath.Join(root, "repos", "api"), filepath.Join(root, "repos", "web"), filepath.Join(root, "worktrees", "api-feature")} {
		if !seen[expected] {
			t.Errorf("all-scope targets missing %s: %+v", expected, targets)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("all-scope targets = %+v", targets)
	}
}

func TestTUICapabilityTargetsKeepUnlistedGitCheckoutContextOnly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current := agenttarget.Target{
		RepoName: "external", RepoPath: filepath.Join(root, "repos", "external"), CheckoutRoot: filepath.Join(root, "worktrees", "external"), CommonDir: filepath.Join(root, "repos", "external", ".git"),
	}

	targets := tuiCapabilityTargets(nil, current, tui.CapabilityStartupContext)
	if len(targets) != 1 || targets[0] != current {
		t.Fatalf("unlisted checkout targets = %+v", targets)
	}
}

func TestTUICapabilityTargetsUseRepositoryInventoryOutsideGit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locals := []tui.RepoRow{
		{Repo: repo.Repo{Name: "api", Path: filepath.Join(root, "repos", "api"), MainRoot: filepath.Join(root, "repos", "api"), CommonDir: filepath.Join(root, "repos", "api", ".git"), HasGit: true}},
		{Repo: repo.Repo{Name: "web", Path: filepath.Join(root, "repos", "web"), MainRoot: filepath.Join(root, "repos", "web"), CommonDir: filepath.Join(root, "repos", "web", ".git"), HasGit: true}},
	}
	ordinary := agenttarget.Target{
		RepoName: "scratch", RepoPath: filepath.Join(root, "tmp", "scratch"), CheckoutRoot: filepath.Join(root, "tmp", "scratch"), CommonDir: filepath.Join(root, "tmp", "scratch"),
	}

	targets := tuiCapabilityTargets(locals, ordinary, tui.CapabilityStartupContext)
	if len(targets) != 3 {
		t.Fatalf("outside-project targets = %+v", targets)
	}
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target.CheckoutRoot] = true
	}
	for _, expected := range []string{filepath.Join(root, "repos", "api"), filepath.Join(root, "repos", "web"), filepath.Join(root, "tmp", "scratch")} {
		if !seen[expected] {
			t.Errorf("outside-project targets missing %s: %+v", expected, targets)
		}
	}
}
