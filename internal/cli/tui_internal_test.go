package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestAppLoadInitializesCatalog(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	var diagnostics bytes.Buffer
	app := &App{
		Out:        io.Discard,
		Err:        &diagnostics,
		configPath: filepath.Join(t.TempDir(), "missing.toml"),
	}
	if err := app.Load(); err != nil {
		t.Fatal(err)
	}
	if app.Tasks == nil || app.Catalog == nil || app.Registry == nil || app.Sizes == nil {
		t.Fatalf("stores were not initialized: %+v", app)
	}
	if got, want := app.Catalog.Dir, filepath.Join(dataHome, "dev", "assets"); got != want {
		t.Errorf("catalog dir = %q, want %q", got, want)
	}
	if app.Registry.Store() != app.Catalog {
		t.Error("registry should use App's catalog store")
	}
	if app.Sizes.Cache == nil || app.Sizes.Cache.Path != filepath.Join(config.CacheHome(), "dev", "sizes-v1.json") {
		t.Fatalf("size manager cache = %+v", app.Sizes.Cache)
	}

	if err := os.MkdirAll(app.Catalog.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.Catalog.Dir, "broken.toml"), []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Catalog.List(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "dev: warning: skipping broken.toml") {
		t.Errorf("catalog diagnostics did not use App.Err: %q", diagnostics.String())
	}
}

func TestTUIRepoNewProcessPreservesConfigAndStaysInDashboard(t *testing.T) {
	app := &App{configPath: "/tmp/dev-config.toml", scaffoldsPath: "/tmp/dev-scaffolds.toml"}
	process, err := tuiRepoNewProcess(app)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(process.Args[1:], " ")
	want := "--config /tmp/dev-config.toml --scaffolds /tmp/dev-scaffolds.toml repo new --handoff stay"
	if got != want {
		t.Fatalf("repo new process args = %q, want %q", got, want)
	}
}

func TestCloneRemoteFromTUIUsesRepositoryAcquire(t *testing.T) {
	source := gittest.New(t)
	projectRoot := filepath.Join(t.TempDir(), "projects")
	cfg := config.Default()
	cfg.Paths.ProjectRoot = projectRoot
	cfg.Paths.ScanRoots = []string{projectRoot}
	app := &App{Cfg: cfg}
	row := tui.RemoteRow{Repo: forge.RemoteRepo{
		Forge: forge.GitHub, Name: "copy", FullName: "owner/copy", CloneURL: source.Root,
	}}

	path, err := cloneRemoteFromTUI(t.Context(), app, row)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRoot, "copy")
	if path != want {
		t.Fatalf("clone path = %q, want %q", path, want)
	}
	body, err := os.ReadFile(filepath.Join(path, "README.md"))
	if err != nil || string(body) != "# test repo\n" {
		t.Fatalf("cloned README = %q, %v", body, err)
	}
	problem, err := cloneRemoteFromTUI(t.Context(), app, row)
	if err == nil || !strings.Contains(err.Error(), "already exists") || problem != want {
		t.Fatalf("existing destination result = %q, %v", problem, err)
	}
}

func TestCloneRemoteFromTUIRejectsNestedRepository(t *testing.T) {
	source := gittest.New(t)
	other := gittest.New(t)
	cfg := config.Default()
	cfg.Paths.ProjectRoot = filepath.Join(other.Root, "projects")
	cfg.Paths.ScanRoots = []string{cfg.Paths.ProjectRoot}
	app := &App{Cfg: cfg}
	row := tui.RemoteRow{Repo: forge.RemoteRepo{
		Forge: forge.GitHub, Name: "copy", FullName: "owner/copy", CloneURL: source.Root,
	}}

	if _, err := cloneRemoteFromTUI(t.Context(), app, row); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("nested clone error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Paths.ProjectRoot, "copy")); !os.IsNotExist(err) {
		t.Fatalf("nested clone destination was created: %v", err)
	}
}

func TestCloneRemoteFromTUIRequiresDiscoverableProjectRoot(t *testing.T) {
	source := gittest.New(t)
	projectRoot := filepath.Join(t.TempDir(), "projects")
	cfg := config.Default()
	cfg.Paths.ProjectRoot = projectRoot
	cfg.Paths.ScanRoots = []string{t.TempDir()}
	cfg.Paths.RepoPaths = nil
	app := &App{Cfg: cfg}
	row := tui.RemoteRow{Repo: forge.RemoteRepo{
		Forge: forge.GitHub, Name: "copy", FullName: "owner/copy", CloneURL: source.Root,
	}}

	if _, err := cloneRemoteFromTUI(t.Context(), app, row); err == nil || !strings.Contains(err.Error(), "outside paths.scan_roots") {
		t.Fatalf("undiscoverable project root error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "copy")); !os.IsNotExist(err) {
		t.Fatalf("undiscoverable clone destination was created: %v", err)
	}
}

func TestOpenOrCDReportsFailedRuntimeHandoff(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	app := &App{Cfg: config.Default(), Out: io.Discard, Err: io.Discard}
	app.Cfg.Runtime.Backend = "tmux"
	err := openOrCD(app, context.Background(), t.TempDir(), "try")
	if err == nil || !strings.Contains(err.Error(), "open runtime session") {
		t.Fatalf("failed runtime open should return its error, got %v", err)
	}
}

func TestTUIStartDirectReplacesDoneGeneration(t *testing.T) {
	repository := gittest.New(t)
	store := task.NewStore(t.TempDir())
	completed := &task.Task{
		Name: "old", Repo: "repo", RepoPath: repository.Root, Branch: "main", Base: "main",
		Mode: task.ModeDirect, State: task.Done, Owner: config.Hostname(),
	}
	if err := store.Save(completed); err != nil {
		t.Fatal(err)
	}
	rt := &activityRuntime{openResult: runtime.OpenResult{Handle: "w7", Opened: true, Created: true}}
	app := &App{Cfg: config.Default(), Tasks: store, Out: io.Discard, Err: io.Discard, runtimeInstance: rt}
	row := tui.RepoRow{Repo: repo.Repo{Name: "repo", Path: repository.Root, RealPath: repository.Root, HasGit: true}}
	if _, err := startDirectFromTUI(t.Context(), app, rt, row, "new direct task"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(completed.ID)
	if err != nil || stored.Name != "new direct task" || stored.State != task.Hot {
		t.Fatalf("TUI direct replacement = %+v, %v", stored, err)
	}
}

func TestCollectReposPreservesTaskAndRuntimeInventoryErrors(t *testing.T) {
	repository := gittest.New(t)
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.ScanRoots = nil
	cfg.Paths.RepoPaths = []string{repository.Root}
	cfg.Paths.StateDir = filepath.Join(root, "state")
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "broken.toml"), []byte("not = valid = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	assets := catalog.NewStore(cfg.AssetsDir())
	runtimeFailure := errors.New("runtime inventory failed")
	rt := &activityRuntime{name: "herdr", listErr: runtimeFailure}
	app := &App{
		Cfg: cfg, Tasks: task.NewStore(tasksDir), Catalog: assets, Registry: catalog.NewRegistry(assets),
		Out: io.Discard, Err: io.Discard, runtimeInstance: rt,
	}
	rows, err := collectRepos(t.Context(), app, rt)
	if err != nil || len(rows) != 1 {
		t.Fatalf("collect repos = %d rows, %v", len(rows), err)
	}
	if rows[0].Context.TaskErr == nil || rows[0].Context.RuntimeErr == nil || !errors.Is(rows[0].Context.RuntimeErr, runtimeFailure) {
		t.Fatalf("inventory errors were dropped: task=%v runtime=%v", rows[0].Context.TaskErr, rows[0].Context.RuntimeErr)
	}
}

func TestTryTUIAdapterCreatesListsAndArchivesThroughService(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.TriesRoot = filepath.Join(root, "tries")
	cfg.Paths.ProjectRoot = filepath.Join(root, "projects")
	cfg.Paths.StateDir = filepath.Join(root, "state")
	store := catalog.NewStore(cfg.AssetsDir())
	app := &App{
		Cfg: cfg, Catalog: store, Registry: catalog.NewRegistry(store),
		Out: io.Discard, Err: io.Discard,
	}

	created, err := applyTryAction(context.Background(), app, runtime.None{}, tui.TryRequest{
		Action: tui.TryCreate, Name: "adapter", NoGit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.CD == "" || !created.RefreshRepos {
		t.Fatalf("create result = %+v", created)
	}
	rows, err := collectTries(context.Background(), app, runtime.None{}, false)
	if err != nil || len(rows) != 1 || rows[0].Item.ID == "" || !rows[0].Present() {
		t.Fatalf("collected Try rows = %+v, %v", rows, err)
	}

	archived, err := applyTryAction(context.Background(), app, runtime.None{}, tui.TryRequest{
		Action: tui.TryArchive, ID: rows[0].Item.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.RefreshRepos || archived.CD != "" {
		t.Fatalf("archive result = %+v", archived)
	}
	rows, err = collectTries(context.Background(), app, runtime.None{}, true)
	if err != nil || len(rows) != 1 || rows[0].LocationState() != catalog.LocationArchived || rows[0].Present() {
		t.Fatalf("archived Try rows = %+v, %v", rows, err)
	}
}

func TestExternalToolCommandModesAreExplicit(t *testing.T) {
	app := &App{Cfg: config.Default()}
	app.Cfg.TUI.Tools = []config.Tool{
		{Key: "N", Name: "normal", Run: "printf normal"},
		{Key: "I", Name: "interactive", Run: "my-shell-alias --flag", Interactive: true},
	}
	got := externalTools(app)
	if len(got) != 2 {
		t.Fatalf("got %d tools", len(got))
	}
	if len(got[0].Command) != 3 || got[0].Command[1] != "-c" || got[0].Command[2] != "printf normal" {
		t.Errorf("ordinary command = %v", got[0].Command)
	}
	if len(got[1].Command) != 5 || got[1].Command[1] != "-lic" ||
		got[1].Command[2] != `eval "$1"` || got[1].Command[4] != "my-shell-alias --flag" {
		t.Errorf("interactive command must evaluate after rc loading: %v", got[1].Command)
	}
}

func TestCommandRunnableFindsKnownExecutable(t *testing.T) {
	check := commandRunnable("go version", false)
	if !check(context.Background()) {
		t.Fatal("go should be available to the test process")
	}
}
