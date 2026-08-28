// Package catalog stores durable identity and lifecycle metadata for experiments
// and ordinary repositories. Filesystem presence is host-local, so one logical
// asset can carry a separate location for every machine that observes it.
package catalog

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const CurrentSchemaVersion = 1

var (
	// ErrUnsupportedSchema identifies a catalog record written with an unknown
	// nonzero schema. Callers must surface it instead of interpreting newer data
	// using today's shape.
	ErrUnsupportedSchema = errors.New("unsupported catalog schema version")
	// ErrInvalidID identifies a filename stem that cannot safely name one record.
	ErrInvalidID = errors.New("invalid catalog ID")
)

// UnsupportedSchemaError describes a record whose schema this version cannot
// read. It supports errors.Is(err, ErrUnsupportedSchema).
type UnsupportedSchemaError struct {
	ID      string
	Version int
}

func (e *UnsupportedSchemaError) Error() string {
	if e.ID == "" {
		return fmt.Sprintf("catalog schema version %d: %v", e.Version, ErrUnsupportedSchema)
	}
	return fmt.Sprintf("catalog asset %s has schema version %d: %v", e.ID, e.Version, ErrUnsupportedSchema)
}

func (e *UnsupportedSchemaError) Unwrap() error { return ErrUnsupportedSchema }

// Kind distinguishes short-lived experiments from ordinary repositories.
type Kind string

const (
	KindTry        Kind = "try"
	KindRepository Kind = "repository"

	// Short aliases keep call sites readable when the type already supplies the
	// context, for example Entry{Kind: catalog.Repository}.
	Try        = KindTry
	Repository = KindRepository
)

// ExperimentPhase is the retained lifecycle of an experiment, including after
// it graduates into a repository.
type ExperimentPhase string

const (
	PhaseActive     ExperimentPhase = "active"
	PhaseDeprecated ExperimentPhase = "deprecated"
	PhaseGraduated  ExperimentPhase = "graduated"

	Active     = PhaseActive
	Deprecated = PhaseDeprecated
	Graduated  = PhaseGraduated
)

// LocationState describes what remains on one host.
type LocationState string

const (
	LocationPresent  LocationState = "present"
	LocationArchived LocationState = "archived"
	LocationEvicted  LocationState = "evicted"

	Present  = LocationPresent
	Archived = LocationArchived
	Evicted  = LocationEvicted
)

// Experiment records provenance that must survive renames and graduation.
type Experiment struct {
	Phase ExperimentPhase `toml:"phase" json:"phase"`

	Slug         string    `toml:"slug" json:"slug"`
	OriginURL    string    `toml:"origin_url" json:"origin_url"`
	Started      time.Time `toml:"started" json:"started"`
	OriginalPath string    `toml:"original_path" json:"original_path"`

	DeprecatedAt   time.Time `toml:"deprecated_at" json:"deprecated_at"`
	DeprecatedPath string    `toml:"deprecated_path" json:"deprecated_path"`
	GraduatedAt    time.Time `toml:"graduated_at" json:"graduated_at"`
	GraduatedPath  string    `toml:"graduated_path" json:"graduated_path"`
}

// Location is one host's knowledge of an asset. CurrentPath is where bytes live
// now; RestorePath is where an archived or evicted asset should return. RealPath
// and GitCommonDir make symlink aliases and linked worktrees match the same clone.
type Location struct {
	State LocationState `toml:"state" json:"state"`

	CurrentPath  string `toml:"current_path" json:"current_path"`
	RestorePath  string `toml:"restore_path" json:"restore_path"`
	RealPath     string `toml:"real_path" json:"real_path"`
	GitCommonDir string `toml:"git_common_dir" json:"git_common_dir"`

	Updated time.Time `toml:"updated" json:"updated"`
}

// RecoveryReceipt is enough metadata to explain and verify how an evicted asset
// can be reconstructed. Different recovery methods use different subsets.
type RecoveryReceipt struct {
	Host           string    `toml:"host" json:"host"`
	Method         string    `toml:"method" json:"method"`
	SourcePath     string    `toml:"source_path" json:"source_path"`
	RestorePath    string    `toml:"restore_path" json:"restore_path"`
	RemoteIdentity string    `toml:"remote_identity" json:"remote_identity"`
	RemoteName     string    `toml:"remote_name" json:"remote_name"`
	RemoteURL      string    `toml:"remote_url" json:"remote_url"`
	Revision       string    `toml:"revision" json:"revision"`
	RefsDigest     string    `toml:"refs_digest" json:"refs_digest"`
	ArchivePath    string    `toml:"archive_path" json:"archive_path"`
	Checksum       string    `toml:"checksum" json:"checksum"`
	Created        time.Time `toml:"created" json:"created"`
	Verified       time.Time `toml:"verified" json:"verified"`
}

// MoveIntent is written before a filesystem rename and cleared after the new
// location is durable. A surviving intent lets later commands reconcile a move
// interrupted between those two points.
type MoveIntent struct {
	Host            string    `toml:"host" json:"host"`
	Operation       string    `toml:"operation" json:"operation"`
	SourcePath      string    `toml:"source_path" json:"source_path"`
	DestinationPath string    `toml:"destination_path" json:"destination_path"`
	Started         time.Time `toml:"started" json:"started"`
}

// Entry is one logical asset. ID is the stable filename stem and is deliberately
// not duplicated inside the TOML document.
type Entry struct {
	SchemaVersion int      `toml:"schema_version" json:"schema_version"`
	ID            string   `toml:"-" json:"id"`
	Kind          Kind     `toml:"kind" json:"kind"`
	Name          string   `toml:"name" json:"name"`
	Note          string   `toml:"note" json:"note"`
	Tags          []string `toml:"tags" json:"tags"`

	// RemoteIdentity is a relocation hint, never sufficient on its own when
	// several records or live clones could be the observation's owner.
	RemoteIdentity string `toml:"remote_identity" json:"remote_identity"`

	Experiment      *Experiment         `toml:"experiment,omitempty" json:"experiment,omitempty"`
	Locations       map[string]Location `toml:"locations" json:"locations"`
	RecoveryReceipt *RecoveryReceipt    `toml:"recovery_receipt,omitempty" json:"recovery_receipt,omitempty"`
	MoveIntent      *MoveIntent         `toml:"move_intent,omitempty" json:"move_intent,omitempty"`

	Created    time.Time `toml:"created" json:"created"`
	Discovered time.Time `toml:"discovered" json:"discovered"`
	LastOpened time.Time `toml:"last_opened" json:"last_opened"`
	Updated    time.Time `toml:"updated" json:"updated"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateID checks that id can be used as exactly one visible filename stem.
func ValidateID(id string) error {
	if !validID.MatchString(id) || filepath.Base(id) != id || id == "." || id == ".." {
		return fmt.Errorf("%q: %w", id, ErrInvalidID)
	}
	return nil
}

// NormalizeTags trims, lowercases, deduplicates and sorts tags. Sorting makes
// persisted output deterministic regardless of the order in which callers add
// labels.
func NormalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" {
			seen[tag] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// Normalize applies the lossless normalizations stores use before validation.
func (e *Entry) Normalize() {
	if e == nil {
		return
	}
	e.Name = strings.TrimSpace(e.Name)
	e.Tags = NormalizeTags(e.Tags)
	e.RemoteIdentity = NormalizeRemoteIdentity(e.RemoteIdentity)
	if e.RecoveryReceipt != nil {
		e.RecoveryReceipt.RemoteIdentity = NormalizeRemoteIdentity(e.RecoveryReceipt.RemoteIdentity)
	}
}

// Title is the human label, falling back to the stable ID for damaged display
// data. Persisted entries still require a name during validation.
func (e Entry) Title() string {
	if e.Name != "" {
		return e.Name
	}
	return e.ID
}

// HasTag reports membership after applying the same normalization as storage.
func (e Entry) HasTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	return tag != "" && slices.Contains(e.Tags, tag)
}

// LocationFor returns host's location without exposing the map for mutation.
func (e Entry) LocationFor(host string) (Location, bool) {
	location, ok := e.Locations[host]
	return location, ok
}

// SetLocation validates and records one host location.
func (e *Entry) SetLocation(host string, location Location) error {
	if e == nil {
		return errors.New("set location on nil catalog entry")
	}
	if err := validateLocationForSet(host, location); err != nil {
		return err
	}
	if e.Locations == nil {
		e.Locations = make(map[string]Location)
	}
	e.Locations[host] = location
	return nil
}

// Clone returns a deep copy suitable for mutation before an atomic update.
func (e Entry) Clone() *Entry {
	clone := e
	clone.Tags = append([]string(nil), e.Tags...)
	if e.Experiment != nil {
		value := *e.Experiment
		clone.Experiment = &value
	}
	if e.RecoveryReceipt != nil {
		value := *e.RecoveryReceipt
		clone.RecoveryReceipt = &value
	}
	if e.MoveIntent != nil {
		value := *e.MoveIntent
		clone.MoveIntent = &value
	}
	if e.Locations != nil {
		clone.Locations = make(map[string]Location, len(e.Locations))
		for host, location := range e.Locations {
			clone.Locations[host] = location
		}
	}
	return &clone
}

// Validate rejects records that later lifecycle operations could not interpret
// safely. Store.Create fills identity and timestamps before calling it.
func (e Entry) Validate() error {
	if e.SchemaVersion != CurrentSchemaVersion {
		return &UnsupportedSchemaError{ID: e.ID, Version: e.SchemaVersion}
	}
	if err := ValidateID(e.ID); err != nil {
		return err
	}
	switch e.Kind {
	case KindTry:
		if e.Experiment == nil {
			return fmt.Errorf("catalog asset %s: try kind requires experiment metadata", e.ID)
		}
	case KindRepository:
	default:
		return fmt.Errorf("catalog asset %s: unknown kind %q", e.ID, e.Kind)
	}
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("catalog asset %s: name is required", e.ID)
	}
	if !slices.Equal(e.Tags, NormalizeTags(e.Tags)) {
		return fmt.Errorf("catalog asset %s: tags must be normalized lowercase values", e.ID)
	}
	if e.Experiment != nil {
		switch e.Experiment.Phase {
		case PhaseActive, PhaseDeprecated, PhaseGraduated:
		default:
			return fmt.Errorf("catalog asset %s: unknown experiment phase %q", e.ID, e.Experiment.Phase)
		}
		switch {
		case e.Kind == KindTry && e.Experiment.Phase == PhaseGraduated:
			return fmt.Errorf("catalog asset %s: graduated experiment must be a repository", e.ID)
		case e.Kind == KindRepository && e.Experiment.Phase != PhaseGraduated:
			return fmt.Errorf("catalog asset %s: repository experiment history must be graduated", e.ID)
		}
		if e.Experiment.Started.IsZero() {
			return fmt.Errorf("catalog asset %s: experiment started timestamp is required", e.ID)
		}
		if e.Experiment.Slug == "" {
			return fmt.Errorf("catalog asset %s: experiment slug is required", e.ID)
		}
		if err := pathx.ValidateComponent(e.Experiment.Slug); err != nil {
			return fmt.Errorf("catalog asset %s: experiment slug: %w", e.ID, err)
		}
		if e.Experiment.OriginalPath == "" {
			return fmt.Errorf("catalog asset %s: experiment original path is required", e.ID)
		}
		switch e.Experiment.Phase {
		case PhaseDeprecated:
			if e.Experiment.DeprecatedAt.IsZero() || e.Experiment.DeprecatedPath == "" {
				return fmt.Errorf("catalog asset %s: deprecated experiment requires timestamp and path", e.ID)
			}
		case PhaseGraduated:
			if e.Experiment.GraduatedAt.IsZero() || e.Experiment.GraduatedPath == "" {
				return fmt.Errorf("catalog asset %s: graduated experiment requires timestamp and path", e.ID)
			}
		}
		if err := validateStringFields("experiment", []struct {
			name  string
			value string
		}{
			{"slug", e.Experiment.Slug},
			{"origin_url", e.Experiment.OriginURL},
			{"original_path", e.Experiment.OriginalPath},
			{"deprecated_path", e.Experiment.DeprecatedPath},
			{"graduated_path", e.Experiment.GraduatedPath},
		}); err != nil {
			return fmt.Errorf("catalog asset %s: %w", e.ID, err)
		}
	}
	hosts := make([]string, 0, len(e.Locations))
	for host := range e.Locations {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		if err := validateLocation(host, e.Locations[host]); err != nil {
			return fmt.Errorf("catalog asset %s: %w", e.ID, err)
		}
	}
	if e.RecoveryReceipt != nil {
		if err := validateRecoveryReceipt(*e.RecoveryReceipt); err != nil {
			return fmt.Errorf("catalog asset %s: %w", e.ID, err)
		}
	}
	if e.MoveIntent != nil {
		if err := validateMoveIntent(*e.MoveIntent); err != nil {
			return fmt.Errorf("catalog asset %s: %w", e.ID, err)
		}
	}
	if e.Created.IsZero() || e.Discovered.IsZero() || e.Updated.IsZero() {
		return fmt.Errorf("catalog asset %s: created, discovered and updated timestamps are required", e.ID)
	}
	return nil
}

func validateLocation(host string, location Location) error {
	return validateLocationValue(host, location, true)
}

func validateLocationForSet(host string, location Location) error {
	return validateLocationValue(host, location, false)
}

func validateLocationValue(host string, location Location, requireUpdated bool) error {
	if err := validateHostValue("location host", host); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value string
	}{
		{"current_path", location.CurrentPath},
		{"restore_path", location.RestorePath},
		{"real_path", location.RealPath},
		{"git_common_dir", location.GitCommonDir},
	}
	for _, field := range fields {
		if strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("location %s %s contains NUL", host, field.name)
		}
	}
	if requireUpdated && location.Updated.IsZero() {
		return fmt.Errorf("location %s: updated timestamp is required", host)
	}
	switch location.State {
	case LocationPresent:
		if location.CurrentPath == "" {
			return fmt.Errorf("location %s: present state requires current_path", host)
		}
	case LocationArchived:
		if location.CurrentPath == "" || location.RestorePath == "" {
			return fmt.Errorf("location %s: archived state requires current_path and restore_path", host)
		}
	case LocationEvicted:
		if location.RestorePath == "" {
			return fmt.Errorf("location %s: evicted state requires restore_path", host)
		}
	default:
		return fmt.Errorf("location %s: unknown state %q", host, location.State)
	}
	return nil
}

func validateRecoveryReceipt(receipt RecoveryReceipt) error {
	if err := validateHostValue("recovery receipt host", receipt.Host); err != nil {
		return err
	}
	if receipt.Method == "" || receipt.Method != strings.TrimSpace(receipt.Method) {
		return errors.New("recovery receipt method is required and must be normalized")
	}
	if receipt.Created.IsZero() || receipt.Verified.IsZero() {
		return errors.New("recovery receipt requires created and verified timestamps")
	}
	fields := []struct {
		name  string
		value string
	}{
		{"method", receipt.Method},
		{"source_path", receipt.SourcePath},
		{"restore_path", receipt.RestorePath},
		{"remote_identity", receipt.RemoteIdentity},
		{"remote_name", receipt.RemoteName},
		{"remote_url", receipt.RemoteURL},
		{"revision", receipt.Revision},
		{"refs_digest", receipt.RefsDigest},
		{"archive_path", receipt.ArchivePath},
		{"checksum", receipt.Checksum},
	}
	return validateStringFields("recovery receipt", fields)
}

func validateMoveIntent(intent MoveIntent) error {
	if err := validateHostValue("move intent host", intent.Host); err != nil {
		return err
	}
	if intent.Operation == "" || intent.Operation != strings.TrimSpace(intent.Operation) {
		return errors.New("move intent operation is required and must be normalized")
	}
	if intent.Started.IsZero() {
		return errors.New("move intent started timestamp is required")
	}
	if intent.SourcePath == "" || intent.DestinationPath == "" {
		return errors.New("move intent requires source and destination paths")
	}
	if err := validateStringFields("move intent", []struct {
		name  string
		value string
	}{
		{"operation", intent.Operation},
		{"source_path", intent.SourcePath},
		{"destination_path", intent.DestinationPath},
	}); err != nil {
		return err
	}
	if filepath.Clean(intent.SourcePath) == filepath.Clean(intent.DestinationPath) {
		return errors.New("move intent source and destination are identical")
	}
	return nil
}

func validateHostValue(name, host string) error {
	if host == "" {
		return fmt.Errorf("%s is required", name)
	}
	if host != strings.TrimSpace(host) || strings.ContainsRune(host, '\x00') {
		return fmt.Errorf("%s %q is not normalized", name, host)
	}
	return nil
}

func validateStringFields(scope string, fields []struct {
	name  string
	value string
}) error {
	for _, field := range fields {
		if strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("%s %s contains NUL", scope, field.name)
		}
	}
	return nil
}
