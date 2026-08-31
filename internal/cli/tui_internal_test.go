package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
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

func TestOpenOrCDReportsFailedRuntimeHandoff(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	app := &App{Cfg: config.Default(), Out: io.Discard, Err: io.Discard}
	app.Cfg.Runtime.Backend = "tmux"
	err := openOrCD(app, context.Background(), t.TempDir(), "try")
	if err == nil || !strings.Contains(err.Error(), "open runtime session") {
		t.Fatalf("failed runtime open should return its error, got %v", err)
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
