package taskflow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (s *lifecycleService) parkWarmSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(ParkWarmOptions)
	conditions := s.parkCheckoutConditions(observed, false, options.CommitWIP)

	if observed.statusErr != nil {
		conditions = append(conditions,
			condition(ConditionBranchPublished, VerdictError, RequirementAdvisory, observed.statusErr.Error(), "refresh Git status"),
			condition(ConditionBranchPushed, VerdictError, RequirementAdvisory, observed.statusErr.Error(), "refresh Git status"),
		)
	} else {
		published := "branch has no upstream"
		if observed.status.Published() {
			published = "upstream " + observed.status.Upstream
		}
		conditions = append(conditions,
			condition(ConditionBranchPublished, VerdictMet, RequirementAdvisory, published, "use --push to publish the branch"),
			condition(ConditionBranchPushed, VerdictMet, RequirementAdvisory,
				fmt.Sprintf("ahead=%d behind=%d", observed.status.Ahead, observed.status.Behind),
				"use --push to publish committed work"),
		)
	}

	conditions = append(conditions, s.warmRuntimeConditions(observed, options)...)
	next := observed.desiredNext(options)
	if next == "" {
		conditions = append(conditions, condition(ConditionNextAction, VerdictNeedsInput, RequirementAdvisory,
			"no next action is recorded", "set --next so resuming is cheap"))
	} else {
		conditions = append(conditions, condition(ConditionNextAction, VerdictMet, RequirementAdvisory,
			"next: "+next, ""))
	}

	var effects []Effect
	if options.CommitWIP && observed.statusErr == nil && observed.status.Dirty() {
		effects = append(effects, NewEffect(EffectCommitWIP,
			"checkpoint uncommitted product work", observed.checkout, false, false,
			map[string]string{"message": wipMessage(next)}))
	}
	if options.Push {
		effects = append(effects, NewEffect(EffectPushBranch,
			"push the task branch", observed.task.Branch, false, true,
			map[string]string{"remote": "origin", "checkout": observed.checkout}))
	}
	if shouldCloseWarmRuntime(observed, options) {
		effects = append(effects, NewEffect(EffectCloseRuntime,
			"close runtime sessions covering the checkout", observed.checkout, false, false,
			map[string]string{"backend": runtimeName(observed.runtime)}))
	}
	effects = append(effects, taskUpdateEffect(observed, task.Warm, observed.task.WorktreePath, runtimeDispositionWarm(observed, options), next, desiredNote(observed.task.Note, options.Note)))

	retained := []string{"branch:" + observed.task.Branch}
	if observed.checkout != "" {
		retained = append(retained, "checkout:"+observed.checkout)
	}
	if (options.KeepSession || observed.cleanup.CallerContained) && observed.task.RuntimeHandle != "" {
		retained = append(retained, "runtime:"+observed.task.RuntimeHandle)
	}
	return PlanSpec{
		Authority:         s.baseAuthority(request, observed),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: retained,
		Confirmation:      Confirmation{Kind: ConfirmationApproval, Prompt: "Park this task WARM?"},
		FallbackCommand:   parkFallback(observed.task.ID, false, options),
		Summary:           "Park " + observed.task.Title() + " WARM",
		DisplayedAt:       s.now(),
	}
}

func (s *lifecycleService) parkColdSpec(request Request, observed lifecycleObservation) PlanSpec {
	options := request.Options.(ParkColdOptions)
	conditions := s.parkCheckoutConditions(observed, true, options.CommitWIP)
	conditions = append(conditions, coldPublicationConditions(observed, options)...)

	if observed.mode == task.ModeWorktree {
		switch {
		case !observed.hasCheckout() || !observed.isLinkedWorktree():
			conditions = append(conditions, condition(ConditionArtifactReady, VerdictBlocked, RequirementRequired,
				"artifact readiness requires an exact linked checkout", "restore or reconcile the linked worktree"))
		case observed.artifactErr != nil:
			conditions = append(conditions, condition(ConditionArtifactReady, VerdictError, RequirementRequired,
				observed.artifactErr.Error(), "repair artifact intent observation"))
		case observed.artifact.Ready():
			conditions = append(conditions, condition(ConditionArtifactReady, VerdictMet, RequirementRequired,
				artifactEvidence(observed), ""))
		default:
			conditions = append(conditions, condition(ConditionArtifactReady, VerdictBlocked, RequirementRequired,
				artifactEvidence(observed), "finalize or explicitly discard every artifact intent"))
		}
	} else {
		conditions = append(conditions, condition(ConditionArtifactReady, VerdictMet, RequirementAdvisory,
			"no linked worktree will be removed", ""))
	}
	conditions = append(conditions, s.coldRuntimeConditions(observed)...)

	if observed.mode == task.ModeBranch {
		switch {
		case observed.baseRef == "":
			conditions = append(conditions, condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
				"task has no recorded base and Git has no default branch", "record an explicit base branch"))
		case observed.baseRefExistsErr != nil:
			conditions = append(conditions, condition(ConditionExplicitBase, VerdictError, RequirementRequired,
				observed.baseRefExistsErr.Error(), "repair explicit base ref observation"))
		case !observed.baseRefExists:
			conditions = append(conditions, condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
				"base ref "+observed.baseRef+" does not exist", "restore or choose an existing base branch"))
		default:
			conditions = append(conditions, condition(ConditionExplicitBase, VerdictMet, RequirementRequired,
				"canonical checkout will return to "+observed.baseRef, ""))
		}
	} else {
		conditions = append(conditions, condition(ConditionExplicitBase, VerdictMet, RequirementAdvisory,
			"linked checkout removal retains the task branch", ""))
	}

	next := observed.desiredNext(options)
	if next == "" {
		conditions = append(conditions, condition(ConditionNextAction, VerdictNeedsInput, RequirementAdvisory,
			"no next action is recorded", "set --next so resuming is cheap"))
	} else {
		conditions = append(conditions, condition(ConditionNextAction, VerdictMet, RequirementAdvisory, "next: "+next, ""))
	}

	var effects []Effect
	if options.CommitWIP && observed.statusErr == nil && observed.status.Dirty() {
		effects = append(effects, NewEffect(EffectCommitWIP,
			"checkpoint uncommitted product work", observed.checkout, false, false,
			map[string]string{"message": wipMessage(next)}))
	}
	if options.Push {
		effects = append(effects, NewEffect(EffectPushBranch,
			"push the task branch", observed.task.Branch, false, true,
			map[string]string{"remote": "origin", "checkout": observed.checkout}))
	}
	if observed.runtimeErr == nil && observed.runtime != nil && observed.runtime.Name() != "none" && len(observed.cleanup.Sessions) > 0 {
		effects = append(effects, NewEffect(EffectCloseRuntime,
			"close runtime sessions covering the checkout", observed.checkout, false, false,
			map[string]string{"backend": observed.runtime.Name()}))
	}
	if observed.mode == task.ModeWorktree {
		effects = append(effects, NewEffect(EffectRemoveWorktree,
			"remove the exact linked worktree without force", observed.checkout, true, false,
			map[string]string{
				"repo": observed.repoPath, "branch": observed.task.Branch,
				"head": observed.head, "force": "false",
			}))
	} else {
		effects = append(effects, NewEffect(EffectSwitchBase,
			"switch the canonical checkout back to the explicit base", observed.repoPath, false, false,
			map[string]string{"base": observed.baseRef, "task-branch": observed.task.Branch}))
	}
	effects = append(effects, taskUpdateEffect(observed, task.Cold, "", "clear", next, desiredNote(observed.task.Note, options.Note)))

	return PlanSpec{
		Authority:         s.baseAuthority(request, observed),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: []string{"branch:" + observed.task.Branch},
		Confirmation:      Confirmation{Kind: ConfirmationApproval, Prompt: "Park this task COLD and remove its checkout?"},
		FallbackCommand:   parkFallback(observed.task.ID, true, options),
		Summary:           "Park " + observed.task.Title() + " COLD",
		DisplayedAt:       s.now(),
	}
}

func (s *lifecycleService) parkCheckoutConditions(observed lifecycleObservation, cold, commitWIP bool) []Condition {
	conditions := []Condition{
		condition(ConditionTaskCurrent, VerdictMet, RequirementRequired,
			"task "+observed.task.ID+" revision "+observed.record.Revision, ""),
		condition(ConditionRepoIdentity, VerdictMet, RequirementRequired,
			"Git common directory "+observed.gitCommonDir, ""),
	}

	if !observed.hasCheckout() {
		verdict := VerdictBlocked
		evidence := "the task checkout is not registered"
		if observed.worktreeErr != nil && !errors.Is(observed.worktreeErr, gitx.ErrWorktreeNotFound) {
			verdict = VerdictError
			evidence = observed.worktreeErr.Error()
		}
		conditions = append(conditions,
			condition(ConditionCheckoutPresent, verdict, RequirementRequired, evidence, "restore or reconcile the exact checkout"),
			condition(ConditionCheckoutExact, VerdictBlocked, RequirementRequired, "no exact Git worktree identity", "refresh repository topology"),
			condition(ConditionCheckoutLinked, VerdictBlocked, RequirementRequired, "checkout kind is unknown", "refresh repository topology"),
			condition(ConditionCheckoutUnlocked, VerdictUnknown, requirementFor(cold), "worktree flags are unknown", "refresh repository topology"),
			condition(ConditionCheckoutBranch, VerdictUnknown, RequirementRequired, "checkout branch is unknown", "refresh Git status"),
			condition(ConditionGitStatus, VerdictUnknown, RequirementRequired, "checkout is absent", "restore the checkout"),
			condition(ConditionGitOperation, VerdictUnknown, requirementFor(cold || commitWIP), "checkout is absent", "restore the checkout"),
			condition(ConditionGitConflict, VerdictUnknown, requirementFor(cold || commitWIP), "checkout is absent", "restore the checkout"),
			condition(ConditionCheckoutClean, VerdictUnknown, requirementFor(cold), "checkout is absent", "restore the checkout"),
		)
		return conditions
	}

	conditions = append(conditions, condition(ConditionCheckoutPresent, VerdictMet, RequirementRequired,
		"registered checkout "+observed.checkout, ""))
	exactVerdict, exactEvidence := checkoutExactVerdict(observed)
	conditions = append(conditions, condition(ConditionCheckoutExact, exactVerdict, RequirementRequired,
		exactEvidence, "reconcile the task and Git worktree identities"))

	kindVerdict := VerdictMet
	kindEvidence := "canonical main checkout"
	if observed.mode == task.ModeWorktree {
		kindEvidence = "linked worktree"
		if !observed.isLinkedWorktree() {
			kindVerdict = VerdictBlocked
			kindEvidence = "task checkout resolves to the main or bare worktree"
		}
	} else if !observed.worktree.Worktree.Main || observed.worktree.Worktree.Bare {
		kindVerdict = VerdictBlocked
		kindEvidence = "canonical task does not resolve to Git's main checkout"
	}
	conditions = append(conditions, condition(ConditionCheckoutLinked, kindVerdict, RequirementRequired,
		kindEvidence, "reconcile the checkout mode"))

	flagRequirement := RequirementAdvisory
	if cold && observed.mode == task.ModeWorktree {
		flagRequirement = RequirementRequired
	}
	flagVerdict := VerdictMet
	flagEvidence := "worktree is unlocked and not prunable"
	if observed.worktree.Worktree.Locked || observed.worktree.Worktree.Prunable {
		flagVerdict = VerdictBlocked
		flagEvidence = fmt.Sprintf("locked=%t prunable=%t", observed.worktree.Worktree.Locked, observed.worktree.Worktree.Prunable)
	}
	conditions = append(conditions, condition(ConditionCheckoutUnlocked, flagVerdict, flagRequirement,
		flagEvidence, "unlock or repair the worktree registration"))

	branchVerdict := VerdictMet
	branchEvidence := "checkout is on " + observed.task.Branch
	if observed.worktree.Worktree.Detached || observed.worktree.Worktree.Branch != observed.task.Branch ||
		observed.statusErr == nil && (observed.status.Detached || observed.status.Branch != observed.task.Branch) {
		branchVerdict = VerdictBlocked
		branchEvidence = fmt.Sprintf("registry=%q status=%q expected=%q", observed.worktree.Worktree.Branch, observed.status.Branch, observed.task.Branch)
	}
	conditions = append(conditions, condition(ConditionCheckoutBranch, branchVerdict, RequirementRequired,
		branchEvidence, "switch or reconcile the exact task branch"))

	if observed.statusErr != nil {
		conditions = append(conditions,
			condition(ConditionGitStatus, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionGitOperation, VerdictError, requirementFor(cold || commitWIP), observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionGitConflict, VerdictError, requirementFor(cold || commitWIP), observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionCheckoutClean, VerdictError, requirementFor(cold), observed.statusErr.Error(), "repair Git status observation"),
		)
		return conditions
	}
	conditions = append(conditions, condition(ConditionGitStatus, VerdictMet, RequirementRequired,
		observed.status.Summary(), ""))

	operationRequirement := RequirementAdvisory
	if cold || commitWIP {
		operationRequirement = RequirementRequired
	}
	switch {
	case observed.operationErr != nil:
		conditions = append(conditions, condition(ConditionGitOperation, VerdictError, operationRequirement,
			observed.operationErr.Error(), "repair Git operation observation"))
	case observed.inProgress:
		verdict := VerdictBlocked
		if operationRequirement == RequirementAdvisory {
			verdict = VerdictMet
		}
		conditions = append(conditions, condition(ConditionGitOperation, verdict, operationRequirement,
			"Git operation "+observed.operation+" is in progress", "finish or abort the Git operation"))
	default:
		conditions = append(conditions, condition(ConditionGitOperation, VerdictMet, operationRequirement,
			"no Git operation is in progress", ""))
	}

	conflictRequirement := RequirementAdvisory
	if cold || commitWIP {
		conflictRequirement = RequirementRequired
	}
	conflictVerdict := VerdictMet
	conflictEvidence := "no unmerged paths"
	if observed.status.Conflicted > 0 {
		conflictEvidence = strconv.Itoa(observed.status.Conflicted) + " unmerged path(s)"
		if conflictRequirement == RequirementRequired {
			conflictVerdict = VerdictBlocked
		}
	}
	conditions = append(conditions, condition(ConditionGitConflict, conflictVerdict, conflictRequirement,
		conflictEvidence, "resolve conflicts before checkpointing or cold parking"))

	cleanRequirement := RequirementAdvisory
	if cold {
		cleanRequirement = RequirementRequired
	}
	cleanVerdict := VerdictMet
	cleanEvidence := "checkout is clean"
	if observed.status.Dirty() {
		cleanEvidence = observed.status.Breakdown()
		switch {
		case cold && commitWIP:
			cleanEvidence += "; declared WIP commit will be followed by a fresh clean check"
		case cold:
			cleanVerdict = VerdictBlocked
		default:
			cleanEvidence += "; bytes remain in place"
		}
	}
	conditions = append(conditions, condition(ConditionCheckoutClean, cleanVerdict, cleanRequirement,
		cleanEvidence, "commit changes or request an explicit WIP checkpoint"))
	return conditions
}

func checkoutExactVerdict(observed lifecycleObservation) (Verdict, string) {
	if observed.worktree.GitCommonDir != observed.gitCommonDir {
		return VerdictBlocked, fmt.Sprintf("worktree common directory %q differs from %q", observed.worktree.GitCommonDir, observed.gitCommonDir)
	}
	if observed.mode == task.ModeWorktree {
		if observed.task.WorktreePath == "" {
			if observed.task.State != task.Cold || observed.branchMatches != 1 {
				return VerdictBlocked, "the task has no exact persisted checkout identity"
			}
		} else {
			// ResolveRegisteredWorktree matched the recorded hint by canonical path;
			// subsequent operations use only the returned registered identity.
			if observed.worktree.Path == "" {
				return VerdictBlocked, "the exact registered worktree path is empty"
			}
		}
	} else {
		if observed.task.WorktreePath != "" {
			return VerdictBlocked, fmt.Sprintf("%s task unexpectedly retains worktree path %q", observed.mode, observed.task.WorktreePath)
		}
		if observed.worktree.Path != observed.repoPath {
			return VerdictBlocked, fmt.Sprintf("registered main path %q differs from %q", observed.worktree.Path, observed.repoPath)
		}
	}
	if observed.headErr != nil {
		return VerdictError, observed.headErr.Error()
	}
	if observed.worktree.Worktree.Head != observed.head {
		return VerdictBlocked, fmt.Sprintf("worktree registry HEAD %q differs from checkout HEAD %q", observed.worktree.Worktree.Head, observed.head)
	}
	return VerdictMet, "exact path, Git common directory, and HEAD match"
}

func requirementFor(required bool) Requirement {
	if required {
		return RequirementRequired
	}
	return RequirementAdvisory
}

func (s *lifecycleService) warmRuntimeConditions(observed lifecycleObservation, options ParkWarmOptions) []Condition {
	if options.KeepSession {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictMet, RequirementAdvisory, "runtime is intentionally retained", ""),
			condition(ConditionCleanupOccupancy, VerdictMet, RequirementRequired, "--keep-session requests no runtime closure", ""),
			condition(ConditionCallerContainment, VerdictMet, RequirementAdvisory, "caller containment is irrelevant while retaining the runtime", ""),
		}
	}
	if observed.runtimeErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionCleanupOccupancy, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionCallerContainment, VerdictUnknown, RequirementAdvisory, "runtime was not inspected", "inspect runtime occupancy"),
		}
	}
	if observed.runtime != nil && observed.runtime.Name() == "none" {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictMet, RequirementAdvisory, "runtime=none is explicitly unobserved", ""),
			condition(ConditionCleanupOccupancy, VerdictMet, RequirementRequired, "warm parking does not remove the checkout or require runtime closure", ""),
			condition(ConditionCallerContainment, VerdictMet, RequirementAdvisory, "caller containment is irrelevant without runtime closure", ""),
		}
	}
	availableVerdict := VerdictMet
	availableEvidence := "runtime backend " + observed.runtime.Name() + " is available"
	if observed.runtime.Name() != "none" && !observed.runtimeAvailable {
		availableVerdict = VerdictBlocked
		availableEvidence = "runtime backend " + observed.runtime.Name() + " is unavailable"
	}
	if observed.cleanupErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
			condition(ConditionCleanupOccupancy, VerdictError, RequirementRequired, observed.cleanupErr.Error(), "repair runtime occupancy observation"),
			condition(ConditionCallerContainment, VerdictUnknown, RequirementAdvisory, "runtime occupancy failed", "inspect from outside the checkout"),
		}
	}
	if observed.cleanup.CallerContained {
		return []Condition{
			condition(ConditionRuntimeAvailable, availableVerdict, RequirementAdvisory, availableEvidence, "select an available runtime backend"),
			condition(ConditionCleanupOccupancy, VerdictMet, RequirementRequired,
				"caller is contained; warm parking will not close any runtime", "exit the runtime and let a later sweep close it"),
			condition(ConditionCallerContainment, VerdictMet, RequirementAdvisory,
				"caller remains inside the checkout or its runtime", "runtime will be retained"),
		}
	}
	if len(observed.cleanup.Blockers) > 0 {
		return []Condition{
			condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
			condition(ConditionCleanupOccupancy, VerdictBlocked, RequirementRequired,
				strings.Join(observed.cleanup.Blockers, "; "), "stop active agents or use only the explicit unknown-runtime acknowledgement"),
			condition(ConditionCallerContainment, VerdictMet, RequirementAdvisory, "caller is outside the target runtime", ""),
		}
	}
	return []Condition{
		condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
		condition(ConditionCleanupOccupancy, VerdictMet, RequirementRequired,
			fmt.Sprintf("%d closeable %s session(s)", len(observed.cleanup.Sessions), observed.runtime.Name()), ""),
		condition(ConditionCallerContainment, VerdictMet, RequirementAdvisory, "caller is outside the target runtime", ""),
	}
}

func (s *lifecycleService) coldRuntimeConditions(observed lifecycleObservation) []Condition {
	if observed.runtimeErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionCleanupOccupancy, VerdictError, RequirementRequired, observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionCallerContainment, VerdictUnknown, RequirementRequired, "runtime was not inspected", "run cold parking from outside the checkout"),
		}
	}
	availableVerdict := VerdictMet
	availableEvidence := "runtime backend " + observed.runtime.Name() + " is available"
	if observed.runtime.Name() != "none" && !observed.runtimeAvailable {
		availableVerdict = VerdictBlocked
		availableEvidence = "runtime backend " + observed.runtime.Name() + " is unavailable"
	}
	if observed.cleanupErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
			condition(ConditionCleanupOccupancy, VerdictError, RequirementRequired, observed.cleanupErr.Error(), "repair runtime occupancy observation"),
			condition(ConditionCallerContainment, VerdictUnknown, RequirementRequired, "runtime occupancy failed", "run cold parking from outside the checkout"),
		}
	}
	occupancyVerdict := VerdictMet
	occupancyEvidence := fmt.Sprintf("%d closeable %s session(s)", len(observed.cleanup.Sessions), observed.runtime.Name())
	if len(observed.cleanup.Blockers) > 0 {
		occupancyVerdict = VerdictBlocked
		occupancyEvidence = strings.Join(observed.cleanup.Blockers, "; ")
	}
	callerVerdict := VerdictMet
	callerEvidence := "caller is outside the checkout and runtime"
	if observed.cleanup.CallerContained {
		callerVerdict = VerdictBlocked
		callerEvidence = "caller is inside the checkout or a covering runtime"
	}
	return []Condition{
		condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired, availableEvidence, "select an available runtime backend"),
		condition(ConditionCleanupOccupancy, occupancyVerdict, RequirementRequired, occupancyEvidence, "stop active agents and close mixed runtime panes"),
		condition(ConditionCallerContainment, callerVerdict, RequirementRequired, callerEvidence, "run cold parking from outside the target checkout"),
	}
}

func coldPublicationConditions(observed lifecycleObservation, options ParkColdOptions) []Condition {
	if observed.statusErr != nil {
		return []Condition{
			condition(ConditionBranchPublished, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
			condition(ConditionBranchPushed, VerdictError, RequirementRequired, observed.statusErr.Error(), "repair Git status observation"),
		}
	}
	publishedVerdict := VerdictMet
	publishedEvidence := "upstream " + observed.status.Upstream
	if !observed.status.Published() {
		if options.Push {
			publishedEvidence = "declared push will set origin/" + observed.task.Branch + " as upstream"
		} else {
			publishedVerdict = VerdictBlocked
			publishedEvidence = "branch has no upstream"
		}
	}
	pushedVerdict := VerdictMet
	pushedEvidence := fmt.Sprintf("ahead=%d", observed.status.Ahead)
	willCreateCommit := options.CommitWIP && observed.status.Dirty()
	if observed.status.Ahead > 0 || willCreateCommit {
		if options.Push {
			pushedEvidence = "declared push will be followed by an ahead=0 check"
		} else {
			pushedVerdict = VerdictBlocked
			pushedEvidence = "committed or checkpointed work would remain ahead of upstream"
		}
	}
	return []Condition{
		condition(ConditionBranchPublished, publishedVerdict, RequirementRequired, publishedEvidence, "request --push or publish the branch first"),
		condition(ConditionBranchPushed, pushedVerdict, RequirementRequired, pushedEvidence, "request --push and resolve any rejected push"),
	}
}

func shouldCloseWarmRuntime(observed lifecycleObservation, options ParkWarmOptions) bool {
	return !options.KeepSession && observed.runtimeErr == nil && observed.runtime != nil &&
		observed.runtime.Name() != "none" && !observed.cleanup.CallerContained && len(observed.cleanup.Sessions) > 0
}

func runtimeDispositionWarm(observed lifecycleObservation, options ParkWarmOptions) string {
	if options.KeepSession || observed.cleanup.CallerContained {
		return "retain"
	}
	return "clear"
}

func desiredNote(current, requested string) string {
	if requested != "" {
		return requested
	}
	return current
}

func taskUpdateEffect(observed lifecycleObservation, state task.State, path, runtimeDisposition, next, note string) Effect {
	return NewEffect(EffectUpdateTask, "write lifecycle task state last", observed.task.ID, false, false,
		map[string]string{
			"expected-revision": observed.record.Revision,
			"state":             string(state), "owner": "current-host",
			"worktree-path": path, "runtime": runtimeDisposition,
			"next": next, "note": note,
		})
}

func wipMessage(next string) string {
	if next == "" {
		return "wip: checkpoint"
	}
	return "wip: checkpoint — " + next
}

func artifactEvidence(observed lifecycleObservation) string {
	if observed.artifact.KnownEmpty {
		return "no artifact intents match the exact checkout"
	}
	var states []string
	for _, intent := range observed.artifact.Intents {
		states = append(states, intent.Intent.ID+"="+string(intent.State))
	}
	if len(states) == 0 {
		return "artifact readiness is incomplete"
	}
	return strings.Join(states, ", ")
}

func parkFallback(id string, cold bool, options interface{}) string {
	parts := []string{"dev", "park", shellQuote(id)}
	if cold {
		parts = append(parts, "--cold")
	}
	switch value := options.(type) {
	case ParkWarmOptions:
		parts = appendParkFlags(parts, value.Next, value.Note, value.CommitWIP, value.Push,
			value.CloseUnknown, value.AssumeNoRuntime)
		if value.Timeout > 0 {
			parts = append(parts, "--timeout", value.Timeout.String())
		}
		if value.KeepSession {
			parts = append(parts, "--keep-session")
		}
	case ParkColdOptions:
		parts = appendParkFlags(parts, value.Next, value.Note, value.CommitWIP, value.Push,
			value.CloseUnknown, value.AssumeNoRuntime)
		if value.Timeout > 0 {
			parts = append(parts, "--timeout", value.Timeout.String())
		}
	}
	return strings.Join(parts, " ")
}

func appendParkFlags(parts []string, next, note string, wip, push, closeUnknown, assumeNoRuntime bool) []string {
	if next != "" {
		parts = append(parts, "--next", shellQuote(next))
	}
	if note != "" {
		parts = append(parts, "--note", shellQuote(note))
	}
	if wip {
		parts = append(parts, "--wip")
	}
	if push {
		parts = append(parts, "--push")
	}
	if closeUnknown {
		parts = append(parts, "--close-unknown")
	}
	if assumeNoRuntime {
		parts = append(parts, "--assume-no-runtime")
	}
	return parts
}

func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"`\\$;&|()<>*") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
