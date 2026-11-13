package gitx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestSubmoduleIdentityAndIndependentWorktree(t *testing.T) {
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("-c", "protocol.file.allow=always", "submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: child")
	root := filepath.Join(parent.Root, "child")
	r, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	list, err := gitx.Worktrees(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Path != r.Root {
		t.Fatalf("main = %s, want %s", list[0].Path, r.Root)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(t.Context(), root, linked, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	l, err := gitx.Discover(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if l.MainRoot != r.Root || l.GitCommonDir != r.GitCommonDir {
		t.Fatalf("identity: %+v / %+v", l, r)
	}
	if _, err := gitx.ResolveRegisteredWorktree(t.Context(), linked, root); err != nil {
		t.Fatal(err)
	}
}

func TestSubmoduleInitPinnedAndSharedConfigPreserved(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "child with spaces")
	parent.Git("commit", "-am", "test: child")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(t.Context(), parent.Root, linked, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Initialized {
		t.Fatalf("uninitialized graph: %+v", g)
	}
	g, err = gitx.InitSubmodules(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 || !g.Nodes[0].Initialized || !g.Nodes[0].Status.Detached || g.Nodes[0].HEAD != g.Nodes[0].Gitlink {
		t.Fatalf("initialized graph: %+v", g)
	}
	after, err := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("initialization changed shared configuration")
	}
	if _, err := gitx.InitSubmodules(t.Context(), linked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "child with spaces", "untracked"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err = gitx.InitSubmodules(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Nodes[0].Status.Dirty() {
		t.Fatal("existing content was not preserved")
	}
}

func TestSubmoduleUninitializedNonemptyIsNotParent(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: child")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(t.Context(), parent.Root, linked, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "child", "local"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if g.Nodes[0].Initialized || g.Nodes[0].State != "unknown" {
		t.Fatalf("graph: %+v", g)
	}
	if _, err := gitx.InitSubmodules(t.Context(), linked); err == nil {
		t.Fatal("populated unknown directory initialized")
	}
}

func TestSubmoduleRecursiveInitSkipsUpdateNoneUnlessNamed(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	core, reference, parent := gittest.New(t), gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", core.Root, "core")
	parent.Git("submodule", "add", reference.Root, "reference")
	parent.Git("config", "-f", ".gitmodules", "submodule.reference.update", "none")
	parent.Git("config", "-f", ".gitmodules", "submodule.reference.x-policy", "lazy")
	parent.Git("add", ".gitmodules")
	parent.Git("commit", "-m", "test: lazy reference child")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(t.Context(), parent.Root, linked, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.InitSubmodules(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]gitx.SubmoduleNode{}
	for _, n := range g.Nodes {
		nodes[n.Path] = n
	}
	if !nodes["core"].Initialized {
		t.Fatalf("core not initialized: %+v", nodes["core"])
	}
	lazy := nodes["reference"]
	if lazy.Initialized || lazy.State != "uninitialized" || lazy.Update != "none" || !lazy.SkipsInitialization() {
		t.Fatalf("update = none child was initialized or misreported: %+v", lazy)
	}
	entries, err := os.ReadDir(filepath.Join(linked, "reference"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("update = none child checkout populated: %v", entries)
	}
	g, err = gitx.InitSubmodules(t.Context(), linked, "reference")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.Path == "reference" && (!n.Initialized || n.HEAD != n.Gitlink || !n.Status.Detached) {
			t.Fatalf("named update = none child not initialized at its gitlink: %+v", n)
		}
	}
}
