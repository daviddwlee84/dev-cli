package taskflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type adoptObservation struct {
	destructiveObservation

	base          string
	baseExists    bool
	baseOID       string
	baseOIDErr    error
	taskID        string
	candidate     task.Task
	occupancy     runtime.Occupancy
	occupancyErr  error
	derivedState  task.State
	runtimeName   string
	runtimeHandle string
	stateEvidence string
}

func (s *lifecycleService) adoptHandler() Handler {
	return Handler{
		Plan:  s.planAdopt,
		Apply: s.applyAdopt,
	}
}

func validateAdoptLocator(locator Locator) error {
	switch {
	case locator.TaskID != "":
		return fmt.Errorf("%w: unmanaged adoption must not carry a TaskID", ErrInvalidRequest)
	case locator.TaskRevision != "":
		return fmt.Errorf("%w: unmanaged adoption must not carry a task revision", ErrInvalidRequest)
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
	case locator.Mode != "" && locator.Mode != task.ModeWorktree:
		return fmt.Errorf("%w: unmanaged linked-checkout mode must be empty or %q", ErrInvalidRequest, task.ModeWorktree)
	}
	return nil
}

func (s *lifecycleService) planAdopt(ctx context.Context, request Request) (PlanSpec, error) {
	if err := contextError(ctx); err != nil {
		return PlanSpec{}, err
	}
	if err := validateAdoptLocator(request.Locator); err != nil {
		return PlanSpec{}, err
	}
	commonDir, err := s.canonicalPath(request.Locator.GitCommonDir)
	if err != nil {
		return PlanSpec{}, fmt.Errorf("%w: canonicalize requested repository lock identity: %v", ErrInvalidRequest, err)
	}

	var spec PlanSpec
	err = s.repoLock(ctx, commonDir, func() error {
		return s.tasks.WithLock(ctx, func(tx *task.Tx) error {
			var observeErr error
			spec, _, observeErr = s.observeAdopt(ctx, request, tx.ListRecords)
			return observeErr
		})
	})
	return spec, err
}

func (s *lifecycleService) observeAdopt(ctx context.Context, request Request, loader taskInventoryLoader) (PlanSpec, adoptObservation, error) {
	options := request.Options.(AdoptOptions)
	base := options.Base
	if base == "" {
		base = s.gitDefaultBranch(ctx, request.Locator.RepoPath)
	}

	rt, rtErr := s.runtimeForUnmanaged()
	destructive, err := s.inspectDestructive(ctx, destructiveInspectInput{
		locator: request.Locator,
		runtime: rt,
		rtErr:   rtErr,

		skipCleanup:  true,
		inspectTasks: true,
		loadTasks:    loader,
	})
	if rtErr == nil && rt != nil {
		destructive.runtimeAvailable = rt.Available()
	}
	observed := adoptObservation{destructiveObservation: destructive, base: base}
	if err != nil {
		return PlanSpec{}, observed, err
	}
	if destructive.repoPath != "" && base != "" {
		observed.baseExists, observed.baseOIDErr = s.gitRefState(ctx, destructive.repoPath, base)
		if observed.baseExists && observed.baseOIDErr == nil {
			observed.baseOID, observed.baseOIDErr = s.resolveRefOID(ctx, destructive.repoPath, base)
		}
	}

	observed.taskID = task.MakeID(destructive.repo.Name, request.Locator.Branch)
	appendAdoptIDClaim(&observed.destructiveObservation, observed.taskID)
	if destructive.worktreeFound && rtErr == nil {
		observed.occupancy, observed.occupancyErr = s.inspectOccupancy(ctx, rt, destructive.checkout, runtime.OccupancyOptions{
			Profile:           runtime.OccupancyStrict,
			CallerWorkspaceID: s.callerWorkspace,
			CallerPaneID:      s.callerPane,
		})
	}
	observed.derivedState, observed.runtimeName, observed.runtimeHandle, observed.stateEvidence = deriveAdoptState(observed)
	observed.candidate = task.Task{
		ID: observed.taskID, Name: options.Name,
		Repo: destructive.repo.Name, RepoPath: destructive.repoPath,
		Branch: request.Locator.Branch, Base: base,
		WorktreePath: destructive.checkout, Mode: task.ModeWorktree,
		State: observed.derivedState, Owner: s.host,
		Next: options.Next, Note: options.Note, Tags: options.Tags.Values(),
		RuntimeName: observed.runtimeName, RuntimeHandle: observed.runtimeHandle,
	}
	return s.adoptSpec(request, observed), observed, nil
}

func appendAdoptIDClaim(observed *destructiveObservation, taskID string) {
	if observed == nil || taskID == "" {
		return
	}
	for _, record := range observed.taskRecords {
		if record.Task.ID != taskID {
			continue
		}
		for index := range observed.taskClaims {
			if observed.taskClaims[index].ID == taskID {
				if !strings.Contains(observed.taskClaims[index].Reason, "derived task ID") {
					observed.taskClaims[index].Reason += " and derived task ID"
				}
				sortAdoptClaims(observed.taskClaims)
				return
			}
		}
		observed.taskClaims = append(observed.taskClaims, destructiveTaskClaim{
			ID: taskID, Revision: record.Revision, Reason: "derived task ID",
		})
		sortAdoptClaims(observed.taskClaims)
		return
	}
}

func sortAdoptClaims(claims []destructiveTaskClaim) {
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].ID != claims[j].ID {
			return claims[i].ID < claims[j].ID
		}
		return claims[i].Reason < claims[j].Reason
	})
}

func deriveAdoptState(observed adoptObservation) (task.State, string, string, string) {
	rt := observed.runtime
	switch {
	case observed.runtimeErr != nil:
		return task.Warm, "", "", "runtime backend resolution failed; adoption remains WARM"
	case rt == nil || strings.TrimSpace(rt.Name()) == "":
		return task.Warm, "", "", "runtime backend is unavailable; adoption remains WARM"
	case rt.Name() == "none":
		return task.Warm, "", "", "runtime none leaves session and recognized-agent evidence unobserved; adoption remains WARM"
	case !observed.runtimeAvailable:
		return task.Warm, "", "", "runtime backend is unavailable; adoption remains WARM"
	case !observed.worktreeFound:
		return task.Warm, "", "", "exact checkout is unavailable; adoption remains WARM"
	case adoptStrictOccupancyError(observed.occupancy, observed.occupancyErr) != nil:
		return task.Warm, "", "", "strict runtime occupancy is not ready; adoption remains WARM"
	case !observed.occupancy.SessionList.Observed():
		return task.Warm, "", "", "runtime sessions were not freshly observed; adoption remains WARM"
	case !observed.occupancy.AgentActivityList.Observed():
		return task.Warm, "", "", "recognized-agent activity was not freshly observed; adoption remains WARM"
	case len(observed.occupancy.Sessions) != 1:
		return task.Warm, "", "", fmt.Sprintf("fresh runtime coverage contains %d sessions, not exactly one; adoption remains WARM", len(observed.occupancy.Sessions))
	case len(observed.occupancy.Agents) != 0:
		return task.Warm, "", "", "a recognized agent occupies the checkout; adoption remains WARM"
	}

	session := observed.occupancy.Sessions[0]
	handle := session.Runtime.Handle
	if strings.TrimSpace(handle) == "" || strings.TrimSpace(handle) != handle || strings.ContainsRune(handle, '\x00') {
		return task.Warm, "", "", "the sole covering runtime session has no stable nonempty handle; adoption remains WARM"
	}
	if !adoptSessionShellOnly(session) {
		return task.Warm, "", "", "the sole covering runtime session is not shell-only; adoption remains WARM"
	}
	return task.Hot, rt.Name(), handle, "exactly one freshly observed shell-only runtime session covers the checkout with stable handle " + handle
}

func adoptStrictOccupancyError(occupancy runtime.Occupancy, err error) error {
	if occupancyErr := strictOccupancyError(occupancy, err); occupancyErr != nil {
		return occupancyErr
	}
	var blockers []string
	for _, session := range occupancy.Sessions {
		// OccupancySession.Panes contains only panes whose live cwd covers the
		// checkout. A mixed session may also host an unrelated agent elsewhere;
		// that agent is not a writer claim on this checkout.
		for _, pane := range session.Panes {
			if strings.TrimSpace(pane.Agent) == "" && strings.TrimSpace(pane.AgentSession) == "" {
				continue
			}
			if occupancy.CallerPaneID != "" && pane.ID == occupancy.CallerPaneID {
				continue
			}
			status := strings.TrimSpace(pane.AgentStatus)
			if status == "" {
				status = "unknown"
			}
			blockers = append(blockers, fmt.Sprintf("recognized agent pane %s (%s)", pane.ID, status))
		}
	}
	if len(blockers) > 0 {
		return fmt.Errorf("checkout is occupied by %s", strings.Join(blockers, ", "))
	}
	return nil
}

func adoptSessionShellOnly(session runtime.OccupancySession) bool {
	if strings.TrimSpace(session.Runtime.AgentStatus) != "" || len(session.Runtime.AgentSessions) != 0 {
		return false
	}
	panes := session.Runtime.Panes
	if len(panes) == 0 {
		panes = session.Panes
	}
	for _, pane := range panes {
		if strings.TrimSpace(pane.Agent) != "" || strings.TrimSpace(pane.AgentStatus) != "" || strings.TrimSpace(pane.AgentSession) != "" {
			return false
		}
	}
	return true
}

func (s *lifecycleService) adoptSpec(request Request, observed adoptObservation) PlanSpec {
	conditions := []Condition{retireRepositoryCondition(observed.destructiveObservation)}
	conditions = append(conditions, adoptIdentityConditions(observed.destructiveObservation)...)
	conditions = append(conditions, adoptBaseCondition(observed))
	conditions = append(conditions, adoptTaskInventoryConditions(observed.destructiveObservation)...)
	conditions = append(conditions, adoptRuntimeConditions(observed)...)
	conditions = append(conditions, adoptStateCondition(request.Options.(AdoptOptions), observed))
	conditions = append(conditions, condition(
		ConditionOwner, VerdictMet, RequirementAdvisory,
		"adoption assigns checkout ownership to host "+s.host, "",
	))

	details := map[string]string{
		"id": observed.taskID, "repo": observed.candidate.Repo,
		"repo-path": observed.candidate.RepoPath, "checkout": observed.candidate.WorktreePath,
		"branch": observed.candidate.Branch, "head": observed.locator.HeadOID,
		"base": observed.candidate.Base, "base-oid": observed.baseOID,
		"mode": string(observed.candidate.Mode), "state": string(observed.candidate.State),
		"owner": observed.candidate.Owner, "runtime": observed.candidate.RuntimeName,
		"runtime-handle": observed.candidate.RuntimeHandle,
	}
	effect := NewEffect(
		EffectCreateTask, "create one task record for the exact unmanaged linked checkout",
		observed.taskID, false, false, details,
	)
	retained := []string{
		"checkout:" + observed.checkout,
		"branch:" + observed.locator.Branch + "@" + observed.branchOID,
	}
	if observed.derivedState == task.Hot {
		retained = append(retained, "runtime:"+observed.runtimeName+":"+observed.runtimeHandle)
	}
	return PlanSpec{
		Authority:         adoptAuthority(observed),
		Conditions:        conditions,
		Effects:           []Effect{effect},
		RetainedResources: retained,
		Confirmation: Confirmation{
			Kind:   ConfirmationApproval,
			Prompt: "Adopt this exact unmanaged checkout as a " + strings.ToUpper(string(observed.derivedState)) + " worktree task?",
		},
		FallbackCommand: "dev adopt --apply --state " + shellQuote(string(observed.derivedState)),
		Summary:         "Adopt unmanaged checkout " + observed.checkout,
		DisplayedAt:     s.now(),
	}
}

func adoptAuthority(observed adoptObservation) map[string]string {
	authority := observed.destructiveObservation.authority()
	authority["adopt.base"] = observed.base
	authority["adopt.base-exists"] = boolString(observed.baseExists)
	authority["adopt.base-oid"] = observed.baseOID
	authority["adopt.base-error"] = errorString(observed.baseOIDErr)
	authority["adopt.task-id"] = observed.taskID
	authority["adopt.task"] = adoptTaskAuthority(observed.candidate)
	authority["adopt.occupancy"] = occupancyAuthority(observed.occupancy, observed.occupancyErr)
	authority["adopt.derived-state"] = string(observed.derivedState)
	authority["adopt.runtime-name"] = observed.runtimeName
	authority["adopt.runtime-handle"] = observed.runtimeHandle
	authority["adopt.state-evidence"] = observed.stateEvidence
	return authority
}

func adoptTaskAuthority(candidate task.Task) string {
	values := []string{
		candidate.ID, candidate.Name, candidate.Repo, candidate.RepoPath,
		candidate.Branch, candidate.Base, candidate.WorktreePath,
		string(candidate.Mode), string(candidate.State), candidate.Owner,
		candidate.Next, candidate.Note, candidate.AgentSession,
		candidate.RuntimeName, candidate.RuntimeHandle,
	}
	values = append(values, candidate.Tags...)
	return authorityHash("taskflow-adopt-task-v1", values...)
}

func adoptIdentityConditions(observed destructiveObservation) []Condition {
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
			condition(ConditionCheckoutClean, VerdictUnknown, RequirementAdvisory, "checkout bytes were not inspected", "refresh the exact checkout"),
			condition(ConditionHarnessOwnership, VerdictUnknown, RequirementRequired, "harness ownership is unavailable", "refresh the exact checkout path"),
		}
	}

	exactVerdict := VerdictMet
	exactEvidence := "selected path, repository, Git common directory, registry HEAD, and live HEAD are exact"
	switch {
	case observed.worktreesErr != nil:
		exactVerdict, exactEvidence = VerdictError, observed.worktreesErr.Error()
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
		kindEvidence = fmt.Sprintf("main=%t bare=%t; canonical and bare records are never adoptable", observed.worktree.Worktree.Main, observed.worktree.Worktree.Bare)
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
	branchRefEvidence := fmt.Sprintf("local %s is exactly selected HEAD %s", observed.branchRef, observed.locator.HeadOID)
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
	cleanEvidence := "checkout is clean; adoption writes metadata only"
	if observed.statusErr != nil {
		cleanVerdict, cleanEvidence = VerdictUnknown, "checkout cleanliness is unavailable; required Git status already blocks adoption"
	} else if observed.status.Dirty() {
		cleanEvidence = observed.status.Breakdown() + "; dirty bytes are allowed and remain untouched"
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
		condition(ConditionCheckoutClean, cleanVerdict, RequirementAdvisory, cleanEvidence, "commit later if desired; adoption never changes checkout bytes"),
		condition(ConditionHarnessOwnership, harnessVerdict, RequirementRequired, harnessEvidence, "let the harness own its checkout"),
	}
}

func adoptBaseCondition(observed adoptObservation) Condition {
	switch {
	case observed.base == "":
		return condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"no explicit base was supplied and the repository default is unavailable", "supply an existing base ref")
	case observed.baseOIDErr != nil:
		return condition(ConditionExplicitBase, VerdictError, RequirementRequired,
			observed.baseOIDErr.Error(), "repair exact base-ref observation")
	case !observed.baseExists:
		return condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"base ref "+observed.base+" does not exist", "restore or select an existing base ref")
	case observed.baseOID == "":
		return condition(ConditionExplicitBase, VerdictBlocked, RequirementRequired,
			"base resolved to no commit", "restore the named base ref")
	default:
		return condition(ConditionExplicitBase, VerdictMet, RequirementRequired,
			fmt.Sprintf("base %s is exactly %s", observed.base, observed.baseOID), "")
	}
}

func adoptTaskInventoryConditions(observed destructiveObservation) []Condition {
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
		"no task claims the exact checkout path, repository branch, or derived task ID", "")
	if len(observed.taskClaims) > 0 {
		claims := make([]string, len(observed.taskClaims))
		for index, claim := range observed.taskClaims {
			claims[index] = claim.ID + " (" + claim.Reason + ")"
		}
		claimsCondition = condition(ConditionTaskClaims, VerdictBlocked, RequirementRequired,
			"claimed by "+strings.Join(claims, ", "), "use managed lifecycle or resolve every task claim")
	}
	return []Condition{inventoryCondition, claimsCondition}
}

func adoptRuntimeConditions(observed adoptObservation) []Condition {
	if observed.runtimeErr != nil {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictError, RequirementRequired,
				observed.runtimeErr.Error(), "repair runtime backend resolution"),
			condition(ConditionAgentOccupancy, VerdictError, RequirementRequired,
				observed.runtimeErr.Error(), "repair runtime session and recognized-agent observation"),
		}
	}
	if observed.runtime == nil || observed.runtime.Name() == "none" {
		return []Condition{
			condition(ConditionRuntimeAvailable, VerdictMet, RequirementAdvisory,
				"runtime none performs no session operation", ""),
			condition(ConditionAgentOccupancy, VerdictUnknown, RequirementAdvisory,
				"runtime none leaves session and recognized-agent occupancy unobserved; metadata-only adoption is retained as WARM", "inspect occupancy with a capable runtime if HOT metadata is required"),
		}
	}

	availableVerdict := VerdictMet
	availableEvidence := "runtime backend " + observed.runtime.Name() + " is available for fresh strict inspection"
	if !observed.runtimeAvailable {
		availableVerdict = VerdictBlocked
		availableEvidence = "runtime backend " + observed.runtime.Name() + " is unavailable"
	}
	occupancyVerdict := VerdictUnknown
	occupancyEvidence := "exact checkout was unavailable for strict occupancy inspection"
	if observed.worktreeFound {
		occupancyErr := adoptStrictOccupancyError(observed.occupancy, observed.occupancyErr)
		if occupancyErr == nil {
			occupancyVerdict = VerdictMet
			occupancyEvidence = "no recognized agent other than the exact caller occupies the checkout"
			if !observed.occupancy.AgentActivityList.Observed() {
				occupancyEvidence += "; recognized-agent absence was not strong enough to derive HOT"
			}
		} else {
			occupancyVerdict = VerdictBlocked
			occupancyEvidence = occupancyErr.Error()
			if observed.occupancyErr != nil || observed.occupancy.CurrentPane.Err != nil ||
				observed.occupancy.SessionCoverageErr != nil || observed.occupancy.SessionList.Err != nil ||
				observed.occupancy.AgentActivityList.Err != nil {
				occupancyVerdict = VerdictError
			}
		}
	}
	return []Condition{
		condition(ConditionRuntimeAvailable, availableVerdict, RequirementRequired,
			availableEvidence, "select or restore an available runtime backend"),
		condition(ConditionAgentOccupancy, occupancyVerdict, RequirementRequired,
			occupancyEvidence, "stop every other recognized agent or use another checkout"),
	}
}

func adoptStateCondition(options AdoptOptions, observed adoptObservation) Condition {
	evidence := fmt.Sprintf("derived %s: %s", strings.ToUpper(string(observed.derivedState)), observed.stateEvidence)
	if options.State != "" && options.State != observed.derivedState {
		return condition(ConditionAdoptState, VerdictBlocked, RequirementRequired,
			fmt.Sprintf("requested compatibility state %s does not equal %s", options.State, evidence),
			"omit the requested state or pass the freshly derived state")
	}
	if options.State != "" {
		evidence = fmt.Sprintf("requested compatibility state %s equals %s", options.State, evidence)
	}
	return condition(ConditionAdoptState, VerdictMet, RequirementRequired, evidence, "")
}

func (s *lifecycleService) applyAdopt(ctx context.Context, approved Plan) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if approved.Action != Adopt {
		return Result{}, &InvalidPlanError{PlanID: approved.PlanID, Reason: "adopt handler received " + string(approved.Action)}
	}
	if err := validateAdoptLocator(approved.Locator); err != nil {
		return Result{}, err
	}
	commonDir, err := s.canonicalPath(approved.Locator.GitCommonDir)
	if err != nil {
		return Result{}, fmt.Errorf("%w: canonicalize approved repository lock identity: %v", ErrInvalidPlan, err)
	}

	var result Result
	err = s.repoLock(ctx, commonDir, func() error {
		return s.tasks.WithLock(ctx, func(tx *task.Tx) error {
			spec, observed, observeErr := s.observeAdopt(ctx, approved.Request, tx.ListRecords)
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
					Reason:                       "fresh unmanaged Git, worktree, task, base, runtime, agent, or harness authority differs",
				}
			}
			if fresh.Availability != AvailabilityReady {
				notReady := &PlanNotReadyError{PlanID: fresh.PlanID, Availability: fresh.Availability, conditions: fresh.Conditions()}
				return &InvalidPlanError{PlanID: fresh.PlanID, Reason: "fresh adoption conditions are not ready", Cause: notReady}
			}
			execution := &executionState{service: s, plan: fresh, tx: tx}
			result, observeErr = execution.executeAdopt(observed)
			return observeErr
		})
	})
	return result, err
}

func (e *executionState) executeAdopt(observed adoptObservation) (Result, error) {
	effects := e.plan.Effects()
	if len(effects) != 1 || effects[0].Code != EffectCreateTask {
		return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "Adopt must contain exactly one create-task effect"})
	}
	if observed.runtime != nil && observed.runtime.Name() == "none" {
		e.warnings = append(e.warnings, "runtime none left occupancy unobserved; the adopted task is WARM")
	}

	effect := effects[0]
	expected := observed.candidate
	expected.Tags = append([]string(nil), observed.candidate.Tags...)
	err := e.run(effect, func() (string, error) {
		candidate := expected
		candidate.Tags = append([]string(nil), expected.Tags...)
		created, createErr := e.service.taskCreate(e.tx, &candidate)
		if createErr != nil {
			return "task create made no authorized non-task change", fmt.Errorf("create adopted task %s: %w", expected.ID, createErr)
		}
		if created == nil || created.Revision == "" {
			return "task create returned no revision", errors.New("create adopted task returned no revision")
		}
		if adoptTaskAuthority(created.Task) != adoptTaskAuthority(expected) {
			return "task create returned different metadata", errors.New("created task does not match the approved adoption metadata")
		}
		persisted, verifyErr := e.tx.GetRecord(expected.ID)
		if verifyErr != nil {
			return "task create was not verified", fmt.Errorf("verify created adopted task %s: %w", expected.ID, verifyErr)
		}
		if persisted.Revision != created.Revision || adoptTaskAuthority(persisted.Task) != adoptTaskAuthority(expected) {
			return "task create revision or metadata was not verified", errors.New("persisted adopted task differs from the task create result")
		}
		e.snapshot = "task:" + persisted.Revision
		e.milestone = MilestoneAdopted
		return "created task " + persisted.Task.ID + " at revision " + persisted.Revision, nil
	})
	if err != nil {
		return e.fail(err, "the checkout, branch, runtime, and Git bytes were not changed; repair the task-store failure and refresh adoption")
	}
	if e.milestone != MilestoneAdopted || e.snapshot == "" {
		return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "adoption ended without a created task revision"})
	}
	return e.result(), nil
}
