package experiment_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

type fixture struct {
	t        *testing.T
	root     string
	tries    string
	projects string
	store    *catalog.Store
	registry *catalog.Registry
	service  *experiment.Service
	now      *time.Time
}

func newFixture(t *testing.T, hooks experiment.Hooks, maxWorkers int) *fixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "dev test")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "dev test")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.test")
	root := t.TempDir()
	tries := filepath.Join(root, "tries")
	projects := filepath.Join(root, "projects")
	for _, directory := range []string{tries, projects} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, time.August, 27, 15, 30, 0, 0, time.UTC)
	sequence := 0
	store := catalog.NewStore(filepath.Join(root, "assets"),
		catalog.WithClock(func() time.Time { return now }),
		catalog.WithIDGenerator(func() string {
			sequence++
			return fmt.Sprintf("try-%03d", sequence)
		}))
	registry := catalog.NewRegistry(store)
	service, err := experiment.NewService(experiment.ServiceConfig{
		Registry: registry, Store: store,
		TriesRoot: tries, ProjectRoot: projects, Host: "test-host",
		Clock: func() time.Time { return now }, MaxEnrichment: maxWorkers,
		Hooks: hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t: t, root: root, tries: tries, projects: projects,
		store: store, registry: registry, service: service, now: &now,
	}
}

func (f *fixture) mkdir(name string) string {
	f.t.Helper()
	path := filepath.Join(f.tries, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=dev test",
		"GIT_AUTHOR_EMAIL=dev@example.test",
		"GIT_COMMITTER_NAME=dev test",
		"GIT_COMMITTER_EMAIL=dev@example.test",
		"GIT_AUTHOR_DATE=2026-08-22T10:00:00Z",
		"GIT_COMMITTER_DATE=2026-08-22T10:00:00Z",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), directory, err, output)
	}
	return strings.TrimSpace(string(output))
}

func initRepo(t *testing.T, directory string, commit bool) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "init", "--initial-branch=main")
	if !commit {
		return
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("# experiment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "add", "README.md")
	runGit(t, directory, "commit", "-m", "initial experiment")
}

func itemByBase(t *testing.T, items []experiment.Item, basename string) experiment.Item {
	t.Helper()
	for _, item := range items {
		if item.Basename == basename {
			return item
		}
	}
	t.Fatalf("no item %q in %+v", basename, items)
	return experiment.Item{}
}

func TestReconcileBackfillsGitAndNonGitDirectoriesIdempotently(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	gitPath := f.mkdir("2026-08-20-owner-repo")
	initRepo(t, gitPath, true)
	runGit(t, gitPath, "remote", "add", "origin", "git@github.com:owner/repo.git")
	plainPath := f.mkdir("plain-notes")
	hiddenPath := filepath.Join(f.tries, ".dev", "archive", "hidden")
	if err := os.MkdirAll(hiddenPath, 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(plainPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	first, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	if len(first) != 2 {
		t.Fatalf("List returned %d items, want 2", len(first))
	}

	gitItem := itemByBase(t, first, "2026-08-20-owner-repo")
	if gitItem.ID == "" || gitItem.Live.Repo == nil || gitItem.Live.Status == nil {
		t.Fatalf("Git item lacks durable/live facts: %+v", gitItem)
	}
	if got, want := gitItem.Name, "owner-repo"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := gitItem.OriginURL, "git@github.com:owner/repo.git"; got != want {
		t.Errorf("OriginURL = %q, want %q", got, want)
	}
	if got, want := gitItem.RemoteIdentity, "github.com/owner/repo"; got != want {
		t.Errorf("RemoteIdentity = %q, want %q", got, want)
	}
	wantStarted := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	if !gitItem.Created.Equal(wantStarted) || !gitItem.Started.Equal(wantStarted) {
		t.Errorf("dated timestamps = %s / %s, want %s", gitItem.Created, gitItem.Started, wantStarted)
	}
	canonicalGitPath, err := filepath.EvalSymlinks(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	location, ok := gitItem.Entry.LocationFor("test-host")
	if !ok || location.CurrentPath != canonicalGitPath || location.RealPath != canonicalGitPath || location.GitCommonDir == "" {
		t.Errorf("catalog location = %+v, %v", location, ok)
	}

	plainItem := itemByBase(t, first, "plain-notes")
	if plainItem.Live.Repo != nil || plainItem.Live.Status != nil {
		t.Errorf("plain directory was presented as Git: %+v", plainItem.Live)
	}
	if activity := plainItem.Activity(); !activity.IsZero() {
		t.Errorf("directory mtime leaked into activity: %s", activity)
	}
	if _, err := os.Stat(filepath.Join(plainPath, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backfill wrote inside non-Git Try: %v", err)
	}

	second, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("second List: %v, %+v", err, diagnostics)
	}
	if itemByBase(t, second, gitItem.Basename).ID != gitItem.ID ||
		itemByBase(t, second, plainItem.Basename).ID != plainItem.ID {
		t.Error("reconciliation changed stable IDs")
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("catalog entries = %d, %v", len(entries), err)
	}
}

func TestReconcileDoesNotUseRelativeOriginAsCrossHostIdentity(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	path := f.mkdir("relative-origin")
	initRepo(t, path, false)
	const origin = "repos/source.git"
	runGit(t, path, "remote", "add", "origin", origin)

	items, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("List = %+v, %v, %+v", items, err, diagnostics)
	}
	item := itemByBase(t, items, "relative-origin")
	if item.OriginURL != origin || item.RemoteIdentity != "" {
		t.Errorf("relative origin facts = %q / %q", item.OriginURL, item.RemoteIdentity)
	}
	entry, err := f.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Experiment.OriginURL != origin || entry.RemoteIdentity != "" {
		t.Errorf("relative origin catalog = %+v", entry)
	}
}

func TestReconcileRejectsSymlinkEscapesAndOutsideCatalogLocations(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.tries, "escape")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	items, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("outside symlink was inventoried as a Try: %+v", items)
	}
	if got := fmt.Sprint(diagnostics); !strings.Contains(got, "validate try location") {
		t.Errorf("outside symlink diagnostic = %q", got)
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside symlink was cataloged: %+v, %v", entries, err)
	}

	stale := &catalog.Entry{
		Kind: catalog.KindTry, Name: "outside",
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive, Slug: "outside", Started: *f.now, OriginalPath: outside,
		},
		Locations: map[string]catalog.Location{
			"test-host": {State: catalog.LocationPresent, CurrentPath: outside, RealPath: outside},
		},
	}
	if err := f.store.Create(stale); err != nil {
		t.Fatal(err)
	}
	items, _, err = f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(items) != 0 {
		t.Fatalf("outside catalog location remained eligible: %+v, %v", items, err)
	}
	if _, _, err := f.service.Resolve(context.Background(), "outside"); !errors.Is(err, experiment.ErrNotFound) {
		t.Errorf("outside catalog location resolved through legacy matching: %v", err)
	}
	history, _, err := f.service.List(context.Background(), experiment.ListOptions{IncludeNonPresent: true})
	if err != nil || len(history) != 1 || history[0].ID != stale.ID || history[0].Live.Present {
		t.Errorf("outside history = %+v, %v", history, err)
	}
}

func TestReconcileAttachesAUniqueRemoteAcrossHosts(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	remote := "git@github.com:owner/shared.git"
	original := &catalog.Entry{
		Kind: catalog.KindTry, Name: "shared",
		RemoteIdentity: catalog.NormalizeRemoteIdentity(remote),
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive, Slug: "shared", OriginURL: remote,
			Started: *f.now, OriginalPath: filepath.Join(f.root, "other-host", "shared"),
		},
		Locations: map[string]catalog.Location{
			"other-host": {
				State:       catalog.LocationPresent,
				CurrentPath: filepath.Join(f.root, "other-host", "shared"),
			},
		},
	}
	if err := f.store.Create(original); err != nil {
		t.Fatal(err)
	}
	path := f.mkdir("2026-08-27-shared")
	initRepo(t, path, false)
	runGit(t, path, "remote", "add", "origin", remote)

	items, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("List = %+v, %v, %+v", items, err, diagnostics)
	}
	item := itemByBase(t, items, filepath.Base(path))
	if item.ID != original.ID {
		t.Fatalf("remote attachment ID = %q, want %q", item.ID, original.ID)
	}
	entry, err := f.store.Get(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, ok := entry.LocationFor("test-host")
	if !ok || location.State != catalog.LocationPresent || !strings.HasSuffix(location.CurrentPath, filepath.Base(path)) {
		t.Errorf("attached location = %+v, %v", location, ok)
	}
}

func TestReconcileDoesNotConvertRepositoryOnRemoteHintAlone(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	remote := "git@github.com:owner/shared-project.git"
	repository := &catalog.Entry{
		Kind: catalog.KindRepository, Name: "shared-project",
		RemoteIdentity: catalog.NormalizeRemoteIdentity(remote),
		Locations: map[string]catalog.Location{
			"other-host": {
				State:       catalog.LocationPresent,
				CurrentPath: filepath.Join(f.root, "other-host", "shared-project"),
			},
		},
	}
	if err := f.store.Create(repository); err != nil {
		t.Fatal(err)
	}
	path := f.mkdir("2026-08-27-shared-project-try")
	initRepo(t, path, false)
	runGit(t, path, "remote", "add", "origin", remote)

	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(path))
	if item.ID == repository.ID {
		t.Fatal("remote hint converted an ordinary repository into a Try")
	}
	original, err := f.store.Get(repository.ID)
	if err != nil || original.Kind != catalog.KindRepository || original.Experiment != nil {
		t.Errorf("ordinary repository changed: %+v, %v", original, err)
	}
}

func TestReconcileReusesRepositoryRecordAtTryPath(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	path := f.mkdir("2026-08-27-owned-repo")
	initRepo(t, path, false)
	repository, err := gitx.Discover(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	entry := &catalog.Entry{
		Kind: catalog.KindRepository, Name: "owned-repo", Tags: []string{"keep"},
		Locations: map[string]catalog.Location{
			"test-host": {
				State: catalog.LocationPresent, CurrentPath: path,
				RealPath: path, GitCommonDir: repository.GitCommonDir,
			},
		},
	}
	if err := f.store.Create(entry); err != nil {
		t.Fatal(err)
	}

	items, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("List = %+v, %v, %+v", items, err, diagnostics)
	}
	item := itemByBase(t, items, filepath.Base(path))
	if item.ID != entry.ID || item.Kind != catalog.KindTry || item.Phase != catalog.PhaseActive {
		t.Fatalf("reconciled item = %+v", item)
	}
	updated, err := f.store.Get(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kind != catalog.KindTry || updated.Experiment == nil || !updated.HasTag("keep") {
		t.Errorf("repository identity/metadata was not preserved: %+v", updated)
	}
}

func TestReconcileReturnsPartialItemsForCorruptCatalogAndBrokenGit(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	f.mkdir("healthy")
	broken := f.mkdir("broken-git")
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.store.Dir, "corrupt.toml"), []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("partial List hid a valid directory: %+v", items)
	}
	brokenItem := itemByBase(t, items, "broken-git")
	if brokenItem.ID != "" || brokenItem.Live.DiscoverError == nil ||
		!errors.Is(brokenItem.CatalogError, catalog.ErrIncompleteCatalog) {
		t.Errorf("broken Git/catalog facts = %+v", brokenItem)
	}
	joined := fmt.Sprint(diagnostics)
	if !strings.Contains(joined, "corrupt.toml") || !strings.Contains(joined, "discover Git") {
		t.Errorf("diagnostics = %q", joined)
	}
}

func TestResolveUsesExactPrecedenceAndReportsDeterministicAmbiguity(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	for _, name := range []string{
		"2026-08-01-cache-alpha",
		"2026-08-02-cache-beta",
		"scratch",
	} {
		f.mkdir(name)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	alpha := itemByBase(t, items, "2026-08-01-cache-alpha")

	for label, ref := range map[string]string{
		"stable ID": alpha.ID,
		"basename":  alpha.Basename,
		"stripped":  "cache-alpha",
	} {
		resolved, _, err := f.service.Resolve(context.Background(), ref)
		if err != nil || resolved.ID != alpha.ID {
			t.Errorf("Resolve by %s = %+v, %v", label, resolved, err)
		}
	}

	_, _, err = f.service.Resolve(context.Background(), "CACHE")
	var ambiguous *experiment.AmbiguousError
	if !errors.Is(err, experiment.ErrAmbiguous) || !errors.As(err, &ambiguous) {
		t.Fatalf("ambiguous Resolve = %v", err)
	}
	if len(ambiguous.Candidates) != 2 ||
		ambiguous.Candidates[0].Basename != "2026-08-01-cache-alpha" ||
		ambiguous.Candidates[1].Basename != "2026-08-02-cache-beta" {
		t.Errorf("ambiguous candidates = %+v", ambiguous.Candidates)
	}

	_, _, err = f.service.Resolve(context.Background(), "missing")
	var notFound *experiment.NotFoundError
	if !errors.Is(err, experiment.ErrNotFound) || !errors.As(err, &notFound) {
		t.Errorf("missing Resolve = %v", err)
	}

	created, err := f.service.ResolveOrCreate(context.Background(), experiment.CreateRequest{Name: "001", NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Existing || !strings.HasSuffix(created.Path, "-001") {
		t.Errorf("name matched an unrelated ID substring: %+v", created)
	}

	scratch := itemByBase(t, items, "scratch")
	if err := os.RemoveAll(scratch.Live.CurrentPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.Resolve(context.Background(), "scratch"); !errors.Is(err, experiment.ErrNotFound) {
		t.Errorf("host-nonpresent Try should be excluded, got %v", err)
	}
}

func TestResolveSurfacesAmbiguousCatalogOwnership(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	path := f.mkdir("duplicate-owner")
	for range 2 {
		entry := &catalog.Entry{
			Kind: catalog.KindTry, Name: "duplicate-owner",
			Experiment: &catalog.Experiment{
				Phase: catalog.PhaseActive, Slug: "duplicate-owner",
				Started: *f.now, OriginalPath: path,
			},
			Locations: map[string]catalog.Location{
				"test-host": {State: catalog.LocationPresent, CurrentPath: path, RealPath: path},
			},
		}
		if err := f.store.Create(entry); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := f.service.Resolve(context.Background(), "duplicate-owner")
	var ambiguous *experiment.AmbiguousError
	if !errors.Is(err, experiment.ErrAmbiguous) || !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve with duplicate catalog owners = %v", err)
	}
	if len(ambiguous.Candidates) != 2 || ambiguous.Candidates[0].ID >= ambiguous.Candidates[1].ID {
		t.Errorf("ambiguous catalog candidates = %+v", ambiguous.Candidates)
	}
}

func TestTouchIsExplicitActivityAndGitCommitCanBeNewer(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	plain := f.mkdir("plain")
	mtime := time.Date(2040, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(plain, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "plain")
	if !item.Activity().IsZero() {
		t.Fatalf("activity before Touch = %s", item.Activity())
	}
	touched, err := f.service.Touch(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !touched.TouchedAt.Equal(*f.now) || !touched.Item.Activity().Equal(*f.now) {
		t.Errorf("Touch result = %+v", touched)
	}

	gitPath := f.mkdir("with-history")
	initRepo(t, gitPath, true)
	items, _, err = f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gitItem := itemByBase(t, items, "with-history")
	if gitItem.Live.LastCommit.IsZero() || gitItem.Activity().IsZero() {
		t.Fatalf("Git activity missing: %+v", gitItem.Live)
	}
	earlierOpen := gitItem.Live.LastCommit.Add(-time.Hour)
	if _, err := f.registry.Update(gitItem.ID, func(entry *catalog.Entry) error {
		entry.LastOpened = earlierOpen
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, _, err = f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gitItem = itemByBase(t, items, "with-history")
	if !gitItem.Activity().Equal(gitItem.Live.LastCommit) {
		t.Errorf("Activity = %s, want newer Git commit %s", gitItem.Activity(), gitItem.Live.LastCommit)
	}
	*f.now = gitItem.Live.LastCommit.Add(time.Hour)
	touchedGit, err := f.service.Touch(context.Background(), gitItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !touchedGit.Item.Activity().Equal(*f.now) {
		t.Errorf("Activity = %s, want newer explicit open %s", touchedGit.Item.Activity(), *f.now)
	}
}

func TestCreateNoGitAndInitWarningAreTracked(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	result, err := f.service.Create(context.Background(), experiment.CreateRequest{Name: "plain", NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Tracked || result.Item.ID == "" || result.Item.Live.Repo != nil {
		t.Errorf("no-git result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Path, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--no-git created metadata: %v", err)
	}

	initFailure := errors.New("injected git init failure")
	failing := newFixture(t, experiment.Hooks{
		GitRun: func(ctx context.Context, directory string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "init" {
				return "", initFailure
			}
			return gitx.Run(ctx, directory, args...)
		},
	}, 2)
	warned, err := failing.service.Create(context.Background(), experiment.CreateRequest{Name: "warning"})
	if err != nil {
		t.Fatal(err)
	}
	if !warned.Created || !warned.Tracked || !errors.Is(warned.InitWarning, initFailure) {
		t.Errorf("git-init warning result = %+v", warned)
	}
}

func TestCloneDefaultUsesOwnerRepoAndVersionsCollisions(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var cloneRefs []string
	var hook experiment.GitRunFunc
	hook = func(ctx context.Context, directory string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "clone" {
			cloneRefs = append(cloneRefs, args[1])
			destination := args[2]
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return "", err
			}
			if _, err := gitx.Run(ctx, destination, "init", "-b", "main"); err != nil {
				return "", err
			}
			if _, err := gitx.Run(ctx, destination, "remote", "add", "origin", args[1]); err != nil {
				return "", err
			}
			return "", nil
		}
		return gitx.Run(ctx, directory, args...)
	}
	f := newFixture(t, experiment.Hooks{GitRun: hook}, 2)
	request := experiment.CreateRequest{Clone: "https://github.com/owner/repo.git"}
	first, err := f.service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	third, err := f.service.Create(context.Background(), experiment.CreateRequest{Clone: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(first.Path); got != "2026-08-27-owner-repo" {
		t.Errorf("first clone basename = %q", got)
	}
	if got := filepath.Base(second.Path); got != "2026-08-27-owner-repo-2" {
		t.Errorf("second clone basename = %q", got)
	}
	if got := filepath.Base(third.Path); got != "2026-08-27-owner-repo-3" {
		t.Errorf("third clone basename = %q", got)
	}
	if first.Item.ID == second.Item.ID || second.Item.ID == third.Item.ID ||
		!first.Tracked || !second.Tracked || !third.Tracked {
		t.Errorf("clone results = %+v / %+v / %+v", first, second, third)
	}
	if len(cloneRefs) != 3 || cloneRefs[0] != "https://github.com/owner/repo.git" ||
		cloneRefs[2] != "https://github.com/owner/repo.git" {
		t.Errorf("normalized clone refs = %v", cloneRefs)
	}
}

func TestCreateVersionsPastDanglingSymlinkCollision(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	reserved := filepath.Join(f.tries, "2026-08-27-reserved")
	target := filepath.Join(f.tries, "future-target")
	if err := os.Symlink(target, reserved); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := f.service.Create(context.Background(), experiment.CreateRequest{Name: "reserved", NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(result.Path); got != "2026-08-27-reserved-2" {
		t.Errorf("created path = %q, want collision-safe version", got)
	}
	if linkTarget, err := os.Readlink(reserved); err != nil || linkTarget != target {
		t.Errorf("reserved symlink = %q, %v", linkTarget, err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dangling target was populated: %v", err)
	}
}

func TestCloneRefsExpandLocalPathsAndPreserveSCPRemotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	localSource := filepath.Join(home, "source.git")
	if err := os.Mkdir(localSource, 0o755); err != nil {
		t.Fatal(err)
	}

	var cloneRefs []string
	hook := func(ctx context.Context, directory string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "clone" {
			cloneRefs = append(cloneRefs, args[1])
			destination := args[2]
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return "", err
			}
			if _, err := gitx.Run(ctx, destination, "init", "-b", "main"); err != nil {
				return "", err
			}
			if _, err := gitx.Run(ctx, destination, "remote", "add", "origin", args[1]); err != nil {
				return "", err
			}
			return "", nil
		}
		return gitx.Run(ctx, directory, args...)
	}
	f := newFixture(t, experiment.Hooks{GitRun: hook}, 2)
	if _, err := f.service.Create(context.Background(), experiment.CreateRequest{Clone: "~/source.git"}); err != nil {
		t.Fatal(err)
	}
	scp := "alice@example.com:Team/Repo.git"
	if _, err := f.service.Create(context.Background(), experiment.CreateRequest{Clone: scp}); err != nil {
		t.Fatal(err)
	}
	if len(cloneRefs) != 2 || cloneRefs[0] != localSource || cloneRefs[1] != scp {
		t.Errorf("normalized clone refs = %v", cloneRefs)
	}
}

func TestCreateFailureStatesPreserveFilesystemAndDoNotPretendTracked(t *testing.T) {
	cloneFailure := errors.New("clone failed after writing")
	f := newFixture(t, experiment.Hooks{
		GitRun: func(_ context.Context, _ string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "clone" {
				destination := args[2]
				if err := os.MkdirAll(destination, 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(filepath.Join(destination, "partial.data"), []byte("keep"), 0o644); err != nil {
					return "", err
				}
				return "", cloneFailure
			}
			return "", nil
		},
	}, 2)
	failedClone, err := f.service.Create(context.Background(), experiment.CreateRequest{
		Clone: "https://github.com/owner/repo.git",
	})
	if !errors.Is(err, cloneFailure) || failedClone.Created || failedClone.Tracked {
		t.Fatalf("failed clone result = %+v, %v", failedClone, err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(failedClone.Path, "partial.data")); readErr != nil || string(contents) != "keep" {
		t.Errorf("partial clone data was removed: %q, %v", contents, readErr)
	}
	entries, listErr := f.store.List()
	if listErr != nil || len(entries) != 0 {
		t.Errorf("failed clone was cataloged: %+v, %v", entries, listErr)
	}

	catalogFailure := errors.New("catalog unavailable")
	untracked := newFixture(t, experiment.Hooks{
		CatalogCreate: func(*catalog.Entry) error { return catalogFailure },
	}, 2)
	created, err := untracked.service.Create(context.Background(), experiment.CreateRequest{Name: "kept", NoGit: true})
	if !errors.Is(err, catalogFailure) || !created.Created || created.Tracked {
		t.Fatalf("catalog failure result = %+v, %v", created, err)
	}
	if info, statErr := os.Stat(created.Path); statErr != nil || !info.IsDir() {
		t.Errorf("uncataloged directory was removed: %v, %v", info, statErr)
	}
}

func TestListExposesUnexpectedDiscoveryErrorWithoutHidingTry(t *testing.T) {
	discoverFailure := errors.New("git executable unavailable")
	f := newFixture(t, experiment.Hooks{
		GitDiscover: func(context.Context, string) (gitx.Repo, error) {
			return gitx.Repo{}, discoverFailure
		},
	}, 2)
	f.mkdir("plain-with-probe-failure")

	items, diagnostics, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "plain-with-probe-failure")
	if item.Live.Repo != nil || !errors.Is(item.Live.DiscoverError, discoverFailure) {
		t.Errorf("discovery facts = %+v", item.Live)
	}
	if len(diagnostics) != 1 || !errors.Is(diagnostics[0], discoverFailure) {
		t.Errorf("discovery diagnostics = %+v", diagnostics)
	}
}

func TestListExposesDiscoverStatusAndCommitErrorsSeparately(t *testing.T) {
	statusFailure := errors.New("status unavailable")
	commitFailure := errors.New("commit unavailable")
	f := newFixture(t, experiment.Hooks{
		GitStatus: func(context.Context, string) (gitx.Status, error) {
			return gitx.Status{}, statusFailure
		},
		GitLastCommit: func(context.Context, string) (int64, string, error) {
			return 0, "", commitFailure
		},
	}, 2)
	path := f.mkdir("git-errors")
	initRepo(t, path, false)
	items, _, err := f.service.List(context.Background(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, "git-errors")
	if item.Live.DiscoverError != nil || item.Live.Repo == nil || item.Live.Status != nil ||
		!errors.Is(item.Live.StatusError, statusFailure) || !errors.Is(item.Live.LastCommitError, commitFailure) {
		t.Errorf("separate Git facts = %+v", item.Live)
	}
}

func TestReconcileBoundsConcurrentGitDiscovery(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	hooks := experiment.Hooks{
		GitDiscover: func(_ context.Context, directory string) (gitx.Repo, error) {
			current := active.Add(1)
			for {
				seen := maximum.Load()
				if current <= seen || maximum.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			active.Add(-1)
			return gitx.Repo{
				Root: directory, MainRoot: directory,
				GitCommonDir: filepath.Join(directory, ".git"), Name: filepath.Base(directory),
			}, nil
		},
	}
	f := newFixture(t, hooks, 2)
	for index := range 8 {
		path := f.mkdir(fmt.Sprintf("try-%d", index))
		if err := os.Mkdir(filepath.Join(path, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	items, _, err := f.service.Reconcile(context.Background())
	if err != nil || len(items) != 8 {
		t.Fatalf("Reconcile = %d, %v", len(items), err)
	}
	if got := maximum.Load(); got > 2 || got < 1 {
		t.Errorf("maximum concurrent Git probes = %d, want 1..2", got)
	}
}

func TestConcurrentReconcileCreatesOneStableRecord(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 2)
	f.mkdir("shared")
	var wait sync.WaitGroup
	wait.Add(2)
	errorsSeen := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			items, _, err := f.service.Reconcile(context.Background())
			if err == nil && (len(items) != 1 || items[0].ID == "") {
				err = fmt.Errorf("unexpected reconcile items: %+v", items)
			}
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent catalog records = %d, %v", len(entries), err)
	}
}

func TestReconcileAcrossProcessesCreatesOneStableRecord(t *testing.T) {
	if os.Getenv("DEV_EXPERIMENT_RECONCILE_HELPER") == "1" {
		gate := os.Getenv("DEV_EXPERIMENT_RECONCILE_GATE")
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		store := catalog.NewStore(os.Getenv("DEV_EXPERIMENT_CATALOG_DIR"))
		service, err := experiment.NewService(experiment.ServiceConfig{
			Registry: catalog.NewRegistry(store), Store: store,
			TriesRoot:   os.Getenv("DEV_EXPERIMENT_TRIES_ROOT"),
			ProjectRoot: os.Getenv("DEV_EXPERIMENT_PROJECT_ROOT"),
			Host:        "process-host", MaxEnrichment: 1,
			Hooks: experiment.Hooks{
				GitDiscover: func(context.Context, string) (gitx.Repo, error) {
					return gitx.Repo{}, gitx.ErrNotARepo
				},
				CatalogCreate: func(entry *catalog.Entry) error {
					time.Sleep(50 * time.Millisecond)
					return store.CreateUnderLock(entry)
				},
			},
		})
		if err == nil {
			_, _, err = service.Reconcile(context.Background())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}

	root := t.TempDir()
	triesRoot := filepath.Join(root, "tries")
	projectRoot := filepath.Join(root, "projects")
	catalogDir := filepath.Join(root, "assets")
	gate := filepath.Join(root, "start")
	for _, directory := range []string{filepath.Join(triesRoot, "shared"), projectRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	commands := make([]*exec.Cmd, 6)
	for index := range commands {
		command := exec.Command(os.Args[0], "-test.run=^TestReconcileAcrossProcessesCreatesOneStableRecord$")
		command.Env = append(os.Environ(),
			"DEV_EXPERIMENT_RECONCILE_HELPER=1",
			"DEV_EXPERIMENT_RECONCILE_GATE="+gate,
			"DEV_EXPERIMENT_CATALOG_DIR="+catalogDir,
			"DEV_EXPERIMENT_TRIES_ROOT="+triesRoot,
			"DEV_EXPERIMENT_PROJECT_ROOT="+projectRoot,
		)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[index] = command
	}
	if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Errorf("reconcile helper: %v", err)
		}
	}
	entries, err := catalog.NewStore(catalogDir).List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("cross-process catalog records = %d, %v", len(entries), err)
	}
}

func TestConcurrentServicesSerializeLegacyBackfill(t *testing.T) {
	arrivals := make(chan struct{}, 2)
	release := make(chan struct{})
	probeErr := make(chan error, 1)
	go func() {
		<-arrivals
		select {
		case <-arrivals:
			probeErr <- nil
		case <-time.After(2 * time.Second):
			probeErr <- errors.New("second service did not probe before the catalog lock")
		}
		close(release)
	}()
	discover := func(context.Context, string) (gitx.Repo, error) {
		arrivals <- struct{}{}
		<-release
		return gitx.Repo{}, gitx.ErrNotARepo
	}
	hooks := experiment.Hooks{GitDiscover: discover}
	f := newFixture(t, hooks, 1)
	f.mkdir("shared-across-services")
	second := serviceWithHooks(t, f, hooks)

	services := []*experiment.Service{f.service, second}
	results := make(chan error, len(services))
	var wait sync.WaitGroup
	wait.Add(len(services))
	for _, service := range services {
		go func(service *experiment.Service) {
			defer wait.Done()
			items, _, err := service.Reconcile(context.Background())
			if err == nil && (len(items) != 1 || items[0].ID == "") {
				err = fmt.Errorf("unexpected reconcile items: %+v", items)
			}
			results <- err
		}(service)
	}
	wait.Wait()
	if err := <-probeErr; err != nil {
		t.Error(err)
	}
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("cross-service catalog records = %d, %v", len(entries), err)
	}
}
