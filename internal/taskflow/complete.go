package taskflow

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func isCompletionAction(action Action) bool {
	switch action {
	case CompleteDirect, CompleteFF, ReviewHandoff, VerifyMerged:
		return true
	default:
		return false
	}
}

func (s *lifecycleService) observeCompletionFacts(ctx context.Context, request Request, observed *lifecycleObservation) {
	observed.completionBranch = observed.task.Branch
	if observed.hasCheckout() && observed.headErr == nil {
		observed.completionBranchOID = observed.head
	}
	if observed.completionBaseRef != "" {
		observed.completionBaseOID, observed.completionBaseOIDErr = s.resolveRefOID(
			ctx, observed.repoPath, observed.completionBaseRef,
		)
	}
	if observed.hasCheckout() {
		observed.artifact, observed.artifactErr = s.inspectArtifacts(ctx, s.artifacts, observed.checkout)
		if observed.completionBaseRef != "" {
			observed.finish, observed.finishErr = s.analyzeFinish(
				ctx, observed.checkout, observed.completionBaseRef, observed.task.Branch,
			)
		}
	}

	switch request.Action {
	case CompleteFF:
		s.observeIntegrationFacts(ctx, observed)
	case ReviewHandoff:
		observed.reviewRemoteURL = strings.TrimSpace(s.gitRemote(ctx, observed.repoPath, "origin"))
		identityKind, identity := forge.IdentityFromURL(observed.reviewRemoteURL)
		observed.reviewKind = s.detectForge(ctx, observed.repoPath)
		if identityKind != forge.Unknown {
			if observed.reviewKind != forge.Unknown && observed.reviewKind != identityKind {
				observed.reviewResolveErr = fmt.Errorf("detected forge %s differs from origin identity %s", observed.reviewKind, identityKind)
			}
			observed.reviewKind = identityKind
			observed.reviewRepository = identity
		}
		if observed.reviewResolveErr == nil {
			observed.reviewProvider, observed.reviewResolveErr = s.resolveForge(observed.reviewKind)
		}
		if observed.reviewResolveErr == nil {
			switch {
			case observed.reviewProvider == nil:
				observed.reviewResolveErr = errors.New("forge resolver returned no provider")
			case observed.reviewProvider.Kind() != observed.reviewKind:
				observed.reviewResolveErr = fmt.Errorf("forge resolver returned %s for detected %s remote",
					observed.reviewProvider.Kind(), observed.reviewKind)
			default:
				observed.reviewAvailable = observed.reviewProvider.Available()
			}
		}
	case VerifyMerged:
		options := request.Options.(VerifyMergedOptions)
		observed.proofRef = options.SquashCommit
		if observed.proofRef == "" {
			observed.proofRef = observed.task.Branch
		}
		if observed.proofRef != "" {
			observed.proofOID, observed.proofOIDErr = s.resolveRefOID(ctx, observed.repoPath, observed.proofRef)
		}
		if observed.completionBaseOIDErr == nil && observed.completionBaseOID != "" &&
			observed.proofOIDErr == nil && observed.proofOID != "" {
			observed.proofContained, observed.proofErr = s.isAncestor(
				ctx, observed.repoPath, observed.proofOID, observed.completionBaseOID,
			)
		}
	}
}

func (s *lifecycleService) observeIntegrationFacts(ctx context.Context, observed *lifecycleObservation) {
	if observed.mode == task.ModeBranch {
		observed.integration = completionIntegrationObservation{
			worktree: observed.worktree, worktreeFound: observed.worktreeFound, worktreeErr: observed.worktreeErr,
			status: observed.status, statusErr: observed.statusErr,
			head: observed.head, headErr: observed.headErr,
			operation: observed.operation, inProgress: observed.inProgress, operationErr: observed.operationErr,
		}
		return
	}

	registered, err := s.resolveWorktree(ctx, observed.repoPath, observed.repoPath)
	observed.integration.worktree, observed.integration.worktreeErr = registered, err
	if err != nil {
		return
	}
	observed.integration.worktreeFound = true
	observed.integration.status, observed.integration.statusErr = s.gitStatus(ctx, observed.repoPath)
	if observed.integration.statusErr != nil {
		return
	}
	observed.integration.head, observed.integration.headErr = s.gitRun(ctx, observed.repoPath, "rev-parse", "HEAD")
	observed.integration.head = strings.TrimSpace(observed.integration.head)
	observed.integration.operation, observed.integration.inProgress, observed.integration.operationErr =
		s.gitInProgress(ctx, observed.repoPath)
	if observed.integration.status.Dirty() {
		observed.integration.stashSafety, observed.integration.stashSafetyErr =
			s.inspectStash(ctx, observed.repoPath)
	}
}

func (s *lifecycleService) completeDirectSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(CompleteDirectOptions)
	conditions := s.completionConditions(observed, options.Dirty, options.CommitMessage)
	conditions = append(conditions, condition(
		ConditionBranchRelation, VerdictMet, RequirementAdvisory,
		"direct mode records completion on "+observed.task.Branch+" without integrating another branch", "",
	))

	effects := s.completionDirtyEffects(observed, options.Dirty, options.CommitMessage)
	if completionDirtyExecutable(observed, options.Dirty, options.CommitMessage) {
		if options.Push {
			effects = append(effects, NewEffect(
				EffectPushBranch, "push the directly completed branch", observed.task.Branch, false, true,
				map[string]string{"remote": "origin", "checkout": observed.checkout},
			))
		}
		effects = append(effects, completionTaskUpdateEffect(observed))
	}
	return s.completionPlanSpec(
		request, observed, conditions, effects,
		completionConfirmation(observed, options.Dirty, "Complete this direct task?"),
		completionFallback(observed.task.ID, "", options.Dirty, options.CommitMessage, options.Push, "", ""),
		"Complete "+observed.task.Title()+" directly on "+observed.task.Branch,
	)
}

func (s *lifecycleService) completeFFSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(CompleteFFOptions)
	conditions := s.completionConditions(observed, options.Dirty, options.CommitMessage)
	relation := effectiveCompletionRelation(observed, options.Dirty, options.CommitMessage)
	if !relation.Contained() {
		conditions = append(conditions, completionIntegrationCondition(
			observed, options.Dirty, options.CommitMessage, options.IntegrationTargetPolicy))
		if observed.mode == task.ModeWorktree {
			conditions = append(conditions, s.completionIntegrationOccupancyCondition(observed))
		}
	} else if observed.mode == task.ModeWorktree {
		conditions = append(conditions, completionContainedIntegrationCondition(observed))
	}

	relationEvidence := fmt.Sprintf("%s is behind %d and ahead %d relative to %s",
		observed.task.Branch, relation.BaseOnly, relation.BranchOnly, observed.completionBaseRef)
	if relation.Contained() {
		relationEvidence = observed.task.Branch + " is already contained in " + observed.completionBaseRef
	}
	conditions = append(conditions, condition(
		ConditionBranchRelation, VerdictMet, RequirementRequired, relationEvidence, "",
	))

	effects := s.completionDirtyEffects(observed, options.Dirty, options.CommitMessage)
	if completionDirtyExecutable(observed, options.Dirty, options.CommitMessage) {
		if !relation.Contained() {
			if relation.BaseOnly > 0 {
				effects = append(effects, NewEffect(
					EffectRebaseBranch, "rebase the task branch onto the explicit base", observed.checkout, false, false,
					map[string]string{"branch": observed.task.Branch, "base": observed.completionBaseRef},
				))
			}
			if observed.integration.statusErr == nil && observed.integration.status.Dirty() {
				switch options.IntegrationTargetPolicy {
				case IntegrationTargetStashRestore:
					effects = append(effects, NewEffect(
						EffectStashTarget, "stash canonical integration checkout changes by exact OID", observed.repoPath, false, false,
						map[string]string{"scope": "staged-unstaged-untracked", "integration-authority": integrationAuthority(observed.integration)},
					))
				case IntegrationTargetDiscard:
					effects = append(effects, NewEffect(
						EffectDiscardTarget, "discard canonical integration checkout changes", observed.repoPath, true, false,
						map[string]string{"scope": "all", "token": "DROP", "integration-authority": integrationAuthority(observed.integration)},
					))
				}
			}
			effects = append(effects,
				NewEffect(
					EffectSwitchBase, "switch the canonical main checkout to the explicit base", observed.repoPath, false, false,
					map[string]string{"base": observed.completionBaseRef, "task-branch": observed.task.Branch},
				),
				NewEffect(
					EffectMergeFF, "fast-forward the explicit base to the task branch", observed.repoPath, false, false,
					map[string]string{"base": observed.completionBaseRef, "branch": observed.task.Branch, "strategy": "ff-only"},
				),
			)
			if options.IntegrationTargetPolicy == IntegrationTargetStashRestore &&
				observed.integration.statusErr == nil && observed.integration.status.Dirty() {
				effects = append(effects, NewEffect(
					EffectRestoreTarget, "restore canonical integration checkout changes from the exact stash", observed.repoPath, false, false,
					map[string]string{"index": "restore", "retain-on-failure": "true"},
				))
			}
		}
		if options.PushBase {
			effects = append(effects, completionPushBaseEffect(observed))
		}
		effects = append(effects, completionTaskUpdateEffect(observed))
	}
	return s.completionPlanSpec(
		request, observed, conditions, effects,
		completionFFConfirmation(observed, options),
		completionFallback(observed.task.ID, "--ff", options.Dirty, options.CommitMessage, options.PushBase, "", ""),
		"Fast-forward "+observed.task.Title()+" into "+observed.completionBaseRef,
	)
}

func (s *lifecycleService) reviewHandoffSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(ReviewHandoffOptions)
	conditions := s.completionConditions(observed, options.Dirty, options.CommitMessage)
	relation := effectiveCompletionRelation(observed, options.Dirty, options.CommitMessage)
	relationVerdict := VerdictMet
	relationEvidence := fmt.Sprintf("%s has %d commit(s) not in %s", observed.task.Branch, relation.BranchOnly, observed.completionBaseRef)
	if relation.Contained() {
		if observed.statusErr == nil && observed.status.Dirty() &&
			(options.Dirty == DirtyAuto || options.Dirty == DirtyCommit && strings.TrimSpace(options.CommitMessage) == "") {
			relationEvidence = "reviewability will be recomputed after an explicit dirty policy is selected"
		} else {
			relationVerdict = VerdictBlocked
			relationEvidence = observed.task.Branch + " is already contained in " + observed.completionBaseRef
		}
	}
	conditions = append(conditions,
		condition(ConditionBranchRelation, relationVerdict, RequirementRequired, relationEvidence,
			"use fast-forward completion or add reviewable task-branch commits"),
		completionReviewProviderCondition(observed),
	)

	effects := s.completionDirtyEffects(observed, options.Dirty, options.CommitMessage)
	if completionDirtyExecutable(observed, options.Dirty, options.CommitMessage) {
		effects = append(effects, NewEffect(
			EffectPushBranch, "publish the task branch for review", observed.task.Branch, false, true,
			map[string]string{
				"remote": "origin", "remote-url": observed.reviewRemoteURL,
				"repository": observed.reviewRepository, "checkout": observed.checkout,
			},
		))
		if observed.reviewResolveErr == nil && observed.reviewProvider != nil && observed.reviewAvailable {
			effects = append(effects, NewEffect(
				EffectCreateReview, "create a provider pull or merge request", observed.checkout, false, true,
				map[string]string{
					"provider": string(observed.reviewKind), "repository": observed.reviewRepository,
					"remote-url": observed.reviewRemoteURL, "base": observed.completionBaseRef,
					"head": observed.task.Branch, "draft": boolString(options.Draft),
					"title": options.Title, "body": options.Body,
				},
			))
		}
	}
	return s.completionPlanSpec(
		request, observed, conditions, effects,
		completionConfirmation(observed, options.Dirty, "Publish this task for review?"),
		completionFallback(observed.task.ID, "--pr", options.Dirty, options.CommitMessage, false, "", ""),
		"Publish "+observed.task.Title()+" for review into "+observed.completionBaseRef,
	)
}

func (s *lifecycleService) verifyMergedSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(VerifyMergedOptions)
	conditions := s.completionConditions(observed, options.Dirty, options.CommitMessage)
	proofCondition := completionProofCondition(observed)
	proofSurvivesDirtyPolicy := true
	if options.SquashCommit != "" && observed.statusErr == nil && observed.status.Dirty() && options.Dirty == DirtyCommit {
		proofSurvivesDirtyPolicy = false
		proofCondition = condition(ConditionMergeProof, VerdictBlocked, RequirementRequired,
			"the squash attestation predates the new commit requested by --dirty=commit",
			"commit and integrate the dirty work before supplying a fresh squash attestation")
	}
	conditions = append(conditions,
		condition(ConditionBranchRelation, VerdictMet, RequirementAdvisory,
			fmt.Sprintf("local relation to %s is behind %d and ahead %d",
				observed.completionBaseRef, observed.finish.Relation.BaseOnly, observed.finish.Relation.BranchOnly), ""),
		proofCondition,
	)

	effects := s.completionDirtyEffects(observed, options.Dirty, options.CommitMessage)
	if !proofSurvivesDirtyPolicy {
		effects = nil
	} else if completionDirtyExecutable(observed, options.Dirty, options.CommitMessage) {
		effects = append(effects, NewEffect(
			EffectVerifyAncestry, "prove exact merge ancestry", observed.completionBaseRef, false, false,
			map[string]string{
				"proof-ref": observed.proofRef, "proof-oid": observed.proofOID,
				"base-ref": observed.completionBaseRef, "base-oid": observed.completionBaseOID,
			},
		))
		if options.PushBase {
			effects = append(effects, completionPushBaseEffect(observed))
		}
		effects = append(effects, completionTaskUpdateEffect(observed))
	}
	return s.completionPlanSpec(
		request, observed, conditions, effects,
		completionConfirmation(observed, options.Dirty, "Verify this merge and record DONE?"),
		completionFallback(observed.task.ID, "--merged", options.Dirty, options.CommitMessage,
			options.PushBase, options.BaseRef, options.SquashCommit),
		"Verify "+observed.task.Title()+" merged into "+observed.completionBaseRef,
	)
}

func (s *lifecycleService) completionPlanSpec(
	request Request,
	observed lifecycleObservation,
	conditions []Condition,
	effects []Effect,
	confirmation Confirmation,
	fallback string,
	summary string,
) PlanSpec {
	return PlanSpec{
		Authority:         s.baseAuthority(request, observed),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: completionRetainedResources(observed),
		Confirmation:      confirmation,
		FallbackCommand:   fallback,
		Summary:           summary,
		DisplayedAt:       s.now(),
	}
}

func (s *lifecycleService) completionConditions(
	observed lifecycleObservation,
	dirty DirtyPolicy,
	message string,
) []Condition {
	conditions := []Condition{
		condition(ConditionTaskCurrent, VerdictMet, RequirementRequired,
			"task "+observed.task.ID+" revision "+observed.record.Revision, ""),
		condition(ConditionRepoIdentity, VerdictMet, RequirementRequired,
			"Git common directory "+observed.gitCommonDir, ""),
	}
	conditions = append(conditions, completionCheckoutConditions(observed)...)
	conditions = append(conditions, completionGitConditions(observed, dirty, message)...)
	conditions = append(conditions, completionArtifactCondition(observed))
	conditions = append(conditions, s.completionRuntimeConditions(observed)...)
	conditions = append(conditions, completionBaseCondition(observed))
	return conditions
}

func completionCheckoutConditions(observed lifecycleObservation) []Condition {
	if !observed.hasCheckout() {
		verdict := VerdictBlocked
		evidence := "the exact task checkout is not registered"
		if observed.worktreeErr != nil && !isWorktreeNotFound(observed.worktreeErr) {
			verdict = VerdictError
			evidence = observed.worktreeErr.Error()
		}
		return []Condition{
			condition(ConditionCheckoutPresent, verdict, RequirementRequired, evidence, "resume or reconcile the exact checkout"),
			condition(ConditionCheckoutExact, VerdictUnknown, RequirementRequired, "checkout identity is unavailable", "refresh worktree topology"),
			condition(ConditionCheckoutLinked, VerdictUnknown, RequirementRequired, "checkout kind is unavailable", "refresh worktree topology"),
			condition(ConditionCheckoutBranch, VerdictUnknown, RequirementRequired, "checkout branch is unavailable", "restore the named task branch"),
		}
	}

	exactVerdict, exactEvidence := checkoutExactVerdict(observed)
	kindVerdict := VerdictMet
	kindEvidence := "canonical main checkout"
	if observed.mode == task.ModeWorktree {
		kindEvidence = "exact linked worktree"
		if !observed.isLinkedWorktree() {
			kindVerdict = VerdictBlocked
			kindEvidence = "worktree task does not resolve to a linked checkout"
		}
	} else if !observed.worktree.Worktree.Main || observed.worktree.Worktree.Bare {
		kindVerdict = VerdictBlocked
		kindEvidence = "branch or direct task does not resolve to Git's canonical main checkout"
	}

	branchVerdict := VerdictMet
	branchEvidence := "checkout and registry name task branch " + observed.task.Branch
	if observed.task.Branch == "" || observed.statusErr != nil || observed.status.Detached ||
		observed.status.Branch != observed.task.Branch || observed.worktree.Worktree.Detached ||
		observed.worktree.Worktree.Branch != observed.task.Branch {
		branchVerdict = VerdictBlocked
		branchEvidence = fmt.Sprintf("registry=%q status=%q expected=%q detached=%t",
			observed.worktree.Worktree.Branch, observed.status.Branch, observed.task.Branch,
			observed.status.Detached || observed.worktree.Worktree.Detached)
		if observed.statusErr != nil {
			branchVerdict = VerdictError
			branchEvidence = observed.statusErr.Error()
		}
	}
	return []Condition{
		condition(ConditionCheckoutPresent, VerdictMet, RequirementRequired, "registered checkout "+observed.checkout, ""),
		condition(ConditionCheckoutExact, exactVerdict, RequirementRequired, exactEvidence, "reconcile task and worktree identity"),
		condition(ConditionCheckoutLinked, kindVerdict, RequirementRequired, kindEvidence, "reconcile the task checkout mode"),
		condition(ConditionCheckoutBranch, branchVerdict, RequirementRequired, branchEvidence, "restore the exact named task branch"),
	}
}

func completionGitConditions(observed lifecycleObservation, dirty DirtyPolicy, message string) []Condition {
	status := observed.status
	statusVerdict := VerdictMet
	statusEvidence := status.Summary()
	if observed.statusErr != nil {
		statusVerdict = VerdictError
		statusEvidence = observed.statusErr.Error()
	}

	finishVerdict := VerdictMet
	finishEvidence := fmt.Sprintf("fingerprint %s; base-only=%d branch-only=%d",
		observed.finish.Fingerprint, observed.finish.Relation.BaseOnly, observed.finish.Relation.BranchOnly)
	switch {
	case !observed.hasCheckout():
		finishVerdict = VerdictUnknown
		finishEvidence = "finish analysis requires the exact checkout"
	case observed.completionBaseRef == "":
		finishVerdict = VerdictBlocked
		finishEvidence = "finish analysis requires an explicit base ref"
	case observed.finishErr != nil:
		finishVerdict = VerdictError
		finishEvidence = observed.finishErr.Error()
	case observed.finish.Fingerprint == "":
		finishVerdict = VerdictError
		finishEvidence = "finish analysis returned no content fingerprint"
	}

	operationVerdict := VerdictMet
	operationEvidence := "no Git operation is in progress"
	if observed.operationErr != nil {
		operationVerdict = VerdictError
		operationEvidence = observed.operationErr.Error()
	} else if observed.inProgress {
		operationVerdict = VerdictBlocked
		operationEvidence = "Git operation " + observed.operation + " is in progress"
	}

	conflictVerdict := VerdictMet
	conflictEvidence := "no unmerged paths"
	if observed.statusErr != nil {
		conflictVerdict = VerdictError
		conflictEvidence = observed.statusErr.Error()
	} else if observed.status.Conflicted > 0 || observed.finish.Status.Conflicted > 0 {
		conflictVerdict = VerdictBlocked
		conflictEvidence = strconv.Itoa(max(observed.status.Conflicted, observed.finish.Status.Conflicted)) + " conflicted path(s)"
	}

	cleanVerdict, cleanEvidence := completionDirtyCondition(observed, dirty, message)
	return []Condition{
		condition(ConditionGitStatus, statusVerdict, RequirementRequired, statusEvidence, "repair Git status observation"),
		condition(ConditionFinishAnalysis, finishVerdict, RequirementRequired, finishEvidence, "refresh the exact finish analysis"),
		condition(ConditionGitOperation, operationVerdict, RequirementRequired, operationEvidence, "finish or abort the Git operation"),
		condition(ConditionGitConflict, conflictVerdict, RequirementRequired, conflictEvidence, "resolve or abort conflicts before completion"),
		condition(ConditionCheckoutClean, cleanVerdict, RequirementRequired, cleanEvidence, "select fail, commit with a message, or discard"),
	}
}

func completionDirtyCondition(observed lifecycleObservation, dirty DirtyPolicy, message string) (Verdict, string) {
	if observed.statusErr != nil {
		return VerdictError, observed.statusErr.Error()
	}
	if !observed.status.Dirty() {
		return VerdictMet, "checkout is clean; dirty policy " + string(dirty) + " causes no content mutation"
	}
	evidence := observed.status.Breakdown()
	if observed.finishErr == nil {
		evidence += fmt.Sprintf("; %d path(s) match %s and %d contain unique content",
			observed.finish.EquivalentDirty(), observed.completionBaseRef, observed.finish.UniqueDirty())
	}
	switch dirty {
	case DirtyAuto:
		return VerdictNeedsInput, evidence + "; choose an explicit dirty policy"
	case DirtyFail:
		return VerdictBlocked, evidence + "; fail policy refuses dirty completion"
	case DirtyCommit:
		if strings.TrimSpace(message) == "" {
			return VerdictNeedsInput, evidence + "; commit-all requires a nonempty message"
		}
		return VerdictMet, evidence + "; commit-all was explicitly selected"
	case DirtyDiscard:
		if observed.finish.UniqueDirty() > 0 {
			return VerdictMet, evidence + "; discard-all was explicitly selected and requires DROP"
		}
		return VerdictMet, evidence + "; discard-all was explicitly selected and requires approval"
	default:
		return VerdictError, "unknown dirty policy " + string(dirty)
	}
}

func completionArtifactCondition(observed lifecycleObservation) Condition {
	switch {
	case !observed.hasCheckout():
		return condition(ConditionArtifactReady, VerdictUnknown, RequirementRequired,
			"artifact readiness requires the exact checkout", "restore the checkout")
	case observed.artifactErr != nil:
		return condition(ConditionArtifactReady, VerdictError, RequirementRequired,
			observed.artifactErr.Error(), "repair artifact readiness observation")
	case observed.artifact.Ready():
		return condition(ConditionArtifactReady, VerdictMet, RequirementRequired, artifactEvidence(observed), "")
	default:
		return condition(ConditionArtifactReady, VerdictBlocked, RequirementRequired,
			artifactEvidence(observed), "finalize or explicitly discard every artifact intent")
	}
}

func (s *lifecycleService) completionRuntimeConditions(observed lifecycleObservation) []Condition {
	if observed.runtimeErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictError, RequirementRequired,
				observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionAgentOccupancy, VerdictError, RequirementRequired,
				observed.runtimeErr.Error(), "repair recognized-agent observation"),
		}
	}
	availableVerdict := VerdictMet
	availableEvidence := "runtime backend " + runtimeName(observed.runtime) + " was available for strict inspection"
	if observed.runtime != nil && observed.runtime.Name() != "none" && !observed.runtimeAvailable {
		availableVerdict = VerdictBlocked
		availableEvidence = "runtime backend " + observed.runtime.Name() + " is unavailable"
	}
	occupancyVerdict, occupancyEvidence := s.completionOccupancyVerdict(observed.occupancy, observed.occupancyErr)
	return []Condition{
		condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired,
			availableEvidence, "select an available runtime backend"),
		condition(ConditionAgentOccupancy, occupancyVerdict, RequirementRequired,
			occupancyEvidence, "stop every other recognized agent or use another checkout"),
	}
}

func (s *lifecycleService) completionOccupancyVerdict(occupancy runtime.Occupancy, err error) (Verdict, string) {
	occupancyErr := s.writerOccupancyError(occupancy, err)
	if occupancyErr == nil {
		if s.allowSharedCheckout {
			return VerdictMet, "shared-checkout override accepts the observed or explicitly unobserved runtime occupancy"
		}
		return VerdictMet, "no other recognized agent occupies the checkout; only the exact caller pane is excluded"
	}
	verdict := VerdictBlocked
	if err != nil || occupancy.CurrentPane.Err != nil || occupancy.SessionCoverageErr != nil ||
		occupancy.SessionList.Err != nil || occupancy.AgentActivityList.Err != nil || occupancyObservationIncomplete(occupancy) {
		verdict = VerdictError
	}
	return verdict, occupancyErr.Error()
}

func completionBaseCondition(observed lifecycleObservation) Condition {
	requirement := RequirementRequired
	if observed.mode == task.ModeDirect {
		requirement = RequirementAdvisory
	}
	switch {
	case observed.completionBaseRef == "":
		return condition(ConditionExplicitBase, VerdictBlocked, requirement,
			"completion has no explicit base ref", "record or select an explicit base ref")
	case observed.completionBaseOIDErr != nil:
		return condition(ConditionExplicitBase, VerdictError, requirement,
			observed.completionBaseOIDErr.Error(), "restore the exact base ref")
	case observed.completionBaseOID == "":
		return condition(ConditionExplicitBase, VerdictBlocked, requirement,
			"base ref "+observed.completionBaseRef+" resolved to no commit", "restore the exact base ref")
	default:
		return condition(ConditionExplicitBase, VerdictMet, requirement,
			fmt.Sprintf("base %s at %s", observed.completionBaseRef, observed.completionBaseOID), "")
	}
}

func completionIntegrationCondition(
	observed lifecycleObservation,
	dirty DirtyPolicy,
	message string,
	targetPolicy IntegrationTargetPolicy,
) Condition {
	if observed.mode == task.ModeBranch {
		verdict, _ := completionDirtyCondition(observed, dirty, message)
		if verdict == VerdictMet {
			return condition(ConditionIntegrationTarget, VerdictMet, RequirementRequired,
				"the exact canonical task checkout will be clean before switching to "+observed.completionBaseRef, "")
		}
		return condition(ConditionIntegrationTarget, verdict, RequirementRequired,
			"the canonical task checkout is not yet switchable", "finalize its dirty policy")
	}
	integration := observed.integration
	switch {
	case integration.worktreeErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.worktreeErr.Error(), "repair canonical main worktree identity")
	case !integration.worktreeFound:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"canonical main checkout is not registered", "restore the canonical main checkout")
	case integration.worktree.Path != observed.repoPath || integration.worktree.GitCommonDir != observed.gitCommonDir ||
		!integration.worktree.Worktree.Main || integration.worktree.Worktree.Bare:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"integration target is not the exact canonical main checkout", "repair worktree topology")
	case integration.statusErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.statusErr.Error(), "repair canonical checkout status observation")
	case integration.headErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.headErr.Error(), "repair canonical checkout HEAD observation")
	case integration.operationErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.operationErr.Error(), "repair canonical checkout Git-operation observation")
	case integration.inProgress:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"canonical checkout has Git operation "+integration.operation+" in progress", "finish or abort it")
	case integration.status.Conflicted > 0:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			fmt.Sprintf("canonical checkout has %d conflicted path(s)", integration.status.Conflicted), "resolve or abort conflicts")
	case integration.status.Dirty():
		switch targetPolicy {
		case IntegrationTargetDiscard:
			return condition(ConditionIntegrationTarget, VerdictMet, RequirementRequired,
				"canonical checkout changes will be discarded under typed confirmation", "")
		case IntegrationTargetStashRestore:
			switch {
			case integration.stashSafetyErr != nil:
				return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
					integration.stashSafetyErr.Error(), "repair canonical stash-safety observation")
			case integration.stashSafety.DirtySubmodules > 0:
				return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
					fmt.Sprintf("canonical checkout has %d dirty or unavailable submodule checkout(s)", integration.stashSafety.DirtySubmodules),
					"commit, stash, or clean each submodule independently")
			case len(integration.stashSafety.NestedRepositories) > 0:
				return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
					"canonical checkout contains nested repositories at "+strings.Join(integration.stashSafety.NestedRepositories, ", "),
					"preserve each nested repository independently")
			default:
				return condition(ConditionIntegrationTarget, VerdictMet, RequirementRequired,
					"canonical checkout changes will be stashed by exact OID and restored with index state after integration", "")
			}
		}
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"canonical checkout is dirty: "+integration.status.Breakdown(), "preserve its unrelated bytes before integration")
	case integration.worktree.Worktree.Head != integration.head:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"canonical worktree registry HEAD differs from checkout HEAD", "refresh worktree topology")
	default:
		return condition(ConditionIntegrationTarget, VerdictMet, RequirementRequired,
			"canonical main checkout is exact, clean, conflict-free, and switchable", "")
	}
}

func completionContainedIntegrationCondition(observed lifecycleObservation) Condition {
	integration := observed.integration
	switch {
	case integration.worktreeErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.worktreeErr.Error(), "repair canonical main worktree identity")
	case !integration.worktreeFound:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"canonical main checkout is not registered", "restore the canonical main checkout")
	case integration.statusErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.statusErr.Error(), "repair canonical checkout status observation")
	case integration.operationErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.operationErr.Error(), "repair canonical checkout Git-operation observation")
	case integration.inProgress:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			"canonical checkout has Git operation "+integration.operation+" in progress", "finish or abort it")
	case integration.status.Conflicted > 0:
		return condition(ConditionIntegrationTarget, VerdictBlocked, RequirementRequired,
			fmt.Sprintf("canonical checkout has %d conflicted path(s)", integration.status.Conflicted),
			"resolve or reset the failed restore before recording DONE")
	case integration.headErr != nil:
		return condition(ConditionIntegrationTarget, VerdictError, RequirementRequired,
			integration.headErr.Error(), "repair canonical checkout HEAD observation")
	default:
		return condition(ConditionIntegrationTarget, VerdictMet, RequirementRequired,
			"canonical checkout has no unresolved integration operation or conflict", "")
	}
}

func (s *lifecycleService) completionIntegrationOccupancyCondition(observed lifecycleObservation) Condition {
	if observed.runtimeErr != nil {
		return condition(ConditionIntegrationOccupancy, VerdictError, RequirementRequired,
			observed.runtimeErr.Error(), "repair runtime backend resolution")
	}
	verdict, evidence := s.completionOccupancyVerdict(observed.integration.occupancy, observed.integration.occupancyErr)
	return condition(ConditionIntegrationOccupancy, verdict, RequirementRequired, evidence,
		"stop every other recognized agent in the canonical integration checkout")
}

func completionReviewProviderCondition(observed lifecycleObservation) Condition {
	switch {
	case observed.reviewResolveErr != nil:
		return condition(ConditionReviewProvider, VerdictUnsupported, RequirementAdvisory,
			observed.reviewResolveErr.Error()+"; a successful branch push remains a valid publication", "open the review manually")
	case observed.reviewProvider == nil:
		return condition(ConditionReviewProvider, VerdictUnsupported, RequirementAdvisory,
			"no forge provider was resolved; a successful branch push remains a valid publication", "open the review manually")
	case !observed.reviewAvailable:
		return condition(ConditionReviewProvider, VerdictUnsupported, RequirementAdvisory,
			fmt.Sprintf("%s provider CLI %s is unavailable; a successful branch push remains a valid publication",
				observed.reviewKind, observed.reviewProvider.Bin()), "install or authenticate the provider CLI, or open the review manually")
	default:
		return condition(ConditionReviewProvider, VerdictMet, RequirementAdvisory,
			fmt.Sprintf("%s provider CLI %s will create the review", observed.reviewKind, observed.reviewProvider.Bin()), "")
	}
}

func completionProofCondition(observed lifecycleObservation) Condition {
	switch {
	case observed.completionBaseRef == "":
		return condition(ConditionMergeProof, VerdictBlocked, RequirementRequired,
			"merge proof requires an exact base ref", "select a local or explicit remote-tracking base ref")
	case observed.completionBaseOIDErr != nil:
		return condition(ConditionMergeProof, VerdictError, RequirementRequired,
			observed.completionBaseOIDErr.Error(), "restore the selected base ref")
	case observed.completionBaseOID == "":
		return condition(ConditionMergeProof, VerdictBlocked, RequirementRequired,
			"merge proof requires an exact base OID", "restore the selected base ref")
	case observed.proofRef == "":
		return condition(ConditionMergeProof, VerdictBlocked, RequirementRequired,
			"merge proof ref is empty", "restore the task branch or name an exact squash commit")
	case observed.proofOIDErr != nil:
		return condition(ConditionMergeProof, VerdictError, RequirementRequired,
			observed.proofOIDErr.Error(), "restore the exact proof ref")
	case observed.proofOID == "":
		return condition(ConditionMergeProof, VerdictBlocked, RequirementRequired,
			"proof ref "+observed.proofRef+" resolved to no commit", "restore the exact proof commit")
	case observed.proofErr != nil:
		return condition(ConditionMergeProof, VerdictError, RequirementRequired,
			observed.proofErr.Error(), "repair local ancestry observation")
	case !observed.proofContained:
		return condition(ConditionMergeProof, VerdictBlocked, RequirementRequired,
			fmt.Sprintf("%s (%s) is not an ancestor of %s (%s)",
				observed.proofRef, observed.proofOID, observed.completionBaseRef, observed.completionBaseOID),
			"fetch explicitly if desired, then select the freshly observed ref; no fetch is assumed")
	default:
		return condition(ConditionMergeProof, VerdictMet, RequirementRequired,
			fmt.Sprintf("%s (%s) is an ancestor of %s (%s)",
				observed.proofRef, observed.proofOID, observed.completionBaseRef, observed.completionBaseOID), "")
	}
}

func (s *lifecycleService) completionDirtyEffects(
	observed lifecycleObservation,
	dirty DirtyPolicy,
	message string,
) []Effect {
	if observed.statusErr != nil || !observed.status.Dirty() {
		return nil
	}
	switch dirty {
	case DirtyCommit:
		if strings.TrimSpace(message) == "" {
			return nil
		}
		return []Effect{NewEffect(
			EffectCommitAll, "commit every staged, unstaged, and untracked change", observed.checkout, false, false,
			map[string]string{"message": strings.TrimSpace(message), "scope": "all"},
		)}
	case DirtyDiscard:
		details := map[string]string{"scope": "all", "finish-fingerprint": observed.finish.Fingerprint}
		if observed.finish.UniqueDirty() > 0 {
			details["token"] = "DROP"
		}
		return []Effect{NewEffect(
			EffectDiscardAll, "discard every staged, unstaged, and non-ignored untracked change", observed.checkout, true, false,
			details,
		)}
	default:
		return nil
	}
}

func completionDirtyExecutable(observed lifecycleObservation, dirty DirtyPolicy, message string) bool {
	if observed.statusErr != nil || !observed.status.Dirty() {
		return true
	}
	switch dirty {
	case DirtyCommit:
		return strings.TrimSpace(message) != ""
	case DirtyDiscard:
		return true
	default:
		return false
	}
}

func effectiveCompletionRelation(observed lifecycleObservation, dirty DirtyPolicy, message string) gitx.BranchRelation {
	relation := observed.finish.Relation
	if observed.statusErr == nil && observed.status.Dirty() && dirty == DirtyCommit && strings.TrimSpace(message) != "" {
		relation.BranchOnly++
	}
	return relation
}

func completionConfirmation(observed lifecycleObservation, dirty DirtyPolicy, prompt string) Confirmation {
	if observed.statusErr == nil && observed.status.Dirty() && dirty == DirtyDiscard && observed.finish.UniqueDirty() > 0 {
		return Confirmation{
			Kind:   ConfirmationTyped,
			Prompt: fmt.Sprintf("Type DROP to discard %d unique path(s) under this exact plan", observed.finish.UniqueDirty()),
			Token:  "DROP",
		}
	}
	return Confirmation{Kind: ConfirmationApproval, Prompt: prompt}
}

func completionFFConfirmation(observed lifecycleObservation, options CompleteFFOptions) Confirmation {
	if options.IntegrationTargetPolicy == IntegrationTargetDiscard && observed.integration.statusErr == nil && observed.integration.status.Dirty() {
		detail := fmt.Sprintf("%d canonical integration checkout path(s)", observed.integration.status.Changed)
		if observed.statusErr == nil && observed.status.Dirty() && options.Dirty == DirtyDiscard && observed.finish.UniqueDirty() > 0 {
			detail += fmt.Sprintf(" and %d unique task-checkout path(s)", observed.finish.UniqueDirty())
		}
		return Confirmation{
			Kind: ConfirmationTyped, Token: "DROP",
			Prompt: "Type DROP to discard " + detail + " under this exact plan",
		}
	}
	if options.IntegrationTargetPolicy == IntegrationTargetStashRestore &&
		observed.integration.statusErr == nil && observed.integration.status.Dirty() {
		return Confirmation{Kind: ConfirmationApproval, Prompt: fmt.Sprintf(
			"Stash %d canonical path(s), fast-forward, then restore their index and worktree state?",
			observed.integration.status.Changed,
		)}
	}
	return completionConfirmation(observed, options.Dirty, "Integrate this task with fast-forward only?")
}

func completionPushBaseEffect(observed lifecycleObservation) Effect {
	return NewEffect(
		EffectPushBase, "optionally push the selected base", observed.completionBaseRef, false, true,
		map[string]string{"remote": "origin", "base": observed.completionBaseRef, "warning-on-failure": "true"},
	)
}

func completionTaskUpdateEffect(observed lifecycleObservation) Effect {
	return NewEffect(
		EffectUpdateTask, "write DONE lifecycle state last", observed.task.ID, false, false,
		map[string]string{
			"expected-revision": observed.record.Revision, "state": string(task.Done),
			"owner": "retain", "worktree-path": observed.task.WorktreePath,
			"runtime-name": observed.task.RuntimeName, "runtime-handle": observed.task.RuntimeHandle,
		},
	)
}

func completionRetainedResources(observed lifecycleObservation) []string {
	retained := []string{"branch:" + observed.task.Branch}
	if observed.checkout != "" {
		retained = append(retained, "checkout:"+observed.checkout)
	}
	if observed.task.RuntimeName != "" || observed.task.RuntimeHandle != "" {
		retained = append(retained, "runtime:"+observed.task.RuntimeName+":"+observed.task.RuntimeHandle)
	}
	return retained
}

func completionFallback(id, mode string, dirty DirtyPolicy, message string, push bool, baseRef, squash string) string {
	parts := []string{"dev", "done", shellQuote(id)}
	if mode != "" {
		parts = append(parts, mode)
	}
	if dirty != DirtyAuto {
		parts = append(parts, "--dirty="+string(dirty))
	}
	if dirty == DirtyCommit && strings.TrimSpace(message) != "" {
		parts = append(parts, "--message", shellQuote(strings.TrimSpace(message)))
	}
	if baseRef != "" {
		parts = append(parts, "--base-ref", shellQuote(baseRef))
	}
	if squash != "" {
		parts = append(parts, "--confirm-squash", shellQuote(squash))
	}
	if push {
		parts = append(parts, "--push")
	}
	return strings.Join(parts, " ")
}

func gitIsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	_, err := gitx.Run(ctx, dir, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func reviewProviderBin(provider forge.Forge) string {
	if provider == nil {
		return ""
	}
	return provider.Bin()
}

func reviewProviderWarning(observed lifecycleObservation) string {
	switch {
	case observed.reviewResolveErr != nil:
		return observed.reviewResolveErr.Error() + "; branch was pushed, but no review URL was observed"
	case observed.reviewProvider == nil:
		return "no forge provider was resolved; branch was pushed, but no review URL was observed"
	case !observed.reviewAvailable:
		return fmt.Sprintf("%s provider CLI %s is unavailable; branch was pushed, but no review URL was observed",
			observed.reviewKind, observed.reviewProvider.Bin())
	default:
		return ""
	}
}

func completionReviewRequest(observed lifecycleObservation, options ReviewHandoffOptions) forge.PRRequest {
	return forge.PRRequest{
		Base: observed.completionBaseRef, Head: observed.task.Branch,
		Title: options.Title, Body: options.Body, Draft: options.Draft,
		Fill: options.Title == "", Web: false,
	}
}
