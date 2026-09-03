package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

var (
	// ErrWorktreeNotFound reports that Git's current registry has no worktree at
	// the requested canonical path.
	ErrWorktreeNotFound = errors.New("registered worktree not found")
	// ErrWorktreeAmbiguous reports that multiple Git records resolve to the same
	// canonical path. Callers must not choose one of the aliases implicitly.
	ErrWorktreeAmbiguous = errors.New("registered worktree path is ambiguous")
)

// RegisteredWorktree is an identity snapshot from Git's worktree registry.
// Worktree preserves the complete parsed Git record; the other fields are canonical
// filesystem identities suitable for exact comparison across symlink and macOS
// path aliases. A snapshot proves registration, not permission to remove it.
type RegisteredWorktree struct {
	Worktree       Worktree
	RepositoryPath string
	GitCommonDir   string
	Path           string
}

// IsLinkedWorktree reports whether the record is a linked checkout rather than
// Git's main checkout or a bare repository. It is classification, not a removal
// safety decision.
func (w RegisteredWorktree) IsLinkedWorktree() bool {
	return !w.Worktree.Main && !w.Worktree.Bare
}

// ResolveRegisteredWorktree resolves exactPath against a fresh authoritative
// `git worktree list --porcelain` query for the repository containing repoPath.
// Matching is exclusively by canonical filesystem identity, never by branch.
// It performs no prune, removal, or other mutation.
func ResolveRegisteredWorktree(ctx context.Context, repoPath, exactPath string) (RegisteredWorktree, error) {
	worktrees, err := Worktrees(ctx, repoPath)
	if err != nil {
		return RegisteredWorktree{}, fmt.Errorf("query registered worktrees: %w", err)
	}
	commonDir, err := run(ctx, repoPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return RegisteredWorktree{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	return resolveRegisteredWorktree(worktrees, commonDir, exactPath)
}

func resolveRegisteredWorktree(worktrees []Worktree, commonDir, exactPath string) (RegisteredWorktree, error) {
	path, err := pathx.Canonical(exactPath)
	if err != nil {
		return RegisteredWorktree{}, fmt.Errorf("canonicalize requested worktree %q: %w", exactPath, err)
	}
	canonicalCommonDir, err := pathx.Canonical(commonDir)
	if err != nil {
		return RegisteredWorktree{}, fmt.Errorf("canonicalize Git common directory %q: %w", commonDir, err)
	}

	type match struct {
		worktree Worktree
		path     string
	}
	var (
		repositoryPath string
		matches        []match
	)
	for _, worktree := range worktrees {
		canonicalPath, canonicalErr := pathx.Canonical(worktree.Path)
		if canonicalErr != nil {
			return RegisteredWorktree{}, fmt.Errorf("canonicalize registered worktree %q: %w", worktree.Path, canonicalErr)
		}
		if worktree.Main {
			repositoryPath = canonicalPath
		}
		if sameCanonicalPath(path, canonicalPath) {
			matches = append(matches, match{worktree: worktree, path: canonicalPath})
		}
	}

	switch len(matches) {
	case 0:
		return RegisteredWorktree{}, fmt.Errorf("%w: %s", ErrWorktreeNotFound, path)
	case 1:
		if repositoryPath == "" {
			return RegisteredWorktree{}, errors.New("registered worktree query has no main repository record")
		}
		return RegisteredWorktree{
			Worktree:       matches[0].worktree,
			RepositoryPath: repositoryPath,
			GitCommonDir:   canonicalCommonDir,
			Path:           matches[0].path,
		}, nil
	default:
		return RegisteredWorktree{}, fmt.Errorf("%w: %s resolves to %d records", ErrWorktreeAmbiguous, path, len(matches))
	}
}

func sameCanonicalPath(left, right string) bool {
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
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

// WorktreeFor finds the first worktree checked out on branch, if any. Git
// normally refuses duplicate branch checkouts, but --force can create them;
// identity-sensitive callers must use ResolveRegisteredWorktree instead.
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

// RefState reports whether any ref or commit-ish resolves while preserving
// observation failures. A normal rev-parse exit 1 with no diagnostic is an
// authoritative missing ref; every other failure remains an error.
func RefState(ctx context.Context, dir, ref string) (bool, error) {
	if ref == "" {
		return false, nil
	}
	_, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err == nil {
		return true, nil
	}
	var commandErr *Error
	var exitErr *exec.ExitError
	if errors.As(err, &commandErr) && errors.As(commandErr.Err, &exitErr) &&
		exitErr.ExitCode() == 1 && strings.TrimSpace(commandErr.Stderr) == "" {
		return false, nil
	}
	return false, err
}

// RefExists is the compatibility predicate for callers where probe failure is
// intentionally equivalent to absence. Guarded lifecycle code uses RefState.
func RefExists(ctx context.Context, dir, ref string) bool {
	exists, _ := RefState(ctx, dir, ref)
	return exists
}
