package taskflow

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidRequest marks an action request with mismatched options or an
	// illegal source-state transition.
	ErrInvalidRequest = errors.New("invalid taskflow request")
	// ErrInvalidTransition marks a mode, source state, and action combination
	// that is outside the approved lifecycle graph.
	ErrInvalidTransition = errors.New("invalid taskflow transition")
	// ErrInvalidPlan marks a malformed, internally inconsistent, or non-ready
	// plan presented to Apply.
	ErrInvalidPlan = errors.New("invalid taskflow plan")
	// ErrStalePlan marks a plan or approval whose exact authority identity no
	// longer matches.
	ErrStalePlan = errors.New("stale taskflow plan")
	// ErrApprovalRequired marks an Apply call without an affirmative approval.
	ErrApprovalRequired = errors.New("taskflow approval required")
	// ErrInvalidApproval marks a missing or incorrect typed confirmation.
	ErrInvalidApproval = errors.New("invalid taskflow approval")
	// ErrPlanNotReady marks a plan whose required conditions do not aggregate to
	// READY.
	ErrPlanNotReady = errors.New("taskflow plan is not ready")
	// ErrHandlerUnavailable marks an action for which the requested planning or
	// apply hook was not injected.
	ErrHandlerUnavailable = errors.New("taskflow action handler unavailable")
	// ErrLocatorUnavailable marks a generic dispatcher that was not constructed
	// with the guarded managed-task locator.
	ErrLocatorUnavailable = errors.New("taskflow exact task locator unavailable")
)

// InvalidTransitionError describes one denied graph cell.
type InvalidTransitionError struct {
	Rule TransitionRule
}

func (e *InvalidTransitionError) Error() string {
	reason := e.Rule.Reason
	if reason == "" {
		reason = "the edge is not in the approved lifecycle graph"
	}
	return fmt.Sprintf("%s task in %s cannot %s: %s", e.Rule.Mode, e.Rule.Source, e.Rule.Action, reason)
}

func (e *InvalidTransitionError) Unwrap() error { return ErrInvalidTransition }

// InvalidPlanError provides a typed explanation while remaining discoverable
// through errors.Is(err, ErrInvalidPlan). Cause may expose a more specific
// sentinel such as ErrPlanNotReady.
type InvalidPlanError struct {
	PlanID string
	Reason string
	Cause  error
}

func (e *InvalidPlanError) Error() string {
	prefix := "invalid taskflow plan"
	if e.PlanID != "" {
		prefix = fmt.Sprintf("invalid taskflow plan %s", e.PlanID)
	}
	if e.Reason != "" && e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", prefix, e.Reason, e.Cause)
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", prefix, e.Reason)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", prefix, e.Cause)
	}
	return prefix
}

func (e *InvalidPlanError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrInvalidPlan}
	}
	return []error{ErrInvalidPlan, e.Cause}
}

// StalePlanError identifies the expected and observed plan authority. It is
// used both for a modified plan and for approval of a different PlanID.
type StalePlanError struct {
	ExpectedPlanID               string
	ActualPlanID                 string
	ExpectedAuthorityFingerprint string
	ActualAuthorityFingerprint   string
	Reason                       string
}

func (e *StalePlanError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "plan authority changed"
	}
	switch {
	case e.ExpectedPlanID != "" || e.ActualPlanID != "":
		return fmt.Sprintf("%v: %s (expected plan %q, actual %q)", ErrStalePlan, reason, e.ExpectedPlanID, e.ActualPlanID)
	case e.ExpectedAuthorityFingerprint != "" || e.ActualAuthorityFingerprint != "":
		return fmt.Sprintf("%v: %s (expected authority %q, actual %q)", ErrStalePlan, reason, e.ExpectedAuthorityFingerprint, e.ActualAuthorityFingerprint)
	default:
		return fmt.Sprintf("%v: %s", ErrStalePlan, reason)
	}
}

func (e *StalePlanError) Unwrap() error { return ErrStalePlan }

// ApprovalError describes a missing affirmative approval or typed token.
type ApprovalError struct {
	PlanID string
	Reason string
	Cause  error
}

func (e *ApprovalError) Error() string {
	if e.PlanID == "" {
		return fmt.Sprintf("approval: %s: %v", e.Reason, e.Cause)
	}
	return fmt.Sprintf("approval for plan %s: %s: %v", e.PlanID, e.Reason, e.Cause)
}

func (e *ApprovalError) Unwrap() error { return e.Cause }

// PlanNotReadyError retains the conditions that prevented Apply.
type PlanNotReadyError struct {
	PlanID       string
	Availability Availability
	conditions   []Condition
}

func (e *PlanNotReadyError) Error() string {
	return fmt.Sprintf("%v: plan %s is %s", ErrPlanNotReady, e.PlanID, e.Availability)
}

func (e *PlanNotReadyError) Unwrap() error { return ErrPlanNotReady }

// Conditions returns a copy of the non-ready plan's conditions.
func (e *PlanNotReadyError) Conditions() []Condition {
	return append([]Condition(nil), e.conditions...)
}

// HandlerUnavailableError names the missing action stage.
type HandlerUnavailableError struct {
	Action Action
	Stage  string
}

func (e *HandlerUnavailableError) Error() string {
	return fmt.Sprintf("%v: no %s hook for %s", ErrHandlerUnavailable, e.Stage, e.Action)
}

func (e *HandlerUnavailableError) Unwrap() error { return ErrHandlerUnavailable }
