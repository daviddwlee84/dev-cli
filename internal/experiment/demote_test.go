package experiment_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func demoteHooks() experiment.Hooks {
	return experiment.Hooks{
		DemoteGuard: func(context.Context, experiment.DemoteGuardRequest) (string, error) { return "observed-unclaimed", nil },
		DemoteLock:  gitx.WithLifecycleMoveLock,
	}
}

func graduatedForDemotion(t *testing.T) (*fixture, experiment.GraduateResult) {
	t.Helper()
	f := newFixture(t, demoteHooks(), 1)
	source := f.mkdir("2026-08-20-demote")
	initRepo(t, source, true)
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(source))
	graduated, err := f.service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	return f, graduated
}

func TestDemotePreservesCurrentWorkIdentityAndHistory(t *testing.T) {
	f, graduated := graduatedForDemotion(t)
	source := graduated.Plan.Destination
	runGit(t, source, "remote", "add", "origin", "https://example.invalid/owner/demo.git")
	runGit(t, source, "tag", "local-tag")
	for path, body := range map[string]string{"README.md": "changed\n", "untracked.txt": "new work\n", ".gitignore": "ignored.dat\n", "ignored.dat": "keep ignored\n"} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := f.registry.Update(graduated.Item.ID, func(e *catalog.Entry) error {
		e.Tags = []string{"keep"}
		e.Note = "promising"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	head := runGit(t, source, "rev-parse", "HEAD")
	plan, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: before.ID, Expected: before})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source != source || plan.Destination != before.Experiment.OriginalPath || plan.ID != before.ID {
		t.Fatalf("plan = %+v", plan)
	}
	result, err := f.service.ApplyDemote(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Moved || result.Item.ID != before.ID || result.Item.Kind != catalog.KindTry || result.Item.Phase != catalog.PhaseActive {
		t.Fatalf("demoted result = %+v", result)
	}
	entry, err := f.store.Get(before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Note != before.Note || strings.Join(entry.Tags, ",") != "keep" ||
		entry.Experiment.GraduatedPath != before.Experiment.GraduatedPath ||
		!entry.Experiment.GraduatedAt.Equal(before.Experiment.GraduatedAt) ||
		!entry.Experiment.Started.Equal(before.Experiment.Started) || entry.MoveIntent != nil {
		t.Fatalf("history or metadata changed: %+v", entry)
	}
	if got := runGit(t, plan.Destination, "rev-parse", "HEAD"); got != head {
		t.Fatal("HEAD changed")
	}
	if got := runGit(t, plan.Destination, "tag", "--list"); got != "local-tag" {
		t.Fatalf("tags = %q", got)
	}
	if got := runGit(t, plan.Destination, "remote", "get-url", "origin"); got != "https://example.invalid/owner/demo.git" {
		t.Fatalf("origin = %q", got)
	}
	for _, path := range []string{"README.md", "untracked.txt", "ignored.dat", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(plan.Destination, path)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(plan.Source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source retained: %v", err)
	}
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil || len(items) != 1 || items[0].ID != entry.ID {
		t.Fatalf("demoted list = %+v, %v", items, err)
	}
	// The same identity remains usable by the existing lifecycle.
	if _, err := f.service.Archive(t.Context(), experiment.TransitionRequest{Ref: entry.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Restore(t.Context(), experiment.TransitionRequest{Ref: entry.ID}); err != nil {
		t.Fatal(err)
	}
	reg, err := f.service.Graduate(t.Context(), experiment.GraduateRequest{Ref: entry.ID})
	if err != nil || reg.Item.ID != entry.ID {
		t.Fatalf("regraduate = %+v, %v", reg, err)
	}
}

func TestDemoteSelectionAndDryRunAreReadOnly(t *testing.T) {
	f, graduated := graduatedForDemotion(t)
	before, err := os.ReadFile(filepath.Join(f.store.Dir, graduated.Item.ID+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{graduated.Item.ID, graduated.Plan.Destination, graduated.Plan.Name} {
		plan, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: ref, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.service.ApplyDemote(t.Context(), plan)
		if err != nil || result.Moved {
			t.Fatalf("dry-run = %+v, %v", result, err)
		}
	}
	after, err := os.ReadFile(filepath.Join(f.store.Dir, graduated.Item.ID+".toml"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("dry-run changed catalog: %v", err)
	}
	if _, err := os.Stat(graduated.Plan.Destination); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(f.projects, "ordinary")
	initRepo(t, plain, true)
	if _, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: plain}); !errors.Is(err, experiment.ErrNotFound) {
		t.Fatalf("ordinary repository accepted: %v", err)
	}
	stale := graduated.Item.Entry.Clone()
	stale.Note = "outdated UI selection"
	if _, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID, Expected: stale}); err == nil {
		t.Fatal("stale expected entry accepted")
	}
}

func TestDemoteRequiresSafeFreeDestination(t *testing.T) {
	for _, kind := range []string{"occupied-original", "outside-original", "nested-to", "outside-to", "symlink", "dangling-inside", "cross-filesystem", "cwd", "nested-repo", "nested-bare", "linked-children", "missing-guard", "external-storage", "gitlink", "alternates", "core-worktree", "symlink-git-objects"} {
		t.Run(kind, func(t *testing.T) {
			f, graduated := graduatedForDemotion(t)
			hooks := demoteHooks()
			request := experiment.DemoteRequest{Ref: graduated.Item.ID}
			switch kind {
			case "occupied-original":
				if err := os.Mkdir(graduated.Plan.Source, 0700); err != nil {
					t.Fatal(err)
				}
			case "outside-original":
				_, err := f.registry.Update(graduated.Item.ID, func(e *catalog.Entry) error { e.Experiment.OriginalPath = filepath.Join(f.root, "outside"); return nil })
				if err != nil {
					t.Fatal(err)
				}
			case "nested-to":
				request.To = "nested/path"
			case "outside-to":
				request.To = filepath.Join(f.root, "outside")
			case "symlink":
				if err := os.Symlink(filepath.Join(f.root, "outside"), graduated.Plan.Source); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "dangling-inside":
				if err := os.Symlink(filepath.Join(f.tries, "missing-target"), graduated.Plan.Source); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "cross-filesystem":
				hooks.SameFilesystem = func(string, string) (bool, error) { return false, nil }
			case "cwd":
				hooks.Getwd = func() (string, error) { return graduated.Plan.Destination, nil }
			case "nested-repo":
				initRepo(t, filepath.Join(graduated.Plan.Destination, "nested"), true)
			case "nested-bare":
				runGit(t, graduated.Plan.Destination, "init", "--bare", "nested.git")
			case "linked-children":
				if err := gitx.AddWorktree(t.Context(), graduated.Plan.Destination, filepath.Join(f.root, "child"), "feat/child", "main"); err != nil {
					t.Fatal(err)
				}
			case "missing-guard":
				hooks.DemoteGuard = nil
			case "external-storage":
				external := filepath.Join(f.root, "external.git")
				if err := os.Rename(filepath.Join(graduated.Plan.Destination, ".git"), external); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(graduated.Plan.Destination, ".git"), []byte("gitdir: "+external+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "gitlink":
				head := runGit(t, graduated.Plan.Destination, "rev-parse", "HEAD")
				runGit(t, graduated.Plan.Destination, "update-index", "--add", "--cacheinfo", "160000,"+head+",child")
			case "alternates":
				if err := os.WriteFile(filepath.Join(graduated.Plan.Destination, ".git", "objects", "info", "alternates"), []byte(filepath.Join(f.root, "external-objects")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "core-worktree":
				runGit(t, graduated.Plan.Destination, "config", "core.worktree", graduated.Plan.Destination)
			case "symlink-git-objects":
				objects := filepath.Join(graduated.Plan.Destination, ".git", "objects")
				external := filepath.Join(f.root, "external-objects")
				if err := os.Rename(objects, external); err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(filepath.Dir(objects), external)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(relative, objects); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			service := serviceWithHooks(t, f, hooks)
			if _, err := service.PlanDemote(t.Context(), request); err == nil {
				t.Fatal("unsafe demotion accepted")
			}
			if _, err := os.Stat(graduated.Plan.Destination); err != nil {
				t.Fatal("source changed", err)
			}
			if kind == "occupied-original" || kind == "outside-original" {
				request.To = "2026-09-24-returned"
				if _, err := service.PlanDemote(t.Context(), request); err != nil {
					t.Fatalf("explicit safe destination refused: %v", err)
				}
			}
		})
	}
}

func TestDemoteKeepsWorkingTreeSymlinks(t *testing.T) {
	f, graduated := graduatedForDemotion(t)
	if err := os.Symlink("README.md", filepath.Join(graduated.Plan.Destination, "readme-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	plan, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyDemote(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(plan.Destination, "readme-link")); err != nil || target != "README.md" {
		t.Fatalf("link = %q, %v", target, err)
	}
}

func TestDemoteRejectsChangedReviewAndBoundaryAuthority(t *testing.T) {
	for _, kind := range []string{"modified-plan", "catalog", "contents", "guard", "guard-at-boundary", "parent", "source"} {
		t.Run(kind, func(t *testing.T) {
			f, graduated := graduatedForDemotion(t)
			guard := "v1"
			calls := 0
			hooks := demoteHooks()
			hooks.DemoteGuard = func(context.Context, experiment.DemoteGuardRequest) (string, error) {
				calls++
				if kind == "guard-at-boundary" && calls >= 3 {
					return "v2", nil
				}
				return guard, nil
			}
			service := serviceWithHooks(t, f, hooks)
			plan, err := service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "modified-plan":
				plan.Destination = filepath.Join(f.tries, "other")
			case "catalog":
				if _, err := f.registry.Update(graduated.Item.ID, func(e *catalog.Entry) error { e.Note = "changed"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "contents":
				if err := os.WriteFile(filepath.Join(plan.Source, "late"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			case "guard":
				guard = "v2"
			case "parent":
				if err := os.Rename(f.tries, f.tries+"-previous"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.tries, 0700); err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := os.Rename(plan.Source, plan.Source+"-previous"); err != nil {
					t.Fatal(err)
				}
				initRepo(t, plan.Source, true)
			}
			result, err := service.ApplyDemote(t.Context(), plan)
			if err == nil || result.Moved {
				t.Fatalf("changed plan applied: %+v, %v", result, err)
			}
			if _, err := os.Stat(plan.Source); err != nil {
				t.Fatal("source lost", err)
			}
			if _, err := os.Lstat(plan.Destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("destination created", err)
			}
		})
	}
}

func TestDemoteLinkedCheckoutPreservesMainRepository(t *testing.T) {
	f := newFixture(t, demoteHooks(), 1)
	main := filepath.Join(f.root, "main")
	initRepo(t, main, true)
	linked := filepath.Join(f.tries, "2026-08-20-linked")
	if err := gitx.AddWorktree(t.Context(), main, linked, "feat/try", "main"); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(linked))
	graduated, err := f.service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.LinkedWorktree {
		t.Fatal("linked checkout identity lost")
	}
	result, err := f.service.ApplyDemote(t.Context(), plan)
	if err != nil || !result.Moved {
		t.Fatalf("demote linked: %+v, %v", result, err)
	}
	wt, ok, err := gitx.WorktreeFor(t.Context(), main, "feat/try")
	if err != nil || !ok || wt.Path != plan.Destination {
		t.Fatalf("registration = %+v, %v", wt, err)
	}
	if _, err := os.Stat(filepath.Join(main, ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestDemoteCatalogFailureRollsBackOrRetainsRecovery(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "retained-recovery"}[rollbackFails], func(t *testing.T) {
			f, graduated := graduatedForDemotion(t)
			hooks := demoteHooks()
			failure := errors.New("injected catalog failure")
			hooks.CatalogUpdate = func(id string, mutate func(*catalog.Entry) error) (*catalog.Entry, error) {
				current, err := f.store.Get(id)
				if err != nil {
					return nil, err
				}
				candidate := current.Clone()
				if err := mutate(candidate); err != nil {
					return nil, err
				}
				if candidate.Kind == catalog.KindTry {
					return nil, failure
				}
				return f.store.UpdateUnderLock(id, mutate)
			}
			if rollbackFails {
				moves := 0
				hooks.Rename = func(source, destination string) error {
					moves++
					if moves > 1 {
						return errors.New("injected rollback failure")
					}
					return os.Rename(source, destination)
				}
			}
			service := serviceWithHooks(t, f, hooks)
			plan, err := service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.ApplyDemote(t.Context(), plan)
			if !errors.Is(err, failure) || result.RolledBack == rollbackFails || result.Moved != rollbackFails {
				t.Fatalf("failure result = %+v, %v", result, err)
			}
			entry, err := f.store.Get(plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if entry.Kind != catalog.KindRepository || (entry.MoveIntent != nil) != rollbackFails {
				t.Fatalf("catalog = %+v", entry)
			}
			if rollbackFails {
				if diagnostics, err := f.service.ReconcileMoveIntents(t.Context()); err != nil || len(diagnostics) > 0 {
					t.Fatalf("reconcile = %+v, %v", diagnostics, err)
				}
				entry, err = f.store.Get(plan.ID)
				if err != nil || entry.Kind != catalog.KindTry || entry.MoveIntent != nil {
					t.Fatalf("recovered catalog = %+v, %v", entry, err)
				}
			}
		})
	}
}

func TestDemoteInterruptedRecoveryRetainsAmbiguousOrReplacedPaths(t *testing.T) {
	for _, state := range []string{"source-only", "both", "neither", "replaced-destination", "replaced-source"} {
		t.Run(state, func(t *testing.T) {
			f, graduated := graduatedForDemotion(t)
			hooks := demoteHooks()
			moves := 0
			hooks.Rename = func(source, destination string) error {
				moves++
				if moves > 1 {
					return errors.New("stop rollback")
				}
				return os.Rename(source, destination)
			}
			hooks.CatalogUpdate = func(id string, mutate func(*catalog.Entry) error) (*catalog.Entry, error) {
				entry, err := f.store.Get(id)
				if err != nil {
					return nil, err
				}
				if err := mutate(entry); err != nil {
					return nil, err
				}
				if entry.Kind == catalog.KindTry {
					return nil, errors.New("stop finalization")
				}
				return f.store.UpdateUnderLock(id, mutate)
			}
			service := serviceWithHooks(t, f, hooks)
			plan, err := service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
			if err != nil {
				t.Fatal(err)
			}
			if result, err := service.ApplyDemote(t.Context(), plan); err == nil || !result.Moved {
				t.Fatalf("pending result = %+v, %v", result, err)
			}
			switch state {
			case "source-only":
				if err := os.Rename(plan.Destination, plan.Source); err != nil {
					t.Fatal(err)
				}
			case "both":
				if err := os.Mkdir(plan.Source, 0700); err != nil {
					t.Fatal(err)
				}
			case "neither", "replaced-destination", "replaced-source":
				if err := os.Rename(plan.Destination, plan.Destination+"-retained"); err != nil {
					t.Fatal(err)
				}
				if state == "replaced-destination" {
					initRepo(t, plan.Destination, true)
				}
				if state == "replaced-source" {
					initRepo(t, plan.Source, true)
				}
			}
			diagnostics, err := f.service.ReconcileMoveIntents(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			entry, err := f.store.Get(plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state == "source-only" {
				if len(diagnostics) > 0 || entry.MoveIntent != nil || entry.Kind != catalog.KindRepository {
					t.Fatalf("source-only reconciliation = %+v, %+v", entry, diagnostics)
				}
			} else if len(diagnostics) == 0 || entry.MoveIntent == nil || entry.Kind != catalog.KindRepository {
				t.Fatalf("ambiguous recovery changed catalog = %+v, %+v", entry, diagnostics)
			}
		})
	}
}

func TestDemoteDoesNotRollBackSubstitutedDestination(t *testing.T) {
	f, graduated := graduatedForDemotion(t)
	hooks := demoteHooks()
	moves := 0
	hooks.Rename = func(source, destination string) error {
		moves++
		if err := os.Rename(source, destination); err != nil {
			return err
		}
		if err := os.Rename(destination, destination+"-retained"); err != nil {
			return err
		}
		initRepo(t, destination, true)
		return nil
	}
	service := serviceWithHooks(t, f, hooks)
	plan, err := service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyDemote(t.Context(), plan)
	if err == nil || !result.Moved || result.RolledBack || moves != 1 {
		t.Fatalf("substitution result = %+v, moves=%d, err=%v", result, moves, err)
	}
	if _, err := os.Stat(plan.Destination + "-retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("substituted destination was moved into source", err)
	}
}

func TestDemoteReconciliationCannotClearActiveMoveIntent(t *testing.T) {
	f, graduated := graduatedForDemotion(t)
	hooks := demoteHooks()
	reconciled := make(chan struct{})
	hooks.Rename = func(source, destination string) error {
		go func() {
			// Ordinary services have no caller-provided demotion guard/lock.
			ordinary := serviceWithHooks(t, f, experiment.Hooks{})
			_, _ = ordinary.ReconcileMoveIntents(t.Context())
			close(reconciled)
		}()
		select {
		case <-reconciled:
			t.Fatal("reconciliation bypassed the active repository move lease")
		case <-time.After(80 * time.Millisecond):
		}
		entry, err := f.store.Get(graduated.Item.ID)
		if err != nil || entry.MoveIntent == nil {
			t.Fatalf("active journal disappeared: %+v, %v", entry, err)
		}
		if _, err := f.registry.Attach(entry.ID, catalog.Observation{Host: "test-host", Path: destination, Name: "racing", CommonDir: filepath.Join(destination, ".git")}); err == nil {
			t.Fatal("registry attachment reassigned an active move")
		}
		return os.Rename(source, destination)
	}
	service := serviceWithHooks(t, f, hooks)
	plan, err := service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: graduated.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyDemote(t.Context(), plan)
	if err != nil || !result.Moved {
		t.Fatalf("move = %+v, %v", result, err)
	}
	select {
	case <-reconciled:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciliation did not finish after move lease released")
	}
	entry, err := f.store.Get(plan.ID)
	if err != nil || entry.MoveIntent != nil || entry.Kind != catalog.KindTry {
		t.Fatalf("catalog after concurrent recovery = %+v, %v", entry, err)
	}
}
