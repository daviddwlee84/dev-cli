package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type remoteListForge struct {
	publishTestForge
	rows []forge.RemoteRepo
	err  error
}

func (f remoteListForge) ListRepos(context.Context) ([]forge.RemoteRepo, error) {
	return f.rows, f.err
}

func saveRemoteTestCache(t *testing.T, app *App, repositories []forge.RemoteRepo) {
	t.Helper()
	now := time.Now().UTC()
	providers := map[forge.Kind]forge.ProviderStatus{}
	for _, repository := range repositories {
		providers[repository.Forge] = forge.ProviderStatus{FetchedAt: now, Complete: true}
	}
	if err := forge.SaveCacheState(remoteCachePath(), forge.Cache{
		Version: forge.CacheVersion, SourceID: remoteCacheSourceID(app),
		FetchedAt: now, Complete: true, Providers: providers, Repos: repositories,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteCacheSourceTracksForgeEndpoints(t *testing.T) {
	app := &App{Cfg: config.Default()}
	baseline := remoteCacheSourceID(app)
	app.Cfg.Forge.AzureDevOps = []config.AzureDevOpsTarget{{
		Organization: "https://dev.azure.com/acme", Project: "platform",
	}}
	if changed := remoteCacheSourceID(app); changed == baseline {
		t.Fatal("Azure target change did not change remote cache source identity")
	}
	t.Setenv("GH_HOST", "github.enterprise.example")
	if changed := remoteCacheSourceID(&App{Cfg: config.Default()}); changed == baseline {
		t.Fatal("GH_HOST change did not change remote cache source identity")
	}
}

func TestAutomaticRemoteCacheRejectsDifferentSource(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	app := &App{Cfg: config.Default()}
	now := time.Now().UTC()
	if err := forge.SaveCacheState(remoteCachePath(), forge.Cache{
		Version: forge.CacheVersion, SourceID: "different-source",
		FetchedAt: now, Complete: true,
		Repos: []forge.RemoteRepo{{Forge: forge.GitHub, Name: "private", FullName: "owner/private"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := cachedRemoteRows(app); found {
		t.Fatal("automatic TUI seed accepted a cache from another source")
	}
	if _, _, found := cachedRemotesAny(t.Context(), app); found {
		t.Fatal("explicit cached path accepted a non-legacy cache from another source")
	}
}

func TestCollectRemotesMatchesSuppliedRowsWithoutDiscovery(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	app := &App{Cfg: config.Default()}
	local := tui.RepoRow{
		Repo:        repo.Repo{Name: "api", Path: "/src/api"},
		RemoteForge: forge.GitHub, RemoteName: "owner/api",
	}
	rows, err := collectRemotesWithOptions(t.Context(), app, remoteCollectOptions{
		Locals: []tui.RepoRow{local}, LocalsSet: true, ProvidersSet: true,
		Providers: []forge.Forge{remoteListForge{rows: []forge.RemoteRepo{{
			Forge: forge.GitHub, Name: "api", FullName: "owner/api",
		}}}},
	})
	if err != nil || len(rows) != 1 || rows[0].LocalPath != "/src/api" {
		t.Fatalf("matched rows=%+v err=%v", rows, err)
	}
}

func TestCollectRemotesPersistsSuccessfulEmptySnapshot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	app := &App{Cfg: config.Default()}
	old := forge.RemoteRepo{Forge: forge.GitHub, Name: "old", FullName: "owner/old"}
	saveRemoteTestCache(t, app, []forge.RemoteRepo{old})

	rows, err := collectRemotesWithOptions(t.Context(), app, remoteCollectOptions{
		LocalsSet: true, ProvidersSet: true,
		Providers: []forge.Forge{remoteListForge{}},
	})
	if err != nil || len(rows) != 0 {
		t.Fatalf("refresh rows=%+v err=%v", rows, err)
	}
	cached, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok || !cached.Complete || len(cached.Repos) != 0 {
		t.Fatalf("empty success was not persisted: %+v, ok=%v", cached, ok)
	}
}

func TestCollectRemotesRetainsCacheWhenEveryProviderFails(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	app := &App{Cfg: config.Default()}
	old := forge.RemoteRepo{Forge: forge.GitHub, Name: "old", FullName: "owner/old"}
	saveRemoteTestCache(t, app, []forge.RemoteRepo{old})

	rows, err := collectRemotesWithOptions(t.Context(), app, remoteCollectOptions{
		LocalsSet: true, ProvidersSet: true,
		Providers: []forge.Forge{remoteListForge{err: errors.New("provider unavailable")}},
	})
	if err == nil || len(rows) != 1 || rows[0].Repo.FullName != old.FullName {
		t.Fatalf("failed refresh rows=%+v err=%v", rows, err)
	}
	cached, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok || len(cached.Repos) != 1 || cached.Repos[0].FullName != old.FullName || !cached.Complete {
		t.Fatalf("failed refresh replaced prior cache: %+v, ok=%v", cached, ok)
	}
}

func TestFleetSnapshotReusesRowsAndExcludesActiveTries(t *testing.T) {
	repository := tui.RepoRow{Repo: repo.Repo{Name: "api", Path: "/src/api", RealPath: "/src/api"}}
	try := tui.RepoRow{
		Repo:  repo.Repo{Name: "scratch", Path: "/src/tries/scratch", RealPath: "/src/tries/scratch"},
		Asset: &catalog.Entry{Kind: catalog.KindTry, Experiment: &catalog.Experiment{Phase: catalog.PhaseActive}},
	}
	snapshot := fleetSnapshotFromRepoRows([]tui.RepoRow{repository, try}, "none")
	if snapshot.Runtime != "none" || len(snapshot.Repositories) != 1 || snapshot.Repositories[0].Name != "api" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestTUIRemoteCloneDestinationStaysInsideProjectRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	cfg := config.Default()
	cfg.Paths.ProjectRoot = root
	app := &App{Cfg: cfg}

	destination, err := tuiRemoteCloneDestination(app, "safe-repo")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if destination != filepath.Join(canonicalRoot, "safe-repo") {
		t.Fatalf("destination = %q", destination)
	}
	if _, err := tuiRemoteCloneDestination(app, "../outside"); err == nil {
		t.Fatal("parent traversal repository name was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !os.IsNotExist(err) {
		t.Fatalf("unsafe destination was created: %v", err)
	}
}

func TestCanceledRemoteGenerationCannotCommitCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	app := &App{Cfg: config.Default()}
	old := forge.RemoteRepo{Forge: forge.GitHub, Name: "old", FullName: "owner/old"}
	saveRemoteTestCache(t, app, []forge.RemoteRepo{old})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = collectRemotesWithOptions(ctx, app, remoteCollectOptions{
		LocalsSet: true, ProvidersSet: true,
		Providers: []forge.Forge{remoteListForge{rows: []forge.RemoteRepo{{
			Forge: forge.GitHub, Name: "new", FullName: "owner/new",
		}}}},
	})
	cached, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok || len(cached.Repos) != 1 || cached.Repos[0].FullName != old.FullName {
		t.Fatalf("canceled generation committed cache: %+v, ok=%v", cached, ok)
	}
}
