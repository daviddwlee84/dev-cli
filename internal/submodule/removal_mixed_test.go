package submodule_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
)

func mixedRemovalFixture(t *testing.T, nested bool) (config.Config, *gittest.Repo, string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	leaf, child, parent := gittest.New(t), gittest.New(t), gittest.New(t)
	empty := "empty"
	if nested {
		child.Git("submodule", "add", leaf.Root, "empty")
		child.Git("commit", "-am", "test: nested empty gitlink")
		empty = "child/empty"
	} else {
		parent.Git("submodule", "add", leaf.Root, "empty")
	}
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: mixed gitlinks")
	root := filepath.Join(t.TempDir(), "mixed")
	if err := gitx.AddWorktree(t.Context(), parent.Root, root, "mixed", "HEAD"); err != nil {
		t.Fatal(err)
	}
	// Initialize exactly the outer child, not its nested leaf or sibling.
	if _, err := gitx.Run(t.Context(), root, "-c", "submodule.child.url="+child.Root, "-c", "submodule.child.active=true", "submodule", "update", "--init", "--checkout", "--", "child"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	return cfg, parent, root, filepath.Join(root, filepath.FromSlash(empty))
}

func TestCleanupRegressionSubmoduleMixedInitializedEmptyRollback(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "siblings"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			cfg, parent, root, empty := mixedRemovalFixture(t, nested)
			before, err := os.Stat(empty)
			if err != nil {
				t.Fatal(err)
			}
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Graph.Nodes) != 2 || p.InitializedCount() != 1 {
				t.Fatalf("wrong initialized subset: %+v", p.Graph)
			}
			if err := p.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("outer failed")
			if err := p.Apply(t.Context(), nil, func() error { return cause }); !errors.Is(err, cause) {
				t.Fatalf("rollback: %v", err)
			}
			after, err := os.Stat(empty)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("empty child not retained exactly: %v", err)
			}
			g, err := gitx.SubmodulesOf(t.Context(), root)
			if err != nil || len(g.Nodes) != 2 || !g.Nodes[0].Initialized {
				t.Fatalf("initialized clone not restored: %+v %v", g, err)
			}
			// Lease creation/closure is intentionally not a user layout change.
			fresh, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err != nil || fresh.LayoutFingerprint != p.LayoutFingerprint {
				t.Fatalf("lease/rollback changed layout authority: %v", err)
			}
			if err := fresh.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := fresh.Apply(t.Context(), nil, func() error { return gitx.RemoveWorktree(t.Context(), parent.Root, root, false) }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("mixed outer remains", err)
			}
		})
	}
}

func TestCleanupRegressionSubmoduleMixedLateEmptyData(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "siblings"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			cfg, _, root, empty := mixedRemovalFixture(t, nested)
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			called := false
			err = p.Apply(t.Context(), func(context.Context, string) error {
				return os.WriteFile(filepath.Join(empty, "keep"), []byte("keep"), 0600)
			}, func() error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("late empty-child data accepted: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(empty, "keep"))
			if err != nil || string(data) != "keep" {
				t.Fatal("late data removed", err)
			}
			if _, err := gitx.Discover(t.Context(), filepath.Join(root, "child")); err != nil {
				t.Fatal("initialized child moved before revalidation", err)
			}
			areas, _ := filepath.Glob(filepath.Join(filepath.Dir(root), ".dev-submodule-retirement-*"))
			if len(areas) != 0 {
				t.Fatal("late data caused staging", areas)
			}
		})
	}
}

func TestCleanupRegressionSubmoduleDeinitializedRetainedStore(t *testing.T) {
	cfg, _, _, root := removalFixture(t)
	child := filepath.Join(root, "child")
	// Test-only deinitialization: retain every private Git byte but remove the
	// disposable fixture checkout. Empty is unsafe when its clone still exists.
	if err := os.RemoveAll(child); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false); err == nil {
		t.Fatal("deinitialized retained store was treated as empty")
	}
}

func TestCleanupRegressionSubmoduleMixedRecovery(t *testing.T) {
	cfg, parent, root, _ := mixedRemovalFixture(t, true)
	p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.VerifyRemote(t.Context()); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("interruption not reached")
			}
		}()
		_ = p.Apply(t.Context(), nil, func() error {
			if err := gitx.RemoveWorktree(t.Context(), parent.Root, root, false); err != nil {
				t.Fatal(err)
			}
			panic("fixture interruption after outer removal")
		})
	}()
	journals, err := filepath.Glob(filepath.Join(filepath.Dir(root), ".dev-submodule-retirement-*", "journal.json"))
	if err != nil || len(journals) != 1 {
		t.Fatalf("journal missing: %v %v", journals, err)
	}
	if err := submodule.Recover(t.Context(), journals[0], nil, false); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), root)
	if err != nil || len(g.Nodes) != 2 || !g.Nodes[0].Initialized || g.Nodes[1].Initialized {
		t.Fatalf("mixed recovery: %+v %v", g, err)
	}
}
