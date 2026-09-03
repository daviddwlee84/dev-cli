package taskflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

func (s *lifecycleService) applyGuarded(ctx context.Context, action Action, approved Plan) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if approved.Action != action {
		return Result{}, &InvalidPlanError{PlanID: approved.PlanID, Reason: fmt.Sprintf("handler %s received %s", action, approved.Action)}
	}
	if err := validateLifecycleLocator(approved.Locator); err != nil {
		return Result{}, err
	}
	commonDir, err := s.canonicalPath(approved.Locator.GitCommonDir)
	if err != nil {
		return Result{}, fmt.Errorf("%w: canonicalize approved repository lock identity: %v", ErrInvalidPlan, err)
	}

	var result Result
	err = s.repoLock(ctx, commonDir, func() error {
		return s.tasks.WithLock(ctx, func(tx *task.Tx) error {
			record, loadErr := tx.GetRecord(approved.Locator.TaskID)
			if loadErr != nil {
				return &StalePlanError{
					ExpectedPlanID: approved.PlanID,
					Reason:         "task record disappeared before apply",
				}
			}
			if identityErr := validateRecordIdentity(approved.Locator, *record); identityErr != nil {
				return identityErr
			}
			fresh, observed, planErr := s.freshPlan(ctx, approved.Request, *record)
			if planErr != nil {
				return planErr
			}
			current, reloadErr := tx.GetRecord(approved.Locator.TaskID)
			if reloadErr != nil || current.Revision != record.Revision {
				actual := ""
				if current != nil {
					actual = current.Revision
				}
				return staleTaskRevision(record.Revision, actual, "task record changed during locked plan rebuild")
			}
			if fresh.PlanID != approved.PlanID || fresh.AuthorityFingerprint != approved.AuthorityFingerprint {
				return &StalePlanError{
					ExpectedPlanID:               approved.PlanID,
					ActualPlanID:                 fresh.PlanID,
					ExpectedAuthorityFingerprint: approved.AuthorityFingerprint,
					ActualAuthorityFingerprint:   fresh.AuthorityFingerprint,
					Reason:                       "fresh task, Git, worktree, artifact, runtime, or option authority differs",
				}
			}
			if fresh.Availability != AvailabilityReady {
				notReady := &PlanNotReadyError{PlanID: fresh.PlanID, Availability: fresh.Availability, conditions: fresh.Conditions()}
				return &InvalidPlanError{PlanID: fresh.PlanID, Reason: "fresh required conditions are not ready", Cause: notReady}
			}

			execution := &executionState{
				service: s, plan: fresh, observed: observed, tx: tx, revision: record.Revision,
			}
			var applyErr error
			switch action {
			case ParkWarm:
				result, applyErr = execution.applyParkWarm(ctx)
			case ParkCold:
				result, applyErr = execution.applyParkCold(ctx)
			case Resume:
				result, applyErr = execution.applyResume(ctx)
			case CompleteDirect:
				result, applyErr = execution.applyCompleteDirect(ctx)
			case CompleteFF:
				result, applyErr = execution.applyCompleteFF(ctx)
			case ReviewHandoff:
				result, applyErr = execution.applyReviewHandoff(ctx)
			case VerifyMerged:
				result, applyErr = execution.applyVerifyMerged(ctx)
			default:
				applyErr = &HandlerUnavailableError{Action: action, Stage: "apply"}
			}
			return applyErr
		})
	})
	return result, err
}

func (e *executionState) applyParkWarm(ctx context.Context) (Result, error) {
	options := e.plan.Request.Options.(ParkWarmOptions)
	observed := &e.observed
	if observed.statusErr == nil && observed.status.Dirty() && !options.CommitWIP {
		e.warnings = append(e.warnings, observed.checkout+" has uncommitted changes; bytes remain in place")
	}
	if observed.desiredNext(options) == "" {
		e.warnings = append(e.warnings, "no next action is recorded")
	}
	if !options.KeepSession && observed.cleanup.CallerContained {
		e.warnings = append(e.warnings, "runtime remains active because the caller is contained; exit normally and reconcile later")
	}

	closedRuntime := false
	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCommitWIP:
			if err := e.service.revalidateGitBaseline(ctx, *observed); err != nil {
				return e.fail(err, "refresh the task and retry warm parking")
			}
			err := e.run(effect, func() (string, error) {
				made, commitErr := e.service.wipCommit(ctx, observed.checkout, effect.Details.Map()["message"])
				if commitErr != nil {
					return "", fmt.Errorf("checkpoint WIP: %w", commitErr)
				}
				if !made {
					return "no product changes required a checkpoint", nil
				}
				return "created WIP checkpoint", nil
			})
			if err != nil {
				return e.fail(err, "preserve the checkout and retry after fixing the checkpoint failure")
			}
			if err := e.service.refreshCheckout(ctx, observed, observed.task.Branch); err != nil {
				return e.fail(fmt.Errorf("refresh after WIP checkpoint: %w", err), "inspect the committed checkpoint before retrying")
			}
		case EffectPushBranch:
			if err := e.service.revalidateGitBaseline(ctx, *observed); err != nil {
				return e.fail(err, "inspect the checkout and retry the push")
			}
			err := e.run(effect, func() (string, error) {
				_, pushErr := e.service.gitRun(ctx, observed.checkout, "push", "--set-upstream", "origin", observed.task.Branch)
				if pushErr != nil {
					return "", fmt.Errorf("push %s: %w", observed.task.Branch, pushErr)
				}
				return "pushed origin/" + observed.task.Branch, nil
			})
			if err != nil {
				return e.fail(err, "the checkout remains present; resolve the push and retry warm parking")
			}
			if err := e.service.refreshCheckout(ctx, observed, observed.task.Branch); err != nil {
				return e.fail(fmt.Errorf("refresh after push: %w", err), "inspect the published branch before retrying")
			}
		case EffectCloseRuntime:
			if err := e.service.revalidateGitBaseline(ctx, *observed); err != nil {
				return e.fail(err, "refresh the task and retry runtime closure")
			}
			if err := e.service.revalidateCleanupBaseline(ctx, *observed, options.CloseUnknown, options.AssumeNoRuntime, options.Timeout); err != nil {
				return e.fail(err, "inspect runtime occupancy from outside the checkout")
			}
			err := e.run(effect, func() (string, error) {
				closed, closeErr := e.service.closeAndWait(ctx, observed.runtime, observed.checkout,
					observed.cleanupOptions(options.CloseUnknown, options.AssumeNoRuntime, options.Timeout))
				if closeErr != nil {
					return "", closeErr
				}
				observed.cleanup = closed
				return fmt.Sprintf("closed %d %s session(s)", closed.ClosedSessions, observed.runtime.Name()), nil
			})
			if err != nil {
				return e.fail(err, "the checkout and task record remain; inspect runtime occupancy and retry")
			}
			if err := e.service.ensureRuntimeReleased(ctx, observed, options.CloseUnknown, options.AssumeNoRuntime, options.Timeout); err != nil {
				return e.fail(err, "a runtime reclaimed the checkout; close it from outside and retry")
			}
			closedRuntime = true
		case EffectUpdateTask:
			if err := e.service.revalidateGitBaseline(ctx, *observed); err != nil {
				return e.fail(err, "refresh the task before recording WARM")
			}
			if !options.KeepSession {
				var runtimeErr error
				if closedRuntime {
					runtimeErr = e.service.ensureRuntimeReleased(ctx, observed, options.CloseUnknown, options.AssumeNoRuntime, options.Timeout)
				} else {
					runtimeErr = e.service.revalidateCleanupBaseline(ctx, *observed, options.CloseUnknown, options.AssumeNoRuntime, options.Timeout)
				}
				if runtimeErr != nil {
					return e.fail(runtimeErr, "runtime occupancy changed; refresh and retry warm parking")
				}
			}
			err := e.run(effect, func() (string, error) {
				candidate := observed.task
				candidate.State = task.Warm
				candidate.Owner = e.service.host
				if options.Next != "" {
					candidate.Next = options.Next
				}
				if options.Note != "" {
					candidate.Note = options.Note
				}
				if !options.KeepSession && !observed.cleanup.CallerContained {
					candidate.RuntimeHandle = ""
					candidate.RuntimeName = ""
				}
				updated, updateErr := e.service.taskUpdate(e.tx, &candidate, e.revision)
				if updateErr != nil {
					return "", fmt.Errorf("write WARM task state: %w", updateErr)
				}
				if updated == nil {
					return "", errors.New("write WARM task state returned no revision")
				}
				e.snapshot = "task:" + updated.Revision
				observed.task = updated.Task
				return "recorded WARM at revision " + updated.Revision, nil
			})
			if err != nil {
				return e.fail(err, "Git/runtime effects may already be complete; reload the task and reconcile its state")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected ParkWarm effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (e *executionState) applyParkCold(ctx context.Context) (Result, error) {
	options := e.plan.Request.Options.(ParkColdOptions)
	observed := &e.observed
	if observed.desiredNext(options) == "" {
		e.warnings = append(e.warnings, "no next action is recorded")
	}
	closedRuntime := false
	checkoutRemoved := false
	baseSwitched := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCommitWIP:
			if err := e.service.revalidateGitBaseline(ctx, *observed); err != nil {
				return e.fail(err, "refresh the task and retry cold parking")
			}
			err := e.run(effect, func() (string, error) {
				made, commitErr := e.service.wipCommit(ctx, observed.checkout, effect.Details.Map()["message"])
				if commitErr != nil {
					return "", fmt.Errorf("checkpoint WIP: %w", commitErr)
				}
				if !made {
					return "no product changes required a checkpoint", nil
				}
				return "created WIP checkpoint", nil
			})
			if err != nil {
				return e.fail(err, "preserve the checkout and resolve the checkpoint failure")
			}
			if err := e.service.refreshCheckout(ctx, observed, observed.task.Branch); err != nil {
				return e.fail(fmt.Errorf("refresh after WIP checkpoint: %w", err), "inspect the checkpoint before retrying")
			}
			if observed.status.Dirty() {
				return e.fail(fmt.Errorf("checkout remains dirty after WIP checkpoint: %s", observed.status.Breakdown()),
					"commit or finalize the remaining bytes before cold parking")
			}
		case EffectPushBranch:
			if err := e.service.ensureParkBranchIdentity(ctx, observed); err != nil {
				return e.fail(err, "inspect the checkout before retrying the push")
			}
			err := e.run(effect, func() (string, error) {
				_, pushErr := e.service.gitRun(ctx, observed.checkout, "push", "--set-upstream", "origin", observed.task.Branch)
				if pushErr != nil {
					return "", fmt.Errorf("push %s: %w", observed.task.Branch, pushErr)
				}
				return "pushed origin/" + observed.task.Branch, nil
			})
			if err != nil {
				return e.fail(err, "the checkout remains present; resolve the push before retrying cold parking")
			}
			if err := e.service.refreshCheckout(ctx, observed, observed.task.Branch); err != nil {
				return e.fail(fmt.Errorf("refresh after push: %w", err), "inspect the published branch before retrying")
			}
		case EffectCloseRuntime:
			if err := e.service.ensureColdGitReady(ctx, observed); err != nil {
				return e.fail(err, "restore a clean, fully pushed branch before runtime cleanup")
			}
			if err := e.service.revalidateColdArtifact(ctx, *observed); err != nil {
				return e.fail(err, "finalize or restore artifact receipts before runtime cleanup")
			}
			if err := e.service.revalidateCleanupBaseline(ctx, *observed, options.CloseUnknown, options.AssumeNoRuntime, options.Timeout); err != nil {
				return e.fail(err, "runtime occupancy changed; refresh cold parking")
			}
			err := e.run(effect, func() (string, error) {
				closed, closeErr := e.service.closeAndWait(ctx, observed.runtime, observed.checkout,
					observed.cleanupOptions(options.CloseUnknown, options.AssumeNoRuntime, options.Timeout))
				if closeErr != nil {
					return "", closeErr
				}
				observed.cleanup = closed
				return fmt.Sprintf("closed %d %s session(s)", closed.ClosedSessions, observed.runtime.Name()), nil
			})
			if err != nil {
				return e.fail(err, "the checkout remains present; inspect runtime occupancy and retry")
			}
			if err := e.service.ensureRuntimeReleased(ctx, observed, options.CloseUnknown, options.AssumeNoRuntime, options.Timeout); err != nil {
				return e.fail(err, "a runtime reclaimed the checkout; close it from outside before retrying")
			}
			closedRuntime = true
		case EffectRemoveWorktree:
			if err := e.service.ensureColdRemovalBoundary(ctx, observed, options, closedRuntime); err != nil {
				return e.fail(err, "the worktree was preserved; refresh every safety condition before retrying")
			}
			err := e.run(effect, func() (string, error) {
				if removeErr := e.service.removeWorktree(ctx, observed.repoPath, observed.checkout, false); removeErr != nil {
					return "", fmt.Errorf("remove exact worktree %s: %w", observed.checkout, removeErr)
				}
				return "removed exact linked worktree; branch retained", nil
			})
			if err != nil {
				return e.fail(err, "inspect the still-registered worktree; never force removal without preserving bytes")
			}
			checkoutRemoved = true
			branchExists, branchErr := e.service.gitRefState(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
			if branchErr != nil {
				return e.fail(fmt.Errorf("verify retained task branch after worktree removal: %w", branchErr), "restore reliable Git ref observation before updating task state")
			}
			if !branchExists {
				return e.fail(errors.New("task branch disappeared after worktree removal"), "restore the retained task branch before updating task state")
			}
		case EffectSwitchBase:
			if err := e.service.ensureColdRemovalBoundary(ctx, observed, options, closedRuntime); err != nil {
				return e.fail(err, "canonical checkout was not switched; refresh cold parking")
			}
			base := effect.Details.Map()["base"]
			baseExists, baseErr := e.service.gitRefState(ctx, observed.repoPath, base)
			if baseErr != nil {
				return e.fail(fmt.Errorf("observe explicit base before canonical checkout switch: %w", baseErr), "repair Git ref observation and retry")
			}
			if base == "" || !baseExists {
				return e.fail(staleBoundary("explicit base ref changed before canonical checkout switch"), "restore the recorded base and retry")
			}
			err := e.run(effect, func() (string, error) {
				_, switchErr := e.service.gitRun(ctx, observed.repoPath, "switch", base)
				if switchErr != nil {
					return "", fmt.Errorf("switch canonical checkout to %s: %w", base, switchErr)
				}
				return "switched canonical checkout to " + base, nil
			})
			if err != nil {
				return e.fail(err, "the task branch remains retained; inspect the canonical checkout and retry")
			}
			if err := e.service.refreshCheckout(ctx, observed, base); err != nil {
				return e.fail(fmt.Errorf("verify canonical base switch: %w", err), "inspect the canonical checkout before reconciling task state")
			}
			branchExists, branchErr := e.service.gitRefState(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
			if branchErr != nil {
				return e.fail(fmt.Errorf("verify task branch after canonical checkout switch: %w", branchErr), "repair Git ref observation before updating task state")
			}
			if !branchExists {
				return e.fail(errors.New("task branch disappeared during canonical checkout switch"), "restore the task branch before updating task state")
			}
			baseSwitched = true
		case EffectUpdateTask:
			if observed.mode == task.ModeWorktree && !checkoutRemoved {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "task update reached before worktree removal"})
			}
			if observed.mode == task.ModeBranch && !baseSwitched {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "task update reached before canonical base switch"})
			}
			err := e.run(effect, func() (string, error) {
				candidate := observed.task
				candidate.State = task.Cold
				candidate.Owner = e.service.host
				candidate.WorktreePath = ""
				candidate.RuntimeHandle = ""
				candidate.RuntimeName = ""
				if options.Next != "" {
					candidate.Next = options.Next
				}
				if options.Note != "" {
					candidate.Note = options.Note
				}
				updated, updateErr := e.service.taskUpdate(e.tx, &candidate, e.revision)
				if updateErr != nil {
					return "", fmt.Errorf("write COLD task state: %w", updateErr)
				}
				if updated == nil {
					return "", errors.New("write COLD task state returned no revision")
				}
				e.snapshot = "task:" + updated.Revision
				observed.task = updated.Task
				return "recorded COLD at revision " + updated.Revision, nil
			})
			if err != nil {
				return e.fail(err, "checkout cleanup succeeded but task persistence failed; reload and reconcile the task path/runtime/state")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected ParkCold effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (e *executionState) applyResume(ctx context.Context) (Result, error) {
	options := e.plan.Request.Options.(ResumeOptions)
	observed := &e.observed
	e.resumePath = observed.checkout
	if observed.runtime != nil && observed.runtime.Name() != "none" && observed.savedRuntimeLive {
		e.resumeRuntimeName = observed.runtime.Name()
		e.resumeRuntimeHandle = observed.task.RuntimeHandle
	}
	runtimeMutated := false
	checkoutMutated := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectFetchRefs:
			if err := e.service.revalidateRepository(ctx, *observed); err != nil {
				return e.fail(err, "refresh the canonical repository before fetching")
			}
			if observed.hasCheckout() {
				if err := e.service.revalidateGitBaseline(ctx, *observed); err != nil {
					return e.fail(err, "refresh checkout authority before fetching")
				}
			} else if matches, err := e.service.branchCheckoutCount(ctx, *observed); err != nil || matches != observed.branchMatches {
				if err != nil {
					return e.fail(err, "refresh worktree topology before fetching")
				}
				return e.fail(staleBoundary("task branch checkout topology changed before fetch"), "refresh resume")
			}
			e.runWarning(effect, func() (string, error) {
				_, fetchErr := e.service.gitRun(ctx, observed.repoPath, "fetch", "--prune", "origin")
				if fetchErr != nil {
					return "", fetchErr
				}
				return "fetched origin", nil
			}, "fetch failed")
			if observed.hasCheckout() {
				if err := e.service.refreshCheckout(ctx, observed, resumeExpectedBranchBeforeSwitch(*observed)); err != nil {
					return e.fail(fmt.Errorf("refresh checkout after fetch: %w", err), "inspect fetched refs and retry resume")
				}
			}
		case EffectCreateWorktree:
			if err := e.service.revalidateResumeBranchRefs(ctx, observed, options.FetchRefs); err != nil {
				return e.fail(err, "refresh the task branch before rebuilding its checkout")
			}
			localExists, localErr := e.service.gitRefState(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
			if localErr != nil {
				return e.fail(fmt.Errorf("observe local task branch before reconstruction: %w", localErr), "repair Git ref observation and retry")
			}
			remoteExists, remoteErr := e.service.gitRefState(ctx, observed.repoPath, observed.remoteBranch)
			if remoteErr != nil {
				return e.fail(fmt.Errorf("observe published task branch before reconstruction: %w", remoteErr), "repair Git ref observation and retry")
			}
			base := observed.task.Branch
			if !localExists {
				base = observed.remoteBranch
			}
			if !localExists && !remoteExists {
				return e.fail(staleBoundary("local and published task branches are unavailable after fetch"), "restore origin/"+observed.task.Branch+" before reconstructing the checkout")
			}
			if matches, err := e.service.branchCheckoutCount(ctx, *observed); err != nil {
				return e.fail(err, "refresh repository topology before rebuilding")
			} else if matches != 0 {
				return e.fail(staleBoundary("the task branch acquired a checkout before reconstruction"), "refresh and reuse the newly registered exact checkout")
			}
			err := e.run(effect, func() (string, error) {
				created, createErr := e.service.createWorktree(ctx, wt.CreateRequest{
					RepoPath: observed.repoPath, RepoName: observed.task.Repo,
					Branch: observed.task.Branch, Base: base,
					Path: effect.Target, Label: resumeRuntimeLabel(observed.task),
					NoProvision: options.NoProvision, NoRuntime: true,
				})
				if createErr != nil {
					var exists *wt.ErrExists
					if errors.As(createErr, &exists) {
						return "", staleBoundary("the task branch became checked out at " + exists.Path)
					}
					return "", createErr
				}
				if created == nil || created.Path == "" {
					return "", errors.New("worktree manager returned no checkout path")
				}
				path, canonicalErr := e.service.canonicalPath(created.Path)
				if canonicalErr != nil {
					return "", canonicalErr
				}
				target, targetErr := e.service.canonicalPath(effect.Target)
				if targetErr != nil || path != target {
					return "", fmt.Errorf("worktree manager created %q, approved target is %q", path, target)
				}
				e.resumePath = path
				return "rebuilt linked worktree from " + base, nil
			})
			if err != nil {
				return e.fail(err, "inspect any partially created checkout, then refresh resume before retrying")
			}
			registered, resolveErr := e.service.resolveWorktree(ctx, observed.repoPath, e.resumePath)
			if resolveErr != nil {
				return e.fail(fmt.Errorf("resolve rebuilt exact worktree: %w", resolveErr), "preserve the rebuilt checkout and reconcile its registration")
			}
			observed.worktree = registered
			observed.worktreeFound = true
			observed.worktreeErr = nil
			observed.checkout = registered.Path
			if !registered.IsLinkedWorktree() || registered.Worktree.Branch != observed.task.Branch {
				return e.fail(errors.New("rebuilt checkout is not the exact linked task branch"), "preserve and reconcile the rebuilt checkout")
			}
			if err := e.service.refreshCheckout(ctx, observed, observed.task.Branch); err != nil {
				return e.fail(fmt.Errorf("verify rebuilt worktree: %w", err), "preserve and inspect the rebuilt checkout")
			}
			checkoutMutated = true
		case EffectSwitchBranch:
			if err := e.service.revalidateResumeOccupancy(ctx, observed, !runtimeMutated && !checkoutMutated); err != nil {
				return e.fail(err, "another agent or runtime changed occupancy; refresh resume")
			}
			if err := e.service.ensureBranchSwitchBoundary(ctx, observed); err != nil {
				return e.fail(err, "preserve canonical checkout changes and retry resume")
			}
			err := e.run(effect, func() (string, error) {
				_, switchErr := e.service.gitRun(ctx, observed.repoPath, "switch", observed.task.Branch)
				if switchErr != nil {
					return "", switchErr
				}
				return "switched canonical checkout to " + observed.task.Branch, nil
			})
			if err != nil {
				return e.fail(err, "inspect the canonical checkout and retry resume")
			}
			if err := e.service.refreshCheckout(ctx, observed, observed.task.Branch); err != nil {
				return e.fail(fmt.Errorf("verify task branch switch: %w", err), "inspect the canonical checkout before retrying")
			}
			checkoutMutated = true
		case EffectOpenRuntime:
			if err := e.service.revalidateResumeCheckout(ctx, observed); err != nil {
				return e.fail(err, "refresh the exact checkout before opening a runtime")
			}
			if err := e.service.revalidateResumeOccupancy(ctx, observed, !runtimeMutated && !checkoutMutated); err != nil {
				return e.fail(err, "another agent or runtime changed occupancy; refresh resume")
			}
			if savedHandleLive(observed.occupancy, observed.task.RuntimeHandle) {
				return e.fail(staleBoundary("saved runtime handle became live before the declared open"), "refresh and reuse the saved runtime")
			}
			err := e.run(effect, func() (string, error) {
				opened, openErr := e.service.openRuntime(ctx, observed.runtime, observed.checkout, resumeRuntimeLabel(observed.task))
				if openErr != nil {
					return "", openErr
				}
				if opened.Handle == "" {
					return "", errors.New("runtime opened without a handle")
				}
				e.resumeRuntimeName = observed.runtime.Name()
				e.resumeRuntimeHandle = opened.Handle
				return "opened " + observed.runtime.Name() + " runtime " + opened.Handle, nil
			})
			if err != nil {
				return e.fail(err, "the checkout remains available; open a runtime manually or retry resume")
			}
			runtimeMutated = true
		case EffectUpdateTask:
			if err := e.service.revalidateResumeCheckout(ctx, observed); err != nil {
				return e.fail(err, "refresh the exact checkout before recording HOT")
			}
			if err := e.service.revalidateResumeOccupancy(ctx, observed, !runtimeMutated && !checkoutMutated); err != nil {
				return e.fail(err, "another recognized agent appeared; close the new surface or refresh resume")
			}
			if observed.runtime.Name() != "none" && !runtimeMutated && observed.task.RuntimeHandle != "" &&
				!savedHandleLive(observed.occupancy, observed.task.RuntimeHandle) {
				return e.fail(staleBoundary("saved runtime handle disappeared before HOT task write"), "refresh resume and open a new runtime")
			}
			err := e.run(effect, func() (string, error) {
				candidate := observed.task
				candidate.State = task.Hot
				candidate.Owner = e.service.host
				if observed.mode == task.ModeWorktree {
					candidate.WorktreePath = observed.checkout
				} else {
					candidate.WorktreePath = ""
				}
				if observed.runtime.Name() == "none" {
					candidate.RuntimeName = ""
					candidate.RuntimeHandle = ""
				} else {
					candidate.RuntimeName = e.resumeRuntimeName
					candidate.RuntimeHandle = e.resumeRuntimeHandle
					if candidate.RuntimeName == "" || candidate.RuntimeHandle == "" {
						return "", errors.New("HOT task requires the validated or newly opened runtime handle")
					}
				}
				updated, updateErr := e.service.taskUpdate(e.tx, &candidate, e.revision)
				if updateErr != nil {
					return "", fmt.Errorf("write HOT task state: %w", updateErr)
				}
				if updated == nil {
					return "", errors.New("write HOT task state returned no revision")
				}
				e.snapshot = "task:" + updated.Revision
				observed.task = updated.Task
				if observed.runtime.Name() == "none" {
					e.handoff = &Handoff{Kind: HandoffDirectory, Path: observed.checkout, Label: candidate.Title()}
				} else {
					e.handoff = &Handoff{Kind: HandoffRuntime, Path: observed.checkout,
						Runtime: candidate.RuntimeName, RuntimeHandle: candidate.RuntimeHandle, Label: candidate.Title()}
				}
				return "recorded HOT at revision " + updated.Revision, nil
			})
			if err != nil {
				return e.fail(err, "checkout/runtime preparation may be complete; reload and reconcile task ownership, path, and runtime")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected Resume effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (s *lifecycleService) revalidateRepository(ctx context.Context, observed lifecycleObservation) error {
	repository, err := s.gitDiscover(ctx, observed.repoPath)
	if err != nil {
		return staleBoundary("canonical repository is no longer discoverable: " + err.Error())
	}
	mainPath, err := s.canonicalPath(repository.MainRoot)
	if err != nil {
		return staleBoundary("canonical repository path is no longer resolvable: " + err.Error())
	}
	commonDir, err := s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		return staleBoundary("Git common directory is no longer resolvable: " + err.Error())
	}
	if mainPath != observed.repoPath || commonDir != observed.gitCommonDir {
		return staleBoundary("canonical repository or Git common directory changed")
	}
	return nil
}

func (s *lifecycleService) revalidateGitBaseline(ctx context.Context, observed lifecycleObservation) error {
	registered, err := s.resolveWorktree(ctx, observed.repoPath, observed.checkout)
	if err != nil {
		return staleBoundary("exact worktree registration changed: " + err.Error())
	}
	if registered != observed.worktree {
		return staleBoundary("exact worktree path, branch, HEAD, Main, locked, or prunable flags changed")
	}
	status, err := s.gitStatus(ctx, observed.checkout)
	if err != nil {
		return staleBoundary("Git status observation changed: " + err.Error())
	}
	if !sameStatus(status, observed.status) {
		return staleBoundary("Git status, branch, upstream, ahead, behind, dirty, or conflict evidence changed")
	}
	head, err := s.gitRun(ctx, observed.checkout, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != observed.head {
		return staleBoundary("checkout HEAD changed")
	}
	baseOID, baseErr := s.resolveRefOID(ctx, observed.repoPath, observed.task.Base)
	upstreamOID, upstreamErr := s.resolveRefOID(ctx, observed.repoPath, status.Upstream)
	if errorString(baseErr) != errorString(observed.baseOIDErr) || baseOID != observed.baseOID ||
		errorString(upstreamErr) != errorString(observed.upstreamOIDErr) || upstreamOID != observed.upstreamOID {
		return staleBoundary("base or upstream ref OID changed")
	}
	operation, active, err := s.gitInProgress(ctx, observed.checkout)
	if err != nil || operation != observed.operation || active != observed.inProgress {
		return staleBoundary("Git operation evidence changed")
	}
	return nil
}

func (s *lifecycleService) refreshCheckout(ctx context.Context, observed *lifecycleObservation, expectedBranch string) error {
	registered, err := s.resolveWorktree(ctx, observed.repoPath, observed.checkout)
	if err != nil {
		return err
	}
	if registered.Path != observed.checkout || registered.GitCommonDir != observed.gitCommonDir {
		return errors.New("checkout path or Git common directory changed")
	}
	if observed.mode == task.ModeWorktree && !registered.IsLinkedWorktree() {
		return errors.New("worktree task no longer resolves to a linked checkout")
	}
	if observed.mode != task.ModeWorktree && (!registered.Worktree.Main || registered.Worktree.Bare) {
		return errors.New("canonical task no longer resolves to the main checkout")
	}
	status, err := s.gitStatus(ctx, observed.checkout)
	if err != nil {
		return err
	}
	if status.Detached || expectedBranch != "" && status.Branch != expectedBranch || registered.Worktree.Branch != status.Branch {
		return fmt.Errorf("checkout branch changed: registry=%q status=%q expected=%q", registered.Worktree.Branch, status.Branch, expectedBranch)
	}
	head, err := s.gitRun(ctx, observed.checkout, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	head = strings.TrimSpace(head)
	if registered.Worktree.Head != head {
		return fmt.Errorf("worktree registry HEAD %q differs from checkout HEAD %q", registered.Worktree.Head, head)
	}
	baseOID, baseOIDErr := s.resolveRefOID(ctx, observed.repoPath, observed.task.Base)
	upstreamOID, upstreamOIDErr := s.resolveRefOID(ctx, observed.repoPath, status.Upstream)
	operation, active, operationErr := s.gitInProgress(ctx, observed.checkout)
	if operationErr != nil {
		return operationErr
	}
	observed.worktree = registered
	observed.status = status
	observed.statusErr = nil
	observed.head = head
	observed.headErr = nil
	observed.baseOID = baseOID
	observed.baseOIDErr = baseOIDErr
	observed.upstreamOID = upstreamOID
	observed.upstreamOIDErr = upstreamOIDErr
	observed.operation = operation
	observed.inProgress = active
	observed.operationErr = nil
	return nil
}

func (s *lifecycleService) ensureParkBranchIdentity(ctx context.Context, observed *lifecycleObservation) error {
	if err := s.revalidateGitBaseline(ctx, *observed); err != nil {
		return err
	}
	if observed.status.Detached || observed.status.Branch != observed.task.Branch || observed.inProgress || observed.status.Conflicted > 0 {
		return staleBoundary("task branch, conflict, or Git operation evidence changed before push")
	}
	return nil
}

func (s *lifecycleService) ensureColdGitReady(ctx context.Context, observed *lifecycleObservation) error {
	if err := s.revalidateGitBaseline(ctx, *observed); err != nil {
		return err
	}
	if observed.status.Dirty() {
		return fmt.Errorf("cold parking requires a clean checkout: %s", observed.status.Breakdown())
	}
	if observed.status.Detached || observed.status.Branch != observed.task.Branch {
		return errors.New("cold parking checkout is no longer on the task branch")
	}
	if observed.inProgress || observed.status.Conflicted > 0 {
		return errors.New("cold parking refuses an active Git operation or unresolved conflict")
	}
	if !observed.status.Published() || observed.status.Ahead != 0 {
		return fmt.Errorf("cold parking requires a published ahead=0 branch; status is %s", observed.status.Summary())
	}
	return nil
}

func (s *lifecycleService) revalidateColdArtifact(ctx context.Context, observed lifecycleObservation) error {
	if observed.mode != task.ModeWorktree {
		return nil
	}
	fresh, err := s.inspectArtifacts(ctx, s.artifacts, observed.checkout)
	if err != nil {
		return err
	}
	if !fresh.Ready() {
		return errors.New("artifact readiness changed to blocked")
	}
	if artifactAuthority(fresh, nil) != artifactAuthority(observed.artifact, observed.artifactErr) {
		return staleBoundary("artifact intent or receipt authority changed")
	}
	return nil
}

func (s *lifecycleService) revalidateCleanupBaseline(ctx context.Context, observed lifecycleObservation, closeUnknown, assumeNoRuntime bool, timeout time.Duration) error {
	options := observed.cleanupOptions(closeUnknown, assumeNoRuntime, timeout)
	fresh, err := s.inspectCleanup(ctx, observed.runtime, observed.checkout, options)
	if err != nil {
		return err
	}
	if cleanupAuthority(fresh, nil) != cleanupAuthority(observed.cleanup, observed.cleanupErr) {
		return staleBoundary("runtime occupancy, caller, agent, or session evidence changed")
	}
	return nil
}

func (s *lifecycleService) ensureRuntimeReleased(ctx context.Context, observed *lifecycleObservation, closeUnknown, assumeNoRuntime bool, timeout time.Duration) error {
	fresh, err := s.inspectCleanup(ctx, observed.runtime, observed.checkout,
		observed.cleanupOptions(closeUnknown, assumeNoRuntime, timeout))
	if err != nil {
		return err
	}
	if !fresh.Ready() || fresh.CallerContained || len(fresh.Sessions) > 0 {
		return errors.New("runtime coverage reappeared after closure")
	}
	observed.cleanup = fresh
	observed.cleanupErr = nil
	return nil
}

func (s *lifecycleService) ensureColdRemovalBoundary(ctx context.Context, observed *lifecycleObservation, options ParkColdOptions, runtimeWasClosed bool) error {
	if err := s.ensureColdGitReady(ctx, observed); err != nil {
		return err
	}
	if err := s.revalidateColdArtifact(ctx, *observed); err != nil {
		return err
	}
	fresh, err := s.inspectCleanup(ctx, observed.runtime, observed.checkout,
		observed.cleanupOptions(options.CloseUnknown, options.AssumeNoRuntime, options.Timeout))
	if err != nil {
		return err
	}
	if !fresh.Ready() || fresh.CallerContained || len(fresh.Sessions) > 0 {
		return errors.New("cleanup occupancy is no longer empty and safe")
	}
	if !runtimeWasClosed && cleanupAuthority(fresh, nil) != cleanupAuthority(observed.cleanup, observed.cleanupErr) {
		return staleBoundary("runtime occupancy changed before cleanup")
	}
	observed.cleanup = fresh
	observed.cleanupErr = nil
	return nil
}

func (s *lifecycleService) revalidateResumeCheckout(ctx context.Context, observed *lifecycleObservation) error {
	expected := observed.task.Branch
	if err := s.refreshCheckout(ctx, observed, expected); err != nil {
		return staleBoundary("resume checkout identity changed: " + err.Error())
	}
	return nil
}

func (s *lifecycleService) revalidateResumeOccupancy(ctx context.Context, observed *lifecycleObservation, compareAuthority bool) error {
	fresh, err := s.inspectOccupancy(ctx, observed.runtime, observed.checkout, runtime.OccupancyOptions{
		Profile:           runtime.OccupancyStrict,
		CallerWorkspaceID: s.callerWorkspace,
		CallerPaneID:      s.callerPane,
	})
	if occupancyErr := s.writerOccupancyError(fresh, err); occupancyErr != nil {
		return occupancyErr
	}
	if compareAuthority && occupancyAuthority(fresh, nil) != occupancyAuthority(observed.occupancy, observed.occupancyErr) {
		return staleBoundary("runtime session, caller, or recognized-agent occupancy changed")
	}
	observed.occupancy = fresh
	observed.occupancyErr = nil
	observed.savedRuntimeLive = savedHandleLive(fresh, observed.task.RuntimeHandle)
	return nil
}

func (s *lifecycleService) ensureBranchSwitchBoundary(ctx context.Context, observed *lifecycleObservation) error {
	if err := s.revalidateGitBaseline(ctx, *observed); err != nil {
		return err
	}
	if observed.status.Dirty() || observed.status.Detached || observed.inProgress || observed.status.Conflicted > 0 {
		return errors.New("canonical checkout is no longer clean and switchable")
	}
	exists, err := s.gitRefState(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	if err != nil {
		return fmt.Errorf("observe local task branch before switch: %w", err)
	}
	if !exists {
		return staleBoundary("local task branch disappeared before switch")
	}
	return nil
}

func (s *lifecycleService) revalidateResumeBranchRefs(ctx context.Context, observed *lifecycleObservation, allowRemoteChange bool) error {
	localExists, localErr := s.gitRefState(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	localOID := ""
	if localErr != nil {
		return fmt.Errorf("observe local task branch before reconstruction: %w", localErr)
	}
	if localExists {
		localOID, localErr = s.resolveRefOID(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	}
	if localExists != observed.localBranchExists || localOID != observed.localBranchOID ||
		errorString(localErr) != errorString(observed.localBranchOIDErr) {
		return staleBoundary("local task branch OID changed before worktree reconstruction")
	}
	if allowRemoteChange {
		return nil
	}
	remoteExists, remoteErr := s.gitRefState(ctx, observed.repoPath, observed.remoteBranch)
	remoteOID := ""
	if remoteErr != nil {
		return fmt.Errorf("observe published task branch before reconstruction: %w", remoteErr)
	}
	if remoteExists {
		remoteOID, remoteErr = s.resolveRefOID(ctx, observed.repoPath, observed.remoteBranch)
	}
	if remoteExists != observed.remoteBranchExists || remoteOID != observed.remoteBranchOID ||
		errorString(remoteErr) != errorString(observed.remoteBranchOIDErr) {
		return staleBoundary("remote task branch OID changed before worktree reconstruction")
	}
	return nil
}

func (s *lifecycleService) branchCheckoutCount(ctx context.Context, observed lifecycleObservation) (int, error) {
	worktrees, err := s.gitWorktrees(ctx, observed.repoPath)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, worktree := range worktrees {
		if worktree.Branch == observed.task.Branch {
			count++
		}
	}
	return count, nil
}

func resumeExpectedBranchBeforeSwitch(observed lifecycleObservation) string {
	if observed.mode == task.ModeBranch && observed.statusErr == nil {
		return observed.status.Branch
	}
	return observed.task.Branch
}
