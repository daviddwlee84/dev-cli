package experiment_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func removalHooks() experiment.Hooks {
	return experiment.Hooks{RemovalGuard: func(context.Context, string) (string, error) { return "test:unoccupied", nil }, TrashAvailable: func() error { return nil }}
}

func removalItem(t *testing.T, f *fixture) experiment.Item {
	t.Helper()
	path := f.mkdir("disposable")
	if err := os.WriteFile(filepath.Join(path, "notes.txt"), []byte("keep these bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return itemByBase(t, items, "disposable")
}

func TestRemovalTrashAndManualRestoreRetainIdentity(t *testing.T) {
	hooks := removalHooks()
	trash := filepath.Join(t.TempDir(), "fake-trash")
	hooks.Trash = func(_ context.Context, path string) error { return os.Rename(path, trash) }
	f := newFixture(t, hooks, 2)
	item := removalItem(t, f)
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ApplyRemoval(t.Context(), plan)
	if err != nil || result.Outcome != "removed" {
		t.Fatalf("result %+v: %v", result, err)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := entry.LocationFor("test-host")
	if location.State != catalog.LocationEvicted || location.RemovalMethod != "trash" || entry.RecoveryReceipt != nil {
		t.Fatalf("wrong retained state: %+v", entry)
	}
	if err := os.Rename(trash, item.Live.CurrentPath); err != nil {
		t.Fatal(err)
	}
	// A dashboard refresh after OS restore must not create a new identity.
	if _, _, err := f.service.List(t.Context(), experiment.ListOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	restored, err := f.service.RestoreRemoved(t.Context(), item.ID, item.Live.CurrentPath)
	if err != nil || restored.ID != item.ID {
		t.Fatalf("restore %+v: %v", restored, err)
	}
	body, err := os.ReadFile(filepath.Join(restored.Live.CurrentPath, "notes.txt"))
	if err != nil || string(body) != "keep these bytes" {
		t.Fatalf("bytes=%q: %v", body, err)
	}
}

func TestRemovalRejectsChangedPlanAndNewContents(t *testing.T) {
	for _, change := range []string{"public plan", "new file", "catalog"} {
		t.Run(change, func(t *testing.T) {
			hooks := removalHooks()
			calls := 0
			hooks.Trash = func(context.Context, string) error { calls++; return nil }
			f := newFixture(t, hooks, 2)
			item := removalItem(t, f)
			plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "public plan":
				plan.Source = t.TempDir()
			case "new file":
				if err := os.WriteFile(filepath.Join(item.Live.CurrentPath, "new"), []byte("unsaved"), 0600); err != nil {
					t.Fatal(err)
				}
			case "catalog":
				if _, err := f.registry.Patch(item.ID, func(e *catalog.Entry) error { e.Note = "changed"; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.ApplyRemoval(t.Context(), plan); err == nil || calls != 0 {
				t.Fatalf("stale plan applied, calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRemovalFailureIsNeverRetriedOrMarkedRemoved(t *testing.T) {
	hooks := removalHooks()
	calls := 0
	hooks.Trash = func(context.Context, string) error { calls++; return errors.New("interrupted") }
	f := newFixture(t, hooks, 2)
	item := removalItem(t, f)
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ApplyRemoval(t.Context(), plan)
	if err == nil || result.Outcome != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
	_, diagnostics, err := f.service.List(t.Context(), experiment.ListOptions{All: true})
	if err != nil || len(diagnostics) == 0 || calls != 1 {
		t.Fatalf("diagnostics=%v calls=%d err=%v", diagnostics, calls, err)
	}
	entry, _ := f.store.Get(item.ID)
	location, _ := entry.LocationFor("test-host")
	if entry.MoveIntent == nil || location.State == catalog.LocationEvicted {
		t.Fatal("indeterminate operation lost its intent")
	}
}

func TestPermanentRemovalKeepsCatalogAndRefusesReplacementRecovery(t *testing.T) {
	f := newFixture(t, removalHooks(), 2)
	item := removalItem(t, f)
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID, Permanent: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyRemoval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(item.Live.CurrentPath); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if err := os.Mkdir(item.Live.CurrentPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RestoreRemoved(t.Context(), item.ID, item.Live.CurrentPath); err == nil {
		t.Fatal("permanent deletion claimed recovery")
	}
}

func TestRemovalRejectsUnobservedOccupationAndNestedGit(t *testing.T) {
	for _, kind := range []string{"guard", "nested", "shared"} {
		t.Run(kind, func(t *testing.T) {
			hooks := removalHooks()
			if kind == "guard" {
				hooks.RemovalGuard = func(context.Context, string) (string, error) { return "", errors.New("occupied") }
			}
			f := newFixture(t, hooks, 2)
			item := removalItem(t, f)
			if kind == "nested" {
				if err := os.MkdirAll(filepath.Join(item.Live.CurrentPath, "nested", ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "shared" {
				if _, err := gitx.Run(t.Context(), item.Live.CurrentPath, "init"); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(item.Live.CurrentPath, ".git", "objects", "info", "alternates")
				if err := os.WriteFile(path, []byte(t.TempDir()), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID, Permanent: true}); err == nil {
				t.Fatalf("%s was not blocked", kind)
			}
		})
	}
}

func TestTrashUnavailableDoesNotBecomePermanent(t *testing.T) {
	hooks := removalHooks()
	hooks.TrashAvailable = func() error { return errors.New("no trash") }
	f := newFixture(t, hooks, 2)
	item := removalItem(t, f)
	if _, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID}); err == nil || !strings.Contains(err.Error(), "no trash") {
		t.Fatal(err)
	}
	if _, err := os.Stat(item.Live.CurrentPath); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalArchivedTrashRestoresArchivedLocation(t *testing.T) {
	hooks := removalHooks()
	trash := filepath.Join(t.TempDir(), "fake-trash")
	hooks.Trash = func(_ context.Context, path string) error { return os.Rename(path, trash) }
	f := newFixture(t, hooks, 2)
	item := removalItem(t, f)
	archived, err := f.service.Archive(t.Context(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyRemoval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(trash, archived.Plan.Destination); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.service.RestoreRemoved(t.Context(), item.ID, archived.Plan.Destination)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := recovered.Entry.LocationFor("test-host")
	if location.State != catalog.LocationArchived || location.RestorePath != item.Live.CurrentPath {
		t.Fatalf("%+v", location)
	}
	if _, err := f.service.Restore(t.Context(), experiment.TransitionRequest{Ref: item.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalUnknownTrashCanReassociateUnchangedOriginal(t *testing.T) {
	hooks := removalHooks()
	calls := 0
	hooks.Trash = func(context.Context, string) error { calls++; return errors.New("failed before move") }
	f := newFixture(t, hooks, 2)
	item := removalItem(t, f)
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyRemoval(t.Context(), plan); err == nil {
		t.Fatal("failure missing")
	}
	recovered, err := f.service.RestoreRemoved(t.Context(), item.ID, item.Live.CurrentPath)
	if err != nil || recovered.Entry.MoveIntent != nil || calls != 1 {
		t.Fatalf("%+v calls=%d err=%v", recovered, calls, err)
	}
}

func TestRemovalIndependentGitIncludesItsLocalStorage(t *testing.T) {
	hooks := removalHooks()
	trash := filepath.Join(t.TempDir(), "fake-trash")
	hooks.Trash = func(_ context.Context, path string) error { return os.Rename(path, trash) }
	f := newFixture(t, hooks, 2)
	item := removalItem(t, f)
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"add", "notes.txt"}, {"commit", "-m", "initial"}, {"tag", "local-only"}} {
		if _, err := gitx.Run(t.Context(), item.Live.CurrentPath, args...); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyRemoval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(trash, ".git", "refs", "tags", "local-only")); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalRejectsNestedBareRepository(t *testing.T) {
	f := newFixture(t, removalHooks(), 2)
	item := removalItem(t, f)
	if _, err := gitx.Run(t.Context(), item.Live.CurrentPath, "init", "--bare", "nested.git"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID, Permanent: true}); err == nil || !strings.Contains(err.Error(), "nested bare") {
		t.Fatal(err)
	}
}

func TestRemovalRejectsSymlinkReplacementWithoutTouchingTarget(t *testing.T) {
	f := newFixture(t, removalHooks(), 2)
	item := removalItem(t, f)
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID, Permanent: true})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "untouched"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(item.Live.CurrentPath, item.Live.CurrentPath+"-kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, item.Live.CurrentPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := f.service.ApplyRemoval(t.Context(), plan); err == nil {
		t.Fatal("symlink replacement accepted")
	}
	if body, err := os.ReadFile(filepath.Join(outside, "untouched")); err != nil || string(body) != "outside" {
		t.Fatalf("outside changed: %q %v", body, err)
	}
}
