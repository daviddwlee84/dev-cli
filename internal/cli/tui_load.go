package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
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
	err             error
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

	readRepoCatalog  func(context.Context, *App) (tuiRepoCatalog, error)
	discoverFavorite func(context.Context, repo.Repo) (repo.Repo, error)
	collectRepoRow   func(context.Context, tui.RepoRow) tui.RepoRow
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
	ctx = gitx.WithObservations(ctx)
	id := l.next.Add(1)
	results := make(chan tui.LocalResult, 16)
	limiter := inventory.NewLimiter(8)
	send := func(result tui.LocalResult) {
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}
	cachePath := filepath.Join(cacheRoot(), "repos-v1.json")
	key := repo.SnapshotKey(appSnapshot.Cfg.DiscoveryRoots(), appSnapshot.Cfg.StateDir())
	streaming := l.collectRepos == nil
	presentationReady := make(chan struct{})
	if !streaming {
		close(presentationReady)
	}

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
			snapshot.err = listErr
			return snapshot, nil
		}
	}
	readCatalog := l.readRepoCatalog
	if readCatalog == nil {
		readCatalog = readTUIRepoCatalog
	}
	catalogState := startTUIFuture(func() (tuiRepoCatalog, error) {
		return readCatalog(ctx, &appSnapshot)
	})
	discoverRepos := l.discoverRepos
	if discoverRepos == nil {
		discoverRepos = func(ctx context.Context) ([]repo.Repo, error) {
			options := repo.DefaultOptions()
			options.Workers = 8
			var incomplete error
			options.OnError = func(path string, e error) {
				incomplete = errors.Join(incomplete, fmt.Errorf("discovery %s: %w", path, e))
			}
			options.OnCandidate = func(r repo.Repo) {
				// Publish the initial presentation before discovery advances the
				// model's phase; otherwise a fast candidate discards the cache.
				select {
				case <-presentationReady:
				case <-ctx.Done():
					return
				}
				row := tui.RepoRow{Repo: r, Pending: "loading"}
				select {
				case <-catalogState.ready:
					row.Asset = catalogState.value.asset(r)
				default:
				}
				send(tui.LocalResult{View: tui.ViewRepos, Generation: request.ReposGeneration, Repos: []tui.RepoRow{row}, Valid: true, Phase: "discovery"})
			}
			finish := appSnapshot.trace.Start(perftrace.TUIReposDiscovery, perftrace.Fields{})
			rows, err := repo.Discover(ctx, appSnapshot.Cfg.DiscoveryRoots(), options)
			err = errors.Join(err, incomplete)
			finish(resolverOutcome(err))
			return rows, err
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
	collectTries := l.collectTries
	if collectTries == nil {
		collectTries = func(ctx context.Context, all bool, runtimeSnapshot tuiRuntimeSnapshot) ([]tui.TryRow, error) {
			rows, err := collectTriesWithOptions(ctx, &appSnapshot, runtimeSnapshot.runtime,
				experiment.ListOptions{All: all}, runtimeSnapshot.sessions, true)
			for n := range rows {
				rows[n].RuntimeErr = runtimeSnapshot.err
			}
			return rows, errors.Join(err, runtimeSnapshot.err)
		}
	}

	tasks := startTUIFuture(listTasks)
	runtimeState := startTUIFuture(func() (tuiRuntimeSnapshot, error) {
		return loadRuntime(ctx)
	})
	repositories := startTUIFuture(func() ([]repo.Repo, error) {
		return discoverRepos(ctx)
	})

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
			Tasks: rows, Valid: !errors.Is(err, context.Canceled) && (err == nil || rows != nil), Err: err,
		})
	})
	producers.Go(func() {
		var rows []tui.RepoRow
		var err error
		if streaming {
			metadata, catalogErr := catalogState.wait(ctx)
			if len(metadata.seeds) > 0 {
				send(tui.LocalResult{View: tui.ViewRepos, Generation: request.ReposGeneration, Repos: metadata.seeds, Valid: true, Phase: "favorites"})
			}
			finish := appSnapshot.trace.Start(perftrace.TUICacheReposRead, perftrace.Fields{})
			snapshot, cacheErr := repo.ReadSnapshot(ctx, cachePath, key)
			finish(resolverOutcome(cacheErr))
			if cacheErr == nil {
				cached := make([]tui.RepoRow, 0, len(snapshot.Rows))
				for _, r := range snapshot.Rows {
					row := tui.RepoRow{Repo: r.Repo, Status: r.Status, LastActivity: r.LastActivity, ObservedAt: snapshot.ObservedAt, Pending: "cached", GitKnown: r.GitKnown, Asset: metadata.asset(r.Repo)}
					if r.Try && row.Asset == nil {
						row.Asset = &catalog.Entry{Kind: catalog.KindTry, Experiment: &catalog.Experiment{Phase: catalog.PhaseActive}}
					}
					cached = append(cached, row)
				}
				send(tui.LocalResult{View: tui.ViewRepos, Generation: request.ReposGeneration, Repos: cached, Valid: true, Phase: "cache"})
			}
			close(presentationReady)
			tracked, tasksErr := tasks.wait(ctx)
			err = errors.Join(catalogErr, tasksErr)
			collectRow := l.collectRepoRow
			var collectErr error
			if collectRow == nil && ctx.Err() == nil {
				collectRow, collectErr = prepareRepoRowCollector(ctx, &appSnapshot, runtime.None{}, repoCollectOptions{
					IncludeTries: true, DeferTopology: true, Tasks: tracked, TasksSet: true,
					SessionsSet: true, SessionsErr: errors.New("runtime observation pending"),
				})
			}
			validate := l.discoverFavorite
			if validate == nil {
				validate = discoverTUIFavorite
			}
			finish = appSnapshot.trace.Start(perftrace.TUIProducerRepos, perftrace.Fields{View: perftrace.ViewRepos, Generation: request.ReposGeneration})
			if collectRow != nil && ctx.Err() == nil {
				rows, collectErr = collectTUIRepoStream(ctx, metadata, repositories, runtimeState, limiter, validate, collectRow, tasksErr,
					func(phase string, row tui.RepoRow) {
						send(tui.LocalResult{View: tui.ViewRepos, Generation: request.ReposGeneration, Repos: []tui.RepoRow{row}, Valid: true, Phase: phase})
					})
			} else if ctx.Err() == nil {
				for _, row := range metadata.seeds {
					row.Pending, row.Unavailable = "unavailable", collectErr.Error()
					rows = append(rows, row)
				}
			}
			err = errors.Join(err, collectErr, ctx.Err())
			finish(resolverOutcome(err))
		} else {
			// Custom collectors keep the original shared-source contract.
			tracked, tasksErr := tasks.wait(ctx)
			discovered, reposErr := repositories.wait(ctx)
			live, runtimeErr := runtimeState.wait(ctx)
			err = errors.Join(tasksErr, reposErr, runtimeErr)
			if ctx.Err() == nil && (err == nil || discovered != nil) {
				var collectErr error
				rows, collectErr = l.collectRepos(ctx, tracked, discovered, live, limiter)
				err = errors.Join(err, collectErr)
			}
		}
		if streaming && err == nil && ctx.Err() == nil {
			snapshot := repo.Snapshot{Fingerprint: key, ObservedAt: time.Now()}
			for _, r := range rows {
				snapshot.Rows = append(snapshot.Rows, repo.CachedRow{Repo: r.Repo, Status: r.Status, GitKnown: r.GitKnown, LastActivity: r.LastActivity, Try: r.IsTry()})
			}
			_ = repo.WriteSnapshot(cachePath, snapshot)
		}
		send(tui.LocalResult{View: tui.ViewRepos, Generation: request.ReposGeneration, Repos: rows, Valid: !errors.Is(err, context.Canceled) && (err == nil || rows != nil), Err: err})
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
			Tries: rows, Valid: !errors.Is(err, context.Canceled) && (err == nil || rows != nil), Err: err,
		})
	})
	go func() {
		producers.Wait()
		// Discovery's candidate callback is also a producer. On cancellation,
		// wait for it to leave before closing the result stream.
		<-repositories.ready
		close(results)
	}()
	return tui.LocalLoad{ID: id, Request: request, Results: results}
}

func joinRuntime(row *tui.RepoRow, live tuiRuntimeSnapshot) {
	name := "none"
	if live.runtime != nil {
		name = live.runtime.Name()
	}
	row.Context = inventory.WithRuntime(row.Context, name, live.sessions, live.err)
	row.Live = false
	row.RuntimeHandle = ""
	row.RuntimeStatus = ""
	row.Runtime = name
	if sessions := row.Context.Sessions(); len(sessions) > 0 {
		row.Live = true
		row.RuntimeHandle = sessions[0].Handle
		row.RuntimeStatus = sessions[0].AgentStatus
	}
}
