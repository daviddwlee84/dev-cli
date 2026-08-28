package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestProjectHasArtifactScannerRequiresActiveHook(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gitleaks"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r.Write(".gitleaks.toml", "title = \"test\"\n")
	if projectHasArtifactScanner(r.Root) {
		t.Fatal("configuration without an installed hook is not an active scanner")
	}

	hooks := filepath.Join(r.Root, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "core.hooksPath", "hooks")
	if projectHasArtifactScanner(r.Root) {
		t.Fatal("an unrelated hook must not activate artifact amendment")
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ngitleaks git --staged\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !projectHasArtifactScanner(r.Root) {
		t.Fatal("installed gitleaks hook plus repository config should be recognized")
	}
}

func TestArtifactCommitReachabilityRejectsOrphanedReceipt(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	r.Commit(".specstory/history/session.md", "transcript\n", "chore: artifact")
	commit := r.Git("rev-parse", "HEAD")
	intent := artifact.Intent{
		RepoPath: r.Root, WorktreePath: r.Root, Branch: "main", Base: "main", ArtifactCommit: commit,
	}
	if !artifactCommitReachable(intent) {
		t.Fatal("current artifact commit should be reachable")
	}
	r.Git("reset", "--hard", "HEAD^")
	if artifactCommitReachable(intent) {
		t.Fatal("orphaned artifact receipt must not authorize integration or retirement")
	}
}
