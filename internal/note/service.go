package note

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Service keeps durable file operations and the disposable index in sync.
type Service struct {
	Store     *Store
	IndexPath string
	// IndexDiagnostic reports an incremental index failure after the durable
	// file operation already succeeded. Search/reindex return index errors
	// directly because they have no useful result without it.
	IndexDiagnostic func(error)
}

func NewService(store *Store, indexPath string) *Service {
	return &Service{Store: store, IndexPath: indexPath}
}

func (s *Service) Add(ctx context.Context, repositoryID, repository, body string, tags []string) (*Note, error) {
	n, err := s.Store.Create(ctx, repositoryID, repository, body, tags)
	if err != nil {
		return nil, err
	}
	s.updateIndex(func(i *Index) error { return i.Upsert(n) })
	return n, nil
}

func (s *Service) List(repositoryID string) ([]*Note, error) {
	return s.Store.List(repositoryID)
}

func (s *Service) Get(id string) (*Note, error) { return s.Store.Get(id) }

func (s *Service) Update(ctx context.Context, id, expectedRevision, body string, tags []string) (*Note, error) {
	n, err := s.Store.Update(ctx, id, expectedRevision, body, tags)
	if err != nil {
		return nil, err
	}
	s.updateIndex(func(i *Index) error { return i.Upsert(n) })
	return n, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	s.updateIndex(func(i *Index) error { return i.Delete(id) })
	return nil
}

func (s *Service) Search(query, repositoryID string, limit int) ([]*Note, error) {
	notes, err := s.Store.List("")
	if err != nil {
		return nil, err
	}
	search := func() ([]Hit, error) {
		i, err := s.openIndexRecover()
		if err != nil {
			return nil, err
		}
		defer i.Close()
		if err := i.Ensure(notes); err != nil {
			return nil, err
		}
		return i.Search(query, repositoryID, limit)
	}
	hits, err := search()
	if err != nil {
		// A valid SQLite file can still have incompatible tables or become
		// corrupt after Open. It is disposable: clear, rebuild, retry once.
		if clearErr := s.ClearIndex(); clearErr != nil {
			return nil, errors.Join(err, clearErr)
		}
		hits, err = search()
	}
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*Note, len(notes))
	for _, n := range notes {
		byID[n.ID] = n
	}
	out := make([]*Note, 0, len(hits))
	for _, hit := range hits {
		if n := byID[hit.ID]; n != nil {
			out = append(out, clone(n))
		}
	}
	return out, nil
}

func (s *Service) Reindex() (int, error) {
	notes, err := s.Store.List("")
	if err != nil {
		return 0, err
	}
	rebuild := func() error {
		i, err := s.openIndexRecover()
		if err != nil {
			return err
		}
		defer i.Close()
		return i.Rebuild(notes)
	}
	if err := rebuild(); err != nil {
		if clearErr := s.ClearIndex(); clearErr != nil {
			return 0, errors.Join(err, clearErr)
		}
		if err := rebuild(); err != nil {
			return 0, err
		}
	}
	return len(notes), nil
}

// ClearIndex removes SQLite and its WAL sidecars. Durable Markdown notes are
// deliberately outside this path and remain untouched.
func (s *Service) ClearIndex() error {
	var err error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if removeErr := os.Remove(s.IndexPath + suffix); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove notes index%s: %w", suffix, removeErr))
		}
	}
	return err
}

// openIndexRecover treats a malformed SQLite file as the disposable cache it
// is: remove it and rebuild on the caller's next Ensure/Rebuild. Durable
// Markdown is never under this path.
func (s *Service) openIndexRecover() (*Index, error) {
	i, err := OpenIndex(s.IndexPath)
	if err == nil {
		return i, nil
	}
	if clearErr := s.ClearIndex(); clearErr != nil {
		return nil, errors.Join(err, clearErr)
	}
	return OpenIndex(s.IndexPath)
}

func (s *Service) updateIndex(operation func(*Index) error) {
	i, err := s.openIndexRecover()
	if err == nil {
		err = operation(i)
		err = errors.Join(err, i.Close())
	}
	if err != nil && s.IndexDiagnostic != nil {
		s.IndexDiagnostic(err)
	}
}
