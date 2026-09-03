package taskflow

import (
	"fmt"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

// ConfirmationKind is the second, action-specific confirmation required after
// a caller has reviewed an exact PlanID. Apply always also requires affirmative
// Approval, including when this value is ConfirmationNone.
type ConfirmationKind string

const (
	ConfirmationNone     ConfirmationKind = ""
	ConfirmationApproval ConfirmationKind = "approval"
	ConfirmationTyped    ConfirmationKind = "typed"
)

// Confirmation describes the action-specific prompt. Prompt is presentation;
// Token is authority when Kind is ConfirmationTyped.
type Confirmation struct {
	Kind   ConfirmationKind
	Prompt string
	Token  string
}

// Validate checks that typed confirmation has an exact non-empty token and
// other classes cannot accidentally retain one.
func (c Confirmation) Validate() error {
	switch c.Kind {
	case ConfirmationNone, ConfirmationApproval:
		if c.Token != "" {
			return fmt.Errorf("confirmation %q cannot carry a typed token", c.Kind)
		}
	case ConfirmationTyped:
		if c.Token == "" {
			return fmt.Errorf("typed confirmation token is required")
		}
	default:
		return fmt.Errorf("unknown confirmation kind %q", c.Kind)
	}
	return nil
}

// Approval is an affirmative decision bound to one exact PlanID. Granted maps
// CLI --yes semantics into the protocol; it does not alter any condition.
type Approval struct {
	PlanID  string
	Granted bool
	Token   string
}

// Approve constructs an affirmative approval without a typed token.
func Approve(planID string) Approval { return Approval{PlanID: planID, Granted: true} }

// ApproveWithToken constructs an affirmative approval with exact typed text.
func ApproveWithToken(planID, token string) Approval {
	return Approval{PlanID: planID, Granted: true, Token: token}
}

// Milestone is an outcome overlay, never a persisted task state.
type Milestone string

const (
	MilestoneNone        Milestone = ""
	MilestoneAdopted     Milestone = "adopted"
	MilestoneReviewReady Milestone = "review-ready"
	MilestoneMerged      Milestone = "merged"
	MilestoneRetired     Milestone = "retired"
	MilestoneReconciled  Milestone = "reconciled"
)

// HandoffKind names work a caller may perform after leaving an alternate-screen
// UI. A handoff is an outcome, not proof that activation already happened.
type HandoffKind string

const (
	HandoffDirectory HandoffKind = "directory"
	HandoffRuntime   HandoffKind = "runtime"
	HandoffURL       HandoffKind = "url"
)

// Handoff is a value-only navigation or activation target.
type Handoff struct {
	Kind          HandoffKind
	Path          string
	Runtime       string
	RuntimeHandle string
	URL           string
	Label         string
}

// StepStatus is the last known state of one attempted effect.
type StepStatus string

const (
	StepAttempted StepStatus = "attempted"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
)

// StepResult is one entry in the ordered execution ledger. Failure is text
// because the typed operation error is returned separately from Result.
type StepResult struct {
	Effect     Effect
	Status     StepStatus
	Detail     string
	Failure    string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Clone returns a step with independent effect detail storage.
func (s StepResult) Clone() StepResult {
	s.Effect = s.Effect.Clone()
	return s
}

// NamedRefObservation is one exact local Git ref observation. Missing refs are
// represented by Exists=false; probe failures retain safe text separately and
// never collapse into a known missing ref.
type NamedRefObservation struct {
	Ref     string
	Exists  bool
	OID     string
	Failure string
}

// RemoteRefsObservation is the four named refs relevant to a manual remote
// refresh. ObservedAt is result freshness only and never contributes to PlanID.
type RemoteRefsObservation struct {
	LocalHead  NamedRefObservation
	LocalBase  NamedRefObservation
	RemoteHead NamedRefObservation
	RemoteBase NamedRefObservation
	ObservedAt time.Time
}

// RemoteReviewFailureKind is a portable classification for a failed provider
// observation. Provider diagnostics remain in Failure without being inferred as
// review absence.
type RemoteReviewFailureKind string

const (
	RemoteReviewFailureAmbiguous        RemoteReviewFailureKind = "ambiguous"
	RemoteReviewFailureMalformed        RemoteReviewFailureKind = "malformed"
	RemoteReviewFailureUnsupported      RemoteReviewFailureKind = "unsupported"
	RemoteReviewFailureMissingCLI       RemoteReviewFailureKind = "missing-cli"
	RemoteReviewFailureMissingExtension RemoteReviewFailureKind = "missing-extension"
	RemoteReviewFailureProvider         RemoteReviewFailureKind = "provider"
)

// RemoteReviewObservation is one explicit review-query outcome. State=known and
// Exists=false is authoritative absence; every provider or decoding failure is
// State=error with Failure populated.
type RemoteReviewObservation struct {
	State       ObservationState
	Exists      bool
	Provider    forge.Kind
	ReviewState forge.ReviewState
	Draft       bool
	URL         string
	ObservedAt  time.Time
	FailureKind RemoteReviewFailureKind
	Failure     string
}

// RemoteObservation is the structured, run-local output of RefreshRemote. It is
// deliberately not a handoff or persistent cache. All fields are value-only so
// Result can return defensive copies without sharing mutable backing storage.
type RemoteObservation struct {
	RemoteName   string
	RemoteURL    string
	Provider     forge.Kind
	Repository   string
	Head         string
	Base         string
	BeforeRefs   RemoteRefsObservation
	AfterRefs    RemoteRefsObservation
	HasAfterRefs bool
	Review       RemoteReviewObservation
	HasReview    bool
}

// Clone returns an independent value. The explicit method keeps this contract
// stable if collection-valued observation fields are added later.
func (o RemoteObservation) Clone() RemoteObservation { return o }

// ResultSpec is mutable construction input. NewResult copies every collection
// and optional value before returning a Result.
type ResultSpec struct {
	Steps            []StepResult
	Warnings         []string
	Recovery         []string
	PartialSuccess   bool
	Milestone        Milestone
	Handoff          *Handoff
	Remote           *RemoteObservation
	FreshSnapshotRef string
}

// Result is a value-safe execution ledger. It deliberately has no rollback
// field: completed effects remain completed and recovery describes the next
// safe action instead of claiming reversal.
type Result struct {
	PartialSuccess bool
	Milestone      Milestone

	steps            []StepResult
	warnings         []string
	recovery         []string
	handoff          Handoff
	hasHandoff       bool
	remote           RemoteObservation
	hasRemote        bool
	freshSnapshotRef string
}

// NewResult defensively copies a result specification.
func NewResult(spec ResultSpec) Result {
	result := Result{
		PartialSuccess:   spec.PartialSuccess,
		Milestone:        spec.Milestone,
		warnings:         append([]string(nil), spec.Warnings...),
		recovery:         append([]string(nil), spec.Recovery...),
		freshSnapshotRef: spec.FreshSnapshotRef,
	}
	result.steps = cloneSteps(spec.Steps)
	if spec.Handoff != nil {
		result.handoff = *spec.Handoff
		result.hasHandoff = true
	}
	if spec.Remote != nil {
		result.remote = spec.Remote.Clone()
		result.hasRemote = true
	}
	if !result.PartialSuccess && len(result.CompletedSteps()) > 0 && len(result.FailedSteps()) > 0 {
		result.PartialSuccess = true
	}
	return result
}

// Clone returns a result with independent ledger and message slices.
func (r Result) Clone() Result {
	r.steps = cloneSteps(r.steps)
	r.warnings = append([]string(nil), r.warnings...)
	r.recovery = append([]string(nil), r.recovery...)
	return r
}

// AttemptedSteps returns every effect entered by the executor, in order.
func (r Result) AttemptedSteps() []StepResult { return cloneSteps(r.steps) }

// CompletedSteps returns completed effects in ledger order.
func (r Result) CompletedSteps() []StepResult { return stepsWithStatus(r.steps, StepCompleted) }

// FailedSteps returns failed effects in ledger order.
func (r Result) FailedSteps() []StepResult { return stepsWithStatus(r.steps, StepFailed) }

// Warnings returns non-fatal diagnostics in emission order.
func (r Result) Warnings() []string { return append([]string(nil), r.warnings...) }

// Recovery returns ordered, safe follow-up instructions after partial work.
func (r Result) Recovery() []string { return append([]string(nil), r.recovery...) }

// Handoff returns the optional post-UI navigation or activation target.
func (r Result) Handoff() (Handoff, bool) { return r.handoff, r.hasHandoff }

// RemoteObservation returns the optional run-local remote/ref/review evidence.
// The returned value shares no mutable backing storage with Result.
func (r Result) RemoteObservation() (RemoteObservation, bool) {
	return r.remote.Clone(), r.hasRemote
}

// FreshSnapshotRef returns the optional freshly loaded after-snapshot reference.
func (r Result) FreshSnapshotRef() (string, bool) {
	return r.freshSnapshotRef, r.freshSnapshotRef != ""
}

func cloneSteps(steps []StepResult) []StepResult {
	out := make([]StepResult, len(steps))
	for index, step := range steps {
		out[index] = step.Clone()
	}
	return out
}

func stepsWithStatus(steps []StepResult, status StepStatus) []StepResult {
	out := make([]StepResult, 0, len(steps))
	for _, step := range steps {
		if step.Status == status {
			out = append(out, step.Clone())
		}
	}
	return out
}
