package submodule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const removalLayoutLimit = 8192

type layoutDirectory struct {
	Identity string   `json:"identity"`
	Mode     uint32   `json:"mode"`
	Listing  []string `json:"listing,omitempty"`
	Listed   bool     `json:"listed"`
	info     fs.FileInfo
}

type removalLayout struct {
	// Path keys use filepath.Clean so Git's slash spelling and native joins
	// address the same record. Physical identity and no-link checks stay exact.
	Directories map[string]layoutDirectory `json:"directories"`
	// Absence records the FIRST missing component, not merely a missing leaf.
	// Every existing ancestor below the held checkout/admin root is also bound.
	Absent      map[string]bool `json:"absent"`
	Initialized []string        `json:"initialized"`
	scaffolds   []string
}

func (l *removalLayout) fingerprint() string {
	data, _ := json.Marshal(l)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (l *removalLayout) record(path string, root *os.Root, info fs.FileInfo, listing bool) error {
	if len(l.Directories)+len(l.Absent) >= removalLayoutLimit {
		return errors.New("submodule removal layout exceeds inspection limit")
	}
	identity, err := gitx.DirectoryIdentity(path)
	if err != nil {
		return err
	}
	d := layoutDirectory{Identity: identity, Mode: uint32(info.Mode()), info: info}
	key := filepath.Clean(path)
	if prior, ok := l.Directories[key]; ok {
		if !os.SameFile(prior.info, info) {
			return safefile.ErrChanged
		}
		d = prior
	}
	if listing {
		file, err := root.Open(".")
		if err != nil {
			return err
		}
		names, readErr := file.Readdirnames(removalLayoutLimit + 1)
		closeErr := file.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(names) > removalLayoutLimit {
			return errors.New("submodule administration listing exceeds inspection limit")
		}
		sort.Strings(names)
		d.Listing, d.Listed = names, true
	}
	if err := safefile.VerifyRoot(path, info); err != nil {
		return err
	}
	l.Directories[key] = d
	return nil
}

// path observes a bounded component-by-component walk. Empty leaves cannot hide
// nested user directories, .git markers, ignored files, links or reparse points.
func (l *removalLayout) path(ctx context.Context, base, relative string, empty bool) error {
	root, info, err := safefile.OpenRoot(base)
	if err != nil {
		return err
	}
	defer func() { root.Close() }()
	if err := l.record(base, root, info, false); err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if relative == "." {
		parts = nil
	}
	if len(parts) > 64 {
		return errors.New("submodule layout path exceeds inspection depth")
	}
	path := base
	for _, name := range parts {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		child, childInfo, err := safefile.OpenChildRoot(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			// A failed open alone is not an absence observation: prove the name
			// really is missing relative to the same held ancestor.
			if _, statErr := root.Lstat(name); !errors.Is(statErr, fs.ErrNotExist) {
				return errors.Join(safefile.ErrChanged, statErr)
			}
			l.Absent[filepath.Join(path, name)] = true
			return safefile.VerifyRoot(path, info)
		}
		if err != nil {
			return err
		}
		if err := safefile.VerifyRoot(path, info); err != nil {
			child.Close()
			return err
		}
		root.Close()
		root, info, path = child, childInfo, filepath.Join(path, name)
		if err := l.record(path, root, info, false); err != nil {
			return err
		}
	}
	if empty {
		if err := l.record(path, root, info, true); err != nil {
			return err
		}
		if len(l.Directories[filepath.Clean(path)].Listing) != 0 {
			return fmt.Errorf("uninitialized submodule %s contains retained data", relative)
		}
	}
	return nil
}

func observeRemovalLayout(ctx context.Context, repo gitx.Repo, g gitx.SubmoduleGraph) (*removalLayout, error) {
	l := &removalLayout{Directories: map[string]layoutDirectory{}, Absent: map[string]bool{}}
	for _, path := range []string{repo.Root, repo.GitDir} {
		if err := l.path(ctx, path, ".", false); err != nil {
			return l, err
		}
	}
	known := map[string]bool{}
	modules := filepath.Join(repo.GitDir, "modules")
	for _, n := range g.Nodes {
		if err := l.path(ctx, repo.Root, n.Path, !n.Initialized); err != nil {
			return l, fmt.Errorf("submodule %s: %w", n.Path, err)
		}
		if n.Initialized {
			known[filepath.Clean(n.GitDir)] = true
			l.Initialized = append(l.Initialized, n.Path)
		}
	}
	sort.Strings(l.Initialized)
	if err := l.storage(ctx, repo.GitDir, "modules", known, 0); err != nil {
		return l, err
	}
	for path := range known {
		if _, ok := l.Directories[path]; !ok {
			return l, fmt.Errorf("initialized submodule storage %s is outside observed %s", path, modules)
		}
	}
	return l, nil
}

func (l *removalLayout) storage(ctx context.Context, parentPath, name string, known map[string]bool, depth int) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if depth > 64 {
		return errors.New("submodule administration exceeds inspection depth")
	}
	parent, parentInfo, err := safefile.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := l.record(parentPath, parent, parentInfo, false); err != nil {
		return err
	}
	root, info, err := safefile.OpenChildRoot(parent, name)
	path := filepath.Join(parentPath, name)
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := parent.Lstat(name); !errors.Is(statErr, fs.ErrNotExist) {
			return errors.Join(safefile.ErrChanged, statErr)
		}
		l.Absent[path] = true
		return safefile.VerifyRoot(parentPath, parentInfo)
	}
	if err != nil {
		return fmt.Errorf("unclaimed private module data at %s: %w", path, err)
	}
	defer root.Close()
	if err := l.record(path, root, info, true); err != nil {
		return err
	}
	if known[path] {
		// Leases are self-created coordination data, not layout changes. Other
		// private Git entries remain listed and inspectPrivateState checks them.
		d := l.Directories[path]
		var names []string
		for _, entry := range d.Listing {
			if entry != "dev-taskflow" && entry != ".dev-taskflow.lock" {
				names = append(names, entry)
			}
		}
		d.Listing = names
		l.Directories[path] = d
		return l.storage(ctx, path, "modules", known, depth+1)
	}
	l.scaffolds = append(l.scaffolds, path)
	for _, entry := range l.Directories[path].Listing {
		if entry == "HEAD" {
			return fmt.Errorf("orphan submodule repository %s requires preservation", path)
		}
		if err := l.storage(ctx, path, entry, known, depth+1); err != nil {
			return err
		}
	}
	return safefile.VerifyChildRoot(parent, name, info)
}

func inspectRemovalParent(ctx context.Context, root string) error {
	status, err := gitx.StatusOf(ctx, root)
	if err != nil {
		return err
	}
	if status.Dirty() {
		return errors.New("outer checkout has uncommitted changes (possibly a missing tracked gitlink); restore or commit them before recursive cleanup")
	}
	return nil
}

// FileInfo returned by a path-only Stat may defer loading its Windows file ID
// until SameFile is called. A containing directory can be moved before that
// comparison. Capture the held-root identity while the original path exists.
func captureRemovalPlaceholder(path string) (os.FileInfo, error) {
	root, info, err := safefile.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	return info, nil
}

func verifyStagedPlaceholders(ctx context.Context, placeholders map[string]os.FileInfo, moves []moveRecord) error {
	for path, expected := range placeholders {
		mapped, best := path, ""
		// A placeholder is created AFTER its own move. Only subsequent moves
		// of containing initialized parents relocate it into quarantine.
		for _, move := range moves {
			if strings.HasPrefix(path, move.From+string(filepath.Separator)) && len(move.From) > len(best) {
				best = move.From
				mapped = move.To + strings.TrimPrefix(path, move.From)
			}
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		root, info, err := safefile.OpenRoot(mapped)
		if err != nil {
			return err
		}
		if !os.SameFile(expected, info) {
			root.Close()
			return safefile.ErrChanged
		}
		l := &removalLayout{Directories: map[string]layoutDirectory{}, Absent: map[string]bool{}}
		err = l.record(mapped, root, info, true)
		root.Close()
		if err != nil {
			return err
		}
		if len(l.Directories[filepath.Clean(mapped)].Listing) != 0 {
			return fmt.Errorf("staged submodule placeholder is no longer empty: %s", path)
		}
	}
	return nil
}

func (p *Removal) revalidateLocal(ctx context.Context, expected *removalLayout) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	g, err := gitx.SubmodulesOf(ctx, p.Graph.Root)
	if err != nil {
		return err
	}
	if g.Fingerprint != p.Graph.Fingerprint {
		return errors.New("stale submodule cleanup plan")
	}
	if err := inspectRemovalParent(ctx, p.Graph.Root); err != nil {
		return err
	}
	_, revision, err := Load(p.Config, p.Workspace.Repository, p.Workspace.Branch)
	if err != nil {
		return err
	}
	if revision != p.Revision {
		return errors.New("stale workspace member intent")
	}
	return p.revalidateLayout(ctx, expected)
}

// Staging relocates only initialized directories. Empty children and missing
// components still carry their original exact identity/absence at the mapped
// location, including those inside a moved initialized parent.
func (p *Removal) verifyStagedLayout(ctx context.Context, moves []moveRecord) error {
	mapPath := func(path string) string {
		best := ""
		mapped := path
		for _, move := range moves {
			if (path == move.From || strings.HasPrefix(path, move.From+string(filepath.Separator))) && len(move.From) > len(best) {
				best = move.From
				mapped = move.To + strings.TrimPrefix(path, move.From)
			}
		}
		return mapped
	}
	known := map[string]bool{}
	for _, n := range p.initialized {
		known[filepath.Clean(n.GitDir)] = true
	}
	for path, expected := range p.layout.Directories {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		mapped := mapPath(path)
		root, info, err := safefile.OpenRoot(mapped)
		if err != nil {
			return err
		}
		if !os.SameFile(expected.info, info) {
			root.Close()
			return safefile.ErrChanged
		}
		l := &removalLayout{Directories: map[string]layoutDirectory{}, Absent: map[string]bool{}}
		err = l.record(mapped, root, info, expected.Listed)
		root.Close()
		if err != nil {
			return err
		}
		actual := l.Directories[filepath.Clean(mapped)]
		if known[path] {
			var names []string
			for _, name := range actual.Listing {
				if name != "dev-taskflow" && name != ".dev-taskflow.lock" {
					names = append(names, name)
				}
			}
			actual.Listing = names
		}
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(expected)
		if string(a) != string(b) {
			return fmt.Errorf("staged submodule layout changed at %s", path)
		}
	}
	for path := range p.layout.Absent {
		mapped := mapPath(path)
		parent, _, err := safefile.OpenRoot(filepath.Dir(mapped))
		if err != nil {
			return err
		}
		_, err = parent.Lstat(filepath.Base(mapped))
		parent.Close()
		if !errors.Is(err, fs.ErrNotExist) {
			return errors.Join(safefile.ErrChanged, err)
		}
	}
	return nil
}

func (p *Removal) revalidateLayout(ctx context.Context, expected *removalLayout) error {
	fresh, err := observeRemovalLayout(ctx, p.Repository, p.Graph)
	if err != nil {
		return err
	}
	if fresh.fingerprint() != expected.fingerprint() {
		return errors.New("stale submodule removal layout")
	}
	return nil
}

// PrunedDirectoriesError reports irreversible directory-only housekeeping before
// an outer removal failed. It never claims the worktree or task was removed.
type PrunedDirectoriesError struct {
	Paths []string
	Cause error
}

func (e *PrunedDirectoriesError) Error() string {
	return fmt.Sprintf("pruned empty submodule administration directories [%s]; outer cleanup incomplete: %v", strings.Join(e.Paths, ", "), e.Cause)
}
func (e *PrunedDirectoriesError) Unwrap() error { return e.Cause }

func (p *Removal) applyEmpty(ctx context.Context, check func(context.Context, string) error, removeOuter func() error) (err error) {
	// This lane creates no proof repository, lease, journal or quarantine.
	l, err := observeRemovalLayout(ctx, p.Repository, p.Graph)
	if err != nil {
		return err
	}
	if l.fingerprint() != p.LayoutFingerprint {
		return errors.New("stale submodule removal layout")
	}
	var pruned []string
	defer func() {
		if err != nil && len(pruned) > 0 {
			err = &PrunedDirectoriesError{Paths: append([]string(nil), pruned...), Cause: err}
		}
	}()
	sort.Slice(l.scaffolds, func(i, j int) bool {
		if len(l.scaffolds[i]) != len(l.scaffolds[j]) {
			return len(l.scaffolds[i]) > len(l.scaffolds[j])
		}
		return l.scaffolds[i] < l.scaffolds[j]
	})
	for _, path := range l.scaffolds {
		if err := p.revalidateLocal(ctx, l); err != nil {
			return err
		}
		if check != nil {
			if err := check(ctx, p.Graph.Root); err != nil {
				return err
			}
		}
		if err := p.revalidateLocal(ctx, l); err != nil {
			return err
		}
		parentPath := filepath.Dir(path)
		parent, info, err := safefile.OpenRoot(parentPath)
		if err != nil {
			return err
		}
		if !os.SameFile(info, l.Directories[parentPath].info) {
			parent.Close()
			return safefile.ErrChanged
		}
		err = safefile.RemoveEmptyChildDir(ctx, parent, filepath.Base(path), l.Directories[path].info)
		parent.Close()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(p.Repository.GitDir, path)
		pruned = append(pruned, filepath.ToSlash(rel))
		delete(l.Directories, path)
		if path == filepath.Join(p.Repository.GitDir, "modules") {
			l.Absent[path] = true
		} else {
			d := l.Directories[parentPath]
			var names []string
			for _, name := range d.Listing {
				if name != filepath.Base(path) {
					names = append(names, name)
				}
			}
			d.Listing = names
			l.Directories[parentPath] = d
		}
	}
	// Unconditional even for zero initialized children and absent modules/.
	if err := p.revalidateLocal(ctx, l); err != nil {
		return err
	}
	if check != nil {
		if err := check(ctx, p.Graph.Root); err != nil {
			return err
		}
	}
	if err := p.revalidateLocal(ctx, l); err != nil {
		return err
	}
	return removeOuter()
}
