package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestSubmoduleCloneDefaultsAndOptOut(t *testing.T) {
	h := newHarness(t)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child := gittest.New(t)
	h.repo.Git("submodule", "add", child.Root, "child")
	h.repo.Git("commit", "-am", "test: submodule")
	for _, mode := range []string{"recursive", "none"} {
		path := filepath.Join(h.scanRoot, "clone-"+mode)
		h.mustRun("repo", "clone", h.repo.Root, "--path", path, "--submodules", mode)
		g, err := gitx.SubmodulesOf(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Nodes) != 1 || g.Nodes[0].Initialized != (mode == "recursive") {
			t.Fatalf("%s: %+v", mode, g)
		}
	}
}

func TestSubmoduleStartSelectsTaskBranchAndPublishesGraph(t *testing.T) {
	h := newHarness(t)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child := gittest.New(t)
	h.repo.Git("submodule", "add", child.Root, "child")
	h.repo.Git("commit", "-am", "test: submodule")
	out := h.mustRun("start", h.repo.Root, "--task", "submodules", "--base", "main", "--submodule", "child", "--no-provision", "--json")
	var result struct {
		Checkout string `json:"checkout"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), result.Checkout)
	if err != nil {
		t.Fatal(err)
	}
	if g.Nodes[0].Branch != "feat/submodules" || g.Nodes[0].HEAD != g.Nodes[0].Gitlink {
		t.Fatalf("selected child: %+v", g)
	}
	if _, err := os.Stat(filepath.Join(result.Checkout, "child", "README.md")); err != nil {
		t.Fatal(err)
	}
	out = h.mustRun("ls", "--json")
	var rows []struct {
		Submodules []gitx.SubmoduleNode `json:"submodules"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Submodules) != 1 {
		t.Fatalf("graph omitted: %s", out)
	}
}

func TestSubmoduleStatusLabelsSuperprojectWorkspaceWorktrees(t *testing.T) {
	h := newHarness(t)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	core, reference := gittest.New(t), gittest.New(t)
	h.repo.Git("submodule", "add", core.Root, "app")
	h.repo.Git("submodule", "add", reference.Root, "paper")
	h.repo.Git("config", "-f", ".gitmodules", "submodule.app.x-policy", "track")
	h.repo.Git("config", "-f", ".gitmodules", "submodule.paper.x-policy", "lazy")
	h.repo.Git("config", "-f", ".gitmodules", "submodule.paper.update", "none")
	h.repo.Git("add", ".gitmodules")
	h.repo.Git("commit", "-m", "test: superproject-workspace children")
	h.repo.Git("submodule", "deinit", "paper")
	task := filepath.Join(h.repo.Root, ".worktrees", "_tasks", "login", "app")
	if _, err := gitx.Run(t.Context(), filepath.Join(h.repo.Root, "app"), "worktree", "add", "-b", "login", task, "HEAD"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(h.repo.Root)

	text := h.mustRun("git", "submodule", "status")
	for _, want := range []string{
		"superproject-workspace convention (gitmodules-x-policy); child worktree root .worktrees [default]",
		"superproject.worktreeRoot <dir outside the repo>",
		`superproject-workspace task "login" worktree; commit and push, then close it with the superproject-workspace skill: ` + "`superproject.py task close login`",
		"uninitialized (update = none: initialized only when developed)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text lacks %q:\n%s", want, text)
		}
	}

	var result struct {
		Submodules []gitx.SubmoduleNode `json:"submodules"`
		Convention *struct {
			Detected           bool   `json:"detected"`
			WorktreeRootSource string `json:"worktree_root_source"`
			Inside             bool   `json:"worktree_root_inside_repository"`
		} `json:"convention"`
		ChildWorktrees []struct {
			Submodule         string `json:"submodule"`
			Store             string `json:"store"`
			ConventionManaged bool   `json:"convention_managed"`
			Layout            string `json:"layout"`
			Task              string `json:"task"`
		} `json:"child_worktrees"`
	}
	if err := json.Unmarshal([]byte(h.mustRun("git", "submodule", "status", "--json")), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Submodules) != 2 || result.Convention == nil || !result.Convention.Detected || !result.Convention.Inside {
		t.Fatalf("status json: %+v", result)
	}
	if len(result.ChildWorktrees) != 1 {
		t.Fatalf("child worktrees: %+v", result.ChildWorktrees)
	}
	w := result.ChildWorktrees[0]
	if w.Submodule != "app" || w.Store != "canonical" || !w.ConventionManaged || w.Layout != "task" || w.Task != "login" {
		t.Fatalf("child worktree: %+v", w)
	}
}
