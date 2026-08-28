package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestConfiguredForgesIncludesAzureDevOpsTargets(t *testing.T) {
	app := &App{Cfg: config.Default()}
	if got := configuredForges(app); len(got) != 2 {
		t.Fatalf("default providers = %d, want 2", len(got))
	}
	app.Cfg.Forge.AzureDevOps = []config.AzureDevOpsTarget{{
		Organization: "https://dev.azure.com/acme", Project: "Platform",
	}}
	got := configuredForges(app)
	if len(got) != 3 || got[2].Kind() != forge.AzureDevOps {
		t.Fatalf("configured providers = %+v", got)
	}
}

func TestAzureDevOpsRemoteMatchingTreatsHTTPSAndSSHAsSameRepo(t *testing.T) {
	repository := gittest.New(t)
	repository.Git("remote", "add", "origin", "git@ssh.dev.azure.com:v3/acme/Platform/api")
	app := &App{Cfg: config.Default()}
	app.Cfg.Paths.ScanRoots = []string{filepath.Dir(repository.Root)}
	rows := matchRemoteLocals(t.Context(), app, []forge.RemoteRepo{{
		Forge: forge.AzureDevOps, Name: "api", FullName: "acme/Platform/api",
		CloneURL: "https://dev.azure.com/acme/Platform/_git/api",
	}})
	if len(rows) != 1 || rows[0].LocalPath != repository.Root {
		t.Fatalf("Azure remote match = %+v", rows)
	}
}

func newRepoCatalogCollectorFixture(t *testing.T) (*App, *catalog.Entry, string) {
	t.Helper()
	physicalRoot := t.TempDir()
	repository := gittest.New(t)
	repository.Git("remote", "add", "origin", "https://github.com/owner/catalog-try.git")
	physical := filepath.Join(physicalRoot, "catalog-try")
	if err := os.Rename(repository.Root, physical); err != nil {
		t.Fatal(err)
	}
	scanRoot := t.TempDir()
	alias := filepath.Join(scanRoot, "catalog-try")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	discovered, err := gitx.Discover(context.Background(), physical)
	if err != nil {
		t.Fatal(err)
	}
	store := catalog.NewStore(filepath.Join(t.TempDir(), "assets"),
		catalog.WithIDGenerator(func() string { return "collector-try" }))
	entry := &catalog.Entry{
		Kind: catalog.KindTry, Name: "catalog-try",
		RemoteIdentity: catalog.NormalizeRemoteIdentity("https://github.com/owner/catalog-try.git"),
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive, Slug: "catalog-try", Started: time.Now().UTC(),
			OriginURL: "https://github.com/owner/catalog-try.git", OriginalPath: physical,
		},
		Locations: map[string]catalog.Location{
			config.Hostname(): {
				State: catalog.LocationPresent, CurrentPath: physical, RealPath: physical,
				GitCommonDir: discovered.GitCommonDir,
			},
		},
	}
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{scanRoot}
	app := &App{
		Cfg: cfg, Tasks: task.NewStore(filepath.Join(t.TempDir(), "tasks")),
		Catalog: store, Registry: catalog.NewRegistry(store),
		Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
	}
	return app, entry, alias
}

func TestCollectReposSuppressesCanonicalTryWithOptInAndCorruptFallback(t *testing.T) {
	app, entry, alias := newRepoCatalogCollectorFixture(t)
	rows, err := collectRepos(context.Background(), app, runtime.None{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("default collector exposed active Try through symlink: %+v", rows)
	}
	rows, err = collectReposWithOptions(context.Background(), app, runtime.None{}, repoCollectOptions{IncludeTries: true})
	if err != nil || len(rows) != 1 || rows[0].Asset == nil || rows[0].Asset.ID != entry.ID || rows[0].Repo.Path != alias {
		t.Fatalf("opt-in collector = %+v, %v", rows, err)
	}

	if err := os.WriteFile(filepath.Join(app.Catalog.Dir, "broken.toml"), []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err = collectRepos(context.Background(), app, runtime.None{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("incomplete catalog should leave row visible: %+v, %v", rows, err)
	}
	if warnings := app.Err.(*bytes.Buffer).String(); !strings.Contains(warnings, "broken.toml") {
		t.Fatalf("catalog warning missing: %q", warnings)
	}
}

func TestRemoteMatchingDoesNotChooseBetweenDuplicateClones(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := gitx.Run(context.Background(), path, "init", "-b", "main"); err != nil {
			t.Fatal(err)
		}
		if _, err := gitx.Run(context.Background(), path, "remote", "add", "origin", "https://github.com/owner/shared.git"); err != nil {
			t.Fatal(err)
		}
	}
	store := catalog.NewStore(filepath.Join(t.TempDir(), "assets"))
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{root}
	app := &App{
		Cfg: cfg, Tasks: task.NewStore(filepath.Join(t.TempDir(), "tasks")),
		Catalog: store, Registry: catalog.NewRegistry(store),
		Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
	}
	rows := matchRemoteLocals(context.Background(), app, []forge.RemoteRepo{{
		Forge: forge.GitHub, Name: "shared", FullName: "owner/shared",
		CloneURL: "https://github.com/owner/shared.git",
	}})
	if len(rows) != 1 || rows[0].LocalPath != "" || rows[0].LocalKind != "" {
		t.Fatalf("ambiguous local clones were resolved arbitrarily: %+v", rows)
	}
}

func TestCollectReposIncludesGraduatedEntryAndRemoteKindUsesAllRepos(t *testing.T) {
	app, entry, _ := newRepoCatalogCollectorFixture(t)
	if _, err := app.Registry.Update(entry.ID, func(candidate *catalog.Entry) error {
		candidate.Kind = catalog.KindRepository
		candidate.Experiment.Phase = catalog.PhaseGraduated
		candidate.Experiment.GraduatedAt = time.Now().UTC()
		candidate.Experiment.GraduatedPath = candidate.Locations[config.Hostname()].CurrentPath
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := collectRepos(context.Background(), app, runtime.None{})
	if err != nil || len(rows) != 1 || rows[0].Asset == nil || rows[0].Asset.Kind != catalog.KindRepository {
		t.Fatalf("graduated row = %+v, %v", rows, err)
	}

	// Return the entry to Try state to verify REMOTE scans all repositories even
	// though the default REPOS collector suppresses it.
	if _, err := app.Registry.Update(entry.ID, func(candidate *catalog.Entry) error {
		candidate.Kind = catalog.KindTry
		candidate.Experiment.Phase = catalog.PhaseActive
		candidate.Experiment.GraduatedAt = time.Time{}
		candidate.Experiment.GraduatedPath = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	remote := forge.RemoteRepo{
		Forge: forge.GitHub, Name: "catalog-try", FullName: "owner/catalog-try",
		CloneURL: "https://github.com/owner/catalog-try.git",
	}
	matched := matchRemoteLocals(context.Background(), app, []forge.RemoteRepo{remote})
	if len(matched) != 1 || matched[0].LocalPath == "" || matched[0].LocalKind != catalog.KindTry {
		t.Fatalf("live remote local kind = %+v", matched)
	}
}
