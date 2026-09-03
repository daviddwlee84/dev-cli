// Package flowtui implements the independent, plan-first Bubble Tea interface
// for repository lifecycle flows. It owns presentation and interaction only;
// callers inject every repository read, plan, and mutation.
package flowtui

import (
	"context"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

// Freshness describes the local repository observation rendered by a Snapshot.
// Only FreshnessFresh with no snapshot error is authoritative display data.
type Freshness string

const (
	FreshnessUnknown Freshness = "unknown"
	FreshnessFresh   Freshness = "fresh"
	FreshnessStale   Freshness = "stale"
)

// SurfaceKind is the caller-projected ownership/topology class of one row.
type SurfaceKind string

const (
	SurfaceCanonical SurfaceKind = "canonical"
	SurfaceManaged   SurfaceKind = "managed"
	SurfaceUnmanaged SurfaceKind = "unmanaged"
	SurfaceHarness   SurfaceKind = "harness"
	SurfaceTaskOnly  SurfaceKind = "task-only"
	SurfaceConflict  SurfaceKind = "conflict"
)

// RepositoryRow is one stable repository-picker entry. FocusTarget optionally
// names a RowKey, TaskID, or checkout path to select after the repository loads.
type RepositoryRow struct {
	RepoKey     string
	Name        string
	Path        string
	Available   bool
	Error       string
	FocusTarget string
}

// Lines is an ordered, immutable-by-interface list of display evidence.
type Lines = taskflow.StringList

// NewLines copies display lines without changing their order.
func NewLines(values ...string) Lines { return taskflow.NewStringList(values...) }

// ActionChoice is one already-concrete operation variant supplied by the
// caller. The options value is normalized to an independent value at both the
// constructor and accessor boundaries.
type ActionChoice struct {
	ID          string
	Label       string
	Description string

	options taskflow.ActionOptions
}

// NewActionChoice copies the concrete action options. A nil/typed-nil option is
// retained as unavailable input and will never be sent to Plan.
func NewActionChoice(id, label, description string, options taskflow.ActionOptions) ActionChoice {
	return ActionChoice{
		ID: id, Label: label, Description: description,
		options: cloneActionOptions(options),
	}
}

// Action returns the stable taskflow action represented by this choice.
func (c ActionChoice) Action() taskflow.Action {
	options := cloneActionOptions(c.options)
	if options == nil {
		return ""
	}
	return options.Action()
}

// Options returns a defensive copy of the concrete options value.
func (c ActionChoice) Options() taskflow.ActionOptions { return cloneActionOptions(c.options) }

// Valid reports whether a choice carries stable UI identity and concrete
// taskflow options.
func (c ActionChoice) Valid() bool {
	return c.ID != "" && c.Label != "" && c.Action().Valid() && c.Options() != nil
}

func (c ActionChoice) clone() ActionChoice {
	c.options = cloneActionOptions(c.options)
	return c
}

// ActionList is an ordered, immutable-by-interface collection of choices.
type ActionList struct {
	values []ActionChoice
}

// NewActionList defensively copies choices and their option values.
func NewActionList(values ...ActionChoice) ActionList {
	out := ActionList{values: make([]ActionChoice, len(values))}
	for index, value := range values {
		out.values[index] = value.clone()
	}
	return out
}

// Values returns independent choices in caller-supplied order.
func (l ActionList) Values() []ActionChoice {
	out := make([]ActionChoice, len(l.values))
	for index, value := range l.values {
		out[index] = value.clone()
	}
	return out
}

// Len returns the number of ordered choices.
func (l ActionList) Len() int { return len(l.values) }

// SurfaceRow is one exact checkout or task-only row. Mode and State are empty
// for untracked rows. Drift, conflicts, evidence, and actions are ordered,
// immutable-by-interface values so copying a row cannot expose shared mutation.
type SurfaceRow struct {
	RowKey string
	Kind   SurfaceKind
	Label  string
	Path   string
	Branch string
	Base   string

	Mode  task.CheckoutMode
	State task.State

	Drift     Lines
	Conflicts Lines
	Evidence  Lines
	Locator   taskflow.Locator
	Actions   ActionList
}

func (r SurfaceRow) clone() SurfaceRow {
	r.Drift = NewLines(r.Drift.Values()...)
	r.Conflicts = NewLines(r.Conflicts.Values()...)
	r.Evidence = NewLines(r.Evidence.Values()...)
	r.Actions = NewActionList(r.Actions.Values()...)
	return r
}

// SurfaceList is an ordered, immutable-by-interface surface collection.
type SurfaceList struct {
	values []SurfaceRow
}

// NewSurfaceList copies rows and every collection they own.
func NewSurfaceList(values ...SurfaceRow) SurfaceList {
	out := SurfaceList{values: make([]SurfaceRow, len(values))}
	for index, value := range values {
		out.values[index] = value.clone()
	}
	return out
}

// Values returns independent rows in caller-supplied order.
func (l SurfaceList) Values() []SurfaceRow {
	out := make([]SurfaceRow, len(l.values))
	for index, value := range l.values {
		out[index] = value.clone()
	}
	return out
}

// Len returns the number of projected surfaces.
func (l SurfaceList) Len() int { return len(l.values) }

// OptionalRemoteObservation is a value-safe optional run-local remote result.
type OptionalRemoteObservation struct {
	value taskflow.RemoteObservation
	set   bool
}

// SomeRemoteObservation constructs a present optional observation.
func SomeRemoteObservation(value taskflow.RemoteObservation) OptionalRemoteObservation {
	return OptionalRemoteObservation{value: value.Clone(), set: true}
}

// RemoteObservation returns an independent value when present.
func (o OptionalRemoteObservation) RemoteObservation() (taskflow.RemoteObservation, bool) {
	return o.value.Clone(), o.set
}

// Snapshot is one repository-local observation. Error is part of a valid
// snapshot (for example incomplete enrichment); a callback error instead means
// the refresh failed and the previous snapshot must be retained.
type Snapshot struct {
	Repository RepositoryRow
	Surfaces   SurfaceList
	ObservedAt time.Time
	Freshness  Freshness
	Error      string
	Remote     OptionalRemoteObservation
}

// Clone returns a snapshot with independent surface/action storage.
func (s Snapshot) Clone() Snapshot {
	s.Surfaces = NewSurfaceList(s.Surfaces.Values()...)
	if remote, ok := s.Remote.RemoteObservation(); ok {
		s.Remote = SomeRemoteObservation(remote)
	}
	return s
}

// Authoritative reports whether the displayed local observation is fresh,
// complete, and associated with an available repository.
func (s Snapshot) Authoritative() bool {
	return s.Freshness == FreshnessFresh && s.Error == "" && s.Repository.Available
}

// Actions is the entire side-effect boundary. Every operation receives the
// exact stable keys selected by the model. Plan and Apply also receive an
// independent concrete options value; Apply additionally receives the exact
// locator and approved immutable plan.
type Actions struct {
	// AfterFirstView performs optional background work only after the initial
	// application frame has been computed.
	AfterFirstView   func(context.Context)
	ListRepositories func(context.Context) ([]RepositoryRow, error)
	LoadRepository   func(context.Context, string) (Snapshot, error)
	Plan             func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions) (taskflow.Plan, error)
	Apply            func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error)
}

func cloneActionOptions(options taskflow.ActionOptions) taskflow.ActionOptions {
	switch value := options.(type) {
	case nil:
		return nil
	case taskflow.ParkWarmOptions:
		return value
	case *taskflow.ParkWarmOptions:
		if value != nil {
			return *value
		}
	case taskflow.ParkColdOptions:
		return value
	case *taskflow.ParkColdOptions:
		if value != nil {
			return *value
		}
	case taskflow.ResumeOptions:
		return value
	case *taskflow.ResumeOptions:
		if value != nil {
			return *value
		}
	case taskflow.CompleteDirectOptions:
		return value
	case *taskflow.CompleteDirectOptions:
		if value != nil {
			return *value
		}
	case taskflow.CompleteFFOptions:
		return value
	case *taskflow.CompleteFFOptions:
		if value != nil {
			return *value
		}
	case taskflow.ReviewHandoffOptions:
		return value
	case *taskflow.ReviewHandoffOptions:
		if value != nil {
			return *value
		}
	case taskflow.VerifyMergedOptions:
		return value
	case *taskflow.VerifyMergedOptions:
		if value != nil {
			return *value
		}
	case taskflow.RetireOptions:
		return value
	case *taskflow.RetireOptions:
		if value != nil {
			return *value
		}
	case taskflow.AdoptOptions:
		value.Tags = taskflow.NewStringList(value.Tags.Values()...)
		return value
	case *taskflow.AdoptOptions:
		if value != nil {
			copy := *value
			copy.Tags = taskflow.NewStringList(value.Tags.Values()...)
			return copy
		}
	case taskflow.RemoveCheckoutOptions:
		return value
	case *taskflow.RemoveCheckoutOptions:
		if value != nil {
			return *value
		}
	case taskflow.RefreshRemoteOptions:
		return value
	case *taskflow.RefreshRemoteOptions:
		if value != nil {
			return *value
		}
	case taskflow.ReconcileOptions:
		value.Parameters = taskflow.NewFields(value.Parameters.Map())
		return value
	case *taskflow.ReconcileOptions:
		if value != nil {
			copy := *value
			copy.Parameters = taskflow.NewFields(value.Parameters.Map())
			return copy
		}
	}
	return nil
}
