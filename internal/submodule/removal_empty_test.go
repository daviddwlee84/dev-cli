package submodule_test

import (
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

func emptyRemovalFixture(t *testing.T, gitlink string) (config.Config, *gittest.Repo, string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parent := gittest.New(t)
	if gitlink != "" {
		child := gittest.New(t)
		parent.Git("submodule", "add", child.Root, gitlink)
		parent.Git("commit", "-am", "test: empty gitlink cleanup")
	}
	root := filepath.Join(t.TempDir(), "empty-worktree")
	if err := gitx.AddWorktree(t.Context(), parent.Root, root, "empty-worktree", "HEAD"); err != nil {
		t.Fatal(err)
	}
	repo, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	return cfg, parent, root, filepath.Join(repo.GitDir, "modules")
}

func TestCleanupRegressionSubmoduleNeverInitialized(t *testing.T) {
	for _, childState := range []string{"absent", "empty"} {
		for _, storage := range []string{"absent", "empty", "scaffolding"} {
			t.Run(childState+"-"+storage, func(t *testing.T) {
				cfg, parent, root, modules := emptyRemovalFixture(t, "child")
				if childState == "absent" {
					if err := os.Remove(filepath.Join(root, "child")); err != nil {
						t.Fatal(err)
					}
				}
				if storage != "absent" {
					path := modules
					if storage == "scaffolding" {
						path = filepath.Join(modules, "unused", "nested")
					}
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := gitx.RemoveWorktree(t.Context(), parent.Root, root, false); err == nil {
						t.Fatal("ordinary modules-root guard weakened")
					}
				}
				preview, err := submodule.InspectRemoval(t.Context(), cfg, root, false, false)
				if err == nil || preview == nil || preview.LayoutFingerprint == "" {
					t.Fatalf("missing recursive/layout gate: %+v %v", preview, err)
				}
				if err := preview.VerifyRemote(t.Context()); err == nil {
					t.Fatal("non-consented observation authorized remote proof")
				}
				called := false
				if err := preview.Apply(t.Context(), nil, func() error { called = true; return nil }); err == nil || called {
					t.Fatal("non-consented observation authorized cleanup")
				}
				p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
				status, statusErr := gitx.StatusOf(t.Context(), root)
				if statusErr != nil {
					t.Fatal(statusErr)
				}
				if status.Dirty() {
					if childState != "absent" || err == nil || !strings.Contains(err.Error(), "outer checkout has uncommitted changes") {
						t.Fatalf("dirty absent gitlink not blocked locally: %v", err)
					}
					if storage != "absent" {
						if _, statErr := os.Stat(modules); statErr != nil {
							t.Fatal("administration mutated during observation", statErr)
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if p.LayoutFingerprint != preview.LayoutFingerprint || p.InitializedCount() != 0 {
					t.Fatal("consent changed local layout authority")
				}
				// No child network, temporary proof repository or quarantine is needed.
				t.Setenv("GIT_ALLOW_PROTOCOL", "none")
				proofTemp := t.TempDir()
				t.Setenv("TMPDIR", proofTemp)
				t.Setenv("TMP", proofTemp)
				t.Setenv("TEMP", proofTemp)
				if err := p.VerifyRemote(t.Context()); err != nil {
					t.Fatal(err)
				}
				checks := 0
				if err := p.Apply(t.Context(), func(context.Context, string) error { checks++; return nil }, func() error {
					return gitx.RemoveWorktree(t.Context(), parent.Root, root, false)
				}); err != nil {
					t.Fatal(err)
				}
				if checks == 0 {
					t.Fatal("zero-child final callback omitted")
				}
				if _, err := os.Lstat(root); !os.IsNotExist(err) {
					t.Fatalf("outer remains: %v", err)
				}
				if !gitx.BranchExists(t.Context(), parent.Root, "empty-worktree") {
					t.Fatal("outer branch deleted")
				}
				entries, err := os.ReadDir(proofTemp)
				if err != nil || len(entries) != 0 {
					t.Fatalf("all-empty cleanup created proof data: %v %v", entries, err)
				}
				areas, _ := filepath.Glob(filepath.Join(filepath.Dir(root), ".dev-submodule-retirement-*"))
				if len(areas) != 0 {
					t.Fatalf("all-empty cleanup quarantined data: %v", areas)
				}
			})
		}
	}
}

func TestCleanupRegressionSubmoduleZeroGitlinksStorage(t *testing.T) {
	for _, storage := range []string{"absent", "empty", "scaffolding", "orphan"} {
		t.Run(storage, func(t *testing.T) {
			cfg, parent, root, modules := emptyRemovalFixture(t, "")
			if storage != "absent" {
				path := modules
				if storage == "scaffolding" || storage == "orphan" {
					path = filepath.Join(modules, "old", "nested")
				}
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if storage == "orphan" {
					if err := os.WriteFile(filepath.Join(path, "HEAD"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			preview, previewErr := submodule.InspectRemoval(t.Context(), cfg, root, false, false)
			if storage == "absent" {
				if preview != nil || previewErr != nil {
					t.Fatalf("absent should be no-op: %+v %v", preview, previewErr)
				}
				return
			}
			if previewErr == nil {
				t.Fatal("administration cleanup implicitly authorized")
			}
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if storage == "orphan" {
				if err == nil {
					t.Fatal("orphan data accepted")
				}
				if p != nil {
					if err := p.VerifyRemote(t.Context()); err == nil {
						t.Fatal("incomplete observation authorized proof")
					}
					called := false
					if err := p.Apply(t.Context(), nil, func() error { called = true; return nil }); err == nil || called {
						t.Fatal("incomplete observation authorized cleanup")
					}
				}
				return
			}
			if err != nil || p == nil {
				t.Fatalf("scaffolding rejected: %v", err)
			}
			if err := p.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := p.Apply(t.Context(), nil, func() error { return gitx.RemoveWorktree(t.Context(), parent.Root, root, false) }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupRegressionSubmoduleEmptyClaimsCallbackBeforeEffects(t *testing.T) {
	for _, gitlink := range []string{"", "child"} {
		t.Run("gitlink-"+gitlink, func(t *testing.T) {
			cfg, _, root, modules := emptyRemovalFixture(t, gitlink)
			if err := os.MkdirAll(filepath.Join(modules, "a", "b"), 0700); err != nil {
				t.Fatal(err)
			}
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			blocked := errors.New("late ownership claim")
			called := false
			err = p.Apply(t.Context(), func(context.Context, string) error { return blocked }, func() error { called = true; return nil })
			if !errors.Is(err, blocked) || called {
				t.Fatalf("callback bypassed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(modules, "a", "b")); err != nil {
				t.Fatal("mutation before final callback", err)
			}
		})
	}
}

func TestCleanupRegressionSubmoduleEmptyUnsafeData(t *testing.T) {
	for _, kind := range []string{"file", "ignored", "nested-directory", "git-marker", "symlink", "storage-file", "storage-symlink", "retained-store"} {
		t.Run(kind, func(t *testing.T) {
			cfg, _, root, modules := emptyRemovalFixture(t, "child")
			child := filepath.Join(root, "child")
			switch kind {
			case "file", "ignored":
				if kind == "ignored" {
					if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("child/keep\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(child, "keep"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "nested-directory":
				if err := os.Mkdir(filepath.Join(child, "user-directory"), 0700); err != nil {
					t.Fatal(err)
				}
			case "git-marker":
				if err := os.WriteFile(filepath.Join(child, ".git"), []byte("not a valid gitdir\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(child); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), child); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			default:
				if err := os.MkdirAll(filepath.Join(modules, "child"), 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "storage-symlink" {
					if err := os.Symlink(t.TempDir(), filepath.Join(modules, "link")); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				} else {
					name := "keep"
					if kind == "retained-store" {
						name = "HEAD"
					}
					if err := os.WriteFile(filepath.Join(modules, "child", name), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err == nil {
				t.Fatalf("unsafe %s accepted", kind)
			}
			if p != nil {
				if err := p.VerifyRemote(t.Context()); err == nil {
					t.Fatal("unsafe nonnil plan authorized proof")
				}
				called := false
				if err := p.Apply(t.Context(), nil, func() error { called = true; return nil }); err == nil || called {
					t.Fatal("unsafe nonnil plan authorized effects")
				}
			}
		})
	}
}

func TestCleanupRegressionSubmoduleEmptyStaleLayout(t *testing.T) {
	for _, kind := range []string{"new-file", "new-directory", "empty-replaced", "absent-present", "ancestor-present", "ancestor-replaced", "initialized", "root-replaced", "admin-replaced", "admin-added", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			gitlink := "child"
			if strings.HasPrefix(kind, "ancestor-") {
				gitlink = "deps/nested/child"
			}
			cfg, _, root, modules := emptyRemovalFixture(t, gitlink)
			child := filepath.Join(root, filepath.FromSlash(gitlink))
			if kind == "absent-present" || kind == "ancestor-replaced" {
				if err := os.Remove(child); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "ancestor-present" {
				if err := os.RemoveAll(filepath.Join(root, "deps")); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "admin-replaced" {
				if err := os.Mkdir(modules, 0700); err != nil {
					t.Fatal(err)
				}
			}
			p, inspectErr := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			absentBaseline := kind == "absent-present" || strings.HasPrefix(kind, "ancestor-")
			if inspectErr != nil && (!absentBaseline || p == nil || p.LayoutFingerprint == "" || !strings.Contains(inspectErr.Error(), "outer checkout has uncommitted changes")) {
				t.Fatal(inspectErr)
			}
			if inspectErr == nil {
				if err := p.VerifyRemote(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch kind {
			case "new-file":
				err = os.WriteFile(filepath.Join(child, "keep"), []byte("keep"), 0600)
			case "new-directory":
				err = os.Mkdir(filepath.Join(child, "keep"), 0700)
			case "empty-replaced", "admin-replaced", "root-replaced", "ancestor-replaced":
				path := child
				if kind == "admin-replaced" {
					path = modules
				}
				if kind == "root-replaced" {
					path = root
				}
				if kind == "ancestor-replaced" {
					path = filepath.Dir(child)
				}
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				err = os.Mkdir(path, 0700)
			case "absent-present":
				err = os.Mkdir(child, 0700)
			case "ancestor-present":
				err = os.Mkdir(filepath.Join(root, "deps"), 0700)
			case "initialized":
				_, err = gitx.InitSubmodules(t.Context(), root)
			case "admin-added":
				err = os.MkdirAll(filepath.Join(modules, "unexpected"), 0700)
			case "symlink":
				if err := os.Remove(child); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(t.TempDir(), child)
				if err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if absentBaseline {
				fresh, _ := submodule.InspectRemoval(t.Context(), cfg, root, false, false)
				if fresh == nil || fresh.LayoutFingerprint == p.LayoutFingerprint {
					t.Fatal("missing component/ancestor identity change not captured")
				}
			}
			called := false
			err = p.Apply(t.Context(), nil, func() error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("stale %s applied: %v", kind, err)
			}
		})
	}
}

func TestCleanupRegressionSubmodulePartialPruning(t *testing.T) {
	for _, fail := range []string{"late-claim", "late-data", "outer"} {
		t.Run(fail, func(t *testing.T) {
			cfg, _, root, modules := emptyRemovalFixture(t, "child")
			if err := os.MkdirAll(filepath.Join(modules, "a", "b"), 0700); err != nil {
				t.Fatal(err)
			}
			p, err := submodule.InspectRemoval(t.Context(), cfg, root, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.VerifyRemote(t.Context()); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected failure")
			checks, outer := 0, false
			err = p.Apply(t.Context(), func(context.Context, string) error {
				checks++
				if checks == 2 {
					if fail == "late-claim" {
						return cause
					}
					if fail == "late-data" {
						return os.WriteFile(filepath.Join(modules, "a", "keep"), []byte("keep"), 0600)
					}
				}
				return nil
			}, func() error { outer = true; return cause })
			var partial *submodule.PrunedDirectoriesError
			if !errors.As(err, &partial) || len(partial.Paths) == 0 || partial.Paths[0] != "modules/a/b" {
				t.Fatalf("missing typed partial effect: %v", err)
			}
			if fail != "late-data" && !errors.Is(err, cause) {
				t.Fatal("cause lost", err)
			}
			if fail != "outer" && outer {
				t.Fatal("outer ran after stale claim/data")
			}
			if _, err := os.Stat(root); err != nil {
				t.Fatal("outer checkout removed", err)
			}
			if _, err := os.Stat(filepath.Join(modules, "a", "b")); !os.IsNotExist(err) {
				t.Fatal("pruned directory silently recreated")
			}
		})
	}
}
