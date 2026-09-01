package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/google/uuid"
)

var (
	// ErrNotFound is returned when an asset ID has no record.
	ErrNotFound = errors.New("catalog asset not found")
	// ErrAlreadyExists is returned when ID generation repeatedly collides.
	ErrAlreadyExists = errors.New("catalog asset already exists")
)

// Diagnostic describes one record skipped by a directory listing. The listing
// still returns every valid record; callers decide whether and where to render
// these notices.
type Diagnostic struct {
	Path string
	Err  error
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("skipping %s: %v", filepath.Base(d.Path), d.Err)
}

func (d Diagnostic) Unwrap() error { return d.Err }

// IDGenerator supplies stable random filename stems. It is injectable so tests
// can assert exact filenames without weakening production randomness.
type IDGenerator func() string

// DiagnosticSink receives non-fatal per-record listing failures.
type DiagnosticSink func(Diagnostic)

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithIDGenerator replaces UUID generation.
func WithIDGenerator(generator IDGenerator) StoreOption {
	return func(store *Store) { store.generateID = generator }
}

// WithClock replaces timestamp generation.
func WithClock(clock func() time.Time) StoreOption {
	return func(store *Store) { store.clock = clock }
}

// WithDiagnosticSink directs skipped-record notices to sink.
func WithDiagnosticSink(sink DiagnosticSink) StoreOption {
	return func(store *Store) { store.diagnostics = sink }
}

// WithDiagnosticWriter writes one line per skipped record to writer. It never
// defaults to a process-global stream.
func WithDiagnosticWriter(writer io.Writer) StoreOption {
	if writer == nil {
		return func(*Store) {}
	}
	return WithDiagnosticSink(func(diagnostic Diagnostic) {
		fmt.Fprintln(writer, diagnostic.Error())
	})
}

// Store is a directory of one TOML file per asset.
type Store struct {
	Dir string

	mu          sync.Mutex
	generateID  IDGenerator
	clock       func() time.Time
	diagnostics DiagnosticSink
}

// NewStore returns a catalog rooted at dir.
func NewStore(dir string, options ...StoreOption) *Store {
	store := &Store{
		Dir:        dir,
		generateID: uuid.NewString,
		clock:      time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
}

// WithLock runs operation while holding the catalog's cross-process advisory
// lock. It is intended for short read-match-write transactions; expensive
// discovery belongs before the lock. The operating system releases the lock if
// the process exits unexpectedly.
func (s *Store) WithLock(ctx context.Context, operation func() error) error {
	if s == nil {
		return errors.New("lock nil catalog store")
	}
	if operation == nil {
		return errors.New("catalog lock requires an operation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return lockx.WithDir(ctx, s.Dir, "catalog", operation)
}
func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".toml") }

// Create assigns a random stable ID and atomically persists entry. The caller's
// value is updated only after the rename succeeds.
func (s *Store) Create(entry *Entry) error {
	if err := s.validateCreateInput(entry); err != nil {
		return err
	}
	return s.WithLock(context.Background(), func() error { return s.CreateUnderLock(entry) })
}

// CreateUnderLock is the non-reentrant form used only inside Store.WithLock.
// Calling it without that transaction guard is a programming error.
func (s *Store) CreateUnderLock(entry *Entry) error {
	if entry == nil {
		return errors.New("create nil catalog entry")
	}
	if entry.ID != "" {
		return fmt.Errorf("create catalog asset: ID is assigned by the store")
	}
	prepare := func() (*Entry, error) {
		candidate := entry.Clone()
		candidate.ID = s.nextID()
		if err := ValidateID(candidate.ID); err != nil {
			return nil, fmt.Errorf("generated catalog ID: %w", err)
		}
		now := s.now()
		candidate.SchemaVersion = CurrentSchemaVersion
		if candidate.Created.IsZero() {
			candidate.Created = now
		}
		if candidate.Discovered.IsZero() {
			candidate.Discovered = now
		}
		candidate.Updated = now
		prepareNewNested(candidate, now)
		candidate.Normalize()
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		return candidate, nil
	}
	candidate, err := prepare()
	if err != nil {
		return err
	}
	for range 32 {
		if _, err := os.Lstat(s.path(candidate.ID)); err == nil {
			candidate, err = prepare()
			if err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("check catalog ID %s: %w", candidate.ID, err)
		}
		if err := s.writeAtomic(candidate); err != nil {
			return err
		}
		*entry = *candidate
		return nil
	}
	return fmt.Errorf("generate unique catalog ID: %w", ErrAlreadyExists)
}

func (s *Store) validateCreateInput(entry *Entry) error {
	if entry == nil {
		return errors.New("create nil catalog entry")
	}
	if entry.ID != "" {
		return fmt.Errorf("create catalog asset: ID is assigned by the store")
	}
	candidate := entry.Clone()
	candidate.ID = "00000000-0000-4000-8000-000000000000"
	now := s.now()
	candidate.SchemaVersion = CurrentSchemaVersion
	if candidate.Created.IsZero() {
		candidate.Created = now
	}
	if candidate.Discovered.IsZero() {
		candidate.Discovered = now
	}
	candidate.Updated = now
	prepareNewNested(candidate, now)
	candidate.Normalize()
	return candidate.Validate()
}

// Import merges portable metadata for an exact stable ID and records location as
// host's local view. Source locations are deliberately ignored: only the caller-
// supplied target location may claim a path on this machine. Move intents and
// recovery receipts are host-owned lifecycle authority and cannot be imported.
// The complete read-check-write transaction runs under the catalog's advisory
// lock so concurrent processes cannot claim the same target path.
func (s *Store) Import(ctx context.Context, source *Entry, host string, location Location) (*Entry, error) {
	if s == nil {
		return nil, errors.New("import into nil catalog store")
	}
	if source == nil {
		return nil, errors.New("import nil catalog entry")
	}
	if source.MoveIntent != nil {
		return nil, importConflict(source.ID, source.ID, "move_intent", "host-local move authority cannot be imported")
	}
	if source.RecoveryReceipt != nil {
		return nil, importConflict(source.ID, source.ID, "recovery_receipt", "recovery authority cannot be imported before target verification")
	}
	if err := validateLocationForSet(host, location); err != nil {
		return nil, fmt.Errorf("catalog import target location: %w", err)
	}

	incoming := source.Clone()
	if incoming.SchemaVersion == 0 {
		incoming.SchemaVersion = CurrentSchemaVersion
	}
	incoming.Locations = nil
	incoming.RecoveryReceipt = nil
	incoming.MoveIntent = nil
	incoming.Normalize()

	// Validate all source-owned fields before locking or creating the store. A
	// zero location timestamp is allowed at the API boundary and is stamped only
	// after the lock is held so a replay can retain the first import's timestamp.
	validationLocation := location
	if validationLocation.Updated.IsZero() {
		validationLocation.Updated = incoming.Updated
	}
	incoming.Locations = map[string]Location{host: validationLocation}
	if err := incoming.Validate(); err != nil {
		return nil, fmt.Errorf("validate imported catalog asset %s: %w", incoming.ID, err)
	}

	var imported *Entry
	err := s.WithLock(ctx, func() error {
		entries, diagnostics, err := s.ListWithDiagnostics()
		if err != nil {
			return err
		}
		if len(diagnostics) > 0 {
			return incompleteCatalogError(diagnostics)
		}

		var current *Entry
		for _, entry := range entries {
			if entry.ID == incoming.ID {
				current = entry
				break
			}
		}
		if current != nil && current.MoveIntent != nil {
			return importConflict(current.ID, current.ID, "move_intent",
				fmt.Sprintf("target has a pending %s move", current.MoveIntent.Operation))
		}

		targetLocation := location
		if targetLocation.Updated.IsZero() {
			if previous, ok := currentLocation(current, host); ok && !locationPayloadChanged(previous, targetLocation) {
				targetLocation.Updated = previous.Updated
			} else {
				targetLocation.Updated = s.now()
			}
		} else if previous, ok := currentLocation(current, host); ok &&
			!locationPayloadChanged(previous, targetLocation) && previous.Updated.After(targetLocation.Updated) {
			// Replaying an older observation of the same path must not move the
			// target's location timestamp backwards.
			targetLocation.Updated = previous.Updated
		}
		if err := validateLocation(host, targetLocation); err != nil {
			return fmt.Errorf("catalog import target location: %w", err)
		}
		if err := rejectImportedPathOwner(entries, incoming.ID, host, targetLocation); err != nil {
			return err
		}

		if current == nil {
			created := incoming.Clone()
			created.Locations = map[string]Location{host: targetLocation}
			if err := created.Validate(); err != nil {
				return fmt.Errorf("validate imported catalog asset %s: %w", created.ID, err)
			}
			if err := s.writeAtomic(created); err != nil {
				return err
			}
			imported = created.Clone()
			return nil
		}

		merged, changed, err := mergeImportedEntry(current, incoming, host, targetLocation)
		if err != nil {
			return err
		}
		if !changed {
			imported = current.Clone()
			return nil
		}
		if err := s.writeAtomic(merged); err != nil {
			return err
		}
		imported = merged.Clone()
		return nil
	})
	return imported, err
}

func importConflict(id, existingID, field, reason string) error {
	return &ImportConflictError{ID: id, ExistingID: existingID, Field: field, Reason: reason}
}

func currentLocation(entry *Entry, host string) (Location, bool) {
	if entry == nil {
		return Location{}, false
	}
	return entry.LocationFor(host)
}

func rejectImportedPathOwner(entries []*Entry, importedID, host string, target Location) error {
	targetPaths, err := canonicalLocationPaths(target)
	if err != nil {
		return fmt.Errorf("canonicalize imported location on host %s: %w", host, err)
	}
	for _, entry := range entries {
		if entry.ID == importedID {
			continue
		}
		owned, ok := entry.LocationFor(host)
		if !ok {
			continue
		}
		ownedPaths, err := canonicalLocationPaths(owned)
		if err != nil {
			return fmt.Errorf("canonicalize catalog asset %s location on host %s: %w", entry.ID, host, err)
		}
		ownedSet := make(map[string]struct{}, len(ownedPaths))
		for _, path := range ownedPaths {
			ownedSet[path] = struct{}{}
		}
		for _, path := range targetPaths {
			if _, exists := ownedSet[path]; exists {
				return importConflict(importedID, entry.ID, "location",
					fmt.Sprintf("target path %q on host %s is already owned", path, host))
			}
		}
	}
	return nil
}

func canonicalLocationPaths(location Location) ([]string, error) {
	fields := []struct {
		name  string
		value string
	}{
		{"current_path", location.CurrentPath},
		{"restore_path", location.RestorePath},
		{"real_path", location.RealPath},
		{"git_common_dir", location.GitCommonDir},
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		canonical, err := pathx.Canonical(field.value)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", field.name, field.value, err)
		}
		seen[canonical] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func mergeImportedEntry(current, incoming *Entry, host string, location Location) (*Entry, bool, error) {
	if current.SchemaVersion != incoming.SchemaVersion {
		return nil, false, importConflict(incoming.ID, current.ID, "schema_version", "schema versions differ")
	}
	if current.Kind != incoming.Kind {
		return nil, false, importConflict(incoming.ID, current.ID, "kind",
			fmt.Sprintf("durable kind %q differs from imported kind %q", current.Kind, incoming.Kind))
	}
	if current.Name != incoming.Name {
		return nil, false, importConflict(incoming.ID, current.ID, "name",
			fmt.Sprintf("durable name %q differs from imported name %q", current.Name, incoming.Name))
	}

	merged := current.Clone()
	changed := false

	currentSummary := strings.TrimSpace(merged.Note)
	incomingSummary := strings.TrimSpace(incoming.Note)
	switch {
	case currentSummary != "" && incomingSummary != "" && merged.Note != incoming.Note:
		return nil, false, importConflict(incoming.ID, current.ID, "note", "nonempty summaries differ")
	case currentSummary == "" && incomingSummary != "":
		merged.Note = incoming.Note
		changed = true
	}

	tags := NormalizeTags(append(append([]string(nil), merged.Tags...), incoming.Tags...))
	if !slices.Equal(merged.Tags, tags) {
		merged.Tags = tags
		changed = true
	}

	switch {
	case merged.RemoteIdentity != "" && incoming.RemoteIdentity != "" && merged.RemoteIdentity != incoming.RemoteIdentity:
		return nil, false, importConflict(incoming.ID, current.ID, "remote_identity",
			fmt.Sprintf("durable identity %q differs from imported identity %q", merged.RemoteIdentity, incoming.RemoteIdentity))
	case merged.RemoteIdentity == "" && incoming.RemoteIdentity != "":
		merged.RemoteIdentity = incoming.RemoteIdentity
		changed = true
	}

	switch {
	case merged.Experiment == nil && incoming.Experiment != nil:
		value := *incoming.Experiment
		merged.Experiment = &value
		changed = true
	case merged.Experiment != nil && incoming.Experiment != nil && !experimentsEqual(*merged.Experiment, *incoming.Experiment):
		return nil, false, importConflict(incoming.ID, current.ID, "experiment", "experiment metadata differs")
	}

	if value := earlierTime(merged.Created, incoming.Created); !value.Equal(merged.Created) {
		merged.Created = value
		changed = true
	}
	if value := earlierTime(merged.Discovered, incoming.Discovered); !value.Equal(merged.Discovered) {
		merged.Discovered = value
		changed = true
	}
	if value := laterTime(merged.LastOpened, incoming.LastOpened); !value.Equal(merged.LastOpened) {
		merged.LastOpened = value
		changed = true
	}
	if value := laterTime(merged.Updated, incoming.Updated); !value.Equal(merged.Updated) {
		merged.Updated = value
		changed = true
	}

	previous, exists := merged.LocationFor(host)
	if !exists || locationPayloadChanged(previous, location) || !previous.Updated.Equal(location.Updated) {
		if merged.Locations == nil {
			merged.Locations = make(map[string]Location)
		}
		merged.Locations[host] = location
		changed = true
	}
	merged.Normalize()
	if err := merged.Validate(); err != nil {
		return nil, false, fmt.Errorf("validate merged catalog asset %s: %w", merged.ID, err)
	}
	return merged, changed, nil
}

func experimentsEqual(left, right Experiment) bool {
	return left.Phase == right.Phase &&
		left.Slug == right.Slug &&
		left.OriginURL == right.OriginURL &&
		left.Started.Equal(right.Started) &&
		left.OriginalPath == right.OriginalPath &&
		left.DeprecatedAt.Equal(right.DeprecatedAt) &&
		left.DeprecatedPath == right.DeprecatedPath &&
		left.GraduatedAt.Equal(right.GraduatedAt) &&
		left.GraduatedPath == right.GraduatedPath
}

func earlierTime(left, right time.Time) time.Time {
	if left.IsZero() || !right.IsZero() && right.Before(left) {
		return right
	}
	return left
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

// Get loads one asset by exact ID.
func (s *Store) Get(id string) (*Entry, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	return s.get(id)
}

func (s *Store) get(id string) (*Entry, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("read catalog asset %s: %w", id, err)
	}
	var header struct {
		SchemaVersion int `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &header); err != nil {
		return nil, fmt.Errorf("decode catalog asset %s schema: %w", id, err)
	}
	if header.SchemaVersion != 0 && header.SchemaVersion != CurrentSchemaVersion {
		return nil, &UnsupportedSchemaError{ID: id, Version: header.SchemaVersion}
	}

	var entry Entry
	metadata, err := toml.Decode(string(data), &entry)
	if err != nil {
		return nil, fmt.Errorf("decode catalog asset %s: %w", id, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("decode catalog asset %s: unknown field(s): %v", id, undecoded)
	}
	entry.ID = id
	// Version zero is the only implicit form: it means the schema marker was
	// absent, not that an unknown future layout should be guessed.
	if entry.SchemaVersion == 0 {
		entry.SchemaVersion = CurrentSchemaVersion
	}
	entry.Normalize()
	if err := entry.Validate(); err != nil {
		return nil, fmt.Errorf("validate catalog asset %s: %w", id, err)
	}
	return &entry, nil
}

// List loads every valid record in deterministic ID order. Per-record failures
// are skipped and sent to the configured diagnostic sink; directory-level
// failures are returned.
func (s *Store) List() ([]*Entry, error) {
	entries, diagnostics, err := s.ListWithDiagnostics()
	if err != nil {
		return nil, err
	}
	s.reportDiagnostics(diagnostics)
	return entries, nil
}

func (s *Store) reportDiagnostics(diagnostics []Diagnostic) {
	if s.diagnostics == nil {
		return
	}
	for _, diagnostic := range diagnostics {
		s.diagnostics(diagnostic)
	}
}

// ListWithDiagnostics is List's explicit diagnostic form. It is useful to
// services that want to aggregate notices rather than render them immediately.
func (s *Store) ListWithDiagnostics() ([]*Entry, []Diagnostic, error) {
	directoryEntries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read catalog directory: %w", err)
	}

	var entries []*Entry
	var diagnostics []Diagnostic
	for _, directoryEntry := range directoryEntries {
		name := directoryEntry.Name()
		if directoryEntry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".toml") {
			continue
		}
		id := strings.TrimSuffix(name, ".toml")
		entry, err := s.Get(id)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: filepath.Join(s.Dir, name), Err: err})
			continue
		}
		entries = append(entries, entry)
	}
	Sort(entries)
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].Path < diagnostics[j].Path })
	return entries, diagnostics, nil
}

// Update applies mutate to a deep copy and atomically replaces the record only
// if the mutation and validation both succeed. ID, Created, and Discovered are
// immutable.
func (s *Store) Update(id string, mutate func(*Entry) error) (*Entry, error) {
	var result *Entry
	err := s.WithLock(context.Background(), func() error {
		var err error
		result, err = s.UpdateUnderLock(id, mutate)
		return err
	})
	return result, err
}

// UpdateUnderLock is the non-reentrant form used only inside Store.WithLock.
// Calling it without that transaction guard is a programming error.
func (s *Store) UpdateUnderLock(id string, mutate func(*Entry) error) (*Entry, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if mutate == nil {
		return nil, errors.New("catalog update requires a mutation")
	}
	current, err := s.get(id)
	if err != nil {
		return nil, err
	}
	updated := current.Clone()
	if err := mutate(updated); err != nil {
		return nil, err
	}
	if updated.ID != current.ID {
		return nil, errors.New("catalog update cannot change ID")
	}
	if updated.SchemaVersion != current.SchemaVersion {
		return nil, errors.New("catalog update cannot change schema version")
	}
	if !updated.Created.Equal(current.Created) {
		return nil, errors.New("catalog update cannot change Created")
	}
	if !updated.Discovered.Equal(current.Discovered) {
		return nil, errors.New("catalog update cannot change Discovered")
	}

	now := s.now()
	updated.Updated = now
	prepareUpdatedNested(current, updated, now)
	stampChangedLocations(current, updated, now)
	updated.Normalize()
	if err := updated.Validate(); err != nil {
		return nil, err
	}
	if err := s.writeAtomic(updated); err != nil {
		return nil, err
	}
	return updated.Clone(), nil
}

// Sort orders entries by stable ID.
func Sort(entries []*Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})
}

func (s *Store) writeAtomic(entry *Entry) error {
	temporary, err := os.CreateTemp(s.Dir, "."+entry.ID+".*.tmp")
	if err != nil {
		return fmt.Errorf("create catalog temp file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)

	if err := toml.NewEncoder(temporary).Encode(entry); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode catalog asset %s: %w", entry.ID, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close catalog temp file: %w", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("set catalog file mode: %w", err)
	}
	if err := os.Rename(name, s.path(entry.ID)); err != nil {
		return fmt.Errorf("replace catalog asset %s: %w", entry.ID, err)
	}
	return nil
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

func prepareNewNested(entry *Entry, now time.Time) {
	if entry.Experiment != nil {
		if entry.Experiment.Phase == "" {
			entry.Experiment.Phase = PhaseActive
		}
		if entry.Experiment.Started.IsZero() {
			entry.Experiment.Started = now
		}
	}
	for host, location := range entry.Locations {
		if location.Updated.IsZero() {
			location.Updated = now
			entry.Locations[host] = location
		}
	}
	if entry.RecoveryReceipt != nil && entry.RecoveryReceipt.Created.IsZero() {
		entry.RecoveryReceipt.Created = now
	}
	if entry.MoveIntent != nil && entry.MoveIntent.Started.IsZero() {
		entry.MoveIntent.Started = now
	}
}

func prepareUpdatedNested(current, updated *Entry, now time.Time) {
	if current.Experiment == nil && updated.Experiment != nil {
		if updated.Experiment.Phase == "" {
			updated.Experiment.Phase = PhaseActive
		}
		if updated.Experiment.Started.IsZero() {
			updated.Experiment.Started = now
		}
	}
	if updated.RecoveryReceipt != nil && updated.RecoveryReceipt.Created.IsZero() &&
		(current.RecoveryReceipt == nil || recoveryPayloadChanged(*current.RecoveryReceipt, *updated.RecoveryReceipt)) {
		updated.RecoveryReceipt.Created = now
	}
	if updated.MoveIntent != nil && updated.MoveIntent.Started.IsZero() &&
		(current.MoveIntent == nil || moveIntentPayloadChanged(*current.MoveIntent, *updated.MoveIntent)) {
		updated.MoveIntent.Started = now
	}
}

func recoveryPayloadChanged(left, right RecoveryReceipt) bool {
	left.Created = time.Time{}
	right.Created = time.Time{}
	return left != right
}

func moveIntentPayloadChanged(left, right MoveIntent) bool {
	left.Started = time.Time{}
	right.Started = time.Time{}
	return left != right
}

func stampChangedLocations(current, updated *Entry, now time.Time) {
	for host, location := range updated.Locations {
		previous, existed := current.Locations[host]
		if !existed || locationPayloadChanged(previous, location) {
			location.Updated = now
		} else {
			// Updated is store-owned metadata. A mutation that did not change this
			// host's location must not make it look newly observed.
			location.Updated = previous.Updated
		}
		updated.Locations[host] = location
	}
}

func locationPayloadChanged(left, right Location) bool {
	left.Updated = time.Time{}
	right.Updated = time.Time{}
	return left != right
}
