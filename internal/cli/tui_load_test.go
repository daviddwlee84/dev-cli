package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestTUILocalLoaderSharesInputsAndPublishesViewsIndependently(t *testing.T) {
	app := &App{trace: perftrace.New(64)}
	loader := &tuiLocalLoader{app: app}
	var taskReads, runtimeReads, repoReads atomic.Int32
	taskStarted, runtimeStarted, repoStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	tracked := []*task.Task{{ID: "task-a", Name: "task-a", Repo: "api", RepoPath: "/src/api"}}
	discovered := []repo.Repo{{Name: "api", Path: "/src/api", RealPath: "/src/api"}}
	loader.listTasks = func() ([]*task.Task, error) {
		taskReads.Add(1)
		close(taskStarted)
		return tracked, nil
	}
	loader.loadRuntime = func(context.Context) (tuiRuntimeSnapshot, error) {
		runtimeReads.Add(1)
		close(runtimeStarted)
		return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
	}
	loader.discoverRepos = func(context.Context) ([]repo.Repo, error) {
		repoReads.Add(1)
		close(repoStarted)
		return discovered, nil
	}
	repoGate, tryGate := make(chan struct{}), make(chan struct{})
	var taskLimiter, repoLimiter *inventory.Limiter
	loader.collectTasks = func(_ context.Context, got []*task.Task, _ tuiRuntimeSnapshot,
		limiter *inventory.Limiter) ([]inventory.Row, error) {
		taskLimiter = limiter
		return []inventory.Row{{Task: got[0], Checkout: got[0].RepoPath}}, nil
	}
	loader.collectRepos = func(_ context.Context, gotTasks []*task.Task, gotRepos []repo.Repo,
		_ tuiRuntimeSnapshot, limiter *inventory.Limiter) ([]tui.RepoRow, error) {
		<-repoGate
		repoLimiter = limiter
		if len(gotTasks) != 1 || len(gotRepos) != 1 {
			t.Fatalf("shared inputs tasks=%d repos=%d", len(gotTasks), len(gotRepos))
		}
		return []tui.RepoRow{{Repo: gotRepos[0], Tasks: gotTasks}}, nil
	}
	loader.collectTries = func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) {
		<-tryGate
		return []tui.TryRow{{}}, nil
	}

	load := loader.Start(context.Background(), tui.LocalLoadRequest{
		TasksGeneration: 1, ReposGeneration: 2, TriesGeneration: 3,
	})
	first := receiveLocalResult(t, load.Results)
	if first.View != tui.ViewTasks || first.Generation != 1 || len(first.Tasks) != 1 {
		t.Fatalf("first result = %+v", first)
	}
	<-taskStarted
	<-runtimeStarted
	<-repoStarted
	if taskReads.Load() != 1 || runtimeReads.Load() != 1 || repoReads.Load() != 1 {
		t.Fatalf("input reads tasks=%d runtime=%d repos=%d", taskReads.Load(), runtimeReads.Load(), repoReads.Load())
	}

	close(repoGate)
	second := receiveLocalResult(t, load.Results)
	if second.View != tui.ViewRepos || second.Generation != 2 || len(second.Repos) != 1 {
		t.Fatalf("second result = %+v", second)
	}
	if taskLimiter == nil || taskLimiter != repoLimiter {
		t.Fatal("TASKS and REPOS did not share one enrichment limiter")
	}

	close(tryGate)
	third := receiveLocalResult(t, load.Results)
	if third.View != tui.ViewTries || third.Generation != 3 || len(third.Tries) != 1 {
		t.Fatalf("third result = %+v", third)
	}
	if _, ok := <-load.Results; ok {
		t.Fatal("local result stream did not close")
	}
}

func receiveLocalResult(t *testing.T, results <-chan tui.LocalResult) tui.LocalResult {
	t.Helper()
	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("local result stream closed early")
		}
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for local result")
		return tui.LocalResult{}
	}
}
