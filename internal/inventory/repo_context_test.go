package inventory_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestRepoContextClassifiesAndMatchesNestedCheckouts(t *testing.T) {
	r := gittest.New(t)
	r.Write(".gitignore", ".claude/worktrees/\n")
	r.Git("add", ".gitignore")
	r.Git("commit", "-m", "chore: ignore agent worktrees")

	devPath := filepath.Join(t.TempDir(), "dev-owned")
	if err := gitx.AddWorktree(context.Background(), r.Root, devPath, "feat/dev", "main"); err != nil {
		t.Fatal(err)
	}
	nestedPath := filepath.Join(r.Root, ".claude", "worktrees", "turn-1")
	if err := os.MkdirAll(filepath.Dir(nestedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitx.AddWorktree(context.Background(), r.Root, nestedPath, "worktree-turn-1", "main"); err != nil {
		t.Fatal(err)
	}

	tasks := []*task.Task{{
		ID: "repo__feat-dev", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/dev", WorktreePath: devPath, State: task.Hot,
	}}
	sessions := []runtime.Session{
		{Handle: "main", Dirs: []string{r.Root}, AgentStatus: "idle"},
		{Handle: "dev", Dirs: []string{filepath.Join(devPath, "src")}, AgentStatus: "working",
			AgentSessions: []string{"claude:dev"}},
		{Handle: "nested", Dirs: []string{nestedPath}, AgentStatus: "working",
			AgentSessions: []string{"claude:turn"}},
	}
	ctx := inventory.CollectRepoContext(context.Background(), repo.Repo{
		Name: "repo", Path: r.Root, RealPath: r.Root,
		CommonDir: filepath.Join(r.Root, ".git"), HasGit: true,
	}, tasks, sessions, "herdr")

	if ctx.WorktreeCount != 2 || len(ctx.Checkouts) != 3 {
		t.Fatalf("context worktrees=%d checkouts=%d", ctx.WorktreeCount, len(ctx.Checkouts))
	}
	byBranch := map[string]inventory.RepoCheckout{}
	for _, checkout := range ctx.Checkouts {
		byBranch[checkout.Branch()] = checkout
	}
	if got := byBranch["feat/dev"]; got.Ownership != inventory.CheckoutDev ||
		len(got.Sessions) != 1 || got.Sessions[0].Handle != "dev" {
		t.Errorf("dev checkout = %+v", got)
	}
	if got := byBranch["worktree-turn-1"]; got.Ownership != inventory.CheckoutEphemeral ||
		len(got.Sessions) != 1 || got.Sessions[0].Handle != "nested" {
		t.Errorf("nested checkout = %+v", got)
	}
	main, _ := ctx.Main()
	if len(main.Sessions) != 1 || main.Sessions[0].Handle != "main" {
		t.Errorf("nested session must not leak into main: %+v", main.Sessions)
	}

	markdown := inventory.FormatRepoContext(ctx, -1)
	for _, want := range []string{"# dev repo context: repo", devPath, nestedPath, "claude:turn", "ephemeral"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("context missing %q:\n%s", want, markdown)
		}
	}
	devIndex := -1
	for i, checkout := range ctx.Checkouts {
		if checkout.Branch() == "feat/dev" {
			devIndex = i
			break
		}
	}
	child := inventory.FormatRepoContext(ctx, devIndex)
	if !strings.Contains(child, devPath) || strings.Contains(child, nestedPath) || strings.Contains(child, r.Root+"`") {
		t.Errorf("child context should contain only one checkout:\n%s", child)
	}
}

func TestRepoContextCopyPayloads(t *testing.T) {
	ctx := inventory.RepoContext{
		Runtime: "herdr",
		Checkouts: []inventory.RepoCheckout{
			{Worktree: gitx.Worktree{Path: "/repo", Branch: "main", Main: true}},
			{Worktree: gitx.Worktree{Path: "/wt/one", Branch: "feat/one"}, Sessions: []runtime.Session{{
				Handle: "w1", AgentStatus: "working", AgentSessions: []string{"codex:abc"},
			}}},
			{Worktree: gitx.Worktree{Path: "/wt/two", Branch: "feat/two"}},
		},
	}
	if got := inventory.LinkedWorktreePaths(ctx); got != "/wt/one\n/wt/two" {
		t.Errorf("LinkedWorktreePaths = %q", got)
	}
	if got := inventory.FormatSessions(ctx, 1); !strings.Contains(got, "herdr w1") || !strings.Contains(got, "codex:abc") {
		t.Errorf("FormatSessions = %q", got)
	}
	if got := inventory.FormatSessions(ctx, 2); got != "" {
		t.Errorf("closed checkout sessions = %q", got)
	}
}
