package taskflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestRetireRealGitDoneWorktreeRemovesCheckoutAndReapsTask(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectRemoveWorktree, EffectDeleteTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if result.Milestone != MilestoneRetired || result.PartialSuccess {
		t.Fatalf("milestone=%s partial=%t", result.Milestone, result.PartialSuccess)
	}
	if _, err := fixture.tasks.Get(fixture.record.Task.ID); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("retired task lookup=%v", err)
	}
	if _, err := os.Stat(fixture.worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed worktree stat=%v", err)
	}
	if !gitx.BranchExists(context.Background(), fixture.repo, "feature") {
		t.Fatal("retirement deleted the retained branch")
	}
}

func TestRetireRealGitDoneWithoutCheckoutReapsTask(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	updateFixtureTask(t, fixture, func(candidate *task.Task) { candidate.State = task.Done })
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady || !reflect.DeepEqual(effectCodes(plan), []EffectCode{EffectDeleteTask}) {
		t.Fatalf("availability=%s effects=%v conditions=%+v", plan.Availability, effectCodes(plan), plan.Conditions())
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Milestone != MilestoneRetired || len(result.CompletedSteps()) != 1 {
		t.Fatalf("result milestone=%s steps=%+v", result.Milestone, result.AttemptedSteps())
	}
}

func TestRetireRealGitBranchAndDirectRetainCanonicalCheckout(t *testing.T) {
	for _, mode := range []task.CheckoutMode{task.ModeBranch, task.ModeDirect} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, mode, task.Done)
			plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Availability != AvailabilityReady || !reflect.DeepEqual(effectCodes(plan), []EffectCode{EffectDeleteTask}) {
				t.Fatalf("availability=%s effects=%v conditions=%+v", plan.Availability, effectCodes(plan), plan.Conditions())
			}
			result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Milestone != MilestoneRetired {
				t.Fatalf("milestone=%s", result.Milestone)
			}
			if info, err := os.Stat(fixture.repo); err != nil || !info.IsDir() {
				t.Fatalf("canonical checkout was removed: info=%v err=%v", info, err)
			}
			if !gitx.BranchExists(context.Background(), fixture.repo, fixture.record.Task.Branch) {
				t.Fatal("canonical retirement deleted its branch")
			}
		})
	}
}

func TestRetireRealGitContainedBranchDeletionAndUncontainedBlock(t *testing.T) {
	t.Run("contained branch deletion", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
		plan, err := fixture.service.Plan(context.Background(), fixture.request(t, RetireOptions{DeleteBranch: true}))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.Availability != AvailabilityReady || plan.Confirmation.Kind != ConfirmationTyped {
			t.Fatalf("availability=%s confirmation=%+v conditions=%+v", plan.Availability, plan.Confirmation, plan.Conditions())
		}
		result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
		if err != nil {
			t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
		}
		if result.Milestone != MilestoneRetired || gitx.BranchExists(context.Background(), fixture.repo, "feature") {
			t.Fatalf("milestone=%s branch-exists=%t", result.Milestone, gitx.BranchExists(context.Background(), fixture.repo, "feature"))
		}
		if got := stepCodes(result); !reflect.DeepEqual(got, []EffectCode{EffectRemoveWorktree, EffectDeleteBranch, EffectDeleteTask}) {
			t.Fatalf("steps=%v", got)
		}
	})

	t.Run("uncontained branch", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
		if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("feature\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGitCommand(t, fixture.worktree, "add", "tracked.txt")
		mustGitCommand(t, fixture.worktree, "commit", "-m", "feature only")
		request := fixture.request(t, RetireOptions{DeleteBranch: true})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		relation, ok := conditionByCode(plan, ConditionBranchRelation)
		if !ok || relation.Verdict != VerdictBlocked {
			t.Fatalf("relation=%+v present=%t", relation, ok)
		}
	})
}

func addUnmanagedCheckout(t *testing.T, fixture *lifecycleGitFixture, branch, path string, detached bool) Locator {
	t.Helper()
	if detached {
		mustGitCommand(t, fixture.repo, "worktree", "add", "--detach", path, "main")
	} else {
		mustGitCommand(t, fixture.repo, "branch", branch, "main")
		mustGitCommand(t, fixture.repo, "worktree", "add", path, branch)
	}
	repository, err := gitx.Discover(context.Background(), fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(mustGitCommand(t, path, "rev-parse", "HEAD"))
	return Locator{
		RepoKey: repository.GitCommonDir, RepositoryID: repository.GitCommonDir,
		GitCommonDir: repository.GitCommonDir, RepoPath: repository.MainRoot,
		CheckoutPath: path, Branch: branch, HeadOID: head,
		RowKind: "unmanaged", RowKey: path,
	}
}

func TestRemoveCheckoutRealGitCleanPreservesBranchAndOID(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
	checkout := filepath.Join(fixture.root, "unmanaged")
	locator := addUnmanagedCheckout(t, fixture, "unmanaged", checkout, false)
	request, err := NewRequest(locator, RemoveCheckoutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady || !reflect.DeepEqual(effectCodes(plan), []EffectCode{EffectRemoveWorktree}) {
		t.Fatalf("availability=%s effects=%v conditions=%+v", plan.Availability, effectCodes(plan), plan.Conditions())
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if result.Milestone != MilestoneNone || result.PartialSuccess {
		t.Fatalf("milestone=%s partial=%t", result.Milestone, result.PartialSuccess)
	}
	if !gitx.BranchExists(context.Background(), fixture.repo, "unmanaged") {
		t.Fatal("unmanaged removal deleted the branch")
	}
	gotOID := strings.TrimSpace(mustGitCommand(t, fixture.repo, "rev-parse", "refs/heads/unmanaged"))
	if gotOID != locator.HeadOID {
		t.Fatalf("branch OID=%s want=%s", gotOID, locator.HeadOID)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout stat=%v", err)
	}
	if _, err := fixture.tasks.Get(fixture.record.Task.ID); err != nil {
		t.Fatalf("unmanaged removal changed an unrelated task: %v", err)
	}
}

func TestRemoveCheckoutRealGitContainedBranchDeletion(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
	checkout := filepath.Join(fixture.root, "contained-delete")
	locator := addUnmanagedCheckout(t, fixture, "contained-delete", checkout, false)
	request, err := NewRequest(locator, RemoveCheckoutOptions{
		RequireContained: true, ContainmentBase: "main", DeleteContainedBranch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady || plan.Confirmation.Kind != ConfirmationTyped {
		t.Fatalf("availability=%s confirmation=%+v conditions=%+v", plan.Availability, plan.Confirmation, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectRemoveWorktree, EffectDeleteBranch}) {
		t.Fatalf("effects=%v", got)
	}
	if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); !errors.Is(err, ErrInvalidApproval) {
		t.Fatalf("untyped approval error=%v", err)
	}
	result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
	if err != nil {
		t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
	}
	if got := stepCodes(result); !reflect.DeepEqual(got, []EffectCode{EffectRemoveWorktree, EffectDeleteBranch}) {
		t.Fatalf("steps=%v", got)
	}
	if gitx.BranchExists(context.Background(), fixture.repo, locator.Branch) {
		t.Fatalf("contained branch %q still exists", locator.Branch)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout stat=%v", err)
	}
}

func TestRemoveCheckoutRealGitDirtyDefaultAndTypedDiscard(t *testing.T) {
	t.Run("default blocks", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "dirty-default")
		locator := addUnmanagedCheckout(t, fixture, "dirty-default", checkout, false)
		if err := os.WriteFile(filepath.Join(checkout, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		request, _ := NewRequest(locator, RemoveCheckoutOptions{})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityBlocked {
			t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
		}
		if clean, _ := conditionByCode(plan, ConditionCheckoutClean); clean.Verdict != VerdictBlocked {
			t.Fatalf("clean condition=%+v", clean)
		}
	})

	t.Run("typed discard", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "dirty-discard")
		locator := addUnmanagedCheckout(t, fixture, "dirty-discard", checkout, false)
		if err := os.WriteFile(filepath.Join(checkout, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		request, _ := NewRequest(locator, RemoveCheckoutOptions{DiscardDirty: true})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Availability != AvailabilityReady || plan.Confirmation.Kind != ConfirmationTyped ||
			!strings.Contains(plan.Confirmation.Token, checkout) {
			t.Fatalf("availability=%s confirmation=%+v conditions=%+v", plan.Availability, plan.Confirmation, plan.Conditions())
		}
		if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectDiscardAll, EffectRemoveWorktree}) {
			t.Fatalf("effects=%v", got)
		}
		if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); !errors.Is(err, ErrInvalidApproval) {
			t.Fatalf("untyped approval error=%v", err)
		}
		result, err := fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
		if err != nil {
			t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
		}
		if got := stepCodes(result); !reflect.DeepEqual(got, []EffectCode{EffectDiscardAll, EffectRemoveWorktree}) {
			t.Fatalf("steps=%v", got)
		}
	})

	t.Run("typed discard rejects same-count content replacement", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Done)
		checkout := filepath.Join(fixture.root, "dirty-content-race")
		locator := addUnmanagedCheckout(t, fixture, "dirty-content-race", checkout, false)
		path := filepath.Join(checkout, "tracked.txt")
		if err := os.WriteFile(path, []byte("first dirty bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		request, _ := NewRequest(locator, RemoveCheckoutOptions{DiscardDirty: true})
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil || plan.Availability != AvailabilityReady {
			t.Fatalf("Plan: %v availability=%s", err, plan.Availability)
		}
		if err := os.WriteFile(path, []byte("other dirty bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = fixture.service.Apply(context.Background(), plan, ApproveWithToken(plan.PlanID, plan.Confirmation.Token))
		if !errors.Is(err, ErrStalePlan) {
			t.Fatalf("same-count replacement Apply = %v, want ErrStalePlan", err)
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != "other dirty bytes\n" {
			t.Fatalf("replacement bytes were discarded: %q, %v", body, readErr)
		}
	})
}
