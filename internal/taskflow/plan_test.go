package taskflow

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

func testLocator(mode task.CheckoutMode, state task.State) Locator {
	return Locator{
		RepoKey: "repo-key", RowKey: "row-key", RowKind: "managed",
		RepositoryID: "/repos/example/.git", GitCommonDir: "/repos/example/.git",
		TaskID: "example__feature", TaskRevision: "revision-1",
		RepoPath: "/repos/example", CheckoutPath: "/worktrees/example-feature",
		Branch: "feature", Base: "main", Upstream: "origin/feature", Remote: "origin",
		HeadOID: "1111111", BaseOID: "2222222", UpstreamOID: "3333333",
		Mode: mode, State: state,
	}
}

func mustRequest(t *testing.T, locator Locator, options ActionOptions) Request {
	t.Helper()
	request, err := NewRequest(locator, options)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return request
}

func readyPlanSpec() PlanSpec {
	return PlanSpec{
		Authority: map[string]string{
			"runtime": "none",
			"status":  "clean",
		},
		Conditions: []Condition{
			{Code: "checkout-clean", Verdict: VerdictMet, Requirement: RequirementRequired, Evidence: "clean"},
			{Code: "artifact-ready", Verdict: VerdictMet, Requirement: RequirementRequired, Evidence: "ready"},
		},
		Effects: []Effect{
			NewEffect("close-runtime", "close runtime", "runtime-1", false, false, map[string]string{"backend": "herdr"}),
			NewEffect("write-task", "write task state", "example__feature", false, false, map[string]string{"state": "warm"}),
		},
		RetainedResources: []string{"branch:feature", "checkout:/worktrees/example-feature"},
		Confirmation:      Confirmation{Kind: ConfirmationApproval, Prompt: "Park this task?"},
		FallbackCommand:   "dev park example__feature",
		Summary:           "Park feature warm",
		DisplayedAt:       time.Unix(100, 0),
	}
}

func mustBuildPlan(t *testing.T, request Request, spec PlanSpec) Plan {
	t.Helper()
	plan, err := BuildPlan(request, spec)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return plan
}

func TestActionsAndTypedOptionsCoverProtocol(t *testing.T) {
	wantActions := []Action{
		ParkWarm, ParkCold, Resume, CompleteDirect, CompleteFF, ReviewHandoff,
		VerifyMerged, Retire, Adopt, RemoveCheckout, RefreshRemote, Reconcile,
	}
	if got := Actions(); !reflect.DeepEqual(got, wantActions) {
		t.Fatalf("Actions() = %v, want %v", got, wantActions)
	}

	tests := []struct {
		locator Locator
		options ActionOptions
	}{
		{testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{}},
		{testLocator(task.ModeWorktree, task.Hot), ParkColdOptions{}},
		{testLocator(task.ModeWorktree, task.Cold), ResumeOptions{}},
		{testLocator(task.ModeDirect, task.Hot), CompleteDirectOptions{}},
		{testLocator(task.ModeBranch, task.Hot), CompleteFFOptions{}},
		{testLocator(task.ModeBranch, task.Warm), ReviewHandoffOptions{}},
		{testLocator(task.ModeWorktree, task.Hot), VerifyMergedOptions{}},
		{testLocator(task.ModeWorktree, task.Done), RetireOptions{}},
		{Locator{RowKey: "unmanaged"}, AdoptOptions{Mode: task.ModeWorktree, State: task.Warm}},
		{Locator{RowKey: "unmanaged"}, RemoveCheckoutOptions{}},
		{testLocator(task.ModeBranch, task.Done), RefreshRemoteOptions{FetchRefs: true}},
		{Locator{RowKey: "drift"}, ReconcileOptions{Name: "repair-moved-path"}},
	}
	for _, test := range tests {
		request, err := NewRequest(test.locator, test.options)
		if err != nil {
			t.Errorf("NewRequest(%s): %v", test.options.Action(), err)
			continue
		}
		if request.Action != test.options.Action() {
			t.Errorf("request action = %s, want %s", request.Action, test.options.Action())
		}
	}

	var nilOptions *ParkWarmOptions
	if _, err := NewRequest(testLocator(task.ModeWorktree, task.Hot), nilOptions); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("typed-nil options error = %v, want ErrInvalidRequest", err)
	}
}

func TestPlanIDsAreDeterministicAndExcludeDisplayTimestamps(t *testing.T) {
	locator := testLocator(task.ModeWorktree, task.Hot)
	requestA := mustRequest(t, locator, ParkWarmOptions{Next: "run tests", Push: true})
	requestA.DisplayedAt = time.Unix(1, 0)
	specA := readyPlanSpec()

	requestB := mustRequest(t, locator, ParkWarmOptions{Next: "run tests", Push: true})
	requestB.DisplayedAt = time.Unix(9_999, 0)
	specB := readyPlanSpec()
	specB.DisplayedAt = time.Unix(8_888, 0)
	specB.Authority = map[string]string{"status": "clean", "runtime": "none"}

	planA := mustBuildPlan(t, requestA, specA)
	planB := mustBuildPlan(t, requestB, specB)
	if planA.PlanID != planB.PlanID {
		t.Fatalf("stable PlanIDs differ: %s != %s", planA.PlanID, planB.PlanID)
	}
	if planA.AuthorityFingerprint != planB.AuthorityFingerprint {
		t.Fatalf("stable authority fingerprints differ: %s != %s", planA.AuthorityFingerprint, planB.AuthorityFingerprint)
	}

	changedDisplay := planA.Clone()
	changedDisplay.DisplayedAt = time.Unix(123_456, 0)
	changedDisplay.Request.DisplayedAt = time.Unix(654_321, 0)
	if err := changedDisplay.Validate(); err != nil {
		t.Fatalf("display timestamps changed plan identity: %v", err)
	}
}

func TestPlanIDsChangeWithEveryAuthorityDimensionAndOrder(t *testing.T) {
	baseRequest := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{Next: "next"})
	baseSpec := readyPlanSpec()
	base := mustBuildPlan(t, baseRequest, baseSpec)

	tests := []struct {
		name    string
		request Request
		spec    PlanSpec
	}{
		{
			name: "task revision",
			request: func() Request {
				request := baseRequest.Clone()
				request.Locator.TaskRevision = "revision-2"
				return request
			}(),
			spec: readyPlanSpec(),
		},
		{
			name:    "options",
			request: mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{Next: "different"}),
			spec:    readyPlanSpec(),
		},
		{
			name:    "authority value",
			request: baseRequest,
			spec: func() PlanSpec {
				spec := readyPlanSpec()
				spec.Authority["status"] = "dirty"
				return spec
			}(),
		},
		{
			name:    "condition order",
			request: baseRequest,
			spec: func() PlanSpec {
				spec := readyPlanSpec()
				spec.Conditions[0], spec.Conditions[1] = spec.Conditions[1], spec.Conditions[0]
				return spec
			}(),
		},
		{
			name:    "effect order",
			request: baseRequest,
			spec: func() PlanSpec {
				spec := readyPlanSpec()
				spec.Effects[0], spec.Effects[1] = spec.Effects[1], spec.Effects[0]
				return spec
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := mustBuildPlan(t, test.request, test.spec)
			if plan.PlanID == base.PlanID {
				t.Errorf("PlanID did not change: %s", plan.PlanID)
			}
			if plan.AuthorityFingerprint == base.AuthorityFingerprint {
				t.Errorf("authority fingerprint did not change: %s", plan.AuthorityFingerprint)
			}
		})
	}
}

func TestPlanAndValueBoundariesDefensivelyCopyCollections(t *testing.T) {
	authority := map[string]string{"status": "clean"}
	conditions := []Condition{{
		Code: "checkout-clean", Verdict: VerdictMet, Requirement: RequirementRequired, Evidence: "clean",
	}}
	details := map[string]string{"state": "warm"}
	effects := []Effect{NewEffect("write-task", "write", "task", false, false, details)}
	retained := []string{"branch:feature"}
	spec := PlanSpec{
		Authority: authority, Conditions: conditions, Effects: effects, RetainedResources: retained,
	}
	plan := mustBuildPlan(t, mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{}), spec)

	authority["status"] = "dirty"
	conditions[0].Evidence = "changed"
	effects[0].Description = "changed"
	retained[0] = "changed"
	details["state"] = "cold"
	if got := plan.AuthorityFields()["status"]; got != "clean" {
		t.Errorf("authority changed through input map: %q", got)
	}
	if got := plan.Conditions()[0].Evidence; got != "clean" {
		t.Errorf("condition changed through input slice: %q", got)
	}
	if got := plan.Effects()[0].Description; got != "write" {
		t.Errorf("effect changed through input slice: %q", got)
	}
	if got := plan.Effects()[0].Details.Map()["state"]; got != "warm" {
		t.Errorf("effect details changed through input map: %q", got)
	}
	if got := plan.RetainedResources()[0]; got != "branch:feature" {
		t.Errorf("retained resource changed through input slice: %q", got)
	}

	gotAuthority := plan.AuthorityFields()
	gotAuthority["status"] = "mutated"
	gotConditions := plan.Conditions()
	gotConditions[0].Evidence = "mutated"
	gotEffects := plan.Effects()
	gotEffects[0].Description = "mutated"
	gotDetails := gotEffects[0].Details.Map()
	gotDetails["state"] = "mutated"
	gotRetained := plan.RetainedResources()
	gotRetained[0] = "mutated"
	if plan.AuthorityFields()["status"] != "clean" || plan.Conditions()[0].Evidence != "clean" ||
		plan.Effects()[0].Description != "write" || plan.Effects()[0].Details.Map()["state"] != "warm" ||
		plan.RetainedResources()[0] != "branch:feature" {
		t.Fatal("plan changed through an accessor result")
	}

	clone := plan.Clone()
	clone.authority.entries[0].Value = "clone"
	clone.conditions[0].Evidence = "clone"
	clone.effects[0].Details.entries[0].Value = "clone"
	clone.retained.values[0] = "clone"
	if plan.AuthorityFields()["status"] != "clean" || plan.Conditions()[0].Evidence != "clean" ||
		plan.Effects()[0].Details.Map()["state"] != "warm" || plan.RetainedResources()[0] != "branch:feature" {
		t.Fatal("Plan.Clone shares mutable backing storage")
	}
}

func TestObservationAndRequestCollectionsAreValueSafe(t *testing.T) {
	attributes := map[string]string{"probe": "git"}
	observation := NewObservation(ObservationKnown, "clean", "status clean", "", time.Now(), attributes)
	attributes["probe"] = "runtime"
	copyAttributes := observation.Attributes()
	copyAttributes["probe"] = "forge"
	if got := observation.Attributes()["probe"]; got != "git" {
		t.Fatalf("observation attributes = %q, want git", got)
	}

	tags := []string{"one", "two"}
	options := AdoptOptions{Mode: task.ModeWorktree, State: task.Warm, Tags: NewStringList(tags...)}
	request := mustRequest(t, Locator{RowKey: "external"}, &options)
	tags[0] = "changed"
	options.Tags.values[1] = "changed"
	adopted, ok := request.Options.(AdoptOptions)
	if !ok {
		t.Fatalf("request options type = %T, want AdoptOptions value", request.Options)
	}
	values := adopted.Tags.Values()
	values[0] = "returned-change"
	if got := request.Options.(AdoptOptions).Tags.Values(); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("request tags = %v, want independent original values", got)
	}
}

func TestRefreshRemoteOptionsSelectOperationsIndependently(t *testing.T) {
	locator := testLocator(task.ModeBranch, task.Warm)
	selections := []RefreshRemoteOptions{
		{FetchRefs: true},
		{QueryReview: true},
		{FetchRefs: true, QueryReview: true},
	}
	ids := make(map[string]bool)
	for _, selection := range selections {
		request := mustRequest(t, locator, selection)
		plan := mustBuildPlan(t, request, readyPlanSpec())
		if ids[plan.PlanID] {
			t.Errorf("remote selection %+v reused PlanID %s", selection, plan.PlanID)
		}
		ids[plan.PlanID] = true
	}
	if _, err := NewRequest(locator, RefreshRemoteOptions{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty refresh selection error = %v, want ErrInvalidRequest", err)
	}
}

func TestApprovalValidationAndTypedConfirmation(t *testing.T) {
	request := mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{})
	plain := mustBuildPlan(t, request, readyPlanSpec())
	if err := plain.ValidateApproval(Approve(plain.PlanID)); err != nil {
		t.Fatalf("plain approval: %v", err)
	}
	if err := plain.ValidateApproval(Approval{PlanID: plain.PlanID}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("ungranted approval error = %v, want ErrApprovalRequired", err)
	}

	wrongID := plain.ValidateApproval(Approve("different-plan"))
	if !errors.Is(wrongID, ErrStalePlan) {
		t.Fatalf("wrong PlanID error = %v, want ErrStalePlan", wrongID)
	}
	var stale *StalePlanError
	if !errors.As(wrongID, &stale) {
		t.Fatalf("wrong PlanID error type = %T, want *StalePlanError", wrongID)
	}

	typedSpec := readyPlanSpec()
	typedSpec.Confirmation = Confirmation{Kind: ConfirmationTyped, Prompt: "Type DROP", Token: "DROP"}
	typed := mustBuildPlan(t, request, typedSpec)
	if err := typed.ValidateApproval(Approve(typed.PlanID)); !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("missing token error = %v, want ErrInvalidApproval", err)
	}
	if err := typed.ValidateApproval(ApproveWithToken(typed.PlanID, "drop")); !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("wrong token error = %v, want ErrInvalidApproval", err)
	}
	if err := typed.ValidateApproval(ApproveWithToken(typed.PlanID, "DROP")); err != nil {
		t.Fatalf("typed approval: %v", err)
	}
}

func TestStaleAndInvalidPlanErrorsAreTyped(t *testing.T) {
	plan := mustBuildPlan(
		t,
		mustRequest(t, testLocator(task.ModeWorktree, task.Hot), ParkWarmOptions{}),
		readyPlanSpec(),
	)

	stalePlan := plan.Clone()
	stalePlan.authority.entries[0].Value = "changed"
	err := stalePlan.Validate()
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("stale validation error = %v, want ErrStalePlan", err)
	}
	var stale *StalePlanError
	if !errors.As(err, &stale) {
		t.Fatalf("stale validation type = %T, want *StalePlanError", err)
	}

	invalidPlan := plan.Clone()
	invalidPlan.Availability = AvailabilityBlocked
	err = invalidPlan.Validate()
	if !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid validation error = %v, want ErrInvalidPlan", err)
	}
	var invalid *InvalidPlanError
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid validation type = %T, want *InvalidPlanError", err)
	}
}
