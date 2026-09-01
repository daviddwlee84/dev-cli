package task

import (
	"context"
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
	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

var (
	// ErrNotFound is returned when no task matches an identifier.
	ErrNotFound = errors.New("no such task")
	// ErrConflict is returned when a caller would overwrite different content.
	ErrConflict = errors.New("task changed since it was read")
	// ErrInvalidID is returned when an ID cannot name one visible task file.
	ErrInvalidID = errors.New("invalid task ID")
)

// Diagnostic describes one task record omitted from a partial listing.
type Diagnostic struct {
	Path string
	Err  error
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("skipping %s: %v", filepath.Base(d.Path), d.Err)
}

func (d Diagnostic) Unwrap() error { return d.Err }

// Store is a directory of one-TOML-file-per-task.
//
// One file per task, rather than a single registry document, is a deliberate
// choice: the state directory is meant to be syncable across machines (it can
// simply be a git repo), and a shared central file would conflict on every
// parallel edit while separate files merge cleanly.
type Store struct{ Dir string }

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

// ImportOutcome reports what ImportExact observed or changed.
type ImportOutcome string

const (
	// ImportCreated means no task used the imported ID and a file was created.
	ImportCreated ImportOutcome = "created"
	// ImportIdentical means the same ID and semantic content were already stored.
	ImportIdentical ImportOutcome = "identical"
	// ImportConflict means the ID already held different semantic content.
	ImportConflict ImportOutcome = "conflict"
)

var (
	idUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	validID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ValidateID checks that id can be used as exactly one visible filename stem.
func ValidateID(id string) error {
	if !validID.MatchString(id) || filepath.Base(id) != id || id == "." || id == ".." {
		return fmt.Errorf("%q: %w", id, ErrInvalidID)
	}
	return nil
}

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

// Save creates a task or updates one read by this Store. Existing-task writes
// compare the task's private loaded revision under the cross-process lock, so
// legacy callers cannot silently overwrite a newer process's mutation.
func (s *Store) Save(t *Task) error {
	candidate, err := prepareCandidate(t, true)
	if err != nil {
		return err
	}
	err = s.withMutationLock(func() error {
		current, currentErr := s.get(candidate.ID)
		switch {
		case currentErr == nil:
			if candidate.storedRevision == "" || candidate.storedRevision != current.Revision() {
				return fmt.Errorf("task %s: %w", candidate.ID, ErrConflict)
			}
			if !candidate.Created.Equal(current.Created) {
				return fmt.Errorf("task %s changed its creation time: %w", candidate.ID, ErrConflict)
			}
		case errors.Is(currentErr, ErrNotFound):
			if candidate.storedRevision != "" {
				return fmt.Errorf("task %s was removed after it was read: %w", candidate.ID, ErrConflict)
			}
		default:
			return currentErr
		}
		stampForSave(candidate)
		if err := s.writeAtomic(candidate); err != nil {
			return err
		}
		candidate.storedRevision = candidate.Revision()
		return nil
	})
	if err != nil {
		return err
	}
	*t = *candidate
	return nil
}

// ReplaceDone starts a new task generation under an existing stable ID only
// while the exact observed record is still DONE. It resets Created/Updated and
// rejects a concurrent restart instead of weakening ordinary Save CAS.
func (s *Store) ReplaceDone(t *Task, expectedRevision string) error {
	candidate, err := prepareCandidate(t, true)
	if err != nil {
		return err
	}
	err = s.withMutationLock(func() error {
		current, err := s.get(candidate.ID)
		if err != nil {
			return err
		}
		if expectedRevision == "" || current.State != Done || current.Revision() != expectedRevision {
			return fmt.Errorf("task %s: %w", candidate.ID, ErrConflict)
		}
		candidate.Created = time.Time{}
		candidate.Updated = time.Time{}
		stampForSave(candidate)
		if err := s.writeAtomic(candidate); err != nil {
			return err
		}
		candidate.storedRevision = candidate.Revision()
		return nil
	})
	if err != nil {
		return err
	}
	*t = *candidate
	return nil
}

// SaveIfRevision writes t only while the stored task still has
// expectedRevision. The comparison and replacement happen under one
// cross-process lock so a stale writer cannot overwrite a newer mutation.
func (s *Store) SaveIfRevision(t *Task, expectedRevision string) error {
	return s.withMutationLock(func() error { return s.SaveIfRevisionUnderLock(t, expectedRevision) })
}

// SaveIfRevisionUnderLock is the non-reentrant form used only inside
// Store.WithMutation. Calling it without that transaction guard is a
// programming error.
func (s *Store) SaveIfRevisionUnderLock(t *Task, expectedRevision string) error {
	candidate, err := prepareCandidate(t, true)
	if err != nil {
		return err
	}
	current, err := s.get(candidate.ID)
	if err != nil {
		return err
	}
	if expectedRevision == "" || current.Revision() != expectedRevision || !candidate.Created.Equal(current.Created) {
		return fmt.Errorf("task %s: %w", candidate.ID, ErrConflict)
	}
	stampForSave(candidate)
	if err := s.writeAtomic(candidate); err != nil {
		return err
	}
	candidate.storedRevision = candidate.Revision()
	*t = *candidate
	return nil
}

// ImportExact creates t under its supplied ID without changing any semantic
// field or timestamp. Replaying identical content is a no-op; reusing an ID
// for different content reports ImportConflict and ErrConflict.
func (s *Store) ImportExact(t *Task) (ImportOutcome, error) {
	candidate, err := prepareCandidate(t, false)
	if err != nil {
		return "", err
	}
	var outcome ImportOutcome
	err = s.withMutationLock(func() error {
		current, err := s.get(candidate.ID)
		switch {
		case err == nil && current.Revision() == candidate.Revision():
			outcome = ImportIdentical
			return nil
		case err == nil:
			outcome = ImportConflict
			return fmt.Errorf("task %s: %w", candidate.ID, ErrConflict)
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if err := s.writeAtomic(candidate); err != nil {
			return err
		}
		outcome = ImportCreated
		return nil
	})
	return outcome, err
}

// RecordExists reports whether the exact task pathname is occupied, without
// decoding it. It lets callers distinguish a corrupt same-ID record from an
// unavailable/missing store before starting external side effects.
func (s *Store) RecordExists(id string) (bool, error) {
	if err := ValidateID(id); err != nil {
		return false, err
	}
	_, err := os.Lstat(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Get loads one task by its exact id.
func (s *Store) Get(id string) (*Task, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	return s.get(id)
}

func (s *Store) get(id string) (*Task, error) {
	var t Task
	if _, err := toml.DecodeFile(s.path(id), &t); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("read task %s: %w", id, err)
	}
	t.ID = id
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("validate task %s: %w", id, err)
	}
	t.storedRevision = t.Revision()
	return &t, nil
}

func prepareCandidate(t *Task, deriveID bool) (*Task, error) {
	if t == nil {
		return nil, errors.New("save nil task")
	}
	candidate := *t
	candidate.Tags = append([]string(nil), t.Tags...)
	if candidate.ID == "" && deriveID {
		candidate.ID = MakeID(candidate.Repo, candidate.Branch)
	}
	if candidate.ID == "" {
		return nil, fmt.Errorf("task import requires an exact ID: %w", ErrInvalidID)
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	return &candidate, nil
}

func stampForSave(t *Task) {
	now := time.Now().UTC().Truncate(time.Second)
	if t.Created.IsZero() {
		t.Created = now
	}
	t.Updated = now
}

// WithMutation holds the task store's cross-process lock for a compound
// read-side-effect-CAS transaction. The callback must use non-reentrant store
// methods such as SaveIfRevisionUnderLock.
func (s *Store) WithMutation(ctx context.Context, operation func() error) error {
	if s == nil {
		return errors.New("mutate nil task store")
	}
	if operation == nil {
		return errors.New("task store mutation requires an operation")
	}
	return lockx.WithDir(ctx, s.Dir, "task store", operation)
}

func (s *Store) withMutationLock(operation func() error) error {
	return s.WithMutation(context.Background(), operation)
}

func (s *Store) writeAtomic(t *Task) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create task state directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, "."+t.ID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create task %s temp file: %w", t.ID, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set task %s file mode: %w", t.ID, err)
	}
	if err := toml.NewEncoder(tmp).Encode(t); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode task %s: %w", t.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync task %s temp file: %w", t.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close task %s temp file: %w", t.ID, err)
	}
	if err := replaceFile(name, s.path(t.ID)); err != nil {
		return fmt.Errorf("replace task %s: %w", t.ID, err)
	}
	if err := errors.Join(syncDirectory(s.Dir), syncDirectory(filepath.Dir(s.Dir))); err != nil {
		return fmt.Errorf("sync task %s directory: %w", t.ID, err)
	}
	return nil
}

// List loads every valid task, sorted by state (hot first) then most recent.
// It retains the historical warning behavior; completeness-sensitive callers
// should use ListWithDiagnostics instead of treating a partial list as whole.
func (s *Store) List() ([]*Task, error) {
	tasks, diagnostics, err := s.ListWithDiagnostics()
	if err != nil {
		return nil, err
	}
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(os.Stderr, "dev: warning: %s\n", diagnostic.Error())
	}
	return tasks, nil
}

// ListWithDiagnostics returns every valid task plus deterministic per-record
// errors. A corrupt record never hides valid work and never silently satisfies
// an inventory-completeness gate.
func (s *Store) ListWithDiagnostics() ([]*Task, []Diagnostic, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil // no state yet is a normal, empty inventory
		}
		return nil, nil, err
	}
	var tasks []*Task
	var diagnostics []Diagnostic
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".toml") || strings.HasPrefix(name, ".") {
			continue
		}
		id := strings.TrimSuffix(name, ".toml")
		tracked, err := s.Get(id)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: filepath.Join(s.Dir, name), Err: err})
			continue
		}
		tasks = append(tasks, tracked)
	}
	Sort(tasks)
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].Path < diagnostics[j].Path })
	return tasks, diagnostics, nil
}

// Delete removes a task entry under the mutation lock. It never touches git
// state.
func (s *Store) Delete(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	return s.withMutationLock(func() error {
		err := os.Remove(s.path(id))
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%q: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := syncDirectory(s.Dir); err != nil {
			return fmt.Errorf("sync task directory after deleting %s: %w", id, err)
		}
		return nil
	})
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
