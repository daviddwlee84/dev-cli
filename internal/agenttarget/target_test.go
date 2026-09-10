package agenttarget_test

import (
	"context"
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

func TestFromRepoPreservesNavigationAliasAndCanonicalCheckout(t *testing.T) {
	repository := gittest.New(t)
	index := t.TempDir()
	alias := filepath.Join(index, "friendly")
	if err := os.Symlink(repository.Root, alias); err != nil {
		t.Fatal(err)
	}
	discovered, err := repo.Discover(context.Background(), []string{index}, repo.DefaultOptions())
	if err != nil || len(discovered) != 1 {
		t.Fatalf("discovered = %+v, err = %v", discovered, err)
	}
	target, ok := agenttarget.FromRepo(discovered[0])
	if !ok {
		t.Fatal("discovered repository did not convert")
	}
	if target.RepoPath != alias || target.CheckoutRoot != repository.Root {
		t.Fatalf("target = %+v, want alias path and physical checkout", target)
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

func TestResolveAliasAndWithCurrent(t *testing.T) {
	repository := gittest.New(t)
	parent := filepath.Dir(repository.Root)
	resolved, err := agenttarget.Resolve(context.Background(), []string{parent}, filepath.Base(repository.Root))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := resolved
	duplicate.RepoDisplay = "duplicate alias"
	linked := resolved
	linked.CheckoutRoot = filepath.Join(parent, "linked")
	got := agenttarget.WithCurrent([]agenttarget.Target{duplicate, linked}, resolved)
	if len(got) != 2 {
		t.Fatalf("targets = %+v, want one main and one linked checkout", got)
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
