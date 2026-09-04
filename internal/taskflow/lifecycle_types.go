package taskflow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Stable lifecycle condition codes.
const (
	ConditionTaskCurrent          ConditionCode = "task-current"
	ConditionRepoIdentity         ConditionCode = "repo-identity"
	ConditionCheckoutPresent      ConditionCode = "checkout-present"
	ConditionCheckoutExact        ConditionCode = "checkout-exact"
	ConditionCheckoutLinked       ConditionCode = "checkout-linked"
	ConditionCheckoutUnlocked     ConditionCode = "checkout-unlocked"
	ConditionCheckoutBranch       ConditionCode = "checkout-branch"
	ConditionGitStatus            ConditionCode = "git-status"
	ConditionGitOperation         ConditionCode = "git-operation-clear"
	ConditionGitConflict          ConditionCode = "git-conflict-clear"
	ConditionCheckoutClean        ConditionCode = "checkout-clean"
	ConditionBranchPublished      ConditionCode = "branch-published"
	ConditionBranchPushed         ConditionCode = "branch-pushed"
	ConditionArtifactReady        ConditionCode = "artifact-ready"
	ConditionCleanupOccupancy     ConditionCode = "cleanup-occupancy"
	ConditionCallerContainment    ConditionCode = "caller-containment"
	ConditionNextAction           ConditionCode = "next-action"
	ConditionOwner                ConditionCode = "task-owner"
	ConditionResumeCheckout       ConditionCode = "resume-checkout"
	ConditionSwitchSafe           ConditionCode = "branch-switch-safe"
	ConditionTargetBranch         ConditionCode = "target-branch"
	ConditionAgentOccupancy       ConditionCode = "agent-occupancy"
	ConditionSavedRuntime         ConditionCode = "saved-runtime"
	ConditionRuntimeAvailable     ConditionCode = "runtime-available"
	ConditionExplicitBase         ConditionCode = "explicit-base"
	ConditionFinishAnalysis       ConditionCode = "finish-analysis"
	ConditionBranchRelation       ConditionCode = "branch-relation"
	ConditionIntegrationTarget    ConditionCode = "integration-target"
	ConditionIntegrationOccupancy ConditionCode = "integration-occupancy"
	ConditionReviewProvider       ConditionCode = "review-provider"
	ConditionReviewCapability     ConditionCode = "review-capability"
	ConditionReviewCLI            ConditionCode = "review-cli"
	ConditionReviewBase           ConditionCode = "review-base"
	ConditionRemoteURL            ConditionCode = "remote-url"
	ConditionRemoteRefs           ConditionCode = "remote-local-refs"
	ConditionMergeProof           ConditionCode = "merge-proof"
	ConditionTaskInventory        ConditionCode = "task-inventory-complete"
	ConditionTaskClaims           ConditionCode = "task-unclaimed"
	ConditionHarnessOwnership     ConditionCode = "harness-ownership"
	ConditionBranchRef            ConditionCode = "branch-ref"
	ConditionBranchDeletion       ConditionCode = "branch-deletion"
	ConditionAdoptState           ConditionCode = "adopt-state-compatible"
)

// Stable lifecycle effect codes. Their order in a Plan is execution order.
const (
	EffectFetchRefs      EffectCode = "fetch-refs"
	EffectCommitWIP      EffectCode = "commit-wip"
	EffectPushBranch     EffectCode = "push-branch"
	EffectCloseRuntime   EffectCode = "close-runtime"
	EffectRemoveWorktree EffectCode = "remove-worktree"
	EffectSwitchBase     EffectCode = "switch-base"
	EffectCreateWorktree EffectCode = "create-worktree"
	EffectSwitchBranch   EffectCode = "switch-branch"
	EffectOpenRuntime    EffectCode = "open-runtime"
	EffectCommitAll      EffectCode = "commit-all"
	EffectDiscardAll     EffectCode = "discard-all"
	EffectStashTarget    EffectCode = "stash-integration-target"
	EffectRestoreTarget  EffectCode = "restore-integration-target"
	EffectDiscardTarget  EffectCode = "discard-integration-target"
	EffectRebaseBranch   EffectCode = "rebase-branch"
	EffectMergeFF        EffectCode = "merge-ff"
	EffectPushBase       EffectCode = "push-base"
	EffectCreateReview   EffectCode = "create-review"
	EffectQueryReview    EffectCode = "query-review"
	EffectVerifyAncestry EffectCode = "verify-ancestry"
	EffectUpdateTask     EffectCode = "update-task"
	EffectDeleteBranch   EffectCode = "delete-branch"
	EffectDeleteTask     EffectCode = "delete-task"
	EffectCreateTask     EffectCode = "create-task"
)

type lifecycleObservation struct {
	lifecycleCaller

	record task.Record
	task   task.Task
	mode   task.CheckoutMode

	repo          gitx.Repo
	repoPath      string
	gitCommonDir  string
	checkout      string
	worktree      gitx.RegisteredWorktree
	worktreeFound bool
	worktreeErr   error
	branchMatches int

	status         gitx.Status
	statusErr      error
	head           string
	headErr        error
	baseOID        string
	baseOIDErr     error
	upstreamOID    string
	upstreamOIDErr error
	operation      string
	inProgress     bool
	operationErr   error

	runtime          runtime.Runtime
	runtimeErr       error
	runtimeAvailable bool
	cleanup          retire.Inspection
	cleanupErr       error
	occupancy        runtime.Occupancy
	occupancyErr     error
	savedRuntimeLive bool

	artifact    artifact.ReadinessInspection
	artifactErr error

	finish    gitx.FinishAnalysis
	finishErr error

	completionBaseRef    string
	completionBaseOID    string
	completionBaseOIDErr error
	completionBranch     string
	completionBranchOID  string
	proofRef             string
	proofOID             string
	proofOIDErr          error
	proofContained       bool
	proofErr             error

	reviewKind       forge.Kind
	reviewRemoteURL  string
	reviewRepository string
	reviewProvider   forge.Forge
	reviewResolveErr error
	reviewAvailable  bool

	integration completionIntegrationObservation

	baseRef            string
	baseRefExists      bool
	baseRefExistsErr   error
	localBranchExists  bool
	localBranchOID     string
	localBranchOIDErr  error
	remoteBranch       string
	remoteBranchExists bool
	remoteBranchOID    string
	remoteBranchOIDErr error
	desiredWorktree    string
	desiredWorktreeErr error
}

type completionIntegrationObservation struct {
	worktree       gitx.RegisteredWorktree
	worktreeFound  bool
	worktreeErr    error
	status         gitx.Status
	statusErr      error
	head           string
	headErr        error
	operation      string
	inProgress     bool
	operationErr   error
	occupancy      runtime.Occupancy
	occupancyErr   error
	stashSafety    gitx.StashSafety
	stashSafetyErr error
}

func (o lifecycleObservation) hasCheckout() bool {
	return o.worktreeFound && o.checkout != ""
}

func (o lifecycleObservation) isLinkedWorktree() bool {
	return o.worktreeFound && o.worktree.IsLinkedWorktree()
}

func (o lifecycleObservation) desiredNext(options ActionOptions) string {
	next := o.task.Next
	switch value := options.(type) {
	case ParkWarmOptions:
		if value.Next != "" {
			next = value.Next
		}
	case ParkColdOptions:
		if value.Next != "" {
			next = value.Next
		}
	}
	return next
}

func (o lifecycleObservation) cleanupOptions(closeUnknown, assumeNoRuntime bool, timeout time.Duration) retire.Options {
	return retire.Options{
		CWD:               o.taskflowCWD,
		CallerWorkspaceID: o.taskflowCallerWorkspace,
		CallerPaneID:      o.taskflowCallerPane,
		CloseUnknown:      closeUnknown,
		AssumeNoRuntime:   assumeNoRuntime,
		Timeout:           timeout,
	}
}

// The caller values are copied into observations so execution never consults
// ambient process state after a plan has been approved.
func (o *lifecycleObservation) setCaller(host, cwd, workspace, pane string) {
	o.taskflowHost = host
	o.taskflowCWD = cwd
	o.taskflowCallerWorkspace = workspace
	o.taskflowCallerPane = pane
}

// Private embedded caller authority.
type lifecycleCaller struct {
	taskflowHost            string
	taskflowCWD             string
	taskflowCallerWorkspace string
	taskflowCallerPane      string
}

// executionState accumulates one final-status entry per declared effect.
type executionState struct {
	service  *lifecycleService
	plan     Plan
	observed lifecycleObservation
	tx       *task.Tx
	revision string

	steps     []StepResult
	warnings  []string
	recovery  []string
	snapshot  string
	handoff   *Handoff
	remote    *RemoteObservation
	milestone Milestone
	partial   bool

	resumePath          string
	resumeRuntimeName   string
	resumeRuntimeHandle string

	targetStashOID       string
	targetStashRestored  bool
	targetRestoreAttempt bool
}

func (e *executionState) run(effect Effect, operation func() (string, error)) error {
	step := StepResult{Effect: effect.Clone(), Status: StepAttempted, StartedAt: e.service.now()}
	detail, err := operation()
	step.FinishedAt = e.service.now()
	step.Detail = detail
	if err != nil {
		step.Status = StepFailed
		step.Failure = err.Error()
		e.steps = append(e.steps, step)
		return err
	}
	step.Status = StepCompleted
	e.steps = append(e.steps, step)
	return nil
}

func (e *executionState) runWarning(effect Effect, operation func() (string, error), warningPrefix string) {
	err := e.run(effect, operation)
	if err != nil {
		e.warnings = append(e.warnings, warningPrefix+": "+err.Error())
	}
}

func (e *executionState) result() Result {
	spec := ResultSpec{
		Steps: e.steps, Warnings: e.warnings, Recovery: e.recovery,
		PartialSuccess: e.partial,
		Milestone:      e.milestone, Handoff: e.handoff, Remote: e.remote, FreshSnapshotRef: e.snapshot,
	}
	for _, step := range e.steps {
		if step.Status == StepCompleted {
			spec.PartialSuccess = true
			break
		}
	}
	if len(e.steps) > 0 && e.steps[len(e.steps)-1].Status == StepCompleted &&
		(e.snapshot != "" || e.remote != nil) && len(e.recovery) == 0 {
		// Successful completion is not partial merely because it had effects.
		// Remote refresh is intentionally run-local and therefore completes with
		// structured data rather than a persisted snapshot reference.
		spec.PartialSuccess = false
	}
	return NewResult(spec)
}

func (e *executionState) fail(err error, recovery ...string) (Result, error) {
	e.recovery = append(e.recovery, recovery...)
	return e.result(), err
}

func boolString(value bool) string { return strconv.FormatBool(value) }

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func condition(code ConditionCode, verdict Verdict, requirement Requirement, evidence, remediation string) Condition {
	return Condition{Code: code, Verdict: verdict, Requirement: requirement, Evidence: evidence, Remediation: remediation}
}

func stringifyStatus(status gitx.Status) string {
	return strings.Join([]string{
		status.Branch,
		boolString(status.Detached),
		status.Upstream,
		strconv.Itoa(status.Ahead),
		strconv.Itoa(status.Behind),
		strconv.Itoa(status.Changed),
		strconv.Itoa(status.Staged),
		strconv.Itoa(status.Unstaged),
		strconv.Itoa(status.Untracked),
		strconv.Itoa(status.Conflicted),
	}, "\x00")
}

func sameStatus(left, right gitx.Status) bool {
	return stringifyStatus(left) == stringifyStatus(right)
}

func authorityHash(domain string, values ...string) string {
	writer := newIdentityWriter(domain)
	for index, value := range values {
		writer.addString(strconv.Itoa(index), value)
	}
	return "sha256:" + writer.sumHex()
}

func finishAuthority(analysis gitx.FinishAnalysis, err error) string {
	values := []string{
		analysis.Fingerprint,
		strconv.Itoa(analysis.Relation.BaseOnly),
		strconv.Itoa(analysis.Relation.BranchOnly),
		stringifyStatus(analysis.Status),
		errorString(err),
	}
	for _, change := range analysis.Changes {
		values = append(values,
			change.Path, change.OldPath,
			boolString(change.Staged), boolString(change.Unstaged),
			boolString(change.Untracked), boolString(change.Conflicted),
			boolString(change.BaseEquivalent),
		)
	}
	return authorityHash("taskflow-finish-v1", values...)
}

func artifactAuthority(inspection artifact.ReadinessInspection, err error) string {
	values := []string{
		inspection.Checkout,
		boolString(inspection.KnownEmpty),
		boolString(inspection.Ready()),
		errorString(err),
	}
	for _, intent := range inspection.Intents {
		values = append(values,
			intent.Intent.ID,
			string(intent.Intent.Status),
			intent.Intent.WorktreePath,
			intent.Intent.Branch,
			intent.Intent.Base,
			intent.Intent.ArtifactCommit,
			string(intent.State),
			boolString(intent.Finalized),
			boolString(intent.ReceiptReachable),
			errorString(intent.ObservationError),
		)
	}
	return authorityHash("taskflow-artifact-v1", values...)
}

func cleanupAuthority(inspection retire.Inspection, err error) string {
	values := []string{
		inspection.Target,
		strconv.Itoa(inspection.ClosedSessions),
		boolString(inspection.RuntimeUnknown),
		boolString(inspection.CallerContained),
		errorString(err),
	}
	values = append(values, inspection.Blockers...)
	for _, session := range inspection.Sessions {
		values = append(values, session.Runtime.Handle, session.Runtime.Label, session.Runtime.AgentStatus)
		for _, pane := range session.Panes {
			values = append(values, pane.ID, pane.CWD, pane.ShellCWD, pane.Agent, pane.AgentStatus, pane.AgentSession)
		}
		values = append(values, "mixed")
		for _, pane := range session.Mixed {
			values = append(values, pane.ID, pane.CWD, pane.ShellCWD, pane.Agent, pane.AgentStatus, pane.AgentSession)
		}
	}
	return authorityHash("taskflow-cleanup-v1", values...)
}

func occupancyAuthority(occupancy runtime.Occupancy, err error) string {
	values := []string{
		occupancy.Target,
		occupancy.Backend,
		strconv.Itoa(int(occupancy.Profile)),
		occupancy.CallerWorkspaceID,
		occupancy.ReportedCallerPaneID,
		occupancy.CallerPaneID,
		observationAuthority(occupancy.SessionList),
		observationAuthority(occupancy.AgentActivityList),
		observationAuthority(occupancy.CurrentPane),
		errorString(occupancy.SessionCoverageErr),
		errorString(err),
	}
	for _, session := range occupancy.Sessions {
		values = append(values, session.Runtime.Handle, boolString(session.IsCaller))
		for _, pane := range session.Panes {
			values = append(values, pane.ID, pane.CWD, pane.ShellCWD, pane.Agent, pane.AgentStatus, pane.AgentSession)
		}
		values = append(values, "mixed")
		for _, pane := range session.Mixed {
			values = append(values, pane.ID, pane.CWD, pane.ShellCWD, pane.Agent, pane.AgentStatus, pane.AgentSession)
		}
	}
	for _, agent := range occupancy.Agents {
		values = append(values,
			agent.Activity.PaneID, agent.Activity.WorkspaceID, agent.Activity.Agent,
			agent.Activity.Name, agent.Status, agent.Activity.CWD,
			agent.SessionHandle, boolString(agent.IsCaller), boolString(agent.Blocking),
		)
	}
	return authorityHash("taskflow-occupancy-v1", values...)
}

func observationAuthority(observation runtime.OccupancyObservation) string {
	return strings.Join([]string{
		boolString(observation.Supported),
		boolString(observation.Attempted),
		errorString(observation.Err),
	}, "\x00")
}

func worktreeAuthority(worktree gitx.RegisteredWorktree, found bool, err error, matches int) string {
	if !found {
		return authorityHash("taskflow-worktree-v1", "false", errorString(err), strconv.Itoa(matches))
	}
	value := worktree.Worktree
	return authorityHash("taskflow-worktree-v1",
		"true", errorString(err), strconv.Itoa(matches),
		worktree.RepositoryPath, worktree.GitCommonDir, worktree.Path,
		value.Path, value.Head, value.Branch,
		boolString(value.Detached), boolString(value.Bare), boolString(value.Main),
		boolString(value.Locked), value.LockedReason,
		boolString(value.Prunable), value.PrunableReason,
	)
}

func integrationAuthority(observed completionIntegrationObservation) string {
	return authorityHash("taskflow-integration-v1",
		worktreeAuthority(observed.worktree, observed.worktreeFound, observed.worktreeErr, 0),
		stringifyStatus(observed.status), errorString(observed.statusErr),
		observed.head, errorString(observed.headErr),
		observed.operation, boolString(observed.inProgress), errorString(observed.operationErr),
		occupancyAuthority(observed.occupancy, observed.occupancyErr),
		strconv.Itoa(observed.stashSafety.DirtySubmodules),
		strings.Join(observed.stashSafety.NestedRepositories, "\x00"),
		observed.stashSafety.Fingerprint, errorString(observed.stashSafetyErr),
	)
}

func occupancyProbeError(occupancy runtime.Occupancy, err error) error {
	if err != nil {
		return err
	}
	if occupancy.CurrentPane.Err != nil {
		return fmt.Errorf("resolve current %s runtime pane: %w", occupancy.Backend, occupancy.CurrentPane.Err)
	}
	if occupancy.SessionCoverageErr != nil {
		return fmt.Errorf("classify %s checkout coverage: %w", occupancy.Backend, occupancy.SessionCoverageErr)
	}
	if occupancy.SessionList.Err != nil {
		return fmt.Errorf("list %s runtime sessions: %w", occupancy.Backend, occupancy.SessionList.Err)
	}
	if occupancy.AgentActivityList.Err != nil {
		return fmt.Errorf("list %s recognized agents: %w", occupancy.Backend, occupancy.AgentActivityList.Err)
	}
	return nil
}

func occupancyObservationError(occupancy runtime.Occupancy, err error) error {
	if probeErr := occupancyProbeError(occupancy, err); probeErr != nil {
		return probeErr
	}
	if !occupancy.SessionList.Observed() {
		return fmt.Errorf("%s runtime session inventory is unsupported or was not attempted", occupancy.Backend)
	}
	if occupancy.AgentActivityList.Supported && !occupancy.AgentActivityList.Observed() {
		return fmt.Errorf("%s recognized-agent inventory was not attempted", occupancy.Backend)
	}
	if !occupancy.AgentActivityList.Supported && len(occupancy.Sessions) > 0 && len(occupancy.Agents) == 0 {
		return fmt.Errorf("%s has covering runtime sessions but cannot observe recognized-agent activity", occupancy.Backend)
	}
	return nil
}

func occupancyObservationIncomplete(occupancy runtime.Occupancy) bool {
	return !occupancy.SessionList.Observed() ||
		(occupancy.AgentActivityList.Supported && !occupancy.AgentActivityList.Observed()) ||
		(!occupancy.AgentActivityList.Supported && len(occupancy.Sessions) > 0 && len(occupancy.Agents) == 0)
}

func strictOccupancyError(occupancy runtime.Occupancy, err error) error {
	if observationErr := occupancyObservationError(occupancy, err); observationErr != nil {
		return observationErr
	}
	var blockers []string
	for _, agent := range occupancy.Agents {
		if !agent.Blocking {
			continue
		}
		label := agent.Activity.Name
		if label == "" {
			label = agent.Activity.Agent
		}
		if label == "" {
			label = "agent"
		}
		blockers = append(blockers, fmt.Sprintf("%s (%s, pane %s)", label, agent.Status, agent.Activity.PaneID))
	}
	if len(blockers) > 0 {
		return fmt.Errorf("checkout is occupied by %s", strings.Join(blockers, ", "))
	}
	return nil
}

func (s *lifecycleService) writerOccupancyError(occupancy runtime.Occupancy, err error) error {
	if s.allowSharedCheckout {
		return occupancyProbeError(occupancy, err)
	}
	return strictOccupancyError(occupancy, err)
}

func savedHandleLive(occupancy runtime.Occupancy, handle string) bool {
	if handle == "" {
		return false
	}
	for _, session := range occupancy.Sessions {
		if session.Runtime.Handle == handle {
			return true
		}
	}
	return false
}

func staleBoundary(reason string) error {
	return &StalePlanError{Reason: reason}
}

func isWorktreeNotFound(err error) bool { return errors.Is(err, gitx.ErrWorktreeNotFound) }

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("taskflow context is nil")
	}
	return ctx.Err()
}
