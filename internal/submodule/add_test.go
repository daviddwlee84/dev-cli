package submodule_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
)

const addSource = "https://source.example.test/team/library.git"

func addFixture(t *testing.T) (*gittest.Repo, *gittest.Repo, config.Config) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parent, child := gittest.New(t), gittest.New(t)
	child.Git("branch", "-m", "trunk")
	// Exercise real Git clones without network access. Production still rejects
	// local sources; a process-local Git rewrite maps the test URL to a fixture.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(child.Root)+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", addSource)
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	return parent, child, cfg
}

func TestAddPinnedPreservesParentIndexAndDirtyWork(t *testing.T) {
	parent, child, cfg := addFixture(t)
	parent.Write("README.md", "staged\n")
	parent.Git("add", "README.md")
	parent.Write("README.md", "unstaged\n")
	staged := parent.Git("diff", "--cached", "--binary")
	configBefore, _ := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	plan, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "libs/library"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Report().Checkout != "pinned" || plan.Report().HEAD != "" {
		t.Fatal(plan.Report())
	}
	r, err := plan.Apply(t.Context(), nil)
	if err != nil {
		t.Fatalf("%+v: %v", r, err)
	}
	if r.Phase != "complete" || !r.Staged || r.Branch != "" || r.HEAD != child.Git("rev-parse", "HEAD") {
		t.Fatal(r)
	}
	if diff := parent.Git("diff", "--cached", "--binary", "--", "README.md"); diff != staged {
		t.Fatalf("existing index changed: %s", diff)
	}
	if data, _ := os.ReadFile(filepath.Join(parent.Root, "README.md")); string(data) != "unstaged\n" {
		t.Fatal("dirty content changed")
	}
	configAfter, _ := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("parent shared config changed")
	}
	graph, err := gitx.SubmodulesOf(t.Context(), parent.Root)
	if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].HEAD != graph.Nodes[0].Gitlink || !graph.Nodes[0].Status.Detached {
		t.Fatalf("%+v %v", graph, err)
	}
	if _, err := plan.Apply(t.Context(), nil); err == nil {
		t.Fatal("replayed plan accepted")
	}
}

func TestAddModesAndRefs(t *testing.T) {
	for _, tc := range []struct {
		name, mode, ref string
		wantError       bool
	}{
		{"default", "default-branch", "", false}, {"tag", "pinned", "v1", false}, {"commit", "pinned", "HEAD", false}, {"branch", "pinned", "feature", false}, {"invalid", "pinned", "does-not-exist", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, child, cfg := addFixture(t)
			child.Git("tag", "v1")
			child.Git("branch", "feature")
			p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "library", Checkout: tc.mode, Ref: tc.ref})
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.Apply(t.Context(), nil)
			if tc.wantError {
				if err == nil || r.Phase != "cloned" || len(r.Warnings) == 0 {
					t.Fatalf("%+v %v", r, err)
				}
				if _, err := os.Stat(filepath.Join(parent.Root, ".gitmodules")); !os.IsNotExist(err) {
					t.Fatal("failed ref published metadata")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == "default-branch" {
				if r.Branch != "trunk" {
					t.Fatal(r)
				}
				if got := parent.GitIn(filepath.Join(parent.Root, "library"), "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/trunk" {
					t.Fatal(got)
				}
			}
			data, _ := os.ReadFile(filepath.Join(parent.Root, ".gitmodules"))
			if strings.Contains(string(data), "branch =") {
				t.Fatal("implicitly enabled update --remote policy")
			}
		})
	}
}

func TestAddLinkedAndNestedIsolation(t *testing.T) {
	parent, _, cfg := addFixture(t)
	wt := filepath.Join(t.TempDir(), "feature")
	parent.Git("worktree", "add", "-b", "feature", wt, "HEAD")
	configBefore, _ := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: wt, Source: addSource, Path: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if parent.Git("status", "--porcelain") != "" {
		t.Fatal("canonical checkout changed")
	}
	after, _ := os.ReadFile(filepath.Join(parent.Root, ".git", "config"))
	if !bytes.Equal(configBefore, after) {
		t.Fatal("shared config changed")
	}
	parent.GitIn(wt, "commit", "-m", "add child")
	if _, err := submodule.InspectRemoval(t.Context(), cfg, wt, true, false); err != nil {
		t.Fatalf("new clone incompatible with retirement: %v", err)
	}
	childPath := filepath.Join(wt, "child")
	parent.GitIn(childPath, "switch", "-c", "nested-work")
	// A different portable URL to the same fixture allows a nested layout test.
	other := "https://source.example.test/team/second.git"
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_1", os.Getenv("GIT_CONFIG_KEY_0"))
	t.Setenv("GIT_CONFIG_VALUE_1", other)
	nested, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: childPath, Source: other, Path: "nested"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nested.Apply(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.SubmodulesOf(t.Context(), wt)
	if err != nil || len(g.Nodes) != 2 || g.Nodes[1].Path != "child/nested" {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestAddRejectsUnsafeAndExistingTargets(t *testing.T) {
	for _, path := range []string{"../escape", "/absolute", ".git/data", "child/../../escape", "child\\escape", "child/.git/config", ".", "-child"} {
		t.Run(path, func(t *testing.T) {
			parent, _, cfg := addFixture(t)
			if _, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: path}); err == nil {
				t.Fatal("accepted unsafe target")
			}
		})
	}
	for _, kind := range []string{"existing", "store", "modules-dirty", "operation", "symlink", "tracked-deletion", "self"} {
		t.Run(kind, func(t *testing.T) {
			parent, _, cfg := addFixture(t)
			path, source := "child", addSource
			switch kind {
			case "existing":
				parent.Write("child/keep", "data")
			case "store":
				parent.Write(".git/modules/child/keep", "data")
			case "modules-dirty":
				parent.Write(".gitmodules", "# user edits\n")
			case "operation":
				parent.Write(".git/MERGE_HEAD", parent.Git("rev-parse", "HEAD"))
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(parent.Root, "linked")); err != nil {
					t.Skip(err)
				}
				path = "linked/child"
			case "tracked-deletion":
				path = "README.md"
				if err := os.Remove(filepath.Join(parent.Root, path)); err != nil {
					t.Fatal(err)
				}
			case "self":
				source = "https://parent.example.test/repo.git"
				parent.Git("remote", "add", "origin", source)
			}
			if _, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: source, Path: path}); err == nil {
				t.Fatal("accepted unsafe state")
			}
		})
	}
}

func TestAddStaleAndOccupiedPlanHasNoCloneEffect(t *testing.T) {
	for _, change := range []string{"index", "branch", "config", "occupancy"} {
		t.Run(change, func(t *testing.T) {
			parent, _, cfg := addFixture(t)
			p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"})
			if err != nil {
				t.Fatal(err)
			}
			var guard func(context.Context, string) error
			switch change {
			case "index":
				parent.Write("staged", "keep")
				parent.Git("add", "staged")
			case "branch":
				parent.Git("switch", "-c", "other")
			case "config":
				parent.Git("config", "user.name", "changed")
			case "occupancy":
				guard = func(context.Context, string) error { return errors.New("occupied") }
			}
			r, err := p.Apply(t.Context(), guard)
			if err == nil || r.Phase != "not-added" {
				t.Fatalf("%+v %v", r, err)
			}
			if _, err := os.Stat(filepath.Join(parent.Root, "child")); !os.IsNotExist(err) {
				t.Fatal("stale plan cloned")
			}
		})
	}
}

func TestAddSecondGuardPreservesCloneWithoutPublishing(t *testing.T) {
	parent, _, cfg := addFixture(t)
	p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r, err := p.Apply(t.Context(), func(context.Context, string) error {
		calls++
		if calls == 2 {
			return errors.New("new writer")
		}
		return nil
	})
	if err == nil || r.Phase != "cloned" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(parent.Root, "child", "README.md")); err != nil {
		t.Fatal("clone not retained", err)
	}
	if _, err := os.Stat(filepath.Join(parent.Root, ".gitmodules")); !os.IsNotExist(err) {
		t.Fatal("published without guard")
	}
}

func TestAddPreservesMetadataAndReportsStagingFailure(t *testing.T) {
	parent, _, cfg := addFixture(t)
	parent.Commit(".gitmodules", "# keep my comments\n", "existing modules file")
	parent.Commit(".gitignore", "child\n", "ignore target")
	p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Apply(t.Context(), nil)
	if err == nil || r.Phase != "added-unstaged" || r.Staged || len(r.Warnings) == 0 {
		t.Fatalf("%+v %v", r, err)
	}
	data, _ := os.ReadFile(filepath.Join(parent.Root, ".gitmodules"))
	if !strings.Contains(string(data), "# keep my comments") || !strings.Contains(string(data), addSource) {
		t.Fatal("metadata not retained")
	}
	if _, err := os.Stat(filepath.Join(parent.Root, "child", "README.md")); err != nil {
		t.Fatal("checkout not retained")
	}
}

func TestAddHiddenGitmodulesEditsAndFiltersFailClosed(t *testing.T) {
	for _, scenario := range []string{"assume-unchanged", "skip-worktree", "filter"} {
		t.Run(scenario, func(t *testing.T) {
			parent, _, cfg := addFixture(t)
			parent.Commit(".gitmodules", "# baseline\n", "modules")
			if scenario == "filter" {
				parent.Commit(".gitattributes", ".gitmodules filter=custom\n", "filter")
			} else {
				parent.Git("update-index", "--"+scenario, ".gitmodules")
				parent.Write(".gitmodules", "# hidden user edit\n")
			}
			if _, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"}); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}

func TestAddRecursiveInitializationOnlyTouchesNewSubtree(t *testing.T) {
	for _, mode := range []string{"recursive", "none", "failure"} {
		t.Run(mode, func(t *testing.T) {
			parent, source, cfg := addFixture(t)
			grand := gittest.New(t)
			source.Git("submodule", "add", grand.Root, "nested")
			source.Git("commit", "-am", "nested")
			if mode == "failure" {
				source.Git("config", "--file", ".gitmodules", "submodule.nested.url", filepath.Join(t.TempDir(), "missing.git"))
				source.Git("commit", "-am", "missing remote")
			}
			init := mode
			if init == "failure" {
				init = "recursive"
			}
			p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child", Init: init})
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.Apply(t.Context(), nil)
			if mode == "failure" {
				if err == nil || r.Phase != "initialization-incomplete" || !r.Staged {
					t.Fatalf("%+v %v", r, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			g, err := gitx.SubmodulesOf(t.Context(), parent.Root)
			if err != nil || len(g.Nodes) != 2 || g.Nodes[1].Initialized != (mode == "recursive") {
				t.Fatalf("%+v %v", g, err)
			}
		})
	}
}

func TestAddEmptyRemoteRetainsClone(t *testing.T) {
	parent, _, cfg := addFixture(t)
	remote := filepath.Join(t.TempDir(), "empty.git")
	parent.Git("init", "--bare", remote)
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(remote)+".insteadOf")
	p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Apply(t.Context(), nil)
	if err == nil || r.Phase != "cloned" || r.Staged {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestAddRevalidatesTargetsAndChildAfterGuard(t *testing.T) {
	for _, scenario := range []string{"target", "child-head", "parent-index"} {
		t.Run(scenario, func(t *testing.T) {
			parent, _, cfg := addFixture(t)
			p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			r, err := p.Apply(t.Context(), func(context.Context, string) error {
				calls++
				if scenario == "target" && calls == 1 {
					return os.Mkdir(filepath.Join(parent.Root, "child"), 0755)
				}
				if calls == 2 {
					switch scenario {
					case "child-head":
						parent.GitIn(filepath.Join(parent.Root, "child"), "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "new work")
					case "parent-index":
						parent.Write("other", "keep")
						parent.Git("add", "other")
					}
				}
				return nil
			})
			if err == nil || r.Staged {
				t.Fatalf("%+v %v", r, err)
			}
			if _, err := os.Stat(filepath.Join(parent.Root, ".gitmodules")); !os.IsNotExist(err) {
				t.Fatal("published after guard mutation")
			}
		})
	}
}

func TestAddAcceptsCleanCRLFMetadata(t *testing.T) {
	parent, _, cfg := addFixture(t)
	parent.Commit(".gitmodules", "# portable comment\n", "modules")
	parent.Git("config", "core.autocrlf", "true")
	parent.Write(".gitmodules", "# portable comment\r\n")
	if parent.Git("hash-object", "--path=.gitmodules", ".gitmodules") != parent.Git("rev-parse", "HEAD:.gitmodules") {
		t.Fatal("fixture is not canonically clean")
	}
	p, err := submodule.PlanAdd(t.Context(), cfg, submodule.AddRequest{Parent: parent.Root, Source: addSource, Path: "child"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Apply(t.Context(), nil)
	if err != nil || !r.Staged {
		t.Fatalf("%+v %v", r, err)
	}
}
