package cli

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type tuiFuture[T any] struct {
	ready chan struct{}
	value T
	err   error
}

func startTUIFuture[T any](work func() (T, error)) *tuiFuture[T] {
	future := &tuiFuture[T]{ready: make(chan struct{})}
	go func() {
		future.value, future.err = work()
		close(future.ready)
	}()
	return future
}

func (f *tuiFuture[T]) wait(ctx context.Context) (T, error) {
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-f.ready:
		return f.value, f.err
	}
}

type tuiRuntimeSnapshot struct {
	runtime         runtime.Runtime
	sessions        []runtime.Session
	sessionsTracked bool
}

type tuiLocalLoader struct {
	app     *App
	current func() *App
	runtime *tuiRuntimeResolver
	next    atomic.Uint64

	listTasks     func() ([]*task.Task, error)
	loadRuntime   func(context.Context) (tuiRuntimeSnapshot, error)
	discoverRepos func(context.Context) ([]repo.Repo, error)
	collectTasks  func(context.Context, []*task.Task, tuiRuntimeSnapshot, *inventory.Limiter) ([]inventory.Row, error)
	collectRepos  func(context.Context, []*task.Task, []repo.Repo, tuiRuntimeSnapshot, *inventory.Limiter) ([]tui.RepoRow, error)
	collectTries  func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error)
}

func newTUILocalLoader(app *App, runtimeResolver *tuiRuntimeResolver) *tuiLocalLoader {
	return &tuiLocalLoader{app: app, runtime: runtimeResolver}
}

func (l *tuiLocalLoader) Start(ctx context.Context, request tui.LocalLoadRequest) tui.LocalLoad {
	if ctx == nil {
		ctx = context.Background()
	}
	current := l.app
	if l.current != nil {
		current = l.current()
	}
	appSnapshot := *current
	listTasks := l.listTasks
	if listTasks == nil {
		listTasks = appSnapshot.Tasks.List
	}
	loadRuntime := l.loadRuntime
	if loadRuntime == nil {
		loadRuntime = func(ctx context.Context) (tuiRuntimeSnapshot, error) {
			rt, err := l.runtime.Resolve(ctx)
			if err != nil {
				return tuiRuntimeSnapshot{}, err
			}
			snapshot := tuiRuntimeSnapshot{runtime: rt}
			if rt.Name() == "none" {
				return snapshot, nil
			}
			finish := appSnapshot.trace.Start(perftrace.TUIRuntimeList, perftrace.Fields{})
			sessions, listErr := rt.List(ctx)
			finish(resolverOutcome(listErr))
			if listErr == nil {
				snapshot.sessions = sessions
				snapshot.sessionsTracked = true
			}
			return snapshot, nil
		}
	}
	discoverRepos := l.discoverRepos
	if discoverRepos == nil {
		discoverRepos = func(ctx context.Context) ([]repo.Repo, error) {
			return repo.Discover(ctx, appSnapshot.Cfg.DiscoveryRoots(), repo.DefaultOptions())
		}
	}
	collectTasks := l.collectTasks
	if collectTasks == nil {
		collectTasks = func(ctx context.Context, tracked []*task.Task, runtimeSnapshot tuiRuntimeSnapshot,
			limiter *inventory.Limiter) ([]inventory.Row, error) {
			rows := inventory.Collect(ctx, tracked, runtimeSnapshot.runtime, inventory.Options{
				Sessions: runtimeSnapshot.sessions, SessionsSet: true,
				SessionsTracked: runtimeSnapshot.sessionsTracked, Limiter: limiter,
			})
			return rows, ctx.Err()
		}
	}
	collectRepos := l.collectRepos
	if collectRepos == nil {
		collectRepos = func(ctx context.Context, tracked []*task.Task, discovered []repo.Repo,
			runtimeSnapshot tuiRuntimeSnapshot, limiter *inventory.Limiter) ([]tui.RepoRow, error) {
			return collectReposWithOptions(ctx, &appSnapshot, runtimeSnapshot.runtime, repoCollectOptions{
				IncludeTries: true,
				Sessions:     runtimeSnapshot.sessions, SessionsSet: true,
				Tasks: tracked, TasksSet: true,
				Repos: discovered, ReposSet: true,
				Limiter: limiter,
			})
		}
	}
	collectTries := l.collectTries
	if collectTries == nil {
		collectTries = func(ctx context.Context, all bool, runtimeSnapshot tuiRuntimeSnapshot) ([]tui.TryRow, error) {
			return collectTriesWithOptions(ctx, &appSnapshot, runtimeSnapshot.runtime,
				experiment.ListOptions{All: all}, runtimeSnapshot.sessions, true)
		}
	}

	id := l.next.Add(1)
	results := make(chan tui.LocalResult, 3)
	limiter := inventory.NewLimiter(8)

	tasks := startTUIFuture(listTasks)
	runtimeState := startTUIFuture(func() (tuiRuntimeSnapshot, error) {
		return loadRuntime(ctx)
	})
	repositories := startTUIFuture(func() ([]repo.Repo, error) {
		return discoverRepos(ctx)
	})

	send := func(result tui.LocalResult) {
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}
	var producers sync.WaitGroup
	producers.Go(func() {
		tracked, tasksErr := tasks.wait(ctx)
		runtimeSnapshot, runtimeErr := runtimeState.wait(ctx)
		err := errors.Join(tasksErr, runtimeErr)
		var rows []inventory.Row
		if err == nil {
			finish := l.app.trace.Start(perftrace.TUIProducerTasks, perftrace.Fields{
				View: perftrace.ViewTasks, Generation: request.TasksGeneration,
			})
			rows, err = collectTasks(ctx, tracked, runtimeSnapshot, limiter)
			finish(resolverOutcome(err))
		}
		send(tui.LocalResult{
			View: tui.ViewTasks, Generation: request.TasksGeneration,
			Tasks: rows, Valid: err == nil || rows != nil, Err: err,
		})
	})
	producers.Go(func() {
		tracked, tasksErr := tasks.wait(ctx)
		runtimeSnapshot, runtimeErr := runtimeState.wait(ctx)
		discovered, reposErr := repositories.wait(ctx)
		err := errors.Join(tasksErr, runtimeErr, reposErr)
		var rows []tui.RepoRow
		if err == nil {
			finish := l.app.trace.Start(perftrace.TUIProducerRepos, perftrace.Fields{
				View: perftrace.ViewRepos, Generation: request.ReposGeneration,
			})
			rows, err = collectRepos(ctx, tracked, discovered, runtimeSnapshot, limiter)
			finish(resolverOutcome(err))
		}
		send(tui.LocalResult{
			View: tui.ViewRepos, Generation: request.ReposGeneration,
			Repos: rows, Valid: err == nil || rows != nil, Err: err,
		})
	})
	producers.Go(func() {
		runtimeSnapshot, err := runtimeState.wait(ctx)
		var rows []tui.TryRow
		if err == nil {
			finish := l.app.trace.Start(perftrace.TUIProducerTries, perftrace.Fields{
				View: perftrace.ViewTries, Generation: request.TriesGeneration,
			})
			rows, err = collectTries(ctx, request.ShowAllTries, runtimeSnapshot)
			finish(resolverOutcome(err))
		}
		send(tui.LocalResult{
			View: tui.ViewTries, Generation: request.TriesGeneration,
			Tries: rows, Valid: err == nil || rows != nil, Err: err,
		})
	})
	go func() {
		producers.Wait()
		close(results)
	}()
	return tui.LocalLoad{ID: id, Request: request, Results: results}
}
