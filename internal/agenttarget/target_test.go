package agenttarget_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestCurrentPreservesLinkedWorktree(t *testing.T) {
	repository := gittest.New(t)
	linked := filepath.Join(filepath.Dir(repository.Root), "linked")
	repository.Git("worktree", "add", "-b", "feat/linked-target", linked)
	nested := filepath.Join(linked, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	target, err := agenttarget.Current(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	mainGit, err := gitx.Discover(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if target.RepoPath != repository.Root {
		t.Errorf("repository = %q, want %q", target.RepoPath, repository.Root)
	}
	if target.CheckoutRoot != linked {
		t.Errorf("checkout = %q, want linked worktree %q", target.CheckoutRoot, linked)
	}
	if target.CommonDir != mainGit.GitCommonDir {
		t.Errorf("common dir = %q, want %q", target.CommonDir, mainGit.GitCommonDir)
	}
	if target.RepoPath == target.CheckoutRoot {
		t.Error("linked checkout was collapsed to the main repository")
	}
}

func TestCurrentPropagatesCancellationAndMissingGit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := agenttarget.Current(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Current error = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := agenttarget.Current(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Current treated a missing Git executable as an ordinary folder")
	}
}

func TestCurrentFallsBackToOrdinaryDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ordinary")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	target, err := agenttarget.Current(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if target.RepoPath != root || target.CheckoutRoot != root || target.CommonDir != root {
		t.Fatalf("ordinary directory target = %+v", target)
	}
}

func TestResolveRepositoryAndPath(t *testing.T) {
	repository := gittest.New(t)
	parent := filepath.Dir(repository.Root)

	byName, err := agenttarget.ResolveRepository(context.Background(), []string{parent}, filepath.Base(repository.Root))
	if err != nil {
		t.Fatal(err)
	}
	byPath, err := agenttarget.ResolvePath(context.Background(), filepath.Join(repository.Root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if byName.Key() != byPath.Key() || byName.CheckoutRoot != repository.Root {
		t.Fatalf("repository target = %+v, path target = %+v", byName, byPath)
	}
}

func TestResolveLinkedPathKeepsMainRepositoryIdentity(t *testing.T) {
	repository := gittest.New(t)
	linked := filepath.Join(filepath.Dir(repository.Root), "linked-name")
	repository.Git("worktree", "add", "-b", "feat/linked-name", linked)

	target, err := agenttarget.ResolveRepository(context.Background(), nil, linked)
	if err != nil {
		t.Fatal(err)
	}
	if target.RepoName != filepath.Base(repository.Root) {
		t.Fatalf("linked target repo name = %q, want %q", target.RepoName, filepath.Base(repository.Root))
	}
	if target.CheckoutRoot != linked || target.RepoPath != repository.Root {
		t.Fatalf("linked target paths = %+v", target)
	}
}

func TestResolveLinkedAliasKeepsSelectedCheckoutPath(t *testing.T) {
	repository := gittest.New(t)
	linked := filepath.Join(filepath.Dir(repository.Root), "linked-alias-target")
	repository.Git("worktree", "add", "-b", "feat/linked-alias-target", linked)
	alias := filepath.Join(t.TempDir(), "friendly-linked")
	if err := os.Symlink(linked, alias); err != nil {
		t.Fatal(err)
	}

	target, err := agenttarget.ResolveRepository(context.Background(), nil, alias)
	if err != nil {
		t.Fatal(err)
	}
	if target.CheckoutRoot != alias || target.RepoPath != repository.Root {
		t.Fatalf("linked alias target = %+v", target)
	}
	if target.RepoName != filepath.Base(repository.Root) {
		t.Fatalf("linked alias repo name = %q", target.RepoName)
	}
}

func TestFromRepositoriesSkipsNonGitAndBareAndSorts(t *testing.T) {
	base := t.TempDir()
	repositories := []repo.Repo{
		{Name: "zeta", Path: filepath.Join(base, "zeta"), RealPath: filepath.Join(base, "zeta"), MainRoot: filepath.Join(base, "zeta"), CommonDir: filepath.Join(base, "zeta", ".git"), HasGit: true},
		{Name: "plain", Path: filepath.Join(base, "plain")},
		{Name: "bare", Path: filepath.Join(base, "bare.git"), CommonDir: filepath.Join(base, "bare.git"), HasGit: true, Bare: true},
		{Name: "Alpha", Path: filepath.Join(base, "alpha"), RealPath: filepath.Join(base, "alpha"), MainRoot: filepath.Join(base, "alpha"), CommonDir: filepath.Join(base, "alpha", ".git"), HasGit: true},
	}

	got := agenttarget.FromRepositories(repositories)
	if len(got) != 2 {
		t.Fatalf("targets = %+v, want two working Git repositories", got)
	}
	if got[0].RepoName != "Alpha" || got[1].RepoName != "zeta" {
		t.Fatalf("target order = %q, %q", got[0].RepoName, got[1].RepoName)
	}
}

func TestFromRepositoryPreservesNavigationAlias(t *testing.T) {
	physical := filepath.Join(t.TempDir(), "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "catalog-alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	target, ok := agenttarget.FromRepository(repo.Repo{
		Name: "demo", Path: alias, RealPath: physical, MainRoot: physical,
		CommonDir: filepath.Join(physical, ".git"), HasGit: true, Symlink: true,
	})
	if !ok {
		t.Fatal("repository was skipped")
	}
	if target.RepoPath != alias || target.CheckoutRoot != alias {
		t.Fatalf("navigation alias was lost: %+v", target)
	}
}

func TestDedupeUsesCommonDirAndCheckout(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "common")
	otherCheckout := filepath.Join(root, "other")

	got := agenttarget.Dedupe([]agenttarget.Target{
		{RepoName: "first", RepoDisplay: "first", RepoPath: physical, CheckoutRoot: alias, CommonDir: common},
		{RepoName: "duplicate", RepoDisplay: "duplicate", RepoPath: physical, CheckoutRoot: physical, CommonDir: common},
		{RepoName: "second checkout", RepoDisplay: "second checkout", RepoPath: physical, CheckoutRoot: otherCheckout, CommonDir: common},
	})
	if len(got) != 2 {
		t.Fatalf("targets = %+v, want physical duplicate collapsed but other checkout retained", got)
	}
	names := []string{got[0].RepoName, got[1].RepoName}
	if !reflect.DeepEqual(names, []string{"first", "second checkout"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestDedupeCanonicalizesMissingSuffixBelowSymlink(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "common")
	missing := filepath.Join("not-created", "checkout")

	got := agenttarget.Dedupe([]agenttarget.Target{
		{RepoName: "alias", CheckoutRoot: filepath.Join(alias, missing), CommonDir: common},
		{RepoName: "physical", CheckoutRoot: filepath.Join(physical, missing), CommonDir: common},
	})
	if len(got) != 1 {
		t.Fatalf("targets = %+v, want missing suffix aliases deduplicated", got)
	}
}

func TestReconcileCurrentUsesCanonicalNavigationAlias(t *testing.T) {
	physical := filepath.Join(t.TempDir(), "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "friendly")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(physical, ".git")
	canonical := agenttarget.Target{
		RepoName: "friendly", RepoDisplay: "group/friendly", RepoPath: alias,
		CheckoutRoot: alias, CommonDir: common,
	}
	current := agenttarget.Target{
		RepoName: "physical", RepoDisplay: "physical", RepoPath: physical,
		CheckoutRoot: physical, CommonDir: common,
	}
	got := agenttarget.ReconcileCurrent([]agenttarget.Target{canonical}, current)
	if got.RepoName != canonical.RepoName || got.RepoDisplay != canonical.RepoDisplay ||
		got.RepoPath != alias || got.CheckoutRoot != alias {
		t.Fatalf("reconciled current = %+v", got)
	}
}

func TestWithCurrentAddsOnlyDistinctCheckout(t *testing.T) {
	common := filepath.Join(t.TempDir(), "common")
	canonical := agenttarget.Target{
		RepoName: "demo", RepoDisplay: "group/demo", RepoPath: "/catalog/demo",
		CheckoutRoot: "/catalog/demo", CommonDir: common,
	}
	linked := agenttarget.Target{
		RepoName: "physical-demo", RepoDisplay: "physical-demo", RepoPath: "/src/physical-demo",
		CheckoutRoot: "/worktrees/demo-feature", CommonDir: common,
	}

	if got := agenttarget.WithCurrent([]agenttarget.Target{canonical}, canonical); len(got) != 1 {
		t.Fatalf("canonical duplicate targets = %+v", got)
	}
	got := agenttarget.WithCurrent([]agenttarget.Target{canonical}, linked)
	if len(got) != 2 || got[0].CheckoutRoot == got[1].CheckoutRoot {
		t.Fatalf("linked checkout targets = %+v", got)
	}
	for _, target := range got {
		if target.RepoName != canonical.RepoName || target.RepoDisplay != canonical.RepoDisplay || target.RepoPath != canonical.RepoPath {
			t.Fatalf("canonical metadata was not inherited: %+v", got)
		}
	}
}

func TestAllUsesRepositoryDiscovery(t *testing.T) {
	first := gittest.New(t)
	secondParent := t.TempDir()
	second := filepath.Join(secondParent, "another")
	command := gittest.New(t)
	if err := os.Rename(command.Root, second); err != nil {
		t.Fatal(err)
	}

	got, err := agenttarget.All(context.Background(), []string{filepath.Dir(first.Root), secondParent})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("all targets = %+v", got)
	}
}
