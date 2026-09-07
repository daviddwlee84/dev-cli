package submodule_test

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

func removalFixture(t *testing.T) (config.Config, *gittest.Repo, *gittest.Repo, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: child")
	root := filepath.Join(t.TempDir(), "feature")
	if err := gitx.AddWorktree(t.Context(), parent.Root, root, "feature", "HEAD"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	if _, err := submodule.Prepare(t.Context(), cfg, root, "recursive", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	return cfg, parent, child, root
}

func TestRemovalInsideOutPreservesCanonicalAndOuterBranch(t *testing.T) {
	cfg, parent, _, root := removalFixture(t)
	before, _ := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	if _, err := submodule.InspectRemoval(t.Context(), cfg, root, false, false); err == nil {
		t.Fatal("missing recursive approval accepted")
	}
	p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = p.Apply(t.Context(), nil, func() error { return gitx.RemoveWorktree(t.Context(), parent.Root, root, false) }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("outer checkout remains")
	}
	if !gitx.BranchExists(t.Context(), parent.Root, "feature") {
		t.Fatal("outer branch deleted")
	}
	after, _ := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	if string(before) != string(after) {
		t.Fatal("canonical configuration changed")
	}
	if _, err = gitx.Discover(t.Context(), filepath.Join(parent.Root, "child")); err != nil {
		t.Fatal("canonical child damaged", err)
	}
}

func TestRemovalRollbackAndStaleGraph(t *testing.T) {
	cfg, _, _, root := removalFixture(t)
	p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected outer removal failure")
	if err = p.Apply(t.Context(), nil, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err = gitx.Discover(t.Context(), filepath.Join(root, "child")); err != nil {
		t.Fatal("child was not restored", err)
	}
	if _, err = gitx.Run(t.Context(), filepath.Join(root, "child"), "branch", "later", "HEAD"); err != nil {
		t.Fatal(err)
	}
	called := false
	err = p.Apply(t.Context(), nil, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("stale graph applied: %v", err)
	}
}

func TestRemovalBlocksLocalOnlyRefsAndIgnoredFiles(t *testing.T) {
	cfg, _, _, root := removalFixture(t)
	child := filepath.Join(root, "child")
	if _, err := gitx.Run(t.Context(), child, "branch", "local-only", "HEAD"); err != nil {
		t.Fatal(err)
	}
	p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyRemote(t.Context()); err == nil || !strings.Contains(err.Error(), "local ref") {
		t.Fatalf("local-only branch accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(child, "untracked"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false); err == nil {
		t.Fatal("dirty child accepted")
	}
}

func TestRemovalRejectsOrphanPrivateModuleRepository(t *testing.T) {
	cfg, _, child, root := removalFixture(t)
	r, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), root, "clone", "--bare", child.Root, filepath.Join(r.GitDir, "modules", "orphan")); err != nil {
		t.Fatal(err)
	}
	if _, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false); err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("orphan storage accepted: %v", err)
	}
}

func TestRecoveryOriginCannotBeThePrivateRepositoryBeingRemoved(t *testing.T) {
	cfg, parent, source, root := removalFixture(t)
	child := filepath.Join(root, "child")
	r, err := gitx.Discover(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.ToSlash(r.GitDir)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	parent.GitIn(root, "config", "--file", ".gitmodules", "submodule.child.url", fileURL)
	parent.GitIn(root, "add", ".gitmodules")
	parent.GitIn(root, "commit", "-m", "test: self-referential recovery origin")
	source.GitIn(child, "remote", "set-url", "origin", fileURL)
	p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyRemote(t.Context()); err == nil || !strings.Contains(err.Error(), "depends on the workspace") {
		t.Fatalf("self-reference accepted: %v", err)
	}
}

func TestPublishedUnmergedMemberCanParkAndReconstructButCannotRetire(t *testing.T) {
	cfg, parent, source, root := removalFixture(t)
	if _, err := submodule.Prepare(t.Context(), cfg, root, "recursive", []string{"child"}, nil, true); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	source.GitIn(child, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "test: unmerged work")
	source.GitIn(child, "push", "origin", "feature")
	parent.GitIn(root, "add", "child")
	parent.GitIn(root, "commit", "-m", "test: pin published feature")
	retire, err := submodule.InspectRemoval(t.Context(), cfg, root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = retire.VerifyRemote(t.Context()); err == nil {
		t.Fatal("unmerged member accepted for retirement")
	}
	cold, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = cold.VerifyRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = cold.Apply(t.Context(), nil, func() error { return gitx.RemoveWorktree(t.Context(), parent.Root, root, false) }); err != nil {
		t.Fatal(err)
	}
	manager := wt.Manager{Cfg: cfg}
	res, err := manager.Create(t.Context(), wt.CreateRequest{RepoPath: parent.Root, Branch: "feature", Base: "main", Path: root, NoProvision: true, NoRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if g.Nodes[0].Branch != "feature" || g.Nodes[0].HEAD != g.Nodes[0].Gitlink {
		t.Fatalf("member was not reconstructed: %+v", g)
	}
}

func TestNestedSubmoduleInitializationAndRetirement(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	leaf, child, parent := gittest.New(t), gittest.New(t), gittest.New(t)
	child.Git("submodule", "add", leaf.Root, "leaf")
	child.Git("commit", "-am", "test: nested leaf")
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: child")
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	root := filepath.Join(t.TempDir(), "feature")
	manager := wt.Manager{Cfg: cfg}
	res, err := manager.Create(t.Context(), wt.CreateRequest{RepoPath: parent.Root, Branch: "feature", Base: "main", Path: root, NoProvision: true, NoRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Submodules) != 2 || !res.Submodules[1].Initialized || res.Submodules[1].Parent != "child" {
		t.Fatalf("nested graph: %+v", res.Submodules)
	}
	p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.VerifyRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = p.Apply(t.Context(), nil, func() error { return gitx.RemoveWorktree(t.Context(), parent.Root, root, false) }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("nested workspace remains")
	}
	if _, err = gitx.Discover(t.Context(), filepath.Join(parent.Root, "child")); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalInterruptedJournalRestoresBeforeAndAfterOuterRemoval(t *testing.T) {
	for _, removeOuter := range []bool{false, true} {
		t.Run(fmt.Sprint(removeOuter), func(t *testing.T) {
			cfg, parent, _, root := removalFixture(t)
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = p.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("interruption did not happen")
					}
				}()
				_ = p.Apply(t.Context(), nil, func() error {
					if removeOuter {
						if err := gitx.RemoveWorktree(t.Context(), parent.Root, root, false); err != nil {
							t.Fatal(err)
						}
					}
					panic("simulated interruption")
				})
			}()
			journals, err := filepath.Glob(filepath.Join(filepath.Dir(root), ".dev-submodule-retirement-*", "journal.json"))
			if err != nil || len(journals) != 1 {
				t.Fatalf("journals %v %v", journals, err)
			}
			if err := submodule.Recover(t.Context(), journals[0], nil, true); err != nil {
				t.Fatal(err)
			}
			if err := submodule.Recover(t.Context(), journals[0], nil, false); err != nil {
				t.Fatal(err)
			}
			g, err := gitx.SubmodulesOf(t.Context(), root)
			if err != nil || len(g.Nodes) != 1 || !g.Nodes[0].Initialized {
				t.Fatalf("restore: %+v %v", g, err)
			}
			if _, err := os.Stat(journals[0]); !os.IsNotExist(err) {
				t.Fatal("completed recovery journal remains")
			}
		})
	}
}
