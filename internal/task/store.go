package task

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ErrNotFound is returned when no task matches an identifier.
var ErrNotFound = errors.New("no such task")

// Store is a directory of one-TOML-file-per-task.
//
// One file per task, rather than a single registry document, is a deliberate
// choice: the state directory is meant to be syncable across machines (it can
// simply be a git repo), and a shared central file would conflict on every
// parallel edit while separate files merge cleanly.
type Store struct{ Dir string }

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

var idUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// MakeID derives the stable filename stem for a repo+branch pair.
func MakeID(repo, branch string) string {
	clean := func(s string) string {
		s = idUnsafe.ReplaceAllString(s, "-")
		for strings.Contains(s, "--") {
			s = strings.ReplaceAll(s, "--", "-")
		}
		return strings.Trim(s, "-._")
	}
	r, b := clean(repo), clean(branch)
	if r == "" {
		r = "repo"
	}
	if b == "" {
		b = "branch"
	}
	return r + "__" + b
}

func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".toml") }

// Save writes a task, stamping Updated (and Created on first write).
func (s *Store) Save(t *Task) error {
	if t.ID == "" {
		t.ID = MakeID(t.Repo, t.Branch)
	}
	if err := t.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if t.Created.IsZero() {
		t.Created = now
	}
	t.Updated = now

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	// Write to a temp file and rename, so an interrupted write can never leave
	// a half-parsed task behind.
	tmp, err := os.CreateTemp(s.Dir, "."+t.ID+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(t); err != nil {
		tmp.Close()
		return fmt.Errorf("encode task %s: %w", t.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(t.ID))
}

// Get loads one task by its exact id.
func (s *Store) Get(id string) (*Task, error) {
	var t Task
	if _, err := toml.DecodeFile(s.path(id), &t); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("read task %s: %w", id, err)
	}
	t.ID = id
	return &t, nil
}

// List loads every task, sorted by state (hot first) then most recent.
func (s *Store) List() ([]*Task, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no state yet is a normal, empty inventory
		}
		return nil, err
	}
	var out []*Task
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".toml") || strings.HasPrefix(name, ".") {
			continue
		}
		id := strings.TrimSuffix(name, ".toml")
		t, err := s.Get(id)
		if err != nil {
			// One corrupt file must not hide the rest of the inventory.
			fmt.Fprintf(os.Stderr, "dev: warning: skipping %s: %v\n", name, err)
			continue
		}
		out = append(out, t)
	}
	Sort(out)
	return out, nil
}

// Delete removes a task entry. It never touches git state.
func (s *Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	return err
}

// Resolve finds a task from a loose user-supplied reference: an exact id, an
// exact branch, or a unique case-insensitive substring of the id, name or
// branch. An ambiguous reference is an error listing the candidates rather
// than a silent pick.
func (s *Store) Resolve(ref string) (*Task, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty task reference: %w", ErrNotFound)
	}
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, t := range all {
		if t.ID == ref || t.Branch == ref {
			return t, nil
		}
	}
	needle := strings.ToLower(ref)
	var hits []*Task
	for _, t := range all {
		hay := strings.ToLower(t.ID + " " + t.Name + " " + t.Branch + " " + t.Repo)
		if strings.Contains(hay, needle) {
			hits = append(hits, t)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("%q: %w", ref, ErrNotFound)
	default:
		names := make([]string, len(hits))
		for i, t := range hits {
			names[i] = t.ID
		}
		return nil, fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
}

// FindByWorktree returns the task whose checkout contains dir.
//
// A task lives at exactly one place: its worktree when it has one, otherwise
// the main checkout. A task that has a linked worktree therefore does *not*
// claim the main checkout — otherwise standing in the main repo would match
// every worktree-based task of that repo. Among candidates, the longest path
// match wins so a nested checkout beats its parent.
func (s *Store) FindByWorktree(dir string) (*Task, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	under := func(dir, root string) bool {
		return root != "" && (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)))
	}
	var best *Task
	var bestLen int
	for _, t := range all {
		root := t.WorktreePath
		if root == "" {
			root = t.RepoPath
		}
		if under(dir, root) && len(root) > bestLen {
			best, bestLen = t, len(root)
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%q: %w", dir, ErrNotFound)
	}
	return best, nil
}

// stateRank orders states so the inventory reads as an attention queue.
func stateRank(s State) int {
	switch s {
	case Hot:
		return 0
	case Warm:
		return 1
	case Cold:
		return 2
	case Done:
		return 3
	}
	return 4
}

// Sort orders tasks hot-first, then by most recently updated.
func Sort(ts []*Task) {
	sort.SliceStable(ts, func(i, j int) bool {
		ri, rj := stateRank(ts[i].State), stateRank(ts[j].State)
		if ri != rj {
			return ri < rj
		}
		if !ts[i].Updated.Equal(ts[j].Updated) {
			return ts[i].Updated.After(ts[j].Updated)
		}
		return ts[i].ID < ts[j].ID
	})
}
