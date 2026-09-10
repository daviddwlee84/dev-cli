package submodule

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// Removal is run-local authority, never a durable clean/remote-backed receipt.
// All filesystem changes are delayed until caller/runtime/task guards passed.
type Removal struct {
	roots      map[string]os.FileInfo
	Graph      gitx.SubmoduleGraph
	Repository gitx.Repo
	Workspace  Workspace
	Revision   string
	Config     config.Config
	Merged     bool
	remoteRefs map[string]string
}

func InspectPublication(ctx context.Context, cfg config.Config, root string, merged bool) (*Removal, error) {
	g, err := gitx.SubmodulesOf(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(g.Nodes) == 0 {
		return nil, nil
	}
	r, err := gitx.Discover(ctx, root)
	if err != nil {
		return nil, err
	}
	st, err := gitx.StatusOf(ctx, root)
	if err != nil {
		return nil, err
	}
	w, revision, err := Load(cfg, r.GitCommonDir, st.Branch)
	if err != nil {
		return nil, err
	}
	for _, n := range g.Nodes {
		if !n.Initialized || n.Status.Dirty() || n.HEAD != n.Gitlink {
			return nil, fmt.Errorf("submodule %s must be committed and match its gitlink before publishing/integrating the parent", n.Path)
		}
	}
	return &Removal{Graph: g, Repository: r, Workspace: w, Revision: revision, Config: cfg, Merged: merged, remoteRefs: map[string]string{}}, nil
}

// InspectRemoval is local only. Destructive adapters must call it even when
// Recursive is false: the flag authorizes disposal, not skipping inspection.
func InspectRemoval(ctx context.Context, cfg config.Config, root string, recursive, merged bool) (*Removal, error) {
	g, err := gitx.SubmodulesOf(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(g.Nodes) == 0 {
		return nil, nil
	}
	if !recursive {
		return nil, errors.New("submodules require an explicit --recursive cleanup plan; child repositories are retained")
	}
	r, err := gitx.Discover(ctx, root)
	if err != nil {
		return nil, err
	}
	if !r.IsLinkedWorktree {
		return nil, errors.New("recursive removal cannot remove a canonical checkout")
	}
	registered, err := gitx.ResolveRegisteredWorktree(ctx, root, r.Root)
	if err != nil {
		return nil, err
	}
	if !registered.IsLinkedWorktree() || registered.Worktree.Locked || registered.Worktree.Prunable {
		return nil, errors.New("recursive removal requires an exact unlocked linked checkout")
	}
	w, revision, err := Load(cfg, r.GitCommonDir, registered.Worktree.Branch)
	if err != nil {
		return nil, err
	}
	plan := &Removal{Graph: g, Repository: r, Workspace: w, Revision: revision, Config: cfg, Merged: merged, remoteRefs: map[string]string{}}
	plan.roots = map[string]os.FileInfo{}
	for _, path := range []string{r.Root, r.GitDir, filepath.Join(r.GitDir, "modules")} {
		held, info, err := safefile.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		held.Close()
		plan.roots[path] = info
	}
	for _, n := range g.Nodes {
		if len(n.Blockers) > 0 {
			return nil, fmt.Errorf("submodule %s blocks cleanup: %s", n.Path, strings.Join(n.Blockers, "; "))
		}
		if !n.Initialized {
			return nil, fmt.Errorf("submodule %s must be initialized for a complete recursive recovery proof", n.Path)
		}
		owned, err := pathx.Contains(filepath.Join(r.GitDir, "modules"), n.GitDir)
		if err != nil || !owned || n.GitDir != n.CommonDir {
			return nil, fmt.Errorf("submodule %s has shared or externally owned Git storage", n.Path)
		}
		child := filepath.Join(g.Root, filepath.FromSlash(n.Path))
		for _, path := range []string{child, n.GitDir} {
			held, info, err := safefile.OpenRoot(path)
			if err != nil {
				return nil, err
			}
			held.Close()
			plan.roots[path] = info
		}
		worktrees, err := gitx.Worktrees(ctx, child)
		if err != nil {
			return nil, err
		}
		if len(worktrees) != 1 {
			return nil, fmt.Errorf("submodule %s still owns other worktrees", n.Path)
		}
		if err := inspectPrivateState(ctx, child, n.GitDir); err != nil {
			return nil, fmt.Errorf("submodule %s: %w", n.Path, err)
		}
	}
	if err := inspectModuleStorage(filepath.Join(r.GitDir, "modules"), g.Nodes, filepath.Join(r.GitDir, "modules")); err != nil {
		return nil, err
	}
	return plan, nil
}

// Old gitlinks may leave independent repositories in modules/ after their
// paths disappear from the index. Every store being disposed must be in the
// observed graph; a clean current gitlink is not authority for orphan stores.
func inspectModuleStorage(root string, nodes []gitx.SubmoduleNode, originalRoot string) error {
	known := map[string]bool{}
	for _, n := range nodes {
		rel, err := filepath.Rel(originalRoot, n.GitDir)
		if err != nil {
			return err
		}
		known[filepath.Join(root, rel)] = true
	}
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("unclaimed private module data at %s", path)
			}
			if known[path] {
				if err := walk(filepath.Join(path, "modules")); err != nil {
					return err
				}
				continue
			}
			if _, err := os.Lstat(filepath.Join(path, "HEAD")); err == nil {
				return fmt.Errorf("orphan submodule repository %s requires preservation", path)
			}
			if err := walk(path); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

func inspectPrivateState(ctx context.Context, root, gitdir string) error {
	index, err := gitx.Run(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	gitlinks := map[string]bool{}
	for _, record := range strings.Split(index, "\x00") {
		meta, path, ok := strings.Cut(record, "\t")
		if ok && strings.HasPrefix(meta, "160000 ") {
			gitlinks[filepath.Join(root, filepath.FromSlash(path))] = true
		}
	}
	visited := 0
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited++
		if visited > 500000 {
			return errors.New("worktree inspection limit exceeded")
		}
		if path == root {
			return nil
		}
		if gitlinks[path] && d.IsDir() {
			return filepath.SkipDir
		}
		if strings.EqualFold(d.Name(), ".git") {
			if filepath.Dir(path) != root {
				return fmt.Errorf("nested repository %s requires independent preservation", filepath.Dir(path))
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	}); err != nil {
		return err
	}
	allowed := map[string]bool{"HEAD": true, "index": true, "config": true, "description": true, "objects": true, "refs": true, "logs": true, "hooks": true, "info": true, "packed-refs": true, "FETCH_HEAD": true, "ORIG_HEAD": true, "modules": true, "dev-taskflow": true, ".dev-taskflow.lock": true}
	allowed["lfs"] = true
	allowed["COMMIT_EDITMSG"] = true
	if message, err := safefile.ReadRegular(ctx, filepath.Join(gitdir, "COMMIT_EDITMSG"), 1024*1024); err == nil {
		committed, err := gitx.Run(ctx, root, "log", "-1", "--format=%B")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(message)) != strings.TrimSpace(committed) {
			return errors.New("uncommitted commit-message draft requires preservation")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if description, err := safefile.ReadRegular(ctx, filepath.Join(gitdir, "description"), 1024*1024); err == nil {
		if strings.TrimSpace(string(description)) != "Unnamed repository; edit this file 'description' to name the repository." {
			return errors.New("private repository description requires preservation")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := filepath.WalkDir(filepath.Join(gitdir, "lfs"), func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return errors.New("LFS data requires independent recovery proof")
		}
		return nil
	}); err != nil {
		return err
	}
	entries, err := os.ReadDir(gitdir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("private Git data %s requires preservation", entry.Name())
		}
	}
	infoEntries, err := os.ReadDir(filepath.Join(gitdir, "info"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range infoEntries {
		if entry.Name() != "exclude" {
			return fmt.Errorf("private Git info/%s requires preservation", entry.Name())
		}
	}
	if exclude, err := os.ReadFile(filepath.Join(gitdir, "info", "exclude")); err == nil {
		for _, line := range strings.Split(string(exclude), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				return errors.New("private Git exclude rules require preservation")
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, path := range []string{"shallow", "objects/info/alternates", "info/sparse-checkout", "rr-cache"} {
		if _, err := os.Lstat(filepath.Join(gitdir, filepath.FromSlash(path))); err == nil {
			return fmt.Errorf("%s requires independent preservation", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	files, err := gitx.Run(ctx, root, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, rec := range strings.Split(files, "\x00") {
		if rec != "" && (rec[0] == 'S' || (rec[0] >= 'a' && rec[0] <= 'z')) {
			return errors.New("hidden index content requires independent inspection")
		}
	}
	attrs, err := gitx.Run(ctx, root, "ls-files", "-z")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(attrs, "\x00") {
		if p == "" {
			continue
		}
		a, err := gitx.Run(ctx, root, "check-attr", "filter", "working-tree-encoding", "--", p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(a, "\n") {
			if !strings.HasSuffix(line, ": unspecified") && !strings.HasSuffix(line, ": unset") {
				return errors.New("filtered or encoded working files require independent recovery proof")
			}
		}
	}
	hooks, err := os.ReadDir(filepath.Join(gitdir, "hooks"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, h := range hooks {
		if !strings.HasSuffix(h.Name(), ".sample") {
			return errors.New("custom repository hooks require preservation")
		}
	}
	// A clone's normal configuration is reproducible. Arbitrary local config
	// and includes may hold durable intent or credentials and are not disposed.
	settings, err := gitx.Run(ctx, root, "config", "--local", "--name-only", "--list")
	if err != nil {
		return err
	}
	for _, key := range strings.Split(settings, "\n") {
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "core.symlinks", "core.worktree", "remote.origin.url", "remote.origin.fetch", "extensions.objectformat":
			continue
		}
		if strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge")) {
			continue
		}
		if strings.HasPrefix(key, "submodule.") && (strings.HasSuffix(key, ".url") || strings.HasSuffix(key, ".active")) {
			continue
		}
		return fmt.Errorf("local Git setting %s requires preservation", key)
	}
	return nil
}

// VerifyRemote fetches into a fresh isolated bare repository. Reachability is
// proven against the remote's advertised refs, never local tracking refs or
// objects borrowed from the checkout about to be removed.
func (p *Removal) VerifyRemote(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var removedIdentities []os.FileInfo
	for _, node := range p.Graph.Nodes {
		for _, path := range []string{filepath.Join(p.Graph.Root, filepath.FromSlash(node.Path)), node.GitDir, filepath.Join(p.Graph.Root, filepath.FromSlash(node.Path), ".git")} {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			removedIdentities = append(removedIdentities, info)
		}
	}
	for _, n := range p.Graph.Nodes {
		child := filepath.Join(p.Graph.Root, filepath.FromSlash(n.Path))
		url, err := gitx.Run(ctx, child, "remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("submodule %s has no recovery origin", n.Path)
		}
		expected, err := gitx.SubmoduleSourceURL(ctx, filepath.Join(p.Graph.Root, filepath.FromSlash(n.Parent)), n.URL)
		if err != nil || expected != url {
			return fmt.Errorf("submodule %s recovery origin differs from its declared source", n.Path)
		}
		local, localErr := localRecoveryPath(child, url)
		if localErr != nil {
			return localErr
		}
		if local != "" {
			info, err := os.Stat(local)
			if err != nil {
				return fmt.Errorf("recovery origin is unavailable: %w", err)
			}
			for _, removed := range removedIdentities {
				if os.SameFile(info, removed) {
					return errors.New("recovery origin depends on the workspace being removed")
				}
			}
			for _, removedRoot := range []string{p.Graph.Root, p.Repository.GitDir} {
				inside, err := pathx.Contains(removedRoot, local)
				if err != nil || inside {
					return errors.New("recovery origin depends on the workspace being removed")
				}
			}
		}
		before, err := gitx.Run(ctx, child, "ls-remote", "--refs", "origin")
		if err != nil {
			return fmt.Errorf("submodule %s recovery origin unavailable", n.Path)
		}
		proof, err := os.MkdirTemp("", "dev-submodule-proof-")
		if err != nil {
			return err
		}
		err = func() error {
			defer os.RemoveAll(proof)
			if _, err := gitx.Run(ctx, proof, "init", "--bare", "--quiet"); err != nil {
				return err
			}
			if _, err := gitx.Run(ctx, proof, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--", url, "+refs/*:refs/*"); err != nil {
				return errors.New("could not fetch isolated recovery proof")
			}
			after, err := gitx.Run(ctx, child, "ls-remote", "--refs", "origin")
			if err != nil || before != after {
				return errors.New("recovery remote refs changed during proof")
			}
			objects, err := gitx.Run(ctx, proof, "rev-list", "--objects", "--all")
			if err != nil {
				return err
			}
			available := map[string]bool{}
			for _, line := range strings.Split(objects, "\n") {
				if f := strings.Fields(line); len(f) > 0 {
					available[f[0]] = true
				}
			}
			advertised := map[string]string{}
			for _, line := range strings.Split(after, "\n") {
				if f := strings.Fields(line); len(f) == 2 {
					advertised[f[1]] = f[0]
				}
			}
			refs, err := gitx.Run(ctx, child, "for-each-ref", "--format=%(refname) %(objectname)")
			if err != nil {
				return err
			}
			for _, line := range strings.Split(refs, "\n") {
				f := strings.Fields(line)
				if len(f) != 2 {
					continue
				}
				if strings.HasPrefix(f[0], "refs/remotes/") {
					continue
				}
				if advertised[f[0]] == "" || !available[f[1]] {
					return fmt.Errorf("local ref %s is not preserved by the recovery remote", f[0])
				}
			}
			if !available[n.HEAD] || !available[n.Gitlink] {
				return errors.New("HEAD or gitlink is not remotely reachable")
			}
			reflog, err := gitx.Run(ctx, child, "reflog", "show", "--all", "--format=%H")
			if err != nil {
				return err
			}
			for _, oid := range strings.Fields(reflog) {
				if !available[oid] {
					return errors.New("reflog retains a local-only commit")
				}
			}
			unreachable, err := gitx.Run(ctx, child, "fsck", "--unreachable", "--no-reflogs", "--no-progress")
			if err != nil {
				return errors.New("object integrity inspection failed")
			}
			for _, line := range strings.Split(unreachable, "\n") {
				f := strings.Fields(line)
				if len(f) == 3 && (f[0] == "unreachable" || f[0] == "dangling") && !available[f[2]] {
					return errors.New("unreachable local objects require preservation")
				}
			}
			if p.Merged {
				for _, m := range p.Workspace.Members {
					if m.Path == n.Path {
						base := strings.TrimPrefix(strings.TrimPrefix(m.Base, "refs/remotes/"), "origin/")
						if !strings.HasPrefix(base, "refs/") {
							base = "refs/heads/" + base
						}
						if _, err := gitx.Run(ctx, proof, "merge-base", "--is-ancestor", n.HEAD, base); err != nil {
							return fmt.Errorf("member is not integrated into %s", base)
						}
					}
				}
			}
			return nil
		}()
		if err != nil {
			return fmt.Errorf("submodule %s: %w", n.Path, err)
		}
		p.remoteRefs[n.Path] = before
	}
	return nil
}

func localRecoveryPath(cwd, source string) (string, error) {
	if strings.HasPrefix(source, "file://") {
		u, err := url.Parse(source)
		if err != nil {
			return "", errors.New("invalid recovery file URL")
		}
		if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return "", errors.New("ambiguous recovery file URL")
		}
		path := u.Path
		if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
			path = "//" + u.Host + path
		}
		if filepath.Separator == '\\' && len(path) > 2 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		return filepath.FromSlash(path), nil
	}
	if filepath.IsAbs(source) {
		return source, nil
	}
	if !strings.Contains(source, ":") {
		return filepath.Join(cwd, source), nil
	}
	return "", nil
}

type moveRecord struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Empty bool   `json:"empty_placeholder"`
}
type removalJournal struct {
	Version    int          `json:"version"`
	Root       string       `json:"root"`
	GitDir     string       `json:"git_dir"`
	Repository string       `json:"repository"`
	Branch     string       `json:"branch"`
	Head       string       `json:"head"`
	Moves      []moveRecord `json:"moves"`
	Removed    bool         `json:"outer_removed"`
}

// Apply stages child data reversibly, leaves empty gitlinks, and invokes the
// existing guarded non-force outer removal. A receipt is synced before every
// rename. Failed operations restore original paths only when still unclaimed.
func (p *Removal) Apply(ctx context.Context, check func(context.Context, string) error, removeOuter func() error) error {
	if p == nil {
		return removeOuter()
	}
	if len(p.remoteRefs) != len(p.Graph.Nodes) {
		return errors.New("recursive removal has no fresh remote proof")
	}
	// Same lock namespace as taskflow. Parent is already locked by the caller.
	nodes := append([]gitx.SubmoduleNode(nil), p.Graph.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CommonDir < nodes[j].CommonDir })
	var leases []*lockx.Lease
	defer func() {
		for i := len(leases) - 1; i >= 0; i-- {
			leases[i].Close()
		}
	}()
	for _, n := range nodes {
		lease, err := lockx.AcquireDir(ctx, filepath.Join(n.CommonDir, "dev-taskflow"), "submodule repository")
		if err != nil {
			return err
		}
		leases = append(leases, lease)
	}
	fresh, err := gitx.SubmodulesOf(ctx, p.Graph.Root)
	if err != nil {
		return err
	}
	if fresh.Fingerprint != p.Graph.Fingerprint {
		return errors.New("stale submodule cleanup plan")
	}
	for path, info := range p.roots {
		if err := safefile.VerifyRoot(path, info); err != nil {
			return err
		}
	}
	_, revision, err := Load(p.Config, p.Workspace.Repository, p.Workspace.Branch)
	if err != nil {
		return err
	}
	if revision != p.Revision {
		return errors.New("stale workspace member intent")
	}
	for _, n := range nodes {
		child := filepath.Join(p.Graph.Root, filepath.FromSlash(n.Path))
		if check != nil {
			if err := check(ctx, child); err != nil {
				return err
			}
		}
		refs, err := gitx.Run(ctx, child, "ls-remote", "--refs", "origin")
		if err != nil || refs != p.remoteRefs[n.Path] {
			return fmt.Errorf("submodule %s remote proof is stale", n.Path)
		}
		if err := inspectPrivateState(ctx, child, n.GitDir); err != nil {
			return err
		}
	}
	area, err := os.MkdirTemp(filepath.Dir(p.Graph.Root), ".dev-submodule-retirement-")
	if err != nil {
		return err
	}
	head, err := gitx.Run(ctx, p.Graph.Root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	journal := removalJournal{Version: 1, Root: p.Graph.Root, GitDir: p.Repository.GitDir, Repository: p.Repository.MainRoot, Branch: p.Workspace.Branch, Head: head}
	journalPath := filepath.Join(area, "journal.json")
	persist := func() error { return writeJSON(journalPath, journal) }
	rollback := func(cause error) error {
		for i := len(journal.Moves) - 1; i >= 0; i-- {
			m := journal.Moves[i]
			if _, err := os.Lstat(m.To); os.IsNotExist(err) {
				continue
			}
			if _, err := os.Lstat(m.From); err == nil {
				if !m.Empty {
					return fmt.Errorf("%w; original path reused; inspect %s", cause, journalPath)
				}
				if err := os.Remove(m.From); err != nil {
					return fmt.Errorf("%w; cannot restore; inspect %s", cause, journalPath)
				}
			}
			if err := os.Rename(m.To, m.From); err != nil {
				return fmt.Errorf("%w; restore failed; inspect %s", cause, journalPath)
			}
		}
		os.RemoveAll(area)
		return cause
	}
	move := func(from, to string, empty bool) error {
		if err := safefile.VerifyRoot(from, p.roots[from]); err != nil {
			return err
		}
		journal.Moves = append(journal.Moves, moveRecord{From: from, To: to, Empty: empty})
		if err := persist(); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if err := safefile.VerifyRoot(to, p.roots[from]); err != nil {
			return err
		}
		if empty {
			return os.Mkdir(from, 0755)
		}
		return nil
	}
	// Children leave first; parent moves retain their empty placeholders.
	for i := len(p.Graph.Nodes) - 1; i >= 0; i-- {
		n := p.Graph.Nodes[i]
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		if err := move(filepath.Join(p.Graph.Root, filepath.FromSlash(n.Path)), filepath.Join(area, fmt.Sprintf("checkout-%d", i)), true); err != nil {
			return rollback(err)
		}
	}
	// Release file handles before moving private admin directories on Windows.
	for i := len(leases) - 1; i >= 0; i-- {
		if err := leases[i].Close(); err != nil {
			return rollback(err)
		}
	}
	if err := move(filepath.Join(p.Repository.GitDir, "modules"), filepath.Join(area, "git-modules"), false); err != nil {
		return rollback(err)
	}
	if err := inspectModuleStorage(filepath.Join(area, "git-modules"), p.Graph.Nodes, filepath.Join(p.Repository.GitDir, "modules")); err != nil {
		return rollback(err)
	}
	if err := removeOuter(); err != nil {
		return rollback(err)
	}
	journal.Removed = true
	if err := persist(); err != nil {
		return fmt.Errorf("outer removed; cleanup receipt retained at %s: %w", journalPath, err)
	}
	if err := os.RemoveAll(area); err != nil {
		return fmt.Errorf("outer removed; private submodule data retained at %s: %w", journalPath, err)
	}
	return nil
}
