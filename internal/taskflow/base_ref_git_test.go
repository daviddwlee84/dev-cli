package taskflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (f *lifecycleGitFixture) implementationWith(t *testing.T, hooks LifecycleHooks) *lifecycleService {
	t.Helper()
	implementation, err := newLifecycleImplementation(LifecycleConfig{
		Config: f.cfg, Tasks: f.tasks, Artifacts: f.artifacts,
		DefaultRuntime: func() runtime.Runtime { return runtime.None{} },
		NamedRuntime:   func(string) runtime.Runtime { return runtime.None{} },
		Host:           "test-host", CWD: f.root,
		Clock: func() time.Time { return time.Unix(1_700_000_000, 0) },
		Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("newLifecycleImplementation: %v", err)
	}
	return implementation
}

func TestResolveBaseRefOrderAndFailClosed(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	service := fixture.implementationWith(t, LifecycleHooks{})
	mainOID := strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "main"))
	for _, tc := range []struct {
		input  string
		ref    string
		kind   BaseKind
		exists bool
	}{
		{input: "main", ref: "refs/heads/main", kind: BaseLocalBranch, exists: true},
		{input: "refs/heads/main", ref: "refs/heads/main", kind: BaseLocalBranch, exists: true},
		{input: "origin/main", ref: "refs/remotes/origin/main", kind: BaseRemoteTracking, exists: true},
		{input: "refs/remotes/origin/main", ref: "refs/remotes/origin/main", kind: BaseRemoteTracking, exists: true},
		{input: mainOID, ref: mainOID, kind: BaseCommit, exists: true},
		{input: mainOID[:7], ref: mainOID[:7], kind: BaseCommit, exists: true},
		{input: "refs/heads/origin/main"},
		{input: "missing-base"},
		{input: ""},
	} {
		got := service.resolveBaseRef(context.Background(), fixture.repo, tc.input)
		if got.err != nil || got.ref != tc.ref || got.kind != tc.kind || got.exists != tc.exists {
			t.Errorf("resolve %q = ref %q kind %q exists %t err %v", tc.input, got.ref, got.kind, got.exists, got.err)
		}
		if tc.exists && got.oid != mainOID {
			t.Errorf("resolve %q oid=%s want %s", tc.input, got.oid, mainOID)
		}
	}
	for _, input := range []string{"-main", " main", "main\x00"} {
		if got := service.resolveBaseRef(context.Background(), fixture.repo, input); got.err == nil || got.exists {
			t.Errorf("unnormalized base %q resolved: %+v", input, got)
		}
	}

	probe := errors.New("ref probe failed")
	var probed []string
	failing := fixture.implementationWith(t, LifecycleHooks{GitRefState: func(_ context.Context, _ string, ref string) (bool, error) {
		probed = append(probed, ref)
		return false, probe
	}})
	if got := failing.resolveBaseRef(context.Background(), fixture.repo, mainOID); !errors.Is(got.err, probe) || got.exists || len(probed) != 1 {
		t.Fatalf("probe failure fell through: %+v probed=%v", got, probed)
	}
}

// forkPointFixture records the task base as the commit the branch forked from,
// then fast-forwards main so the branch is integrated but not contained in it.
func forkPointFixture(t *testing.T) (*lifecycleGitFixture, string) {
	t.Helper()
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	forkPoint := strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "main"))
	if err := os.WriteFile(filepath.Join(fixture.worktree, "landed.txt"), []byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCommand(t, fixture.worktree, "add", "landed.txt")
	mustGitCommand(t, fixture.worktree, "commit", "-m", "feature landed")
	mustGitCommand(t, fixture.repo, "merge", "--ff-only", "feature")
	updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.Base = forkPoint[:7] })
	return fixture, forkPoint
}

func TestRetireRealGitCommitBaseIsResolvedAndRevalidated(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	mainOID := strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "main"))
	updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.Base = mainOID })
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
	if err != nil || plan.Availability != AvailabilityReady {
		t.Fatalf("Plan: availability=%s err=%v conditions=%+v", plan.Availability, err, plan.Conditions())
	}
	base, _ := conditionByCode(plan, ConditionExplicitBase)
	if !strings.Contains(base.Evidence, "resolves to commit "+mainOID) {
		t.Fatalf("base evidence=%q", base.Evidence)
	}
	result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
	if err != nil || result.Milestone != MilestoneRetired {
		t.Fatalf("Apply: milestone=%s err=%v steps=%+v", result.Milestone, err, result.AttemptedSteps())
	}
}

func TestRetireRealGitRemoteTrackingBase(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.Base = "origin/main" })
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil || plan.Availability != AvailabilityReady {
		t.Fatalf("Plan: availability=%s err=%v conditions=%+v", plan.Availability, err, plan.Conditions())
	}
	base, _ := conditionByCode(plan, ConditionExplicitBase)
	if !strings.Contains(base.Evidence, "remote-tracking branch refs/remotes/origin/main") {
		t.Fatalf("base evidence=%q", base.Evidence)
	}
	if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestRetireRealGitForkPointBaseRequiresExplicitBaseOverride(t *testing.T) {
	fixture, _ := forkPointFixture(t)
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	relation, _ := conditionByCode(plan, ConditionBranchRelation)
	if plan.Availability != AvailabilityBlocked || relation.Verdict != VerdictBlocked ||
		!strings.Contains(relation.Evidence, "recorded fork-point base") || !strings.Contains(relation.Remediation, "--base") {
		t.Fatalf("fork-point base was not blocked with a --base remediation: availability=%s relation=%+v", plan.Availability, relation)
	}
	options := RetireOptions{Base: "main"}
	plan, err = fixture.service.Plan(context.Background(), fixture.request(t, options))
	if err != nil || plan.Availability != AvailabilityReady {
		t.Fatalf("explicit base: availability=%s err=%v conditions=%+v", plan.Availability, err, plan.Conditions())
	}
	if !strings.Contains(plan.FallbackCommand, "--base main") {
		t.Fatalf("fallback lost the base override: %s", plan.FallbackCommand)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil || result.Milestone != MilestoneRetired {
		t.Fatalf("Apply: milestone=%s err=%v steps=%+v", result.Milestone, err, result.AttemptedSteps())
	}
}

func TestRetireRealGitBaseMovedAfterReviewIsStale(t *testing.T) {
	fixture, forkPoint := forkPointFixture(t)
	mustGitCommand(t, fixture.repo, "branch", "integration", "main")
	options := RetireOptions{Base: "integration"}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, options))
	if err != nil || plan.Availability != AvailabilityReady {
		t.Fatalf("Plan: availability=%s err=%v conditions=%+v", plan.Availability, err, plan.Conditions())
	}
	// The same input now names a different commit after the review.
	mustGitCommand(t, fixture.repo, "branch", "-f", "integration", forkPoint)
	if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("moved base after review error=%v", err)
	}
	if _, err := os.Stat(fixture.worktree); err != nil {
		t.Fatalf("stale retirement removed the checkout: %v", err)
	}
}

func TestRemoveCheckoutRealGitRemoteTrackingContainmentBase(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
	checkout := filepath.Join(fixture.root, "remote-contained")
	locator := addUnmanagedCheckout(t, fixture, "remote-contained", checkout, false)
	request, err := NewRequest(locator, RemoveCheckoutOptions{RequireContained: true, ContainmentBase: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil || plan.Availability != AvailabilityReady {
		t.Fatalf("Plan: availability=%s err=%v conditions=%+v", plan.Availability, err, plan.Conditions())
	}
	if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout stat=%v", err)
	}
}
