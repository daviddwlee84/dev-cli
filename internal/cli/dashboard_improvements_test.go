package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestStreamingReposPublishGitBeforeRuntimeAndCacheCompletedRows(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	git := gittest.New(t)
	app.Cfg.Paths.ScanRoots = []string{git.Root}
	app.Cfg.Paths.RepoPaths = nil
	app.trace = perftrace.New(512)
	loader := newTUILocalLoader(app, nil)
	gate := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loader.loadRuntime = func(ctx context.Context) (tuiRuntimeSnapshot, error) {
		select {
		case <-gate:
			return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
		case <-ctx.Done():
			return tuiRuntimeSnapshot{}, ctx.Err()
		}
	}
	loader.collectTasks = func(context.Context, []*task.Task, tuiRuntimeSnapshot, *inventory.Limiter) ([]inventory.Row, error) {
		return nil, nil
	}
	loader.collectTries = func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil }
	load := loader.Start(ctx, tui.LocalLoadRequest{ReposGeneration: 1})
	timer := time.After(10 * time.Second)
	sawGit := false
	for !sawGit {
		select {
		case result := <-load.Results:
			if result.Phase == "enrichment" {
				if len(result.Repos) != 1 || !result.Repos[0].GitKnown || result.Repos[0].Context.RuntimeErr == nil {
					t.Fatalf("%+v", result)
				}
				sawGit = true
			}
		case <-timer:
			t.Fatal("runtime blocked Git rows")
		}
	}
	close(gate)
	complete := false
	for result := range load.Results {
		if result.View == tui.ViewRepos && result.Phase == "" {
			complete = result.Valid && result.Err == nil && result.Repos[0].Pending == ""
		}
	}
	if !complete {
		t.Fatal("no completed repository result")
	}
	cache, err := repo.ReadSnapshot(ctx, filepath.Join(cacheRoot(), "repos-v1.json"), repo.SnapshotKey(app.Cfg.DiscoveryRoots(), app.Cfg.StateDir()))
	if err != nil || len(cache.Rows) != 1 {
		t.Fatalf("cache %+v: %v", cache, err)
	}
}

func TestSkillManagementKeepsExplicitFilteredRepositoryPool(t *testing.T) {
	app, out := newRepoWizardApp(t, "")
	first, second := gittest.New(t), gittest.New(t)
	app.Cfg.Paths.ScanRoots = []string{first.Root, second.Root}
	// Uncheckable local sources must not initiate a network request.
	for _, path := range []string{first.Root, second.Root} {
		if err := os.WriteFile(filepath.Join(path, "skills-lock.json"), []byte(`{"version":1,"skills":{"demo":{"source":"local","sourceType":"local"}}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if request.Prompt == "Repositories" {
			if len(request.Items) != 1 || !strings.Contains(request.Items[0].Description, filepath.Base(filepath.Dir(first.Root))) {
				t.Fatalf("expanded explicit scope: %+v", request)
			}
			return picker.Result{Items: request.Items}, nil
		}
		for _, item := range request.Items {
			if item.Value == "check" {
				return picker.Result{Item: item}, nil
			}
		}
		return picker.Result{}, errors.New("unexpected picker")
	}
	run, err := runSkillManage(context.Background(), app, skillManageRequest{Refs: []string{first.Root}, Scope: "repos"})
	if err != nil || len(run.Targets) != 1 || run.Targets[0].CheckoutRoot != first.Root || run.Mutated {
		t.Fatalf("%+v %v\n%s", run, err, out.String())
	}
}

func TestSkillManageNoninteractiveRequiresExistingAutomationCommands(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	cmd := newSkillManageCmd(app)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatal(err)
	}
}
