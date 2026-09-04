package cli

import (
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestTUICapabilityTargetsPreferExactStartupCheckoutInRepository(t *testing.T) {
	locals := []tui.RepoRow{
		{Repo: repo.Repo{Name: "api", Path: "/repos/api", MainRoot: "/repos/api", CommonDir: "/repos/api/.git", HasGit: true}},
		{Repo: repo.Repo{Name: "web", Path: "/repos/web", MainRoot: "/repos/web", CommonDir: "/repos/web/.git", HasGit: true}},
	}
	current := agenttarget.Target{
		RepoName: "api", RepoPath: "/repos/api", CheckoutRoot: "/worktrees/api-feature", CommonDir: "/repos/api/.git",
	}

	targets := tuiCapabilityTargets(locals, current, tui.CapabilityStartupContext)
	if len(targets) != 1 || targets[0].CheckoutRoot != current.CheckoutRoot || targets[0].RepoName != "api" {
		t.Fatalf("repository context targets = %+v", targets)
	}
}

func TestTUICapabilityTargetsIncludeRepositoriesAndStartupCheckoutInAllScope(t *testing.T) {
	locals := []tui.RepoRow{
		{Repo: repo.Repo{Name: "api", Path: "/repos/api", MainRoot: "/repos/api", CommonDir: "/repos/api/.git", HasGit: true}},
		{Repo: repo.Repo{Name: "web", Path: "/repos/web", MainRoot: "/repos/web", CommonDir: "/repos/web/.git", HasGit: true}},
	}
	current := agenttarget.Target{
		RepoName: "api", RepoPath: "/repos/api", CheckoutRoot: "/worktrees/api-feature", CommonDir: "/repos/api/.git",
	}

	targets := tuiCapabilityTargets(locals, current, tui.CapabilityAllRepositories)
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target.CheckoutRoot] = true
	}
	for _, expected := range []string{"/repos/api", "/repos/web", "/worktrees/api-feature"} {
		if !seen[expected] {
			t.Errorf("all-scope targets missing %s: %+v", expected, targets)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("all-scope targets = %+v", targets)
	}
}

func TestTUICapabilityTargetsKeepUnlistedGitCheckoutContextOnly(t *testing.T) {
	current := agenttarget.Target{
		RepoName: "external", RepoPath: "/repos/external", CheckoutRoot: "/worktrees/external", CommonDir: "/repos/external/.git",
	}

	targets := tuiCapabilityTargets(nil, current, tui.CapabilityStartupContext)
	if len(targets) != 1 || targets[0] != current {
		t.Fatalf("unlisted checkout targets = %+v", targets)
	}
}

func TestTUICapabilityTargetsUseRepositoryInventoryOutsideGit(t *testing.T) {
	locals := []tui.RepoRow{
		{Repo: repo.Repo{Name: "api", Path: "/repos/api", MainRoot: "/repos/api", CommonDir: "/repos/api/.git", HasGit: true}},
		{Repo: repo.Repo{Name: "web", Path: "/repos/web", MainRoot: "/repos/web", CommonDir: "/repos/web/.git", HasGit: true}},
	}
	ordinary := agenttarget.Target{
		RepoName: "scratch", RepoPath: "/tmp/scratch", CheckoutRoot: "/tmp/scratch", CommonDir: "/tmp/scratch",
	}

	targets := tuiCapabilityTargets(locals, ordinary, tui.CapabilityStartupContext)
	if len(targets) != 3 {
		t.Fatalf("outside-project targets = %+v", targets)
	}
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target.CheckoutRoot] = true
	}
	for _, expected := range []string{"/repos/api", "/repos/web", "/tmp/scratch"} {
		if !seen[expected] {
			t.Errorf("outside-project targets missing %s: %+v", expected, targets)
		}
	}
}
