// Package perftrace records opt-in, process-local performance diagnostics.
// Events deliberately carry only fixed categorical fields and relative times:
// repository names, paths, commands, keys, URLs and raw errors never enter the
// trace schema.
package perftrace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// SchemaVersion is the on-disk trace format.
	SchemaVersion = 1
	// DefaultLimit bounds memory use when a dashboard is refreshed repeatedly.
	DefaultLimit = 4096
)

// Name identifies one stable trace boundary.
type Name string

const (
	CLIExecuteBegin         Name = "cli.execute_begin"
	CLIRootBuild            Name = "cli.root_build"
	AppLoad                 Name = "app.load"
	TUISetup                Name = "tui.setup"
	TUIRuntimeResolve       Name = "tui.runtime_resolve"
	TUIRuntimeList          Name = "tui.runtime_list"
	TUIProjectRootResolve   Name = "tui.project_root_resolve"
	TUICacheRemoteRead      Name = "tui.cache.remote_read"
	TUICacheFleetRead       Name = "tui.cache.fleet_read"
	TUIProgramRunBegin      Name = "tui.program_run_begin"
	TUIInitialViewReturned  Name = "tui.initial_view_returned"
	TUIFirstKeyReceived     Name = "tui.first_key_received"
	TUIKeyUpdate            Name = "tui.key_update"
	TUIProducerTasks        Name = "tui.producer.tasks"
	TUIProducerRepos        Name = "tui.producer.repos"
	TUIProducerTries        Name = "tui.producer.tries"
	TUIProducerRemote       Name = "tui.producer.remote"
	TUIProducerFleet        Name = "tui.producer.fleet"
	TUIProducerSkills       Name = "tui.producer.skills"
	TUIProducerMCP          Name = "tui.producer.mcp"
	TUIProducerTools        Name = "tui.producer.tools"
	TUIViewLoadRequested    Name = "tui.view.load_requested"
	TUIViewSnapshotAccepted Name = "tui.view.snapshot_accepted"
	TUIViewLoadFinished     Name = "tui.view.load_finished"
	TUIViewResultDiscarded  Name = "tui.view.result_discarded"
)

// View is a fixed dashboard view name.
type View string

const (
	ViewTasks  View = "tasks"
	ViewRepos  View = "repos"
	ViewFleet  View = "fleet"
	ViewTries  View = "try"
	ViewRemote View = "remote"
	ViewSkills View = "skills"
	ViewMCP    View = "mcp"
)

// Stage is a categorical lifecycle stage.
type Stage string

const (
	StageRequested        Stage = "requested"
	StageCacheAccepted    Stage = "cache_accepted"
	StageSnapshotAccepted Stage = "snapshot_accepted"
	StageFirstEnrichment  Stage = "first_enrichment"
	StageFinished         Stage = "finished"
	StageDiscarded        Stage = "discarded"
)

// Source identifies where a result came from.
type Source string

const (
	SourceCache Source = "cache"
	SourceLive  Source = "live"
)

// Freshness classifies a usable snapshot without making it authoritative.
type Freshness string

const (
	FreshnessFresh Freshness = "fresh"
	FreshnessStale Freshness = "stale"
)

// Outcome is a fixed terminal result class. Raw errors are intentionally absent.
type Outcome string

const (
	OutcomeSuccess    Outcome = "success"
	OutcomePartial    Outcome = "partial"
	OutcomeFailed     Outcome = "failed"
	OutcomeCanceled   Outcome = "canceled"
	OutcomeSuperseded Outcome = "superseded"
)

// Fields are the optional categorical dimensions accepted by a trace event.
type Fields struct {
	View       View
	Stage      Stage
	Source     Source
	Freshness  Freshness
	Outcome    Outcome
	Generation uint64
	Rows       *int
}

// Event is one immutable entry in the versioned trace.
type Event struct {
	Sequence   uint64    `json:"sequence"`
	AtMicros   int64     `json:"at_us"`
	DurationUS int64     `json:"duration_us,omitempty"`
	Name       Name      `json:"name"`
	View       View      `json:"view,omitempty"`
	Stage      Stage     `json:"stage,omitempty"`
	Source     Source    `json:"source,omitempty"`
	Freshness  Freshness `json:"freshness,omitempty"`
	Outcome    Outcome   `json:"outcome,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
	Rows       *int      `json:"rows,omitempty"`
}

// Snapshot is the complete frozen on-disk document.
type Snapshot struct {
	SchemaVersion int     `json:"schema_version"`
	DroppedEvents uint64  `json:"dropped_events"`
	Events        []Event `json:"events"`
}

// Recorder is safe for concurrent producers. A nil recorder is a no-op.
type Recorder struct {
	mu sync.Mutex

	clock  func() time.Time
	origin time.Time
	limit  int

	next    uint64
	dropped uint64
	events  []Event
	once    map[Name]struct{}
	frozen  bool
}

// New starts a recorder at the current time.
func New(limit int) *Recorder {
	return NewWithClock(time.Now, limit)
}

// NewWithClock supplies deterministic time for tests.
func NewWithClock(clock func() time.Time, limit int) *Recorder {
	if clock == nil {
		clock = time.Now
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Recorder{
		clock: clock, origin: clock(), limit: limit,
		events: make([]Event, 0, min(limit, 64)), once: map[Name]struct{}{},
	}
}

// Mark records an instantaneous event.
func (r *Recorder) Mark(name Name, fields Fields) {
	if r == nil {
		return
	}
	r.record(name, fields, r.clock(), 0)
}

// MarkOnce records only the first occurrence of name.
func (r *Recorder) MarkOnce(name Name, fields Fields) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.frozen {
		r.mu.Unlock()
		return
	}
	if _, exists := r.once[name]; exists {
		r.mu.Unlock()
		return
	}
	r.once[name] = struct{}{}
	now := r.clock()
	r.recordLocked(name, fields, now, 0)
	r.mu.Unlock()
}

// Start returns a closure that records one span. Calling the closure more than
// once is harmless.
func (r *Recorder) Start(name Name, fields Fields) func(Outcome) {
	if r == nil {
		return func(Outcome) {}
	}
	started := r.clock()
	var once sync.Once
	return func(outcome Outcome) {
		once.Do(func() {
			fields.Outcome = outcome
			finished := r.clock()
			duration := finished.Sub(started)
			duration = max(duration, 0)
			r.record(name, fields, started, duration)
		})
	}
}

// Freeze stops the recorder and returns an independent snapshot. Late events are
// ignored so background Bubble Tea commands cannot race trace persistence.
func (r *Recorder) Freeze() Snapshot {
	if r == nil {
		return Snapshot{SchemaVersion: SchemaVersion, Events: []Event{}}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true
	return Snapshot{
		SchemaVersion: SchemaVersion,
		DroppedEvents: r.dropped,
		Events:        append([]Event(nil), r.events...),
	}
}

func (r *Recorder) record(name Name, fields Fields, at time.Time, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return
	}
	r.recordLocked(name, fields, at, duration)
}

func (r *Recorder) recordLocked(name Name, fields Fields, at time.Time, duration time.Duration) {
	if len(r.events) >= r.limit {
		r.dropped++
		return
	}
	r.next++
	offset := max(at.Sub(r.origin), 0)
	r.events = append(r.events, Event{
		Sequence: r.next, AtMicros: offset.Microseconds(), DurationUS: duration.Microseconds(),
		Name: name, View: fields.View, Stage: fields.Stage, Source: fields.Source,
		Freshness: fields.Freshness, Outcome: fields.Outcome,
		Generation: fields.Generation, Rows: fields.Rows,
	})
}

// WriteNew writes a trace without overwriting an existing path. Traces are
// diagnostics rather than durable state, so an interrupted write is removed.
func WriteNew(path string, snapshot Snapshot) error {
	if !filepath.IsAbs(path) {
		return errors.New("performance trace path must be absolute")
	}
	if snapshot.SchemaVersion == 0 {
		snapshot.SchemaVersion = SchemaVersion
	}
	if snapshot.Events == nil {
		snapshot.Events = []Event{}
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode performance trace: %w", err)
	}
	data = append(data, '\n')

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create performance trace: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write performance trace: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync performance trace: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close performance trace: %w", err)
	}
	remove = false
	return nil
}
