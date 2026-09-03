package taskflow

import (
	"errors"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestTransitionTableExhaustive(t *testing.T) {
	modes := []task.CheckoutMode{task.ModeWorktree, task.ModeBranch, task.ModeDirect}
	states := []task.State{task.Hot, task.Warm, task.Cold, task.Done}
	actions := []Action{
		ParkWarm,
		ParkCold,
		Resume,
		CompleteDirect,
		CompleteFF,
		ReviewHandoff,
		VerifyMerged,
		Retire,
	}

	got := Transitions()
	wantCount := len(modes) * len(states) * len(actions)
	if len(got) != wantCount {
		t.Fatalf("transition count = %d, want %d", len(got), wantCount)
	}
	seen := make(map[transitionKey]bool, wantCount)
	for _, rule := range got {
		key := transitionKey{mode: rule.Mode, state: rule.Source, action: rule.Action}
		if seen[key] {
			t.Fatalf("duplicate transition cell: %+v", key)
		}
		seen[key] = true
	}

	for _, mode := range modes {
		for _, state := range states {
			for _, action := range actions {
				rule, ok := LookupTransition(mode, state, action)
				if !ok {
					t.Fatalf("missing transition for %s/%s/%s", mode, state, action)
				}
				want := expectedTransition(mode, state, action)
				if rule.Allowed != want.Allowed || rule.Target != want.Target ||
					rule.StatePreserving != want.StatePreserving || rule.RemovesTask != want.RemovesTask ||
					rule.Milestone != want.Milestone {
					t.Errorf("transition %s/%s/%s = %+v, want %+v", mode, state, action, rule, want)
				}
			}
		}
	}
}

func TestRequestValidationUsesEveryTransitionCell(t *testing.T) {
	for _, rule := range Transitions() {
		request := Request{
			Locator: Locator{Mode: rule.Mode, State: rule.Source},
			Action:  rule.Action,
			Options: transitionOptions(rule.Action),
		}
		err := request.Validate()
		if rule.Allowed {
			if err != nil {
				t.Errorf("allowed request %s/%s/%s: %v", rule.Mode, rule.Source, rule.Action, err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidRequest) || !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("denied request %s/%s/%s error = %v, want invalid request and transition", rule.Mode, rule.Source, rule.Action, err)
		}
	}
}

func transitionOptions(action Action) ActionOptions {
	switch action {
	case ParkWarm:
		return ParkWarmOptions{}
	case ParkCold:
		return ParkColdOptions{}
	case Resume:
		return ResumeOptions{}
	case CompleteDirect:
		return CompleteDirectOptions{}
	case CompleteFF:
		return CompleteFFOptions{}
	case ReviewHandoff:
		return ReviewHandoffOptions{}
	case VerifyMerged:
		return VerifyMergedOptions{}
	case Retire:
		return RetireOptions{}
	default:
		panic("non-transition action " + action)
	}
}

func expectedTransition(mode task.CheckoutMode, state task.State, action Action) TransitionRule {
	rule := TransitionRule{Mode: mode, Source: state, Action: action}
	allow := func(target task.State, preserving, removes bool, milestone Milestone) TransitionRule {
		rule.Allowed = true
		rule.Target = target
		rule.StatePreserving = preserving
		rule.RemovesTask = removes
		rule.Milestone = milestone
		return rule
	}

	if mode == task.ModeDirect && state == task.Cold {
		return rule
	}
	if action == Retire && state == task.Done {
		return allow("", false, true, MilestoneRetired)
	}
	if state == task.Done || state == task.Cold && action != Resume {
		return rule
	}

	switch action {
	case ParkWarm:
		if state == task.Hot {
			return allow(task.Warm, false, false, MilestoneNone)
		}
		if state == task.Warm {
			return allow(task.Warm, true, false, MilestoneNone)
		}
	case ParkCold:
		if mode != task.ModeDirect && (state == task.Hot || state == task.Warm) {
			return allow(task.Cold, false, false, MilestoneNone)
		}
	case Resume:
		if state == task.Warm || mode != task.ModeDirect && state == task.Cold {
			return allow(task.Hot, false, false, MilestoneNone)
		}
	case CompleteDirect:
		if mode == task.ModeDirect && (state == task.Hot || state == task.Warm) {
			return allow(task.Done, false, false, MilestoneMerged)
		}
	case CompleteFF, VerifyMerged:
		if mode != task.ModeDirect && (state == task.Hot || state == task.Warm) {
			return allow(task.Done, false, false, MilestoneMerged)
		}
	case ReviewHandoff:
		if mode != task.ModeDirect && (state == task.Hot || state == task.Warm) {
			return allow(state, true, false, MilestoneReviewReady)
		}
	}
	return rule
}

func TestTransitionTableRejectsInvalidColdAndDoneEdges(t *testing.T) {
	for _, action := range TransitionActions() {
		rule, ok := LookupTransition(task.ModeDirect, task.Cold, action)
		if !ok || rule.Allowed {
			t.Errorf("direct/COLD/%s should be denied, got %+v, found=%t", action, rule, ok)
		}
	}

	for _, mode := range []task.CheckoutMode{task.ModeWorktree, task.ModeBranch, task.ModeDirect} {
		for _, action := range TransitionActions() {
			rule, _ := LookupTransition(mode, task.Done, action)
			want := action == Retire
			if rule.Allowed != want {
				t.Errorf("%s/DONE/%s allowed=%t, want %t", mode, action, rule.Allowed, want)
			}
		}
	}

	for _, mode := range []task.CheckoutMode{task.ModeWorktree, task.ModeBranch} {
		for _, action := range TransitionActions() {
			rule, _ := LookupTransition(mode, task.Cold, action)
			want := action == Resume
			if rule.Allowed != want {
				t.Errorf("%s/COLD/%s allowed=%t, want %t", mode, action, rule.Allowed, want)
			}
		}
	}
}

func TestTransitionStatePreservationAndDomainActions(t *testing.T) {
	for _, rule := range Transitions() {
		if !rule.Allowed || !rule.HasTarget() || rule.Target != rule.Source {
			continue
		}
		if !rule.StatePreserving {
			t.Errorf("same-state rule is not marked preserving: %+v", rule)
		}
		switch rule.Action {
		case ParkWarm:
			if rule.Source != task.Warm {
				t.Errorf("ParkWarm same-state edge starts at %s", rule.Source)
			}
		case ReviewHandoff:
		default:
			t.Errorf("undocumented same-state action: %+v", rule)
		}
	}

	for _, action := range []Action{Adopt, RemoveCheckout, RefreshRemote, Reconcile} {
		if _, ok := LookupTransition(task.ModeWorktree, task.Hot, action); ok {
			t.Errorf("domain action %s unexpectedly appears in persisted table", action)
		}
	}

	_, err := RequireTransition(task.ModeDirect, task.Cold, Resume)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("direct COLD error = %v, want ErrInvalidTransition", err)
	}
	var transitionErr *InvalidTransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("direct COLD error type = %T, want *InvalidTransitionError", err)
	}
}

func TestConditionAvailabilityAggregation(t *testing.T) {
	tests := []struct {
		verdict Verdict
		want    Availability
	}{
		{VerdictMet, AvailabilityReady},
		{VerdictNeedsInput, AvailabilityNeedsInput},
		{VerdictBlocked, AvailabilityBlocked},
		{VerdictUnknown, AvailabilityUnknown},
		{VerdictError, AvailabilityError},
		{VerdictUnsupported, AvailabilityUnsupported},
		{VerdictCurrent, AvailabilityCurrent},
	}
	for _, test := range tests {
		t.Run(string(test.verdict), func(t *testing.T) {
			conditions := []Condition{{
				Code: "test-condition", Verdict: test.verdict, Requirement: RequirementRequired,
			}}
			if got := AvailabilityFor(conditions); got != test.want {
				t.Fatalf("availability = %s, want %s", got, test.want)
			}
		})
	}

	advisoryVerdicts := []Verdict{
		VerdictNeedsInput, VerdictBlocked, VerdictUnknown, VerdictError, VerdictUnsupported, VerdictCurrent,
	}
	for _, verdict := range advisoryVerdicts {
		conditions := []Condition{
			{Code: "required", Verdict: VerdictMet, Requirement: RequirementRequired},
			{Code: "advisory", Verdict: verdict, Requirement: RequirementAdvisory},
		}
		if got := AvailabilityFor(conditions); got != AvailabilityReady {
			t.Errorf("advisory %s produced %s, want READY", verdict, got)
		}
	}

	conditions := []Condition{
		{Code: "unknown", Verdict: VerdictUnknown, Requirement: RequirementRequired},
		{Code: "input", Verdict: VerdictNeedsInput, Requirement: RequirementRequired},
		{Code: "blocked", Verdict: VerdictBlocked, Requirement: RequirementRequired},
		{Code: "error", Verdict: VerdictError, Requirement: RequirementRequired},
	}
	if got := AvailabilityFor(conditions); got != AvailabilityError {
		t.Fatalf("mixed availability = %s, want ERROR", got)
	}
	if got := AvailabilityFor(nil); got != AvailabilityReady {
		t.Fatalf("empty conditions = %s, want READY", got)
	}
	malformed := []Condition{{Code: "malformed", Verdict: VerdictMet, Requirement: "mystery"}}
	if got := AvailabilityFor(malformed); got != AvailabilityError {
		t.Fatalf("malformed requirement = %s, want ERROR", got)
	}
}

func TestUnknownAndErrorObservationsFailClosed(t *testing.T) {
	states := []struct {
		state ObservationState
		want  Verdict
	}{
		{ObservationKnown, VerdictMet},
		{ObservationUnknown, VerdictUnknown},
		{ObservationError, VerdictError},
		{ObservationSkipped, VerdictUnknown},
		{ObservationLoading, VerdictUnknown},
	}
	for _, test := range states {
		observation := NewObservation(test.state, "value", "evidence", "failure", time.Now(), nil)
		condition := ConditionFromObservation("observed", RequirementRequired, observation, "refresh")
		if condition.Verdict != test.want {
			t.Errorf("observation %s verdict = %s, want %s", test.state, condition.Verdict, test.want)
		}
		availability := AvailabilityFor([]Condition{condition})
		if test.state != ObservationKnown && availability == AvailabilityReady {
			t.Errorf("observation %s unexpectedly became READY", test.state)
		}
	}
}
