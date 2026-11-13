package submodule_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
)

func isolatedGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	p, err := pathx.Canonical(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectConventionSourcesAndWorktreeRootPrecedence(t *testing.T) {
	isolatedGit(t)
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "app")
	parent.Git("commit", "-am", "test: app")
	c, err := submodule.DetectConvention(t.Context(), parent.Root)
	if err != nil || c.Detected {
		t.Fatalf("plain submodules detected as convention: %+v %v", c, err)
	}

	parent.Git("config", "-f", ".gitmodules", "submodule.app.x-policy", "track")
	c, err = submodule.DetectConvention(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Detected || strings.Join(c.Sources, ",") != "gitmodules-x-policy" || c.WorktreeRootSource != "default" ||
		c.WorktreeRoot != canonical(t, filepath.Join(parent.Root, ".worktrees")) || !c.WorktreeRootInside {
		t.Fatalf("x-policy detection: %+v", c)
	}

	if err := os.WriteFile(filepath.Join(parent.Root, ".superproject"), []byte("[workspace]\n\tworktree-root = ../shared-worktrees\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = submodule.DetectConvention(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Sources, ",") != "gitmodules-x-policy,superproject-file" || c.WorktreeRootSource != "superproject-file" ||
		c.WorktreeRoot != canonical(t, filepath.Join(filepath.Dir(parent.Root), "shared-worktrees")) || c.WorktreeRootInside {
		t.Fatalf("marker worktree root: %+v", c)
	}

	outside := t.TempDir()
	parent.Git("config", "--local", "superproject.worktreeRoot", outside)
	c, err = submodule.DetectConvention(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if c.WorktreeRootSource != "git-config" || c.WorktreeRoot != canonical(t, outside) || c.WorktreeRootInside {
		t.Fatalf("local config worktree root: %+v", c)
	}

	// The marker alone is enough.
	marker, other := gittest.New(t), gittest.New(t)
	marker.Git("submodule", "add", other.Root, "lib")
	if err := os.WriteFile(filepath.Join(marker.Root, ".superproject"), []byte("[workspace]\n\tmode = submodule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err = submodule.DetectConvention(t.Context(), marker.Root); err != nil || !c.Detected || c.Sources[0] != "superproject-file" {
		t.Fatalf("marker detection: %+v %v", c, err)
	}
}

func TestChildWorktreesLabelSkillLayoutsInCanonicalStore(t *testing.T) {
	isolatedGit(t)
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "app")
	parent.Git("config", "-f", ".gitmodules", "submodule.app.x-policy", "track")
	parent.Git("add", ".gitmodules")
	parent.Git("commit", "-m", "test: app")
	app := filepath.Join(parent.Root, "app")
	single := filepath.Join(parent.Root, ".worktrees", "app", "feat-login")
	task := filepath.Join(parent.Root, ".worktrees", "_tasks", "login", "app")
	foreign := filepath.Join(t.TempDir(), "foreign")
	for _, add := range [][]string{
		{"worktree", "add", "-b", "feat/login", single, "HEAD"},
		{"worktree", "add", "-b", "login", task, "HEAD"},
		{"worktree", "add", "-b", "elsewhere", foreign, "HEAD"},
	} {
		if _, err := gitx.Run(t.Context(), app, add...); err != nil {
			t.Fatal(err)
		}
	}
	g, err := gitx.SubmodulesOf(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	c, rows, err := submodule.ChildWorktrees(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Detected || len(rows) != 3 {
		t.Fatalf("convention %+v rows %+v", c, rows)
	}
	byPath := map[string]submodule.ChildWorktree{}
	for _, row := range rows {
		if row.Submodule != "app" || row.Store != "canonical" {
			t.Fatalf("row identity: %+v", row)
		}
		byPath[canonical(t, row.Path)] = row
	}
	s := byPath[canonical(t, single)]
	if !s.ConventionManaged || s.Layout != "child" || s.Task != "" || !strings.Contains(s.Guidance, "superproject.py worktree remove app feat/login") {
		t.Fatalf("single-child worktree: %+v", s)
	}
	tk := byPath[canonical(t, task)]
	if !tk.ConventionManaged || tk.Layout != "task" || tk.Task != "login" || !strings.Contains(tk.Guidance, "superproject.py task close login") ||
		!strings.Contains(tk.Label(), `superproject-workspace task "login" worktree`) {
		t.Fatalf("task worktree: %+v / %s", tk, tk.Label())
	}
	f := byPath[canonical(t, foreign)]
	if f.ConventionManaged || f.Guidance != "" || !strings.Contains(f.Label(), "(linked worktree)") {
		t.Fatalf("foreign worktree: %+v / %s", f, f.Label())
	}
}

func TestChildWorktreesAreOrdinaryWithoutConvention(t *testing.T) {
	isolatedGit(t)
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "app")
	parent.Git("commit", "-am", "test: app")
	wt := filepath.Join(parent.Root, ".worktrees", "_tasks", "login", "app")
	if _, err := gitx.Run(t.Context(), filepath.Join(parent.Root, "app"), "worktree", "add", "-b", "login", wt, "HEAD"); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	c, rows, err := submodule.ChildWorktrees(t.Context(), g)
	if err != nil || c.Detected || len(rows) != 1 || rows[0].ConventionManaged || rows[0].Task != "" {
		t.Fatalf("unlabelled layout: %+v %+v %v", c, rows, err)
	}
}

func TestRetirementBlockerNamesSkillTaskWorktreeInOuterWorktreeStore(t *testing.T) {
	isolatedGit(t)
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("config", "-f", ".gitmodules", "submodule.child.x-policy", "track")
	parent.Git("add", ".gitmodules")
	parent.Git("commit", "-m", "test: child")
	outside := t.TempDir()
	parent.Git("config", "--local", "superproject.worktreeRoot", outside)
	root := filepath.Join(t.TempDir(), "feature")
	if err := gitx.AddWorktree(t.Context(), parent.Root, root, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	if _, err := submodule.Prepare(t.Context(), cfg, root, "recursive", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	// What an older skill (before its guard) created from a dev outer worktree.
	task := filepath.Join(outside, "_tasks", "login", "child")
	if _, err := gitx.Run(t.Context(), filepath.Join(root, "child"), "worktree", "add", "-b", "login", task, "HEAD"); err != nil {
		t.Fatal(err)
	}
	_, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err == nil {
		t.Fatal("child task worktree did not block removal")
	}
	for _, want := range []string{"still owns other worktrees", `superproject-workspace task "login" worktree`, "orphaned when it is removed", "superproject.py task close login"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("blocker %q lacks %q", err, want)
		}
	}
}

func TestOnDemandChildSkippedByRecursiveInitAndPublicationButDevelopable(t *testing.T) {
	isolatedGit(t)
	core, reference, parent := gittest.New(t), gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", core.Root, "core")
	parent.Git("submodule", "add", reference.Root, "reference")
	parent.Git("config", "-f", ".gitmodules", "submodule.reference.update", "none")
	parent.Git("add", ".gitmodules")
	parent.Git("commit", "-m", "test: on-demand reference")
	root := filepath.Join(t.TempDir(), "feature")
	if err := gitx.AddWorktree(t.Context(), parent.Root, root, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	g, err := submodule.Prepare(t.Context(), cfg, root, "recursive", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if (n.Path == "reference") == n.Initialized {
			t.Fatalf("recursive Prepare initialization: %+v", n)
		}
	}
	p, err := submodule.InspectPublication(t.Context(), cfg, root, false)
	if err != nil {
		t.Fatalf("absent on-demand child blocked publication: %v", err)
	}
	if p.InitializedCount() != 1 {
		t.Fatalf("publication verifies %d children, want 1", p.InitializedCount())
	}
	g, err = submodule.Prepare(t.Context(), cfg, root, "recursive", []string{"reference"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.Path == "reference" && (!n.Initialized || n.Branch != "feature") {
			t.Fatalf("developed on-demand child: %+v", n)
		}
	}
}
