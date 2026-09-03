package taskflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

type lifecycleFakeRuntime struct {
	mu sync.Mutex

	name       string
	available  bool
	sessions   []runtime.Session
	agents     []runtime.AgentActivity
	listErr    error
	agentErr   error
	openErr    error
	closeErr   error
	openResult runtime.OpenResult

	openCalls  int
	closeCalls int
}

func newLifecycleFakeRuntime() *lifecycleFakeRuntime {
	return &lifecycleFakeRuntime{
		name: "fake", available: true,
		openResult: runtime.OpenResult{Handle: "opened-runtime", Opened: true, Created: true},
	}
}

func (r *lifecycleFakeRuntime) Name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.name
}

func (r *lifecycleFakeRuntime) Available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available
}

func (r *lifecycleFakeRuntime) Open(_ context.Context, dir, label string) (runtime.OpenResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openCalls++
	if r.openErr != nil {
		return runtime.OpenResult{}, r.openErr
	}
	result := r.openResult
	if result.Handle == "" {
		result.Handle = "opened-runtime"
	}
	r.sessions = append(r.sessions, runtime.Session{
		Handle: result.Handle, Label: label, Dirs: []string{dir},
		Panes: []runtime.Pane{{ID: result.Handle + ":pane", CWD: dir}},
	})
	return result, nil
}

func (r *lifecycleFakeRuntime) Close(_ context.Context, handle string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCalls++
	if r.closeErr != nil {
		return r.closeErr
	}
	kept := r.sessions[:0]
	for _, session := range r.sessions {
		if session.Handle != handle {
			kept = append(kept, session)
		}
	}
	r.sessions = append([]runtime.Session(nil), kept...)
	return nil
}

func (r *lifecycleFakeRuntime) List(context.Context) ([]runtime.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return cloneRuntimeSessions(r.sessions), nil
}

func (r *lifecycleFakeRuntime) Annotate(context.Context, string, map[string]string) error { return nil }

func (r *lifecycleFakeRuntime) AgentActivities(context.Context) ([]runtime.AgentActivity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agentErr != nil {
		return nil, r.agentErr
	}
	return append([]runtime.AgentActivity(nil), r.agents...), nil
}

func (r *lifecycleFakeRuntime) setSessions(sessions ...runtime.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = cloneRuntimeSessions(sessions)
}

func (r *lifecycleFakeRuntime) setAgents(agents ...runtime.AgentActivity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append([]runtime.AgentActivity(nil), agents...)
}

func (r *lifecycleFakeRuntime) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openCalls, r.closeCalls
}

func cloneRuntimeSessions(input []runtime.Session) []runtime.Session {
	output := make([]runtime.Session, len(input))
	for index, session := range input {
		output[index] = session
		output[index].Dirs = append([]string(nil), session.Dirs...)
		output[index].Panes = append([]runtime.Pane(nil), session.Panes...)
		output[index].AgentSessions = append([]string(nil), session.AgentSessions...)
	}
	return output
}

func (f *lifecycleGitFixture) serviceWith(t *testing.T, rt runtime.Runtime, cwd string, hooks LifecycleHooks) *Service {
	t.Helper()
	if rt == nil {
		rt = runtime.None{}
	}
	if cwd == "" {
		cwd = f.root
	}
	service, err := NewLifecycleService(LifecycleConfig{
		Config: f.cfg, Tasks: f.tasks, Artifacts: f.artifacts,
		DefaultRuntime: func() runtime.Runtime { return rt },
		NamedRuntime: func(name string) runtime.Runtime {
			if name == rt.Name() || name == "" {
				return rt
			}
			return runtime.None{}
		},
		Host: "test-host", CWD: cwd,
		CallerWorkspaceID: "test-caller-workspace", CallerPaneID: "test-caller-pane",
		Clock: func() time.Time { return time.Unix(1_700_000_000, 0) },
		Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("NewLifecycleService: %v", err)
	}
	return service
}

func updateFixtureTask(t *testing.T, fixture *lifecycleGitFixture, mutate func(*task.Task)) task.Record {
	t.Helper()
	record, err := fixture.tasks.GetRecord(fixture.record.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := record.Task
	mutate(&candidate)
	updated, err := fixture.tasks.Update(context.Background(), &candidate, record.Revision)
	if err != nil {
		t.Fatalf("update fixture task: %v", err)
	}
	fixture.record = *updated
	return *updated
}

type lifecycleEffectCounter struct{ calls atomic.Int32 }

func (c *lifecycleEffectCounter) hooks() LifecycleHooks {
	return LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && (args[0] == "push" || args[0] == "fetch" || args[0] == "switch") {
				c.calls.Add(1)
			}
			return gitx.Run(ctx, dir, args...)
		},
		WIPCommit: func(ctx context.Context, dir, message string) (bool, error) {
			c.calls.Add(1)
			return gitx.WipCommit(ctx, dir, message)
		},
		RemoveWorktree: func(ctx context.Context, repo, path string, force bool) error {
			c.calls.Add(1)
			return gitx.RemoveWorktree(ctx, repo, path, force)
		},
		CloseAndWait: func(ctx context.Context, rt runtime.Runtime, path string, options retire.Options) (retire.Inspection, error) {
			c.calls.Add(1)
			return retire.CloseAndWait(ctx, rt, path, options)
		},
		OpenRuntime: func(ctx context.Context, rt runtime.Runtime, path, label string) (runtime.OpenResult, error) {
			c.calls.Add(1)
			return rt.Open(ctx, path, label)
		},
		CreateWorktree: func(context.Context, wt.CreateRequest) (*wt.CreateResult, error) {
			c.calls.Add(1)
			return nil, errors.New("unexpected worktree creation")
		},
		TaskUpdate: func(tx *task.Tx, candidate *task.Task, revision string) (*task.Record, error) {
			c.calls.Add(1)
			return tx.Update(candidate, revision)
		},
	}
}

func TestLifecyclePlanConditionsAndExactEffects(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		options      ActionOptions
		availability Availability
		effects      []EffectCode
		condition    ConditionCode
		verdict      Verdict
		requirement  Requirement
	}{
		{
			name: "warm dirty is advisory", options: ParkWarmOptions{},
			availability: AvailabilityReady, effects: []EffectCode{EffectUpdateTask},
			condition: ConditionCheckoutClean, verdict: VerdictMet, requirement: RequirementAdvisory,
		},
		{
			name: "cold dirty blocks without WIP", options: ParkColdOptions{},
			availability: AvailabilityBlocked, effects: []EffectCode{EffectRemoveWorktree, EffectUpdateTask},
			condition: ConditionCheckoutClean, verdict: VerdictBlocked, requirement: RequirementRequired,
		},
		{
			name: "cold WIP requires publication", options: ParkColdOptions{CommitWIP: true},
			availability: AvailabilityBlocked, effects: []EffectCode{EffectCommitWIP, EffectRemoveWorktree, EffectUpdateTask},
			condition: ConditionBranchPushed, verdict: VerdictBlocked, requirement: RequirementRequired,
		},
		{
			name: "cold WIP and push are ready", options: ParkColdOptions{CommitWIP: true, Push: true},
			availability: AvailabilityReady,
			effects:      []EffectCode{EffectCommitWIP, EffectPushBranch, EffectRemoveWorktree, EffectUpdateTask},
			condition:    ConditionArtifactReady, verdict: VerdictMet, requirement: RequirementRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := fixture.service.Plan(context.Background(), fixture.request(t, test.options))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != test.availability {
				t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
			}
			if got := effectCodes(plan); !reflect.DeepEqual(got, test.effects) {
				t.Fatalf("effects=%v want=%v", got, test.effects)
			}
			got, ok := conditionByCode(plan, test.condition)
			if !ok || got.Verdict != test.verdict || got.Requirement != test.requirement {
				t.Fatalf("condition %s = %+v present=%t", test.condition, got, ok)
			}
			for _, key := range []string{
				"task.revision", "git.branch", "git.upstream", "git.ahead", "git.behind",
				"git.conflicted", "git.base-oid", "git.upstream-oid", "worktree.fingerprint",
				"artifact.fingerprint", "runtime.cleanup-fingerprint", "caller.cwd", "option.commit-wip",
			} {
				if _, exists := plan.AuthorityFields()[key]; !exists {
					t.Errorf("authority is missing %s", key)
				}
			}
		})
	}
}

func conditionByCode(plan Plan, code ConditionCode) (Condition, bool) {
	for _, condition := range plan.Conditions() {
		if condition.Code == code {
			return condition, true
		}
	}
	return Condition{}, false
}

func TestLifecycleApplyRejectsEveryChangedAuthorityBeforeEffects(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, *lifecycleGitFixture, *lifecycleFakeRuntime)
		mutate func(*testing.T, *lifecycleGitFixture, *lifecycleFakeRuntime)
	}{
		{
			name: "task revision",
			mutate: func(t *testing.T, fixture *lifecycleGitFixture, _ *lifecycleFakeRuntime) {
				updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.Next = "changed after plan" })
			},
		},
		{
			name: "Git status",
			mutate: func(t *testing.T, fixture *lifecycleGitFixture, _ *lifecycleFakeRuntime) {
				if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Git HEAD",
			mutate: func(t *testing.T, fixture *lifecycleGitFixture, _ *lifecycleFakeRuntime) {
				if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustGitCommand(t, fixture.worktree, "add", "tracked.txt")
				mustGitCommand(t, fixture.worktree, "commit", "-m", "concurrent commit")
			},
		},
		{
			name: "worktree flags",
			mutate: func(t *testing.T, fixture *lifecycleGitFixture, _ *lifecycleFakeRuntime) {
				mustGitCommand(t, fixture.repo, "worktree", "lock", fixture.worktree)
				t.Cleanup(func() {
					_ = execGitForCleanup(fixture.repo, "worktree", "unlock", fixture.worktree)
				})
			},
		},
		{
			name: "artifact intent",
			mutate: func(t *testing.T, fixture *lifecycleGitFixture, _ *lifecycleFakeRuntime) {
				repository, _ := gitx.Discover(context.Background(), fixture.worktree)
				intent := artifact.Intent{
					ID: "intent-race", RunID: "run-race", Provider: "claude",
					SessionID: "1234567890abcdef", TaskID: fixture.record.Task.ID,
					RepoPath: fixture.repo, GitCommonDir: repository.GitCommonDir,
					WorktreePath: fixture.worktree, Branch: "feature", Base: "main",
					Head: strings.TrimSpace(mustGitCommand(t, fixture.worktree, "rev-parse", "HEAD")),
				}
				if err := fixture.artifacts.Create(context.Background(), &intent); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "runtime occupancy",
			setup: func(_ *testing.T, fixture *lifecycleGitFixture, fake *lifecycleFakeRuntime) {
				fake.setSessions()
			},
			mutate: func(_ *testing.T, fixture *lifecycleGitFixture, fake *lifecycleFakeRuntime) {
				fake.setSessions(idleRuntimeSession("race-runtime", fixture.worktree))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
			fake := newLifecycleFakeRuntime()
			if test.setup != nil {
				test.setup(t, fixture, fake)
			}
			counter := &lifecycleEffectCounter{}
			rt := runtime.Runtime(lifecycleObservedEmptyRuntime{})
			if test.name == "runtime occupancy" {
				rt = fake
			}
			service := fixture.serviceWith(t, rt, fixture.root, counter.hooks())
			plan, err := service.Plan(context.Background(), fixture.request(t, ParkColdOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != AvailabilityReady {
				t.Fatalf("plan availability=%s conditions=%+v", plan.Availability, plan.Conditions())
			}
			if counter.calls.Load() != 0 {
				t.Fatalf("planning performed %d effects", counter.calls.Load())
			}
			test.mutate(t, fixture, fake)
			result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if !errors.Is(err, ErrStalePlan) {
				t.Fatalf("Apply error=%v result=%+v", err, result.AttemptedSteps())
			}
			if counter.calls.Load() != 0 {
				t.Fatalf("stale apply performed %d effect(s): %+v", counter.calls.Load(), result.AttemptedSteps())
			}
			if len(result.AttemptedSteps()) != 0 {
				t.Fatalf("stale result has steps: %+v", result.AttemptedSteps())
			}
		})
	}
}

func execGitForCleanup(dir string, args ...string) error {
	_, err := gitx.Run(context.Background(), dir, args...)
	return err
}

func TestLifecycleApplyUsesFixedLockAndEffectOrder(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("ordered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := newLifecycleFakeRuntime()
	fake.setSessions(idleRuntimeSession("ordered-runtime", fixture.worktree))

	var (
		mu           sync.Mutex
		calls        []string
		taskLockHeld atomic.Bool
	)
	record := func(value string) {
		mu.Lock()
		calls = append(calls, value)
		mu.Unlock()
	}
	hooks := LifecycleHooks{
		RepoLock: func(_ context.Context, _ string, operation func() error) error {
			record("repo-lock")
			return operation()
		},
		WIPCommit: func(ctx context.Context, dir, message string) (bool, error) {
			record("wip")
			return gitx.WipCommit(ctx, dir, message)
		},
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "push" {
				record("push")
			}
			return gitx.Run(ctx, dir, args...)
		},
		CloseAndWait: func(ctx context.Context, rt runtime.Runtime, path string, options retire.Options) (retire.Inspection, error) {
			record("close")
			return retire.CloseAndWait(ctx, rt, path, options)
		},
		RemoveWorktree: func(ctx context.Context, repo, path string, force bool) error {
			record("remove")
			return gitx.RemoveWorktree(ctx, repo, path, force)
		},
		TaskUpdate: func(tx *task.Tx, candidate *task.Task, revision string) (*task.Record, error) {
			record("update")
			probeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			probeDone := make(chan error, 1)
			go func() {
				probeDone <- fixture.tasks.WithLock(probeCtx, func(*task.Tx) error { return nil })
			}()
			if probeErr := <-probeDone; probeErr != nil {
				taskLockHeld.Store(true)
			}
			return tx.Update(candidate, revision)
		},
	}
	service := fixture.serviceWith(t, fake, fixture.root, hooks)
	plan, err := service.Plan(context.Background(), fixture.request(t, ParkColdOptions{CommitWIP: true, Push: true}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	want := []string{"repo-lock", "wip", "push", "close", "remove", "update"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v want=%v", got, want)
	}
	if !taskLockHeld.Load() {
		t.Fatal("TaskUpdate did not run while task Store.WithLock was held")
	}
	if gotCodes := stepCodes(result); !reflect.DeepEqual(gotCodes,
		[]EffectCode{EffectCommitWIP, EffectPushBranch, EffectCloseRuntime, EffectRemoveWorktree, EffectUpdateTask}) {
		t.Fatalf("ledger=%v", gotCodes)
	}
}

func TestLifecycleWarmParkCallerContainedRetainsRuntime(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	fake := newLifecycleFakeRuntime()
	fake.setSessions(idleRuntimeSession("caller-runtime", fixture.worktree))
	updateFixtureTask(t, fixture, func(candidate *task.Task) {
		candidate.RuntimeName = "fake"
		candidate.RuntimeHandle = "caller-runtime"
	})
	service := fixture.serviceWith(t, fake, fixture.worktree, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), fixture.request(t, ParkWarmOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Warm || updated.RuntimeName != "fake" || updated.RuntimeHandle != "caller-runtime" {
		t.Fatalf("updated task=%+v", updated)
	}
	_, closes := fake.counts()
	if closes != 0 {
		t.Fatalf("caller runtime closed %d time(s)", closes)
	}
	if !containsMessage(result.Warnings(), "caller") {
		t.Fatalf("warnings=%v", result.Warnings())
	}
}

func TestLifecycleColdRemovalReinspectsAfterRuntimeClosure(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	fake := newLifecycleFakeRuntime()
	initial := idleRuntimeSession("initial-runtime", fixture.worktree)
	fake.setSessions(initial)
	var (
		closed     atomic.Bool
		postChecks atomic.Int32
		removes    atomic.Int32
	)
	hooks := LifecycleHooks{
		CloseAndWait: func(ctx context.Context, rt runtime.Runtime, path string, options retire.Options) (retire.Inspection, error) {
			inspection, err := retire.CloseAndWait(ctx, rt, path, options)
			closed.Store(true)
			return inspection, err
		},
		InspectCleanup: func(ctx context.Context, rt runtime.Runtime, path string, options retire.Options) (retire.Inspection, error) {
			if closed.Load() && postChecks.Add(1) == 2 {
				fake.setSessions(idleRuntimeSession("reappeared-runtime", fixture.worktree))
			}
			return retire.Inspect(ctx, rt, path, options)
		},
		RemoveWorktree: func(ctx context.Context, repo, path string, force bool) error {
			removes.Add(1)
			return gitx.RemoveWorktree(ctx, repo, path, force)
		},
	}
	service := fixture.serviceWith(t, fake, fixture.root, hooks)
	plan, err := service.Plan(context.Background(), fixture.request(t, ParkColdOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil {
		t.Fatal("Apply unexpectedly removed a reclaimed checkout")
	}
	if removes.Load() != 0 {
		t.Fatalf("remove called %d time(s)", removes.Load())
	}
	if got := stepCodes(result); !reflect.DeepEqual(got, []EffectCode{EffectCloseRuntime}) {
		t.Fatalf("ledger=%v steps=%+v", got, result.AttemptedSteps())
	}
	if !result.PartialSuccess || len(result.Recovery()) == 0 {
		t.Fatalf("partial=%t recovery=%v", result.PartialSuccess, result.Recovery())
	}
	if _, statErr := os.Stat(fixture.worktree); statErr != nil {
		t.Fatalf("worktree was not preserved: %v", statErr)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("task state=%s", updated.State)
	}
}

func TestLifecycleResumeOwnerTakeover(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
	updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.Owner = "other-host" })
	blocked, err := fixture.service.Plan(context.Background(), fixture.request(t, ResumeOptions{}))
	if err != nil {
		t.Fatalf("blocked Plan: %v", err)
	}
	if blocked.Availability != AvailabilityBlocked {
		t.Fatalf("without takeover availability=%s", blocked.Availability)
	}
	ready, err := fixture.service.Plan(context.Background(), fixture.request(t, ResumeOptions{TakeOwnership: true}))
	if err != nil {
		t.Fatalf("takeover Plan: %v", err)
	}
	if ready.Availability != AvailabilityReady {
		t.Fatalf("takeover availability=%s conditions=%+v", ready.Availability, ready.Conditions())
	}
	if _, err := fixture.service.Apply(context.Background(), ready, Approve(ready.PlanID)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot || updated.Owner != "test-host" {
		t.Fatalf("updated task=%+v", updated)
	}
}

func TestLifecycleResumeStrictOccupancyBlocksEveryAgentStatus(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "agent-runtime", Dirs: []string{fixture.worktree},
		Panes: []runtime.Pane{{ID: "agent-pane", CWD: fixture.worktree}},
	})
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	for _, status := range []string{"working", "idle", "done", ""} {
		name := status
		if name == "" {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			fake.setAgents(runtime.AgentActivity{
				PaneID: "agent-pane", WorkspaceID: "agent-runtime",
				Agent: "claude", Name: "worker", Status: status, CWD: fixture.worktree,
			})
			plan, err := service.Plan(context.Background(), fixture.request(t, ResumeOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != AvailabilityBlocked {
				t.Fatalf("status=%q availability=%s conditions=%+v", status, plan.Availability, plan.Conditions())
			}
			condition, ok := conditionByCode(plan, ConditionAgentOccupancy)
			if !ok || condition.Verdict != VerdictBlocked {
				t.Fatalf("agent condition=%+v present=%t", condition, ok)
			}
		})
	}
}

func TestLifecycleResumeRefProbeFailureIsNotMissingBranch(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	probeErr := errors.New("injected local branch probe failure")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		GitRefState: func(ctx context.Context, dir, ref string) (bool, error) {
			if ref == "refs/heads/feature" {
				return false, probeErr
			}
			return gitx.RefState(ctx, dir, ref)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, ResumeOptions{FetchRefs: true}))
	if err != nil {
		t.Fatal(err)
	}
	branch, _ := conditionByCode(plan, ConditionBranchRef)
	if plan.Availability != AvailabilityError || branch.Verdict != VerdictError || !strings.Contains(branch.Evidence, probeErr.Error()) {
		t.Fatalf("availability=%s branch=%+v conditions=%+v", plan.Availability, branch, plan.Conditions())
	}
}

func TestLifecycleResumeFailsClosedOnUnobservedWriterOccupancy(t *testing.T) {
	for _, test := range []struct {
		name    string
		runtime runtime.Runtime
		hooks   LifecycleHooks
	}{
		{name: "runtime none", runtime: runtime.None{}},
		{
			name: "agent inventory unattempted", runtime: newLifecycleFakeRuntime(),
			hooks: LifecycleHooks{InspectOccupancy: func(_ context.Context, _ runtime.Runtime, target string, _ runtime.OccupancyOptions) (runtime.Occupancy, error) {
				return runtime.Occupancy{
					Target: target, Backend: "fake",
					SessionList:       runtime.OccupancyObservation{Supported: true, Attempted: true},
					AgentActivityList: runtime.OccupancyObservation{Supported: true},
				}, nil
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
			service := fixture.serviceWith(t, test.runtime, fixture.root, test.hooks)
			plan, err := service.Plan(context.Background(), fixture.request(t, ResumeOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			condition, ok := conditionByCode(plan, ConditionAgentOccupancy)
			if plan.Availability != AvailabilityError || !ok || condition.Verdict != VerdictError {
				t.Fatalf("availability=%s occupancy=%+v conditions=%+v", plan.Availability, condition, plan.Conditions())
			}
		})
	}
}

func TestLifecycleResumeSavedRuntimeReuseAndStaleReplacement(t *testing.T) {
	tests := []struct {
		name        string
		live        bool
		wantOpen    int
		wantHandle  string
		wantEffects []EffectCode
	}{
		{name: "reuse live handle", live: true, wantHandle: "saved-runtime", wantEffects: []EffectCode{EffectUpdateTask}},
		{name: "replace stale handle", wantOpen: 1, wantHandle: "opened-runtime", wantEffects: []EffectCode{EffectOpenRuntime, EffectUpdateTask}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
			fake := newLifecycleFakeRuntime()
			if test.live {
				fake.setSessions(runtime.Session{
					Handle: "saved-runtime", Dirs: []string{fixture.worktree},
					Panes: []runtime.Pane{{ID: "saved-pane", CWD: fixture.worktree}},
				})
			}
			updateFixtureTask(t, fixture, func(candidate *task.Task) {
				candidate.RuntimeName = "fake"
				candidate.RuntimeHandle = "saved-runtime"
			})
			service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
			plan, err := service.Plan(context.Background(), fixture.request(t, ResumeOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got := effectCodes(plan); !reflect.DeepEqual(got, test.wantEffects) {
				t.Fatalf("effects=%v want=%v", got, test.wantEffects)
			}
			result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if err != nil {
				t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
			}
			updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
			if updated.RuntimeName != "fake" || updated.RuntimeHandle != test.wantHandle {
				t.Fatalf("runtime=%s/%s", updated.RuntimeName, updated.RuntimeHandle)
			}
			opens, _ := fake.counts()
			if opens != test.wantOpen {
				t.Fatalf("open calls=%d want=%d", opens, test.wantOpen)
			}
			handoff, ok := result.Handoff()
			if !ok || handoff.Kind != HandoffRuntime || handoff.RuntimeHandle != test.wantHandle {
				t.Fatalf("handoff=%+v present=%t", handoff, ok)
			}
		})
	}
}

func TestLifecycleResumeRuntimeOpenFailureReturnsPartialAfterRebuild(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	fake := newLifecycleFakeRuntime()
	fake.openErr = errors.New("runtime open failed")
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), fixture.request(t, ResumeOptions{NoProvision: true}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectCreateWorktree, EffectOpenRuntime, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || !strings.Contains(err.Error(), "runtime open failed") {
		t.Fatalf("Apply error=%v", err)
	}
	if !result.PartialSuccess || !reflect.DeepEqual(stepCodes(result), []EffectCode{EffectCreateWorktree, EffectOpenRuntime}) {
		t.Fatalf("partial=%t steps=%+v", result.PartialSuccess, result.AttemptedSteps())
	}
	steps := result.AttemptedSteps()
	if steps[0].Status != StepCompleted || steps[1].Status != StepFailed || len(result.Recovery()) == 0 {
		t.Fatalf("steps=%+v recovery=%v", steps, result.Recovery())
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Cold || updated.WorktreePath != "" {
		t.Fatalf("task mutated despite open failure: %+v", updated)
	}
	worktree, found, findErr := gitx.WorktreeFor(context.Background(), fixture.repo, "feature")
	if findErr != nil || !found || worktree.Path == "" {
		t.Fatalf("rebuilt worktree missing: %+v found=%t err=%v", worktree, found, findErr)
	}
}

func TestLifecycleFinalTaskCASFailureReturnsPartialLedger(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("checkpoint before CAS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	casErr := errors.New("injected final CAS failure")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskUpdate: func(*task.Tx, *task.Task, string) (*task.Record, error) { return nil, casErr },
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, ParkWarmOptions{CommitWIP: true}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, casErr) {
		t.Fatalf("Apply error=%v", err)
	}
	steps := result.AttemptedSteps()
	if len(steps) != 2 || steps[0].Effect.Code != EffectCommitWIP || steps[0].Status != StepCompleted ||
		steps[1].Effect.Code != EffectUpdateTask || steps[1].Status != StepFailed {
		t.Fatalf("steps=%+v", steps)
	}
	if !result.PartialSuccess || len(result.Recovery()) == 0 {
		t.Fatalf("partial=%t recovery=%v", result.PartialSuccess, result.Recovery())
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot {
		t.Fatalf("task state=%s", updated.State)
	}
	status, _ := gitx.StatusOf(context.Background(), fixture.worktree)
	if status.Dirty() || status.Ahead != 1 {
		t.Fatalf("checkpoint status=%+v", status)
	}
}

func TestLifecycleResumeFetchFailureWarnsAndStillHandsOff(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "fetch" {
				return "", errors.New("offline")
			}
			return gitx.Run(ctx, dir, args...)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, ResumeOptions{FetchRefs: true}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectFetchRefs, EffectOpenRuntime, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	steps := result.AttemptedSteps()
	if len(steps) != 3 || steps[0].Status != StepFailed || steps[1].Status != StepCompleted || steps[2].Status != StepCompleted ||
		!containsMessage(result.Warnings(), "fetch failed") {
		t.Fatalf("steps=%+v warnings=%v", steps, result.Warnings())
	}
	if handoff, ok := result.Handoff(); !ok || handoff.Kind != HandoffRuntime || handoff.RuntimeHandle != "test-runtime" {
		t.Fatalf("handoff=%+v present=%t", handoff, ok)
	}
}

func idleRuntimeSession(handle, checkout string) runtime.Session {
	return runtime.Session{
		Handle: handle, Dirs: []string{checkout}, AgentStatus: "idle",
		Panes: []runtime.Pane{{
			ID: handle + ":pane", CWD: checkout,
			Agent: "claude", AgentStatus: "idle", AgentSession: "claude:1234567890abcdef",
		}},
	}
}

func stepCodes(result Result) []EffectCode {
	steps := result.AttemptedSteps()
	codes := make([]EffectCode, len(steps))
	for index, step := range steps {
		codes[index] = step.Effect.Code
	}
	return codes
}

func containsMessage(messages []string, substring string) bool {
	for _, message := range messages {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}

func TestLifecyclePlanRequiresExactTaskIdentity(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	base := fixture.request(t, ParkWarmOptions{})
	tests := []struct {
		name   string
		mutate func(*Request)
		want   error
	}{
		{"missing TaskID", func(request *Request) { request.Locator.TaskID = "" }, ErrInvalidRequest},
		{"missing revision", func(request *Request) { request.Locator.TaskRevision = "" }, ErrInvalidRequest},
		{"different mode", func(request *Request) { request.Locator.Mode = task.ModeBranch }, ErrStalePlan},
		{"different state", func(request *Request) { request.Locator.State = task.Warm }, ErrStalePlan},
		{"different repo", func(request *Request) { request.Locator.RepoPath += "-other" }, ErrStalePlan},
		{"different branch", func(request *Request) { request.Locator.Branch = "other" }, ErrStalePlan},
		{"different base", func(request *Request) { request.Locator.Base = "other" }, ErrStalePlan},
		{"different checkout", func(request *Request) { request.Locator.CheckoutPath += "-other" }, ErrStalePlan},
		{"different common dir", func(request *Request) { request.Locator.GitCommonDir = fixture.root }, ErrStalePlan},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base.Clone()
			test.mutate(&request)
			_, err := fixture.service.Plan(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Plan error=%v want=%v", err, test.want)
			}
		})
	}
}
