// Package bootstrap discovers and organises an existing machine without
// imposing a new physical layout on it.
//
// Repositories are deduplicated by Git's common directory, not by pathname:
// a canonical checkout, a symlink to it and each linked worktree are several
// filesystem paths but one clone. Keeping that distinction is what lets a
// bootstrap report aliases honestly without treating them as duplicate repos.
package bootstrap

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Kind describes what one discovered checkout is.
type Kind string

const (
	// Canonical is the main, non-bare checkout of a clone.
	Canonical Kind = "canonical"
	// Worktree is a linked git worktree sharing a clone's common directory.
	Worktree Kind = "worktree"
	// Bare is a bare repository or worktree hub.
	Bare Kind = "bare"
)

// Alias is another path that resolves to the same checkout.
type Alias struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// Repository is one discovered checkout. Paths resolving to the same checkout
// are folded into Aliases; linked worktrees remain their own entries, because
// they have their own branch, status and lifecycle even though CommonDir is
// shared.
type Repository struct {
	Path     string `json:"path"`
	RealPath string `json:"real_path"`
	Root     string `json:"root"`
	Relative string `json:"relative"`
	Name     string `json:"name"`
	Kind     Kind   `json:"kind"`
	Depth    int    `json:"depth"`

	// CommonDir identifies the clone shared by every linked worktree.
	CommonDir string `json:"common_dir"`
	// MainRoot is the clone's main checkout.
	MainRoot string      `json:"main_root"`
	Branch   string      `json:"branch,omitempty"`
	Status   gitx.Status `json:"status"`
	Remote   string      `json:"remote,omitempty"`

	// Symlink reports that Path itself is a symlink. Aliases holds every other
	// symlink that reached the same checkout.
	Symlink       bool    `json:"symlink"`
	SymlinkTarget string  `json:"symlink_target,omitempty"`
	Aliases       []Alias `json:"aliases,omitempty"`
}

// Dirty reports uncommitted work in this checkout.
func (r Repository) Dirty() bool { return r.Status.Dirty() }

// CloneKey groups a main checkout and its linked worktrees.
func (r Repository) CloneKey() string { return r.CommonDir }

// CheckoutKey identifies this checkout independent of which alias reached it.
func (r Repository) CheckoutKey() string { return r.RealPath }

// Options tune a recursive scan.
type Options struct {
	// MaxDepth is how deep below each root to descend. Zero means unlimited.
	MaxDepth int
	// FollowSymlinkDirs follows symlinks to directories, with realpath cycle
	// detection. A symlink directly to a repo is always inspected even when
	// this is false; the flag controls symlinked container directories.
	FollowSymlinkDirs bool
	// IncludeWorktrees includes linked worktrees. The default bootstrap report
	// includes them because moving a clone with worktrees is unsafe; callers
	// need to know they exist even if they do not index them.
	IncludeWorktrees bool
}

// DefaultOptions are deliberately recursive enough for ghq's
// host/owner/repo and category/group/repo layouts without wandering through a
// whole mounted filesystem forever.
func DefaultOptions() Options {
	return Options{MaxDepth: 8, FollowSymlinkDirs: true, IncludeWorktrees: true}
}

// skipped directory names cannot be project containers and dominate scan time.
var skipped = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"target": true, "vendor": true, "__pycache__": true, ".tox": true,
	"dist": true, "build": true, ".next": true, ".cache": true,
	".terraform": true, ".Trash": true, "Library": true,
}

// Scan recursively locates repositories under roots.
//
// Unreadable and missing paths are returned as warnings rather than failing
// the whole scan: a removable volume not mounted today should not hide every
// repository on the other roots.
func Scan(ctx context.Context, roots []string, opts Options) ([]Repository, []error) {
	if opts.MaxDepth < 0 {
		opts.MaxDepth = 0
	}
	if opts == (Options{}) {
		opts = DefaultOptions()
	}

	var (
		found    []Repository
		warnings []error
	)
	// Index by physical checkout. Several symlinks to one checkout become
	// aliases on one row rather than fake duplicate repos.
	byCheckout := map[string]int{}
	// Real directories traversed, to stop symlink loops.
	visitedDirs := map[string]bool{}

	for _, root := range roots {
		root, err := filepath.Abs(root)
		if err != nil {
			warnings = append(warnings, err)
			continue
		}
		root = filepath.Clean(root)
		if err := scanPath(ctx, root, root, 0, opts, visitedDirs, byCheckout, &found, &warnings); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return found, append(warnings, err)
			}
			warnings = append(warnings, err)
		}
	}

	if opts.IncludeWorktrees {
		addRegisteredWorktrees(ctx, &found, byCheckout, &warnings)
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].Kind != found[j].Kind {
			return kindRank(found[i].Kind) < kindRank(found[j].Kind)
		}
		if strings.ToLower(found[i].Name) != strings.ToLower(found[j].Name) {
			return strings.ToLower(found[i].Name) < strings.ToLower(found[j].Name)
		}
		return found[i].Path < found[j].Path
	})
	return found, warnings
}

// addRegisteredWorktrees asks each canonical checkout for Git's complete
// worktree list. A filesystem walk stops at repo roots on purpose (otherwise
// source submodules look like projects), so this is how worktrees nested under
// .claude/ or living outside every scan root still appear in the report.
func addRegisteredWorktrees(ctx context.Context, found *[]Repository,
	byCheckout map[string]int, warnings *[]error) {

	// Snapshot the canonical rows: appending while ranging over the same slice
	// is legal but makes the intent easy to misread.
	canonicals := append([]Repository(nil), (*found)...)
	for _, main := range canonicals {
		if main.Kind != Canonical {
			continue
		}
		list, err := gitx.Worktrees(ctx, main.Path)
		if err != nil {
			*warnings = append(*warnings, &PathError{Path: main.Path, Err: err})
			continue
		}
		for _, w := range list {
			if w.Main || w.Path == "" {
				continue
			}
			real := w.Path
			if resolved, err := filepath.EvalSymlinks(w.Path); err == nil {
				real = resolved
			}
			_, linkErr := os.Lstat(w.Path)
			isLink := linkErr == nil && isPathSymlink(w.Path)
			r, ok := inspectRepo(ctx, main.Root, w.Path, real, main.Depth+1, isLink)
			if !ok {
				// A prunable worktree's directory is already gone. Preserve a
				// minimal row because its stale registration is exactly what a
				// bootstrap audit should expose.
				r = Repository{
					Path: w.Path, RealPath: w.Path, Root: main.Root,
					Relative: w.Path, Name: main.Name, Kind: Worktree,
					CommonDir: main.CommonDir, MainRoot: main.MainRoot,
					Branch: w.Branch,
				}
			}
			key := r.CheckoutKey()
			if _, exists := byCheckout[key]; exists {
				continue
			}
			byCheckout[key] = len(*found)
			*found = append(*found, r)
		}
	}
}

func isPathSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func scanPath(ctx context.Context, root, path string, depth int, opts Options,
	visited map[string]bool, byCheckout map[string]int,
	found *[]Repository, warnings *[]error) error {

	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		*warnings = append(*warnings, &PathError{Path: path, Err: err})
		return nil
	}

	isLink := info.Mode()&os.ModeSymlink != 0
	real := path
	if isLink {
		real, err = filepath.EvalSymlinks(path)
		if err != nil {
			*warnings = append(*warnings, &PathError{Path: path, Err: err})
			return nil
		}
		real, _ = filepath.Abs(real)
		info, err = os.Stat(real)
		if err != nil {
			*warnings = append(*warnings, &PathError{Path: path, Err: err})
			return nil
		}
	}
	if !info.IsDir() {
		return nil
	}
	// Canonicalise even a non-symlink path: on macOS /var itself is a symlink
	// to /private/var, and Git reports the latter. Checkout identity has to use
	// the same physical spelling for both or one repo appears twice.
	if resolved, err := filepath.EvalSymlinks(real); err == nil {
		real = resolved
	}

	// Check the path as a repo before the visited-dir guard: a symlink directly
	// to a repo must be retained as an alias even when its target was found
	// first through another path.
	if r, ok := inspectRepo(ctx, root, path, real, depth, isLink); ok {
		if r.Kind == Worktree && !opts.IncludeWorktrees {
			return nil
		}
		if i, exists := byCheckout[r.CheckoutKey()]; exists {
			current := &(*found)[i]
			if path == current.Path {
				return nil
			}
			// Prefer the physical path as the row and keep symlinks as
			// aliases, regardless of which one alphabetical traversal found
			// first. That makes a later `--index` plan stable.
			if current.Symlink && !r.Symlink {
				r.Aliases = append(r.Aliases,
					Alias{Path: current.Path, Target: current.RealPath})
				r.Aliases = append(r.Aliases, current.Aliases...)
				*current = r
				return nil
			}
			current.Aliases = append(current.Aliases, Alias{Path: path, Target: real})
			return nil
		}
		byCheckout[r.CheckoutKey()] = len(*found)
		*found = append(*found, r)
		return nil // never descend into a repository's source tree
	}

	if isLink && !opts.FollowSymlinkDirs {
		return nil
	}
	realDir, err := filepath.EvalSymlinks(real)
	if err == nil {
		if visited[realDir] {
			return nil
		}
		visited[realDir] = true
	}
	if opts.MaxDepth > 0 && depth >= opts.MaxDepth {
		return nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		*warnings = append(*warnings, &PathError{Path: path, Err: err})
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if skipped[name] {
			continue
		}
		// Hidden container directories can hold repos (.config rarely should,
		// .local/share often does). Only skip well-known generated ones above;
		// the max depth is the bound for everything else.
		if err := scanPath(ctx, root, filepath.Join(path, name), depth+1, opts,
			visited, byCheckout, found, warnings); err != nil {
			return err
		}
	}
	return nil
}

func inspectRepo(ctx context.Context, root, displayPath, realPath string,
	depth int, symlink bool) (Repository, bool) {
	kind := Canonical
	var g gitx.Repo

	if isBare(realPath) {
		kind = Bare
		g = gitx.Repo{Root: realPath, MainRoot: realPath, GitCommonDir: realPath,
			Name: strings.TrimSuffix(filepath.Base(realPath), ".git"), Bare: true}
	} else {
		if _, err := os.Lstat(filepath.Join(realPath, ".git")); err != nil {
			return Repository{}, false
		}
		var err error
		g, err = gitx.Discover(ctx, realPath)
		if err != nil {
			return Repository{}, false
		}
		if g.IsLinkedWorktree {
			kind = Worktree
		}
	}

	rel, err := filepath.Rel(root, displayPath)
	if err != nil || rel == "." {
		rel = filepath.Base(displayPath)
	}
	r := Repository{
		Path: displayPath, RealPath: realPath, Root: root, Relative: filepath.ToSlash(rel),
		Name: g.Name, Kind: kind, Depth: depth,
		CommonDir: g.GitCommonDir, MainRoot: g.MainRoot,
		Symlink: symlink,
	}
	if symlink {
		r.SymlinkTarget = realPath
	}
	if kind != Bare {
		r.Status, _ = gitx.StatusOf(ctx, realPath)
		r.Branch = r.Status.Branch
		r.Remote = gitx.Remote(ctx, realPath, "origin")
	}
	return r, true
}

func isBare(path string) bool {
	for _, marker := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(path, marker)); err != nil {
			return false
		}
	}
	// A normal checkout also has HEAD under .git, not at its root.
	_, dotGitErr := os.Lstat(filepath.Join(path, ".git"))
	return errors.Is(dotGitErr, fs.ErrNotExist)
}

func kindRank(k Kind) int {
	switch k {
	case Canonical:
		return 0
	case Worktree:
		return 1
	case Bare:
		return 2
	}
	return 3
}

// PathError is a non-fatal scan warning.
type PathError struct {
	Path string
	Err  error
}

func (e *PathError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *PathError) Unwrap() error { return e.Err }

// EnrichAliases adds paths from other to matching checkouts in base without
// adding new repositories. It is used before a move: the explicit roots decide
// what may move, while configured roots (often a symlink index) contribute
// aliases that would be broken by that move.
func EnrichAliases(base []Repository, other []Repository) []Repository {
	byKey := map[string]int{}
	for i := range base {
		byKey[base[i].CheckoutKey()] = i
	}
	for _, r := range other {
		i, ok := byKey[r.CheckoutKey()]
		if !ok {
			continue
		}
		seen := map[string]bool{base[i].Path: true}
		for _, a := range base[i].Aliases {
			seen[a.Path] = true
		}
		add := func(path, target string) {
			if path == "" || seen[path] {
				return
			}
			seen[path] = true
			base[i].Aliases = append(base[i].Aliases, Alias{Path: path, Target: target})
		}
		if r.Path != base[i].Path {
			add(r.Path, r.RealPath)
		}
		for _, a := range r.Aliases {
			add(a.Path, a.Target)
		}
	}
	return base
}
