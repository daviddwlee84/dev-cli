package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestRepositoryFavoritePersistsWithoutLosingMetadata(t *testing.T) {
	repository := gittest.New(t)
	git, err := gitx.Discover(t.Context(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	store := catalog.NewStore(filepath.Join(t.TempDir(), "assets"))
	app := &App{Catalog: store, Registry: catalog.NewRegistry(store)}
	row := tui.RepoRow{Repo: repo.Repo{Name: "test", Path: repository.Root, RealPath: git.Root, CommonDir: git.GitCommonDir, HasGit: true}}
	entry, err := setRepositoryFavorite(t.Context(), app, row, true)
	if err != nil || entry == nil || !entry.Favorite {
		t.Fatalf("favorite: %+v, %v", entry, err)
	}
	entry, err = app.Registry.Patch(entry.ID, func(e *catalog.Entry) error {
		e.Note, e.Tags = "keep this note", []string{"work"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	row.Asset = entry
	// A new store/registry observes durable intent, not a TUI/cache flag.
	app.Catalog = catalog.NewStore(store.Dir)
	app.Registry = catalog.NewRegistry(app.Catalog)
	loaded, err := app.Registry.Get(entry.ID)
	if err != nil || !loaded.Favorite {
		t.Fatalf("favorite did not survive reopen: %+v, %v", loaded, err)
	}
	entry, err = setRepositoryFavorite(t.Context(), app, row, false)
	if err != nil || entry.Favorite || entry.Note != "keep this note" || !entry.HasTag("work") {
		t.Fatalf("unfavorite lost metadata: %+v, %v", entry, err)
	}
	loaded, err = catalog.NewStore(store.Dir).Get(entry.ID)
	if err != nil || loaded.Favorite {
		t.Fatalf("false favorite did not persist: %+v, %v", loaded, err)
	}
}

func TestRepositoryFavoriteMissingKnownAssetCanBeRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	store := catalog.NewStore(filepath.Join(t.TempDir(), "assets"))
	app := &App{Catalog: store, Registry: catalog.NewRegistry(store)}
	entry := &catalog.Entry{Kind: catalog.KindRepository, Name: "missing", Favorite: true, Locations: map[string]catalog.Location{
		config.Hostname(): {State: catalog.LocationPresent, CurrentPath: path, RealPath: path},
	}}
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	row := tui.RepoRow{Repo: repo.Repo{Name: "missing", Path: path}, Asset: entry, Pending: "unavailable", Unavailable: "missing"}
	got, err := setRepositoryFavorite(t.Context(), app, row, false)
	if err != nil || got.Favorite {
		t.Fatalf("remove missing favorite: %+v, %v", got, err)
	}
}

func TestRepositoryFavoriteRejectsStaleUncatalogedTarget(t *testing.T) {
	store := catalog.NewStore(filepath.Join(t.TempDir(), "assets"))
	app := &App{Catalog: store, Registry: catalog.NewRegistry(store)}
	path := t.TempDir()
	row := tui.RepoRow{Repo: repo.Repo{Name: "gone-git", Path: path, HasGit: true}}
	if _, err := setRepositoryFavorite(t.Context(), app, row, true); err == nil {
		t.Fatal("missing Git identity became a plain-directory favorite")
	}
	entries, err := store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed validation created catalog entries: %+v, %v", entries, err)
	}
	row.Repo.HasGit = false
	entry, err := setRepositoryFavorite(t.Context(), app, row, true)
	if err != nil || !entry.Favorite {
		t.Fatalf("explicit plain-directory favorite: %+v, %v", entry, err)
	}
}

func TestRepositoryFavoriteHonorsCancellation(t *testing.T) {
	store := catalog.NewStore(filepath.Join(t.TempDir(), "assets"))
	app := &App{Catalog: store, Registry: catalog.NewRegistry(store)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := setRepositoryFavorite(ctx, app, tui.RepoRow{Repo: repo.Repo{Name: "plain", Path: t.TempDir()}}, true)
	if err == nil {
		t.Fatal("canceled favorite was accepted")
	}
	if _, err := os.Stat(store.Dir); !os.IsNotExist(err) {
		t.Fatalf("canceled favorite touched the store: %v", err)
	}
}
