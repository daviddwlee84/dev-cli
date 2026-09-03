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

func TestLifecyclePhaseAndLocationTransitionsAreIndependent(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	source := f.mkdir("2026-08-27-lifecycle")
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(source))
	source = item.Live.CurrentPath
	if _, err := f.registry.Patch(item.ID, func(entry *catalog.Entry) error {
		entry.Tags = []string{"Keep", "prototype"}
		entry.Note = "valuable"
		entry.Locations["other-host"] = catalog.Location{
			State: catalog.LocationPresent, CurrentPath: "/srv/tries/lifecycle",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	deprecated, err := f.service.Deprecate(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if deprecated.Item.Phase != catalog.PhaseDeprecated || !deprecated.Item.Live.Present {
		t.Fatalf("deprecated result = %+v", deprecated.Item)
	}
	if visible, _, err := f.service.List(context.Background(), experiment.ListOptions{}); err != nil || len(visible) != 0 {
		t.Fatalf("default list after deprecation = %+v, %v", visible, err)
	}
	all, _, err := f.service.List(context.Background(), experiment.ListOptions{All: true})
	if err != nil || len(all) != 1 || all[0].Phase != catalog.PhaseDeprecated {
		t.Fatalf("all history after deprecation = %+v, %v", all, err)
	}

	archived, err := f.service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Moved || !strings.Contains(archived.Plan.Destination,
		filepath.Join(".dev", "archive", item.ID, filepath.Base(source))) {
		t.Fatalf("archive result = %+v", archived)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, ok := entry.LocationFor("test-host")
	if !ok || entry.Experiment.Phase != catalog.PhaseDeprecated ||
		location.State != catalog.LocationArchived || location.RestorePath != source || entry.MoveIntent != nil {
		t.Fatalf("archived catalog = %+v", entry)
	}
	if other := entry.Locations["other-host"]; other.CurrentPath != "/srv/tries/lifecycle" || other.State != catalog.LocationPresent {
		t.Errorf("other host location changed: %+v", other)
	}

	reactivated, err := f.service.Reactivate(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.Item.Phase != catalog.PhaseActive || reactivated.Item.Live.Present {
		t.Fatalf("reactivated archived item = %+v", reactivated.Item)
	}
	restored, err := f.service.Restore(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Moved || restored.Item.ID != item.ID || !restored.Item.Live.Present {
		t.Fatalf("restore result = %+v", restored)
	}
	body, err := os.ReadFile(filepath.Join(source, "notes.txt"))
	if err != nil || string(body) != "keep\n" {
		t.Fatalf("restored data = %q, %v", body, err)
	}
	entry, _ = f.store.Get(item.ID)
	location, _ = entry.LocationFor("test-host")
	if entry.Note != "valuable" || strings.Join(entry.Tags, ",") != "keep,prototype" ||
		location.State != catalog.LocationPresent || location.CurrentPath != source || location.RestorePath != "" {
		t.Errorf("restored metadata = %+v", entry)
	}
}

func TestReconcileDoesNotDuplicateGraduatedHistoryAtAnOldHostPath(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	f.mkdir("graduated-history")
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "graduated-history")
	if _, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Kind = catalog.KindRepository
		entry.Experiment.Phase = catalog.PhaseGraduated
		entry.Experiment.GraduatedAt = *f.now
		entry.Experiment.GraduatedPath = item.Live.CurrentPath
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	visible, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(diagnostics) != 0 || len(visible) != 0 {
		t.Fatalf("graduated history leaked into default list: %+v, %+v, %v", visible, diagnostics, err)
	}
	history, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{All: true})
	if err != nil || len(diagnostics) != 0 || len(history) != 1 || history[0].ID != item.ID || history[0].Kind != catalog.KindRepository {
		t.Fatalf("graduated history = %+v, %+v, %v", history, diagnostics, err)
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("graduated path was duplicated: %+v, %v", entries, err)
	}
}

func TestOtherHostMoveIntentDoesNotHideThisHostsPresentTry(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	f.mkdir("multi-host-intent")
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "multi-host-intent")
	if _, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Locations["other-host"] = catalog.Location{
			State: catalog.LocationPresent, CurrentPath: "/srv/tries/multi-host-intent",
		}
		entry.MoveIntent = &catalog.MoveIntent{
			Host: "other-host", Operation: "archive",
			SourcePath:      "/srv/tries/multi-host-intent",
			DestinationPath: "/srv/tries/.dev/archive/asset/multi-host-intent",
			Started:         *f.now,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	listed, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(diagnostics) != 0 || len(listed) != 1 || listed[0].ID != item.ID || !listed[0].Live.Present {
		t.Fatalf("this-host list was blocked by another host intent: %+v, %+v, %v", listed, diagnostics, err)
	}
	entry, _ := f.store.Get(item.ID)
	if entry.MoveIntent == nil || entry.MoveIntent.Host != "other-host" {
		t.Errorf("another host's intent was cleared: %+v", entry.MoveIntent)
	}
}

func TestTouchAndOriginRefreshRefusePendingHostMove(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	path := f.mkdir("pending-activity")
	initRepo(t, path, false)
	runGit(t, path, "remote", "add", "origin", "https://github.com/owner/pending-activity.git")
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "pending-activity")
	destination := filepath.Join(f.tries, ".dev", "archive", item.ID, item.Basename)
	if _, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.MoveIntent = &catalog.MoveIntent{
			Host: "test-host", Operation: "archive", SourcePath: item.Live.CurrentPath,
			DestinationPath: destination, Started: *f.now,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Touch(context.Background(), item.ID); err == nil || !strings.Contains(err.Error(), "pending archive move") {
		t.Fatalf("Touch during pending move = %v", err)
	}
	if _, err := f.service.RefreshOrigin(context.Background(), item.ID); err == nil || !strings.Contains(err.Error(), "pending archive move") {
		t.Fatalf("RefreshOrigin during pending move = %v", err)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.LastOpened.IsZero() || entry.MoveIntent == nil {
		t.Fatalf("pending intent or activity changed: %+v", entry)
	}
}

func TestAttachAddsOneHostWithoutReassigningLiveOrArchivedLocations(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	entry := &catalog.Entry{
		Kind: catalog.KindTry, Name: "portable",
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive, Slug: "portable", Started: *f.now,
			OriginalPath: "/srv/tries/portable",
		},
		Locations: map[string]catalog.Location{
			"other-host": {State: catalog.LocationPresent, CurrentPath: "/srv/tries/portable"},
		},
	}
	if err := f.store.Create(entry); err != nil {
		t.Fatal(err)
	}
	if visible, _, err := f.service.List(context.Background(), experiment.ListOptions{}); err != nil || len(visible) != 0 {
		t.Fatalf("other-host-only asset leaked into default list: %+v, %v", visible, err)
	}
	if history, _, err := f.service.List(context.Background(), experiment.ListOptions{All: true}); err != nil ||
		len(history) != 1 || history[0].ID != entry.ID || history[0].Live.Present {
		t.Fatalf("other-host-only history was hidden: %+v, %v", history, err)
	}
	target := f.mkdir("portable")
	attached, err := f.service.Attach(context.Background(), entry.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != entry.ID || !attached.Live.Present {
		t.Fatalf("attached item = %+v", attached)
	}
	loaded, _ := f.store.Get(entry.ID)
	if len(loaded.Locations) != 2 || loaded.Locations["other-host"].CurrentPath != "/srv/tries/portable" {
		t.Errorf("cross-host attach changed another host: %+v", loaded.Locations)
	}

	second := f.mkdir("second")
	if _, err := f.service.Attach(context.Background(), entry.ID, second); err == nil || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("attach reassigned a live location: %v", err)
	}
	if _, err := f.service.Archive(context.Background(), experiment.TransitionRequest{Ref: entry.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Attach(context.Background(), entry.ID, second); err == nil || !strings.Contains(err.Error(), "use restore") {
		t.Fatalf("attach bypassed archived lifecycle: %v", err)
	}
}

func TestArchiveAndRestoreRejectCollisionsAndEscapes(t *testing.T) {
	t.Run("archive collision", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 1)
		source := f.mkdir("collision")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "collision")
		destination := filepath.Join(f.tries, ".dev", "archive", item.ID, filepath.Base(source))
		if err := os.MkdirAll(destination, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.PlanArchive(context.Background(), experiment.TransitionRequest{Ref: item.ID}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("archive collision = %v", err)
		}
	})

	t.Run("archive symlink escape", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 1)
		f.mkdir("escape")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(f.root, "outside")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(f.tries, ".dev")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := f.service.PlanArchive(context.Background(), experiment.TransitionRequest{Ref: items[0].ID}); err == nil {
			t.Fatal("archive through an escaping .dev symlink succeeded")
		}
	})

	t.Run("restore explicit escape", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 1)
		f.mkdir("restore-escape")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "restore-escape")
		if _, err := f.service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID}); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(f.root, "outside-restore")
		if _, err := f.service.PlanRestore(context.Background(), experiment.TransitionRequest{Ref: item.ID, To: outside}); err == nil {
			t.Fatal("restore outside tries_root succeeded")
		}
	})
}

func TestMoveIntentReconciliationStates(t *testing.T) {
	for _, test := range []struct {
		name        string
		source      bool
		destination bool
		wantState   catalog.LocationState
		wantIntent  bool
		wantDiag    bool
	}{
		{name: "source only rolls back", source: true, wantState: catalog.LocationPresent},
		{name: "destination only finalizes", destination: true, wantState: catalog.LocationArchived},
		{name: "both retains intent", source: true, destination: true, wantState: catalog.LocationPresent, wantIntent: true, wantDiag: true},
		{name: "neither retains intent", wantState: catalog.LocationPresent, wantIntent: true, wantDiag: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, experiment.Hooks{}, 1)
			source := f.mkdir("crash-state")
			items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			item := itemByBase(t, items, "crash-state")
			source = item.Live.CurrentPath
			destination := filepath.Join(filepath.Dir(source), ".dev", "archive", item.ID, filepath.Base(source))
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			if !test.source {
				if err := os.RemoveAll(source); err != nil {
					t.Fatal(err)
				}
			}
			if test.destination {
				if err := os.MkdirAll(destination, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
				entry.MoveIntent = &catalog.MoveIntent{
					Host: "test-host", Operation: "archive", SourcePath: source,
					DestinationPath: destination, Started: *f.now,
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			diagnostics, err := f.service.ReconcileMoveIntents(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := len(diagnostics) > 0; got != test.wantDiag {
				t.Errorf("diagnostics = %+v, want any=%t", diagnostics, test.wantDiag)
			}
			entry, err := f.store.Get(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			location, _ := entry.LocationFor("test-host")
			if location.State != test.wantState || (entry.MoveIntent != nil) != test.wantIntent {
				t.Errorf("reconciled entry = %+v", entry)
			}
		})
	}
}

func TestArchiveMoveFailureClearsIntentAndFinalizeFailureRollsBack(t *testing.T) {
	t.Run("move failure", func(t *testing.T) {
		failure := errors.New("move failed")
		f := newFixture(t, experiment.Hooks{}, 1)
		source := f.mkdir("move-failure")
		items, _, _ := f.service.List(context.Background(), experiment.ListOptions{})
		item := itemByBase(t, items, "move-failure")
		service := serviceWithHooks(t, f, experiment.Hooks{
			Rename: func(string, string) error { return failure },
		})
		result, err := service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID})
		if !errors.Is(err, failure) || result.Moved {
			t.Fatalf("Archive = %+v, %v", result, err)
		}
		entry, _ := f.store.Get(item.ID)
		if entry.MoveIntent != nil {
			t.Errorf("failed move retained intent: %+v", entry.MoveIntent)
		}
		if _, err := os.Stat(source); err != nil {
			t.Errorf("failed move lost source: %v", err)
		}
	})

	t.Run("finalize failure", func(t *testing.T) {
		failure := errors.New("finalize failed")
		f := newFixture(t, experiment.Hooks{}, 1)
		source := f.mkdir("archive-rollback")
		items, _, _ := f.service.List(context.Background(), experiment.ListOptions{})
		item := itemByBase(t, items, "archive-rollback")
		service := serviceWithHooks(t, f, experiment.Hooks{
			CatalogUpdate: func(id string, mutate func(*catalog.Entry) error) (*catalog.Entry, error) {
				current, err := f.store.Get(id)
				if err != nil {
					return nil, err
				}
				candidate := current.Clone()
				if err := mutate(candidate); err != nil {
					return nil, err
				}
				if location, ok := candidate.LocationFor("test-host"); ok && location.State == catalog.LocationArchived {
					return nil, failure
				}
				return f.store.UpdateUnderLock(id, mutate)
			},
		})
		result, err := service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID})
		var finalize *experiment.FinalizeError
		if !errors.Is(err, failure) || !errors.As(err, &finalize) || !result.RolledBack || result.Moved {
			t.Fatalf("Archive = %+v, %v", result, err)
		}
		entry, _ := f.store.Get(item.ID)
		location, _ := entry.LocationFor("test-host")
		if entry.MoveIntent != nil || location.State != catalog.LocationPresent {
			t.Errorf("catalog after rollback = %+v", entry)
		}
		if _, err := os.Stat(source); err != nil {
			t.Errorf("rollback did not restore source: %v", err)
		}
	})
}

func TestSourceOnlyRepairClearsIntentAfterConcurrentLocationChange(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	source := f.mkdir("concurrent-location")
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "concurrent-location")
	other := filepath.Join(f.root, "other-location")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := serviceWithHooks(t, f, experiment.Hooks{
		SameFilesystem: func(string, string) (bool, error) {
			calls++
			if calls == 2 {
				_, updateErr := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
					return entry.SetLocation("test-host", catalog.Location{
						State: catalog.LocationPresent, CurrentPath: other, RealPath: other,
					})
				})
				return true, updateErr
			}
			return true, nil
		},
	})
	if _, err := service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID}); err == nil || !strings.Contains(err.Error(), "reassigned") {
		t.Fatalf("concurrent archive = %v", err)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := entry.LocationFor("test-host")
	if entry.MoveIntent != nil || location.CurrentPath != other {
		t.Errorf("source-only repair clobbered concurrent metadata or retained intent: %+v", entry)
	}
	if _, err := os.Stat(source); err != nil {
		t.Errorf("source-only failed move changed source: %v", err)
	}
}

func TestArchiveRejectsMainCheckoutWithLinkedWorktree(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	main := f.mkdir("shared-main")
	initRepo(t, main, true)
	linked := filepath.Join(f.root, "linked-child")
	if err := gitx.AddWorktree(context.Background(), main, linked, "feat/child", "main"); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "shared-main")
	if _, err := f.service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID}); err == nil || !strings.Contains(err.Error(), "main checkout") {
		t.Fatalf("archive shared main = %v", err)
	}
	if _, err := os.Stat(main); err != nil {
		t.Errorf("rejected main checkout moved: %v", err)
	}
	if _, err := os.Stat(linked); err != nil {
		t.Errorf("rejected linked checkout changed: %v", err)
	}
}

func TestLinkedWorktreeArchiveRestoreAndGraduatePreserveSharedGit(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	main := filepath.Join(f.root, "main-repo")
	initRepo(t, main, true)
	linked := filepath.Join(f.tries, "2026-08-27-linked")
	if err := gitx.AddWorktree(context.Background(), main, linked, "feat/linked", "main"); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(linked))
	linked = item.Live.CurrentPath
	commonBefore := item.Live.Repo.GitCommonDir

	archived, err := f.service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Plan.LinkedWorktree {
		t.Fatal("linked worktree was not recognized")
	}
	worktree, ok, err := gitx.WorktreeFor(context.Background(), main, "feat/linked")
	if err != nil || !ok || worktree.Path != archived.Plan.Destination {
		t.Fatalf("archived Git worktree = %+v, %t, %v", worktree, ok, err)
	}
	restored, err := f.service.Restore(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Plan.Destination != linked {
		t.Errorf("restored destination = %s, want %s", restored.Plan.Destination, linked)
	}
	discovered, err := gitx.Discover(context.Background(), linked)
	if err != nil || !discovered.IsLinkedWorktree || discovered.GitCommonDir != commonBefore {
		t.Fatalf("restored linked worktree = %+v, %v", discovered, err)
	}
	if !gitx.BranchExists(context.Background(), main, "feat/linked") {
		t.Fatal("worktree branch was lost")
	}

	graduated, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !graduated.Plan.LinkedWorktree || !graduated.Moved {
		t.Fatalf("graduated linked result = %+v", graduated)
	}
	discovered, err = gitx.Discover(context.Background(), graduated.Plan.Destination)
	if err != nil || !discovered.IsLinkedWorktree || discovered.GitCommonDir != commonBefore {
		t.Fatalf("graduated linked worktree = %+v, %v", discovered, err)
	}
	if _, err := os.Stat(main); err != nil {
		t.Errorf("main checkout was changed: %v", err)
	}
}

func TestGraduateArchivedTryPreservesIdentityAndMetadata(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	source := f.mkdir("2026-08-27-archived-project")
	if err := os.WriteFile(filepath.Join(source, "prototype.txt"), []byte("valuable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, _ := f.service.List(context.Background(), experiment.ListOptions{})
	item := itemByBase(t, items, filepath.Base(source))
	if _, err := f.registry.Patch(item.ID, func(entry *catalog.Entry) error {
		entry.Tags = []string{"keep"}
		entry.Note = "ship it"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	archived, err := f.service.Archive(context.Background(), experiment.TransitionRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	graduated, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID, Category: "Labs"})
	if err != nil {
		t.Fatal(err)
	}
	if graduated.Item.ID != item.ID || !graduated.Moved || !graduated.GitInitialized || !graduated.InitialCommitMade {
		t.Fatalf("graduated archived result = %+v", graduated)
	}
	if _, err := os.Stat(archived.Plan.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("archive source remained: %v", err)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != catalog.KindRepository || entry.Experiment.Phase != catalog.PhaseGraduated ||
		entry.Note != "ship it" || !entry.HasTag("keep") || entry.MoveIntent != nil {
		t.Errorf("graduated catalog = %+v", entry)
	}
}
