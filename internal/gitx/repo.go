package gitx

import (
	"context"
	"path/filepath"
	"strings"
)

// Repo identifies one repository, independent of which worktree you are
// standing in. Two linked worktrees of the same clone share a Key.
type Repo struct {
	// Root is the working-tree root of the checkout that was probed.
	Root string
	// GitCommonDir is the shared .git directory — the same value for every
	// worktree of a clone, which makes it the natural identity.
	GitCommonDir string
	// Name is the basename users recognise the repo by.
	Name string
	// IsLinkedWorktree reports whether Root is a linked worktree rather than
	// the main checkout.
	IsLinkedWorktree bool
	// MainRoot is the main (non-linked) checkout of this clone.
	MainRoot string
	// Bare reports whether the main checkout is a bare repository.
	Bare bool
}

// Key is the stable identity of the clone, shared by all its worktrees.
func (r Repo) Key() string { return r.GitCommonDir }

// Discover probes dir and reports the repository containing it.
func Discover(ctx context.Context, dir string) (Repo, error) {
	// A single rev-parse call answers everything: batching avoids four
	// process spawns per repo, which matters when scanning 39 of them.
	out, err := run(ctx, dir,
		"rev-parse",
		"--path-format=absolute",
		"--show-toplevel",
		"--git-common-dir",
		"--is-bare-repository",
	)
	if err != nil {
		return Repo{}, err
	}
	f := lines(out)
	r := Repo{}
	// A bare repo has no toplevel, so git prints one fewer line.
	switch len(f) {
	case 3:
		r.Root = f[0]
		r.GitCommonDir = f[1]
		r.Bare = f[2] == "true"
	case 2:
		r.GitCommonDir = f[0]
		r.Bare = f[1] == "true"
	default:
		return Repo{}, ErrNotARepo
	}

	// The main checkout is the parent of the common .git dir, except for a
	// bare repo (where the common dir *is* the repo).
	if r.Bare {
		r.MainRoot = strings.TrimSuffix(r.GitCommonDir, string(filepath.Separator))
	} else {
		r.MainRoot = filepath.Dir(r.GitCommonDir)
	}
	r.IsLinkedWorktree = r.Root != "" && r.Root != r.MainRoot
	r.Name = filepath.Base(r.MainRoot)
	if r.Bare {
		// ".../foo/.bare" or ".../foo.git" should both read as "foo".
		r.Name = strings.TrimSuffix(filepath.Base(r.MainRoot), ".git")
		if strings.HasPrefix(r.Name, ".") {
			r.Name = filepath.Base(filepath.Dir(r.MainRoot))
		}
	}
	return r, nil
}

// IsRepo reports whether dir is inside a git working tree, without the cost of
// a full Discover.
func IsRepo(ctx context.Context, dir string) bool {
	_, err := run(ctx, dir, "rev-parse", "--git-dir")
	return err == nil
}

// DefaultBranch resolves the repository's default branch — origin/HEAD when the
// remote advertises it, else the first of main/master that exists locally.
// Returns "" when nothing can be determined, which callers must treat as
// "ask the user" rather than guessing.
func DefaultBranch(ctx context.Context, dir string) string {
	if out, err := run(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if _, b, ok := strings.Cut(out, "/"); ok {
			return b
		}
	}
	for _, cand := range []string{"main", "master", "trunk", "develop"} {
		if _, err := run(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+cand); err == nil {
			return cand
		}
	}
	return ""
}

// Remote returns the push URL of the named remote ("origin" when empty).
func Remote(ctx context.Context, dir, name string) string {
	if name == "" {
		name = "origin"
	}
	out, err := run(ctx, dir, "remote", "get-url", name)
	if err != nil {
		return ""
	}
	return out
}
