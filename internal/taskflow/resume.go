package taskflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (s *lifecycleService) resumeSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(ResumeOptions)
	conditions := []Condition{
		condition(ConditionTaskCurrent, VerdictMet, RequirementRequired,
			"task "+observed.task.ID+" revision "+observed.record.Revision, ""),
		condition(ConditionRepoIdentity, VerdictMet, RequirementRequired,
			"Git common directory "+observed.gitCommonDir, ""),
	}
	conditions = append(conditions, resumeOwnerCondition(observed, options))
	conditions = append(conditions, s.resumeCheckoutConditions(observed, options)...)
	conditions = append(conditions, s.resumeRuntimeConditions(observed)...)

	var effects []Effect
	if options.FetchRefs {
		effects = append(effects, NewEffect(EffectFetchRefs,
			"fetch and prune origin before resuming", observed.repoPath, false, true,
			map[string]string{"remote": "origin", "warning-on-failure": "true"}))
	}
	if observed.mode == task.ModeWorktree && !observed.hasCheckout() &&
		(observed.task.State == task.Warm || observed.task.State == task.Cold) {
		effects = append(effects, NewEffect(EffectCreateWorktree,
			"rebuild the missing linked worktree", observed.desiredWorktree, false, false,
			map[string]string{
				"repo": observed.repoPath, "branch": observed.task.Branch,
				"preferred-base": observed.remoteBranch, "fallback-base": observed.task.Base,
				"selected-base": observed.baseRef,
				"provision":     boolString(!options.NoProvision), "runtime": "false",
			}))
	}
	if resumeNeedsBranchSwitch(observed) {
		effects = append(effects, NewEffect(EffectSwitchBranch,
			"switch the clean canonical checkout to the task branch", observed.repoPath, false, false,
			map[string]string{"from": observed.status.Branch, "to": observed.task.Branch}))
	}
	if shouldOpenResumeRuntime(observed) {
		effects = append(effects, NewEffect(EffectOpenRuntime,
			"open a runtime surface for the checkout", resumeEffectCheckout(observed), false, false,
			map[string]string{"backend": runtimeName(observed.runtime), "label": resumeRuntimeLabel(observed.task)}))
	}
	runtimeDisposition := "clear"
	if observed.runtime != nil && observed.runtime.Name() != "none" {
		if observed.savedRuntimeLive {
			runtimeDisposition = "reuse:" + observed.runtime.Name() + ":" + observed.task.RuntimeHandle
		} else {
			runtimeDisposition = "open:" + observed.runtime.Name()
		}
	}
	path := observed.task.WorktreePath
	if observed.mode == task.ModeWorktree {
		if observed.hasCheckout() {
			path = observed.checkout
		} else {
			path = observed.desiredWorktree
		}
	} else {
		path = ""
	}
	effects = append(effects, taskUpdateEffect(observed, task.Hot, path, runtimeDisposition,
		observed.task.Next, observed.task.Note))

	retained := []string{"branch:" + observed.task.Branch}
	if checkout := resumeEffectCheckout(observed); checkout != "" {
		retained = append(retained, "checkout:"+checkout)
	}
	if observed.runtime != nil && observed.runtime.Name() != "none" {
		retained = append(retained, "runtime:"+observed.runtime.Name())
	}
	return PlanSpec{
		Authority:         s.baseAuthority(request, observed),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: retained,
		Confirmation:      Confirmation{Kind: ConfirmationApproval, Prompt: "Resume this task and claim its checkout?"},
		FallbackCommand:   resumeFallback(observed.task.ID, options),
		Summary:           "Resume " + observed.task.Title() + " HOT",
		DisplayedAt:       s.now(),
	}
}

func resumeOwnerCondition(observed lifecycleObservation, options ResumeOptions) Condition {
	if observed.task.OwnedBy(observed.taskflowHost) {
		owner := observed.task.Owner
		if owner == "" {
			owner = "unclaimed"
		}
		return condition(ConditionOwner, VerdictMet, RequirementRequired, "task owner is "+owner, "")
	}
	if options.TakeOwnership {
		return condition(ConditionOwner, VerdictMet, RequirementRequired,
			"ownership transfer from "+observed.task.Owner+" was explicitly requested", "")
	}
	return condition(ConditionOwner, VerdictBlocked, RequirementRequired,
		"task is owned by "+observed.task.Owner, "confirm the other machine pushed its work, then take ownership explicitly")
}

func (s *lifecycleService) resumeCheckoutConditions(observed lifecycleObservation, options ResumeOptions) []Condition {
	conditions := make([]Condition, 0, 12)
	if !observed.hasCheckout() {
		missingVerdict := VerdictBlocked
		missingEvidence := "the task checkout is not registered"
		rebuildAllowed := observed.mode == task.ModeWorktree &&
			(observed.task.State == task.Warm || observed.task.State == task.Cold) &&
			(observed.worktreeErr == nil || errors.Is(observed.worktreeErr, gitx.ErrWorktreeNotFound))
		if observed.worktreeErr != nil && !errors.Is(observed.worktreeErr, gitx.ErrWorktreeNotFound) {
			missingVerdict = VerdictError
			missingEvidence = observed.worktreeErr.Error()
		}
		if rebuildAllowed {
			missingVerdict = VerdictMet
			missingEvidence = strings.ToUpper(string(observed.task.State)) + " worktree is absent and will be rebuilt"
		}
		conditions = append(conditions,
			condition(ConditionCheckoutPresent, missingVerdict, RequirementRequired, missingEvidence, "restore or rebuild the exact checkout"),
			condition(ConditionCheckoutExact, missingVerdict, RequirementRequired, missingEvidence, "refresh repository topology"),
			condition(ConditionResumeCheckout, missingVerdict, RequirementRequired, missingEvidence, "resume COLD or reconcile the missing WARM checkout"),
		)
		branchVerdict := VerdictMet
		branchEvidence := "local task branch is available for reconstruction"
		switch {
		case observed.localBranchOIDErr != nil:
			branchVerdict, branchEvidence = VerdictError, observed.localBranchOIDErr.Error()
		case observed.localBranchExists:
		case observed.remoteBranchOIDErr != nil:
			branchVerdict, branchEvidence = VerdictError, observed.remoteBranchOIDErr.Error()
		case observed.remoteBranchExists:
			branchEvidence = "published task branch is available for reconstruction"
		case options.FetchRefs:
			branchEvidence = "published task branch must appear after the declared fetch"
		default:
			branchVerdict, branchEvidence = VerdictBlocked, "neither local nor published task branch exists"
		}
		conditions = append(conditions, condition(ConditionBranchRef, branchVerdict, RequirementRequired,
			branchEvidence, "restore or fetch the exact task branch before reconstruction"))
		if observed.mode == task.ModeWorktree &&
			(observed.task.State == task.Warm || observed.task.State == task.Cold) {
			switch {
			case observed.desiredWorktreeErr != nil:
				conditions = append(conditions, condition(ConditionCheckoutLinked, VerdictError, RequirementRequired,
					observed.desiredWorktreeErr.Error(), "choose a valid managed worktree destination"))
			case observed.desiredWorktree == "":
				conditions = append(conditions, condition(ConditionCheckoutLinked, VerdictBlocked, RequirementRequired,
					"managed worktree destination is empty", "configure paths.worktree_path"))
			default:
				conditions = append(conditions, condition(ConditionCheckoutLinked, VerdictMet, RequirementRequired,
					"managed linked worktree will be created at "+observed.desiredWorktree, ""))
			}
			if observed.baseRef == "" {
				conditions = append(conditions, condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
					"neither origin/"+observed.task.Branch+" nor the recorded base is available", "record an explicit base or fetch the published branch"))
			} else if observed.baseRefExistsErr != nil {
				conditions = append(conditions, condition(ConditionExplicitBase, VerdictError, RequirementRequired,
					observed.baseRefExistsErr.Error(), "repair reconstruction base observation"))
			} else if !observed.baseRefExists && !options.FetchRefs {
				conditions = append(conditions, condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
					"explicit reconstruction base "+observed.baseRef+" does not exist", "fetch refs or restore the explicit base"))
			} else {
				evidence := "reconstruction base " + observed.baseRef
				if options.FetchRefs && !observed.baseRefExists {
					evidence += " will be revalidated after fetch"
				}
				conditions = append(conditions, condition(ConditionExplicitBase, VerdictMet, RequirementRequired, evidence, ""))
			}
		} else {
			conditions = append(conditions,
				condition(ConditionCheckoutLinked, VerdictBlocked, RequirementRequired, "resume requires a present checkout", "reconcile the task checkout"),
				condition(ConditionExplicitBase, VerdictMet, RequirementAdvisory, "no reconstruction is planned", ""),
			)
		}
		deferredVerdict := VerdictUnknown
		deferredEvidence := "checkout is absent"
		if rebuildAllowed {
			deferredVerdict = VerdictMet
			deferredEvidence = "exact branch and status will be checked immediately after reconstruction"
		}
		conditions = append(conditions,
			condition(ConditionCheckoutBranch, deferredVerdict, RequirementRequired, deferredEvidence, "restore the checkout"),
			condition(ConditionGitStatus, deferredVerdict, RequirementRequired, deferredEvidence, "restore the checkout"),
			condition(ConditionGitOperation, deferredVerdict, RequirementAdvisory, deferredEvidence, "restore the checkout"),
			condition(ConditionGitConflict, deferredVerdict, RequirementAdvisory, deferredEvidence, "restore the checkout"),
			condition(ConditionSwitchSafe, VerdictMet, RequirementAdvisory, "no canonical branch switch is planned", ""),
			condition(ConditionTargetBranch, VerdictMet, RequirementAdvisory, "worktree creation names the exact task branch", ""),
		)
		return conditions
	}

	conditions = append(conditions,
		condition(ConditionCheckoutPresent, VerdictMet, RequirementRequired, "registered checkout "+observed.checkout, ""))
	exactVerdict, exactEvidence := checkoutExactVerdict(observed)
	conditions = append(conditions, condition(ConditionCheckoutExact, exactVerdict, RequirementRequired,
		exactEvidence, "reconcile the task and Git worktree identities"))

	kindVerdict := VerdictMet
	kindEvidence := "canonical main checkout"
	if observed.mode == task.ModeWorktree {
		kindEvidence = "exact linked worktree"
		if !observed.isLinkedWorktree() {
			kindVerdict = VerdictBlocked
			kindEvidence = "worktree task resolves to the main or bare checkout"
		}
	} else if !observed.worktree.Worktree.Main || observed.worktree.Worktree.Bare {
		kindVerdict = VerdictBlocked
		kindEvidence = "branch/direct task does not resolve to Git's main checkout"
	}
	conditions = append(conditions,
		condition(ConditionCheckoutLinked, kindVerdict, RequirementRequired, kindEvidence, "reconcile the checkout mode"),
		condition(ConditionResumeCheckout, kindVerdict, RequirementRequired, "fresh exact checkout will be reused", "reconcile the checkout mode"),
		condition(ConditionExplicitBase, VerdictMet, RequirementAdvisory, "no reconstruction is needed", ""),
	)

	if observed.statusErr != nil {
		conditions = append(conditions,
			condition(ConditionCheckoutBranch, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionGitStatus, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionGitOperation, VerdictError, RequirementAdvisory, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionGitConflict, VerdictError, RequirementAdvisory, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionSwitchSafe, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionTargetBranch, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
		)
		return conditions
	}
	conditions = append(conditions, condition(ConditionGitStatus, VerdictMet, RequirementRequired, observed.status.Summary(), ""))

	branchVerdict := VerdictMet
	branchEvidence := "checkout is on task branch " + observed.task.Branch
	switch observed.mode {
	case task.ModeWorktree:
		if observed.status.Detached || observed.status.Branch != observed.task.Branch || observed.worktree.Worktree.Branch != observed.task.Branch {
			branchVerdict = VerdictBlocked
			branchEvidence = fmt.Sprintf("registry=%q status=%q expected=%q", observed.worktree.Worktree.Branch, observed.status.Branch, observed.task.Branch)
		}
	case task.ModeDirect:
		if observed.status.Detached || observed.status.Branch != observed.task.Branch {
			branchVerdict = VerdictBlocked
			branchEvidence = fmt.Sprintf("direct checkout is on %q, expected %q", observed.status.Branch, observed.task.Branch)
		}
	case task.ModeBranch:
		if observed.status.Detached {
			branchVerdict = VerdictBlocked
			branchEvidence = "canonical checkout is detached"
		} else if observed.status.Branch != observed.task.Branch {
			branchEvidence = fmt.Sprintf("canonical checkout will switch from %q to %q", observed.status.Branch, observed.task.Branch)
		}
	}
	conditions = append(conditions, condition(ConditionCheckoutBranch, branchVerdict, RequirementRequired,
		branchEvidence, "switch or reconcile the exact task branch"))

	operationVerdict := VerdictMet
	operationEvidence := "no Git operation is in progress"
	if observed.operationErr != nil {
		operationVerdict = VerdictError
		operationEvidence = observed.operationErr.Error()
	} else if observed.inProgress {
		operationVerdict = VerdictBlocked
		operationEvidence = "Git operation " + observed.operation + " is in progress"
	}
	operationRequirement := RequirementAdvisory
	if resumeNeedsBranchSwitch(observed) {
		operationRequirement = RequirementRequired
	}
	conditions = append(conditions, condition(ConditionGitOperation, operationVerdict, operationRequirement,
		operationEvidence, "finish or abort the Git operation before switching branches"))

	conflictVerdict := VerdictMet
	conflictEvidence := "no unmerged paths"
	if observed.status.Conflicted > 0 {
		conflictEvidence = fmt.Sprintf("%d unmerged path(s)", observed.status.Conflicted)
		if resumeNeedsBranchSwitch(observed) {
			conflictVerdict = VerdictBlocked
		}
	}
	conflictRequirement := RequirementAdvisory
	if resumeNeedsBranchSwitch(observed) {
		conflictRequirement = RequirementRequired
	}
	conditions = append(conditions, condition(ConditionGitConflict, conflictVerdict, conflictRequirement,
		conflictEvidence, "resolve conflicts before switching branches"))

	switchVerdict := VerdictMet
	switchEvidence := "no branch switch is needed"
	if resumeNeedsBranchSwitch(observed) {
		switchEvidence = "canonical checkout is clean and may switch branches"
		if observed.status.Dirty() {
			switchVerdict = VerdictBlocked
			switchEvidence = observed.status.Breakdown()
		}
	}
	conditions = append(conditions, condition(ConditionSwitchSafe, switchVerdict, RequirementRequired,
		switchEvidence, "commit or preserve canonical checkout changes before switching"))

	targetVerdict := VerdictMet
	targetEvidence := "target branch " + observed.task.Branch + " exists"
	if observed.mode == task.ModeBranch && resumeNeedsBranchSwitch(observed) && !observed.localBranchExists {
		targetVerdict = VerdictBlocked
		targetEvidence = "target branch " + observed.task.Branch + " does not exist"
	}
	conditions = append(conditions, condition(ConditionTargetBranch, targetVerdict, RequirementRequired,
		targetEvidence, "restore the local task branch"))
	return conditions
}

func (s *lifecycleService) resumeRuntimeConditions(observed lifecycleObservation) []Condition {
	if observed.runtimeErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionAgentOccupancy, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionSavedRuntime, VerdictUnknown, RequirementAdvisory, "saved runtime was not validated", "refresh runtime state"),
		}
	}
	availableVerdict := VerdictMet
	availableEvidence := "runtime backend " + observed.runtime.Name() + " is available"
	if observed.runtime.Name() != "none" && !observed.runtimeAvailable {
		availableVerdict = VerdictBlocked
		availableEvidence = "runtime backend " + observed.runtime.Name() + " is unavailable"
	}
	if !observed.hasCheckout() {
		occupancyVerdict := VerdictMet
		occupancyEvidence := "missing checkout cannot be occupied; strict occupancy will be checked after reconstruction"
		if observed.runtime.Name() == "none" && !s.allowSharedCheckout {
			occupancyVerdict = VerdictError
			occupancyEvidence = "runtime=none cannot observe writers after checkout reconstruction"
		}
		return []Condition{
			condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
			condition(ConditionAgentOccupancy, occupancyVerdict, RequirementRequired,
				occupancyEvidence, "select an observable runtime backend before reconstructing the checkout"),
			condition(ConditionSavedRuntime, VerdictMet, RequirementAdvisory,
				"saved runtime hint will be revalidated after reconstruction", ""),
		}
	}
	occupancyErr := s.writerOccupancyError(observed.occupancy, observed.occupancyErr)
	occupancyVerdict := VerdictMet
	occupancyEvidence := "no other recognized agent occupies the checkout"
	if s.allowSharedCheckout && occupancyErr == nil {
		occupancyEvidence = "shared-checkout override accepts the observed or explicitly unobserved runtime occupancy"
	}
	if occupancyErr != nil {
		occupancyVerdict = VerdictBlocked
		occupancyEvidence = occupancyErr.Error()
		if observed.occupancyErr != nil || observed.occupancy.CurrentPane.Err != nil ||
			observed.occupancy.SessionCoverageErr != nil || observed.occupancy.SessionList.Err != nil ||
			observed.occupancy.AgentActivityList.Err != nil || occupancyObservationIncomplete(observed.occupancy) {
			occupancyVerdict = VerdictError
		}
	}
	savedEvidence := "no saved runtime handle"
	if observed.task.RuntimeHandle != "" {
		if observed.savedRuntimeLive {
			savedEvidence = "saved " + observed.runtime.Name() + " handle " + observed.task.RuntimeHandle + " covers the exact checkout"
		} else {
			savedEvidence = "saved runtime handle is stale and will be replaced or cleared"
		}
	}
	return []Condition{
		condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
		condition(ConditionAgentOccupancy, occupancyVerdict, RequirementRequired, occupancyEvidence, "stop the other recognized agent or use another checkout"),
		condition(ConditionSavedRuntime, VerdictMet, RequirementAdvisory, savedEvidence, ""),
	}
}

func resumeNeedsBranchSwitch(observed lifecycleObservation) bool {
	return observed.mode == task.ModeBranch && observed.hasCheckout() && observed.statusErr == nil &&
		!observed.status.Detached && observed.status.Branch != observed.task.Branch
}

func shouldOpenResumeRuntime(observed lifecycleObservation) bool {
	return observed.runtimeErr == nil && observed.runtime != nil && observed.runtime.Name() != "none" && !observed.savedRuntimeLive
}

func resumeEffectCheckout(observed lifecycleObservation) string {
	if observed.hasCheckout() {
		return observed.checkout
	}
	return observed.desiredWorktree
}

func resumeRuntimeLabel(candidate task.Task) string {
	if candidate.EffectiveMode() == task.ModeWorktree {
		return candidate.Repo + "/" + candidate.Branch
	}
	return candidate.Title()
}

func resumeFallback(id string, options ResumeOptions) string {
	parts := []string{"dev", "resume", shellQuote(id)}
	if !options.FetchRefs {
		parts = append(parts, "--fetch=false")
	}
	if options.NoProvision {
		parts = append(parts, "--no-provision")
	}
	if options.TakeOwnership {
		parts = append(parts, "--force")
	}
	return strings.Join(parts, " ")
}
