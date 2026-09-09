package triage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestSelectedCollectionDoesNotDiscoverUnrelatedRoots(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX Git probe recorder")
	}
	s, r := fixture(t)
	other := gittest.New(t)
	r.Git("branch", "forgotten")
	wt := filepath.Join(t.TempDir(), "linked")
	r.Git("worktree", "add", "-b", "linked", wt)
	g, err := gitx.Discover(t.Context(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Config.Paths.ScanRoots = []string{other.Root}
	s.cfg.CWD = other.Root
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "probes")
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" >> " + quote(log) + "\nexec " + quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	report, err := s.Collect(t.Context(), Options{Selection: []Target{{Path: r.Root, RepositoryID: g.GitCommonDir, Kind: "repo"}}})
	if err != nil {
		t.Fatal(err)
	}
	branch, linked := false, false
	for _, i := range report.Items {
		if i.RepositoryID != g.GitCommonDir {
			t.Fatalf("scope leaked: %+v", i)
		}
		branch = branch || (i.Branch != nil && i.Branch.Ref == "refs/heads/forgotten")
		canonical, _ := pathx.Canonical(wt)
		linked = linked || i.Path == canonical
	}
	if !branch || !linked {
		t.Fatal("whole-repository scope incomplete")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	otherPath, _ := pathx.Canonical(other.Root)
	if strings.Contains(string(data), otherPath) {
		t.Fatal("unrelated Git probe", string(data))
	}
	empty, err := s.Collect(t.Context(), Options{Selection: []Target{}})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("empty scope expanded: %+v %v", empty, err)
	}
}

func TestDashboardSnapshotIsReusedButCannotAuthorizeFetch(t *testing.T) {
	s, r := fixture(t)
	g, err := gitx.Discover(t.Context(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().Add(-time.Minute).UTC()
	seed := RepositorySnapshot{Repo: repo.Repo{Path: r.Root, CommonDir: g.GitCommonDir}, ObservedAt: observed, Topology: gitx.RecoveryTopology{Remotes: []gitx.RemoteInfo{{Name: "cached-remote"}}}}
	report, err := s.Collect(t.Context(), Options{Selection: []Target{{Path: r.Root, RepositoryID: g.GitCommonDir}}, Snapshots: []RepositorySnapshot{seed}})
	if err != nil {
		t.Fatal(err)
	}
	var selected []Item
	for _, i := range report.Items {
		if a, ok := i.Action("fetch"); ok && a.Remote == "cached-remote" {
			selected = append(selected, i)
		}
	}
	if len(selected) != 1 {
		t.Fatal("metadata snapshot not reused")
	}
	provenance := false
	for _, source := range report.Sources {
		provenance = provenance || source.ObservedAt.Equal(observed)
	}
	if !provenance {
		t.Fatal("snapshot time lost")
	}
	b, err := s.Prepare(t.Context(), selected, "fetch")
	if err != nil {
		t.Fatal(err)
	}
	if b.ReadyCount() != 0 {
		t.Fatal("cached remote authorized a live fetch")
	}
}

func TestTryBatchForgetChecksNotesAtApply(t *testing.T) {
	s, _ := fixture(t)
	service, err := s.tryService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.Config.Paths.TriesRoot, "accidental")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	items, _, err := service.List(t.Context(), experiment.ListOptions{})
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	item := items[0]
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	report, err := s.Collect(t.Context(), Options{Selection: []Target{{Path: item.Live.CurrentPath, CatalogID: item.ID, Kind: "try"}}})
	if err != nil || len(report.Items) != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	if report.Items[0].Presence != "missing" {
		t.Fatal(report.Items[0])
	}
	b, err := s.Prepare(t.Context(), report.Items, "forget-try")
	if err != nil || b.ReadyCount() != 1 || b.Token != "FORGET 1" {
		t.Fatalf("%+v %v", b.Previews(), err)
	}
	notes := note.NewStore(s.cfg.Config.NotesDir())
	if _, err := notes.Create(t.Context(), item.ID, "accidental", "preserve this thought", nil); err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Outcomes) != 1 || ledger.Outcomes[0].Status != "failed" {
		t.Fatal(ledger)
	}
	if _, err := s.cfg.Catalog.Get(item.ID); err != nil {
		t.Fatal("deleted referenced catalog", err)
	}
}

func TestTryBatchTrashPreservesIgnoredBytesAndSkipsIncompatible(t *testing.T) {
	s, r := fixture(t)
	path := filepath.Join(s.cfg.Config.Paths.TriesRoot, "scratch")
	r.Git("clone", r.Root, path)
	if err := os.WriteFile(filepath.Join(path, ".gitignore"), []byte("private/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "private", "important"), []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(t.TempDir(), "trash")
	s.cfg.TryHooks = experiment.Hooks{TrashAvailable: func() error { return nil }, Trash: func(_ context.Context, p string) error { return os.Rename(p, trash) }}
	report, err := s.Collect(t.Context(), Options{Selection: []Target{{Path: path, Kind: "try"}}})
	if err != nil {
		t.Fatal(err)
	}
	var targets []Item
	for _, i := range report.Items {
		if _, ok := i.Action("trash-try"); ok {
			targets = append(targets, i)
		}
	}
	targets = append(targets, Item{ID: "unrelated", Path: r.Root, Kind: "repo"})
	b, err := s.Prepare(t.Context(), targets, "trash-try")
	if err != nil || b.ReadyCount() != 1 {
		t.Fatalf("%+v %v", b.Previews(), err)
	}
	entries, _ := s.cfg.Catalog.List()
	if len(entries) != 0 {
		t.Fatal("preview enrolled Try")
	}
	ledger, err := s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Outcomes[0].Status != "completed" || ledger.Outcomes[1].Status != "skipped" {
		t.Fatal(ledger)
	}
	data, err := os.ReadFile(filepath.Join(trash, "private", "important"))
	if err != nil || string(data) != "saved" {
		t.Fatalf("%q %v", data, err)
	}
	entries, err = s.cfg.Catalog.List()
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	l, _ := entries[0].LocationFor(s.cfg.Host)
	if l.State != catalog.LocationEvicted {
		t.Fatal(l)
	}
}
