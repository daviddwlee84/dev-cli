package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func loaderTestApp(t *testing.T, roots ...string) *App {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg := config.Config{}
	cfg.Paths.ScanRoots = roots
	cfg.Paths.StateDir = t.TempDir()
	return &App{Cfg: cfg, Catalog: catalog.NewStore(filepath.Join(t.TempDir(), "assets")),
		Tasks: task.NewStore(filepath.Join(t.TempDir(), "tasks")), trace: perftrace.New(64)}
}

func loaderFavorite(t *testing.T, app *App, r repo.Repo) *catalog.Entry {
	t.Helper()
	entry := &catalog.Entry{Kind: catalog.KindRepository, Name: r.Name, Favorite: true,
		Locations: map[string]catalog.Location{configHostname(): {
			State: catalog.LocationPresent, CurrentPath: r.Path, RealPath: r.RealPath, GitCommonDir: r.CommonDir,
		}}}
	if err := app.Catalog.Create(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func loaderDiscoveredRepo(t *testing.T, path string) repo.Repo {
	t.Helper()
	g, err := gitx.Discover(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Repo{Name: filepath.Base(path), Path: path, RealPath: g.MainRoot,
		GitDir: g.GitDir, CommonDir: g.GitCommonDir, MainRoot: g.MainRoot, HasGit: true}
}

func waitRepoResult(t *testing.T, results <-chan tui.LocalResult, phase string) tui.LocalResult {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case result, ok := <-results:
			if !ok {
				t.Fatalf("result stream ended before REPOS phase %q", phase)
			}
			if result.View == tui.ViewRepos && result.Phase == phase {
				return result
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for REPOS phase %q", phase)
		}
	}
}

func awaitLoaderValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for loader barrier")
		var zero T
		return zero
	}
}

func TestTUILocalLoaderPublishesCacheBeforeDiscoveryCandidates(t *testing.T) {
	r := gittest.New(t)
	discovered := loaderDiscoveredRepo(t, r.Root)
	app := loaderTestApp(t, r.Root)
	snapshot := repo.Snapshot{Fingerprint: repo.SnapshotKey(app.Cfg.DiscoveryRoots(), app.Cfg.StateDir()), ObservedAt: time.Now(),
		Rows: []repo.CachedRow{{Repo: discovered}}}
	if err := repo.WriteSnapshot(filepath.Join(cacheRoot(), "repos-v1.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	loader := &tuiLocalLoader{app: app,
		loadRuntime: func(context.Context) (tuiRuntimeSnapshot, error) {
			return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
		},
		collectTries: func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil },
	}
	load := loader.Start(t.Context(), tui.LocalLoadRequest{ReposGeneration: 1})
	first, sawDiscovery := true, false
	for result := range load.Results {
		if result.View != tui.ViewRepos {
			continue
		}
		if first && result.Phase != "cache" {
			t.Fatalf("discovery overtook cached presentation: %q", result.Phase)
		}
		first = false
		sawDiscovery = sawDiscovery || result.Phase == "discovery"
	}
	if first || !sawDiscovery {
		t.Fatal("loader did not publish cache and discovery")
	}
}

func TestTUIRepoStreamStatusFailureDoesNotMakeNavigationUnavailable(t *testing.T) {
	r := repo.Repo{Name: "favorite", Path: t.TempDir(), HasGit: true}
	metadata := tuiRepoCatalog{seeds: []tui.RepoRow{{Repo: r, Asset: &catalog.Entry{ID: "favorite", Kind: catalog.KindRepository, Favorite: true}}}}
	discovered := startTUIFuture(func() ([]repo.Repo, error) { return nil, nil })
	live := startTUIFuture(func() (tuiRuntimeSnapshot, error) { return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil })
	rows, err := collectTUIRepoStream(t.Context(), metadata, discovered, live, inventory.NewLimiter(1),
		func(_ context.Context, r repo.Repo) (repo.Repo, error) { return r, nil },
		func(_ context.Context, row tui.RepoRow) tui.RepoRow { return row }, nil, func(string, tui.RepoRow) {})
	if err == nil || len(rows) != 1 || rows[0].GitKnown || rows[0].Pending != "Git observation unavailable" || rows[0].Unavailable != "" {
		t.Fatalf("status error became target unavailability or a clean observation: %+v, %v", rows, err)
	}
}

func TestTUILocalLoaderFavoriteBeforeDiscoveryAndRuntimeRejoin(t *testing.T) {
	r := gittest.New(t)
	discovered := loaderDiscoveredRepo(t, r.Root)
	app := loaderTestApp(t, r.Root)
	entry := loaderFavorite(t, app, discovered)
	before, err := os.ReadFile(filepath.Join(app.Catalog.Dir, entry.ID+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	discoveryGate, runtimeGate := make(chan struct{}), make(chan struct{})
	loader := &tuiLocalLoader{app: app,
		discoverRepos: func(ctx context.Context) ([]repo.Repo, error) {
			select {
			case <-discoveryGate:
				return []repo.Repo{discovered}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		loadRuntime: func(ctx context.Context) (tuiRuntimeSnapshot, error) {
			select {
			case <-runtimeGate:
				return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
			case <-ctx.Done():
				return tuiRuntimeSnapshot{}, ctx.Err()
			}
		},
		collectTries: func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil },
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	load := loader.Start(ctx, tui.LocalLoadRequest{ReposGeneration: 42})
	seeds := waitRepoResult(t, load.Results, "favorites")
	if seeds.Generation != 42 || len(seeds.Repos) != 1 || !seeds.Repos[0].Favorite() || seeds.Repos[0].Pending == "" {
		t.Fatalf("cold-start seeds = %+v", seeds)
	}
	enriched := waitRepoResult(t, load.Results, "enrichment")
	if enriched.Generation != 42 || len(enriched.Repos) != 1 || !enriched.Repos[0].GitKnown || enriched.Repos[0].Pending != "runtime pending" {
		t.Fatalf("favorite did not enrich before discovery/runtime: %+v", enriched)
	}
	close(runtimeGate)
	rejoined := waitRepoResult(t, load.Results, "runtime")
	if len(rejoined.Repos) != 1 || rejoined.Repos[0].Pending != "" || rejoined.Repos[0].Context.RuntimeErr != nil {
		t.Fatalf("runtime-ready row = %+v", rejoined)
	}
	// Full discovery is still blocked: the runtime update cannot be a final join.
	close(discoveryGate)
	final := waitRepoResult(t, load.Results, "")
	if final.Err != nil || len(final.Repos) != 1 || !final.Repos[0].Favorite() || final.Repos[0].Pending != "" {
		t.Fatalf("final favorite = %+v", final)
	}
	for range load.Results {
	}
	after, err := os.ReadFile(filepath.Join(app.Catalog.Dir, entry.ID+".toml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("discovery wrote catalog: %v", err)
	}
	cached, err := os.ReadFile(filepath.Join(cacheRoot(), "repos-v1.json"))
	if err != nil || bytes.Contains(cached, []byte(`"favorite"`)) || bytes.Contains(cached, []byte(entry.ID)) {
		t.Fatalf("cache contains preferences or was not written: %s, %v", cached, err)
	}
}

func readyLoaderFuture[T any](value T, err error) *tuiFuture[T] {
	ready := make(chan struct{})
	close(ready)
	return &tuiFuture[T]{ready: ready, value: value, err: err}
}

func syntheticLoaderRow(root, name string, favorite bool) tui.RepoRow {
	return tui.RepoRow{Repo: repo.Repo{Name: name, Path: filepath.Join(root, name), RealPath: filepath.Join(root, name)},
		Asset: &catalog.Entry{ID: name, Kind: catalog.KindRepository, Favorite: favorite}}
}

func TestTUIRepoStreamSlowFavoriteDoesNotBlockOrdinaryAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	favorite := syntheticLoaderRow(root, "favorite", true)
	ordinary := syntheticLoaderRow(root, "ordinary", false)
	gate, started := make(chan struct{}), make(chan struct{})
	published := make(chan tui.RepoRow, 8)
	var collects atomic.Int32
	done := startTUIFuture(func() ([]tui.RepoRow, error) {
		return collectTUIRepoStream(t.Context(), tuiRepoCatalog{seeds: []tui.RepoRow{favorite}},
			readyLoaderFuture([]repo.Repo{favorite.Repo, ordinary.Repo}, nil),
			readyLoaderFuture(tuiRuntimeSnapshot{runtime: runtime.None{}}, nil), inventory.NewLimiter(2),
			func(ctx context.Context, r repo.Repo) (repo.Repo, error) {
				close(started)
				select {
				case <-gate:
					return r, nil
				case <-ctx.Done():
					return r, ctx.Err()
				}
			}, func(_ context.Context, row tui.RepoRow) tui.RepoRow { collects.Add(1); return row }, nil,
			func(_ string, row tui.RepoRow) { published <- row })
	})
	awaitLoaderValue(t, started)
	row := awaitLoaderValue(t, published)
	if row.Repo.Path != ordinary.Repo.Path {
		t.Fatalf("slow favorite blocked unrelated row: %+v", row)
	}
	close(gate)
	awaitLoaderValue(t, done.ready)
	if done.err != nil || len(done.value) != 2 || collects.Load() != 2 {
		t.Fatalf("dedup/final rows = %d, collects=%d, err=%v", len(done.value), collects.Load(), done.err)
	}
}

func TestTUIRepoStreamRuntimeRejoinsWhileOrdinarySlotsAreBusy(t *testing.T) {
	root := t.TempDir()
	favorite := syntheticLoaderRow(root, "favorite", true)
	var discovered []repo.Repo
	for i := 0; i < 3; i++ {
		discovered = append(discovered, syntheticLoaderRow(root, fmt.Sprint(i), false).Repo)
	}
	runtimeReady, gate := make(chan struct{}), make(chan struct{})
	ordinaryStarted := make(chan struct{}, 3)
	published := make(chan tui.LocalResult, 8)
	done := startTUIFuture(func() ([]tui.RepoRow, error) {
		return collectTUIRepoStream(t.Context(), tuiRepoCatalog{seeds: []tui.RepoRow{favorite}}, readyLoaderFuture(discovered, nil),
			&tuiFuture[tuiRuntimeSnapshot]{ready: runtimeReady, value: tuiRuntimeSnapshot{runtime: runtime.None{}}}, inventory.NewLimiter(2),
			func(_ context.Context, r repo.Repo) (repo.Repo, error) { return r, nil },
			func(ctx context.Context, row tui.RepoRow) tui.RepoRow {
				if !row.Favorite() {
					ordinaryStarted <- struct{}{}
					select {
					case <-gate:
					case <-ctx.Done():
					}
				}
				return row
			}, nil, func(phase string, row tui.RepoRow) {
				published <- tui.LocalResult{View: tui.ViewRepos, Phase: phase, Repos: []tui.RepoRow{row}}
			})
	})
	first := waitRepoResult(t, published, "enrichment")
	if len(first.Repos) != 1 || !first.Repos[0].Favorite() || first.Repos[0].Pending != "runtime pending" {
		t.Fatalf("initial favorite = %+v", first)
	}
	awaitLoaderValue(t, ordinaryStarted)
	awaitLoaderValue(t, ordinaryStarted)
	close(runtimeReady)
	rejoined := waitRepoResult(t, published, "runtime")
	if !rejoined.Repos[0].Favorite() || rejoined.Repos[0].Pending != "" {
		t.Fatalf("runtime join waited behind occupied slots: %+v", rejoined)
	}
	close(gate)
	awaitLoaderValue(t, done.ready)
	if done.err != nil || len(done.value) != 4 {
		t.Fatalf("final rows=%d err=%v", len(done.value), done.err)
	}
}

func TestTUIRepoStreamFavoriteAdmissionAndSharedBound(t *testing.T) {
	root := t.TempDir()
	var seeds []tui.RepoRow
	var discovered []repo.Repo
	for i := 0; i < 10; i++ {
		row := syntheticLoaderRow(root, fmt.Sprintf("favorite-%02d", i), true)
		seeds = append(seeds, row)
		discovered = append(discovered, row.Repo)
	}
	for i := 0; i < 12; i++ {
		discovered = append(discovered, syntheticLoaderRow(root, fmt.Sprintf("ordinary-%02d", i), false).Repo)
	}
	limiter := inventory.NewLimiter(8)
	// Simulate TASKS retaining three of the shared eight permits.
	for i := 0; i < 3; i++ {
		release, ok := limiter.Acquire(t.Context())
		if !ok {
			t.Fatal("failed to reserve task permit")
		}
		defer release()
	}
	started, gate := make(chan bool, 32), make(chan struct{})
	var active, maximum, validations, collects atomic.Int32
	work := func(ctx context.Context, favorite bool) {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		started <- favorite
		select {
		case <-gate:
		case <-ctx.Done():
		}
		active.Add(-1)
	}
	done := startTUIFuture(func() ([]tui.RepoRow, error) {
		return collectTUIRepoStream(t.Context(), tuiRepoCatalog{seeds: seeds}, readyLoaderFuture(discovered, nil),
			readyLoaderFuture(tuiRuntimeSnapshot{runtime: runtime.None{}}, nil), limiter,
			func(ctx context.Context, r repo.Repo) (repo.Repo, error) {
				validations.Add(1)
				work(ctx, true)
				return r, nil
			}, func(ctx context.Context, row tui.RepoRow) tui.RepoRow {
				collects.Add(1)
				work(ctx, row.Favorite())
				return row
			}, nil, func(string, tui.RepoRow) {})
	})
	for i := 0; i < 5; i++ {
		if !awaitLoaderValue(t, started) {
			t.Fatal("nonfavorite admitted while favorites remained queued")
		}
	}
	select {
	case <-started:
		t.Fatal("enrichment exceeded shared eight-slot limit")
	default:
	}
	close(gate)
	awaitLoaderValue(t, done.ready)
	if done.err != nil || len(done.value) != 22 || validations.Load() != 10 || collects.Load() != 22 || maximum.Load() > 5 {
		t.Fatalf("rows=%d validations=%d collects=%d maximum=%d err=%v", len(done.value), validations.Load(), collects.Load(), maximum.Load(), done.err)
	}
}

func TestTUIRepoStreamCancellationReleasesAdmittedWork(t *testing.T) {
	root := t.TempDir()
	var seeds []tui.RepoRow
	for i := 0; i < 30; i++ {
		seeds = append(seeds, syntheticLoaderRow(root, fmt.Sprint(i), true))
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{}, 8)
	limiter := inventory.NewLimiter(8)
	var active atomic.Int32
	done := startTUIFuture(func() ([]tui.RepoRow, error) {
		return collectTUIRepoStream(ctx, tuiRepoCatalog{seeds: seeds}, readyLoaderFuture([]repo.Repo{}, nil),
			readyLoaderFuture(tuiRuntimeSnapshot{runtime: runtime.None{}}, nil), limiter,
			func(ctx context.Context, r repo.Repo) (repo.Repo, error) {
				active.Add(1)
				defer active.Add(-1)
				started <- struct{}{}
				<-ctx.Done()
				return r, ctx.Err()
			}, func(context.Context, tui.RepoRow) tui.RepoRow { panic("canceled favorite enriched") }, nil, func(string, tui.RepoRow) {})
	})
	for i := 0; i < 8; i++ {
		awaitLoaderValue(t, started)
	}
	cancel()
	awaitLoaderValue(t, done.ready)
	if !errors.Is(done.err, context.Canceled) || active.Load() != 0 {
		t.Fatalf("cancellation err=%v active=%d", done.err, active.Load())
	}
	for i := 0; i < 8; i++ {
		permitCtx, permitCancel := context.WithTimeout(t.Context(), time.Second)
		release, ok := limiter.Acquire(permitCtx)
		permitCancel()
		if !ok {
			t.Fatal("canceled loader retained a shared permit")
		}
		defer release()
	}
}

func TestTUILocalLoaderUnavailableFavoriteAndCatalogDiagnostics(t *testing.T) {
	root := t.TempDir()
	app := loaderTestApp(t, root)
	missing := syntheticLoaderRow(root, "missing", true)
	missing.Repo.HasGit, missing.Repo.CommonDir = true, filepath.Join(missing.Repo.Path, ".git")
	loaderFavorite(t, app, missing.Repo)
	if err := os.WriteFile(filepath.Join(app.Catalog.Dir, "broken.toml"), []byte("not = = toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := &tuiLocalLoader{app: app,
		loadRuntime: func(context.Context) (tuiRuntimeSnapshot, error) {
			return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
		},
		collectTries: func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil },
	}
	load := loader.Start(t.Context(), tui.LocalLoadRequest{ReposGeneration: 9})
	final := waitRepoResult(t, load.Results, "")
	if final.Err == nil || !final.Valid || len(final.Repos) != 1 {
		t.Fatalf("missing favorite final = %+v", final)
	}
	row := final.Repos[0]
	if !row.Favorite() || row.Pending != "unavailable" || row.Unavailable == "" || row.GitKnown {
		t.Fatalf("unavailable favorite became authoritative: %+v", row)
	}
	for range load.Results {
	}
	if _, err := os.Stat(filepath.Join(cacheRoot(), "repos-v1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial observation was cached: %v", err)
	}
}

func TestTUILocalLoaderCancellationDoesNotReplaceCache(t *testing.T) {
	root := t.TempDir()
	app := loaderTestApp(t, root)
	row := syntheticLoaderRow(root, "favorite", true)
	loaderFavorite(t, app, row.Repo)
	path := filepath.Join(cacheRoot(), "repos-v1.json")
	key := repo.SnapshotKey(app.Cfg.DiscoveryRoots(), app.Cfg.StateDir())
	if err := repo.WriteSnapshot(path, repo.Snapshot{Fingerprint: key, ObservedAt: time.Now(), Rows: []repo.CachedRow{{Repo: row.Repo}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	loader := &tuiLocalLoader{app: app,
		discoverFavorite: func(ctx context.Context, r repo.Repo) (repo.Repo, error) {
			close(started)
			<-ctx.Done()
			return r, ctx.Err()
		},
		discoverRepos: func(ctx context.Context) ([]repo.Repo, error) { <-ctx.Done(); return nil, ctx.Err() },
		loadRuntime: func(context.Context) (tuiRuntimeSnapshot, error) {
			return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
		},
		collectTries: func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil },
	}
	ctx, cancel := context.WithCancel(t.Context())
	load := loader.Start(ctx, tui.LocalLoadRequest{})
	cached := waitRepoResult(t, load.Results, "cache")
	if len(cached.Repos) != 1 || !cached.Repos[0].Favorite() {
		t.Fatalf("current preference not joined to cache: %+v", cached)
	}
	awaitLoaderValue(t, started)
	cancel()
	for range load.Results {
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("cancellation replaced cache: %v", err)
	}
}

// Cached rows join recorded tasks and derive a size target so the first frame
// orders hot work correctly and can show cached sizes; the live hint is an
// ordering hint that enrichment clears.
func TestTUILocalLoaderCachedRowsCarryTasksSizeTargetAndLiveHint(t *testing.T) {
	r := gittest.New(t)
	discovered := loaderDiscoveredRepo(t, r.Root)
	app := loaderTestApp(t, r.Root)
	if err := app.Tasks.Save(&task.Task{Repo: discovered.Name, RepoPath: discovered.Path, Branch: "hot-work", Base: "main", Mode: task.ModeDirect, State: task.Hot}); err != nil {
		t.Fatal(err)
	}
	snapshot := repo.Snapshot{Fingerprint: repo.SnapshotKey(app.Cfg.DiscoveryRoots(), app.Cfg.StateDir()), ObservedAt: time.Now(),
		Rows: []repo.CachedRow{{Repo: discovered, Worktrees: 1, Live: true}}}
	cachePath := filepath.Join(cacheRoot(), "repos-v1.json")
	if err := repo.WriteSnapshot(cachePath, snapshot); err != nil {
		t.Fatal(err)
	}
	app.Sizes = diskusage.NewManager(diskusage.NewCache("", time.Hour), 1)
	if err := app.Sizes.Cache.Set(repoSizeTarget(discovered, 1), diskusage.Usage{CheckoutBytes: 7, OwnedBytes: 7, Complete: true, MeasuredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	loader := &tuiLocalLoader{app: app,
		loadRuntime: func(context.Context) (tuiRuntimeSnapshot, error) {
			return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
		},
		collectTries: func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil },
	}
	load := loader.Start(t.Context(), tui.LocalLoadRequest{ReposGeneration: 1})
	cached := waitRepoResult(t, load.Results, "cache")
	if len(cached.Repos) != 1 {
		t.Fatalf("cache rows = %+v", cached.Repos)
	}
	row := cached.Repos[0]
	if row.Usage == nil || !row.Usage.Cached || row.Usage.OwnedBytes != 7 {
		t.Fatalf("cached row did not read the size cache: %+v", row.Usage)
	}
	if row.HotTasks() != 1 || row.Worktrees != 1 || !row.CachedLive || row.Live || row.SizeTarget.Checkout == "" {
		t.Fatalf("cached row lacks hints: tasks=%d worktrees=%d cachedLive=%v live=%v size=%q", row.HotTasks(), row.Worktrees, row.CachedLive, row.Live, row.SizeTarget.Checkout)
	}
	var complete tui.LocalResult
	for result := range load.Results {
		if result.View == tui.ViewRepos && result.Phase == "enrichment" && result.Repos[0].CachedLive {
			t.Fatal("enrichment carried the cache hint")
		}
		if result.View == tui.ViewRepos && result.Phase == "" {
			complete = result
		}
	}
	if len(complete.Repos) != 1 || complete.Repos[0].CachedLive || complete.Repos[0].HotTasks() != 1 {
		t.Fatalf("complete snapshot = %+v", complete.Repos)
	}
	written, err := repo.ReadSnapshot(t.Context(), cachePath, snapshot.Fingerprint)
	if err != nil || len(written.Rows) != 1 || written.Rows[0].Live || written.Rows[0].Worktrees != 0 {
		t.Fatalf("rewritten cache must record the fresh observation (no runtime, no linked worktrees): %+v, %v", written.Rows, err)
	}
}
