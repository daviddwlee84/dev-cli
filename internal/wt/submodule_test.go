package wt_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

type submoduleRuntime struct {
	fakeRuntime
	opens int
}

func (r *submoduleRuntime) Open(ctx context.Context, path, label string) (runtime.OpenResult, error) {
	r.opens++
	return r.fakeRuntime.Open(ctx, path, label)
}

func TestSubmoduleInitializationFailurePreventsRuntimeAndRetainsCheckout(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("config", "--file", ".gitmodules", "submodule.child.url", filepath.Join(t.TempDir(), "missing"))
	parent.Git("commit", "-am", "test: unavailable child")
	cfg := cfgFor(t)
	cfg.Paths.StateDir = t.TempDir()
	rt := &submoduleRuntime{}
	m := wt.Manager{Cfg: cfg, Runtime: rt}
	created, err := m.Create(t.Context(), wt.CreateRequest{RepoPath: parent.Root, Branch: "feature", Base: "main", NoProvision: true})
	if err == nil || created == nil || rt.opens != 0 {
		t.Fatalf("created=%+v err=%v opens=%d", created, err, rt.opens)
	}
	if _, err := gitx.ResolveRegisteredWorktree(t.Context(), parent.Root, created.Path); err != nil {
		t.Fatal("partial checkout was not retained", err)
	}
}

func TestSubmodulePlanAndProvisionRespectExplicitOptOut(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	child, parent := gittest.New(t), gittest.New(t)
	parent.Git("submodule", "add", child.Root, "child")
	parent.Git("commit", "-am", "test: child")
	cfg := cfgFor(t)
	cfg.Paths.StateDir = t.TempDir()
	plan := wt.BuildPlan(t.Context(), wt.SettingsFor(cfg, parent.Root), parent.Root)
	found := false
	for _, step := range plan.Runnable() {
		if step.Kind == wt.StepInitSubmodules {
			found = true
		}
	}
	if !found {
		t.Fatal("Git initialization omitted from worktree plan")
	}
	m := wt.Manager{Cfg: cfg}
	created, err := m.Create(t.Context(), wt.CreateRequest{RepoPath: parent.Root, Branch: "feature", Base: "main", Submodules: "none"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if g.Nodes[0].Initialized {
		t.Fatal("provisioning overrode explicit submodules=none")
	}
}
