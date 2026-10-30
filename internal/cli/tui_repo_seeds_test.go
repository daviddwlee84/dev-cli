package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestTUIRepoCatalogSeedsRespectHostAndDiscoveryScope(t *testing.T) {
	root := t.TempDir()
	app := loaderTestApp(t, root)
	for _, name := range []string{"direct", "group/team/repo", "one/two/three/too-deep", ".hidden/repo", "node_modules/repo"} {
		loaderFavorite(t, app, syntheticLoaderRow(root, name, true).Repo)
	}
	loaderFavorite(t, app, syntheticLoaderRow(t.TempDir(), "outside", true).Repo)
	foreign := loaderFavorite(t, app, syntheticLoaderRow(root, "foreign", true).Repo)
	if _, err := app.Catalog.Update(foreign.ID, func(entry *catalog.Entry) error {
		location := entry.Locations[configHostname()]
		entry.Locations = map[string]catalog.Location{configHostname() + "-other": location}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	metadata, err := readTUIRepoCatalog(t.Context(), app)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, seed := range metadata.seeds {
		got[seed.Repo.Path] = true
	}
	if len(got) != 2 || !got[filepath.Join(root, "direct")] || !got[filepath.Join(root, "group/team/repo")] {
		t.Fatalf("favorite scope expanded discovery: %+v", got)
	}
}

func TestTUIRepoCatalogReportsAmbiguousMetadata(t *testing.T) {
	root := t.TempDir()
	app := loaderTestApp(t, root)
	r := syntheticLoaderRow(root, "duplicate", true).Repo
	loaderFavorite(t, app, r)
	loaderFavorite(t, app, r)
	metadata, err := readTUIRepoCatalog(t.Context(), app)
	if err == nil || metadata.asset(r) != nil {
		t.Fatalf("ambiguous catalog treated as complete: asset=%+v err=%v", metadata.asset(r), err)
	}
}

func TestTUIRepoCatalogSeedsRespectSymlinkDiscoveryPolicy(t *testing.T) {
	physical := t.TempDir()
	index := t.TempDir()
	alias := filepath.Join(index, "api")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	app := loaderTestApp(t, index)
	loaderFavorite(t, app, repo.Repo{Name: "api", Path: physical, RealPath: physical})
	metadata, err := readTUIRepoCatalog(t.Context(), app)
	if err != nil || len(metadata.seeds) != 1 {
		t.Fatalf("direct alias seed = %+v, %v", metadata.seeds, err)
	}
	container := filepath.Join(index, "container")
	if err := os.MkdirAll(filepath.Join(container, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(alias, filepath.Join(container, "nested")); err != nil {
		t.Fatal(err)
	}
	metadata, err = readTUIRepoCatalog(t.Context(), app)
	if err != nil || len(metadata.seeds) != 0 {
		t.Fatalf("nested hidden alias entered discovery: %+v, %v", metadata.seeds, err)
	}
	linkRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(physical, linkRoot); err != nil {
		t.Fatal(err)
	}
	app.Cfg.Paths.ScanRoots = []string{linkRoot}
	loaderFavorite(t, app, syntheticLoaderRow(physical, "child", true).Repo)
	metadata, err = readTUIRepoCatalog(t.Context(), app)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range metadata.seeds {
		if seed.Repo.Name == "child" {
			t.Fatal("symlink-root descendants became favorite seeds")
		}
	}
}

func TestTUIFavoriteTargetRejectsChangedIdentityAndAcceptsPlainDirectory(t *testing.T) {
	first, second := gittest.New(t), gittest.New(t)
	expected := loaderDiscoveredRepo(t, first.Root)
	expected.CommonDir = loaderDiscoveredRepo(t, second.Root).CommonDir
	if _, err := discoverTUIFavorite(t.Context(), expected); err == nil {
		t.Fatal("favorite accepted changed Git common directory")
	}
	plain := t.TempDir()
	actual, err := discoverTUIFavorite(t.Context(), repo.Repo{Name: "plain", Path: plain, RealPath: plain})
	if err != nil || actual.HasGit {
		t.Fatalf("plain target = %+v, %v", actual, err)
	}
}

func TestTUIFavoriteTargetPreservesPlainSubdirectoryAndRejectsBrokenGit(t *testing.T) {
	r := gittest.New(t)
	plain := filepath.Join(r.Root, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	actual, err := discoverTUIFavorite(t.Context(), repo.Repo{Name: "plain", Path: plain, RealPath: plain})
	if err != nil || actual.HasGit || actual.CommonDir != "" || actual.Path != plain {
		t.Fatalf("plain target acquired ancestor identity: %+v, %v", actual, err)
	}
	if err := os.WriteFile(filepath.Join(plain, ".git"), []byte("not a Git file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverTUIFavorite(t.Context(), repo.Repo{Name: "plain", Path: plain}); err == nil {
		t.Fatal("broken Git metadata downgraded to plain directory")
	}
}

func TestTUIFavoriteTargetSupportsBareHub(t *testing.T) {
	path := t.TempDir()
	if _, err := gitx.Run(t.Context(), path, "init", "--bare"); err != nil {
		t.Fatal(err)
	}
	actual, err := discoverTUIFavorite(t.Context(), repo.Repo{Name: "hub", Path: path, RealPath: path, CommonDir: path, HasGit: true})
	if err != nil || !actual.Bare || !actual.HasGit || actual.CommonDir != canonicalRepoValue(path) {
		t.Fatalf("bare target = %+v, %v", actual, err)
	}
}

func TestTUIRepoStreamKeepsDiscoveryAliasWithoutDoubleEnrichment(t *testing.T) {
	r := gittest.New(t)
	physical := loaderDiscoveredRepo(t, r.Root)
	index := t.TempDir()
	aliasPath := filepath.Join(index, "human-alias")
	if err := os.Symlink(physical.Path, aliasPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	alias := physical
	alias.Path, alias.Name, alias.Root, alias.Symlink = aliasPath, "human-alias", index, true
	seed := tui.RepoRow{Repo: physical, Asset: &catalog.Entry{Favorite: true}}
	calls := 0
	rows, err := collectTUIRepoStream(t.Context(), tuiRepoCatalog{seeds: []tui.RepoRow{seed}}, readyLoaderFuture([]repo.Repo{alias, physical}, nil),
		readyLoaderFuture(tuiRuntimeSnapshot{runtime: runtime.None{}}, nil), inventory.NewLimiter(8), discoverTUIFavorite,
		func(_ context.Context, row tui.RepoRow) tui.RepoRow { calls++; row.GitKnown = true; return row }, nil, func(string, tui.RepoRow) {})
	if err != nil || len(rows) != 1 || calls != 1 || rows[0].Repo.Path != aliasPath || !rows[0].Favorite() {
		t.Fatalf("alias dedup rows=%+v calls=%d err=%v", rows, calls, err)
	}
}
