package gitx

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path string
	// Head is the commit the worktree is checked out at.
	Head string
	// Branch is the short branch name, empty when detached.
	Branch   string
	Detached bool
	Bare     bool
	// Locked reports a `git worktree lock`; Prunable reports a checkout whose
	// directory has gone missing. Both block a plain remove.
	Locked         bool
	LockedReason   string
	Prunable       bool
	PrunableReason string
	// Main reports the primary (non-linked) checkout.
	Main bool
}

// Worktrees lists every worktree of the repository containing dir, in git's
// own order: the main checkout first, then linked worktrees.
func Worktrees(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := run(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var (
		list []Worktree
		cur  *Worktree
	)
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" { // records are blank-line separated
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Worktree{Path: val, Main: len(list) == 0}
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked, cur.LockedReason = true, val
			}
		case "prunable":
			if cur != nil {
				cur.Prunable, cur.PrunableReason = true, val
			}
		}
	}
	flush()
	return list, nil
}

// WorktreeFor finds the worktree checked out on branch, if any. Git refuses to
// check the same branch out twice, so at most one can match.
func WorktreeFor(ctx context.Context, dir, branch string) (Worktree, bool, error) {
	list, err := Worktrees(ctx, dir)
	if err != nil {
		return Worktree{}, false, err
	}
	for _, w := range list {
		if w.Branch == branch {
			return w, true, nil
		}
	}
	return Worktree{}, false, nil
}

// AddWorktree creates a linked worktree at path.
//
// When branch already exists it is checked out; otherwise it is created from
// base. Passing an explicit base matters: without one git branches from the
// *current* HEAD, so a worktree created while standing on feature/A would
// silently inherit feature/A's commits.
func AddWorktree(ctx context.Context, dir, path, branch, base string) error {
	exists := BranchExists(ctx, dir, branch)
	args := []string{"worktree", "add"}
	if exists {
		args = append(args, path, branch)
	} else {
		args = append(args, "-b", branch, path)
		if base != "" {
			args = append(args, base)
		}
	}
	_, err := run(ctx, dir, args...)
	return err
}

// MoveWorktree relocates a linked worktree through Git so the shared
// administrative metadata keeps pointing at the checkout's new path. dir may
// be any checkout of the same repository.
func MoveWorktree(ctx context.Context, dir, source, destination string) error {
	_, err := run(ctx, dir, "worktree", "move", source, destination)
	return err
}

// RemoveWorktree removes a linked worktree checkout. It never deletes the
// branch — that is a separate, explicit decision.
func RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory before removing worktree: %w", err)
	}
	inside, err := pathx.Contains(path, cwd)
	if err != nil {
		return fmt.Errorf("compare current directory with worktree %s: %w", path, err)
	}
	if inside {
		return fmt.Errorf("refusing to remove worktree %s while the caller is inside it", path)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err = run(ctx, dir, args...)
	return err
}

// PruneWorktrees clears administrative entries for checkouts whose directories
// were deleted behind git's back.
func PruneWorktrees(ctx context.Context, dir string) error {
	_, err := run(ctx, dir, "worktree", "prune")
	return err
}

// BranchExists reports whether a local branch of that name exists.
func BranchExists(ctx context.Context, dir, branch string) bool {
	if branch == "" {
		return false
	}
	_, err := run(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// RefExists reports whether any ref or commit-ish resolves.
func RefExists(ctx context.Context, dir, ref string) bool {
	if ref == "" {
		return false
	}
	_, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}
