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

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestCompletionDirtyPolicyPlanning(t *testing.T) {
	t.Run("clean ignores every content mutation policy", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
		for _, policy := range []DirtyPolicy{DirtyAuto, DirtyFail, DirtyCommit, DirtyDiscard} {
			t.Run(string(policy), func(t *testing.T) {
				plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteDirectOptions{
					Dirty: policy, CommitMessage: map[bool]string{true: "chore: unused"}[policy == DirtyCommit],
				}))
				if err != nil {
					t.Fatalf("Plan: %v", err)
				}
				if plan.Availability != AvailabilityReady {
					t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
				}
				if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectUpdateTask}) {
					t.Fatalf("effects=%v", got)
				}
				if plan.Confirmation.Kind != ConfirmationApproval {
					t.Fatalf("clean confirmation=%+v", plan.Confirmation)
				}
			})
		}
	})

	tests := []struct {
		name         string
		options      CompleteDirectOptions
		availability Availability
		effects      []EffectCode
		confirmation ConfirmationKind
	}{
		{name: "auto needs input", options: CompleteDirectOptions{Dirty: DirtyAuto}, availability: AvailabilityNeedsInput, effects: []EffectCode{}},
		{name: "fail blocks", options: CompleteDirectOptions{Dirty: DirtyFail}, availability: AvailabilityBlocked, effects: []EffectCode{}},
		{name: "commit needs message", options: CompleteDirectOptions{Dirty: DirtyCommit}, availability: AvailabilityNeedsInput, effects: []EffectCode{}},
		{name: "commit declares all", options: CompleteDirectOptions{Dirty: DirtyCommit, CommitMessage: "chore: finalize"}, availability: AvailabilityReady,
			effects: []EffectCode{EffectCommitAll, EffectUpdateTask}, confirmation: ConfirmationApproval},
		{name: "discard declares all", options: CompleteDirectOptions{Dirty: DirtyDiscard}, availability: AvailabilityReady,
			effects: []EffectCode{EffectDiscardAll, EffectUpdateTask}, confirmation: ConfirmationTyped},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
			if err := os.WriteFile(filepath.Join(fixture.repo, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := fixture.service.Plan(context.Background(), fixture.request(t, test.options))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != test.availability {
				t.Fatalf("availability=%s want=%s conditions=%+v", plan.Availability, test.availability, plan.Conditions())
			}
			if got := effectCodes(plan); !reflect.DeepEqual(got, test.effects) {
				t.Fatalf("effects=%v want=%v", got, test.effects)
			}
			if test.confirmation != "" && plan.Confirmation.Kind != test.confirmation {
				t.Fatalf("confirmation=%+v", plan.Confirmation)
			}
			if test.confirmation == ConfirmationTyped && plan.Confirmation.Token != "DROP" {
				t.Fatalf("typed confirmation=%+v", plan.Confirmation)
			}
			if test.availability != AvailabilityReady && len(plan.Effects()) != 0 {
				t.Fatalf("non-executable dirty plan has effects=%v", effectCodes(plan))
			}
		})
	}
}

func TestCompletionDiscardRequiresPlanBoundDROP(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
	path := filepath.Join(fixture.repo, "tracked.txt")
	if err := os.WriteFile(path, []byte("drop me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteDirectOptions{Dirty: DirtyDiscard}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, approval := range []Approval{Approve(plan.PlanID), ApproveWithToken(plan.PlanID, "drop")} {
		if _, err := fixture.service.Apply(context.Background(), plan, approval); !errors.Is(err, ErrInvalidApproval) {
			t.Fatalf("approval %+v error=%v", approval, err)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != "drop me\n" {
			t.Fatalf("wrong token changed bytes=%q err=%v", got, readErr)
		}
	}
	result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, "DROP"))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "main\n" {
		t.Fatalf("discarded bytes=%q err=%v", got, readErr)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Done || result.Milestone != MilestoneMerged {
		t.Fatalf("task=%+v milestone=%s", updated, result.Milestone)
	}
}

func TestCompletionStrictOccupancyAndCallerExclusion(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "completion-runtime", Dirs: []string{fixture.repo},
		Panes: []runtime.Pane{{ID: "other-pane", CWD: fixture.repo}},
	})
	fake.setAgents(runtime.AgentActivity{
		PaneID: "other-pane", WorkspaceID: "completion-runtime", Agent: "claude", Name: "other",
		Status: "done", CWD: fixture.repo,
	})
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	blocked, err := service.Plan(context.Background(), fixture.request(t, CompleteDirectOptions{}))
	if err != nil {
		t.Fatalf("blocked Plan: %v", err)
	}
	if blocked.Availability != AvailabilityBlocked {
		t.Fatalf("other done agent availability=%s conditions=%+v", blocked.Availability, blocked.Conditions())
	}
	occupancy, ok := conditionByCode(blocked, ConditionAgentOccupancy)
	if !ok || occupancy.Verdict != VerdictBlocked {
		t.Fatalf("occupancy=%+v present=%t", occupancy, ok)
	}

	fake.setSessions(runtime.Session{
		Handle: "test-caller-workspace", Dirs: []string{fixture.repo},
		Panes: []runtime.Pane{{ID: "test-caller-pane", CWD: fixture.repo}},
	})
	fake.setAgents(runtime.AgentActivity{
		PaneID: "test-caller-pane", WorkspaceID: "test-caller-workspace", Agent: "claude", Name: "caller",
		Status: "working", CWD: fixture.repo,
	})
	ready, err := service.Plan(context.Background(), fixture.request(t, CompleteDirectOptions{}))
	if err != nil {
		t.Fatalf("caller Plan: %v", err)
	}
	if ready.Availability != AvailabilityReady {
		t.Fatalf("exact caller availability=%s conditions=%+v", ready.Availability, ready.Conditions())
	}
}

func TestCompletionArtifactReadinessBlocks(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	repository, err := gitx.Discover(context.Background(), fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	intent := artifact.Intent{
		ID: "completion-pending", RunID: "completion-run", Provider: "claude",
		SessionID: "1234567890abcdef", TaskID: fixture.record.Task.ID,
		RepoPath: fixture.repo, GitCommonDir: repository.GitCommonDir,
		WorktreePath: fixture.worktree, Branch: "feature", Base: "main",
		Head: mustGitCommand(t, fixture.worktree, "rev-parse", "HEAD"), Status: artifact.Armed,
	}
	if err := fixture.artifacts.Create(context.Background(), &intent); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, CompleteFFOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityBlocked {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	condition, ok := conditionByCode(plan, ConditionArtifactReady)
	if !ok || condition.Verdict != VerdictBlocked {
		t.Fatalf("artifact condition=%+v present=%t", condition, ok)
	}
}

func TestCompletionFinishFingerprintStalenessPreventsEffects(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Hot)
	var updates int
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskUpdate: func(tx *task.Tx, candidate *task.Task, revision string) (*task.Record, error) {
			updates++
			return tx.Update(candidate, revision)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, CompleteDirectOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.AuthorityFields()["finish.fingerprint"] == "" {
		t.Fatal("plan authority has no finish fingerprint")
	}
	if err := os.WriteFile(filepath.Join(fixture.repo, "new.txt"), []byte("new writer bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Apply error=%v steps=%+v", err, result.AttemptedSteps())
	}
	if updates != 0 || len(result.AttemptedSteps()) != 0 {
		t.Fatalf("stale plan updates=%d steps=%+v", updates, result.AttemptedSteps())
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("task state=%s", updated.State)
	}
}

type completionFakeForge struct {
	mu        sync.Mutex
	available bool
	url       string
	err       error
	calls     int
	requests  []forge.PRRequest
}

func (f *completionFakeForge) Kind() forge.Kind { return forge.GitHub }
func (f *completionFakeForge) Bin() string      { return "fake-forge" }
func (f *completionFakeForge) Available() bool  { return f.available }
func (f *completionFakeForge) CreatePR(_ context.Context, _ string, request forge.PRRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.requests = append(f.requests, request)
	return f.url, f.err
}
func (f *completionFakeForge) CreateRepo(context.Context, string, forge.RepoRequest) (string, error) {
	return "", errors.New("unexpected CreateRepo")
}
func (f *completionFakeForge) CloneURL(value string) string { return value }
func (f *completionFakeForge) ListRepos(context.Context) ([]forge.RemoteRepo, error) {
	return nil, errors.New("unexpected ListRepos")
}

func (f *completionFakeForge) callState() (int, []forge.PRRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]forge.PRRequest(nil), f.requests...)
}

func assertResourcesRetained(t *testing.T, fixture *lifecycleGitFixture, checkout, branch string) {
	t.Helper()
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("checkout was removed: %v", err)
	}
	if !gitx.BranchExists(context.Background(), fixture.repo, branch) {
		t.Fatalf("branch %s was removed", branch)
	}
	updated, err := fixture.tasks.Get(fixture.record.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != task.Done {
		t.Fatalf("task state=%s", updated.State)
	}
	if updated.WorktreePath != fixture.record.Task.WorktreePath ||
		updated.RuntimeName != fixture.record.Task.RuntimeName || updated.RuntimeHandle != fixture.record.Task.RuntimeHandle {
		t.Fatalf("completion changed retained task resources: before=%+v after=%+v", fixture.record.Task, updated)
	}
}

func completionCommit(t *testing.T, dir, path, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCommand(t, dir, "add", path)
	mustGitCommand(t, dir, "commit", "-m", message)
	return strings.TrimSpace(mustGitCommand(t, dir, "rev-parse", "HEAD"))
}
