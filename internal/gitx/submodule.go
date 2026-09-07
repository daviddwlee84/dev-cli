package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// SubmoduleNode describes one exact gitlink edge, relative to Graph.Root.
// Unknown is never represented as a clean initialized checkout.
type SubmoduleNode struct {
	CheckoutIdentity string   `json:"checkout_identity,omitempty"`
	GitIdentity      string   `json:"git_identity,omitempty"`
	Path             string   `json:"path"`
	Parent           string   `json:"parent"`
	Name             string   `json:"name"`
	URL              string   `json:"-"`
	Gitlink          string   `json:"gitlink"`
	HEAD             string   `json:"head,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	GitDir           string   `json:"git_dir,omitempty"`
	CommonDir        string   `json:"common_dir,omitempty"`
	Initialized      bool     `json:"initialized"`
	State            string   `json:"state"`
	Status           Status   `json:"status"`
	Ignored          int      `json:"ignored"`
	Blockers         []string `json:"blockers,omitempty"`
}

type SubmoduleGraph struct {
	RootIdentity string          `json:"root_identity"`
	Root         string          `json:"root"`
	Nodes        []SubmoduleNode `json:"submodules"`
	Fingerprint  string          `json:"fingerprint"`
}

// SubmodulesOf never fetches and never descends across an unverified Git root.
func SubmodulesOf(ctx context.Context, dir string) (SubmoduleGraph, error) {
	r, err := Discover(ctx, dir)
	if err != nil {
		return SubmoduleGraph{}, err
	}
	g := SubmoduleGraph{Root: r.Root, Nodes: []SubmoduleNode{}}
	g.RootIdentity, err = submoduleDirectoryIdentity(r.Root)
	if err != nil {
		return g, err
	}
	seen := map[string]bool{}
	var walk func(string, string, int) error
	walk = func(root, parent string, depth int) error {
		if depth > 32 || len(g.Nodes) > 1024 {
			return errors.New("submodule graph exceeds inspection limit")
		}
		identity, err := Discover(ctx, root)
		if err != nil {
			return err
		}
		if !sameCanonicalPath(identity.Root, root) {
			return fmt.Errorf("%s is not an exact submodule checkout", root)
		}
		if seen[identity.GitDir] {
			return fmt.Errorf("repeated submodule Git directory %s", identity.GitDir)
		}
		seen[identity.GitDir] = true
		entries, err := submoduleEntries(ctx, root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := validateSubmodulePath(root, entry.Path); err != nil {
				return err
			}
			rel := filepath.ToSlash(filepath.Join(parent, entry.Path))
			child, err := pathx.CanonicalChild(root, filepath.Join(root, filepath.FromSlash(entry.Path)))
			if err != nil {
				return fmt.Errorf("submodule %s: %w", rel, err)
			}
			n := SubmoduleNode{Path: rel, Parent: parent, Name: entry.Name, URL: entry.URL, Gitlink: entry.Gitlink, State: "uninitialized"}
			info, statErr := os.Lstat(child)
			if statErr != nil && !os.IsNotExist(statErr) {
				return gError(rel, statErr)
			}
			if statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("submodule %s is not a regular directory", rel)
			}
			_, markerErr := os.Lstat(filepath.Join(child, ".git"))
			if markerErr != nil && !os.IsNotExist(markerErr) {
				return gError(rel, markerErr)
			}
			if os.IsNotExist(markerErr) {
				if statErr == nil {
					contents, err := os.ReadDir(child)
					if err != nil {
						return gError(rel, err)
					}
					if len(contents) != 0 {
						n.State = "unknown"
						n.Blockers = append(n.Blockers, "uninitialized directory contains files")
					}
				}
				g.Nodes = append(g.Nodes, n)
				continue
			}
			cr, err := Discover(ctx, child)
			if err != nil || !sameCanonicalPath(cr.Root, child) {
				return fmt.Errorf("submodule %s has invalid checkout identity", rel)
			}
			n.Initialized, n.State, n.GitDir, n.CommonDir = true, "available", cr.GitDir, cr.GitCommonDir
			n.CheckoutIdentity, err = submoduleDirectoryIdentity(child)
			if err != nil {
				return err
			}
			n.GitIdentity, err = submoduleDirectoryIdentity(cr.GitDir)
			if err != nil {
				return err
			}
			n.HEAD, err = run(ctx, child, "rev-parse", "HEAD")
			if err != nil {
				return gError(rel, err)
			}
			raw, err := run(ctx, child, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=none", "-z")
			if err != nil {
				return gError(rel, err)
			}
			n.Status = statusFromOutput(child, raw)
			n.Branch = n.Status.Branch
			ignored, err := run(ctx, child, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
			if err != nil {
				return gError(rel, err)
			}
			n.Ignored = nonEmptyNULRecords(ignored)
			if n.HEAD != n.Gitlink {
				n.Blockers = append(n.Blockers, "HEAD differs from parent gitlink")
			}
			if n.Status.Dirty() {
				n.Blockers = append(n.Blockers, "uncommitted content")
			}
			if n.Ignored > 0 {
				n.Blockers = append(n.Blockers, "ignored content")
			}
			if operation, active, err := InProgress(child); err != nil {
				return gError(rel, err)
			} else if active {
				n.Blockers = append(n.Blockers, "Git operation: "+operation)
			}
			if exists, err := RefState(ctx, child, "refs/stash"); err != nil {
				return gError(rel, err)
			} else if exists {
				n.Blockers = append(n.Blockers, "stash requires independent preservation")
			}
			g.Nodes = append(g.Nodes, n)
			if err := walk(child, rel, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(r.Root, "", 0); err != nil {
		return g, err
	}
	// Include local refs and content, not only status counts: a same-count edit
	// or newly created local branch must invalidate cleanup authority.
	h := sha256.New()
	h.Write([]byte(g.RootIdentity))
	data, _ := json.Marshal(g.Nodes)
	h.Write(data)
	for _, n := range g.Nodes {
		h.Write([]byte(n.URL))
		if !n.Initialized {
			continue
		}
		root := filepath.Join(g.Root, filepath.FromSlash(n.Path))
		for _, args := range [][]string{{"for-each-ref", "--format=%(refname) %(objectname)"}, {"reflog", "show", "--all", "--format=%H"}, {"diff", "--binary", "HEAD", "--", "."}} {
			out, err := run(ctx, root, args...)
			if err != nil {
				return g, err
			}
			h.Write([]byte(out))
			h.Write([]byte{0})
		}
	}
	g.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return g, nil
}

func gError(path string, err error) error { return fmt.Errorf("submodule %s: %w", path, err) }

func validateSubmodulePath(root, rel string) error {
	if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.Contains(rel, "\\") || filepath.ToSlash(filepath.Clean(rel)) != rel || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("invalid submodule path %q", rel)
	}
	p := root
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("submodule path %s crosses a symlink", rel)
		}
	}
	return nil
}

func submoduleEntries(ctx context.Context, dir string) ([]SubmoduleNode, error) {
	index, err := run(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	var entries []SubmoduleNode
	for _, record := range nulLines(index) {
		meta, path, ok := strings.Cut(record, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[0] != "160000" {
			continue
		}
		if f[2] != "0" {
			return nil, fmt.Errorf("conflicted gitlink %s", path)
		}
		entries = append(entries, SubmoduleNode{Path: path, Gitlink: f[1]})
	}
	if len(entries) == 0 {
		return entries, nil
	}
	// Read the indexed .gitmodules, not a mutable working file or inherited
	// global configuration. Names and paths are NUL-safe.
	modules, err := run(ctx, dir, "config", "--null", "--blob", ":.gitmodules", "--get-regexp", `^submodule\..*\.(path|url)$`)
	if err != nil {
		return nil, fmt.Errorf("read indexed .gitmodules: %w", err)
	}
	paths, urls := map[string]string{}, map[string]string{}
	for _, record := range nulLines(modules) {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return nil, errors.New("invalid .gitmodules entry")
		}
		if strings.HasSuffix(key, ".path") {
			paths[strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")] = value
		}
		if strings.HasSuffix(key, ".url") {
			urls[strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".url")] = value
		}
	}
	for i := range entries {
		matches := 0
		for name, path := range paths {
			if path == entries[i].Path {
				matches++
				entries[i].Name = name
				entries[i].URL = urls[name]
			}
		}
		if matches != 1 || entries[i].URL == "" {
			return nil, fmt.Errorf("gitlink %s has no unique .gitmodules mapping", entries[i].Path)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// InitSubmodules only materializes missing checkouts. Existing content/HEADs
// are never reset. Each level uses command-scoped configuration so shared
// superproject config is not rewritten by submodule init.
func InitSubmodules(ctx context.Context, dir string) (SubmoduleGraph, error) {
	for {
		g, err := SubmodulesOf(ctx, dir)
		if err != nil {
			return g, err
		}
		changed := false
		for _, n := range g.Nodes {
			if n.Initialized {
				continue
			}
			if n.State != "uninitialized" {
				return g, fmt.Errorf("submodule %s: %s", n.Path, strings.Join(n.Blockers, "; "))
			}
			parent := filepath.Join(g.Root, filepath.FromSlash(n.Parent))
			url, err := resolveSubmoduleURL(ctx, parent, n.URL)
			if err != nil {
				return g, err
			}
			key := "submodule." + n.Name
			_, err = run(ctx, parent, "-c", key+".url="+url, "-c", key+".active=true", "-c", key+".update=checkout", "submodule", "update", "--init", "--checkout", "--", relativeSubmodulePath(n))
			if err != nil {
				return g, fmt.Errorf("initialize submodule %s; checkout retained (retry dev submodule init)", n.Path)
			}
			changed = true
		}
		if !changed {
			return g, nil
		}
	}
}

func relativeSubmodulePath(n SubmoduleNode) string {
	if n.Parent == "" {
		return n.Path
	}
	return strings.TrimPrefix(n.Path, n.Parent+"/")
}

func resolveSubmoduleURL(ctx context.Context, parent, url string) (string, error) {
	if url == "" || strings.HasPrefix(url, "-") || strings.ContainsAny(url, "\x00\r\n") {
		return "", errors.New("invalid submodule source URL")
	}
	if !strings.HasPrefix(url, "./") && !strings.HasPrefix(url, "../") {
		return url, nil
	}
	base, err := run(ctx, parent, "remote", "get-url", "origin")
	if err != nil {
		return "", errors.New("relative submodule URL requires an origin remote")
	}
	// Git resolves relative submodule URLs against the repository URL itself.
	for strings.HasPrefix(url, "../") {
		url = strings.TrimPrefix(url, "../")
		i := strings.LastIndex(strings.TrimSuffix(base, "/"), "/")
		if i < 0 {
			return "", errors.New("cannot resolve relative submodule URL")
		}
		base = base[:i]
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(url, "./"), nil
}

// SubmoduleSourceURL resolves only the declared gitlink source, not a child
// checkout's potentially redirected origin.
func SubmoduleSourceURL(ctx context.Context, parent, url string) (string, error) {
	return resolveSubmoduleURL(ctx, parent, url)
}
