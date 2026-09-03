package taskflow

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestServiceDispatchesExactActionHandler(t *testing.T) {
	var planned Action
	var warmApplied, coldApplied int
	handlers := Handlers{
		ParkWarm: {
			Plan: func(_ context.Context, request Request) (PlanSpec, error) {
				planned = request.Action
				return readyPlanSpec(), nil
			},
			Apply: func(_ context.Context, plan Plan) (Result, error) {
				if plan.Action != ParkWarm {
					t.Fatalf("warm handler received %s", plan.Action)
				}
				warmApplied++
				return NewResult(ResultSpec{}), nil
			},
		},
		ParkCold: {
			Plan: func(_ context.Context, _ Request) (PlanSpec, error) {
				return readyPlanSpec(), nil
			},
			Apply: func(_ context.Context, _ Plan) (Result, error) {
				coldApplied++
				return NewResult(ResultSpec{}), nil
			},
		},
	}
	service := NewService(handlers)
	delete(handlers, ParkWarm)

	request := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), &ParkWarmOptions{})
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planned != ParkWarm {
		t.Fatalf("planning action = %s, want %s", planned, ParkWarm)
	}
	if _, err := service.Apply(context.Background(), plan, Approve(plan.PlanID)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if warmApplied != 1 || coldApplied != 0 {
		t.Fatalf("handler counts warm=%d cold=%d, want 1/0", warmApplied, coldApplied)
	}
}

func TestServiceMissingHandlerCannotApply(t *testing.T) {
	var applied int
	service := NewService(Handlers{
		ParkWarm: {
			Plan: func(_ context.Context, _ Request) (PlanSpec, error) {
				return readyPlanSpec(), nil
			},
		},
		ParkCold: {
			Apply: func(_ context.Context, _ Plan) (Result, error) {
				applied++
				return NewResult(ResultSpec{}), nil
			},
		},
	})
	request := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{})
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	_, err = service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrHandlerUnavailable) {
		t.Fatalf("missing apply error = %v, want ErrHandlerUnavailable", err)
	}
	if applied != 0 {
		t.Fatalf("different action handler ran %d time(s)", applied)
	}

	empty := NewService(nil)
	_, err = empty.Plan(context.Background(), request)
	if !errors.Is(err, ErrHandlerUnavailable) {
		t.Fatalf("missing plan error = %v, want ErrHandlerUnavailable", err)
	}
}

func TestApprovalNeverBypassesNonReadyConditions(t *testing.T) {
	var applied int
	service := NewService(Handlers{
		ParkWarm: {
			Plan: func(_ context.Context, _ Request) (PlanSpec, error) {
				spec := readyPlanSpec()
				spec.Conditions[0].Verdict = VerdictBlocked
				spec.Conditions[0].Evidence = "recognized agent is working"
				return spec, nil
			},
			Apply: func(_ context.Context, _ Plan) (Result, error) {
				applied++
				return NewResult(ResultSpec{}), nil
			},
		},
	})
	request := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{})
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityBlocked {
		t.Fatalf("availability = %s, want BLOCKED", plan.Availability)
	}
	_, err = service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrPlanNotReady) || !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("blocked apply error = %v, want ErrPlanNotReady and ErrInvalidPlan", err)
	}
	var invalid *InvalidPlanError
	if !errors.As(err, &invalid) {
		t.Fatalf("blocked apply error type = %T, want *InvalidPlanError", err)
	}
	var notReady *PlanNotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("blocked apply error type = %T, want *PlanNotReadyError", err)
	}
	conditions := notReady.Conditions()
	conditions[0].Evidence = "mutated"
	if notReady.Conditions()[0].Evidence != "recognized agent is working" {
		t.Fatal("PlanNotReadyError exposes mutable condition backing")
	}
	if applied != 0 {
		t.Fatalf("blocked plan applied %d time(s)", applied)
	}
}

func TestServicePreservesPartialResultAlongsideError(t *testing.T) {
	executionErr := errors.New("remove checkout failed")
	service := NewService(Handlers{
		ParkCold: {
			Plan: func(_ context.Context, _ Request) (PlanSpec, error) {
				return readyPlanSpec(), nil
			},
			Apply: func(_ context.Context, plan Plan) (Result, error) {
				effects := plan.Effects()
				handoff := Handoff{Kind: HandoffDirectory, Path: "/repos/example", Label: "example"}
				return NewResult(ResultSpec{
					Steps: []StepResult{
						{Effect: effects[0], Status: StepCompleted, Detail: "runtime closed"},
						{Effect: effects[1], Status: StepFailed, Failure: executionErr.Error()},
					},
					Warnings:         []string{"runtime is already closed"},
					Recovery:         []string{"inspect the checkout and retry the same action"},
					Milestone:        MilestoneNone,
					Handoff:          &handoff,
					FreshSnapshotRef: "snapshot:after-1",
				}), executionErr
			},
		},
	})
	request := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkColdOptions{})
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, executionErr) {
		t.Fatalf("Apply error = %v, want execution error", err)
	}
	if !result.PartialSuccess {
		t.Fatal("partial result not marked PartialSuccess")
	}
	if len(result.AttemptedSteps()) != 2 || len(result.CompletedSteps()) != 1 || len(result.FailedSteps()) != 1 {
		t.Fatalf("step counts attempted=%d completed=%d failed=%d, want 2/1/1",
			len(result.AttemptedSteps()), len(result.CompletedSteps()), len(result.FailedSteps()))
	}
	if got := result.Warnings(); !reflect.DeepEqual(got, []string{"runtime is already closed"}) {
		t.Fatalf("warnings = %v", got)
	}
	if got := result.Recovery(); !reflect.DeepEqual(got, []string{"inspect the checkout and retry the same action"}) {
		t.Fatalf("recovery = %v", got)
	}
	if result.Milestone != MilestoneNone {
		t.Fatalf("milestone = %s, want none", result.Milestone)
	}
	if handoff, ok := result.Handoff(); !ok || handoff.Path != "/repos/example" {
		t.Fatalf("handoff = %+v, present=%t", handoff, ok)
	}
	if snapshot, ok := result.FreshSnapshotRef(); !ok || snapshot != "snapshot:after-1" {
		t.Fatalf("snapshot = %q, present=%t", snapshot, ok)
	}
}

func TestResultDefensivelyCopiesLedgerWarningsRecoveryAndHandoff(t *testing.T) {
	details := map[string]string{"path": "/worktree"}
	steps := []StepResult{{
		Effect: NewEffect("remove-worktree", "remove", "/worktree", true, false, details),
		Status: StepCompleted,
	}}
	warnings := []string{"warning"}
	recovery := []string{"retry"}
	handoff := Handoff{Kind: HandoffRuntime, Runtime: "herdr", RuntimeHandle: "workspace-1"}
	result := NewResult(ResultSpec{
		Steps: steps, Warnings: warnings, Recovery: recovery, Handoff: &handoff,
	})

	steps[0].Detail = "changed"
	steps[0].Effect.Description = "changed"
	warnings[0] = "changed"
	recovery[0] = "changed"
	handoff.RuntimeHandle = "changed"
	details["path"] = "changed"
	if got := result.AttemptedSteps()[0]; got.Detail != "" || got.Effect.Description != "remove" || got.Effect.Details.Map()["path"] != "/worktree" {
		t.Fatalf("result step changed through inputs: %+v", got)
	}
	if result.Warnings()[0] != "warning" || result.Recovery()[0] != "retry" {
		t.Fatal("result messages changed through inputs")
	}
	if got, _ := result.Handoff(); got.RuntimeHandle != "workspace-1" {
		t.Fatalf("handoff changed through input pointer: %+v", got)
	}

	gotSteps := result.AttemptedSteps()
	gotSteps[0].Effect.Details.entries[0].Value = "mutated"
	gotWarnings := result.Warnings()
	gotWarnings[0] = "mutated"
	gotRecovery := result.Recovery()
	gotRecovery[0] = "mutated"
	if result.AttemptedSteps()[0].Effect.Details.Map()["path"] != "/worktree" ||
		result.Warnings()[0] != "warning" || result.Recovery()[0] != "retry" {
		t.Fatal("result changed through accessor values")
	}
}

func TestServiceRejectsStaleAndInvalidPlansBeforeHandler(t *testing.T) {
	var applied int
	service := NewService(Handlers{
		ParkWarm: {
			Plan: func(_ context.Context, _ Request) (PlanSpec, error) { return readyPlanSpec(), nil },
			Apply: func(_ context.Context, _ Plan) (Result, error) {
				applied++
				return NewResult(ResultSpec{}), nil
			},
		},
	})
	request := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{})
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	stale := plan.Clone()
	stale.conditions[0].Evidence = "changed after planning"
	_, err = service.Apply(context.Background(), stale, Approve(stale.PlanID))
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("stale Apply error = %v, want ErrStalePlan", err)
	}
	var staleErr *StalePlanError
	if !errors.As(err, &staleErr) {
		t.Fatalf("stale Apply type = %T, want *StalePlanError", err)
	}

	invalid := plan.Clone()
	invalid.Target = task.Hot
	_, err = service.Apply(context.Background(), invalid, Approve(invalid.PlanID))
	if !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid Apply error = %v, want ErrInvalidPlan", err)
	}
	var invalidErr *InvalidPlanError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("invalid Apply type = %T, want *InvalidPlanError", err)
	}
	if applied != 0 {
		t.Fatalf("invalid plans applied %d time(s)", applied)
	}
}
