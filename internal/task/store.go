package task

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	// ErrAlreadyExists is returned when a create-only transaction finds an
	// existing task with the same derived filename.
	ErrAlreadyExists = errors.New("task already exists")
	// ErrStaleRevision is returned when a conditional mutation was planned from
	// bytes that are no longer current.
	ErrStaleRevision = errors.New("stale task revision")
)

// StaleRevisionError describes an optimistic-concurrency failure.
type StaleRevisionError struct {
	ID       string
	Expected string
	Actual   string
}

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("task %q: %v (expected %q, actual %q)", e.ID, ErrStaleRevision, e.Expected, e.Actual)
}

func (e *StaleRevisionError) Unwrap() error { return ErrStaleRevision }

// Record is a decoded task paired with an ephemeral fingerprint of its exact
// persisted representation. Revision is not part of the task TOML schema.
type Record struct {
	Task     Task
	Revision string
}

// Diagnostic describes one task file skipped by a directory listing.
type Diagnostic struct {
	ID   string
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

// Tx is a task-store transaction whose caller holds the store's cross-process
// lock. It lets a lifecycle operation reload, revalidate and perform its final
// conditional write without another dev process changing task intent between
// those steps.
type Tx struct{ store *Store }

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

// Save writes a task, stamping Updated (and Created on first write). Save is a
// compatibility upsert; callers that need collision or lost-update protection
// should use Create or Update.
func (s *Store) Save(t *Task) error {
	return s.WithLock(context.Background(), func(tx *Tx) error {
		_, err := tx.Save(t)
		return err
	})
}

// Create atomically writes t only when its derived ID is unused. The caller's
// task receives its ID and timestamps only after the write succeeds.
func (s *Store) Create(ctx context.Context, t *Task) (*Record, error) {
	var created *Record
	err := s.WithLock(ctx, func(tx *Tx) error {
		var err error
		created, err = tx.Create(t)
		return err
	})
	return created, err
}

// Update atomically replaces t only when expectedRevision still describes the
// exact persisted bytes for its ID. On success t receives the store-owned
// Updated timestamp and the returned record carries the new revision.
func (s *Store) Update(ctx context.Context, t *Task, expectedRevision string) (*Record, error) {
	var updated *Record
	err := s.WithLock(ctx, func(tx *Tx) error {
		var err error
		updated, err = tx.Update(t, expectedRevision)
		return err
	})
	return updated, err
}

// Get loads one task by its exact id.
func (s *Store) Get(id string) (*Task, error) {
	record, err := s.GetRecord(id)
	if err != nil {
		return nil, err
	}
	return &record.Task, nil
}

// GetRecord loads one task and fingerprints its exact persisted bytes plus ID.
func (s *Store) GetRecord(id string) (*Record, error) {
	return s.getRecordUnlocked(id)
}

// List loads every task, sorted by state (hot first) then most recent. Corrupt
// records retain the historical warning behavior; ListRecords exposes them as
// typed diagnostics to callers that need to aggregate or retain the failures.
func (s *Store) List() ([]*Task, error) {
	records, diagnostics, err := s.ListRecords()
	if err != nil {
		return nil, err
	}
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(os.Stderr, "dev: warning: %v\n", diagnostic)
	}
	var out []*Task
	for i := range records {
		out = append(out, &records[i].Task)
	}
	return out, nil
}

// ListRecords returns every valid task and a typed diagnostic for each task
// file that could not be read or decoded. Directory-level failures are fatal.
func (s *Store) ListRecords() ([]Record, []Diagnostic, error) {
	return s.listRecordsUnlocked()
}

// listRecordsUnlocked implements both the public read-only inventory and the
// transaction-scoped inventory. Callers using it for mutation authority must
// hold the store lock through the guarded operation.
func (s *Store) listRecordsUnlocked() ([]Record, []Diagnostic, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil // no state yet is a normal, empty inventory
		}
		return nil, nil, err
	}
	var records []Record
	var diagnostics []Diagnostic
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".toml") || strings.HasPrefix(name, ".") {
			continue
		}
		id := strings.TrimSuffix(name, ".toml")
		record, err := s.getRecordUnlocked(id)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				ID: id, Path: filepath.Join(s.Dir, name), Err: err,
			})
			continue
		}
		records = append(records, *record)
	}
	sortRecords(records)
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].Path < diagnostics[j].Path })
	return records, diagnostics, nil
}

// Delete removes a task entry. It never touches git state. Delete is the
// compatibility unconditional form; use DeleteIfRevision to protect a planned
// mutation from concurrent changes.
func (s *Store) Delete(id string) error {
	return s.WithLock(context.Background(), func(tx *Tx) error {
		return tx.Delete(id)
	})
}

// DeleteIfRevision removes a task only while expectedRevision still describes
// its exact persisted bytes.
func (s *Store) DeleteIfRevision(ctx context.Context, id, expectedRevision string) error {
	return s.WithLock(ctx, func(tx *Tx) error {
		return tx.DeleteIfRevision(id, expectedRevision)
	})
}

// WithLock runs operation while holding the task store's cross-process lock.
// Callers must use the supplied Tx inside the callback; calling Store mutation
// methods from it would try to acquire the same lock again.
func (s *Store) WithLock(ctx context.Context, operation func(*Tx) error) error {
	if operation == nil {
		return errors.New("task store transaction callback is nil")
	}
	return lockx.WithDir(ctx, s.Dir, "task store", func() error {
		return operation(&Tx{store: s})
	})
}

// GetRecord returns one record while the transaction lock is held.
func (tx *Tx) GetRecord(id string) (*Record, error) {
	return tx.store.getRecordUnlocked(id)
}

// ListRecords returns the complete diagnostic-preserving task inventory while
// the transaction lock is held. It is intended for claim checks that must stay
// stable through a subsequent guarded mutation.
func (tx *Tx) ListRecords() ([]Record, []Diagnostic, error) {
	return tx.store.listRecordsUnlocked()
}

// Save performs the compatibility unconditional write inside a transaction.
func (tx *Tx) Save(t *Task) (*Record, error) {
	if t == nil {
		return nil, errors.New("save nil task")
	}
	return tx.store.saveUnlocked(t)
}

// Create writes a task only when its derived ID is not already present.
func (tx *Tx) Create(t *Task) (*Record, error) {
	if t == nil {
		return nil, errors.New("create nil task")
	}
	candidate := cloneTask(*t)
	if candidate.ID == "" {
		candidate.ID = MakeID(candidate.Repo, candidate.Branch)
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(tx.store.path(candidate.ID)); err == nil {
		return nil, fmt.Errorf("task %q: %w", candidate.ID, ErrAlreadyExists)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("check task %s: %w", candidate.ID, err)
	}
	record, err := tx.store.saveUnlocked(&candidate)
	if err != nil {
		return nil, err
	}
	*t = cloneTask(candidate)
	return record, nil
}

// Update replaces a task only when the expected revision is still current.
func (tx *Tx) Update(t *Task, expectedRevision string) (*Record, error) {
	if t == nil {
		return nil, errors.New("update nil task")
	}
	candidate := cloneTask(*t)
	if candidate.ID == "" {
		candidate.ID = MakeID(candidate.Repo, candidate.Branch)
	}
	current, err := tx.store.getRecordUnlocked(candidate.ID)
	if err != nil {
		return nil, err
	}
	if current.Revision != expectedRevision {
		return nil, &StaleRevisionError{
			ID: candidate.ID, Expected: expectedRevision, Actual: current.Revision,
		}
	}
	record, err := tx.store.saveUnlocked(&candidate)
	if err != nil {
		return nil, err
	}
	*t = cloneTask(candidate)
	return record, nil
}

// Delete removes a task unconditionally inside a compatibility transaction.
func (tx *Tx) Delete(id string) error {
	return tx.store.deleteUnlocked(id)
}

// DeleteIfRevision removes a task only while its revision remains current.
func (tx *Tx) DeleteIfRevision(id, expectedRevision string) error {
	current, err := tx.store.getRecordUnlocked(id)
	if err != nil {
		return err
	}
	if current.Revision != expectedRevision {
		return &StaleRevisionError{ID: id, Expected: expectedRevision, Actual: current.Revision}
	}
	return tx.store.deleteUnlocked(id)
}

// saveUnlocked atomically persists t. The caller must hold the store lock.
func (s *Store) saveUnlocked(t *Task) (*Record, error) {
	if t.ID == "" {
		t.ID = MakeID(t.Repo, t.Branch)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if t.Created.IsZero() {
		t.Created = now
	}
	t.Updated = now

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(t); err != nil {
		return nil, fmt.Errorf("encode task %s: %w", t.ID, err)
	}
	data := encoded.Bytes()

	// Write to a temp file and rename, so an interrupted write can never leave
	// a half-parsed task behind.
	tmp, err := os.CreateTemp(s.Dir, "."+t.ID+".*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write task %s: %w", t.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), s.path(t.ID)); err != nil {
		return nil, err
	}
	return &Record{Task: cloneTask(*t), Revision: revisionFor(t.ID, data)}, nil
}

// getRecordUnlocked reads a complete record without taking the mutation lock.
// Atomic rename makes concurrent reads see either the previous or next bytes.
func (s *Store) getRecordUnlocked(id string) (*Record, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("read task %s: %w", id, err)
	}
	var t Task
	if _, err := toml.Decode(string(data), &t); err != nil {
		return nil, fmt.Errorf("read task %s: %w", id, err)
	}
	t.ID = id
	return &Record{Task: t, Revision: revisionFor(id, data)}, nil
}

// deleteUnlocked removes one record. The caller must hold the store lock.
func (s *Store) deleteUnlocked(id string) error {
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	return err
}

// revisionFor hashes the task ID, a NUL domain separator, and the exact bytes
// persisted in the task file. A byte-for-byte rewrite keeps the revision;
// any byte or filename-ID change produces a different optimistic token.
func revisionFor(id string, data []byte) string {
	payload := make([]byte, 0, len(id)+1+len(data))
	payload = append(payload, id...)
	payload = append(payload, 0)
	payload = append(payload, data...)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func cloneTask(t Task) Task {
	t.Tags = append([]string(nil), t.Tags...)
	return t
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
		return taskLess(*ts[i], *ts[j])
	})
}

func sortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool {
		return taskLess(records[i].Task, records[j].Task)
	})
}

func taskLess(left, right Task) bool {
	rl, rr := stateRank(left.State), stateRank(right.State)
	if rl != rr {
		return rl < rr
	}
	if !left.Updated.Equal(right.Updated) {
		return left.Updated.After(right.Updated)
	}
	return left.ID < right.ID
}
