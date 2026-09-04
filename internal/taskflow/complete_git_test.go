package taskflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestCompletionRealGitDirectCommitPushAndHookOrder(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
	fakeRuntime := newLifecycleFakeRuntime()
	updateFixtureTask(t, fixture, func(candidate *task.Task) {
		candidate.RuntimeName = "fake"
		candidate.RuntimeHandle = "retained-runtime"
	})
	if err := os.WriteFile(filepath.Join(fixture.repo, "tracked.txt"), []byte("direct completion\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repo, "untracked.txt"), []byte("included\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		mu    sync.Mutex
		calls []string
	)
	record := func(value string) {
		mu.Lock()
		calls = append(calls, value)
		mu.Unlock()
	}
	service := fixture.serviceWith(t, fakeRuntime, fixture.root, LifecycleHooks{
		AnalyzeFinish: func(ctx context.Context, dir, base, branch string) (gitx.FinishAnalysis, error) {
			record("analyze")
			return gitx.AnalyzeFinish(ctx, dir, base, branch)
		},
		CommitAll: func(ctx context.Context, dir, message string) error {
			record("commit")
			return gitx.CommitAllChanges(ctx, dir, message)
		},
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "push" {
				record("push")
			}
			return gitx.Run(ctx, dir, args...)
		},
		TaskUpdate: func(tx *task.Tx, candidate *task.Task, revision string) (*task.Record, error) {
			record("update")
			return tx.Update(candidate, revision)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteDirectOptions{
		Dirty: DirtyCommit, CommitMessage: "feat: complete directly", Push: true,
	}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectCommitAll, EffectPushBranch, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	commitAt, pushAt, updateAt := indexOfString(gotCalls, "commit"), indexOfString(gotCalls, "push"), indexOfString(gotCalls, "update")
	if commitAt < 1 || pushAt <= commitAt || updateAt <= pushAt ||
		!sliceContains(gotCalls[:commitAt], "analyze") || !sliceContains(gotCalls[commitAt+1:pushAt], "analyze") ||
		!sliceContains(gotCalls[pushAt+1:updateAt], "analyze") {
		t.Fatalf("hook order=%v", gotCalls)
	}
	if result.Milestone != MilestoneMerged || result.PartialSuccess {
		t.Fatalf("milestone=%s partial=%t", result.Milestone, result.PartialSuccess)
	}
	if got := mustGitCommand(t, fixture.repo, "show", "HEAD:untracked.txt"); got != "included" {
		t.Fatalf("committed untracked content=%q", got)
	}
	if local, remote := mustGitCommand(t, fixture.repo, "rev-parse", "main"), mustGitCommand(t, fixture.repo, "rev-parse", "origin/main"); local != remote {
		t.Fatalf("direct push local=%s remote=%s", local, remote)
	}
	assertResourcesRetained(t, fixture, fixture.repo, "main")
	_, closes := fakeRuntime.counts()
	if closes != 0 {
		t.Fatalf("completion closed runtime %d time(s)", closes)
	}
}

func TestCompletionRealGitContainedFFWritesDoneOnlyAndRetainsResources(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	fakeRuntime := newLifecycleFakeRuntime()
	updateFixtureTask(t, fixture, func(candidate *task.Task) {
		candidate.RuntimeName = "fake"
		candidate.RuntimeHandle = "retained-worktree-runtime"
	})
	service := fixture.serviceWith(t, fakeRuntime, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectUpdateTask}) {
		t.Fatalf("contained effects=%v", got)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Milestone != MilestoneMerged {
		t.Fatalf("milestone=%s", result.Milestone)
	}
	assertResourcesRetained(t, fixture, fixture.worktree, "feature")
	_, closes := fakeRuntime.counts()
	if closes != 0 {
		t.Fatalf("completion closed runtime %d time(s)", closes)
	}
}

func TestCompletionRealGitFFWithoutRebasePushesBase(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{PushBase: true}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got,
		[]EffectCode{EffectSwitchBase, EffectMergeFF, EffectPushBase, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	branchOID := mustGitCommand(t, fixture.repo, "rev-parse", "feature")
	for _, ref := range []string{"main", "origin/main"} {
		if got := mustGitCommand(t, fixture.repo, "rev-parse", ref); got != branchOID {
			t.Fatalf("%s=%s want=%s", ref, got, branchOID)
		}
	}
	assertResourcesRetained(t, fixture, fixture.worktree, "feature")
}

func TestCompletionRealGitFFCanDiscardCanonicalTargetUnderTypedPlan(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	canonicalOnly := filepath.Join(fixture.repo, "canonical-only.txt")
	if err := os.WriteFile(canonicalOnly, []byte("drop\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	blocked, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Availability != AvailabilityBlocked {
		t.Fatalf("dirty canonical target availability=%s", blocked.Availability)
	}

	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetDiscard,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Availability != AvailabilityReady || plan.Confirmation.Kind != ConfirmationTyped || plan.Confirmation.Token != "DROP" {
		t.Fatalf("discard plan availability=%s confirmation=%+v conditions=%+v", plan.Availability, plan.Confirmation, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{
		EffectDiscardTarget, EffectSwitchBase, EffectMergeFF, EffectUpdateTask,
	}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, "DROP"))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if _, err := os.Stat(canonicalOnly); !os.IsNotExist(err) {
		t.Fatalf("canonical untracked path survived discard: %v", err)
	}
	if got := mustGitCommand(t, fixture.repo, "show", "main:feature.txt"); got != "feature" {
		t.Fatalf("integrated feature=%q", got)
	}
}

func TestCompletionRealGitFFStashesAndRestoresCanonicalTarget(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	completionCommit(t, fixture.repo, "main.txt", "main\n", "chore: advance main")
	statistics := filepath.Join(fixture.repo, ".specstory", "statistics.json")
	history := filepath.Join(fixture.repo, ".specstory", "history", "session.md")
	if err := os.MkdirAll(filepath.Dir(history), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statistics, []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCommand(t, fixture.repo, "add", ".specstory/statistics.json")
	if err := os.WriteFile(history, []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetStashRestore,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Availability != AvailabilityReady || plan.Confirmation.Kind != ConfirmationApproval {
		t.Fatalf("stash plan availability=%s confirmation=%+v conditions=%+v", plan.Availability, plan.Confirmation, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{
		EffectRebaseBranch, EffectStashTarget, EffectSwitchBase, EffectMergeFF, EffectRestoreTarget, EffectUpdateTask,
	}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	if got := mustGitCommand(t, fixture.repo, "show", "main:feature.txt"); got != "feature" {
		t.Fatalf("integrated feature=%q", got)
	}
	if staged := mustGitCommand(t, fixture.repo, "diff", "--cached", "--name-only"); staged != ".specstory/statistics.json" {
		t.Fatalf("restored index=%q", staged)
	}
	for _, path := range []string{statistics, history} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("restored %s: %v", path, statErr)
		}
	}
	if stash := mustGitCommand(t, fixture.repo, "stash", "list", "--format=%gs"); strings.Contains(stash, "dev-done-") {
		t.Fatalf("completed exact stash was retained: %s", stash)
	}
	record, err := fixture.tasks.GetRecord(fixture.record.Task.ID)
	if err != nil || record.Task.State != task.Done {
		t.Fatalf("task state=%v err=%v", record, err)
	}
}

func TestCompletionRealGitFFRestoreConflictRetainsExactStashAndHotTask(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "tracked.txt", "feature\n", "feat: change tracked")
	if err := os.WriteFile(filepath.Join(fixture.repo, "tracked.txt"), []byte("canonical\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetStashRestore,
	}))
	if err != nil || plan.Availability != AvailabilityReady {
		t.Fatalf("Plan availability=%s err=%v conditions=%+v", plan.Availability, err, plan.Conditions())
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !result.PartialSuccess || len(result.Recovery()) == 0 {
		t.Fatalf("Apply err=%v partial=%t recovery=%v steps=%+v", err, result.PartialSuccess, result.Recovery(), result.AttemptedSteps())
	}
	if !strings.Contains(strings.Join(result.Recovery(), "\n"), "stash apply --index") {
		t.Fatalf("recovery=%v", result.Recovery())
	}
	if stash := mustGitCommand(t, fixture.repo, "stash", "list", "--format=%H"); strings.TrimSpace(stash) == "" {
		t.Fatal("restore conflict dropped the exact stash")
	}
	status, statusErr := gitx.StatusOf(context.Background(), fixture.repo)
	if statusErr != nil || status.Conflicted == 0 {
		t.Fatalf("conflict status=%+v err=%v", status, statusErr)
	}
	record, loadErr := fixture.tasks.GetRecord(fixture.record.Task.ID)
	if loadErr != nil || record.Task.State != task.Hot {
		t.Fatalf("task=%v err=%v", record, loadErr)
	}
	retry, retryErr := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if retryErr != nil || retry.Availability != AvailabilityBlocked {
		t.Fatalf("conflicted retry availability=%s err=%v conditions=%+v", retry.Availability, retryErr, retry.Conditions())
	}
}

func TestCompletionRealGitFFRestoresCanonicalTargetWhenSwitchFails(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	canonical := filepath.Join(fixture.repo, "canonical.txt")
	if err := os.WriteFile(canonical, []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := fixture.serviceWith(t, newLifecycleFakeRuntime(), fixture.root, LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "switch" {
				return "", errors.New("injected switch failure")
			}
			return gitx.Run(ctx, dir, args...)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetStashRestore,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !strings.Contains(strings.Join(result.Warnings(), "\n"), "restored after integration stopped") {
		t.Fatalf("Apply err=%v warnings=%v recovery=%v", err, result.Warnings(), result.Recovery())
	}
	if data, readErr := os.ReadFile(canonical); readErr != nil || string(data) != "preserve\n" {
		t.Fatalf("canonical restore=%q err=%v", data, readErr)
	}
	if main, feature := mustGitCommand(t, fixture.repo, "rev-parse", "main"), mustGitCommand(t, fixture.repo, "rev-parse", "feature"); main == feature {
		t.Fatalf("main unexpectedly integrated feature: %s", main)
	}
}

func TestCompletionRealGitFFDetectsCanonicalWriteAfterRestore(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	if err := os.WriteFile(filepath.Join(fixture.repo, "canonical.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mutated := false
	service := fixture.serviceWith(t, newLifecycleFakeRuntime(), fixture.root, LifecycleHooks{
		IsAncestor: func(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
			contained, ancestorErr := gitIsAncestor(ctx, dir, ancestor, descendant)
			if !mutated && contained {
				mutated = true
				if writeErr := os.WriteFile(filepath.Join(fixture.repo, "concurrent.txt"), []byte("late\n"), 0o644); writeErr != nil {
					return false, writeErr
				}
			}
			return contained, ancestorErr
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetStashRestore,
	}))
	if err != nil {
		t.Fatal(err)
	}
	mutated = false
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Apply err=%v steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	record, loadErr := fixture.tasks.GetRecord(fixture.record.Task.ID)
	if loadErr != nil || record.Task.State != task.Hot {
		t.Fatalf("task=%v err=%v", record, loadErr)
	}
}

func TestCompletionRealGitFFBlocksStashForNestedRepository(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	nested := filepath.Join(fixture.repo, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitCommand(t, nested, "init")
	if err := os.WriteFile(filepath.Join(nested, "content.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetStashRestore,
	}))
	if err != nil {
		t.Fatal(err)
	}
	nestedEvidence := false
	for _, condition := range plan.Conditions() {
		if condition.Code == ConditionIntegrationTarget && strings.Contains(condition.Evidence, "nested repositories") {
			nestedEvidence = true
		}
	}
	if plan.Availability != AvailabilityBlocked || !nestedEvidence {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
}

func TestCompletionRealGitFFContinuesWhenRestoredStashCannotBeDropped(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	canonical := filepath.Join(fixture.repo, "canonical.txt")
	if err := os.WriteFile(canonical, []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := fixture.serviceWith(t, newLifecycleFakeRuntime(), fixture.root, LifecycleHooks{
		CaptureStash: func(ctx context.Context, dir, tag string) (string, error) {
			oid, captureErr := gitx.CaptureExactStash(ctx, dir, tag)
			if captureErr != nil {
				return "", captureErr
			}
			if _, dropErr := gitx.Run(ctx, dir, "stash", "drop", "stash@{0}"); dropErr != nil {
				return "", dropErr
			}
			return oid, nil
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{
		IntegrationTargetPolicy: IntegrationTargetStashRestore,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil || len(result.Warnings()) == 0 || !strings.Contains(strings.Join(result.Warnings(), "\n"), "no droppable selector") {
		t.Fatalf("Apply err=%v warnings=%v steps=%+v", err, result.Warnings(), result.AttemptedSteps())
	}
	if data, readErr := os.ReadFile(canonical); readErr != nil || string(data) != "preserve\n" {
		t.Fatalf("canonical restore=%q err=%v", data, readErr)
	}
	record, loadErr := fixture.tasks.GetRecord(fixture.record.Task.ID)
	if loadErr != nil || record.Task.State != task.Done {
		t.Fatalf("task=%v err=%v", record, loadErr)
	}
}

func TestCompletionRealGitFFRebasesOnlyWhenBaseHasCommits(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	oldFeature := completionCommit(t, fixture.worktree, "feature.txt", "feature\n", "feat: add feature")
	completionCommit(t, fixture.repo, "main.txt", "main\n", "chore: advance main")
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got,
		[]EffectCode{EffectRebaseBranch, EffectSwitchBase, EffectMergeFF, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	newFeature := mustGitCommand(t, fixture.repo, "rev-parse", "feature")
	if newFeature == oldFeature {
		t.Fatal("rebase did not rewrite the feature commit")
	}
	if main := mustGitCommand(t, fixture.repo, "rev-parse", "main"); main != newFeature {
		t.Fatalf("main=%s feature=%s", main, newFeature)
	}
	if got := mustGitCommand(t, fixture.worktree, "show", "HEAD:main.txt"); got != "main" {
		t.Fatalf("rebased feature lacks main change: %q", got)
	}
	assertResourcesRetained(t, fixture, fixture.worktree, "feature")
}

func TestCompletionRealGitBranchModeFFRebasesAndRetainsBranch(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeBranch, task.Hot)
	completionCommit(t, fixture.repo, "feature.txt", "feature\n", "feat: branch mode feature")
	mustGitCommand(t, fixture.repo, "switch", "main")
	completionCommit(t, fixture.repo, "main.txt", "main\n", "chore: branch mode main")
	mustGitCommand(t, fixture.repo, "switch", "feature")
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got,
		[]EffectCode{EffectRebaseBranch, EffectSwitchBase, EffectMergeFF, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	status, err := gitx.StatusOf(context.Background(), fixture.repo)
	if err != nil || status.Branch != "main" {
		t.Fatalf("canonical status=%+v err=%v", status, err)
	}
	if main, feature := mustGitCommand(t, fixture.repo, "rev-parse", "main"), mustGitCommand(t, fixture.repo, "rev-parse", "feature"); main != feature {
		t.Fatalf("main=%s feature=%s", main, feature)
	}
	assertResourcesRetained(t, fixture, fixture.repo, "feature")
}

func TestCompletionRealGitRebaseConflictReturnsPartialAndKeepsTaskActive(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "tracked.txt", "feature version\n", "feat: edit tracked")
	completionCommit(t, fixture.repo, "tracked.txt", "main version\n", "chore: edit tracked on main")
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !strings.Contains(err.Error(), "rebase") {
		t.Fatalf("Apply error=%v steps=%+v", err, result.AttemptedSteps())
	}
	steps := result.AttemptedSteps()
	if len(steps) != 1 || steps[0].Effect.Code != EffectRebaseBranch || steps[0].Status != StepFailed ||
		!result.PartialSuccess || len(result.Recovery()) == 0 || result.Milestone != MilestoneNone {
		t.Fatalf("result partial=%t milestone=%s steps=%+v recovery=%v",
			result.PartialSuccess, result.Milestone, steps, result.Recovery())
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("task state=%s", updated.State)
	}
	if _, err := os.Stat(fixture.worktree); err != nil || !gitx.BranchExists(context.Background(), fixture.repo, "feature") {
		t.Fatalf("resources changed: stat=%v branch=%t", err, gitx.BranchExists(context.Background(), fixture.repo, "feature"))
	}
	if operation, active, _ := gitx.InProgress(fixture.worktree); !active || !strings.HasPrefix(operation, "rebase") {
		t.Fatalf("rebase recovery evidence active=%t operation=%q", active, operation)
	}
}

func TestCompletionRejectsUnexpectedDirtyFinalizationRelation(t *testing.T) {
	tests := []struct {
		name    string
		options CompleteDirectOptions
		hooks   func(context.Context, string) error
	}{
		{
			name:    "commit creates extra commit",
			options: CompleteDirectOptions{Dirty: DirtyCommit, CommitMessage: "feat: expected commit"},
			hooks: func(ctx context.Context, dir string) error {
				if err := gitx.CommitAllChanges(ctx, dir, "feat: expected commit"); err != nil {
					return err
				}
				_, err := gitx.Run(ctx, dir, "commit", "--allow-empty", "-m", "injected extra commit")
				return err
			},
		},
		{
			name:    "discard changes branch",
			options: CompleteDirectOptions{Dirty: DirtyDiscard},
			hooks: func(ctx context.Context, dir string) error {
				return gitx.CommitAllChanges(ctx, dir, "injected instead of discard")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
			if err := os.WriteFile(filepath.Join(fixture.repo, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			hooks := LifecycleHooks{}
			if test.options.Dirty == DirtyCommit {
				hooks.CommitAll = func(ctx context.Context, dir, _ string) error { return test.hooks(ctx, dir) }
			} else {
				hooks.DiscardAll = test.hooks
			}
			service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, hooks)
			plan, err := service.Plan(context.Background(), fixture.request(t, test.options))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			approval := Approve(plan.PlanID)
			if test.options.Dirty == DirtyDiscard {
				approval = ApproveWithToken(plan.PlanID, "DROP")
			}
			result, err := service.Apply(context.Background(), plan, approval)
			if !errors.Is(err, ErrStalePlan) {
				t.Fatalf("Apply error=%v steps=%+v", err, result.AttemptedSteps())
			}
			if len(result.CompletedSteps()) != 1 || !result.PartialSuccess || result.Milestone != MilestoneNone {
				t.Fatalf("steps=%+v partial=%t milestone=%s", result.AttemptedSteps(), result.PartialSuccess, result.Milestone)
			}
			updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
			if updated.State != task.Hot {
				t.Fatalf("unexpected finalization wrote state=%s", updated.State)
			}
		})
	}
}

func TestCompletionRequiredReviewPushFailureKeepsTaskActive(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "review.txt", "review\n", "feat: review push failure")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "push" {
				return "", errors.New("required push failed")
			}
			return gitx.Run(ctx, dir, args...)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, ReviewHandoffOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !strings.Contains(err.Error(), "required push failed") {
		t.Fatalf("Apply error=%v", err)
	}
	steps := result.AttemptedSteps()
	if len(steps) != 1 || steps[0].Effect.Code != EffectPushBranch || steps[0].Status != StepFailed ||
		len(result.Recovery()) == 0 || result.Milestone != MilestoneNone {
		t.Fatalf("steps=%+v recovery=%v milestone=%s", steps, result.Recovery(), result.Milestone)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("push failure wrote state=%s", updated.State)
	}
}

func TestCompletionOptionalBasePushFailureWarnsAndStillWritesDone(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "push" {
				return "", errors.New("offline push")
			}
			return gitx.Run(ctx, dir, args...)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{PushBase: true}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	steps := result.AttemptedSteps()
	if len(steps) != 2 || steps[0].Effect.Code != EffectPushBase || steps[0].Status != StepFailed ||
		steps[1].Effect.Code != EffectUpdateTask || steps[1].Status != StepCompleted ||
		!containsMessage(result.Warnings(), "optional base push failed") || !result.PartialSuccess {
		t.Fatalf("steps=%+v warnings=%v partial=%t", steps, result.Warnings(), result.PartialSuccess)
	}
	assertResourcesRetained(t, fixture, fixture.worktree, "feature")
}

func TestCompletionReviewNoForgePublishesWithoutInventingURLOrDone(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "review.txt", "review\n", "feat: review handoff")
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ReviewHandoffOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectPushBranch}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Milestone != MilestoneReviewReady || len(result.Warnings()) == 0 || result.PartialSuccess {
		t.Fatalf("milestone=%s warnings=%v partial=%t", result.Milestone, result.Warnings(), result.PartialSuccess)
	}
	if handoff, ok := result.Handoff(); ok {
		t.Fatalf("unobserved URL handoff=%+v", handoff)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("review wrote state=%s", updated.State)
	}
	if local, remote := mustGitCommand(t, fixture.repo, "rev-parse", "feature"), mustGitCommand(t, fixture.repo, "rev-parse", "origin/feature"); local != remote {
		t.Fatalf("review publication local=%s remote=%s", local, remote)
	}
}

func TestCompletionReviewProviderFailureReturnsPartialAndPreservesWarmState(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
	completionCommit(t, fixture.worktree, "review.txt", "review\n", "feat: review handoff")
	provider := &completionFakeForge{available: true, err: errors.New("provider rejected request")}
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		DetectForge:  func(context.Context, string) forge.Kind { return forge.GitHub },
		ResolveForge: func(forge.Kind) (forge.Forge, error) { return provider, nil },
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, ReviewHandoffOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !strings.Contains(err.Error(), "provider rejected") {
		t.Fatalf("Apply error=%v", err)
	}
	steps := result.AttemptedSteps()
	if len(steps) != 2 || steps[0].Effect.Code != EffectPushBranch || steps[0].Status != StepCompleted ||
		steps[1].Effect.Code != EffectCreateReview || steps[1].Status != StepFailed || !result.PartialSuccess ||
		result.Milestone != MilestoneNone || len(result.Recovery()) == 0 {
		t.Fatalf("steps=%+v partial=%t milestone=%s recovery=%v", steps, result.PartialSuccess, result.Milestone, result.Recovery())
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Warm {
		t.Fatalf("review failure wrote state=%s", updated.State)
	}
}

func TestCompletionReviewRejectsOriginRepositoryChangeAfterPlan(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "review-remote.txt", "review\n", "feat: bind review remote")
	provider := &completionFakeForge{available: true, url: "https://github.com/acme/one/pull/1"}
	remoteURL := "https://github.com/acme/one.git"
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		GitRemote:    func(context.Context, string, string) string { return remoteURL },
		DetectForge:  func(context.Context, string) forge.Kind { return forge.GitHub },
		ResolveForge: func(forge.Kind) (forge.Forge, error) { return provider, nil },
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, ReviewHandoffOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	remoteURL = "https://github.com/acme/two.git"
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) || len(result.AttemptedSteps()) != 0 {
		t.Fatalf("changed origin Apply = %v steps=%+v", err, result.AttemptedSteps())
	}
	if calls, _ := provider.callState(); calls != 0 {
		t.Fatalf("changed origin created %d review(s)", calls)
	}
}

func TestCompletionReviewReturnsObservedProviderURL(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "review.txt", "review\n", "feat: review handoff")
	provider := &completionFakeForge{available: true, url: "https://example.test/review/42"}
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		DetectForge:  func(context.Context, string) forge.Kind { return forge.GitHub },
		ResolveForge: func(forge.Kind) (forge.Forge, error) { return provider, nil },
	})
	options := ReviewHandoffOptions{Draft: true, Title: "Review title", Body: "Review body"}
	plan, err := service.Plan(context.Background(), fixture.request(t, options))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	handoff, ok := result.Handoff()
	if !ok || handoff.Kind != HandoffURL || handoff.URL != provider.url || result.Milestone != MilestoneReviewReady {
		t.Fatalf("handoff=%+v present=%t milestone=%s", handoff, ok, result.Milestone)
	}
	calls, requests := provider.callState()
	if calls != 1 || len(requests) != 1 || requests[0].Base != "main" || requests[0].Head != "feature" ||
		!requests[0].Draft || requests[0].Title != options.Title || requests[0].Body != options.Body || requests[0].Fill {
		t.Fatalf("calls=%d requests=%+v", calls, requests)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("review URL wrote state=%s", updated.State)
	}
}

func TestCompletionVerifyMergedLocalAndExplicitRemoteRefs(t *testing.T) {
	for _, test := range []struct {
		name    string
		baseRef string
	}{
		{name: "recorded local base"},
		{name: "explicit remote tracking ref", baseRef: "origin/main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
			completionCommit(t, fixture.worktree, "merged.txt", "merged\n", "feat: merged work")
			mustGitCommand(t, fixture.repo, "merge", "--ff-only", "feature")
			mustGitCommand(t, fixture.repo, "push", "origin", "main")
			plan, err := fixture.service.Plan(context.Background(), fixture.request(t, VerifyMergedOptions{BaseRef: test.baseRef}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			fields := plan.AuthorityFields()
			wantRef := test.baseRef
			if wantRef == "" {
				wantRef = "main"
			}
			if fields["completion.base-ref"] != wantRef || fields["completion.base-oid"] == "" {
				t.Fatalf("base authority ref=%q oid=%q", fields["completion.base-ref"], fields["completion.base-oid"])
			}
			if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectVerifyAncestry, EffectUpdateTask}) {
				t.Fatalf("effects=%v", got)
			}
			result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Milestone != MilestoneMerged {
				t.Fatalf("milestone=%s", result.Milestone)
			}
			assertResourcesRetained(t, fixture, fixture.worktree, "feature")
		})
	}
}

func TestCompletionVerifyMergedAcceptsExactSquashAttestation(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "squashed.txt", "squashed\n", "feat: squash source")
	mustGitCommand(t, fixture.repo, "merge", "--squash", "feature")
	mustGitCommand(t, fixture.repo, "commit", "-m", "feat: squash result")
	squashOID := mustGitCommand(t, fixture.repo, "rev-parse", "HEAD")
	if _, err := gitx.Run(context.Background(), fixture.repo, "merge-base", "--is-ancestor", "feature", "main"); err == nil {
		t.Fatal("fixture branch unexpectedly became an ancestor of main")
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, VerifyMergedOptions{SquashCommit: squashOID}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady || plan.AuthorityFields()["completion.proof-oid"] != squashOID {
		t.Fatalf("availability=%s authority=%v conditions=%+v", plan.Availability, plan.AuthorityFields(), plan.Conditions())
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Milestone != MilestoneMerged {
		t.Fatalf("milestone=%s", result.Milestone)
	}
	assertResourcesRetained(t, fixture, fixture.worktree, "feature")
}

func TestCompletionVerifyMergedRejectsDirtyCommitAfterSquashAttestation(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "squashed.txt", "squashed\n", "feat: squash source")
	mustGitCommand(t, fixture.repo, "merge", "--squash", "feature")
	mustGitCommand(t, fixture.repo, "commit", "-m", "feat: squash result")
	squashOID := mustGitCommand(t, fixture.repo, "rev-parse", "HEAD")
	dirty := filepath.Join(fixture.worktree, "after-squash.txt")
	if err := os.WriteFile(dirty, []byte("not merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, VerifyMergedOptions{
		SquashCommit: squashOID, Dirty: DirtyCommit, CommitMessage: "feat: late work",
	}))
	if err != nil {
		t.Fatal(err)
	}
	proof, ok := conditionByCode(plan, ConditionMergeProof)
	if plan.Availability != AvailabilityBlocked || !ok || proof.Verdict != VerdictBlocked || len(plan.Effects()) != 0 {
		t.Fatalf("availability=%s proof=%+v effects=%+v", plan.Availability, proof, plan.Effects())
	}
	if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); !errors.Is(err, ErrPlanNotReady) {
		t.Fatalf("Apply error=%v, want ErrPlanNotReady", err)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("blocked squash plan changed dirty work: %v", err)
	}
	updated, err := fixture.tasks.Get(fixture.record.Task.ID)
	if err != nil || updated.State != task.Hot {
		t.Fatalf("blocked squash plan changed task=%+v err=%v", updated, err)
	}
}

func TestCompletionTaskCASFailureLeavesIntegratedResourcesAndRetryIsContained(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	completionCommit(t, fixture.worktree, "cas.txt", "cas\n", "feat: CAS fixture")
	casErr := errors.New("injected DONE CAS failure")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskUpdate: func(*task.Tx, *task.Task, string) (*task.Record, error) { return nil, casErr },
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, casErr) {
		t.Fatalf("Apply error=%v", err)
	}
	steps := result.AttemptedSteps()
	if len(steps) != 3 || steps[0].Effect.Code != EffectSwitchBase || steps[0].Status != StepCompleted ||
		steps[1].Effect.Code != EffectMergeFF || steps[1].Status != StepCompleted ||
		steps[2].Effect.Code != EffectUpdateTask || steps[2].Status != StepFailed ||
		!result.PartialSuccess || result.Milestone != MilestoneNone {
		t.Fatalf("steps=%+v partial=%t milestone=%s", steps, result.PartialSuccess, result.Milestone)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("CAS failure state=%s", updated.State)
	}
	if _, err := os.Stat(fixture.worktree); err != nil || !gitx.BranchExists(context.Background(), fixture.repo, "feature") {
		t.Fatalf("CAS failure removed resources: stat=%v", err)
	}

	retry, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("retry Plan: %v", err)
	}
	if got := effectCodes(retry); !reflect.DeepEqual(got, []EffectCode{EffectUpdateTask}) {
		t.Fatalf("retry effects=%v", got)
	}
	if _, err := fixture.service.Apply(context.Background(), retry, Approve(retry.PlanID)); err != nil {
		t.Fatalf("retry Apply: %v", err)
	}
	assertResourcesRetained(t, fixture, fixture.worktree, "feature")
}

func indexOfString(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

func sliceContains(values []string, want string) bool { return indexOfString(values, want) >= 0 }
