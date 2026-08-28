package note

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
)

const delimiter = "+++"

type IDGenerator func() string
type DiagnosticSink func(path string, err error)
type StoreOption func(*Store)

func WithIDGenerator(fn IDGenerator) StoreOption { return func(s *Store) { s.generateID = fn } }
func WithClock(fn func() time.Time) StoreOption  { return func(s *Store) { s.clock = fn } }
func WithDiagnosticSink(fn DiagnosticSink) StoreOption {
	return func(s *Store) { s.diagnostics = fn }
}

// Store owns the durable Markdown note tree.
type Store struct {
	Dir string

	mu          sync.Mutex
	generateID  IDGenerator
	clock       func() time.Time
	diagnostics DiagnosticSink
}

func NewStore(dir string, options ...StoreOption) *Store {
	s := &Store{Dir: dir, generateID: uuid.NewString, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}

func (s *Store) Create(ctx context.Context, repositoryID, repositoryName, body string, tags []string) (*Note, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	n := &Note{
		SchemaVersion: CurrentSchemaVersion,
		RepositoryID:  repositoryID,
		Repository:    repositoryName,
		Body:          body,
		Tags:          tags,
	}
	n.Normalize()
	if n.Body == "" {
		return nil, fmt.Errorf("create note: body is empty: %w", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var created *Note
	err := s.withFileLock(ctx, func() error {
		for range 32 {
			candidate := *n
			candidate.ID = s.nextID()
			now := s.now()
			candidate.Created, candidate.Updated = now, now
			if err := candidate.Validate(); err != nil {
				return err
			}
			path := s.path(candidate.RepositoryID, candidate.ID)
			if _, err := os.Lstat(path); err == nil {
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			candidate.Path = path
			if err := s.writeAtomic(&candidate); err != nil {
				return err
			}
			created = clone(&candidate)
			return nil
		}
		return errors.New("generate unique note ID: too many collisions")
	})
	return created, err
}

func (s *Store) Get(id string) (*Note, error) {
	if id == "" {
		return nil, fmt.Errorf("empty note ID: %w", ErrNotFound)
	}
	paths, err := s.notePaths("")
	if err != nil {
		return nil, err
	}
	var match string
	for _, path := range paths {
		if strings.TrimSuffix(filepath.Base(path), ".md") == id {
			if match != "" {
				return nil, fmt.Errorf("duplicate note ID %s", id)
			}
			match = path
		}
	}
	if match == "" {
		return nil, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return s.read(match)
}

// List returns notes newest-first. Empty repositoryID means all repositories.
func (s *Store) List(repositoryID string) ([]*Note, error) {
	paths, err := s.notePaths(repositoryID)
	if err != nil {
		return nil, err
	}
	var notes []*Note
	for _, path := range paths {
		n, err := s.read(path)
		if err != nil {
			if s.diagnostics != nil {
				s.diagnostics(path, err)
			}
			continue
		}
		notes = append(notes, n)
	}
	Sort(notes)
	return notes, nil
}

// Update replaces body/tags while preserving identity, repository and Created.
func (s *Store) Update(ctx context.Context, id, expectedRevision, body string, tags []string) (*Note, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var updated *Note
	err := s.withFileLock(ctx, func() error {
		current, err := s.Get(id)
		if err != nil {
			return err
		}
		if expectedRevision == "" || current.Revision() != expectedRevision {
			return fmt.Errorf("note %s: %w", id, ErrConflict)
		}
		candidate := *current
		candidate.Body, candidate.Tags, candidate.Updated = body, tags, s.now()
		candidate.Normalize()
		if err := candidate.Validate(); err != nil {
			return err
		}
		if err := s.writeAtomic(&candidate); err != nil {
			return err
		}
		updated = clone(&candidate)
		return nil
	})
	return updated, err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withFileLock(ctx, func() error {
		n, err := s.Get(id)
		if err != nil {
			return err
		}
		dir := filepath.Dir(n.Path)
		if err := os.Remove(n.Path); err != nil {
			return err
		}
		if err := syncNoteDir(dir); err != nil {
			return err
		}
		// Empty repository directories are noise and carry no identity of their own.
		if err := os.Remove(dir); err == nil {
			return syncNoteDir(filepath.Dir(dir))
		}
		return nil
	})
}

func (s *Store) Path(repositoryID string) string {
	if repositoryID == "" {
		return s.Dir
	}
	return filepath.Join(s.Dir, repositoryID)
}

func Sort(notes []*Note) {
	sort.SliceStable(notes, func(i, j int) bool {
		if !notes[i].Created.Equal(notes[j].Created) {
			return notes[i].Created.After(notes[j].Created)
		}
		return notes[i].ID < notes[j].ID
	})
}

func (s *Store) read(path string) (*Note, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta, body, err := split(data)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var n Note
	md, err := toml.Decode(string(meta), &n)
	if err != nil {
		return nil, fmt.Errorf("decode %s frontmatter: %w", filepath.Base(path), err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		return nil, fmt.Errorf("decode %s: unknown field(s): %v", filepath.Base(path), unknown)
	}
	n.Body, n.Path = string(body), path
	n.Normalize()
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if want := strings.TrimSuffix(filepath.Base(path), ".md"); n.ID != want {
		return nil, fmt.Errorf("note ID %s does not match filename %s: %w", n.ID, want, ErrInvalid)
	}
	if want := filepath.Base(filepath.Dir(path)); n.RepositoryID != want {
		return nil, fmt.Errorf("repository ID %s does not match directory %s: %w", n.RepositoryID, want, ErrRepository)
	}
	return &n, nil
}

func (s *Store) writeAtomic(n *Note) error {
	dir := filepath.Dir(n.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var front bytes.Buffer
	if err := toml.NewEncoder(&front).Encode(n); err != nil {
		return err
	}
	content := delimiter + "\n" + front.String() + delimiter + "\n\n" + n.Body
	tmp, err := os.CreateTemp(dir, "."+n.ID+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync note temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Rename(name, n.Path); err != nil {
		return err
	}
	// Sync both the note directory (rename entry) and its parent (new
	// repository directory) before reporting durable success.
	return errors.Join(syncNoteDir(dir), syncNoteDir(filepath.Dir(dir)))
}

func split(data []byte) ([]byte, []byte, error) {
	if !bytes.HasPrefix(data, []byte(delimiter+"\n")) {
		return nil, nil, errors.New("missing opening +++ frontmatter delimiter")
	}
	rest := data[len(delimiter)+1:]
	marker := []byte("\n" + delimiter + "\n")
	i := bytes.Index(rest, marker)
	if i < 0 {
		return nil, nil, errors.New("missing closing +++ frontmatter delimiter")
	}
	meta := rest[:i]
	body := rest[i+len(marker):]
	body = bytes.TrimPrefix(body, []byte("\n"))
	return meta, body, nil
}

func (s *Store) notePaths(repositoryID string) ([]string, error) {
	root := s.Dir
	if repositoryID != "" {
		root = filepath.Join(root, repositoryID)
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	sort.Strings(paths)
	return paths, err
}

func (s *Store) path(repositoryID, id string) string {
	return filepath.Join(s.Dir, repositoryID, id+".md")
}

func (s *Store) nextID() string {
	if s.generateID == nil {
		return uuid.NewString()
	}
	return s.generateID()
}

func (s *Store) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock().UTC()
}

func clone(n *Note) *Note {
	if n == nil {
		return nil
	}
	out := *n
	out.Tags = append([]string(nil), n.Tags...)
	return &out
}

func (s *Store) withFileLock(ctx context.Context, operation func() error) (err error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	absolute, err := filepath.Abs(s.Dir)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(filepath.Dir(canonical), "."+filepath.Base(canonical)+".lock")
	lock, err := acquireNoteFileLock(ctx, lockPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return operation()
}
