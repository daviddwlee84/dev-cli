// Package taskflow defines the side-effect-free lifecycle graph and the values
// exchanged between lifecycle planners, callers, and action executors.
package taskflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Field is one stable key/value member of an immutable Fields value.
type Field struct {
	Key   string
	Value string
}

// Fields is a map-shaped value whose backing storage is not exposed. Entries
// are kept in key order so fingerprints do not depend on map iteration order.
type Fields struct {
	entries []Field
}

// NewFields copies values into a deterministic immutable representation.
func NewFields(values map[string]string) Fields {
	entries := make([]Field, 0, len(values))
	for key, value := range values {
		entries = append(entries, Field{Key: key, Value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return Fields{entries: entries}
}

// Entries returns a copy in stable key order.
func (f Fields) Entries() []Field {
	return append([]Field(nil), f.entries...)
}

// Map returns a mutable copy of the fields.
func (f Fields) Map() map[string]string {
	out := make(map[string]string, len(f.entries))
	for _, entry := range f.entries {
		out[entry.Key] = entry.Value
	}
	return out
}

func (f Fields) clone() Fields { return Fields{entries: f.Entries()} }

// StringList is an ordered string value whose backing slice is not exposed.
type StringList struct {
	values []string
}

// NewStringList copies values without changing their order.
func NewStringList(values ...string) StringList {
	return StringList{values: append([]string(nil), values...)}
}

// Values returns a mutable copy of the strings.
func (s StringList) Values() []string { return append([]string(nil), s.values...) }

func (s StringList) clone() StringList { return NewStringList(s.values...) }

// Action names one guarded operation. Values are stable external identifiers,
// not presentation labels.
type Action string

const (
	ParkWarm       Action = "park-warm"
	ParkCold       Action = "park-cold"
	Resume         Action = "resume"
	CompleteDirect Action = "complete-direct"
	CompleteFF     Action = "complete-ff"
	ReviewHandoff  Action = "review-handoff"
	VerifyMerged   Action = "verify-merged"
	Retire         Action = "retire"
	Adopt          Action = "adopt"
	RemoveCheckout Action = "remove-checkout"
	RefreshRemote  Action = "refresh-remote"
	Reconcile      Action = "reconcile"
)

var actionOrder = []Action{
	ParkWarm,
	ParkCold,
	Resume,
	CompleteDirect,
	CompleteFF,
	ReviewHandoff,
	VerifyMerged,
	Retire,
	Adopt,
	RemoveCheckout,
	RefreshRemote,
	Reconcile,
}

// Actions returns every supported action in stable display order.
func Actions() []Action { return append([]Action(nil), actionOrder...) }

// Valid reports whether the action is part of this protocol version.
func (a Action) Valid() bool {
	for _, candidate := range actionOrder {
		if a == candidate {
			return true
		}
	}
	return false
}

// String returns the action's stable code.
func (a Action) String() string { return string(a) }

// Locator freezes the task and checkout identity selected by a caller. Dynamic
// probe fingerprints that do not have a dedicated field belong in PlanSpec's
// Authority map.
type Locator struct {
	RepoKey      string
	RowKey       string
	RowKind      string
	RepositoryID string
	GitCommonDir string

	TaskID       string
	TaskRevision string

	RepoPath     string
	CheckoutPath string
	Branch       string
	Base         string
	Upstream     string
	Remote       string

	HeadOID     string
	BaseOID     string
	UpstreamOID string

	Mode  task.CheckoutMode
	State task.State
}

// ObservationState says what happened when one live fact was requested. A
// skipped, loading, unknown, or failed observation is never equivalent to a
// known false value.
type ObservationState string

const (
	ObservationKnown   ObservationState = "known"
	ObservationUnknown ObservationState = "unknown"
	ObservationError   ObservationState = "error"
	ObservationSkipped ObservationState = "skipped"
	ObservationLoading ObservationState = "loading"
)

// Valid reports whether the observation state has defined semantics.
func (s ObservationState) Valid() bool {
	switch s {
	case ObservationKnown, ObservationUnknown, ObservationError, ObservationSkipped, ObservationLoading:
		return true
	default:
		return false
	}
}

// Observation is one fact and its provenance. ObservedAt is display/freshness
// metadata and is deliberately not itself plan authority.
type Observation struct {
	State      ObservationState
	Value      string
	Evidence   string
	Failure    string
	ObservedAt time.Time
	attributes Fields
}

// NewObservation copies attributes so later probe or UI mutation cannot alter
// the observation.
func NewObservation(state ObservationState, value, evidence, failure string, observedAt time.Time, attributes map[string]string) Observation {
	return Observation{
		State: state, Value: value, Evidence: evidence, Failure: failure,
		ObservedAt: observedAt, attributes: NewFields(attributes),
	}
}

// Attributes returns a mutable copy of observation metadata.
func (o Observation) Attributes() map[string]string { return o.attributes.Map() }

// Clone returns an independent observation value.
func (o Observation) Clone() Observation {
	o.attributes = o.attributes.clone()
	return o
}

// ConditionCode is a stable machine-readable safety or readiness check.
type ConditionCode string

// Verdict is the result of evaluating one condition.
type Verdict string

const (
	VerdictMet         Verdict = "met"
	VerdictNeedsInput  Verdict = "needs-input"
	VerdictBlocked     Verdict = "blocked"
	VerdictUnknown     Verdict = "unknown"
	VerdictError       Verdict = "error"
	VerdictUnsupported Verdict = "unsupported"
	VerdictCurrent     Verdict = "current"
)

// Requirement determines whether a condition can prevent Apply.
type Requirement string

const (
	RequirementRequired Requirement = "required"
	RequirementAdvisory Requirement = "advisory"
)

// Condition carries a stable decision plus operator-facing evidence and the
// narrow remediation for an unmet result.
type Condition struct {
	Code        ConditionCode
	Verdict     Verdict
	Requirement Requirement
	Evidence    string
	Remediation string
}

var stableCode = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// Validate rejects conditions that cannot be fingerprinted or aggregated with
// defined semantics.
func (c Condition) Validate() error {
	if !stableCode.MatchString(string(c.Code)) {
		return fmt.Errorf("condition code %q is not a stable lowercase code", c.Code)
	}
	switch c.Verdict {
	case VerdictMet, VerdictNeedsInput, VerdictBlocked, VerdictUnknown, VerdictError, VerdictUnsupported, VerdictCurrent:
	default:
		return fmt.Errorf("condition %s has unknown verdict %q", c.Code, c.Verdict)
	}
	switch c.Requirement {
	case RequirementRequired, RequirementAdvisory:
	default:
		return fmt.Errorf("condition %s has unknown requirement %q", c.Code, c.Requirement)
	}
	return nil
}

// ConditionFromObservation maps observation completeness to a fail-closed
// verdict. Capability policy such as unsupported remains an explicit condition
// decision rather than an inferred observation result.
func ConditionFromObservation(code ConditionCode, requirement Requirement, observation Observation, remediation string) Condition {
	verdict := VerdictUnknown
	switch observation.State {
	case ObservationKnown:
		verdict = VerdictMet
	case ObservationError:
		verdict = VerdictError
	case ObservationUnknown, ObservationSkipped, ObservationLoading:
		verdict = VerdictUnknown
	default:
		verdict = VerdictError
	}
	evidence := observation.Evidence
	if evidence == "" {
		switch {
		case observation.Failure != "":
			evidence = observation.Failure
		case observation.Value != "":
			evidence = observation.Value
		default:
			evidence = string(observation.State)
		}
	}
	return Condition{
		Code: code, Verdict: verdict, Requirement: requirement,
		Evidence: evidence, Remediation: remediation,
	}
}

// Availability is the aggregate action status shown by any caller.
type Availability string

const (
	AvailabilityReady       Availability = "ready"
	AvailabilityNeedsInput  Availability = "needs-input"
	AvailabilityBlocked     Availability = "blocked"
	AvailabilityUnknown     Availability = "unknown"
	AvailabilityError       Availability = "error"
	AvailabilityUnsupported Availability = "unsupported"
	AvailabilityCurrent     Availability = "current"
)

// AvailabilityFor derives action readiness from required conditions only. The
// order is fail-closed and deterministic when more than one required condition
// is unmet; advisory evidence never changes the result.
func AvailabilityFor(conditions []Condition) Availability {
	availability := AvailabilityReady
	priority := func(value Availability) int {
		switch value {
		case AvailabilityError:
			return 6
		case AvailabilityBlocked:
			return 5
		case AvailabilityNeedsInput:
			return 4
		case AvailabilityUnknown:
			return 3
		case AvailabilityUnsupported:
			return 2
		case AvailabilityCurrent:
			return 1
		default:
			return 0
		}
	}
	for _, condition := range conditions {
		if condition.Requirement == RequirementAdvisory {
			continue
		}
		candidate := AvailabilityReady
		if condition.Requirement != RequirementRequired {
			candidate = AvailabilityError
		} else {
			switch condition.Verdict {
			case VerdictMet:
				continue
			case VerdictNeedsInput:
				candidate = AvailabilityNeedsInput
			case VerdictBlocked:
				candidate = AvailabilityBlocked
			case VerdictUnknown:
				candidate = AvailabilityUnknown
			case VerdictError:
				candidate = AvailabilityError
			case VerdictUnsupported:
				candidate = AvailabilityUnsupported
			case VerdictCurrent:
				candidate = AvailabilityCurrent
			default:
				candidate = AvailabilityError
			}
		}
		if priority(candidate) > priority(availability) {
			availability = candidate
		}
	}
	return availability
}

// EffectCode is a stable machine-readable mutation or external operation.
type EffectCode string

// Effect is one ordered operation proposed by a plan. Details are immutable by
// interface and may carry exact refs, handles, or paths needed by an executor.
type Effect struct {
	Code        EffectCode
	Description string
	Target      string
	Destructive bool
	Network     bool
	Details     Fields
}

// NewEffect copies details into deterministic storage.
func NewEffect(code EffectCode, description, target string, destructive, network bool, details map[string]string) Effect {
	return Effect{
		Code: code, Description: description, Target: target,
		Destructive: destructive, Network: network, Details: NewFields(details),
	}
}

// Clone returns an independent effect value.
func (e Effect) Clone() Effect {
	e.Details = e.Details.clone()
	return e
}

// Validate rejects effects without a stable code.
func (e Effect) Validate() error {
	if !stableCode.MatchString(string(e.Code)) {
		return fmt.Errorf("effect code %q is not a stable lowercase code", e.Code)
	}
	for _, field := range e.Details.entries {
		if strings.TrimSpace(field.Key) == "" {
			return fmt.Errorf("effect %s has an empty detail key", e.Code)
		}
	}
	return nil
}
