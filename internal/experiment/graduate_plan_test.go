package experiment_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func reviewedGraduateFixture(t *testing.T, git, committed bool) (*fixture, experiment.Item) {
	t.Helper()
	f := newFixture(t, experiment.Hooks{}, 1)
	source := f.mkdir("2026-09-24-reviewed")
	if git {
		initRepo(t, source, committed)
	}
	if err := os.WriteFile(filepath.Join(source, "product.txt"), []byte("alpha"), 0600); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f, itemByBase(t, items, filepath.Base(source))
}

func TestGraduateLegacyPreviewAndCanceledPlanDoNotEnroll(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	source := f.mkdir("2026-09-24-legacy")
	for _, dryRun := range []bool{false, true} {
		item, _, err := f.service.ResolveGraduate(t.Context(), experiment.GraduateRequest{Ref: source})
		if err != nil || item.ID != "" {
			t.Fatalf("readonly selection: %+v, %v", item, err)
		}
		plan, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: source, Name: "Jev", DryRun: dryRun})
		if err != nil || !plan.Register || plan.Item.ID != "" {
			t.Fatalf("legacy plan: %+v, %v", plan, err)
		}
		if dryRun {
			result, err := f.service.ApplyGraduate(t.Context(), plan)
			if err != nil || result.Registered || result.Moved {
				t.Fatalf("dry apply: %+v, %v", result, err)
			}
		}
		if _, err := os.Stat(f.store.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preview created catalog state: %v", err)
		}
		if _, err := os.Stat(filepath.Join(source, ".git")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preview initialized Git: %v", err)
		}
	}
	plan, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: source, Name: "Jev"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ApplyGraduate(t.Context(), plan)
	if err != nil || !result.Registered || !result.Moved || result.Item.ID == "" {
		t.Fatalf("legacy apply: %+v, %v", result, err)
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 1 || entries[0].ID != result.Item.ID {
		t.Fatalf("registered entries: %+v, %v", entries, err)
	}
}

func TestGraduateConcurrentLegacyPlansHaveOneRegistration(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	source := f.mkdir("2026-09-24-concurrent")
	first, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: source})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: source})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	for _, plan := range []experiment.GraduatePlan{first, second} {
		wait.Add(1)
		go func(plan experiment.GraduatePlan) {
			defer wait.Done()
			result, err := f.service.ApplyGraduate(ctx, plan)
			results <- err == nil && result.Moved
		}(plan)
	}
	wait.Wait()
	close(results)
	successes := 0
	for success := range results {
		if success {
			successes++
		}
	}
	entries, err := f.store.List()
	if successes != 1 || err != nil || len(entries) != 1 {
		t.Fatalf("successes=%d entries=%+v err=%v", successes, entries, err)
	}
}

func TestGraduateReviewedPlanRejectsChangedInputsWithoutPreparation(t *testing.T) {
	for _, change := range []string{"contents", "restored-mtime", "catalog", "destination", "parent", "plan", "host", "source"} {
		t.Run(change, func(t *testing.T) {
			f, item := reviewedGraduateFixture(t, false, false)
			plan, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: item.ID, Name: "Jev"})
			if err != nil {
				t.Fatal(err)
			}
			service := f.service
			switch change {
			case "contents":
				if err := os.WriteFile(filepath.Join(plan.Source, "ignored.data"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			case "restored-mtime":
				file := filepath.Join(plan.Source, "product.txt")
				before, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("omega"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "catalog":
				if _, err := f.registry.Update(item.ID, func(e *catalog.Entry) error { e.Note = "changed"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "destination":
				if err := os.Mkdir(plan.Destination, 0700); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.Rename(f.projects, f.projects+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.projects, 0700); err != nil {
					t.Fatal(err)
				}
			case "plan":
				plan.Name = "modified"
			case "host":
				service, err = experiment.NewService(experiment.ServiceConfig{Registry: f.registry, Store: f.store, TriesRoot: f.tries, ProjectRoot: f.projects, Host: "other-host"})
				if err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := os.Rename(plan.Source, plan.Source+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(plan.Source, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.ApplyGraduate(t.Context(), plan)
			if err == nil || result.Moved || result.GitInitialized || result.InitialCommitMade {
				t.Fatalf("stale apply: %+v, %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(plan.Source, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale plan prepared Git: %v", err)
			}
		})
	}
}

func TestGraduateCommittedSourceDoesNotAdoptLateGitAuthority(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, true)
	applying, calls := false, 0
	service := serviceWithHooks(t, f, experiment.Hooks{GitDiscover: func(ctx context.Context, path string) (gitx.Repo, error) {
		if applying {
			calls++
			if calls == 2 {
				if _, err := gitx.Run(ctx, item.CurrentPath(), "branch", "late-unreviewed", "HEAD"); err != nil {
					return gitx.Repo{}, err
				}
			}
		}
		return gitx.Discover(ctx, path)
	}})
	plan, err := service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	applying = true
	result, err := service.ApplyGraduate(t.Context(), plan)
	if err == nil || result.Moved {
		t.Fatalf("late refs accepted: %+v, %v", result, err)
	}
	if _, err := os.Stat(plan.Source); err != nil {
		t.Fatal(err)
	}
}

func TestGraduatePreparationRejectsReplacedGitAndUnrelatedRefs(t *testing.T) {
	for _, change := range []string{"git-directory", "other-ref", "product-during-init"} {
		t.Run(change, func(t *testing.T) {
			f, item := reviewedGraduateFixture(t, change != "product-during-init", false)
			service := serviceWithHooks(t, f, experiment.Hooks{GitRun: func(ctx context.Context, path string, args ...string) (string, error) {
				out, err := gitx.Run(ctx, path, args...)
				if err != nil {
					return out, err
				}
				if len(args) > 0 && args[0] == "init" && change == "product-during-init" {
					err = os.WriteFile(filepath.Join(path, "product.txt"), []byte("changed externally"), 0600)
				}
				if len(args) > 0 && args[0] == "commit" {
					if change == "other-ref" {
						_, err = gitx.Run(ctx, path, "branch", "unreviewed", "HEAD")
					}
					if change == "git-directory" {
						if err = os.Rename(filepath.Join(path, ".git"), filepath.Join(f.root, "retained-git")); err == nil {
							for _, command := range [][]string{{"init", "-b", "main"}, {"add", "--all"}, {"commit", "-m", "replacement"}} {
								if _, err = gitx.Run(ctx, path, command...); err != nil {
									break
								}
							}
						}
					}
				}
				return out, err
			}})
			plan, err := service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.ApplyGraduate(t.Context(), plan)
			if err == nil || result.Moved {
				t.Fatalf("unexpected prep adopted: %+v, %v", result, err)
			}
			if _, err := os.Stat(plan.Source); err != nil {
				t.Fatal(err)
			}
			if change == "product-during-init" && (!result.GitInitialized || result.InitialCommitMade) {
				t.Fatalf("wrong retained effects: %+v", result)
			}
		})
	}
}

func TestGraduateRejectsInitializedGitReplacementBeforeLease(t *testing.T) {
	f, item := reviewedGraduateFixture(t, false, false)
	initialized, replaced := false, false
	service := serviceWithHooks(t, f, experiment.Hooks{
		GitRun: func(ctx context.Context, path string, args ...string) (string, error) {
			out, err := gitx.Run(ctx, path, args...)
			if err == nil && len(args) > 0 && args[0] == "init" {
				initialized = true
			}
			return out, err
		},
		GitDiscover: func(ctx context.Context, path string) (gitx.Repo, error) {
			if initialized && !replaced {
				replaced = true
				if err := os.Rename(filepath.Join(path, ".git"), filepath.Join(f.root, "retained-initialized-git")); err != nil {
					return gitx.Repo{}, err
				}
				if _, err := gitx.Run(ctx, path, "init", "-b", "main"); err != nil {
					return gitx.Repo{}, err
				}
			}
			return gitx.Discover(ctx, path)
		},
	})
	result, err := service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err == nil || !strings.Contains(err.Error(), "initialized Git directory changed") || result.Moved || !result.GitInitialized || result.InitialCommitMade || !replaced {
		t.Fatalf("initialized Git replacement: %+v, %v", result, err)
	}
	if _, err := os.Stat(item.CurrentPath()); err != nil {
		t.Fatal(err)
	}
}

func TestGraduateRejectsExternallyCreatedInitialCommit(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, false)
	applying, checks := false, 0
	service := serviceWithHooks(t, f, experiment.Hooks{GitLastCommit: func(ctx context.Context, path string) (int64, string, error) {
		if applying {
			checks++
			if checks == 2 {
				for _, command := range [][]string{{"add", "--all"}, {"commit", "-m", "external initial commit"}} {
					if _, err := gitx.Run(ctx, path, command...); err != nil {
						return 0, "", err
					}
				}
			}
		}
		return gitx.LastCommit(ctx, path)
	}})
	plan, err := service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	applying = true
	result, err := service.ApplyGraduate(t.Context(), plan)
	if err == nil || !strings.Contains(err.Error(), "initial commit appeared outside") || result.Moved || result.InitialCommitMade {
		t.Fatalf("external initial commit adopted: %+v, %v", result, err)
	}
	if got := runGit(t, plan.Source, "log", "-1", "--format=%s"); got != "external initial commit" {
		t.Fatalf("external source commit = %q", got)
	}
}

func TestGraduatePublicationAuthorityIsCapturedUnderMoveLease(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, true)
	checked := false
	service := serviceWithHooks(t, f, experiment.Hooks{GitRun: func(ctx context.Context, path string, args ...string) (string, error) {
		if strings.Join(args, " ") == "symbolic-ref --quiet --short HEAD" {
			repository, err := gitx.Discover(ctx, path)
			if err != nil {
				return "", err
			}
			probe, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
			defer cancel()
			err = gitx.WithLifecycleMoveLock(probe, repository.GitCommonDir, func() error { return errors.New("lease was released early") })
			if !errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			checked = true
		}
		return gitx.Run(ctx, path, args...)
	}})
	result, err := service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil || !result.Moved || result.PublicationError != nil || result.Publication == nil || !checked {
		t.Fatalf("publication authority: %+v, %v", result, err)
	}
	if result.Publication.Branch != "main" || result.Publication.Head != runGit(t, result.Plan.Destination, "rev-parse", "HEAD") {
		t.Fatalf("wrong publication target: %+v", result.Publication)
	}
	identity, err := gitx.DirectoryIdentity(result.Plan.Destination)
	if err != nil || identity != result.Publication.CheckoutIdentity {
		t.Fatalf("checkout identity = %q %v", identity, err)
	}
}

func TestGraduatePublicationRetainsDetachedHeadAuthority(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, true)
	runGit(t, item.CurrentPath(), "checkout", "--detach")
	head := runGit(t, item.CurrentPath(), "rev-parse", "HEAD")
	result, err := f.service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil || !result.Moved || result.PublicationError != nil || result.Publication == nil {
		t.Fatalf("detached graduation: %+v, %v", result, err)
	}
	if !result.Publication.Detached || result.Publication.Branch != "" || result.Publication.Head != head {
		t.Fatalf("detached publication authority: %+v", result.Publication)
	}
}

func TestGraduatePublicationRejectsPostMoveHeadDrift(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, true)
	changed := false
	service := serviceWithHooks(t, f, experiment.Hooks{GitStatus: func(ctx context.Context, path string) (gitx.Status, error) {
		if filepath.Dir(path) == filepath.Dir(filepath.Join(f.projects, "placeholder")) || strings.Contains(path, string(filepath.Separator)+"projects"+string(filepath.Separator)) {
			if !changed {
				changed = true
				if _, err := gitx.Run(ctx, path, "commit", "--allow-empty", "-m", "late unreviewed commit"); err != nil {
					return gitx.Status{}, err
				}
			}
		}
		return gitx.StatusOf(ctx, path)
	}})
	result, err := service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil || !result.Moved || !changed || result.Publication != nil || result.PublicationError == nil {
		t.Fatalf("publication drift: %+v, %v", result, err)
	}
}

func TestGraduateRecoveryWaitsForActiveMove(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, true)
	finished := make(chan struct{})
	service := serviceWithHooks(t, f, experiment.Hooks{Rename: func(source, destination string) error {
		go func() { _, _ = f.service.ReconcileMoveIntents(t.Context()); close(finished) }()
		select {
		case <-finished:
			t.Error("recovery cleared active graduation intent")
		case <-time.After(60 * time.Millisecond):
		}
		entry, err := f.store.Get(item.ID)
		if err != nil || entry.MoveIntent == nil {
			return errors.New("active journal disappeared")
		}
		return os.Rename(source, destination)
	}})
	result, err := service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil || !result.Moved {
		t.Fatalf("graduation: %+v, %v", result, err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("recovery lease did not finish")
	}
}

func TestGraduateCurrentTryDoesNotUseDemotionOccupancyPolicy(t *testing.T) {
	f, item := reviewedGraduateFixture(t, true, true)
	service := serviceWithHooks(t, f, experiment.Hooks{Getwd: func() (string, error) { return item.CurrentPath(), nil }, DemoteGuard: func(context.Context, experiment.DemoteGuardRequest) (string, error) {
		return "", errors.New("active runtime")
	}})
	result, err := service.Graduate(t.Context(), experiment.GraduateRequest{})
	if err != nil || !result.Moved {
		t.Fatalf("active/current Try rejected: %+v, %v", result, err)
	}
}
