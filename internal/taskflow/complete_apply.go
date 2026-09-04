package taskflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (e *executionState) applyCompleteDirect(ctx context.Context) (Result, error) {
	observed := &e.observed
	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCommitAll, EffectDiscardAll:
			if err := e.applyCompletionDirtyEffect(ctx, effect); err != nil {
				return e.fail(err, "the task checkout and lifecycle state were retained; inspect the content finalization before retrying")
			}
		case EffectPushBranch:
			if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
				return e.fail(err, "refresh direct completion before pushing")
			}
			err := e.run(effect, func() (string, error) {
				_, pushErr := e.service.gitRun(ctx, observed.checkout, "push", "--set-upstream", "origin", observed.task.Branch)
				if pushErr != nil {
					return "", fmt.Errorf("push %s: %w", observed.task.Branch, pushErr)
				}
				return "pushed origin/" + observed.task.Branch, nil
			})
			if err != nil {
				return e.fail(err, "the checkout and task remain active; resolve the push and retry")
			}
			if err := e.service.refreshCompletionCheckout(ctx, observed, observed.task.Branch, false); err != nil {
				return e.fail(fmt.Errorf("refresh after direct push: %w", err), "inspect the pushed branch before retrying")
			}
		case EffectUpdateTask:
			if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
				return e.fail(err, "refresh direct completion before recording DONE")
			}
			if err := e.writeCompletionDone(effect); err != nil {
				return e.fail(err, "Git effects may already be complete; reload the task and retry the idempotent DONE write")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected CompleteDirect effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (e *executionState) applyCompleteFF(ctx context.Context) (Result, error) {
	options := e.plan.Request.Options.(CompleteFFOptions)
	observed := &e.observed
	integrated := effectiveCompletionRelation(*observed, options.Dirty, options.CommitMessage).Contained()
	integrationTouched := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCommitAll, EffectDiscardAll:
			if err := e.applyCompletionDirtyEffect(ctx, effect); err != nil {
				return e.fail(err, "the task remains active; inspect content finalization and refresh fast-forward completion")
			}
			if observed.mode == task.ModeBranch {
				syncBranchIntegration(observed)
			}
		case EffectDiscardTarget:
			fresh, inspectErr := e.service.inspectIntegrationTarget(ctx, observed, false)
			if inspectErr != nil {
				return e.fail(inspectErr, "refresh the canonical integration checkout before discarding")
			}
			if integrationAuthority(fresh) != integrationAuthority(observed.integration) {
				return e.fail(staleBoundary("canonical integration checkout changed before discard"),
					"review its current paths and rerun dev done")
			}
			beforeHead := fresh.head
			err := e.run(effect, func() (string, error) {
				if discardErr := e.service.discardAll(ctx, observed.repoPath); discardErr != nil {
					return "canonical discard may have partially changed checkout content",
						fmt.Errorf("discard canonical integration checkout changes: %w", discardErr)
				}
				return "discarded canonical integration checkout changes", nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "the task branch remains intact; inspect canonical checkout content before retrying")
			}
			if err := e.service.refreshIntegrationTarget(ctx, observed, observed.integration.status.Branch); err != nil {
				return e.fail(fmt.Errorf("verify canonical checkout after discard: %w", err),
					"inspect the canonical checkout before retrying integration")
			}
			if observed.integration.head != beforeHead {
				return e.fail(staleBoundary("canonical discard changed committed HEAD"),
					"inspect the canonical branch before retrying")
			}
		case EffectRebaseBranch:
			if err := e.service.revalidateFFBeforeIntegration(ctx, observed, integrationTouched); err != nil {
				return e.fail(err, "refresh branch and canonical checkout authority before rebasing")
			}
			err := e.run(effect, func() (string, error) {
				_, rebaseErr := e.service.gitRun(ctx, observed.checkout, "rebase", observed.completionBaseRef)
				if rebaseErr != nil {
					return "rebase may have left an operation in progress", fmt.Errorf("rebase %s onto %s: %w",
						observed.task.Branch, observed.completionBaseRef, rebaseErr)
				}
				return "rebased " + observed.task.Branch + " onto " + observed.completionBaseRef, nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "resolve or abort the rebase in the retained checkout; the task state remains unchanged")
			}
			if err := e.service.refreshCompletionCheckout(ctx, observed, observed.task.Branch, false); err != nil {
				return e.fail(fmt.Errorf("refresh after rebase: %w", err), "inspect the rebased task branch before retrying")
			}
			if observed.finish.Status.Dirty() || observed.finish.Status.Conflicted > 0 || observed.finish.Relation.BaseOnly != 0 {
				return e.fail(errors.New("rebase did not produce a clean branch based on the exact base"),
					"inspect the retained branch and rerun completion from a fresh plan")
			}
			if observed.mode == task.ModeBranch {
				syncBranchIntegration(observed)
			}
		case EffectSwitchBase:
			if err := e.service.revalidateFFBeforeIntegration(ctx, observed, integrationTouched); err != nil {
				return e.fail(err, "refresh both checkouts before switching the canonical main checkout")
			}
			err := e.run(effect, func() (string, error) {
				_, switchErr := e.service.gitRun(ctx, observed.repoPath, "switch", observed.completionBaseRef)
				if switchErr != nil {
					return "", fmt.Errorf("switch canonical checkout to %s: %w", observed.completionBaseRef, switchErr)
				}
				return "switched canonical checkout to " + observed.completionBaseRef, nil
			})
			if err != nil {
				return e.fail(err, "the task branch is retained; inspect the canonical checkout and retry")
			}
			integrationTouched = true
			if err := e.service.refreshIntegrationTarget(ctx, observed, observed.completionBaseRef); err != nil {
				return e.fail(fmt.Errorf("verify canonical base switch: %w", err), "inspect the canonical checkout before merging")
			}
			if observed.integration.head != observed.completionBaseOID {
				return e.fail(staleBoundary("canonical base HEAD differs from the approved base OID after switch"),
					"refresh fast-forward completion")
			}
			if observed.mode == task.ModeBranch {
				e.service.syncPrimaryFromIntegration(ctx, observed)
			}
			if err := e.service.refreshCompletionAnalysis(ctx, observed, false); err != nil {
				return e.fail(fmt.Errorf("refresh branch relation after base switch: %w", err), "refresh fast-forward completion")
			}
		case EffectMergeFF:
			if err := e.service.revalidateFFMergeBoundary(ctx, observed); err != nil {
				return e.fail(err, "refresh exact branch and base authority before the fast-forward merge")
			}
			err := e.run(effect, func() (string, error) {
				_, mergeErr := e.service.gitRun(ctx, observed.repoPath, "merge", "--ff-only", observed.task.Branch)
				if mergeErr != nil {
					return "", fmt.Errorf("fast-forward %s into %s: %w",
						observed.task.Branch, observed.completionBaseRef, mergeErr)
				}
				return "fast-forwarded " + observed.completionBaseRef + " to " + observed.task.Branch, nil
			})
			if err != nil {
				return e.fail(err, "the canonical checkout remains on the explicit base and the task branch is retained; refresh and retry")
			}
			if err := e.service.refreshIntegrationTarget(ctx, observed, observed.completionBaseRef); err != nil {
				return e.fail(fmt.Errorf("verify fast-forward merge: %w", err), "inspect the integrated base before recording DONE")
			}
			branchOID, err := e.service.resolveRefOID(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
			if err != nil {
				return e.fail(fmt.Errorf("resolve task branch after fast-forward: %w", err), "restore the retained branch before recording DONE")
			}
			if observed.integration.head != branchOID {
				return e.fail(staleBoundary("fast-forward base HEAD does not equal the exact task branch OID"),
					"inspect concurrent branch movement and refresh completion")
			}
			observed.completionBaseOID = observed.integration.head
			observed.completionBranchOID = branchOID
			if observed.mode == task.ModeBranch {
				e.service.syncPrimaryFromIntegration(ctx, observed)
			}
			if err := e.service.refreshCompletionAnalysis(ctx, observed, true); err != nil {
				return e.fail(fmt.Errorf("refresh after fast-forward merge: %w", err), "inspect the integrated refs before recording DONE")
			}
			if !observed.finish.Relation.Contained() {
				return e.fail(errors.New("task branch is not contained after fast-forward merge"), "refresh and inspect the retained refs")
			}
			integrated = true
		case EffectPushBase:
			if !integrated {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "base push reached before integration"})
			}
			if err := e.service.revalidateFFCompleted(ctx, observed, integrationTouched); err != nil {
				return e.fail(err, "refresh integrated authority before the optional base push")
			}
			pushedBase := false
			e.runWarning(effect, func() (string, error) {
				_, pushErr := e.service.gitRun(ctx, observed.repoPath, "push", "origin", observed.completionBaseRef)
				if pushErr != nil {
					return "", fmt.Errorf("push %s: %w", observed.completionBaseRef, pushErr)
				}
				pushedBase = true
				return "pushed origin/" + observed.completionBaseRef, nil
			}, "optional base push failed")
			if pushedBase && integrationTouched {
				if err := e.service.refreshIntegrationTarget(ctx, observed, observed.completionBaseRef); err != nil {
					return e.fail(fmt.Errorf("refresh after optional base push: %w", err), "inspect the pushed base before recording DONE")
				}
				if observed.mode == task.ModeBranch {
					e.service.syncPrimaryFromIntegration(ctx, observed)
					if err := e.service.refreshCompletionAnalysis(ctx, observed, false); err != nil {
						return e.fail(fmt.Errorf("refresh branch relation after optional base push: %w", err), "inspect the pushed base")
					}
				}
			}
		case EffectUpdateTask:
			if !integrated {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "DONE update reached before integration proof"})
			}
			if err := e.service.revalidateFFCompleted(ctx, observed, integrationTouched); err != nil {
				return e.fail(err, "refresh exact containment before recording DONE")
			}
			if err := e.writeCompletionDone(effect); err != nil {
				return e.fail(err, "integration is retained; reload the task and retry the now-contained DONE write")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected CompleteFF effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (e *executionState) applyReviewHandoff(ctx context.Context) (Result, error) {
	options := e.plan.Request.Options.(ReviewHandoffOptions)
	observed := &e.observed
	pushed := false
	created := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCommitAll, EffectDiscardAll:
			if err := e.applyCompletionDirtyEffect(ctx, effect); err != nil {
				return e.fail(err, "the task remains active; inspect content finalization and refresh review handoff")
			}
		case EffectPushBranch:
			if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
				return e.fail(err, "refresh review handoff before publishing the branch")
			}
			if err := e.service.revalidateReviewRemote(ctx, observed); err != nil {
				return e.fail(err, "origin changed; refresh review handoff before publishing")
			}
			err := e.run(effect, func() (string, error) {
				_, pushErr := e.service.gitRun(ctx, observed.checkout, "push", "--set-upstream", "origin", observed.task.Branch)
				if pushErr != nil {
					return "", fmt.Errorf("push %s for review: %w", observed.task.Branch, pushErr)
				}
				return "published origin/" + observed.task.Branch, nil
			})
			if err != nil {
				return e.fail(err, "the task state and resources remain unchanged; resolve the push and retry")
			}
			pushed = true
			if err := e.service.refreshCompletionCheckout(ctx, observed, observed.task.Branch, false); err != nil {
				return e.fail(fmt.Errorf("refresh after review push: %w", err), "inspect the published branch before retrying")
			}
		case EffectCreateReview:
			if !pushed {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "review creation reached before branch publication"})
			}
			if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
				return e.fail(err, "refresh checkout authority after the successful push before creating review")
			}
			if err := e.service.revalidateReviewRemote(ctx, observed); err != nil {
				return e.fail(err, "origin changed after publication; do not create review against a different repository")
			}
			if observed.reviewProvider == nil || !observed.reviewAvailable || !observed.reviewProvider.Available() {
				err := errors.New("approved forge provider became unavailable after branch publication")
				stepErr := e.run(effect, func() (string, error) { return "", err })
				return e.fail(stepErr, "the branch is published and task state is unchanged; open the review manually or retry provider creation")
			}
			err := e.run(effect, func() (string, error) {
				url, createErr := e.service.createPR(ctx, observed.reviewProvider, observed.checkout,
					completionReviewRequest(*observed, options))
				if createErr != nil {
					return "", fmt.Errorf("create %s review: %w", observed.reviewKind, createErr)
				}
				created = true
				if strings.TrimSpace(url) != "" {
					e.handoff = &Handoff{Kind: HandoffURL, URL: strings.TrimSpace(url), Label: observed.task.Title()}
					return "created review at " + strings.TrimSpace(url), nil
				}
				return "provider reported successful review creation without a URL", nil
			})
			if err != nil {
				return e.fail(err, "the branch is published and task state is unchanged; retry review creation or open it manually")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected ReviewHandoff effect " + string(effect.Code)})
		}
	}
	if !pushed {
		return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "review handoff completed without publishing the branch"})
	}
	if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
		return e.fail(err, "the branch is published; refresh local safety evidence before reporting review readiness")
	}
	if !created {
		if warning := reviewProviderWarning(*observed); warning != "" {
			e.warnings = append(e.warnings, warning)
		}
	}
	e.milestone = MilestoneReviewReady
	e.snapshot = "task:" + e.revision
	return e.result(), nil
}

func (e *executionState) applyVerifyMerged(ctx context.Context) (Result, error) {
	observed := &e.observed
	verified := false
	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCommitAll, EffectDiscardAll:
			if err := e.applyCompletionDirtyEffect(ctx, effect); err != nil {
				return e.fail(err, "the task remains active; inspect content finalization and refresh merge verification")
			}
			if observed.proofRef == observed.task.Branch {
				observed.proofOID = observed.completionBranchOID
			}
		case EffectVerifyAncestry:
			if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
				return e.fail(err, "refresh checkout authority before verifying merge ancestry")
			}
			err := e.run(effect, func() (string, error) {
				contained, proofErr := e.service.revalidateMergeProof(ctx, observed)
				if proofErr != nil {
					return "", proofErr
				}
				if !contained {
					return "", fmt.Errorf("cannot verify %s (%s) is an ancestor of %s (%s)",
						observed.proofRef, observed.proofOID, observed.completionBaseRef, observed.completionBaseOID)
				}
				return fmt.Sprintf("verified %s (%s) is an ancestor of %s (%s)",
					observed.proofRef, observed.proofOID, observed.completionBaseRef, observed.completionBaseOID), nil
			})
			if err != nil {
				return e.fail(err, "no remote freshness was assumed; refresh or select the exact ref and retry while the task remains active")
			}
			verified = true
		case EffectPushBase:
			if !verified {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "base push reached before merge proof"})
			}
			if err := e.service.revalidateVerifiedCompletion(ctx, observed); err != nil {
				return e.fail(err, "refresh exact merge proof before the optional base push")
			}
			e.runWarning(effect, func() (string, error) {
				_, pushErr := e.service.gitRun(ctx, observed.repoPath, "push", "origin", observed.completionBaseRef)
				if pushErr != nil {
					return "", fmt.Errorf("push %s: %w", observed.completionBaseRef, pushErr)
				}
				return "pushed origin/" + observed.completionBaseRef, nil
			}, "optional base push failed")
		case EffectUpdateTask:
			if !verified {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "DONE update reached before merge proof"})
			}
			if err := e.service.revalidateVerifiedCompletion(ctx, observed); err != nil {
				return e.fail(err, "merge proof changed before recording DONE")
			}
			if err := e.writeCompletionDone(effect); err != nil {
				return e.fail(err, "the verified refs and resources remain; reload the task and retry the DONE write")
			}
		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected VerifyMerged effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (e *executionState) applyCompletionDirtyEffect(ctx context.Context, effect Effect) error {
	observed := &e.observed
	if err := e.service.revalidateCompletionSafety(ctx, observed); err != nil {
		return err
	}
	before := observed.finish
	beforeBranchOID := observed.completionBranchOID
	err := e.run(effect, func() (string, error) {
		switch effect.Code {
		case EffectCommitAll:
			message := effect.Details.Map()["message"]
			if commitErr := e.service.commitAll(ctx, observed.checkout, message); commitErr != nil {
				return "commit-all may have changed the index", fmt.Errorf("commit all checkout changes: %w", commitErr)
			}
			return "committed all checkout changes", nil
		case EffectDiscardAll:
			if discardErr := e.service.discardAll(ctx, observed.checkout); discardErr != nil {
				return "discard-all may have partially reset checkout content", fmt.Errorf("discard all checkout changes: %w", discardErr)
			}
			return "discarded all staged, unstaged, and non-ignored untracked changes", nil
		default:
			return "", &InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected dirty effect " + string(effect.Code)}
		}
	})
	if err != nil {
		e.partial = true
		return err
	}
	allowBaseMove := observed.mode == task.ModeDirect && effect.Code == EffectCommitAll
	if err := e.service.refreshCompletionCheckout(ctx, observed, observed.task.Branch, allowBaseMove); err != nil {
		return fmt.Errorf("refresh after %s: %w", effect.Code, err)
	}
	if observed.finish.Status.Dirty() || observed.finish.Status.Conflicted > 0 || observed.inProgress {
		return fmt.Errorf("checkout changed again after %s: %s", effect.Code, observed.finish.Status.Breakdown())
	}
	switch effect.Code {
	case EffectCommitAll:
		parent, parentErr := e.service.resolveRefOID(ctx, observed.repoPath, observed.completionBranchOID+"^")
		if parentErr != nil || parent != beforeBranchOID {
			return staleBoundary("commit-all did not create exactly one normal commit from the approved branch OID")
		}
		if observed.mode == task.ModeDirect {
			if observed.finish.Relation != (gitx.BranchRelation{}) {
				return staleBoundary("direct commit changed the self-relation unexpectedly")
			}
		} else if observed.finish.Relation.BaseOnly != before.Relation.BaseOnly ||
			observed.finish.Relation.BranchOnly != before.Relation.BranchOnly+1 {
			return staleBoundary("commit-all changed branch relation by more than the declared single commit")
		}
	case EffectDiscardAll:
		if observed.completionBranchOID != beforeBranchOID || observed.finish.Relation != before.Relation {
			return staleBoundary("discard-all unexpectedly changed committed branch identity or relation")
		}
	}
	if observed.proofRef == observed.task.Branch {
		observed.proofOID = observed.completionBranchOID
	} else if e.plan.Action == VerifyMerged && effect.Code == EffectCommitAll {
		return staleBoundary("dirty commit created a new task tip that is not represented by the approved merge proof")
	}
	return nil
}

func (e *executionState) writeCompletionDone(effect Effect) error {
	return e.run(effect, func() (string, error) {
		candidate := e.observed.task
		candidate.State = task.Done
		updated, updateErr := e.service.taskUpdate(e.tx, &candidate, e.revision)
		if updateErr != nil {
			return "", fmt.Errorf("write DONE task state: %w", updateErr)
		}
		if updated == nil {
			return "", errors.New("write DONE task state returned no revision")
		}
		e.snapshot = "task:" + updated.Revision
		e.observed.task = updated.Task
		e.milestone = MilestoneMerged
		return "recorded DONE at revision " + updated.Revision, nil
	})
}

func (s *lifecycleService) revalidateReviewRemote(ctx context.Context, observed *lifecycleObservation) error {
	remoteURL := strings.TrimSpace(s.gitRemote(ctx, observed.repoPath, "origin"))
	identityKind, repository := forge.IdentityFromURL(remoteURL)
	kind := s.detectForge(ctx, observed.repoPath)
	if identityKind != forge.Unknown {
		if kind != forge.Unknown && kind != identityKind {
			return staleBoundary("detected forge no longer matches origin repository identity")
		}
		kind = identityKind
	}
	if remoteURL != observed.reviewRemoteURL || kind != observed.reviewKind || repository != observed.reviewRepository {
		return staleBoundary("origin URL or forge repository identity changed after planning")
	}
	return nil
}

func (s *lifecycleService) revalidateCompletionSafety(ctx context.Context, observed *lifecycleObservation) error {
	if err := s.revalidateRepository(ctx, *observed); err != nil {
		return err
	}
	if err := s.revalidateGitBaseline(ctx, *observed); err != nil {
		return err
	}
	if observed.status.Detached || observed.status.Branch != observed.completionBranch ||
		observed.worktree.Worktree.Branch != observed.completionBranch {
		return staleBoundary(fmt.Sprintf("completion checkout branch changed: registry=%q status=%q expected=%q",
			observed.worktree.Worktree.Branch, observed.status.Branch, observed.completionBranch))
	}
	if observed.inProgress || observed.operationErr != nil || observed.status.Conflicted > 0 {
		return errors.New("completion checkout acquired a Git operation or conflict")
	}
	freshFinish, err := s.analyzeFinish(ctx, observed.checkout, observed.completionBaseRef, observed.task.Branch)
	if err != nil {
		return staleBoundary("finish analysis failed during revalidation: " + err.Error())
	}
	if finishAuthority(freshFinish, nil) != finishAuthority(observed.finish, observed.finishErr) {
		return staleBoundary("finish content fingerprint or branch relation changed")
	}
	baseOID, err := s.resolveRefOID(ctx, observed.repoPath, observed.completionBaseRef)
	if err != nil || baseOID != observed.completionBaseOID {
		return staleBoundary("completion base ref or OID changed")
	}
	if err := s.revalidateCompletionArtifacts(ctx, observed); err != nil {
		return err
	}
	if err := s.revalidateCompletionOccupancy(ctx, observed); err != nil {
		return err
	}
	return nil
}

func (s *lifecycleService) refreshCompletionCheckout(
	ctx context.Context,
	observed *lifecycleObservation,
	expectedBranch string,
	allowBaseMove bool,
) error {
	if err := s.refreshCheckout(ctx, observed, expectedBranch); err != nil {
		return err
	}
	observed.completionBranch = expectedBranch
	if err := s.refreshCompletionAnalysis(ctx, observed, allowBaseMove); err != nil {
		return err
	}
	branchOID, err := s.resolveRefOID(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	if err != nil {
		return err
	}
	observed.completionBranchOID = branchOID
	return nil
}

func (s *lifecycleService) refreshCompletionAnalysis(ctx context.Context, observed *lifecycleObservation, allowBaseMove bool) error {
	baseOID, err := s.resolveRefOID(ctx, observed.repoPath, observed.completionBaseRef)
	if err != nil {
		return err
	}
	if !allowBaseMove && baseOID != observed.completionBaseOID {
		return staleBoundary("completion base OID changed outside a declared integration effect")
	}
	analysis, err := s.analyzeFinish(ctx, observed.checkout, observed.completionBaseRef, observed.task.Branch)
	if err != nil {
		return err
	}
	observed.completionBaseOID = baseOID
	if observed.completionBaseRef == observed.task.Base {
		observed.baseOID = baseOID
		observed.baseOIDErr = nil
	}
	observed.finish = analysis
	observed.finishErr = nil
	return nil
}

func (s *lifecycleService) revalidateCompletionArtifacts(ctx context.Context, observed *lifecycleObservation) error {
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
	observed.artifact = fresh
	observed.artifactErr = nil
	return nil
}

func (s *lifecycleService) revalidateCompletionOccupancy(ctx context.Context, observed *lifecycleObservation) error {
	if observed.runtime == nil {
		return errors.New("completion runtime is unavailable")
	}
	if observed.runtime.Name() != "none" && !observed.runtime.Available() {
		return errors.New("completion runtime became unavailable")
	}
	fresh, err := s.inspectOccupancy(ctx, observed.runtime, observed.checkout, runtime.OccupancyOptions{
		Profile:           runtime.OccupancyStrict,
		CallerWorkspaceID: observed.taskflowCallerWorkspace,
		CallerPaneID:      observed.taskflowCallerPane,
	})
	if occupancyErr := s.writerOccupancyError(fresh, err); occupancyErr != nil {
		return occupancyErr
	}
	observed.occupancy = fresh
	observed.occupancyErr = nil
	return nil
}

func (s *lifecycleService) revalidateFFBeforeIntegration(
	ctx context.Context,
	observed *lifecycleObservation,
	integrationTouched bool,
) error {
	if err := s.revalidateCompletionSafety(ctx, observed); err != nil {
		return err
	}
	if observed.mode == task.ModeWorktree {
		return s.revalidateIntegrationTarget(ctx, observed, !integrationTouched, "")
	}
	return nil
}

func (s *lifecycleService) revalidateFFMergeBoundary(ctx context.Context, observed *lifecycleObservation) error {
	if observed.mode == task.ModeWorktree {
		if err := s.revalidateCompletionSafety(ctx, observed); err != nil {
			return err
		}
	}
	if err := s.revalidateIntegrationTarget(ctx, observed, true, observed.completionBaseRef); err != nil {
		return err
	}
	branchOID, err := s.resolveRefOID(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	if err != nil {
		return err
	}
	if branchOID != observed.completionBranchOID {
		return staleBoundary("task branch OID changed before fast-forward merge")
	}
	baseOID, err := s.resolveRefOID(ctx, observed.repoPath, observed.completionBaseRef)
	if err != nil || baseOID != observed.completionBaseOID {
		return staleBoundary("base OID changed before fast-forward merge")
	}
	analysis, err := s.analyzeFinish(ctx, observed.checkout, observed.completionBaseRef, observed.task.Branch)
	if err != nil {
		return err
	}
	if analysis.Relation.BaseOnly != 0 || analysis.Status.Dirty() || analysis.Status.Conflicted > 0 {
		return errors.New("task branch is not a clean fast-forward descendant of the explicit base")
	}
	return nil
}

func (s *lifecycleService) revalidateFFCompleted(
	ctx context.Context,
	observed *lifecycleObservation,
	integrationTouched bool,
) error {
	if err := s.revalidateCompletionSafety(ctx, observed); err != nil {
		return err
	}
	if observed.mode == task.ModeWorktree && integrationTouched {
		if err := s.revalidateIntegrationTarget(ctx, observed, true, observed.completionBaseRef); err != nil {
			return err
		}
	}
	branchOID, err := s.resolveRefOID(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	if err != nil || branchOID != observed.completionBranchOID {
		return staleBoundary("task branch OID changed after integration")
	}
	baseOID, err := s.resolveRefOID(ctx, observed.repoPath, observed.completionBaseRef)
	if err != nil || baseOID != observed.completionBaseOID {
		return staleBoundary("base OID changed after integration")
	}
	contained, err := s.isAncestor(ctx, observed.repoPath, branchOID, baseOID)
	if err != nil {
		return err
	}
	if !contained {
		return errors.New("task branch is no longer contained in the exact base")
	}
	return nil
}

func (s *lifecycleService) revalidateIntegrationTarget(
	ctx context.Context,
	observed *lifecycleObservation,
	compareAuthority bool,
	expectedBranch string,
) error {
	fresh, err := s.inspectIntegrationTarget(ctx, observed, true)
	if err != nil {
		return err
	}
	if compareAuthority && integrationAuthority(fresh) != integrationAuthority(observed.integration) {
		return staleBoundary("canonical integration checkout or occupancy changed")
	}
	if expectedBranch != "" && (fresh.status.Detached || fresh.status.Branch != expectedBranch ||
		fresh.worktree.Worktree.Branch != expectedBranch) {
		return staleBoundary(fmt.Sprintf("canonical integration checkout is on %q/%q, expected %q",
			fresh.worktree.Worktree.Branch, fresh.status.Branch, expectedBranch))
	}
	observed.integration = fresh
	return nil
}

func (s *lifecycleService) inspectIntegrationTarget(
	ctx context.Context,
	observed *lifecycleObservation,
	requireClean bool,
) (completionIntegrationObservation, error) {
	var fresh completionIntegrationObservation
	registered, err := s.resolveWorktree(ctx, observed.repoPath, observed.repoPath)
	fresh.worktree, fresh.worktreeErr = registered, err
	if err != nil {
		return fresh, err
	}
	fresh.worktreeFound = true
	if registered.Path != observed.repoPath || registered.GitCommonDir != observed.gitCommonDir ||
		!registered.Worktree.Main || registered.Worktree.Bare {
		return fresh, errors.New("integration target is not the exact canonical main checkout")
	}
	fresh.status, fresh.statusErr = s.gitStatus(ctx, observed.repoPath)
	if fresh.statusErr != nil {
		return fresh, fresh.statusErr
	}
	if fresh.status.Conflicted > 0 || requireClean && fresh.status.Dirty() {
		return fresh, fmt.Errorf("canonical integration checkout is not clean: %s", fresh.status.Breakdown())
	}
	fresh.head, fresh.headErr = s.gitRun(ctx, observed.repoPath, "rev-parse", "HEAD")
	if fresh.headErr != nil {
		return fresh, fresh.headErr
	}
	fresh.head = strings.TrimSpace(fresh.head)
	if registered.Worktree.Head != fresh.head {
		return fresh, errors.New("canonical worktree registry HEAD differs from checkout HEAD")
	}
	fresh.operation, fresh.inProgress, fresh.operationErr = s.gitInProgress(ctx, observed.repoPath)
	if fresh.operationErr != nil {
		return fresh, fresh.operationErr
	}
	if fresh.inProgress {
		return fresh, errors.New("canonical integration checkout has Git operation " + fresh.operation + " in progress")
	}
	fresh.occupancy, fresh.occupancyErr = s.inspectOccupancy(ctx, observed.runtime, observed.repoPath, runtime.OccupancyOptions{
		Profile:           runtime.OccupancyStrict,
		CallerWorkspaceID: observed.taskflowCallerWorkspace,
		CallerPaneID:      observed.taskflowCallerPane,
	})
	if occupancyErr := s.writerOccupancyError(fresh.occupancy, fresh.occupancyErr); occupancyErr != nil {
		return fresh, occupancyErr
	}
	return fresh, nil
}

func (s *lifecycleService) refreshIntegrationTarget(
	ctx context.Context,
	observed *lifecycleObservation,
	expectedBranch string,
) error {
	fresh, err := s.inspectIntegrationTarget(ctx, observed, true)
	if err != nil {
		return err
	}
	if fresh.status.Detached || fresh.status.Branch != expectedBranch || fresh.worktree.Worktree.Branch != expectedBranch {
		return fmt.Errorf("canonical integration checkout branch is registry=%q status=%q expected=%q",
			fresh.worktree.Worktree.Branch, fresh.status.Branch, expectedBranch)
	}
	observed.integration = fresh
	return nil
}

func syncBranchIntegration(observed *lifecycleObservation) {
	observed.integration = completionIntegrationObservation{
		worktree: observed.worktree, worktreeFound: observed.worktreeFound, worktreeErr: observed.worktreeErr,
		status: observed.status, statusErr: observed.statusErr,
		head: observed.head, headErr: observed.headErr,
		operation: observed.operation, inProgress: observed.inProgress, operationErr: observed.operationErr,
		occupancy: observed.occupancy, occupancyErr: observed.occupancyErr,
	}
}

func (s *lifecycleService) syncPrimaryFromIntegration(ctx context.Context, observed *lifecycleObservation) {
	observed.worktree = observed.integration.worktree
	observed.worktreeFound = observed.integration.worktreeFound
	observed.worktreeErr = observed.integration.worktreeErr
	observed.checkout = observed.integration.worktree.Path
	observed.status = observed.integration.status
	observed.statusErr = observed.integration.statusErr
	observed.head = observed.integration.head
	observed.headErr = observed.integration.headErr
	observed.operation = observed.integration.operation
	observed.inProgress = observed.integration.inProgress
	observed.operationErr = observed.integration.operationErr
	observed.occupancy = observed.integration.occupancy
	observed.occupancyErr = observed.integration.occupancyErr
	observed.completionBranch = observed.integration.status.Branch
	observed.baseOID, observed.baseOIDErr = s.resolveRefOID(ctx, observed.repoPath, observed.task.Base)
	observed.upstreamOID, observed.upstreamOIDErr = s.resolveRefOID(ctx, observed.repoPath, observed.status.Upstream)
}

func (s *lifecycleService) revalidateMergeProof(
	ctx context.Context,
	observed *lifecycleObservation,
) (bool, error) {
	baseOID, err := s.resolveRefOID(ctx, observed.repoPath, observed.completionBaseRef)
	if err != nil {
		return false, err
	}
	if baseOID != observed.completionBaseOID {
		return false, staleBoundary("verification base ref moved from its approved OID")
	}
	proofOID, err := s.resolveRefOID(ctx, observed.repoPath, observed.proofRef)
	if err != nil {
		return false, err
	}
	if proofOID != observed.proofOID {
		return false, staleBoundary("verification proof ref moved from its expected OID")
	}
	contained, err := s.isAncestor(ctx, observed.repoPath, proofOID, baseOID)
	if err != nil {
		return false, err
	}
	observed.proofContained = contained
	observed.proofErr = nil
	return contained, nil
}

func (s *lifecycleService) revalidateVerifiedCompletion(ctx context.Context, observed *lifecycleObservation) error {
	if err := s.revalidateCompletionSafety(ctx, observed); err != nil {
		return err
	}
	contained, err := s.revalidateMergeProof(ctx, observed)
	if err != nil {
		return err
	}
	if !contained {
		return errors.New("exact merge proof is no longer contained in the selected base")
	}
	return nil
}
