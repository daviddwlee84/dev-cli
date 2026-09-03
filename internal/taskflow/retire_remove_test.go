package taskflow

import (
	"context"
	"errors"
	"fmt"
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
)

func TestRetireApplyRejectsTaskStateAndRevisionRacesBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*task.Task)
	}{
		{name: "revision", mutate: func(candidate *task.Task) { candidate.Next = "changed after plan" }},
		{name: "state", mutate: func(candidate *task.Task) { candidate.State = task.Warm }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
			var effects atomic.Int32
			service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
				RemoveWorktree: func(context.Context, string, string, bool) error {
					effects.Add(1)
					return nil
				},
				TaskDelete: func(*task.Tx, string, string) error {
					effects.Add(1)
					return nil
				},
			})
			plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			updateFixtureTask(t, fixture, test.mutate)
			result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if !errors.Is(err, ErrStalePlan) {
				t.Fatalf("Apply error=%v steps=%+v", err, result.AttemptedSteps())
			}
			if effects.Load() != 0 || len(result.AttemptedSteps()) != 0 || result.Milestone != MilestoneNone {
				t.Fatalf("effects=%d steps=%+v milestone=%s", effects.Load(), result.AttemptedSteps(), result.Milestone)
			}
		})
	}
}

func TestRetireTaskDeleteFailureReportsPartialWithoutMilestone(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	deleteErr := errors.New("injected task delete failure")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskDelete: func(*task.Tx, string, string) error { return deleteErr },
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, deleteErr) {
		t.Fatalf("Apply error=%v", err)
	}
	steps := result.AttemptedSteps()
	if !result.PartialSuccess || result.Milestone != MilestoneNone || len(result.Recovery()) == 0 ||
		len(steps) != 2 || steps[0].Effect.Code != EffectRemoveWorktree || steps[0].Status != StepCompleted ||
		steps[1].Effect.Code != EffectDeleteTask || steps[1].Status != StepFailed {
		t.Fatalf("partial=%t milestone=%s steps=%+v recovery=%v", result.PartialSuccess, result.Milestone, steps, result.Recovery())
	}
	if _, err := fixture.tasks.Get(fixture.record.Task.ID); err != nil {
		t.Fatalf("failed CAS reap removed task: %v", err)
	}
	if _, err := os.Stat(fixture.worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree removal did not remain complete: %v", err)
	}
}

func TestRetireVerifiesTaskDeletionBeforeMilestone(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.State = task.Done })
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskDelete: func(*task.Tx, string, string) error { return nil },
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || result.Milestone != MilestoneNone || result.PartialSuccess {
		t.Fatalf("error=%v milestone=%s partial=%t steps=%+v", err, result.Milestone, result.PartialSuccess, result.AttemptedSteps())
	}
	if steps := result.AttemptedSteps(); len(steps) != 1 || steps[0].Status != StepFailed {
		t.Fatalf("steps=%+v", steps)
	}
}

func TestRetireRequiresExactLinkedCheckoutHead(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	request := fixture.request(t, RetireOptions{})
	request.Locator.HeadOID = ""
	if _, err := fixture.service.Plan(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Plan error=%v, want ErrInvalidRequest", err)
	}
}

func TestRetireHookOrderAndLocksCoverEveryDestructiveEffect(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	fake := newLifecycleFakeRuntime()
	fake.setSessions(idleRuntimeSession("retire-order", fixture.worktree))
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
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{
		RepoLock: func(_ context.Context, _ string, operation func() error) error {
			record("repo-lock")
			return operation()
		},
		CloseAndWait: func(ctx context.Context, rt runtime.Runtime, path string, options retire.Options) (retire.Inspection, error) {
			record("close")
			return retire.CloseAndWait(ctx, rt, path, options)
		},
		RemoveWorktree: func(ctx context.Context, repoPath, path string, force bool) error {
			record("remove")
			probeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			probeErr := fixture.tasks.WithLock(probeCtx, func(*task.Tx) error { return nil })
			taskLockHeld.Store(errors.Is(probeErr, context.DeadlineExceeded))
			return gitx.RemoveWorktree(ctx, repoPath, path, force)
		},
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 1 && args[0] == "branch" && args[1] == "-d" {
				record("delete-branch")
			}
			return gitx.Run(ctx, dir, args...)
		},
		TaskDelete: func(tx *task.Tx, id, revision string) error {
			record("delete-task")
			return tx.DeleteIfRevision(id, revision)
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	result, err := service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	want := []string{"repo-lock", "close", "remove", "delete-branch", "delete-task"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hook order=%v want=%v", got, want)
	}
	if !taskLockHeld.Load() {
		t.Fatal("worktree removal did not run while the task-store lock was held")
	}
	if result.Milestone != MilestoneRetired {
		t.Fatalf("milestone=%s", result.Milestone)
	}
}

func TestRetireDoneNoCheckoutAcceptsMissingBranchButRuntimeHintBlocks(t *testing.T) {
	t.Run("missing branch", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
		updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.State = task.Done })
		mustGitCommand(t, fixture.repo, "branch", "-D", "feature")
		plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityReady || !reflect.DeepEqual(effectCodes(plan), []EffectCode{EffectDeleteTask}) {
			t.Fatalf("availability=%s effects=%v conditions=%+v", plan.Availability, effectCodes(plan), plan.Conditions())
		}
		result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
		if err != nil || result.Milestone != MilestoneRetired {
			t.Fatalf("Apply error=%v milestone=%s steps=%+v", err, result.Milestone, result.AttemptedSteps())
		}
	})

	t.Run("ref probe error", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
		updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.State = task.Done })
		probeErr := errors.New("injected ref probe failure")
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			GitRefState: func(ctx context.Context, dir, ref string) (bool, error) {
				if ref == "refs/heads/feature" {
					return false, probeErr
				}
				return gitx.RefState(ctx, dir, ref)
			},
		})
		plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		branch, _ := conditionByCode(plan, ConditionBranchRef)
		if plan.Availability != AvailabilityError || branch.Verdict != VerdictError || !strings.Contains(branch.Evidence, probeErr.Error()) {
			t.Fatalf("availability=%s branch=%+v conditions=%+v", plan.Availability, branch, plan.Conditions())
		}
	})

	t.Run("runtime hint", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
		updateFixtureTask(t, fixture, func(candidate *task.Task) {
			candidate.State = task.Done
			candidate.RuntimeName = "fake"
			candidate.RuntimeHandle = "lost-runtime"
		})
		fake := newLifecycleFakeRuntime()
		service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
		plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
	})
}

func TestRetireBranchDeletionAndDirectDeletionPolicy(t *testing.T) {
	t.Run("branch deletion after canonical moved to base", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeBranch, task.Done)
		mustGitCommand(t, fixture.repo, "switch", "main")
		plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityReady {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
		if err != nil {
			t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
		}
		if result.Milestone != MilestoneRetired || gitx.BranchExists(context.Background(), fixture.repo, "feature") {
			t.Fatalf("milestone=%s branch-exists=%t", result.Milestone, gitx.BranchExists(context.Background(), fixture.repo, "feature"))
		}
	})

	t.Run("direct never deletes", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		deletion, ok := conditionByCode(plan, ConditionBranchDeletion)
		if !ok || deletion.Verdict != VerdictBlocked || containsEffect(plan, EffectDeleteBranch) {
			t.Fatalf("condition=%+v present=%t effects=%v", deletion, ok, effectCodes(plan))
		}
	})
}

func TestRetireBlocksConflictingManagedTaskClaim(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	conflict := &task.Task{
		Repo: "example", RepoPath: fixture.repo, Branch: "other-task", Base: "main",
		WorktreePath: fixture.worktree, Mode: task.ModeWorktree, State: task.Warm,
	}
	if _, err := fixture.tasks.Create(context.Background(), conflict); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	claims, ok := conditionByCode(plan, ConditionTaskClaims)
	if !ok || claims.Verdict != VerdictBlocked || plan.Availability != AvailabilityBlocked {
		t.Fatalf("conflicting managed claim was not blocked: availability=%s claims=%+v", plan.Availability, claims)
	}
}

func TestRetireBranchAndDirectModesRequireArtifactReadiness(t *testing.T) {
	for _, mode := range []task.CheckoutMode{task.ModeBranch, task.ModeDirect} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, mode, task.Done)
			createPendingArtifact(t, fixture, fixture.repo, fixture.record.Task.Branch, "pending-"+string(mode))
			plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			artifactCondition, ok := conditionByCode(plan, ConditionArtifactReady)
			if !ok || artifactCondition.Verdict != VerdictBlocked || plan.Availability != AvailabilityBlocked {
				t.Fatalf("%s artifact readiness = %+v availability=%s", mode, artifactCondition, plan.Availability)
			}
		})
	}
}

func TestRemoveCheckoutBackendBlocksCanonicalHarnessClaimsAndIncompleteInventory(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		repository, _ := gitx.Discover(context.Background(), fixture.repo)
		head := strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "HEAD"))
		request, _ := NewRequest(Locator{
			RepoKey: repository.GitCommonDir, GitCommonDir: repository.GitCommonDir,
			RepoPath: repository.MainRoot, CheckoutPath: repository.MainRoot,
			Branch: "main", HeadOID: head,
		}, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		linked, _ := conditionByCode(plan, ConditionCheckoutLinked)
		if plan.Availability == AvailabilityReady || linked.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s linked=%+v conditions=%+v", plan.Availability, linked, plan.Conditions())
		}
	})

	t.Run("harness", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		harness := filepath.Join(fixture.repo, ".claude", "worktrees", "harness-remove")
		if err := os.MkdirAll(filepath.Dir(harness), 0o755); err != nil {
			t.Fatal(err)
		}
		locator := addUnmanagedCheckout(t, fixture, "harness-remove", harness, false)
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		harnessCondition, _ := conditionByCode(plan, ConditionHarnessOwnership)
		if plan.Availability != AvailabilityBlocked || harnessCondition.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s harness=%+v conditions=%+v", plan.Availability, harnessCondition, plan.Conditions())
		}
	})

	for _, claimKind := range []string{"branch", "path"} {
		t.Run("task claimed by "+claimKind, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
			checkout := filepath.Join(fixture.root, "claimed-"+claimKind)
			locator := addUnmanagedCheckout(t, fixture, "claimed-"+claimKind, checkout, false)
			claimed := &task.Task{
				Repo: "example", RepoPath: fixture.repo, Base: "main", State: task.Done,
				Mode: task.ModeBranch, Branch: "claimed-" + claimKind,
			}
			if claimKind == "path" {
				claimed.Branch = "different-branch"
				claimed.Mode = task.ModeWorktree
				claimed.WorktreePath = checkout
			}
			if _, err := fixture.tasks.Create(context.Background(), claimed); err != nil {
				t.Fatal(err)
			}
			request, _ := NewRequest(locator, RemoveCheckoutOptions{})
			plan, err := fixture.service.Plan(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			claims, _ := conditionByCode(plan, ConditionTaskClaims)
			if plan.Availability != AvailabilityBlocked || claims.Verdict != VerdictBlocked {
				t.Fatalf("availability=%s claims=%+v conditions=%+v", plan.Availability, claims, plan.Conditions())
			}
		})
	}

	t.Run("ambiguous worktree", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "ambiguous")
		locator := addUnmanagedCheckout(t, fixture, "ambiguous", checkout, false)
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			ResolveWorktree: func(context.Context, string, string) (gitx.RegisteredWorktree, error) {
				return gitx.RegisteredWorktree{}, fmt.Errorf("%w: duplicate exact path", gitx.ErrWorktreeAmbiguous)
			},
		})
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityError {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
	})

	t.Run("corrupt inventory", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "incomplete")
		locator := addUnmanagedCheckout(t, fixture, "incomplete", checkout, false)
		if err := os.WriteFile(filepath.Join(fixture.tasks.Dir, "corrupt.toml"), []byte("not = = toml"), 0o644); err != nil {
			t.Fatal(err)
		}
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		inventoryCondition, _ := conditionByCode(plan, ConditionTaskInventory)
		if plan.Availability != AvailabilityError || inventoryCondition.Verdict != VerdictError ||
			!strings.Contains(inventoryCondition.Evidence, "corrupt") {
			t.Fatalf("availability=%s inventory=%+v", plan.Availability, inventoryCondition)
		}
	})
}

func TestRemoveCheckoutBlocksDetachedLockedPrunableInProgressAndArtifact(t *testing.T) {
	t.Run("detached", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "detached")
		locator := addUnmanagedCheckout(t, fixture, "detached-selected", checkout, true)
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		branch, _ := conditionByCode(plan, ConditionCheckoutBranch)
		if plan.Availability != AvailabilityBlocked || branch.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s branch=%+v conditions=%+v", plan.Availability, branch, plan.Conditions())
		}
	})

	t.Run("locked", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "locked")
		locator := addUnmanagedCheckout(t, fixture, "locked", checkout, false)
		mustGitCommand(t, fixture.repo, "worktree", "lock", "--reason", "test", checkout)
		t.Cleanup(func() { _ = execGitForCleanup(fixture.repo, "worktree", "unlock", checkout) })
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		flags, _ := conditionByCode(plan, ConditionCheckoutUnlocked)
		if plan.Availability != AvailabilityBlocked || flags.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s flags=%+v", plan.Availability, flags)
		}
	})

	t.Run("prunable", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "prunable")
		locator := addUnmanagedCheckout(t, fixture, "prunable", checkout, false)
		if err := os.RemoveAll(checkout); err != nil {
			t.Fatal(err)
		}
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		flags, _ := conditionByCode(plan, ConditionCheckoutUnlocked)
		if plan.Availability == AvailabilityReady || flags.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s flags=%+v conditions=%+v", plan.Availability, flags, plan.Conditions())
		}
	})

	t.Run("Git operation", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "operation")
		locator := addUnmanagedCheckout(t, fixture, "operation", checkout, false)
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			GitInProgress: func(context.Context, string) (string, bool, error) {
				return "rebase", true, nil
			},
		})
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		operation, _ := conditionByCode(plan, ConditionGitOperation)
		if plan.Availability != AvailabilityBlocked || operation.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s operation=%+v", plan.Availability, operation)
		}
	})

	t.Run("artifact pending", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "artifact")
		locator := addUnmanagedCheckout(t, fixture, "artifact", checkout, false)
		createPendingArtifact(t, fixture, checkout, "artifact", "artifact-block")
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		artifactCondition, _ := conditionByCode(plan, ConditionArtifactReady)
		if plan.Availability != AvailabilityBlocked || artifactCondition.Verdict != VerdictBlocked {
			t.Fatalf("availability=%s artifact=%+v", plan.Availability, artifactCondition)
		}
	})
}

func TestRemoveCheckoutRuntimeRetirementPolicy(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		mixed      bool
		caller     bool
		listErr    bool
		options    RemoveCheckoutOptions
		want       Availability
		apply      bool
		wantCloses int
	}{
		{name: "caller", status: "idle", caller: true, want: AvailabilityBlocked},
		{name: "mixed", status: "idle", mixed: true, want: AvailabilityBlocked},
		{name: "active", status: "working", want: AvailabilityBlocked},
		{name: "unrecognized", status: "sleeping", want: AvailabilityBlocked},
		{name: "idle", status: "idle", want: AvailabilityReady, apply: true, wantCloses: 1},
		{name: "done", status: "done", want: AvailabilityReady, apply: true, wantCloses: 1},
		{name: "unknown default", status: "", want: AvailabilityBlocked},
		{name: "unknown acknowledged", status: "", options: RemoveCheckoutOptions{CloseUnknown: true}, want: AvailabilityReady, apply: true, wantCloses: 1},
		{name: "list error", listErr: true, want: AvailabilityError},
		{name: "list error acknowledged", listErr: true, options: RemoveCheckoutOptions{AssumeNoRuntime: true}, want: AvailabilityReady, apply: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
			checkout := filepath.Join(fixture.root, "runtime-"+strings.ReplaceAll(test.name, " ", "-"))
			locator := addUnmanagedCheckout(t, fixture, "runtime-"+strings.ReplaceAll(test.name, " ", "-"), checkout, false)
			fake := newLifecycleFakeRuntime()
			if test.listErr {
				fake.listErr = errors.New("runtime list failed")
			} else {
				panes := []runtime.Pane{{
					ID: "runtime-pane", CWD: checkout, Agent: "claude", AgentStatus: test.status,
					AgentSession: "claude:1234567890abcdef",
				}}
				if test.mixed {
					panes = append(panes, runtime.Pane{ID: "outside-pane", CWD: fixture.root})
				}
				fake.setSessions(runtime.Session{
					Handle: "runtime-session", Dirs: []string{checkout}, Panes: panes, AgentStatus: test.status,
				})
			}
			cwd := fixture.root
			if test.caller {
				cwd = checkout
			}
			service := fixture.serviceWith(t, fake, cwd, LifecycleHooks{})
			request, _ := NewRequest(locator, test.options)
			plan, err := service.Plan(context.Background(), request)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != test.want {
				t.Fatalf("availability=%s want=%s conditions=%+v", plan.Availability, test.want, plan.Conditions())
			}
			if !test.apply {
				return
			}
			result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if err != nil {
				t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
			}
			_, closes := fake.counts()
			if closes != test.wantCloses {
				t.Fatalf("close calls=%d want=%d", closes, test.wantCloses)
			}
		})
	}
}

func TestRemoveCheckoutRuntimeNoneRequiresExplicitAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name    string
		options RemoveCheckoutOptions
		want    Availability
	}{
		{name: "default blocks", want: AvailabilityBlocked},
		{name: "acknowledged", options: RemoveCheckoutOptions{AssumeNoRuntime: true}, want: AvailabilityReady},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
			checkout := filepath.Join(fixture.root, "runtime-none-"+strings.ReplaceAll(test.name, " ", "-"))
			locator := addUnmanagedCheckout(t, fixture, "runtime-none-"+strings.ReplaceAll(test.name, " ", "-"), checkout, false)
			service := fixture.serviceWith(t, runtime.None{}, fixture.root, LifecycleHooks{})
			request, _ := NewRequest(locator, test.options)
			plan, err := service.Plan(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Availability != test.want {
				t.Fatalf("availability=%s want=%s conditions=%+v", plan.Availability, test.want, plan.Conditions())
			}
		})
	}
}

func TestDestructivePostCloseIdentityAndArtifactRacesBlockRemoval(t *testing.T) {
	for _, race := range []string{"identity", "artifact"} {
		t.Run(race, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
			fake := newLifecycleFakeRuntime()
			fake.setSessions(idleRuntimeSession("post-close", fixture.worktree))
			var removes atomic.Int32
			service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{
				CloseAndWait: func(ctx context.Context, rt runtime.Runtime, path string, options retire.Options) (retire.Inspection, error) {
					inspection, err := retire.CloseAndWait(ctx, rt, path, options)
					if err != nil {
						return inspection, err
					}
					switch race {
					case "identity":
						if _, lockErr := gitx.Run(ctx, fixture.repo, "worktree", "lock", "--reason", "post-close-race", fixture.worktree); lockErr != nil {
							t.Fatalf("lock worktree: %v", lockErr)
							t.Cleanup(func() { _ = execGitForCleanup(fixture.repo, "worktree", "unlock", fixture.worktree) })
						}
					case "artifact":
						createPendingArtifact(t, fixture, fixture.worktree, "feature", "post-close-artifact")
					}
					return inspection, nil
				},
				RemoveWorktree: func(context.Context, string, string, bool) error {
					removes.Add(1)
					return nil
				},
			})
			plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if err == nil {
				t.Fatal("post-close race unexpectedly applied")
			}
			if removes.Load() != 0 || result.Milestone != MilestoneNone ||
				!reflect.DeepEqual(stepCodes(result), []EffectCode{EffectCloseRuntime}) || !result.PartialSuccess {
				t.Fatalf("removes=%d milestone=%s partial=%t steps=%+v error=%v", removes.Load(), result.Milestone, result.PartialSuccess, result.AttemptedSteps(), err)
			}
		})
	}
}

func TestRemoveCheckoutHoldsRepositoryThenTaskStoreLockThroughRemoval(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
	checkout := filepath.Join(fixture.root, "unmanaged-locks")
	locator := addUnmanagedCheckout(t, fixture, "unmanaged-locks", checkout, false)
	var (
		mu           sync.Mutex
		calls        []string
		taskLockHeld atomic.Bool
	)
	recordCall := func(value string) {
		mu.Lock()
		calls = append(calls, value)
		mu.Unlock()
	}
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		RepoLock: func(_ context.Context, _ string, operation func() error) error {
			recordCall("repo-lock")
			return operation()
		},
		RemoveWorktree: func(ctx context.Context, repoPath, path string, force bool) error {
			recordCall("remove")
			probeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			claim := &task.Task{Repo: "example", RepoPath: fixture.repo, Branch: "unmanaged-locks", Base: "main", Mode: task.ModeWorktree, State: task.Done, WorktreePath: checkout}
			_, probeErr := fixture.tasks.Create(probeCtx, claim)
			taskLockHeld.Store(errors.Is(probeErr, context.DeadlineExceeded))
			return gitx.RemoveWorktree(ctx, repoPath, path, force)
		},
	})
	request, _ := NewRequest(locator, RemoveCheckoutOptions{})
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []string{"repo-lock", "remove"}) || !taskLockHeld.Load() {
		t.Fatalf("calls=%v task-lock-held=%t", got, taskLockHeld.Load())
	}
}

func TestRemoveCheckoutHoldsTaskStoreLockThroughContainedBranchDeletion(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
	checkout := filepath.Join(fixture.root, "contained-delete-lock")
	locator := addUnmanagedCheckout(t, fixture, "contained-delete-lock", checkout, false)
	var taskLockHeld atomic.Bool
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 1 && args[0] == "branch" && args[1] == "-d" {
				probeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer cancel()
				claim := &task.Task{
					Repo: "example", RepoPath: fixture.repo, Branch: locator.Branch, Base: "main",
					Mode: task.ModeWorktree, State: task.Done, WorktreePath: checkout,
				}
				_, probeErr := fixture.tasks.Create(probeCtx, claim)
				taskLockHeld.Store(errors.Is(probeErr, context.DeadlineExceeded))
			}
			return gitx.Run(ctx, dir, args...)
		},
	})
	request, err := NewRequest(locator, RemoveCheckoutOptions{
		RequireContained: true, ContainmentBase: "main", DeleteContainedBranch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if !taskLockHeld.Load() {
		t.Fatal("task store lock was not held through contained branch deletion")
	}
	if got := stepCodes(result); !reflect.DeepEqual(got, []EffectCode{EffectRemoveWorktree, EffectDeleteBranch}) {
		t.Fatalf("steps=%v", got)
	}
}

func TestRemoveCheckoutTaskClaimRaceAndPartialRemovalOutcomes(t *testing.T) {
	t.Run("task claim appears before apply", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "claim-race")
		locator := addUnmanagedCheckout(t, fixture, "claim-race", checkout, false)
		var removes atomic.Int32
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			RemoveWorktree: func(context.Context, string, string, bool) error {
				removes.Add(1)
				return nil
			},
		})
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		claim := &task.Task{Repo: "example", RepoPath: fixture.repo, Branch: "claim-race", Base: "main", Mode: task.ModeWorktree, State: task.Done, WorktreePath: checkout}
		if _, err := fixture.tasks.Create(context.Background(), claim); err != nil {
			t.Fatal(err)
		}
		result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
		if !errors.Is(err, ErrStalePlan) || removes.Load() != 0 || len(result.AttemptedSteps()) != 0 {
			t.Fatalf("error=%v removes=%d steps=%+v", err, removes.Load(), result.AttemptedSteps())
		}
	})

	t.Run("remove reports error after deleting checkout", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "partial-remove")
		locator := addUnmanagedCheckout(t, fixture, "partial-remove", checkout, false)
		removeErr := errors.New("injected post-remove failure")
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			RemoveWorktree: func(ctx context.Context, repoPath, path string, force bool) error {
				if err := gitx.RemoveWorktree(ctx, repoPath, path, force); err != nil {
					return err
				}
				return removeErr
			},
		})
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, _ := service.Plan(context.Background(), request)
		result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
		if !errors.Is(err, removeErr) || !result.PartialSuccess || result.Milestone != MilestoneNone || len(result.Recovery()) == 0 {
			t.Fatalf("error=%v partial=%t milestone=%s recovery=%v steps=%+v", err, result.PartialSuccess, result.Milestone, result.Recovery(), result.AttemptedSteps())
		}
		if steps := result.AttemptedSteps(); len(steps) != 1 || steps[0].Status != StepFailed {
			t.Fatalf("steps=%+v", steps)
		}
	})

	t.Run("branch changes after successful removal", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "branch-race")
		locator := addUnmanagedCheckout(t, fixture, "branch-race", checkout, false)
		if err := os.WriteFile(filepath.Join(checkout, "tracked.txt"), []byte("new commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGitCommand(t, checkout, "add", "tracked.txt")
		mustGitCommand(t, checkout, "commit", "-m", "branch commit")
		locator.HeadOID = strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD"))
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			RemoveWorktree: func(ctx context.Context, repoPath, path string, force bool) error {
				if err := gitx.RemoveWorktree(ctx, repoPath, path, force); err != nil {
					return err
				}
				_, err := gitx.Run(ctx, repoPath, "update-ref", "refs/heads/branch-race", "refs/heads/main")
				return err
			},
		})
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, _ := service.Plan(context.Background(), request)
		result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
		if err == nil || !result.PartialSuccess || result.Milestone != MilestoneNone || len(result.CompletedSteps()) != 1 {
			t.Fatalf("error=%v partial=%t milestone=%s steps=%+v", err, result.PartialSuccess, result.Milestone, result.AttemptedSteps())
		}
	})
}

func TestRetireCloseFailureCarriesLedgerAndNeverRemovesOrRetires(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	fake := newLifecycleFakeRuntime()
	fake.setSessions(idleRuntimeSession("close-failure", fixture.worktree))
	closeErr := errors.New("runtime close failed")
	fake.closeErr = closeErr
	var removes atomic.Int32
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{
		RemoveWorktree: func(context.Context, string, string, bool) error {
			removes.Add(1)
			return nil
		},
	})
	plan, err := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, closeErr) || removes.Load() != 0 || !result.PartialSuccess ||
		result.Milestone != MilestoneNone || len(result.Recovery()) == 0 {
		t.Fatalf("error=%v removes=%d partial=%t milestone=%s recovery=%v", err, removes.Load(), result.PartialSuccess, result.Milestone, result.Recovery())
	}
	if steps := result.AttemptedSteps(); len(steps) != 1 || steps[0].Effect.Code != EffectCloseRuntime || steps[0].Status != StepFailed {
		t.Fatalf("steps=%+v", steps)
	}
}

func TestRetireRemoveAndBranchDeleteFailuresNeverReportRetired(t *testing.T) {
	t.Run("worktree remove failure", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
		removeErr := errors.New("remove failed")
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			RemoveWorktree: func(context.Context, string, string, bool) error { return removeErr },
		})
		plan, _ := service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
		result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
		if !errors.Is(err, removeErr) || !result.PartialSuccess || result.Milestone != MilestoneNone {
			t.Fatalf("error=%v partial=%t milestone=%s steps=%+v", err, result.PartialSuccess, result.Milestone, result.AttemptedSteps())
		}
	})

	t.Run("branch delete failure", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
		deleteErr := errors.New("branch delete failed")
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
				if len(args) > 1 && args[0] == "branch" && args[1] == "-d" {
					return "", deleteErr
				}
				return gitx.Run(ctx, dir, args...)
			},
		})
		plan, _ := service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
		result, err := service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
		if !errors.Is(err, deleteErr) || !result.PartialSuccess || result.Milestone != MilestoneNone {
			t.Fatalf("error=%v partial=%t milestone=%s steps=%+v", err, result.PartialSuccess, result.Milestone, result.AttemptedSteps())
		}
		if _, err := fixture.tasks.Get(fixture.record.Task.ID); err != nil {
			t.Fatalf("branch delete failure reaped task: %v", err)
		}
	})
}

func createPendingArtifact(t *testing.T, fixture *lifecycleGitFixture, checkout, branch, id string) {
	t.Helper()
	repository, err := gitx.Discover(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	intent := artifact.Intent{
		ID: id, RunID: "run-" + id, Provider: "claude", SessionID: "1234567890abcdef",
		RepoPath: fixture.repo, GitCommonDir: repository.GitCommonDir,
		WorktreePath: checkout, Branch: branch, Base: "main",
		Head: strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD")),
	}
	if err := fixture.artifacts.Create(context.Background(), &intent); err != nil {
		t.Fatal(err)
	}
}

func containsEffect(plan Plan, code EffectCode) bool {
	for _, effect := range plan.Effects() {
		if effect.Code == code {
			return true
		}
	}
	return false
}
