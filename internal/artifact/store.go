package artifact

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"syscall"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

var ErrIntentNotFound = errors.New("artifact intent not found")
var ErrStaleRevision = errors.New("stale artifact intent revision")

// Record binds a decoded intent to its exact persisted bytes, not its timestamp.
type Record struct {
	Intent   *Intent
	Revision string
}

type StaleRevisionError struct {
	ID       string
	Expected string
	Actual   string
}

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("artifact intent %q: %v (expected %q, actual %q)", e.ID, ErrStaleRevision, e.Expected, e.Actual)
}

func (e *StaleRevisionError) Unwrap() error { return ErrStaleRevision }

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
	return lockx.WithDir(ctx, s.Dir, "artifact intent", func() error {
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
		if existing, err := s.FindByRunID(candidate.RunID); err == nil {
			return fmt.Errorf("artifact run %s already belongs to intent %s", candidate.RunID, existing.ID)
		} else if !errors.Is(err, ErrIntentNotFound) {
			return err
		}
		if err := s.write(candidate); err != nil {
			return err
		}
		stored, err := s.Get(candidate.ID)
		if err != nil {
			return err
		}
		*intent = *stored
		return nil
	})
}

func (s *Store) Get(id string) (*Intent, error) {
	record, err := s.GetRecord(id)
	if err != nil {
		return nil, err
	}
	return record.Intent, nil
}

func (s *Store) GetRecord(id string) (*Record, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid artifact intent id %q", id)
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("artifact intent %s: %w", id, ErrIntentNotFound)
		}
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
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
	digest := sha256.Sum256(append([]byte(id+"\x00"), data...))
	intent.storedRevision = hex.EncodeToString(digest[:])
	return &Record{Intent: &intent, Revision: intent.storedRevision}, nil
}

func (s *Store) List() ([]Intent, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		// Windows can report PATH_NOT_FOUND when the target or an ancestor
		// is a file. Only genuine directory absence means an empty store.
		missing, inspectErr := missingStoreDirectory(s.Dir)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if missing {
			return nil, nil
		}
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

func missingStoreDirectory(dir string) (bool, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	for probe := absolute; ; probe = filepath.Dir(probe) {
		info, err := os.Lstat(probe)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(probe)
				if err != nil {
					return false, err
				}
			}
			if !info.IsDir() {
				return false, &os.PathError{Op: "readdir", Path: dir, Err: syscall.ENOTDIR}
			}
			return probe != absolute, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(probe) == probe {
			return false, err
		}
	}
}

func (s *Store) FindByRunID(runID string) (*Intent, error) {
	intents, err := s.List()
	if err != nil {
		return nil, err
	}
	var match *Intent
	for i := range intents {
		if intents[i].RunID == runID {
			if match != nil {
				return nil, fmt.Errorf("artifact run %s matches multiple intents; select an exact intent", runID)
			}
			match = &intents[i]
		}
	}
	if match != nil {
		return match, nil
	}
	return nil, fmt.Errorf("artifact run %s: %w", runID, ErrIntentNotFound)
}

// Update is the compatibility transaction for callers deriving a change entirely
// inside mutate. Reviewed or previously observed authority must use UpdateIfRevision.
func (s *Store) Update(ctx context.Context, id string, mutate func(*Intent) error) error {
	_, err := s.update(ctx, id, "", false, mutate)
	return err
}

func (s *Store) UpdateIfRevision(ctx context.Context, id, expectedRevision string, mutate func(*Intent) error) (*Record, error) {
	return s.update(ctx, id, expectedRevision, true, mutate)
}

// CheckRevision is a read-only checkpoint; it never refreshes stale authority.
func (s *Store) CheckRevision(id, expectedRevision string) error {
	current, err := s.GetRecord(id)
	if errors.Is(err, ErrIntentNotFound) {
		return &StaleRevisionError{ID: id, Expected: expectedRevision}
	}
	if err != nil {
		return err
	}
	if expectedRevision == "" || current.Revision != expectedRevision {
		return &StaleRevisionError{ID: id, Expected: expectedRevision, Actual: current.Revision}
	}
	return nil
}

func (s *Store) update(ctx context.Context, id, revision string, conditional bool, mutate func(*Intent) error) (*Record, error) {
	if mutate == nil {
		return nil, fmt.Errorf("artifact update needs a mutation")
	}
	var updated *Record
	err := lockx.WithDir(ctx, s.Dir, "artifact intent", func() error {
		record, err := s.GetRecord(id)
		if conditional && errors.Is(err, ErrIntentNotFound) {
			return &StaleRevisionError{ID: id, Expected: revision}
		}
		if err != nil {
			return err
		}
		if conditional && (revision == "" || record.Revision != revision) {
			return &StaleRevisionError{ID: id, Expected: revision, Actual: record.Revision}
		}
		intent := record.Intent
		if err := mutate(intent); err != nil {
			return err
		}
		if intent.ID != id {
			return errors.New("artifact update cannot change intent identity")
		}
		intent.UpdatedAt = s.clock().UTC().Truncate(time.Second)
		if err := intent.Validate(); err != nil {
			return err
		}
		if err := s.write(*intent); err != nil {
			return err
		}
		updated, err = s.GetRecord(id)
		return err
	})
	return updated, err
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
