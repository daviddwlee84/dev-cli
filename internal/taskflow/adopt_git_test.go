package taskflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

func newAdoptGitFixture(t *testing.T, branch string) (*lifecycleGitFixture, Locator, string) {
	t.Helper()
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
	checkout := filepath.Join(fixture.root, "adopt-"+strings.ReplaceAll(branch, "/", "-"))
	locator := addUnmanagedCheckout(t, fixture, branch, checkout, false)
	return fixture, locator, checkout
}

func newAdoptRequest(t *testing.T, locator Locator, options AdoptOptions) Request {
	t.Helper()
	request, err := NewRequest(locator, options)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return request
}

func requireAdoptCondition(t *testing.T, plan Plan, code ConditionCode, verdict Verdict, requirement Requirement) Condition {
	t.Helper()
	condition, ok := conditionByCode(plan, code)
	if !ok {
		t.Fatalf("condition %s is missing from %+v", code, plan.Conditions())
	}
	if condition.Verdict != verdict || condition.Requirement != requirement {
		t.Fatalf("condition %s=%+v, want verdict=%s requirement=%s", code, condition, verdict, requirement)
	}
	return condition
}

func adoptEffectDetails(t *testing.T, plan Plan) map[string]string {
	t.Helper()
	effects := plan.Effects()
	if len(effects) != 1 || effects[0].Code != EffectCreateTask {
		t.Fatalf("effects=%v, want only %s", effectCodes(plan), EffectCreateTask)
	}
	if effects[0].Destructive || effects[0].Network {
		t.Fatalf("create-task effect unexpectedly destructive/network: %+v", effects[0])
	}
	return effects[0].Details.Map()
}

func assertTaskMissing(t *testing.T, store *task.Store, id string) {
	t.Helper()
	if _, err := store.GetRecord(id); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("task %s lookup error=%v, want ErrNotFound", id, err)
	}
}

func TestAdoptRealGitDirtyCheckoutCreatesWarmMetadataOnly(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "dirty-adopt")
	dirtyPath := filepath.Join(checkout, "tracked.txt")
	if err := os.WriteFile(dirtyPath, []byte("dirty bytes stay here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeHead := strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD"))
	options := AdoptOptions{
		Mode: task.ModeWorktree, State: task.Warm,
		Name: "existing dirty work", Owner: "must-not-override-host",
		Next: "continue safely", Note: "imported metadata",
		Tags: NewStringList("existing", "important"),
	}
	plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, options))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady || plan.Confirmation.Kind != ConfirmationApproval ||
		plan.ExpectedMilestone != MilestoneAdopted {
		t.Fatalf("availability=%s confirmation=%+v expected-milestone=%s conditions=%+v",
			plan.Availability, plan.Confirmation, plan.ExpectedMilestone, plan.Conditions())
	}
	details := adoptEffectDetails(t, plan)
	if details["state"] != string(task.Warm) || details["mode"] != string(task.ModeWorktree) ||
		details["base"] != "main" || details["runtime"] != "" || details["runtime-handle"] != "" {
		t.Fatalf("create-task details=%v", details)
	}
	clean := requireAdoptCondition(t, plan, ConditionCheckoutClean, VerdictMet, RequirementAdvisory)
	if !strings.Contains(clean.Evidence, "dirty bytes are allowed") {
		t.Fatalf("dirty advisory evidence=%q", clean.Evidence)
	}

	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if result.Milestone != MilestoneAdopted || result.PartialSuccess ||
		!reflect.DeepEqual(stepCodes(result), []EffectCode{EffectCreateTask}) {
		t.Fatalf("milestone=%s partial=%t steps=%+v", result.Milestone, result.PartialSuccess, result.AttemptedSteps())
	}
	id := task.MakeID("example", locator.Branch)
	record, err := fixture.tasks.GetRecord(id)
	if err != nil {
		t.Fatalf("created task: %v", err)
	}
	created := record.Task
	canonicalRepo, canonicalRepoErr := pathx.Canonical(fixture.repo)
	canonicalCheckout, canonicalCheckoutErr := pathx.Canonical(checkout)
	if canonicalRepoErr != nil || canonicalCheckoutErr != nil {
		t.Fatalf("canonical paths repo=%v checkout=%v", canonicalRepoErr, canonicalCheckoutErr)
	}
	if created.ID != id || created.Name != options.Name || created.Repo != "example" ||
		created.RepoPath != canonicalRepo || created.Branch != locator.Branch || created.Base != "main" ||
		created.WorktreePath != canonicalCheckout || created.Mode != task.ModeWorktree || created.State != task.Warm ||
		created.Owner != "test-host" || created.Next != options.Next || created.Note != options.Note ||
		!reflect.DeepEqual(created.Tags, []string{"existing", "important"}) ||
		created.RuntimeName != "" || created.RuntimeHandle != "" {
		t.Fatalf("created task=%+v", created)
	}
	if snapshot, ok := result.FreshSnapshotRef(); !ok || snapshot != "task:"+record.Revision {
		t.Fatalf("snapshot=%q present=%t revision=%s", snapshot, ok, record.Revision)
	}
	if got, readErr := os.ReadFile(dirtyPath); readErr != nil || string(got) != "dirty bytes stay here\n" {
		t.Fatalf("dirty bytes=%q err=%v", got, readErr)
	}
	status, statusErr := gitx.StatusOf(context.Background(), checkout)
	if statusErr != nil || !status.Dirty() || status.Branch != locator.Branch {
		t.Fatalf("post-adopt status=%+v err=%v", status, statusErr)
	}
	if afterHead := strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s", beforeHead, afterHead)
	}
}

func TestAdoptRuntimeNoneIsExplicitlyUnobservedWarm(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "runtime-none")
	service := fixture.serviceWith(t, runtime.None{}, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady || adoptEffectDetails(t, plan)["state"] != string(task.Warm) {
		t.Fatalf("availability=%s effects=%+v conditions=%+v", plan.Availability, plan.Effects(), plan.Conditions())
	}
	occupancy := requireAdoptCondition(t, plan, ConditionAgentOccupancy, VerdictUnknown, RequirementAdvisory)
	if !strings.Contains(occupancy.Evidence, "unobserved") || !strings.Contains(occupancy.Evidence, "WARM") {
		t.Fatalf("runtime-none advisory=%q", occupancy.Evidence)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Milestone != MilestoneAdopted || len(result.Warnings()) != 1 ||
		!strings.Contains(result.Warnings()[0], "unobserved") {
		t.Fatalf("milestone=%s warnings=%v", result.Milestone, result.Warnings())
	}
}

func TestAdoptOneFreshShellSessionCreatesHotWithVerifiedHandle(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "shell-hot")
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "stable-shell", Label: "existing shell", Dirs: []string{checkout},
		Panes: []runtime.Pane{{ID: "shell-pane", CWD: checkout, ShellCWD: checkout}},
	})
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{State: task.Hot}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	details := adoptEffectDetails(t, plan)
	if plan.Availability != AvailabilityReady || details["state"] != string(task.Hot) ||
		details["runtime"] != "fake" || details["runtime-handle"] != "stable-shell" {
		t.Fatalf("availability=%s details=%v conditions=%+v", plan.Availability, details, plan.Conditions())
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	created, err := fixture.tasks.Get(task.MakeID("example", locator.Branch))
	if err != nil {
		t.Fatal(err)
	}
	if created.State != task.Hot || created.RuntimeName != "fake" || created.RuntimeHandle != "stable-shell" {
		t.Fatalf("created task=%+v", created)
	}
	opens, closes := fake.counts()
	if opens != 0 || closes != 0 || result.Milestone != MilestoneAdopted {
		t.Fatalf("runtime opens=%d closes=%d milestone=%s", opens, closes, result.Milestone)
	}
}

func TestAdoptDerivesWarmWithoutExactlyOneStableShellSession(t *testing.T) {
	tests := []struct {
		name     string
		sessions func(string) []runtime.Session
	}{
		{name: "zero sessions", sessions: func(string) []runtime.Session { return nil }},
		{name: "two sessions", sessions: func(path string) []runtime.Session {
			return []runtime.Session{
				{Handle: "one", Dirs: []string{path}, Panes: []runtime.Pane{{ID: "one-pane", CWD: path}}},
				{Handle: "two", Dirs: []string{path}, Panes: []runtime.Pane{{ID: "two-pane", CWD: path}}},
			}
		}},
		{name: "empty handle", sessions: func(path string) []runtime.Session {
			return []runtime.Session{{Dirs: []string{path}, Panes: []runtime.Pane{{ID: "pane", CWD: path}}}}
		}},
		{name: "non-shell pane metadata", sessions: func(path string) []runtime.Session {
			return []runtime.Session{{
				Handle: "agentish", Dirs: []string{path}, AgentStatus: "idle",
				Panes: []runtime.Pane{{ID: "pane", CWD: path, AgentStatus: "idle"}},
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			branch := "warm-" + strings.ReplaceAll(test.name, " ", "-")
			fixture, locator, checkout := newAdoptGitFixture(t, branch)
			fake := newLifecycleFakeRuntime()
			fake.setSessions(test.sessions(checkout)...)
			service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
			plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Availability != AvailabilityReady || adoptEffectDetails(t, plan)["state"] != string(task.Warm) {
				t.Fatalf("availability=%s effects=%+v conditions=%+v", plan.Availability, plan.Effects(), plan.Conditions())
			}
		})
	}
}

func TestAdoptStrictOccupancyBlocksEveryOtherRecognizedAgentState(t *testing.T) {
	for _, status := range []string{"working", "waiting", "idle", "done", ""} {
		name := status
		if name == "" {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			fixture, locator, checkout := newAdoptGitFixture(t, "agent-"+name)
			fake := newLifecycleFakeRuntime()
			fake.setSessions(runtime.Session{
				Handle: "occupied", Dirs: []string{checkout},
				Panes: []runtime.Pane{{ID: "other-pane", CWD: checkout}},
			})
			fake.setAgents(runtime.AgentActivity{
				PaneID: "other-pane", WorkspaceID: "occupied", Agent: "claude", Name: "worker",
				Status: status, CWD: checkout,
			})
			service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
			plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != AvailabilityBlocked {
				t.Fatalf("status=%q availability=%s conditions=%+v", status, plan.Availability, plan.Conditions())
			}
			requireAdoptCondition(t, plan, ConditionAgentOccupancy, VerdictBlocked, RequirementRequired)
			assertTaskMissing(t, fixture.tasks, task.MakeID("example", locator.Branch))
		})
	}
}

func TestAdoptExactCallerAgentIsExcludedButCannotManufactureHot(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "caller-agent")
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "caller-runtime", Dirs: []string{checkout},
		Panes: []runtime.Pane{{ID: "test-caller-pane", CWD: checkout}},
	})
	fake.setAgents(runtime.AgentActivity{
		PaneID: "test-caller-pane", WorkspaceID: "caller-runtime", Agent: "claude",
		Name: "caller", Status: "working", CWD: checkout,
	})
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Availability != AvailabilityReady || adoptEffectDetails(t, plan)["state"] != string(task.Warm) {
		t.Fatalf("availability=%s effects=%+v conditions=%+v", plan.Availability, plan.Effects(), plan.Conditions())
	}
	requireAdoptCondition(t, plan, ConditionAgentOccupancy, VerdictMet, RequirementRequired)
}

func TestAdoptPaneAgentEvidenceBlocksWhenActivityListIsInconsistent(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "pane-agent-evidence")
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "occupied", Dirs: []string{checkout}, AgentSessions: []string{"claude:session"},
		Panes: []runtime.Pane{{
			ID: "other-pane", CWD: checkout, Agent: "claude", AgentStatus: "idle",
			AgentSession: "claude:session",
		}},
	})
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Availability != AvailabilityBlocked {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	requireAdoptCondition(t, plan, ConditionAgentOccupancy, VerdictBlocked, RequirementRequired)
}

func TestAdoptRuntimeListAndActivityErrorsBlock(t *testing.T) {
	for _, failure := range []string{"list", "activity"} {
		t.Run(failure, func(t *testing.T) {
			fixture, locator, checkout := newAdoptGitFixture(t, "runtime-error-"+failure)
			fake := newLifecycleFakeRuntime()
			fake.setSessions(runtime.Session{
				Handle: "shell", Dirs: []string{checkout},
				Panes: []runtime.Pane{{ID: "shell-pane", CWD: checkout}},
			})
			if failure == "list" {
				fake.listErr = errors.New("injected runtime list error")
			} else {
				fake.agentErr = errors.New("injected activity list error")
			}
			service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
			plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != AvailabilityError {
				t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
			}
			requireAdoptCondition(t, plan, ConditionAgentOccupancy, VerdictError, RequirementRequired)
		})
	}
}

func TestAdoptBlocksIneligibleGitAndHarnessEvidence(t *testing.T) {
	t.Run("main checkout", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		repository, err := gitx.Discover(context.Background(), fixture.repo)
		if err != nil {
			t.Fatal(err)
		}
		locator := Locator{
			RepoKey: repository.GitCommonDir, GitCommonDir: repository.GitCommonDir,
			RepoPath: repository.MainRoot, CheckoutPath: repository.MainRoot,
			Branch: "main", HeadOID: strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "HEAD")),
		}
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability == AvailabilityReady {
			t.Fatalf("main checkout was adoptable: %+v", plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionCheckoutLinked, VerdictBlocked, RequirementRequired)
	})

	t.Run("strict harness path", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.repo, ".claude", "worktrees", "adopt-harness")
		if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
			t.Fatal(err)
		}
		locator := addUnmanagedCheckout(t, fixture, "harness-adopt", checkout, false)
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionHarnessOwnership, VerdictBlocked, RequirementRequired)
	})

	t.Run("detached", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "adopt-detached")
		locator := addUnmanagedCheckout(t, fixture, "selected-detached-name", checkout, true)
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionCheckoutBranch, VerdictBlocked, RequirementRequired)
	})

	t.Run("locked", func(t *testing.T) {
		fixture, locator, checkout := newAdoptGitFixture(t, "adopt-locked")
		mustGitCommand(t, fixture.repo, "worktree", "lock", "--reason", "adopt test", checkout)
		t.Cleanup(func() { _ = execGitForCleanup(fixture.repo, "worktree", "unlock", checkout) })
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionCheckoutUnlocked, VerdictBlocked, RequirementRequired)
	})

	t.Run("prunable", func(t *testing.T) {
		fixture, locator, checkout := newAdoptGitFixture(t, "adopt-prunable")
		if err := os.RemoveAll(checkout); err != nil {
			t.Fatal(err)
		}
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability == AvailabilityReady {
			t.Fatalf("prunable checkout was adoptable: %+v", plan.Conditions())
		}
		condition, ok := conditionByCode(plan, ConditionCheckoutUnlocked)
		if !ok || condition.Verdict == VerdictMet {
			t.Fatalf("checkout flags=%+v present=%t", condition, ok)
		}
	})

	t.Run("Git operation in progress", func(t *testing.T) {
		fixture, locator, _ := newAdoptGitFixture(t, "adopt-operation")
		service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
			GitInProgress: func(context.Context, string) (string, bool, error) { return "rebase", true, nil },
		})
		plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionGitOperation, VerdictBlocked, RequirementRequired)
	})

	t.Run("wrong selected HEAD", func(t *testing.T) {
		fixture, locator, _ := newAdoptGitFixture(t, "adopt-wrong-head")
		locator.HeadOID = strings.Repeat("0", 40)
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionCheckoutExact, VerdictBlocked, RequirementRequired)
	})

	t.Run("missing explicit base", func(t *testing.T) {
		fixture, locator, _ := newAdoptGitFixture(t, "adopt-missing-base")
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{Base: "does-not-exist"}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		requireAdoptCondition(t, plan, ConditionExplicitBase, VerdictBlocked, RequirementRequired)
	})
}

func TestAdoptRejectsIncompleteInventoryAndEveryClaimKind(t *testing.T) {
	for _, claimKind := range []string{"path", "branch", "id"} {
		t.Run(claimKind, func(t *testing.T) {
			fixture, locator, checkout := newAdoptGitFixture(t, "claim-"+claimKind)
			claimed := task.Task{
				ID: "existing-" + claimKind, Repo: "other-display", RepoPath: fixture.repo,
				Branch: "different-" + claimKind, Base: "main", Mode: task.ModeBranch, State: task.Warm,
			}
			switch claimKind {
			case "path":
				claimed.Mode = task.ModeWorktree
				claimed.WorktreePath = checkout
			case "branch":
				claimed.Branch = locator.Branch
			case "id":
				claimed.ID = task.MakeID("example", locator.Branch)
			}
			if _, err := fixture.tasks.Create(context.Background(), &claimed); err != nil {
				t.Fatal(err)
			}
			plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Availability != AvailabilityBlocked {
				t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
			}
			claims := requireAdoptCondition(t, plan, ConditionTaskClaims, VerdictBlocked, RequirementRequired)
			if !strings.Contains(claims.Evidence, claimKind) && claimKind != "branch" {
				t.Fatalf("claim evidence=%q", claims.Evidence)
			}
		})
	}

	t.Run("sanitized ID collision", func(t *testing.T) {
		fixture, locator, _ := newAdoptGitFixture(t, "topic-a")
		colliding := task.Task{
			Repo: "example", RepoPath: fixture.repo, Branch: "topic/a", Base: "main",
			Mode: task.ModeBranch, State: task.Warm,
		}
		created, err := fixture.tasks.Create(context.Background(), &colliding)
		if err != nil {
			t.Fatal(err)
		}
		if created.Task.ID != task.MakeID("example", locator.Branch) {
			t.Fatalf("fixture IDs do not collide: existing=%s adopt=%s", created.Task.ID, task.MakeID("example", locator.Branch))
		}
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		claims := requireAdoptCondition(t, plan, ConditionTaskClaims, VerdictBlocked, RequirementRequired)
		if !strings.Contains(claims.Evidence, "derived task ID") {
			t.Fatalf("collision evidence=%q", claims.Evidence)
		}
	})

	t.Run("corrupt task inventory", func(t *testing.T) {
		fixture, locator, _ := newAdoptGitFixture(t, "corrupt-inventory")
		if err := os.WriteFile(filepath.Join(fixture.tasks.Dir, "corrupt-adopt.toml"), []byte("not = = toml"), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityError {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		inventory := requireAdoptCondition(t, plan, ConditionTaskInventory, VerdictError, RequirementRequired)
		if !strings.Contains(inventory.Evidence, "corrupt-adopt") {
			t.Fatalf("inventory evidence=%q", inventory.Evidence)
		}
	})
}

func TestAdoptStateCompatibilityCannotManufactureState(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "state-compatibility")
	for _, requested := range []task.State{task.Hot, task.Cold, task.Done} {
		t.Run(string(requested), func(t *testing.T) {
			plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{State: requested}))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Availability != AvailabilityBlocked || adoptEffectDetails(t, plan)["state"] != string(task.Warm) {
				t.Fatalf("requested=%s availability=%s effects=%+v conditions=%+v", requested, plan.Availability, plan.Effects(), plan.Conditions())
			}
			requireAdoptCondition(t, plan, ConditionAdoptState, VerdictBlocked, RequirementRequired)
		})
	}
	compatible, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{State: task.Warm}))
	if err != nil {
		t.Fatal(err)
	}
	if compatible.Availability != AvailabilityReady {
		t.Fatalf("compatible availability=%s conditions=%+v", compatible.Availability, compatible.Conditions())
	}
	if _, err := NewRequest(locator, AdoptOptions{Mode: task.ModeBranch}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("branch mode error=%v, want ErrInvalidRequest", err)
	}
	badLocator := locator
	badLocator.Mode = task.ModeDirect
	if _, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, badLocator, AdoptOptions{})); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("direct locator mode error=%v, want ErrInvalidRequest", err)
	}
}

func TestAdoptHotCompatibilityMustMatchFreshDerivation(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "hot-state-mismatch")
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "hot-shell", Dirs: []string{checkout},
		Panes: []runtime.Pane{{ID: "hot-pane", CWD: checkout}},
	})
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{State: task.Warm}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Availability != AvailabilityBlocked || adoptEffectDetails(t, plan)["state"] != string(task.Hot) {
		t.Fatalf("availability=%s effects=%+v conditions=%+v", plan.Availability, plan.Effects(), plan.Conditions())
	}
	requireAdoptCondition(t, plan, ConditionAdoptState, VerdictBlocked, RequirementRequired)
}

func TestAdoptApplyRejectsPlanToApplyAuthorityRacesWithoutTaskCreation(t *testing.T) {
	t.Run("checkout HEAD", func(t *testing.T) {
		fixture, locator, checkout := newAdoptGitFixture(t, "race-head")
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(checkout, "race.txt"), []byte("new commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGitCommand(t, checkout, "add", "race.txt")
		mustGitCommand(t, checkout, "commit", "-m", "race target head")
		assertStaleAdoptApply(t, fixture.service, fixture.tasks, locator, plan)
	})

	t.Run("base OID", func(t *testing.T) {
		fixture, locator, _ := newAdoptGitFixture(t, "race-base")
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.repo, "base-race.txt"), []byte("move base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGitCommand(t, fixture.repo, "add", "base-race.txt")
		mustGitCommand(t, fixture.repo, "commit", "-m", "race base oid")
		assertStaleAdoptApply(t, fixture.service, fixture.tasks, locator, plan)
	})

	t.Run("worktree flags", func(t *testing.T) {
		fixture, locator, checkout := newAdoptGitFixture(t, "race-lock")
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		mustGitCommand(t, fixture.repo, "worktree", "lock", "--reason", "race", checkout)
		t.Cleanup(func() { _ = execGitForCleanup(fixture.repo, "worktree", "unlock", checkout) })
		assertStaleAdoptApply(t, fixture.service, fixture.tasks, locator, plan)
	})

	t.Run("task path claim", func(t *testing.T) {
		fixture, locator, checkout := newAdoptGitFixture(t, "race-claim")
		plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		claim := task.Task{
			ID: "late-path-claim", Repo: "late", RepoPath: fixture.repo,
			Branch: "late-other", Base: "main", WorktreePath: checkout,
			Mode: task.ModeWorktree, State: task.Warm,
		}
		if _, err := fixture.tasks.Create(context.Background(), &claim); err != nil {
			t.Fatal(err)
		}
		assertStaleAdoptApply(t, fixture.service, fixture.tasks, locator, plan)
	})

	t.Run("runtime coverage", func(t *testing.T) {
		fixture, locator, checkout := newAdoptGitFixture(t, "race-runtime")
		fake := newLifecycleFakeRuntime()
		service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
		plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		fake.setSessions(runtime.Session{
			Handle: "appeared-shell", Dirs: []string{checkout},
			Panes: []runtime.Pane{{ID: "appeared-pane", CWD: checkout}},
		})
		assertStaleAdoptApply(t, service, fixture.tasks, locator, plan)
	})
}

func assertStaleAdoptApply(t *testing.T, service *Service, store *task.Store, locator Locator, plan Plan) {
	t.Helper()
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Apply error=%v steps=%+v, want ErrStalePlan", err, result.AttemptedSteps())
	}
	if len(result.AttemptedSteps()) != 0 || result.Milestone != MilestoneNone || result.PartialSuccess {
		t.Fatalf("stale result milestone=%s partial=%t steps=%+v", result.Milestone, result.PartialSuccess, result.AttemptedSteps())
	}
	assertTaskMissing(t, store, task.MakeID("example", locator.Branch))
}

func TestAdoptCreateFailureChangesNoGitFilesystemOrRuntimeState(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "create-failure")
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "untouched-shell", Dirs: []string{checkout},
		Panes: []runtime.Pane{{ID: "untouched-pane", CWD: checkout}},
	})
	createErr := errors.New("injected task create failure")
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{
		TaskCreate: func(*task.Tx, *task.Task) (*task.Record, error) { return nil, createErr },
	})
	beforeWorktrees := mustGitCommand(t, fixture.repo, "worktree", "list", "--porcelain")
	beforeHead := strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD"))
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, createErr) {
		t.Fatalf("Apply error=%v, want injected failure", err)
	}
	if result.Milestone != MilestoneNone || result.PartialSuccess || len(result.CompletedSteps()) != 0 ||
		len(result.FailedSteps()) != 1 {
		t.Fatalf("milestone=%s partial=%t steps=%+v", result.Milestone, result.PartialSuccess, result.AttemptedSteps())
	}
	assertTaskMissing(t, fixture.tasks, task.MakeID("example", locator.Branch))
	if after := mustGitCommand(t, fixture.repo, "worktree", "list", "--porcelain"); after != beforeWorktrees {
		t.Fatalf("worktree registry changed\nbefore:\n%s\nafter:\n%s", beforeWorktrees, after)
	}
	if afterHead := strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s", beforeHead, afterHead)
	}
	opens, closes := fake.counts()
	if opens != 0 || closes != 0 {
		t.Fatalf("runtime opens=%d closes=%d", opens, closes)
	}
}

func TestAdoptVerifiesTaskCreateBeforeMilestone(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "unverified-create")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskCreate: func(_ *task.Tx, candidate *task.Task) (*task.Record, error) {
			copy := *candidate
			copy.Tags = append([]string(nil), candidate.Tags...)
			return &task.Record{Task: copy, Revision: "not-persisted"}, nil
		},
	})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err == nil || result.Milestone != MilestoneNone || result.PartialSuccess || len(result.FailedSteps()) != 1 {
		t.Fatalf("error=%v milestone=%s partial=%t steps=%+v", err, result.Milestone, result.PartialSuccess, result.AttemptedSteps())
	}
	assertTaskMissing(t, fixture.tasks, task.MakeID("example", locator.Branch))
}

func TestAdoptPlanAndApplyHoldRepositoryThenTaskStoreLock(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "lock-order")
	var repoHeld atomic.Bool
	var repoCalls atomic.Int32
	var occupancyUnderBoth atomic.Int32
	var createUnderBoth atomic.Bool
	probeTaskLock := func() bool {
		probeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		err := fixture.tasks.WithLock(probeCtx, func(*task.Tx) error { return nil })
		return errors.Is(err, context.DeadlineExceeded)
	}
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		RepoLock: func(_ context.Context, _ string, operation func() error) error {
			repoCalls.Add(1)
			if !repoHeld.CompareAndSwap(false, true) {
				return errors.New("repository lock re-entered")
			}
			defer repoHeld.Store(false)
			return operation()
		},
		InspectOccupancy: func(ctx context.Context, rt runtime.Runtime, target string, options runtime.OccupancyOptions) (runtime.Occupancy, error) {
			if repoHeld.Load() && probeTaskLock() {
				occupancyUnderBoth.Add(1)
			}
			return runtime.InspectOccupancy(ctx, rt, target, options)
		},
		TaskCreate: func(tx *task.Tx, candidate *task.Task) (*task.Record, error) {
			createUnderBoth.Store(repoHeld.Load() && probeTaskLock())
			return tx.Create(candidate)
		},
	})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if repoCalls.Load() != 1 || occupancyUnderBoth.Load() != 1 {
		t.Fatalf("after Plan repo-calls=%d occupancy-under-both=%d", repoCalls.Load(), occupancyUnderBoth.Load())
	}
	if _, err := service.Apply(context.Background(), plan, Approve(plan.PlanID)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if repoCalls.Load() != 2 || occupancyUnderBoth.Load() != 2 || !createUnderBoth.Load() {
		t.Fatalf("repo-calls=%d occupancy-under-both=%d create-under-both=%t",
			repoCalls.Load(), occupancyUnderBoth.Load(), createUnderBoth.Load())
	}
}

func TestAdoptRunsOnlyTaskCreateMutationHook(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "hook-isolation")
	fake := newLifecycleFakeRuntime()
	fake.setSessions(runtime.Session{
		Handle: "existing-shell", Dirs: []string{checkout},
		Panes: []runtime.Pane{{ID: "existing-pane", CWD: checkout}},
	})
	var nonTaskMutations atomic.Int32
	var taskCreates atomic.Int32
	unexpected := func() { nonTaskMutations.Add(1) }
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{
		GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) == 0 || args[0] != "rev-parse" {
				unexpected()
			}
			return gitx.Run(ctx, dir, args...)
		},
		WIPCommit: func(context.Context, string, string) (bool, error) {
			unexpected()
			return false, errors.New("unexpected WIP commit")
		},
		AnalyzeFinish: func(context.Context, string, string, string) (gitx.FinishAnalysis, error) {
			unexpected()
			return gitx.FinishAnalysis{}, errors.New("unexpected finish analysis")
		},
		CommitAll: func(context.Context, string, string) error {
			unexpected()
			return errors.New("unexpected commit")
		},
		DiscardAll: func(context.Context, string) error {
			unexpected()
			return errors.New("unexpected discard")
		},
		RemoveWorktree: func(context.Context, string, string, bool) error {
			unexpected()
			return errors.New("unexpected worktree removal")
		},
		InspectArtifacts: func(context.Context, *artifact.Store, string) (artifact.ReadinessInspection, error) {
			unexpected()
			return artifact.ReadinessInspection{}, errors.New("unexpected artifact inspection")
		},
		InspectCleanup: func(context.Context, runtime.Runtime, string, retire.Options) (retire.Inspection, error) {
			unexpected()
			return retire.Inspection{}, errors.New("unexpected cleanup inspection")
		},
		CloseAndWait: func(context.Context, runtime.Runtime, string, retire.Options) (retire.Inspection, error) {
			unexpected()
			return retire.Inspection{}, errors.New("unexpected runtime close")
		},
		OpenRuntime: func(context.Context, runtime.Runtime, string, string) (runtime.OpenResult, error) {
			unexpected()
			return runtime.OpenResult{}, errors.New("unexpected runtime open")
		},
		CreateWorktree: func(context.Context, wt.CreateRequest) (*wt.CreateResult, error) {
			unexpected()
			return nil, errors.New("unexpected worktree create")
		},
		DetectForge: func(context.Context, string) forge.Kind {
			unexpected()
			return forge.Unknown
		},
		ResolveForge: func(forge.Kind) (forge.Forge, error) {
			unexpected()
			return nil, errors.New("unexpected forge resolve")
		},
		CreatePR: func(context.Context, forge.Forge, string, forge.PRRequest) (string, error) {
			unexpected()
			return "", errors.New("unexpected review create")
		},
		TaskUpdate: func(*task.Tx, *task.Task, string) (*task.Record, error) {
			unexpected()
			return nil, errors.New("unexpected task update")
		},
		TaskDelete: func(*task.Tx, string, string) error {
			unexpected()
			return errors.New("unexpected task delete")
		},
		TaskCreate: func(tx *task.Tx, candidate *task.Task) (*task.Record, error) {
			taskCreates.Add(1)
			return tx.Create(candidate)
		},
	})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if nonTaskMutations.Load() != 0 || taskCreates.Load() != 0 {
		t.Fatalf("planning called non-task=%d task-create=%d mutation hooks", nonTaskMutations.Load(), taskCreates.Load())
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if nonTaskMutations.Load() != 0 || taskCreates.Load() != 1 {
		t.Fatalf("non-task mutation hooks=%d task-create hooks=%d", nonTaskMutations.Load(), taskCreates.Load())
	}
	opens, closes := fake.counts()
	if opens != 0 || closes != 0 || !reflect.DeepEqual(stepCodes(result), []EffectCode{EffectCreateTask}) {
		t.Fatalf("runtime opens=%d closes=%d steps=%+v", opens, closes, result.AttemptedSteps())
	}
}

func TestAdoptLocatorRequiresExactUnmanagedIdentity(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "locator-validation")
	tests := []struct {
		name   string
		mutate func(*Locator)
	}{
		{name: "task id", mutate: func(value *Locator) { value.TaskID = "managed" }},
		{name: "revision", mutate: func(value *Locator) { value.TaskRevision = "revision" }},
		{name: "repository", mutate: func(value *Locator) { value.RepoPath = "" }},
		{name: "common directory", mutate: func(value *Locator) { value.GitCommonDir = "" }},
		{name: "checkout", mutate: func(value *Locator) { value.CheckoutPath = "" }},
		{name: "branch", mutate: func(value *Locator) { value.Branch = "" }},
		{name: "HEAD", mutate: func(value *Locator) { value.HeadOID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := locator
			test.mutate(&changed)
			request := newAdoptRequest(t, changed, AdoptOptions{})
			if _, err := fixture.service.Plan(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Plan error=%v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestAdoptExplicitBaseOIDIsPlanAuthority(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "explicit-base")
	remotePlan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{Base: "origin/main"}))
	if err != nil {
		t.Fatal(err)
	}
	if remotePlan.Availability != AvailabilityReady || adoptEffectDetails(t, remotePlan)["base"] != "origin/main" {
		t.Fatalf("remote-ref base availability=%s conditions=%+v", remotePlan.Availability, remotePlan.Conditions())
	}

	mustGitCommand(t, fixture.repo, "branch", "release-base", "main")
	plan, err := fixture.service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{Base: "release-base"}))
	if err != nil {
		t.Fatal(err)
	}
	baseOID := strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "release-base^{commit}"))
	details := adoptEffectDetails(t, plan)
	if plan.Availability != AvailabilityReady || details["base"] != "release-base" || details["base-oid"] != baseOID {
		t.Fatalf("availability=%s details=%v conditions=%+v", plan.Availability, details, plan.Conditions())
	}
	if err := os.WriteFile(filepath.Join(fixture.repo, "move-release-base.txt"), []byte("move release base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitCommand(t, fixture.repo, "add", "move-release-base.txt")
	mustGitCommand(t, fixture.repo, "commit", "-m", "move release base")
	mustGitCommand(t, fixture.repo, "branch", "-f", "release-base", "main")
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) || len(result.AttemptedSteps()) != 0 {
		t.Fatalf("Apply error=%v steps=%+v", err, result.AttemptedSteps())
	}
}

func TestAdoptUnavailableRuntimeBlocks(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "runtime-unavailable")
	fake := newLifecycleFakeRuntime()
	fake.available = false
	service := fixture.serviceWith(t, fake, fixture.root, LifecycleHooks{})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Availability != AvailabilityBlocked {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	requireAdoptCondition(t, plan, ConditionRuntimeAvailable, VerdictBlocked, RequirementRequired)
}

func TestAdoptTaskCreateHookReceivesApprovedCandidate(t *testing.T) {
	fixture, locator, checkout := newAdoptGitFixture(t, "candidate-hook")
	options := AdoptOptions{Name: "candidate", Base: "main", Next: "next", Note: "note", Tags: NewStringList("one")}
	var received task.Task
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskCreate: func(tx *task.Tx, candidate *task.Task) (*task.Record, error) {
			received = *candidate
			received.Tags = append([]string(nil), candidate.Tags...)
			return tx.Create(candidate)
		},
	})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, options))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepo, canonicalRepoErr := pathx.Canonical(fixture.repo)
	canonicalCheckout, canonicalCheckoutErr := pathx.Canonical(checkout)
	if canonicalRepoErr != nil || canonicalCheckoutErr != nil {
		t.Fatalf("canonical paths repo=%v checkout=%v", canonicalRepoErr, canonicalCheckoutErr)
	}
	want := task.Task{
		ID: task.MakeID("example", locator.Branch), Name: options.Name,
		Repo: "example", RepoPath: canonicalRepo, Branch: locator.Branch, Base: "main",
		WorktreePath: canonicalCheckout, Mode: task.ModeWorktree, State: task.Warm, Owner: "test-host",
		Next: options.Next, Note: options.Note, Tags: []string{"one"},
	}
	if adoptTaskAuthority(received) != adoptTaskAuthority(want) {
		t.Fatalf("received candidate=%+v want=%+v", received, want)
	}
	if snapshot, ok := result.FreshSnapshotRef(); !ok || !strings.HasPrefix(snapshot, "task:") {
		t.Fatalf("snapshot=%q present=%t", snapshot, ok)
	}
}

func TestAdoptApplyRejectsTaskCreateCollisionWithoutOverwrite(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "late-id-collision")
	request := newAdoptRequest(t, locator, AdoptOptions{})
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	id := task.MakeID("example", locator.Branch)
	colliding := task.Task{
		ID: id, Name: "preexisting", Repo: "other", RepoPath: fixture.repo,
		Branch: "other-branch", Base: "main", Mode: task.ModeBranch, State: task.Warm,
	}
	created, err := fixture.tasks.Create(context.Background(), &colliding)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) || len(result.AttemptedSteps()) != 0 {
		t.Fatalf("Apply error=%v steps=%+v", err, result.AttemptedSteps())
	}
	current, err := fixture.tasks.GetRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != created.Revision || current.Task.Name != "preexisting" {
		t.Fatalf("collision was overwritten: before=%+v after=%+v", created, current)
	}
}

func TestAdoptErrorMessagesRetainOperationContext(t *testing.T) {
	fixture, locator, _ := newAdoptGitFixture(t, "error-context")
	createErr := fmt.Errorf("disk full")
	service := fixture.serviceWith(t, lifecycleObservedEmptyRuntime{}, fixture.root, LifecycleHooks{
		TaskCreate: func(*task.Tx, *task.Task) (*task.Record, error) { return nil, createErr },
	})
	plan, err := service.Plan(context.Background(), newAdoptRequest(t, locator, AdoptOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, createErr) || !strings.Contains(err.Error(), "create adopted task") {
		t.Fatalf("Apply error=%v", err)
	}
}
