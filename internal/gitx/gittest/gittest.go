// Package gittest builds throwaway git repositories for tests. It is a test
// helper that lives in a normal package so every package's tests can share it.
package gittest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a scratch repository under t.TempDir().
type Repo struct {
	T    *testing.T
	Root string
}

// New initialises a repository with one commit on branch "main".
func New(t *testing.T) *Repo {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Repo{T: t, Root: root}
	r.Git("init", "--initial-branch=main")
	// Identity and signing are configured locally so the test never depends on
	// (or is broken by) the developer's global git config.
	r.Git("config", "user.email", "dev@example.test")
	r.Git("config", "user.name", "dev test")
	r.Git("config", "commit.gpgsign", "false")
	r.Write("README.md", "# test repo\n")
	r.Git("add", "README.md")
	r.Git("commit", "-m", "chore: initial commit")
	return r
}

// Git runs a git command in the repo root and fails the test on error.
func (r *Repo) Git(args ...string) string {
	r.T.Helper()
	return r.GitIn(r.Root, args...)
}

// GitIn runs a git command in an arbitrary directory (e.g. a linked worktree).
func (r *Repo) GitIn(dir string, args ...string) string {
	r.T.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", // ignore the developer's ~/.gitconfig
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.T.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write creates or replaces a file relative to the repo root.
func (r *Repo) Write(rel, content string) string {
	r.T.Helper()
	p := filepath.Join(r.Root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.T.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.T.Fatal(err)
	}
	return p
}

// Commit writes a file and commits it.
func (r *Repo) Commit(rel, content, message string) {
	r.T.Helper()
	r.Write(rel, content)
	r.Git("add", rel)
	r.Git("commit", "-m", message)
}

// Branch creates and checks out a new branch from the current HEAD.
func (r *Repo) Branch(name string) {
	r.T.Helper()
	r.Git("switch", "-c", name)
}

// WithRemote turns a second bare repository into "origin" and pushes main,
// so tests can exercise ahead/behind and published-branch logic.
func (r *Repo) WithRemote() string {
	r.T.Helper()
	remote := filepath.Join(filepath.Dir(r.Root), "origin.git")
	r.GitIn(filepath.Dir(r.Root), "init", "--bare", "--initial-branch=main", remote)
	r.Git("remote", "add", "origin", remote)
	r.Git("push", "-u", "origin", "main")
	return remote
}

// Ctx is a convenience background context for gitx calls in tests.
func Ctx() context.Context { return context.Background() }
