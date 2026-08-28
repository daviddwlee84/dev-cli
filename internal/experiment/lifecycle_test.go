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
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

func serviceWithHooks(t *testing.T, f *fixture, hooks experiment.Hooks) *experiment.Service {
	t.Helper()
	service, err := experiment.NewService(experiment.ServiceConfig{
		Registry: f.registry, Store: f.store,
		TriesRoot: f.tries, ProjectRoot: f.projects, Host: "test-host",
		Clock: func() time.Time { return *f.now }, MaxEnrichment: 2,
		Hooks: hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestGraduatePreservesStableIDHistoryTagsAndDirtyFiles(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	source := f.mkdir("2026-08-20-history-demo")
	initRepo(t, source, true)
	runGit(t, source, "tag", "experiment-v1")
	if err := os.WriteFile(filepath.Join(source, "untracked.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("dirty tracked work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	headBefore := runGit(t, source, "rev-parse", "HEAD")

	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(source))
	lastOpened := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	updated, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Tags = []string{"keep", "Important"}
		entry.Note = "promising"
		entry.LastOpened = lastOpened
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	originalPath := updated.Experiment.OriginalPath

	result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{
		Ref: item.ID, Category: "Infra",
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(f.projects, "Infra", "history-demo")
	canonicalDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Moved || result.GitInitialized || result.InitialCommitMade {
		t.Errorf("Graduate result = %+v", result)
	}
	if result.Item.ID != item.ID || result.Item.Live.CurrentPath != canonicalDestination {
		t.Errorf("graduated Item = %+v", result.Item)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("source still exists: %v", err)
	}
	if got := runGit(t, destination, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD changed from %s to %s", headBefore, got)
	}
	if got := runGit(t, destination, "tag", "--list", "experiment-v1"); got != "experiment-v1" {
		t.Errorf("tag was not preserved: %q", got)
	}
	status := runGit(t, destination, "status", "--porcelain")
	if !strings.Contains(status, "M README.md") || !strings.Contains(status, "?? untracked.txt") {
		t.Errorf("dirty work was not preserved: %q", status)
	}

	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != catalog.KindRepository || entry.Experiment.Phase != catalog.PhaseGraduated {
		t.Errorf("catalog lifecycle = %s / %s", entry.Kind, entry.Experiment.Phase)
	}
	if entry.ID != item.ID || entry.Note != "promising" || strings.Join(entry.Tags, ",") != "important,keep" {
		t.Errorf("catalog identity/metadata changed: %+v", entry)
	}
	if entry.Experiment.OriginalPath != originalPath || entry.Experiment.GraduatedPath != canonicalDestination ||
		!entry.LastOpened.Equal(lastOpened) {
		t.Errorf("catalog provenance = %+v", entry.Experiment)
	}
	location, ok := entry.LocationFor("test-host")
	if !ok || location.State != catalog.LocationPresent || location.CurrentPath != canonicalDestination {
		t.Errorf("graduated location = %+v, %v", location, ok)
	}
	if _, _, err := f.service.Resolve(context.Background(), "history-demo"); !errors.Is(err, experiment.ErrNotFound) {
		t.Errorf("graduated Try remained in legacy resolution: %v", err)
	}
}

func TestGraduateNonGitTryCommitsBeforeMoving(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	source := f.mkdir("2026-08-20-plain-project")
	if err := os.WriteFile(filepath.Join(source, "prototype.txt"), []byte("valuable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(source))

	result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(f.projects, "plain-project")
	if !result.GitInitialized || !result.InitialCommitMade || !result.Moved {
		t.Errorf("Graduate result = %+v", result)
	}
	if _, _, err := gitx.LastCommit(context.Background(), destination); err != nil {
		t.Errorf("graduated non-Git Try has no history: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(destination, "prototype.txt"))
	if err != nil || string(body) != "valuable\n" {
		t.Errorf("prototype contents = %q, %v", body, err)
	}
}

func TestGraduateEmptyTrySeedsREADME(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	f.mkdir("empty-project")
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "empty-project")
	result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(result.Plan.Destination, "README.md"))
	if err != nil || string(body) != "# empty-project\n" {
		t.Errorf("seed README = %q, %v", body, err)
	}
}

func TestGraduateIgnoredOnlyTryCreatesEmptyInitialCommit(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	ignoreFile := filepath.Join(f.root, "global-ignore")
	if err := os.WriteFile(ignoreFile, []byte(".DS_Store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(f.root, "global.gitconfig")
	configBody := "[core]\n\texcludesFile = " + filepath.ToSlash(ignoreFile) + "\n"
	if err := os.WriteFile(globalConfig, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	source := f.mkdir("ignored-only")
	if err := os.WriteFile(filepath.Join(source, ".DS_Store"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "ignored-only")
	result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.GitInitialized || !result.InitialCommitMade || !result.Moved {
		t.Errorf("Graduate result = %+v", result)
	}
	if _, _, err := gitx.LastCommit(context.Background(), result.Plan.Destination); err != nil {
		t.Errorf("ignored-only Try has no initial commit: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(result.Plan.Destination, ".DS_Store"))
	if err != nil || string(body) != "keep me\n" {
		t.Errorf("ignored file after graduation = %q, %v", body, err)
	}
	if tracked := runGit(t, result.Plan.Destination, "ls-files"); tracked != "" {
		t.Errorf("ignored user files were force-added: %q", tracked)
	}
}

func TestGraduateGitPreparationFailuresLeaveSourceInPlace(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		initFailure := errors.New("injected init failure")
		f := newFixture(t, experiment.Hooks{
			GitRun: func(ctx context.Context, directory string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "init" {
					return "", initFailure
				}
				return gitx.Run(ctx, directory, args...)
			},
		}, 2)
		source := f.mkdir("init-failure")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "init-failure")
		result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
		if !errors.Is(err, initFailure) || result.Moved {
			t.Fatalf("Graduate = %+v, %v", result, err)
		}
		if info, statErr := os.Stat(source); statErr != nil || !info.IsDir() {
			t.Errorf("source moved after init failure: %v", statErr)
		}
	})

	t.Run("commit", func(t *testing.T) {
		commitFailure := errors.New("injected commit failure")
		f := newFixture(t, experiment.Hooks{
			GitRun: func(ctx context.Context, directory string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "commit" {
					return "", commitFailure
				}
				return gitx.Run(ctx, directory, args...)
			},
		}, 2)
		source := f.mkdir("commit-failure")
		if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "commit-failure")
		result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
		if !errors.Is(err, commitFailure) || result.Moved || !result.GitInitialized {
			t.Fatalf("Graduate = %+v, %v", result, err)
		}
		if body, readErr := os.ReadFile(filepath.Join(source, "data.txt")); readErr != nil || string(body) != "keep\n" {
			t.Errorf("source data after commit failure = %q, %v", body, readErr)
		}
	})
}

func TestGraduateRejectsUnsafeComponentsCollisionAndCrossFilesystem(t *testing.T) {
	t.Run("components", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 2)
		source := f.mkdir("safe")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "safe")
		for _, request := range []experiment.GraduateRequest{
			{Ref: item.ID, Name: "../escape"},
			{Ref: item.ID, Category: `two/parts`},
			{Ref: item.ID, Name: `two\\parts`},
		} {
			if _, err := f.service.PlanGraduate(context.Background(), request); !errors.Is(err, pathx.ErrInvalidComponent) {
				t.Errorf("PlanGraduate(%+v) = %v", request, err)
			}
		}
		if _, err := os.Stat(source); err != nil {
			t.Errorf("unsafe plan changed source: %v", err)
		}
	})

	t.Run("collision", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 2)
		f.mkdir("collision")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "collision")
		destination := filepath.Join(f.projects, "collision")
		if err := os.MkdirAll(destination, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.PlanGraduate(context.Background(), experiment.GraduateRequest{Ref: item.ID}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("collision plan = %v", err)
		}
	})

	t.Run("dangling destination symlink", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 2)
		f.mkdir("reserved-name")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "reserved-name")
		destination := filepath.Join(f.projects, "reserved-name")
		target := filepath.Join(f.projects, "future-target")
		if err := os.Symlink(target, destination); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := f.service.PlanGraduate(context.Background(), experiment.GraduateRequest{Ref: item.ID}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("dangling-symlink collision plan = %v", err)
		}
		if _, err := os.Lstat(destination); err != nil {
			t.Errorf("destination symlink changed: %v", err)
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("dangling target was created: %v", err)
		}
	})

	t.Run("cross filesystem", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{
			SameFilesystem: func(string, string) (bool, error) { return false, nil },
		}, 2)
		source := f.mkdir("cross-device")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "cross-device")
		if _, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID}); !errors.Is(err, experiment.ErrCrossFilesystem) {
			t.Errorf("cross-filesystem Graduate = %v", err)
		}
		if _, err := os.Stat(filepath.Join(source, ".git")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cross-filesystem refusal modified source: %v", err)
		}
	})
}

func TestGraduateDryRunAndCurrentDirectoryResolutionDoNotMutate(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	source := f.mkdir("2026-08-20-from-cwd")
	nested := filepath.Join(source, "deep", "inside")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{
		CurrentDir: nested, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	canonicalProjects, err := filepath.EvalSymlinks(f.projects)
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved || result.Plan.Source != canonicalSource ||
		result.Plan.Destination != filepath.Join(canonicalProjects, "from-cwd") {
		t.Errorf("dry-run result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(source, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run initialized Git: %v", err)
	}
	if _, err := os.Stat(result.Plan.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run created destination: %v", err)
	}
}

func TestRefreshOriginPersistsRemoteAddedAfterGraduation(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	f.mkdir("remote-project")
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "remote-project")
	result, err := f.service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	remote := "git@github.com:owner/remote-project.git"
	runGit(t, result.Plan.Destination, "remote", "add", "origin", remote)
	refreshed, err := f.service.RefreshOrigin(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.OriginURL != remote || refreshed.RemoteIdentity != "github.com/owner/remote-project" {
		t.Errorf("refreshed origin = %q / %q", refreshed.OriginURL, refreshed.RemoteIdentity)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil || entry.Experiment.OriginURL != remote || entry.RemoteIdentity != "github.com/owner/remote-project" {
		t.Errorf("persisted origin = %+v, %v", entry, err)
	}
}

func TestGraduateRevalidatesDestinationAndCatalogSourceBeforeMove(t *testing.T) {
	t.Run("destination symlink swap", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 2)
		source := f.mkdir("destination-race")
		if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "destination-race")
		outside := filepath.Join(f.root, "outside")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		probeLink := filepath.Join(f.root, "symlink-probe")
		if err := os.Symlink(outside, probeLink); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.Remove(probeLink); err != nil {
			t.Fatal(err)
		}
		calls := 0
		service := serviceWithHooks(t, f, experiment.Hooks{
			SameFilesystem: func(string, string) (bool, error) {
				calls++
				if calls == 2 {
					category := filepath.Join(f.projects, "Safe")
					if err := os.Remove(category); err != nil {
						return false, err
					}
					if err := os.Symlink(outside, category); err != nil {
						return false, err
					}
				}
				return true, nil
			},
		})
		result, err := service.Graduate(context.Background(), experiment.GraduateRequest{
			Ref: item.ID, Category: "Safe",
		})
		if err == nil || result.Moved {
			t.Fatalf("Graduate = %+v, %v", result, err)
		}
		if body, readErr := os.ReadFile(filepath.Join(source, "data.txt")); readErr != nil || string(body) != "keep\n" {
			t.Errorf("source after destination race = %q, %v", body, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(outside, "destination-race")); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("Try escaped project root: %v", statErr)
		}
	})

	t.Run("catalog reassignment", func(t *testing.T) {
		f := newFixture(t, experiment.Hooks{}, 2)
		source := f.mkdir("catalog-race")
		items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := itemByBase(t, items, "catalog-race")
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
					if updateErr != nil {
						return false, updateErr
					}
				}
				return true, nil
			},
		})
		result, err := service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
		if err == nil || result.Moved || !strings.Contains(err.Error(), "reassigned") {
			t.Fatalf("Graduate = %+v, %v", result, err)
		}
		if _, statErr := os.Stat(source); statErr != nil {
			t.Errorf("stale catalog plan moved source: %v", statErr)
		}
	})
}

func TestGraduateCatalogFinalizeFailureRollsMoveBack(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	source := f.mkdir("rollback")
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "rollback")
	finalizeFailure := errors.New("injected catalog finalize failure")
	service := serviceWithHooks(t, f, experiment.Hooks{
		CatalogUpdate: func(id string, mutate func(*catalog.Entry) error) (*catalog.Entry, error) {
			current, getErr := f.store.Get(id)
			if getErr != nil {
				return nil, getErr
			}
			candidate := current.Clone()
			if mutateErr := mutate(candidate); mutateErr != nil {
				return nil, mutateErr
			}
			if candidate.Kind == catalog.KindRepository {
				return nil, finalizeFailure
			}
			return f.registry.Update(id, mutate)
		},
	})

	result, err := service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	var finalize *experiment.FinalizeError
	if !errors.Is(err, finalizeFailure) || !errors.As(err, &finalize) {
		t.Fatalf("Graduate error = %v", err)
	}
	if result.Moved || !result.RolledBack || result.RollbackError != nil {
		t.Errorf("rollback result = %+v", result)
	}
	if body, readErr := os.ReadFile(filepath.Join(source, "data.txt")); readErr != nil || string(body) != "keep\n" {
		t.Errorf("rolled-back source = %q, %v", body, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(f.projects, "rollback")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("destination remained after rollback: %v", statErr)
	}
	entry, getErr := f.store.Get(item.ID)
	if getErr != nil || entry.Kind != catalog.KindTry || entry.Experiment.Phase != catalog.PhaseActive {
		t.Errorf("catalog changed despite rollback: %+v, %v", entry, getErr)
	}
}

func TestGraduateReportsRollbackFailureWithoutDeletingDestination(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	source := f.mkdir("rollback-fails")
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "rollback-fails")
	finalizeFailure := errors.New("finalize failed")
	rollbackFailure := errors.New("rollback failed")
	renameCalls := 0
	service := serviceWithHooks(t, f, experiment.Hooks{
		Rename: func(from, to string) error {
			renameCalls++
			if renameCalls == 2 {
				return rollbackFailure
			}
			return os.Rename(from, to)
		},
		CatalogUpdate: func(id string, mutate func(*catalog.Entry) error) (*catalog.Entry, error) {
			current, getErr := f.store.Get(id)
			if getErr != nil {
				return nil, getErr
			}
			candidate := current.Clone()
			if mutateErr := mutate(candidate); mutateErr != nil {
				return nil, mutateErr
			}
			if candidate.Kind == catalog.KindRepository {
				return nil, finalizeFailure
			}
			return f.registry.Update(id, mutate)
		},
	})

	result, err := service.Graduate(context.Background(), experiment.GraduateRequest{Ref: item.ID})
	if !errors.Is(err, finalizeFailure) || !errors.Is(err, rollbackFailure) ||
		!errors.Is(result.RollbackError, rollbackFailure) || !result.Moved || result.RolledBack {
		t.Fatalf("Graduate = %+v, %v", result, err)
	}
	destination := filepath.Join(f.projects, "rollback-fails")
	if body, readErr := os.ReadFile(filepath.Join(destination, "data.txt")); readErr != nil || string(body) != "keep\n" {
		t.Errorf("destination data after failed rollback = %q, %v", body, readErr)
	}
	if _, statErr := os.Stat(source); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("source unexpectedly recreated after failed rollback: %v", statErr)
	}
}
