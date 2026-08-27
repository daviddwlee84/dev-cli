// Package repo discovers the repositories a user actually owns by walking the
// configured scan roots. It answers "what projects do I have?", which is a
// different question from "what am I working on?" — that one belongs to
// package task.
package repo

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Repo is one discovered repository.
type Repo struct {
	// Name is the directory basename, which is how users refer to it.
	Name string
	// Path is the absolute working-tree root.
	Path string
	// Root is the scan root it was found under.
	Root string
	// Category is the path between the scan root and the repo ("Quant",
	// "Web/Frontend"), empty for a repo sitting directly in the root. It is
	// human organisation metadata, preserved for `dev graduate` to place new
	// projects consistently.
	Category string
	// Bare reports a bare repository (a worktree hub).
	Bare bool
	// HasGit distinguishes a real repo from a plain directory that was found
	// in a scan root; non-repos are reported so `dev repo list` can offer to
	// initialise them.
	HasGit bool
}

// Display renders "Category/Name" or just "Name".
func (r Repo) Display() string {
	if r.Category == "" {
		return r.Name
	}
	return r.Category + "/" + r.Name
}

// Options tunes discovery.
type Options struct {
	// MaxDepth limits how deep below a scan root a repo may be found. The
	// default layout is <root>/<Category>/<Repo>, so 2 suffices; 3 leaves room
	// for a nested grouping without walking an entire home directory.
	MaxDepth int
	// IncludeNonRepos reports plain directories alongside repositories.
	IncludeNonRepos bool
}

// DefaultOptions is the discovery configuration used by dev's commands.
func DefaultOptions() Options { return Options{MaxDepth: 3} }

// skipDirs are never descended into. These are large, never contain a project
// the user organises by hand, and walking them dominates scan time.
var skipDirs = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "target": true,
	"vendor": true, "__pycache__": true, ".tox": true, "dist": true,
	"build": true, ".next": true, ".cache": true, ".terraform": true,
	"Library": true, ".Trash": true,
}

// Discover walks every root and returns the repositories found, sorted by
// display name. Unreadable directories are skipped rather than fatal — a scan
// root that has been unmounted must not break `dev ls`.
func Discover(ctx context.Context, roots []string, opts Options) ([]Repo, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultOptions().MaxDepth
	}
	seen := map[string]bool{}
	var out []Repo

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue // a missing scan root is normal on a fresh machine
		}
		rootClean := filepath.Clean(root)

		err = filepath.WalkDir(rootClean, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry: skip, keep walking
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !d.IsDir() || path == rootClean {
				return nil
			}
			name := d.Name()
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".bare") {
				return filepath.SkipDir
			}

			rel, _ := filepath.Rel(rootClean, path)
			depth := len(strings.Split(rel, string(filepath.Separator)))

			if isRepoDir(path) {
				if !seen[path] {
					seen[path] = true
					out = append(out, Repo{
						Name:     name,
						Path:     path,
						Root:     rootClean,
						Category: filepath.ToSlash(filepath.Dir(rel)),
						Bare:     isBareDir(path),
						HasGit:   true,
					})
				}
				// Never descend into a repo: its subdirectories are source
				// code, and any nested .git is a submodule or a worktree that
				// belongs to this repo, not a project of its own.
				return filepath.SkipDir
			}
			if depth >= opts.MaxDepth {
				return filepath.SkipDir
			}
			if opts.IncludeNonRepos && depth == 1 {
				if !seen[path] {
					seen[path] = true
					out = append(out, Repo{Name: name, Path: path, Root: rootClean, Category: ""})
				}
			}
			return nil
		})
		if err != nil && ctx.Err() != nil {
			return out, ctx.Err()
		}
	}

	for i := range out {
		if out[i].Category == "." {
			out[i].Category = ""
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// isRepoDir reports whether path is a repository root: it holds a .git entry
// (directory for a normal clone, file for a linked worktree or submodule).
func isRepoDir(path string) bool {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return isBareDir(path)
}

// isBareDir recognises a bare repository by its skeleton, without shelling out.
func isBareDir(path string) bool {
	for _, marker := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(path, marker)); err != nil {
			return false
		}
	}
	return true
}

// Resolve finds a repo by name, display name, or path.
//
// Matching is deliberately layered: an exact name or path wins outright, and
// only then does a substring search run — so a repo literally named "web"
// is never shadowed by "web-frontend".
func Resolve(ctx context.Context, roots []string, ref string) (Repo, []Repo, error) {
	all, err := Discover(ctx, roots, DefaultOptions())
	if err != nil {
		return Repo{}, nil, err
	}
	if abs, err := filepath.Abs(ref); err == nil {
		if r, err := gitx.Discover(ctx, abs); err == nil {
			return Repo{Name: r.Name, Path: r.MainRoot, HasGit: true, Bare: r.Bare}, nil, nil
		}
	}
	for _, r := range all {
		if r.Name == ref || r.Display() == ref || r.Path == ref {
			return r, nil, nil
		}
	}
	needle := strings.ToLower(ref)
	var hits []Repo
	for _, r := range all {
		if strings.Contains(strings.ToLower(r.Display()), needle) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil, nil
	case 0:
		return Repo{}, nil, &NotFoundError{Ref: ref}
	default:
		return Repo{}, hits, &AmbiguousError{Ref: ref, Candidates: hits}
	}
}

// NotFoundError reports a repo reference that matched nothing.
type NotFoundError struct{ Ref string }

func (e *NotFoundError) Error() string { return "no repository matching " + e.Ref }

// AmbiguousError reports a repo reference that matched several repos.
type AmbiguousError struct {
	Ref        string
	Candidates []Repo
}

func (e *AmbiguousError) Error() string {
	names := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		names[i] = c.Display()
	}
	return e.Ref + " is ambiguous: " + strings.Join(names, ", ")
}
