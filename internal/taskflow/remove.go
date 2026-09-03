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

func (s *lifecycleService) removeCheckoutHandler() Handler {
	return Handler{
		Plan:  s.planRemoveCheckout,
		Apply: s.applyRemoveCheckout,
	}
}

func validateRemoveCheckoutLocator(locator Locator) error {
	switch {
	case locator.TaskID != "":
		return fmt.Errorf("%w: unmanaged removal must not carry a TaskID", ErrInvalidRequest)
	case locator.TaskRevision != "":
		return fmt.Errorf("%w: unmanaged removal must not carry a task revision", ErrInvalidRequest)
	case locator.RepoPath == "":
		return fmt.Errorf("%w: exact repository path is required", ErrInvalidRequest)
	case locator.GitCommonDir == "":
		return fmt.Errorf("%w: exact Git common directory is required", ErrInvalidRequest)
	case locator.CheckoutPath == "":
		return fmt.Errorf("%w: exact checkout path is required", ErrInvalidRequest)
	case locator.Branch == "":
		return fmt.Errorf("%w: exact named branch is required", ErrInvalidRequest)
	case locator.HeadOID == "":
		return fmt.Errorf("%w: exact checkout HEAD is required", ErrInvalidRequest)
	case strings.TrimSpace(locator.Branch) != locator.Branch || strings.ContainsRune(locator.Branch, '\x00'):
		return fmt.Errorf("%w: exact branch is not normalized", ErrInvalidRequest)
	}
	return nil
}

func (s *lifecycleService) planRemoveCheckout(ctx context.Context, request Request) (PlanSpec, error) {
	if err := contextError(ctx); err != nil {
		return PlanSpec{}, err
	}
	if err := validateRemoveCheckoutLocator(request.Locator); err != nil {
		return PlanSpec{}, err
	}
	spec, _, err := s.observeRemoveCheckout(ctx, request, s.tasks.ListRecords)
	return spec, err
}

func (s *lifecycleService) observeRemoveCheckout(ctx context.Context, request Request, loader taskInventoryLoader) (PlanSpec, destructiveObservation, error) {
	options := request.Options.(RemoveCheckoutOptions)
	rt, rtErr := s.runtimeForUnmanaged()
	cleanup := retire.Options{
		CWD: s.cwd, CallerWorkspaceID: s.callerWorkspace, CallerPaneID: s.callerPane,
		CloseUnknown: options.CloseUnknown, AssumeNoRuntime: options.AssumeNoRuntime, Timeout: options.Timeout,
	}
	observed, err := s.inspectDestructive(ctx, destructiveInspectInput{
		locator: request.Locator, base: options.ContainmentBase,
		runtime: rt, rtErr: rtErr, cleanup: cleanup,
		inspectArtifacts: true, inspectContent: options.DiscardDirty,
		inspectTasks: true, loadTasks: loader,
	})
	if err != nil {
		return PlanSpec{}, observed, err
	}
	return s.removeCheckoutSpec(request, observed), observed, nil
}

func (s *lifecycleService) runtimeForUnmanaged() (runtime.Runtime, error) {
	rt := s.defaultRuntime()
	if rt == nil || strings.TrimSpace(rt.Name()) == "" {
		return nil, errors.New("taskflow runtime resolver returned no backend")
	}
	return rt, nil
}

func (s *lifecycleService) removeCheckoutSpec(request Request, observed destructiveObservation) PlanSpec {
	options := request.Options.(RemoveCheckoutOptions)
	conditions := []Condition{retireRepositoryCondition(observed)}
	conditions = append(conditions, removeCheckoutIdentityConditions(observed, options)...)
	conditions = append(conditions, removeTaskInventoryConditions(observed)...)
	conditions = append(conditions, removeContainmentConditions(observed, options)...)
	conditions = append(conditions, removeArtifactCondition(observed))
	conditions = append(conditions, destructiveRuntimeConditions(observed)...)

	var effects []Effect
	if options.DiscardDirty && observed.statusErr == nil && observed.status.Dirty() {
		effects = append(effects, NewEffect(
			EffectDiscardAll, "discard every staged, unstaged, and non-ignored untracked change", observed.checkout, true, false,
			map[string]string{
				"scope": "all", "head": observed.head, "branch": observed.locator.Branch,
				"content-fingerprint": observed.contentFingerprint,
			},
		))
	}
	if observed.runtimeErr == nil && observed.cleanupErr == nil && len(observed.cleanup.Sessions) > 0 {
		effects = append(effects, NewEffect(
			EffectCloseRuntime, "close eligible runtime sessions covering the unmanaged checkout", observed.checkout, true, false,
			map[string]string{"backend": runtimeName(observed.runtime), "sessions": fmt.Sprintf("%d", len(observed.cleanup.Sessions))},
		))
	}
	effects = append(effects, NewEffect(
		EffectRemoveWorktree, "remove the exact clean unmanaged linked checkout without force", observed.checkout, true, false,
		map[string]string{"repo": observed.repoPath, "branch": observed.locator.Branch, "head": observed.locator.HeadOID, "force": "false", "preserve-branch": "true"},
	))
	if options.DeleteContainedBranch {
		effects = append(effects, NewEffect(
			EffectDeleteBranch, "delete the freshly contained unmanaged branch under the same lock", observed.locator.Branch, true, false,
			map[string]string{"branch-oid": observed.branchOID, "base": options.ContainmentBase, "base-oid": observed.baseOID},
		))
	}

	confirmation := Confirmation{Kind: ConfirmationApproval, Prompt: "Remove this unmanaged checkout while preserving its branch?"}
	var tokenParts []string
	if options.DiscardDirty && observed.statusErr == nil && observed.status.Dirty() {
		tokenParts = append(tokenParts, "DISCARD "+observed.checkout)
	}
	if options.DeleteContainedBranch {
		tokenParts = append(tokenParts, "DELETE "+observed.locator.Branch)
	}
	if len(tokenParts) > 0 {
		token := strings.Join(tokenParts, " ")
		confirmation = Confirmation{
			Kind: ConfirmationTyped, Token: token,
			Prompt: "Type " + token + " to approve every destructive effect in this exact plan",
		}
	}
	retained := []string{"branch:" + observed.locator.Branch + "@" + observed.branchOID}
	if options.DeleteContainedBranch {
		retained = nil
	}
	return PlanSpec{
		Authority:         observed.authority(),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: retained,
		Confirmation:      confirmation,
		FallbackCommand:   removeCheckoutFallback(observed, options),
		Summary:           "Remove unmanaged checkout " + observed.checkout,
		DisplayedAt:       s.now(),
	}
}

func removeCheckoutIdentityConditions(observed destructiveObservation, options RemoveCheckoutOptions) []Condition {
	if !observed.worktreeFound {
		verdict := VerdictBlocked
		evidence := "the exact checkout is not registered"
		if observed.worktreeErr != nil && !errors.Is(observed.worktreeErr, gitx.ErrWorktreeNotFound) {
			verdict = VerdictError
			evidence = observed.worktreeErr.Error()
		}
		return []Condition{
			condition(ConditionCheckoutPresent, verdict, RequirementRequired, evidence, "refresh exact worktree topology"),
			condition(ConditionCheckoutExact, VerdictUnknown, RequirementRequired, "checkout identity is unavailable", "refresh exact worktree topology"),
			condition(ConditionCheckoutLinked, VerdictUnknown, RequirementRequired, "checkout kind is unavailable", "select a registered linked checkout"),
			condition(ConditionCheckoutUnlocked, VerdictUnknown, RequirementRequired, "worktree flags are unavailable", "refresh exact worktree topology"),
			condition(ConditionCheckoutBranch, VerdictUnknown, RequirementRequired, "branch identity is unavailable", "select a named non-detached checkout"),
			condition(ConditionBranchRef, VerdictUnknown, RequirementRequired, "local branch ref is unavailable", "restore the exact local branch"),
			condition(ConditionGitStatus, VerdictUnknown, RequirementRequired, "Git status is unavailable", "restore the exact checkout"),
			condition(ConditionGitOperation, VerdictUnknown, RequirementRequired, "Git operation state is unavailable", "restore the exact checkout"),
			condition(ConditionCheckoutClean, VerdictUnknown, RequirementRequired, "checkout cleanliness is unavailable", "restore the exact checkout"),
			condition(ConditionHarnessOwnership, VerdictUnknown, RequirementRequired, "harness ownership is unavailable", "refresh the exact checkout path"),
		}
	}

	exactVerdict := VerdictMet
	exactEvidence := "selected path, repository, Git common directory, and HEAD are exact"
	switch {
	case observed.worktree.Path != observed.checkout:
		exactVerdict, exactEvidence = VerdictBlocked, "registered path differs from selected checkout"
	case observed.worktree.RepositoryPath != observed.repoPath || observed.worktree.GitCommonDir != observed.gitCommonDir:
		exactVerdict, exactEvidence = VerdictBlocked, "registered repository identity differs from selected repository"
	case observed.headErr != nil:
		exactVerdict, exactEvidence = VerdictError, observed.headErr.Error()
	case observed.worktree.Worktree.Head != observed.head:
		exactVerdict, exactEvidence = VerdictBlocked, fmt.Sprintf("registry HEAD %q differs from live HEAD %q", observed.worktree.Worktree.Head, observed.head)
	case observed.locator.HeadOID != observed.head:
		exactVerdict, exactEvidence = VerdictBlocked, fmt.Sprintf("selected HEAD %q differs from live HEAD %q", observed.locator.HeadOID, observed.head)
	}

	kindVerdict := VerdictMet
	kindEvidence := "registered non-main, non-bare linked worktree"
	if !observed.worktree.IsLinkedWorktree() {
		kindVerdict = VerdictBlocked
		kindEvidence = fmt.Sprintf("main=%t bare=%t; canonical and bare records are never removable", observed.worktree.Worktree.Main, observed.worktree.Worktree.Bare)
	}

	flagVerdict := VerdictMet
	flagEvidence := "worktree is unlocked and non-prunable"
	if observed.worktree.Worktree.Locked || observed.worktree.Worktree.Prunable {
		flagVerdict = VerdictBlocked
		flagEvidence = fmt.Sprintf("locked=%t prunable=%t", observed.worktree.Worktree.Locked, observed.worktree.Worktree.Prunable)
	}

	branchVerdict := VerdictMet
	branchEvidence := "registry and status identify named branch " + observed.locator.Branch
	switch {
	case observed.worktree.Worktree.Detached || observed.worktree.Worktree.Branch == "":
		branchVerdict, branchEvidence = VerdictBlocked, "registered worktree is detached or unnamed"
	case observed.statusErr != nil:
		branchVerdict, branchEvidence = VerdictError, observed.statusErr.Error()
	case observed.status.Detached || observed.status.Branch != observed.locator.Branch || observed.worktree.Worktree.Branch != observed.locator.Branch:
		branchVerdict, branchEvidence = VerdictBlocked, fmt.Sprintf("registry=%q status=%q selected=%q", observed.worktree.Worktree.Branch, observed.status.Branch, observed.locator.Branch)
	}

	branchRefVerdict := VerdictMet
	branchRefEvidence := fmt.Sprintf("local %s remains at selected HEAD %s", observed.branchRef, observed.locator.HeadOID)
	switch {
	case observed.branchOIDErr != nil:
		branchRefVerdict, branchRefEvidence = VerdictError, observed.branchOIDErr.Error()
	case !observed.branchExists:
		branchRefVerdict, branchRefEvidence = VerdictBlocked, "exact local branch ref does not exist"
	case observed.branchOID != observed.locator.HeadOID || observed.branchOID != observed.head:
		branchRefVerdict, branchRefEvidence = VerdictBlocked, fmt.Sprintf("branch OID=%q selected HEAD=%q live HEAD=%q", observed.branchOID, observed.locator.HeadOID, observed.head)
	}

	statusVerdict := VerdictMet
	statusEvidence := observed.status.Summary()
	if observed.statusErr != nil {
		statusVerdict, statusEvidence = VerdictError, observed.statusErr.Error()
	}
	operationVerdict := VerdictMet
	operationEvidence := "no Git operation is in progress"
	if observed.operationErr != nil {
		operationVerdict, operationEvidence = VerdictError, observed.operationErr.Error()
	} else if observed.inProgress {
		operationVerdict, operationEvidence = VerdictBlocked, "Git operation "+observed.operation+" is in progress"
	}
	cleanVerdict := VerdictMet
	cleanEvidence := "checkout is clean"
	if observed.statusErr != nil {
		cleanVerdict, cleanEvidence = VerdictError, observed.statusErr.Error()
	} else if observed.status.Dirty() {
		cleanEvidence = observed.status.Breakdown()
		switch {
		case !options.DiscardDirty:
			cleanVerdict = VerdictBlocked
			cleanEvidence += "; default removal preserves all dirty bytes"
		case observed.contentErr != nil:
			cleanVerdict = VerdictError
			cleanEvidence = "dirty content fingerprint failed: " + observed.contentErr.Error()
		case observed.contentFingerprint == "":
			cleanVerdict = VerdictError
			cleanEvidence = "dirty content fingerprint is unavailable"
		default:
			cleanEvidence += "; exact-plan content fingerprint " + observed.contentFingerprint + " will be rechecked before discard"
		}
	}

	harnessVerdict := VerdictMet
	harnessEvidence := "no strict harness path evidence"
	if observed.isHarness {
		harnessVerdict = VerdictBlocked
		harnessEvidence = "checkout is strictly under harness root " + observed.harness.Root
	}
	return []Condition{
		condition(ConditionCheckoutPresent, VerdictMet, RequirementRequired, "registered checkout "+observed.checkout, ""),
		condition(ConditionCheckoutExact, exactVerdict, RequirementRequired, exactEvidence, "refresh the exact checkout locator"),
		condition(ConditionCheckoutLinked, kindVerdict, RequirementRequired, kindEvidence, "select a non-main linked checkout"),
		condition(ConditionCheckoutUnlocked, flagVerdict, RequirementRequired, flagEvidence, "unlock or repair the registration; pruning is never implicit"),
		condition(ConditionCheckoutBranch, branchVerdict, RequirementRequired, branchEvidence, "restore the exact named non-detached checkout"),
		condition(ConditionBranchRef, branchRefVerdict, RequirementRequired, branchRefEvidence, "restore the exact local branch ref at selected HEAD"),
		condition(ConditionGitStatus, statusVerdict, RequirementRequired, statusEvidence, "repair Git status observation"),
		condition(ConditionGitOperation, operationVerdict, RequirementRequired, operationEvidence, "finish or abort the Git operation"),
		condition(ConditionCheckoutClean, cleanVerdict, RequirementRequired, cleanEvidence, "commit changes or use the explicit CLI-compatible discard option"),
		condition(ConditionHarnessOwnership, harnessVerdict, RequirementRequired, harnessEvidence, "let the harness retire its own checkout"),
	}
}

func removeTaskInventoryConditions(observed destructiveObservation) []Condition {
	inventoryCondition := condition(ConditionTaskInventory, VerdictMet, RequirementRequired,
		"task inventory is complete, including a successful empty inventory", "")
	switch {
	case observed.taskInventoryErr != nil:
		inventoryCondition = condition(ConditionTaskInventory, VerdictError, RequirementRequired,
			observed.taskInventoryErr.Error(), "repair task-store inventory before proving unmanaged ownership")
	case len(observed.taskIncomplete) > 0:
		inventoryCondition = condition(ConditionTaskInventory, VerdictError, RequirementRequired,
			strings.Join(observed.taskIncomplete, "; "), "repair every corrupt or unresolvable task record")
	}
	claimsCondition := condition(ConditionTaskClaims, VerdictMet, RequirementRequired,
		"no task claims the exact checkout path or branch in this repository", "")
	if len(observed.taskClaims) > 0 {
		claims := make([]string, len(observed.taskClaims))
		for index, claim := range observed.taskClaims {
			claims[index] = claim.ID + " (" + claim.Reason + ")"
		}
		claimsCondition = condition(ConditionTaskClaims, VerdictBlocked, RequirementRequired,
			"claimed by "+strings.Join(claims, ", "), "use managed lifecycle or reconcile the task claim")
	}
	return []Condition{inventoryCondition, claimsCondition}
}

func removeContainmentConditions(observed destructiveObservation, options RemoveCheckoutOptions) []Condition {
	if !options.RequireContained {
		return []Condition{
			condition(ConditionExplicitBase, VerdictMet, RequirementAdvisory,
				"unmanaged checkout removal does not require branch integration", ""),
			condition(ConditionBranchRelation, VerdictMet, RequirementAdvisory,
				"branch is preserved regardless of containment", ""),
			condition(ConditionBranchDeletion, VerdictMet, RequirementAdvisory,
				"local branch will be retained", ""),
		}
	}
	baseCondition := condition(ConditionExplicitBase, VerdictMet, RequirementRequired,
		fmt.Sprintf("local base %s is %s", observed.baseRef, observed.baseOID), "")
	switch {
	case observed.baseOIDErr != nil:
		baseCondition = condition(ConditionExplicitBase, VerdictError, RequirementRequired,
			observed.baseOIDErr.Error(), "repair containment-base observation")
	case !observed.baseExists:
		baseCondition = condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"containment base "+observed.baseRef+" does not exist", "restore the exact local base")
	case observed.baseOID == "":
		baseCondition = condition(ConditionExplicitBase, VerdictError, RequirementRequired,
			"containment base resolved without an OID", "repair containment-base observation")
	}
	relationCondition := condition(ConditionBranchRelation, VerdictMet, RequirementRequired,
		fmt.Sprintf("%s (%s) is contained in %s (%s)", observed.locator.Branch, observed.branchOID,
			options.ContainmentBase, observed.baseOID), "")
	switch {
	case observed.containedErr != nil:
		relationCondition = condition(ConditionBranchRelation, VerdictError, RequirementRequired,
			observed.containedErr.Error(), "repair local ancestry observation")
	case !observed.branchExists || observed.branchOID == "" || !observed.baseExists || observed.baseOID == "":
		relationCondition = condition(ConditionBranchRelation, VerdictUnknown, RequirementRequired,
			"exact branch/base containment cannot be proved", "restore both exact local refs")
	case !observed.contained:
		relationCondition = condition(ConditionBranchRelation, VerdictBlocked, RequirementRequired,
			"branch is not contained in "+options.ContainmentBase, "integrate it before contained retirement")
	}
	deletionCondition := condition(ConditionBranchDeletion, VerdictMet, RequirementAdvisory,
		"local branch will be retained after contained checkout removal", "")
	if options.DeleteContainedBranch {
		deletionCondition = condition(ConditionBranchDeletion, VerdictMet, RequirementRequired,
			"ordinary git branch -d will run under the same repository/task lock", "")
		if observed.locator.Branch == options.ContainmentBase {
			deletionCondition = condition(ConditionBranchDeletion, VerdictBlocked, RequirementRequired,
				"selected branch is the containment base", "retain the base branch")
		} else if paths, err := observed.branchCheckoutPaths(true); err != nil {
			deletionCondition = condition(ConditionBranchDeletion, VerdictError, RequirementRequired,
				err.Error(), "refresh worktree topology")
		} else if len(paths) > 0 {
			deletionCondition = condition(ConditionBranchDeletion, VerdictBlocked, RequirementRequired,
				"branch is also checked out at "+strings.Join(paths, ", "), "remove or switch those checkouts first")
		}
	}
	return []Condition{baseCondition, relationCondition, deletionCondition}
}

func removeArtifactCondition(observed destructiveObservation) Condition {
	switch {
	case !observed.worktreeFound:
		return condition(ConditionArtifactReady, VerdictUnknown, RequirementRequired,
			"artifact readiness requires the exact checkout", "restore the exact checkout")
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

func removeCheckoutFallback(observed destructiveObservation, options RemoveCheckoutOptions) string {
	parts := []string{"dev", "wt", "rm", shellQuote(observed.locator.Branch)}
	if options.RequireContained {
		parts = []string{"dev", "retire", shellQuote(observed.checkout)}
		if options.DeleteContainedBranch {
			parts = append(parts, "--delete-branch")
		}
	} else if options.DiscardDirty {
		parts = append(parts, "--force")
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

func (s *lifecycleService) applyRemoveCheckout(ctx context.Context, approved Plan) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if approved.Action != RemoveCheckout {
		return Result{}, &InvalidPlanError{PlanID: approved.PlanID, Reason: "remove-checkout handler received " + string(approved.Action)}
	}
	if err := validateRemoveCheckoutLocator(approved.Locator); err != nil {
		return Result{}, err
	}
	commonDir, err := s.canonicalPath(approved.Locator.GitCommonDir)
	if err != nil {
		return Result{}, fmt.Errorf("%w: canonicalize approved repository lock identity: %v", ErrInvalidPlan, err)
	}

	var result Result
	err = s.repoLock(ctx, commonDir, func() error {
		return s.tasks.WithLock(ctx, func(tx *task.Tx) error {
			spec, observed, observeErr := s.observeRemoveCheckout(ctx, approved.Request, tx.ListRecords)
			if observeErr != nil {
				return observeErr
			}
			fresh, buildErr := BuildPlan(approved.Request, spec)
			if buildErr != nil {
				return buildErr
			}
			if fresh.PlanID != approved.PlanID || fresh.AuthorityFingerprint != approved.AuthorityFingerprint {
				return &StalePlanError{
					ExpectedPlanID: approved.PlanID, ActualPlanID: fresh.PlanID,
					ExpectedAuthorityFingerprint: approved.AuthorityFingerprint,
					ActualAuthorityFingerprint:   fresh.AuthorityFingerprint,
					Reason:                       "fresh unmanaged Git, worktree, task, artifact, runtime, or harness authority differs",
				}
			}
			if fresh.Availability != AvailabilityReady {
				notReady := &PlanNotReadyError{PlanID: fresh.PlanID, Availability: fresh.Availability, conditions: fresh.Conditions()}
				return &InvalidPlanError{PlanID: fresh.PlanID, Reason: "fresh unmanaged removal conditions are not ready", Cause: notReady}
			}
			execution := &executionState{service: s, plan: fresh, tx: tx}
			result, observeErr = execution.executeRemoveCheckout(ctx, observed)
			return observeErr
		})
	})
	return result, err
}

func (e *executionState) executeRemoveCheckout(ctx context.Context, baseline destructiveObservation) (Result, error) {
	options := e.plan.Request.Options.(RemoveCheckoutOptions)
	closedRuntime := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectDiscardAll:
			fresh, err := e.reinspectRemoveCheckout(ctx, baseline, false, false)
			if err != nil {
				return e.fail(err, "refresh unmanaged removal before discarding bytes")
			}
			baseline = fresh
			err = e.run(effect, func() (string, error) {
				if discardErr := e.service.discardAll(ctx, baseline.checkout); discardErr != nil {
					return "dirty discard may be partial", fmt.Errorf("discard exact checkout changes: %w", discardErr)
				}
				return "discarded all staged, unstaged, and non-ignored untracked changes", nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "inspect every checkout byte before retrying removal")
			}
			fresh, err = e.reinspectRemoveCheckout(ctx, baseline, true, false)
			if err != nil {
				return e.fail(err, "discard completed but a safety source changed; preserve the checkout and refresh")
			}
			baseline = fresh

		case EffectCloseRuntime:
			fresh, err := e.reinspectRemoveCheckout(ctx, baseline, false, false)
			if err != nil {
				return e.fail(err, "refresh unmanaged removal before closing any runtime")
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
				return e.fail(err, "the checkout and branch remain; inspect runtime occupancy before retrying")
			}
			fresh, err = e.reinspectRemoveCheckout(ctx, baseline, false, true)
			if err != nil {
				return e.fail(err, "runtime state changed after closure; preserve the checkout and refresh removal")
			}
			baseline = fresh
			closedRuntime = true

		case EffectRemoveWorktree:
			fresh, err := e.reinspectRemoveCheckout(ctx, baseline, false, closedRuntime)
			if err != nil {
				return e.fail(err, "the checkout was preserved; refresh every safety source")
			}
			baseline = fresh
			err = e.run(effect, func() (string, error) {
				if removeErr := e.service.removeWorktree(ctx, baseline.repoPath, baseline.checkout, false); removeErr != nil {
					return "checkout removal may be partial", fmt.Errorf("remove exact unmanaged checkout %s: %w", baseline.checkout, removeErr)
				}
				return "removed exact clean linked checkout without force; branch preserved", nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "inspect the exact worktree registration and preserved branch before retrying")
			}
			if err := e.verifyUnmanagedRemoval(ctx, baseline); err != nil {
				return e.fail(err, "checkout removal completed; restore or reconcile the exact preserved local branch")
			}
			e.snapshot = "checkout:removed:" + baseline.checkout + ":" + baseline.branchOID

		case EffectDeleteBranch:
			if e.snapshot == "" {
				return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "branch deletion reached before verified checkout removal"})
			}
			if err := e.revalidateUnmanagedBranchDeletion(ctx, baseline); err != nil {
				return e.fail(err, "the checkout was removed but the branch and task inventory changed; keep the branch and refresh")
			}
			err := e.run(effect, func() (string, error) {
				if _, deleteErr := e.service.gitRun(ctx, baseline.repoPath, "branch", "-d", "--", baseline.locator.Branch); deleteErr != nil {
					return "contained branch deletion failed", deleteErr
				}
				return "deleted contained local branch " + baseline.locator.Branch, nil
			})
			if err != nil {
				e.partial = true
				return e.fail(err, "checkout removal remains complete; inspect the retained branch and retry")
			}
			exists, observeErr := e.service.gitRefState(ctx, baseline.repoPath, baseline.branchRef)
			if observeErr != nil {
				return e.fail(fmt.Errorf("verify contained branch deletion: %w", observeErr), "inspect the branch before retrying")
			}
			if exists {
				return e.fail(errors.New("local branch still exists after successful deletion"), "inspect the branch before retrying")
			}
			e.snapshot += ":branch-deleted"

		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected RemoveCheckout effect " + string(effect.Code)})
		}
	}
	if e.snapshot == "" {
		return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unmanaged removal ended without verified checkout removal"})
	}
	return e.result(), nil
}

func (e *executionState) reinspectRemoveCheckout(ctx context.Context, baseline destructiveObservation, allowDiscard, allowRuntimeChange bool) (destructiveObservation, error) {
	spec, fresh, err := e.service.observeRemoveCheckout(ctx, e.plan.Request, e.tx.ListRecords)
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
	if allowDiscard {
		if err := comparePostDiscardGit(baseline, fresh); err != nil {
			return fresh, err
		}
	} else if err := compareAuthorityCategory("Git", baseline.gitAuthority(), fresh.gitAuthority()); err != nil {
		return fresh, err
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

func comparePostDiscardGit(before, after destructiveObservation) error {
	if after.statusErr != nil || after.status.Dirty() {
		return staleBoundary("checkout is not clean after explicit discard")
	}
	beforeStable := authorityHash("taskflow-post-discard-git-v1",
		before.status.Branch, boolString(before.status.Detached), before.status.Upstream,
		fmt.Sprintf("%d", before.status.Ahead), fmt.Sprintf("%d", before.status.Behind),
		before.head, errorString(before.headErr), before.operation, boolString(before.inProgress), errorString(before.operationErr),
	)
	afterStable := authorityHash("taskflow-post-discard-git-v1",
		after.status.Branch, boolString(after.status.Detached), after.status.Upstream,
		fmt.Sprintf("%d", after.status.Ahead), fmt.Sprintf("%d", after.status.Behind),
		after.head, errorString(after.headErr), after.operation, boolString(after.inProgress), errorString(after.operationErr),
	)
	return compareAuthorityCategory("post-discard Git identity", beforeStable, afterStable)
}

func (e *executionState) revalidateUnmanagedBranchDeletion(ctx context.Context, baseline destructiveObservation) error {
	records, diagnostics, err := e.tx.ListRecords()
	if err != nil {
		return err
	}
	claims := baseline
	claims.taskRecords = records
	claims.taskDiagnostics = diagnostics
	claims.taskClaims = nil
	claims.taskIncomplete = nil
	e.service.inspectDestructiveTaskClaims(ctx, &claims)
	if len(claims.taskIncomplete) > 0 {
		return staleBoundary("task inventory became incomplete before branch deletion")
	}
	if len(claims.taskClaims) > 0 {
		return staleBoundary("a task claimed the checkout path or branch before branch deletion")
	}
	fresh := destructiveObservation{repoPath: baseline.repoPath}
	fresh.observeRefs(ctx, e.service, baseline.locator.Branch, strings.TrimPrefix(baseline.baseRef, "refs/heads/"))
	if fresh.branchOID != baseline.branchOID || fresh.baseOID != baseline.baseOID ||
		fresh.branchOIDErr != nil || fresh.baseOIDErr != nil || fresh.containedErr != nil || !fresh.contained {
		return staleBoundary("branch or containment-base authority changed before branch deletion")
	}
	worktrees, err := e.service.gitWorktrees(ctx, baseline.repoPath)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		if worktree.Branch == baseline.locator.Branch {
			return staleBoundary("branch acquired another checkout before deletion")
		}
	}
	return nil
}

func (e *executionState) verifyUnmanagedRemoval(ctx context.Context, baseline destructiveObservation) error {
	_, err := e.service.resolveWorktree(ctx, baseline.repoPath, baseline.checkout)
	if err == nil {
		return staleBoundary("exact unmanaged checkout remains registered after removal")
	}
	if !errors.Is(err, gitx.ErrWorktreeNotFound) {
		return fmt.Errorf("verify exact unmanaged checkout removal: %w", err)
	}
	fresh := destructiveObservation{repoPath: baseline.repoPath}
	fresh.observeRefs(ctx, e.service, baseline.locator.Branch, "")
	if !fresh.branchExists || fresh.branchOIDErr != nil || fresh.branchOID != baseline.branchOID {
		return staleBoundary(fmt.Sprintf("preserved local branch changed from %s to %s", baseline.branchOID, fresh.branchOID))
	}
	return nil
}
