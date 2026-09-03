package taskflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (s *lifecycleService) retireHandler() Handler {
	return Handler{
		Plan:  s.planRetire,
		Apply: s.applyRetire,
	}
}

func validateRetireLocator(locator Locator) error {
	if err := validateLifecycleLocator(locator); err != nil {
		return err
	}
	if locator.Mode == task.ModeWorktree && locator.CheckoutPath != "" && locator.HeadOID == "" {
		return fmt.Errorf("%w: exact linked-checkout HEAD is required", ErrInvalidRequest)
	}
	return nil
}

func (s *lifecycleService) planRetire(ctx context.Context, request Request) (PlanSpec, error) {
	if err := contextError(ctx); err != nil {
		return PlanSpec{}, err
	}
	if err := validateRetireLocator(request.Locator); err != nil {
		return PlanSpec{}, err
	}
	record, err := s.tasks.GetRecord(request.Locator.TaskID)
	if err != nil {
		return PlanSpec{}, fmt.Errorf("%w: load exact task %q: %v", ErrInvalidRequest, request.Locator.TaskID, err)
	}
	if err := validateRecordIdentity(request.Locator, *record); err != nil {
		return PlanSpec{}, err
	}
	spec, _, err := s.observeRetire(ctx, request, *record, s.tasks.ListRecords)
	if err != nil {
		return PlanSpec{}, err
	}
	current, err := s.tasks.GetRecord(request.Locator.TaskID)
	if err != nil {
		return PlanSpec{}, &StalePlanError{Reason: "task record disappeared while planning retirement"}
	}
	if current.Revision != record.Revision {
		return PlanSpec{}, staleTaskRevision(record.Revision, current.Revision, "task record changed while planning retirement")
	}
	return spec, nil
}

func (s *lifecycleService) observeRetire(ctx context.Context, request Request, record task.Record, loadTasks taskInventoryLoader) (PlanSpec, destructiveObservation, error) {
	candidate := record.Task
	mode := candidate.EffectiveMode()
	noCheckout := mode == task.ModeWorktree && candidate.WorktreePath == ""
	var (
		rt    runtime.Runtime
		rtErr error
	)
	if noCheckout && candidate.RuntimeName == "" && candidate.RuntimeHandle == "" {
		rt = runtime.None{}
	} else {
		rt, rtErr = s.runtimeFor(candidate)
	}
	options := request.Options.(RetireOptions)
	cleanup := retire.Options{
		CWD: s.cwd, CallerWorkspaceID: s.callerWorkspace, CallerPaneID: s.callerPane,
		CloseUnknown: options.CloseUnknown, AssumeNoRuntime: options.AssumeNoRuntime, Timeout: options.Timeout,
	}
	observed, err := s.inspectDestructive(ctx, destructiveInspectInput{
		locator: request.Locator, base: candidate.Base, runtime: rt, rtErr: rtErr, cleanup: cleanup,
		noCheckout: noCheckout, inspectArtifacts: !noCheckout,
		inspectTasks: true, loadTasks: loadTasks,
	})
	if err != nil {
		return PlanSpec{}, observed, err
	}
	return s.retireSpec(request, record, observed), observed, nil
}

func (s *lifecycleService) retireSpec(request Request, record task.Record, observed destructiveObservation) PlanSpec {
	options := request.Options.(RetireOptions)
	candidate := record.Task
	mode := candidate.EffectiveMode()
	noCheckout := mode == task.ModeWorktree && candidate.WorktreePath == ""
	conditions := []Condition{
		condition(ConditionTaskCurrent, VerdictMet, RequirementRequired,
			"DONE task "+candidate.ID+" revision "+record.Revision, ""),
		retireRepositoryCondition(observed),
	}
	conditions = append(conditions, retireTaskClaimConditions(candidate, observed)...)
	conditions = append(conditions, retireCheckoutConditions(candidate, observed, noCheckout)...)
	conditions = append(conditions, retireRefConditions(candidate, observed, noCheckout)...)
	conditions = append(conditions, retireArtifactCondition(observed, noCheckout))
	conditions = append(conditions, retireRuntimeConditions(candidate, observed, noCheckout)...)
	conditions = append(conditions, retireBranchDeletionCondition(candidate, observed, noCheckout, options))

	var effects []Effect
	if !noCheckout && observed.runtimeErr == nil && observed.cleanupErr == nil && len(observed.cleanup.Sessions) > 0 {
		effects = append(effects, NewEffect(
			EffectCloseRuntime, "close eligible runtime sessions covering the checkout", observed.checkout, true, false,
			map[string]string{"backend": runtimeName(observed.runtime), "sessions": fmt.Sprintf("%d", len(observed.cleanup.Sessions))},
		))
	}
	if mode == task.ModeWorktree && !noCheckout {
		effects = append(effects, NewEffect(
			EffectRemoveWorktree, "remove the exact clean linked worktree without force", observed.checkout, true, false,
			map[string]string{"repo": observed.repoPath, "branch": candidate.Branch, "head": observed.head, "force": "false"},
		))
	}
	if options.DeleteBranch && mode != task.ModeDirect && candidate.Branch != candidate.Base && observed.branchExists {
		effects = append(effects, NewEffect(
			EffectDeleteBranch, "delete the freshly contained local task branch", candidate.Branch, true, false,
			map[string]string{"branch-ref": observed.branchRef, "branch-oid": observed.branchOID, "base-ref": observed.baseRef, "base-oid": observed.baseOID},
		))
	}
	effects = append(effects, NewEffect(
		EffectDeleteTask, "CAS-delete the DONE task record last", candidate.ID, true, false,
		map[string]string{"expected-revision": record.Revision},
	))

	confirmation := Confirmation{Kind: ConfirmationApproval, Prompt: "Retire this DONE task and its eligible local resources?"}
	if options.DeleteBranch && mode != task.ModeDirect && candidate.Branch != candidate.Base && observed.branchExists {
		confirmation = Confirmation{
			Kind: ConfirmationTyped, Token: "DELETE " + candidate.Branch,
			Prompt: "Type DELETE " + candidate.Branch + " to remove the contained local branch under this exact plan",
		}
	}
	retained := retireRetainedResources(candidate, observed, noCheckout, options)
	return PlanSpec{
		Authority:         mergeAuthority(retireTaskAuthority(record), observed.authority()),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: retained,
		Confirmation:      confirmation,
		FallbackCommand:   retireFallback(candidate.ID, options),
		Summary:           "Retire " + candidate.Title(),
		DisplayedAt:       s.now(),
	}
}

func retireTaskAuthority(record task.Record) map[string]string {
	candidate := record.Task
	return map[string]string{
		"task.id": record.Task.ID, "task.revision": record.Revision,
		"task.mode": string(candidate.EffectiveMode()), "task.state": string(candidate.State),
		"task.repo": candidate.Repo, "task.repo-path": candidate.RepoPath,
		"task.branch": candidate.Branch, "task.base": candidate.Base,
		"task.worktree-path": candidate.WorktreePath,
		"task.runtime-name":  candidate.RuntimeName, "task.runtime-handle": candidate.RuntimeHandle,
	}
}

func retireRepositoryCondition(observed destructiveObservation) Condition {
	if observed.repoErr != nil {
		return condition(ConditionRepoIdentity, VerdictError, RequirementRequired,
			observed.repoErr.Error(), "restore the exact repository before retirement")
	}
	return condition(ConditionRepoIdentity, VerdictMet, RequirementRequired,
		"Git main "+observed.repoPath+" with common directory "+observed.gitCommonDir, "")
}

func retireTaskClaimConditions(candidate task.Task, observed destructiveObservation) []Condition {
	inventoryCondition := condition(ConditionTaskInventory, VerdictMet, RequirementRequired,
		"task inventory is complete for retirement claim checks", "")
	switch {
	case observed.taskInventoryErr != nil:
		inventoryCondition = condition(ConditionTaskInventory, VerdictError, RequirementRequired,
			observed.taskInventoryErr.Error(), "repair task-store inventory before retirement")
	case len(observed.taskIncomplete) > 0:
		inventoryCondition = condition(ConditionTaskInventory, VerdictError, RequirementRequired,
			strings.Join(observed.taskIncomplete, "; "), "repair every corrupt or unresolvable task record")
	}

	var conflicting []string
	for _, claim := range observed.taskClaims {
		if claim.ID != candidate.ID {
			conflicting = append(conflicting, claim.ID+" ("+claim.Reason+")")
		}
	}
	claimsCondition := condition(ConditionTaskClaims, VerdictMet, RequirementRequired,
		"only task "+candidate.ID+" claims the retirement branch/checkout", "")
	if len(conflicting) > 0 {
		claimsCondition = condition(ConditionTaskClaims, VerdictBlocked, RequirementRequired,
			"also claimed by "+strings.Join(conflicting, ", "),
			"reconcile every conflicting task before removing shared resources")
	}
	return []Condition{inventoryCondition, claimsCondition}
}

func retireCheckoutConditions(candidate task.Task, observed destructiveObservation, noCheckout bool) []Condition {
	mode := candidate.EffectiveMode()
	if noCheckout {
		paths, err := observed.branchCheckoutPaths(false)
		exactVerdict := VerdictMet
		exactEvidence := "persisted DONE task records no checkout and Git has no checkout for its branch"
		if err != nil {
			exactVerdict = VerdictError
			exactEvidence = err.Error()
		} else if len(paths) > 0 {
			exactVerdict = VerdictBlocked
			exactEvidence = "task branch is still checked out at " + strings.Join(paths, ", ")
		}
		return []Condition{
			condition(ConditionCheckoutPresent, VerdictMet, RequirementRequired,
				"no recorded checkout is the narrow persisted-DONE reap case", ""),
			condition(ConditionCheckoutExact, exactVerdict, RequirementRequired, exactEvidence,
				"reconcile the discovered checkout before retirement"),
			condition(ConditionCheckoutLinked, VerdictMet, RequirementAdvisory,
				"no worktree removal is requested", ""),
			condition(ConditionCheckoutUnlocked, VerdictMet, RequirementAdvisory,
				"no worktree registration will be removed", ""),
			condition(ConditionCheckoutBranch, VerdictMet, RequirementAdvisory,
				"branch identity is proved from the local ref", ""),
			condition(ConditionGitStatus, VerdictMet, RequirementAdvisory,
				"no checkout status is required for record-only reaping", ""),
			condition(ConditionGitOperation, VerdictMet, RequirementAdvisory,
				"no checkout Git operation can be removed", ""),
			condition(ConditionCheckoutClean, VerdictMet, RequirementAdvisory,
				"no checkout bytes will be removed", ""),
			condition(ConditionHarnessOwnership, VerdictMet, RequirementAdvisory,
				"no checkout path is selected", ""),
		}
	}

	if !observed.worktreeFound {
		verdict := VerdictBlocked
		evidence := "the exact recorded checkout is not registered"
		if observed.worktreeErr != nil && !errors.Is(observed.worktreeErr, gitx.ErrWorktreeNotFound) {
			verdict = VerdictError
			evidence = observed.worktreeErr.Error()
		}
		return []Condition{
			condition(ConditionCheckoutPresent, verdict, RequirementRequired, evidence, "restore or reconcile the exact checkout"),
			condition(ConditionCheckoutExact, VerdictUnknown, RequirementRequired, "worktree identity is unavailable", "refresh Git topology"),
			condition(ConditionCheckoutLinked, VerdictUnknown, RequirementRequired, "worktree kind is unavailable", "refresh Git topology"),
			condition(ConditionCheckoutUnlocked, VerdictUnknown, requirementFor(mode == task.ModeWorktree), "worktree flags are unavailable", "refresh Git topology"),
			condition(ConditionCheckoutBranch, VerdictUnknown, requirementFor(mode == task.ModeWorktree), "checkout branch is unavailable", "restore the exact checkout"),
			condition(ConditionGitStatus, VerdictUnknown, requirementFor(mode == task.ModeWorktree), "checkout status is unavailable", "restore the exact checkout"),
			condition(ConditionGitOperation, VerdictUnknown, requirementFor(mode == task.ModeWorktree), "Git operation state is unavailable", "restore the exact checkout"),
			condition(ConditionCheckoutClean, VerdictUnknown, requirementFor(mode == task.ModeWorktree), "checkout cleanliness is unavailable", "restore the exact checkout"),
			condition(ConditionHarnessOwnership, VerdictUnknown, requirementFor(mode == task.ModeWorktree), "checkout ownership is unavailable", "refresh repository topology"),
		}
	}

	exactVerdict := VerdictMet
	exactEvidence := "registered path, repository, Git common directory, and live HEAD are exact"
	switch {
	case observed.worktree.Path != observed.checkout:
		exactVerdict, exactEvidence = VerdictBlocked, "registered path differs from the exact selected checkout"
	case observed.worktree.RepositoryPath != observed.repoPath || observed.worktree.GitCommonDir != observed.gitCommonDir:
		exactVerdict, exactEvidence = VerdictBlocked, "registered repository identity differs from the selected repository"
	case mode == task.ModeWorktree && observed.headErr != nil:
		exactVerdict, exactEvidence = VerdictError, observed.headErr.Error()
	case mode == task.ModeWorktree && observed.worktree.Worktree.Head != observed.head:
		exactVerdict, exactEvidence = VerdictBlocked, fmt.Sprintf("registry HEAD %s differs from live HEAD %s", observed.worktree.Worktree.Head, observed.head)
	case mode == task.ModeWorktree && observed.locator.HeadOID != "" && observed.locator.HeadOID != observed.head:
		exactVerdict, exactEvidence = VerdictBlocked, fmt.Sprintf("selected HEAD %s differs from live HEAD %s", observed.locator.HeadOID, observed.head)
	}

	kindVerdict := VerdictMet
	kindEvidence := "Git main checkout retained"
	if mode == task.ModeWorktree {
		kindEvidence = "registered non-main linked worktree"
		if !observed.worktree.IsLinkedWorktree() {
			kindVerdict = VerdictBlocked
			kindEvidence = "worktree task does not resolve to a non-main linked worktree"
		}
	} else if !observed.worktree.Worktree.Main || observed.worktree.Worktree.Bare || observed.worktree.Path != observed.repoPath {
		kindVerdict = VerdictBlocked
		kindEvidence = "branch/direct task does not resolve to the exact non-bare Git main checkout"
	}

	flagVerdict := VerdictMet
	flagEvidence := "worktree is unlocked and non-prunable"
	flagRequirement := RequirementAdvisory
	if mode == task.ModeWorktree {
		flagRequirement = RequirementRequired
		if observed.worktree.Worktree.Locked || observed.worktree.Worktree.Prunable {
			flagVerdict = VerdictBlocked
			flagEvidence = fmt.Sprintf("locked=%t prunable=%t", observed.worktree.Worktree.Locked, observed.worktree.Worktree.Prunable)
		}
	}

	branchVerdict := VerdictMet
	branchEvidence := "canonical checkout branch is irrelevant because it is retained"
	branchRequirement := RequirementAdvisory
	if mode == task.ModeWorktree {
		branchRequirement = RequirementRequired
		branchEvidence = "registry, status, branch ref, and live HEAD identify " + candidate.Branch
		switch {
		case observed.worktree.Worktree.Detached || observed.worktree.Worktree.Branch == "":
			branchVerdict, branchEvidence = VerdictBlocked, "registered linked worktree is detached or unnamed"
		case observed.statusErr != nil:
			branchVerdict, branchEvidence = VerdictError, observed.statusErr.Error()
		case observed.status.Detached || observed.status.Branch != candidate.Branch || observed.worktree.Worktree.Branch != candidate.Branch:
			branchVerdict, branchEvidence = VerdictBlocked, fmt.Sprintf("registry=%q status=%q expected=%q", observed.worktree.Worktree.Branch, observed.status.Branch, candidate.Branch)
		case observed.branchOIDErr != nil:
			branchVerdict, branchEvidence = VerdictError, observed.branchOIDErr.Error()
		case !observed.branchExists || observed.branchOID != observed.head:
			branchVerdict, branchEvidence = VerdictBlocked, fmt.Sprintf("branch exists=%t branch OID=%q live HEAD=%q", observed.branchExists, observed.branchOID, observed.head)
		}
	}

	statusVerdict := VerdictMet
	statusEvidence := "canonical checkout content is retained"
	statusRequirement := RequirementAdvisory
	operationVerdict := VerdictMet
	operationEvidence := "canonical checkout Git operation is retained"
	operationRequirement := RequirementAdvisory
	cleanVerdict := VerdictMet
	cleanEvidence := "canonical checkout bytes are retained"
	cleanRequirement := RequirementAdvisory
	if mode == task.ModeWorktree {
		statusRequirement, operationRequirement, cleanRequirement = RequirementRequired, RequirementRequired, RequirementRequired
		if observed.statusErr != nil {
			statusVerdict, statusEvidence = VerdictError, observed.statusErr.Error()
			cleanVerdict, cleanEvidence = VerdictError, observed.statusErr.Error()
		} else {
			statusEvidence = observed.status.Summary()
			if observed.status.Dirty() {
				cleanVerdict, cleanEvidence = VerdictBlocked, observed.status.Breakdown()
			} else {
				cleanEvidence = "checkout is clean"
			}
		}
		if observed.operationErr != nil {
			operationVerdict, operationEvidence = VerdictError, observed.operationErr.Error()
		} else if observed.inProgress {
			operationVerdict, operationEvidence = VerdictBlocked, "Git operation "+observed.operation+" is in progress"
		} else {
			operationEvidence = "no Git operation is in progress"
		}
	}

	harnessVerdict := VerdictMet
	harnessEvidence := "checkout has no strict Claude harness ownership evidence"
	harnessRequirement := RequirementAdvisory
	if mode == task.ModeWorktree {
		harnessRequirement = RequirementRequired
		if observed.isHarness {
			harnessVerdict = VerdictBlocked
			harnessEvidence = "checkout is strictly under harness root " + observed.harness.Root
		}
	}
	return []Condition{
		condition(ConditionCheckoutPresent, VerdictMet, RequirementRequired, "registered checkout "+observed.checkout, ""),
		condition(ConditionCheckoutExact, exactVerdict, RequirementRequired, exactEvidence, "refresh the exact checkout identity"),
		condition(ConditionCheckoutLinked, kindVerdict, RequirementRequired, kindEvidence, "reconcile the task checkout mode"),
		condition(ConditionCheckoutUnlocked, flagVerdict, flagRequirement, flagEvidence, "unlock or repair the worktree registration"),
		condition(ConditionCheckoutBranch, branchVerdict, branchRequirement, branchEvidence, "restore the exact named branch and HEAD"),
		condition(ConditionGitStatus, statusVerdict, statusRequirement, statusEvidence, "repair Git status observation"),
		condition(ConditionGitOperation, operationVerdict, operationRequirement, operationEvidence, "finish or abort the Git operation"),
		condition(ConditionCheckoutClean, cleanVerdict, cleanRequirement, cleanEvidence, "commit or preserve every changed byte"),
		condition(ConditionHarnessOwnership, harnessVerdict, harnessRequirement, harnessEvidence, "let the harness retire its own checkout"),
	}
}

func retireRefConditions(candidate task.Task, observed destructiveObservation, noCheckout bool) []Condition {
	baseCondition := condition(ConditionExplicitBase, VerdictMet, RequirementRequired,
		fmt.Sprintf("local base %s at %s", candidate.Base, observed.baseOID), "")
	switch {
	case candidate.Base == "":
		baseCondition = condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"DONE retirement requires the task's explicit local base", "record the exact local base branch")
	case observed.baseOIDErr != nil:
		baseCondition = condition(ConditionExplicitBase, VerdictError, RequirementRequired,
			observed.baseOIDErr.Error(), "repair local base observation")
	case !observed.baseExists:
		baseCondition = condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"local base ref "+observed.baseRef+" does not exist", "restore the named local base branch")
	case observed.baseOID == "":
		baseCondition = condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"local base resolved to no commit", "restore the named local base branch")
	}

	branchCondition := condition(ConditionBranchRef, VerdictMet, RequirementRequired,
		fmt.Sprintf("local branch %s at %s", candidate.Branch, observed.branchOID), "")
	if observed.branchOIDErr != nil {
		branchCondition = condition(ConditionBranchRef, VerdictError, RequirementRequired,
			observed.branchOIDErr.Error(), "repair local task branch observation")
	} else if !observed.branchExists {
		if noCheckout {
			branchCondition = condition(ConditionBranchRef, VerdictMet, RequirementRequired,
				"local task branch is already absent for a persisted DONE task with no checkout", "")
		} else {
			branchCondition = condition(ConditionBranchRef, VerdictBlocked, RequirementRequired,
				"local task branch "+observed.branchRef+" is missing", "restore the task branch before retiring its checkout")
		}
	} else if observed.branchOID == "" {
		branchCondition = condition(ConditionBranchRef, VerdictBlocked, RequirementRequired,
			"local task branch resolved to no commit", "restore the local task branch")
	}

	relationCondition := condition(ConditionBranchRelation, VerdictMet, RequirementRequired,
		fmt.Sprintf("%s (%s) is contained in %s (%s)", candidate.Branch, observed.branchOID, candidate.Base, observed.baseOID), "")
	switch {
	case observed.branchOIDErr != nil || observed.baseOIDErr != nil:
		relationCondition = condition(ConditionBranchRelation, VerdictError, RequirementRequired,
			"branch or base ref observation failed", "repair exact local ref observation")
	case !observed.branchExists && noCheckout:
		relationCondition = condition(ConditionBranchRelation, VerdictMet, RequirementRequired,
			"missing branch is accepted only because persisted DONE records no checkout", "")
	case observed.containedErr != nil:
		relationCondition = condition(ConditionBranchRelation, VerdictError, RequirementRequired,
			observed.containedErr.Error(), "repair local containment observation")
	case observed.branchExists && observed.baseExists && observed.branchOIDErr == nil && observed.baseOIDErr == nil && !observed.contained:
		relationCondition = condition(ConditionBranchRelation, VerdictBlocked, RequirementRequired,
			fmt.Sprintf("%s (%s) is not contained in %s (%s)", candidate.Branch, observed.branchOID, candidate.Base, observed.baseOID),
			"integrate the task branch into the named local base first")
	case !observed.branchExists || !observed.baseExists || observed.branchOID == "" || observed.baseOID == "":
		relationCondition = condition(ConditionBranchRelation, VerdictUnknown, RequirementRequired,
			"containment cannot be proved from exact local branch and base OIDs", "restore both local refs")
	}
	return []Condition{baseCondition, branchCondition, relationCondition}
}

func retireArtifactCondition(observed destructiveObservation, noCheckout bool) Condition {
	if noCheckout {
		return condition(ConditionArtifactReady, VerdictMet, RequirementAdvisory,
			"no checkout is recorded for artifact inspection", "")
	}
	switch {
	case !observed.worktreeFound:
		return condition(ConditionArtifactReady, VerdictUnknown, RequirementRequired,
			"artifact readiness requires the exact linked checkout", "restore the checkout")
	case observed.artifactErr != nil:
		return condition(ConditionArtifactReady, VerdictError, RequirementRequired,
			observed.artifactErr.Error(), "repair artifact readiness observation")
	case !observed.artifact.Ready():
		return condition(ConditionArtifactReady, VerdictBlocked, RequirementRequired,
			artifactEvidenceFromInspection(observed.artifact), "finalize or explicitly discard every artifact intent")
	default:
		return condition(ConditionArtifactReady, VerdictMet, RequirementRequired,
			artifactEvidenceFromInspection(observed.artifact), "")
	}
}

func retireRuntimeConditions(candidate task.Task, observed destructiveObservation, noCheckout bool) []Condition {
	if noCheckout {
		if candidate.RuntimeName != "" || candidate.RuntimeHandle != "" {
			return []Condition{
				condition(ConditionRuntimeAvailable, VerdictBlocked, RequirementRequired,
					"task has a runtime hint but no exact checkout", "reconcile or clear the runtime hint outside retirement"),
				condition(ConditionCleanupOccupancy, VerdictUnknown, RequirementRequired,
					"runtime coverage cannot be tied to an exact checkout", "reconcile the missing checkout"),
				condition(ConditionCallerContainment, VerdictUnknown, RequirementRequired,
					"caller containment cannot be inspected without a checkout", "reconcile the missing checkout"),
			}
		}
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictMet, RequirementAdvisory, "no runtime hint is recorded", ""),
			condition(ConditionCleanupOccupancy, VerdictMet, RequirementRequired, "no checkout or runtime hint requires cleanup", ""),
			condition(ConditionCallerContainment, VerdictMet, RequirementRequired, "no checkout can contain the caller", ""),
		}
	}
	return destructiveRuntimeConditions(observed)
}

func destructiveRuntimeConditions(observed destructiveObservation) []Condition {
	if observed.runtimeErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionCleanupOccupancy, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime observation"),
			condition(ConditionCallerContainment, VerdictUnknown, RequirementRequired, "runtime was not inspected", "run cleanup from outside the checkout"),
		}
	}
	availableEvidence := "runtime backend " + runtimeName(observed.runtime) + " is available"
	if observed.runtime != nil && observed.runtime.Name() != "none" && !observed.runtimeAvailable {
		availableEvidence = "runtime backend " + observed.runtime.Name() + " reports unavailable; cleanup evidence still controls"
	}
	if observed.cleanupErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictMet, RequirementAdvisory, availableEvidence, "select an available backend or use the explicit external acknowledgement"),
			condition(ConditionCleanupOccupancy, VerdictError, RequirementRequired, observed.cleanupErr.Error(), "repair runtime enumeration or use the explicit external acknowledgement"),
			condition(ConditionCallerContainment, VerdictUnknown, RequirementRequired, "runtime occupancy failed", "run cleanup from outside the checkout"),
		}
	}
	occupancyVerdict := VerdictMet
	occupancyEvidence := fmt.Sprintf("%d eligible runtime session(s)", len(observed.cleanup.Sessions))
	if observed.cleanup.RuntimeUnknown {
		occupancyEvidence = "runtime enumeration failed and the external assume-no-runtime acknowledgement is recorded"
	}
	if len(observed.cleanup.Blockers) > 0 {
		occupancyVerdict = VerdictBlocked
		occupancyEvidence = strings.Join(observed.cleanup.Blockers, "; ")
	}
	callerVerdict := VerdictMet
	callerEvidence := "caller is outside the target checkout and covering runtimes"
	if observed.cleanup.CallerContained {
		callerVerdict = VerdictBlocked
		callerEvidence = "caller is inside the target checkout or a covering runtime"
	}
	return []Condition{
		condition(ConditionRuntimeAvailable, VerdictMet, RequirementAdvisory, availableEvidence, ""),
		condition(ConditionCleanupOccupancy, occupancyVerdict, RequirementRequired, occupancyEvidence, "stop active agents and leave mixed/caller runtimes intact"),
		condition(ConditionCallerContainment, callerVerdict, RequirementRequired, callerEvidence, "run cleanup from outside the target checkout and runtime"),
	}
}

func retireBranchDeletionCondition(candidate task.Task, observed destructiveObservation, noCheckout bool, options RetireOptions) Condition {
	if !options.DeleteBranch {
		return condition(ConditionBranchDeletion, VerdictMet, RequirementAdvisory,
			"local task branch will be retained", "")
	}
	if candidate.EffectiveMode() == task.ModeDirect {
		return condition(ConditionBranchDeletion, VerdictBlocked, RequirementRequired,
			"direct tasks never delete their branch", "retire without branch deletion")
	}
	if candidate.Branch == candidate.Base {
		return condition(ConditionBranchDeletion, VerdictBlocked, RequirementRequired,
			"task branch is the explicit base branch", "retire without deleting the base")
	}
	if !observed.branchExists && noCheckout {
		return condition(ConditionBranchDeletion, VerdictMet, RequirementRequired,
			"local task branch is already absent", "")
	}
	paths, err := observed.branchCheckoutPaths(candidate.EffectiveMode() == task.ModeWorktree && !noCheckout)
	if err != nil {
		return condition(ConditionBranchDeletion, VerdictError, RequirementRequired,
			err.Error(), "refresh registered worktrees before branch deletion")
	}
	if len(paths) > 0 {
		return condition(ConditionBranchDeletion, VerdictBlocked, RequirementRequired,
			"branch is checked out at "+strings.Join(paths, ", "), "switch or remove those exact checkouts first")
	}
	return condition(ConditionBranchDeletion, VerdictMet, RequirementRequired,
		"ordinary git branch -d will delete only the freshly contained local branch", "")
}

func retireRetainedResources(candidate task.Task, observed destructiveObservation, noCheckout bool, options RetireOptions) []string {
	var retained []string
	if observed.branchExists && (!options.DeleteBranch || candidate.EffectiveMode() == task.ModeDirect) {
		retained = append(retained, "branch:"+candidate.Branch)
	}
	if candidate.EffectiveMode() != task.ModeWorktree && !noCheckout && observed.checkout != "" {
		retained = append(retained, "checkout:"+observed.checkout)
	}
	return retained
}

func retireFallback(id string, options RetireOptions) string {
	parts := []string{"dev", "retire", shellQuote(id)}
	if options.DeleteBranch {
		parts = append(parts, "--delete-branch")
	}
	if options.CloseUnknown {
		parts = append(parts, "--close-unknown")
	}
	if options.AssumeNoRuntime {
		parts = append(parts, "--assume-no-runtime")
	}
	if options.Timeout > 0 {
		parts = append(parts, "--timeout", options.Timeout.String())
	}
	return strings.Join(parts, " ")
}

func (s *lifecycleService) applyRetire(ctx context.Context, approved Plan) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if approved.Action != Retire {
		return Result{}, &InvalidPlanError{PlanID: approved.PlanID, Reason: "retire handler received " + string(approved.Action)}
	}
	if err := validateRetireLocator(approved.Locator); err != nil {
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
				return &StalePlanError{ExpectedPlanID: approved.PlanID, Reason: "task record disappeared before retirement apply"}
			}
			if identityErr := validateRecordIdentity(approved.Locator, *record); identityErr != nil {
				return identityErr
			}
			spec, observed, observeErr := s.observeRetire(ctx, approved.Request, *record, tx.ListRecords)
			if observeErr != nil {
				return observeErr
			}
			fresh, buildErr := BuildPlan(approved.Request, spec)
			if buildErr != nil {
				return buildErr
			}
			current, reloadErr := tx.GetRecord(approved.Locator.TaskID)
			if reloadErr != nil || current.Revision != record.Revision {
				actual := ""
				if current != nil {
					actual = current.Revision
				}
				return staleTaskRevision(record.Revision, actual, "task record changed during locked retirement replan")
			}
			if fresh.PlanID != approved.PlanID || fresh.AuthorityFingerprint != approved.AuthorityFingerprint {
				return &StalePlanError{
					ExpectedPlanID: approved.PlanID, ActualPlanID: fresh.PlanID,
					ExpectedAuthorityFingerprint: approved.AuthorityFingerprint,
					ActualAuthorityFingerprint:   fresh.AuthorityFingerprint,
					Reason:                       "fresh retirement task, Git, worktree, artifact, runtime, or ref authority differs",
				}
			}
			if fresh.Availability != AvailabilityReady {
				notReady := &PlanNotReadyError{PlanID: fresh.PlanID, Availability: fresh.Availability, conditions: fresh.Conditions()}
				return &InvalidPlanError{PlanID: fresh.PlanID, Reason: "fresh retirement conditions are not ready", Cause: notReady}
			}

			execution := &executionState{service: s, plan: fresh, tx: tx, revision: record.Revision}
			result, observeErr = execution.executeRetire(ctx, *record, observed)
			return observeErr
		})
	})
	return result, err
}

func (e *executionState) executeRetire(ctx context.Context, record task.Record, baseline destructiveObservation) (Result, error) {
	options := e.plan.Request.Options.(RetireOptions)
	candidate := record.Task
	mode := candidate.EffectiveMode()
	noCheckout := mode == task.ModeWorktree && candidate.WorktreePath == ""
	closedRuntime := false
	removedWorktree := false
	deletedBranch := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectCloseRuntime:
			fresh, err := e.reinspectRetire(ctx, record, baseline, false)
			if err != nil {
				return e.fail(err, "refresh retirement before closing any runtime")
			}
			baseline = fresh
			err = e.run(effect, func() (string, error) {
				inspection, closeErr := e.service.closeAndWait(ctx, baseline.runtime, baseline.checkout, retire.Options{
					CWD: e.service.cwd, CallerWorkspaceID: e.service.callerWorkspace, CallerPaneID: e.service.callerPane,
					CloseUnknown: options.CloseUnknown, AssumeNoRuntime: options.AssumeNoRuntime, Timeout: options.Timeout,
				})
				if closeErr != nil {
					return "runtime closure may be partial", closeErr
				}
				return fmt.Sprintf("closed %d %s session(s)", inspection.ClosedSessions, runtimeName(baseline.runtime)), nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "the task and checkout remain; inspect runtime occupancy before retrying")
			}
			fresh, err = e.reinspectRetire(ctx, record, baseline, true)
			if err != nil {
				return e.fail(err, "runtime state changed after closure; preserve the checkout and refresh retirement")
			}
			baseline = fresh
			closedRuntime = true

		case EffectRemoveWorktree:
			fresh, err := e.reinspectRetire(ctx, record, baseline, closedRuntime)
			if err != nil {
				return e.fail(err, "the worktree was preserved; refresh every retirement safety source")
			}
			baseline = fresh
			err = e.run(effect, func() (string, error) {
				if removeErr := e.service.removeWorktree(ctx, baseline.repoPath, baseline.checkout, false); removeErr != nil {
					return "worktree removal may be partial", fmt.Errorf("remove exact worktree %s: %w", baseline.checkout, removeErr)
				}
				return "removed exact linked worktree without force; branch retained", nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "inspect Git worktree registration and retained branch before retrying")
			}
			removedWorktree = true
			if err := e.verifyRemovedCheckout(ctx, baseline); err != nil {
				return e.fail(err, "the checkout removal completed; restore or reconcile the exact retained branch before reaping the task")
			}

		case EffectDeleteBranch:
			if mode == task.ModeWorktree && !noCheckout && !removedWorktree {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "branch deletion reached before linked worktree removal"})
			}
			if mode != task.ModeWorktree || noCheckout {
				fresh, err := e.reinspectRetire(ctx, record, baseline, closedRuntime)
				if err != nil {
					return e.fail(err, "refresh retirement before deleting the contained branch")
				}
				baseline = fresh
			}
			if err := e.revalidateRetireRefs(ctx, baseline, false); err != nil {
				return e.fail(err, "branch or base authority changed; refresh retirement")
			}
			err := e.run(effect, func() (string, error) {
				_, deleteErr := e.service.gitRun(ctx, baseline.repoPath, "branch", "-d", "--", candidate.Branch)
				if deleteErr != nil {
					return "ordinary contained-branch deletion failed", fmt.Errorf("delete contained branch %s: %w", candidate.Branch, deleteErr)
				}
				return "deleted contained local branch " + candidate.Branch, nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "the task record remains DONE; inspect the local branch and retry")
			}
			deletedBranch = true
			exists, observeErr := e.service.gitRefState(ctx, baseline.repoPath, baseline.branchRef)
			if observeErr != nil {
				return e.fail(fmt.Errorf("verify task branch deletion: %w", observeErr), "the task remains DONE; inspect Git refs before retrying")
			}
			if exists {
				return e.fail(errors.New("local branch still exists after successful branch deletion"), "the task remains DONE; inspect Git refs before retrying")
			}
			if err := e.revalidateRetireBase(ctx, baseline); err != nil {
				return e.fail(err, "the branch deletion completed; restore or inspect the explicit base before task reaping")
			}

		case EffectDeleteTask:
			if mode == task.ModeWorktree && !noCheckout && !removedWorktree {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "task deletion reached before linked worktree removal"})
			}
			if err := e.revalidateRetireTerminal(ctx, record, baseline, closedRuntime, removedWorktree, deletedBranch); err != nil {
				return e.fail(err, "the task remains DONE; refresh retirement from current Git/runtime state")
			}
			err := e.run(effect, func() (string, error) {
				if deleteErr := e.service.taskDelete(e.tx, candidate.ID, record.Revision); deleteErr != nil {
					return "DONE task reap failed", fmt.Errorf("delete retired task %s: %w", candidate.ID, deleteErr)
				}
				if _, verifyErr := e.tx.GetRecord(candidate.ID); !errors.Is(verifyErr, task.ErrNotFound) {
					if verifyErr == nil {
						verifyErr = errors.New("task record still exists")
					}
					return "DONE task reap was not verified", fmt.Errorf("verify retired task %s deletion: %w", candidate.ID, verifyErr)
				}
				return "reaped DONE task at revision " + record.Revision, nil
			})
			if err != nil {
				if _, verifyErr := e.tx.GetRecord(candidate.ID); errors.Is(verifyErr, task.ErrNotFound) {
					e.partial = true
				}
				return e.fail(err, "local cleanup may already be complete; reload and retry the CAS task reap")
			}
			e.snapshot = "task:deleted:" + candidate.ID + ":" + record.Revision
			e.milestone = MilestoneRetired

		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected Retire effect " + string(effect.Code)})
		}
	}
	if e.milestone != MilestoneRetired {
		return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "retirement ended without CAS task deletion"})
	}
	return e.result(), nil
}

func (e *executionState) reinspectRetire(ctx context.Context, record task.Record, baseline destructiveObservation, allowRuntimeChange bool) (destructiveObservation, error) {
	current, err := e.tx.GetRecord(record.Task.ID)
	if err != nil || current.Revision != record.Revision {
		actual := ""
		if current != nil {
			actual = current.Revision
		}
		return destructiveObservation{}, staleTaskRevision(record.Revision, actual, "task changed at a retirement boundary")
	}
	spec, fresh, err := e.service.observeRetire(ctx, e.plan.Request, *current, e.tx.ListRecords)
	if err != nil {
		return fresh, err
	}
	if err := destructiveConditionsReady(spec.Conditions); err != nil {
		return fresh, err
	}
	for _, check := range []struct {
		name          string
		before, after string
	}{
		{"repository", baseline.repositoryAuthority(), fresh.repositoryAuthority()},
		{"checkout", baseline.checkoutAuthority(), fresh.checkoutAuthority()},
		{"Git", baseline.gitAuthority(), fresh.gitAuthority()},
		{"refs", baseline.refsAuthority(), fresh.refsAuthority()},
		{"artifact", artifactAuthority(baseline.artifact, baseline.artifactErr), artifactAuthority(fresh.artifact, fresh.artifactErr)},
		{"harness", authorityHash("harness", boolString(baseline.isHarness), baseline.harness.Root), authorityHash("harness", boolString(fresh.isHarness), fresh.harness.Root)},
		{"task inventory", baseline.taskInventoryAuthority(), fresh.taskInventoryAuthority()},
		{"worktree list", baseline.worktreeListAuthority(), fresh.worktreeListAuthority()},
	} {
		if err := compareAuthorityCategory(check.name, check.before, check.after); err != nil {
			return fresh, err
		}
	}
	if allowRuntimeChange {
		if fresh.cleanupErr != nil || !fresh.cleanup.Ready() || fresh.cleanup.CallerContained || len(fresh.cleanup.Sessions) > 0 {
			return fresh, staleBoundary("runtime coverage is not proven empty after closure")
		}
	} else if err := compareAuthorityCategory("runtime", cleanupAuthority(baseline.cleanup, baseline.cleanupErr), cleanupAuthority(fresh.cleanup, fresh.cleanupErr)); err != nil {
		return fresh, err
	}
	return fresh, nil
}

func (e *executionState) verifyRemovedCheckout(ctx context.Context, baseline destructiveObservation) error {
	_, err := e.service.resolveWorktree(ctx, baseline.repoPath, baseline.checkout)
	if err == nil {
		return staleBoundary("exact worktree remains registered after removal")
	}
	if !errors.Is(err, gitx.ErrWorktreeNotFound) {
		return fmt.Errorf("verify exact worktree removal: %w", err)
	}
	if err := e.revalidateRetireRefs(ctx, baseline, false); err != nil {
		return err
	}
	return nil
}

func (e *executionState) revalidateRetireRefs(ctx context.Context, baseline destructiveObservation, allowMissingBranch bool) error {
	fresh := destructiveObservation{repoPath: baseline.repoPath}
	fresh.observeRefs(ctx, e.service, baseline.locator.Branch, strings.TrimPrefix(baseline.baseRef, "refs/heads/"))
	if err := compareAuthorityCategory("explicit base ref", authorityHash("base", baseline.baseRef, boolString(baseline.baseExists), baseline.baseOID, errorString(baseline.baseOIDErr)),
		authorityHash("base", fresh.baseRef, boolString(fresh.baseExists), fresh.baseOID, errorString(fresh.baseOIDErr))); err != nil {
		return err
	}
	if allowMissingBranch && fresh.branchOIDErr == nil && !fresh.branchExists {
		return nil
	}
	if err := compareAuthorityCategory("task branch ref", authorityHash("branch", baseline.branchRef, boolString(baseline.branchExists), baseline.branchOID, errorString(baseline.branchOIDErr)),
		authorityHash("branch", fresh.branchRef, boolString(fresh.branchExists), fresh.branchOID, errorString(fresh.branchOIDErr))); err != nil {
		return err
	}
	if fresh.containedErr != nil || !fresh.contained {
		return staleBoundary("fresh branch containment proof failed")
	}
	return nil
}

func (e *executionState) revalidateRetireBase(ctx context.Context, baseline destructiveObservation) error {
	fresh := destructiveObservation{repoPath: baseline.repoPath}
	fresh.observeRefs(ctx, e.service, "", strings.TrimPrefix(baseline.baseRef, "refs/heads/"))
	return compareAuthorityCategory("explicit base ref",
		authorityHash("base", baseline.baseRef, boolString(baseline.baseExists), baseline.baseOID, errorString(baseline.baseOIDErr)),
		authorityHash("base", fresh.baseRef, boolString(fresh.baseExists), fresh.baseOID, errorString(fresh.baseOIDErr)),
	)
}

func (e *executionState) revalidateRetireTerminal(ctx context.Context, record task.Record, baseline destructiveObservation, closedRuntime, removedWorktree, deletedBranch bool) error {
	current, err := e.tx.GetRecord(record.Task.ID)
	if err != nil || current.Revision != record.Revision {
		actual := ""
		if current != nil {
			actual = current.Revision
		}
		return staleTaskRevision(record.Revision, actual, "task changed before terminal retirement reap")
	}
	mode := record.Task.EffectiveMode()
	noCheckout := mode == task.ModeWorktree && record.Task.WorktreePath == ""
	if removedWorktree {
		if _, resolveErr := e.service.resolveWorktree(ctx, baseline.repoPath, baseline.checkout); resolveErr == nil {
			return staleBoundary("removed linked checkout became registered again")
		} else if !errors.Is(resolveErr, gitx.ErrWorktreeNotFound) {
			return resolveErr
		}
		if deletedBranch {
			exists, observeErr := e.service.gitRefState(ctx, baseline.repoPath, baseline.branchRef)
			if observeErr != nil {
				return fmt.Errorf("verify deleted task branch remains absent: %w", observeErr)
			}
			if exists {
				return staleBoundary("deleted task branch reappeared")
			}
			return e.revalidateRetireBase(ctx, baseline)
		}
		return e.revalidateRetireRefs(ctx, baseline, noCheckout && !baseline.branchExists)
	}
	if deletedBranch {
		return e.revalidateRetireAfterBranchDelete(ctx, record, baseline)
	}
	_, err = e.reinspectRetire(ctx, record, baseline, closedRuntime)
	return err
}

func (e *executionState) revalidateRetireAfterBranchDelete(ctx context.Context, record task.Record, baseline destructiveObservation) error {
	exists, err := e.service.gitRefState(ctx, baseline.repoPath, baseline.branchRef)
	if err != nil {
		return fmt.Errorf("verify deleted task branch remains absent: %w", err)
	}
	if exists {
		return staleBoundary("deleted task branch reappeared")
	}
	if err := e.revalidateRetireBase(ctx, baseline); err != nil {
		return err
	}
	_, fresh, err := e.service.observeRetire(ctx, e.plan.Request, record, e.tx.ListRecords)
	if err != nil {
		return err
	}
	for _, check := range []struct {
		name          string
		before, after string
	}{
		{"repository", baseline.repositoryAuthority(), fresh.repositoryAuthority()},
		{"checkout", baseline.checkoutAuthority(), fresh.checkoutAuthority()},
		{"Git", baseline.gitAuthority(), fresh.gitAuthority()},
		{"artifact", artifactAuthority(baseline.artifact, baseline.artifactErr), artifactAuthority(fresh.artifact, fresh.artifactErr)},
		{"harness", authorityHash("harness", boolString(baseline.isHarness), baseline.harness.Root), authorityHash("harness", boolString(fresh.isHarness), fresh.harness.Root)},
		{"task inventory", baseline.taskInventoryAuthority(), fresh.taskInventoryAuthority()},
		{"runtime", cleanupAuthority(baseline.cleanup, baseline.cleanupErr), cleanupAuthority(fresh.cleanup, fresh.cleanupErr)},
	} {
		if err := compareAuthorityCategory(check.name, check.before, check.after); err != nil {
			return err
		}
	}
	return nil
}
