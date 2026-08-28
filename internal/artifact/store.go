package artifact

import (
	"context"
	"crypto/rand"
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
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

var ErrIntentNotFound = errors.New("artifact intent not found")

// Store keeps one strict JSON intent per finalization handoff.
type Store struct {
	Dir   string
	clock func() time.Time
	newID func() string
}

func NewStore(dir string) *Store {
	return &Store{Dir: dir, clock: time.Now, newID: randomID}
}

func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".json") }

func (s *Store) Create(ctx context.Context, intent *Intent) error {
	if intent == nil {
		return fmt.Errorf("create nil artifact intent")
	}
	return catalog.NewStore(s.Dir).WithLock(ctx, func() error {
		candidate := *intent
		if candidate.ID == "" {
			candidate.ID = s.newID()
		}
		candidate.SchemaVersion = SchemaVersion
		candidate.Status = Armed
		now := s.clock().UTC().Truncate(time.Second)
		candidate.CreatedAt, candidate.UpdatedAt = now, now
		if err := candidate.Validate(); err != nil {
			return err
		}
		if _, err := os.Stat(s.path(candidate.ID)); err == nil {
			return fmt.Errorf("artifact intent %s already exists", candidate.ID)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := s.write(candidate); err != nil {
			return err
		}
		*intent = candidate
		return nil
	})
}

func (s *Store) Get(id string) (*Intent, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid artifact intent id %q", id)
	}
	file, err := os.Open(s.path(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("artifact intent %s: %w", id, ErrIntentNotFound)
		}
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var intent Intent
	if err := decoder.Decode(&intent); err != nil {
		return nil, fmt.Errorf("decode artifact intent %s: %w", id, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode artifact intent %s: %w", id, err)
	}
	if intent.ID != id {
		return nil, fmt.Errorf("artifact intent filename %s disagrees with id %s", id, intent.ID)
	}
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	return &intent, nil
}

func (s *Store) List() ([]Intent, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var intents []Intent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		intent, err := s.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		intents = append(intents, *intent)
	}
	sort.Slice(intents, func(i, j int) bool { return intents[i].CreatedAt.Before(intents[j].CreatedAt) })
	return intents, nil
}

func (s *Store) FindByRunID(runID string) (*Intent, error) {
	intents, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range intents {
		if intents[i].RunID == runID {
			return &intents[i], nil
		}
	}
	return nil, fmt.Errorf("artifact run %s: %w", runID, ErrIntentNotFound)
}

func (s *Store) Update(ctx context.Context, id string, mutate func(*Intent) error) error {
	if mutate == nil {
		return fmt.Errorf("artifact update needs a mutation")
	}
	return catalog.NewStore(s.Dir).WithLock(ctx, func() error {
		intent, err := s.Get(id)
		if err != nil {
			return err
		}
		if err := mutate(intent); err != nil {
			return err
		}
		intent.UpdatedAt = s.clock().UTC().Truncate(time.Second)
		if err := intent.Validate(); err != nil {
			return err
		}
		return s.write(*intent)
	})
}

func (s *Store) write(intent Intent) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".intent-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(intent); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path(intent.ID))
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values")
}

func randomID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("intent-%d", time.Now().UnixNano())
	}
	return "intent-" + hex.EncodeToString(raw[:])
}
