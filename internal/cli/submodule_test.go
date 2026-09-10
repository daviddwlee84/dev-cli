package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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
