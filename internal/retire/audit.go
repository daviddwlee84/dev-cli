package retire

import "strings"

// AuditTargetKind is the one normalized ownership class used by the audit.
type AuditTargetKind string

const (
	AuditTargetCanonical AuditTargetKind = "canonical"
	AuditTargetDev       AuditTargetKind = "dev"
	AuditTargetExternal  AuditTargetKind = "external"
	AuditTargetEphemeral AuditTargetKind = "ephemeral"
)

// AuditStatus is the read-only result of one retirement check or a complete
// audit. Audit eligibility is advisory; Service.Retire remains authoritative
// and revalidates every destructive step.
type AuditStatus string

const (
	AuditEligible      AuditStatus = "eligible"
	AuditBlocked       AuditStatus = "blocked"
	AuditUnknown       AuditStatus = "unknown"
	AuditNotApplicable AuditStatus = "not-applicable"
)

const (
	CheckTargetKind        = "target-kind"
	CheckRegistered        = "registered"
	CheckWorktreeUnlocked  = "worktree-unlocked"
	CheckBranchNamed       = "branch-named"
	CheckTaskIdentity      = "task-identity"
	CheckPathExists        = "path-exists"
	CheckStatusAvailable   = "status-available"
	CheckClean             = "clean"
	CheckGitOperation      = "no-git-operation"
	CheckBaseKnown         = "base-known"
	CheckBranchContained   = "branch-contained"
	CheckTaskState         = "task-state"
	CheckArtifactKnown     = "artifact-known"
	CheckArtifactFinalized = "artifact-finalized"
	CheckRuntimeKnown      = "runtime-known"
	CheckRuntimeReady      = "runtime-ready"
)

// Fact represents a boolean observation that may not have been collected.
// Its zero value is unknown so incomplete evidence cannot become eligible.
type Fact struct {
	Known bool `json:"known"`
	Value bool `json:"value"`
}

// KnownFact constructs a collected boolean fact.
func KnownFact(value bool) Fact { return Fact{Known: true, Value: value} }

// AuditInput contains only already-collected facts. Audit performs no I/O.
type AuditInput struct {
	TargetKind AuditTargetKind `json:"target_kind"`

	Registered      Fact   `json:"registered"`
	Unlocked        Fact   `json:"unlocked"`
	BranchNamed     Fact   `json:"branch_named"`
	IdentityMatches Fact   `json:"identity_matches"`
	PathExists      Fact   `json:"path_exists"`
	StatusError     string `json:"status_error,omitempty"`
	Dirty           Fact   `json:"dirty"`
	InProgress      Fact   `json:"in_progress"`

	BaseKnown bool `json:"base_known"`
	Contained Fact `json:"contained"`

	TaskPresent Fact   `json:"task_present"`
	TaskState   string `json:"task_state,omitempty"`

	ArtifactKnown bool `json:"artifact_known"`
	Finalized     bool `json:"finalized"`

	RuntimeKnown bool `json:"runtime_known"`
	RuntimeReady bool `json:"runtime_ready"`
}

// AuditCheck is one stable retirement gate and its deterministic result.
type AuditCheck struct {
	ID     string      `json:"id"`
	Status AuditStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

// AuditResult is a read-only summary. Eligible never authorizes mutation on its
// own; the executing retirement service must collect and revalidate fresh facts.
type AuditResult struct {
	Status AuditStatus  `json:"status"`
	Checks []AuditCheck `json:"checks"`
}

// Audit classifies already-collected retirement facts without changing any
// runtime, worktree, branch, task, or artifact state.
func Audit(input AuditInput) AuditResult {
	if input.TargetKind == AuditTargetCanonical {
		return notApplicableAudit("canonical checkout retirement is not applicable")
	}
	if input.TargetKind == AuditTargetEphemeral {
		return notApplicableAudit("ephemeral checkout retirement is not managed here")
	}

	checks := []AuditCheck{
		targetKindCheck(input.TargetKind),
		registrationCheck(input),
		unlockedCheck(input.Unlocked),
		branchNamedCheck(input.BranchNamed),
		identityCheck(input.IdentityMatches),
		pathCheck(input.PathExists),
		statusCheck(input),
		cleanCheck(input),
		operationCheck(input),
		baseCheck(input.BaseKnown),
		containmentCheck(input),
		taskCheck(input),
		artifactKnownCheck(input.ArtifactKnown),
		artifactFinalizedCheck(input),
		runtimeKnownCheck(input.RuntimeKnown),
		runtimeReadyCheck(input),
	}
	return AuditResult{Status: aggregateAuditStatus(checks), Checks: checks}
}

func targetKindCheck(kind AuditTargetKind) AuditCheck {
	switch kind {
	case AuditTargetDev, AuditTargetExternal:
		return check(CheckTargetKind, AuditEligible, "target is a non-canonical, non-ephemeral checkout")
	case "":
		return check(CheckTargetKind, AuditUnknown, "checkout ownership is unknown")
	default:
		return check(CheckTargetKind, AuditUnknown, "checkout ownership is unrecognized")
	}
}

func registrationCheck(input AuditInput) AuditCheck {
	if !input.Registered.Known {
		return check(CheckRegistered, AuditUnknown, "worktree registration was not inspected")
	}
	if input.Registered.Value {
		return check(CheckRegistered, AuditEligible, "checkout is registered as a linked worktree")
	}
	if !input.PathExists.Known {
		return check(CheckRegistered, AuditUnknown, "checkout is unregistered but path existence is unknown")
	}
	if input.PathExists.Value {
		return check(CheckRegistered, AuditBlocked, "checkout path exists but is not registered as a linked worktree")
	}
	return check(CheckRegistered, AuditEligible, "checkout is already absent from the worktree registry")
}

func unlockedCheck(unlocked Fact) AuditCheck {
	if !unlocked.Known {
		return check(CheckWorktreeUnlocked, AuditUnknown, "worktree lock state was not inspected")
	}
	if !unlocked.Value {
		return check(CheckWorktreeUnlocked, AuditBlocked, "worktree is locked")
	}
	return check(CheckWorktreeUnlocked, AuditEligible, "worktree is not locked")
}

func branchNamedCheck(named Fact) AuditCheck {
	if !named.Known {
		return check(CheckBranchNamed, AuditUnknown, "worktree branch identity was not inspected")
	}
	if !named.Value {
		return check(CheckBranchNamed, AuditBlocked, "linked worktree is detached or unnamed")
	}
	return check(CheckBranchNamed, AuditEligible, "worktree has a named branch")
}

func identityCheck(matches Fact) AuditCheck {
	if !matches.Known {
		return check(CheckTaskIdentity, AuditUnknown, "task/worktree identity was not inspected")
	}
	if !matches.Value {
		return check(CheckTaskIdentity, AuditBlocked, "task branch does not match the live worktree branch")
	}
	return check(CheckTaskIdentity, AuditEligible, "task and worktree identity agree")
}

func pathCheck(exists Fact) AuditCheck {
	if !exists.Known {
		return check(CheckPathExists, AuditUnknown, "checkout path existence was not inspected")
	}
	if exists.Value {
		return check(CheckPathExists, AuditEligible, "checkout path exists")
	}
	return check(CheckPathExists, AuditEligible, "checkout path is already absent")
}

func statusCheck(input AuditInput) AuditCheck {
	if !input.PathExists.Known {
		return check(CheckStatusAvailable, AuditUnknown, "checkout path existence is unknown")
	}
	if !input.PathExists.Value {
		return check(CheckStatusAvailable, AuditNotApplicable, "checkout path is absent")
	}
	if input.StatusError != "" {
		return check(CheckStatusAvailable, AuditUnknown, "checkout status inspection failed: "+input.StatusError)
	}
	if !input.Dirty.Known {
		return check(CheckStatusAvailable, AuditUnknown, "checkout status was not inspected")
	}
	return check(CheckStatusAvailable, AuditEligible, "checkout status is available")
}

func cleanCheck(input AuditInput) AuditCheck {
	if input.PathExists.Known && !input.PathExists.Value {
		return check(CheckClean, AuditNotApplicable, "checkout path is absent")
	}
	if input.StatusError != "" || !input.Dirty.Known {
		return check(CheckClean, AuditUnknown, "checkout cleanliness is unknown")
	}
	if input.Dirty.Value {
		return check(CheckClean, AuditBlocked, "checkout has uncommitted changes")
	}
	return check(CheckClean, AuditEligible, "checkout is clean")
}

func operationCheck(input AuditInput) AuditCheck {
	if input.PathExists.Known && !input.PathExists.Value {
		return check(CheckGitOperation, AuditNotApplicable, "checkout path is absent")
	}
	if !input.InProgress.Known {
		return check(CheckGitOperation, AuditUnknown, "in-progress Git operations were not inspected")
	}
	if input.InProgress.Value {
		return check(CheckGitOperation, AuditBlocked, "a Git operation is in progress")
	}
	return check(CheckGitOperation, AuditEligible, "no Git operation is in progress")
}

func baseCheck(known bool) AuditCheck {
	if !known {
		return check(CheckBaseKnown, AuditUnknown, "base branch is unknown")
	}
	return check(CheckBaseKnown, AuditEligible, "base branch is known")
}

func containmentCheck(input AuditInput) AuditCheck {
	if !input.BaseKnown {
		return check(CheckBranchContained, AuditUnknown, "branch containment cannot be checked without a known base")
	}
	if !input.Contained.Known {
		return check(CheckBranchContained, AuditUnknown, "branch containment was not inspected")
	}
	if !input.Contained.Value {
		return check(CheckBranchContained, AuditBlocked, "branch is not contained in its base")
	}
	return check(CheckBranchContained, AuditEligible, "branch is contained in its base")
}

func taskCheck(input AuditInput) AuditCheck {
	present := input.TaskPresent
	if !present.Known && input.TaskState != "" {
		present = KnownFact(true)
	}
	if !present.Known {
		return check(CheckTaskState, AuditUnknown, "task presence was not inspected")
	}
	state := strings.ToLower(strings.TrimSpace(input.TaskState))
	if !present.Value {
		if state != "" {
			return check(CheckTaskState, AuditUnknown, "task presence conflicts with the supplied task state")
		}
		return check(CheckTaskState, AuditEligible, "checkout has no persisted task")
	}
	switch state {
	case "done":
		return check(CheckTaskState, AuditEligible, "persisted task is done")
	case "":
		return check(CheckTaskState, AuditUnknown, "persisted task state is unknown")
	default:
		return check(CheckTaskState, AuditBlocked, "persisted task is not done")
	}
}

func artifactKnownCheck(known bool) AuditCheck {
	if !known {
		return check(CheckArtifactKnown, AuditUnknown, "artifact reachability was not inspected")
	}
	return check(CheckArtifactKnown, AuditEligible, "artifact reachability is known")
}

func artifactFinalizedCheck(input AuditInput) AuditCheck {
	if !input.ArtifactKnown {
		return check(CheckArtifactFinalized, AuditUnknown, "artifact finalization is unknown")
	}
	if !input.Finalized {
		return check(CheckArtifactFinalized, AuditBlocked, "artifacts are not finalized and reachable")
	}
	return check(CheckArtifactFinalized, AuditEligible, "artifacts are finalized and reachable")
}

func runtimeKnownCheck(known bool) AuditCheck {
	if !known {
		return check(CheckRuntimeKnown, AuditUnknown, "runtime coverage was not inspected")
	}
	return check(CheckRuntimeKnown, AuditEligible, "runtime coverage is known")
}

func runtimeReadyCheck(input AuditInput) AuditCheck {
	if !input.RuntimeKnown {
		return check(CheckRuntimeReady, AuditUnknown, "runtime close eligibility is unknown")
	}
	if !input.RuntimeReady {
		return check(CheckRuntimeReady, AuditBlocked, "runtime closure is blocked")
	}
	return check(CheckRuntimeReady, AuditEligible, "runtime is structurally ready for closure")
}

func notApplicableAudit(detail string) AuditResult {
	ids := []string{
		CheckTargetKind,
		CheckRegistered,
		CheckWorktreeUnlocked,
		CheckBranchNamed,
		CheckTaskIdentity,
		CheckPathExists,
		CheckStatusAvailable,
		CheckClean,
		CheckGitOperation,
		CheckBaseKnown,
		CheckBranchContained,
		CheckTaskState,
		CheckArtifactKnown,
		CheckArtifactFinalized,
		CheckRuntimeKnown,
		CheckRuntimeReady,
	}
	checks := make([]AuditCheck, 0, len(ids))
	for _, id := range ids {
		checks = append(checks, check(id, AuditNotApplicable, detail))
	}
	return AuditResult{Status: AuditNotApplicable, Checks: checks}
}

func aggregateAuditStatus(checks []AuditCheck) AuditStatus {
	status := AuditEligible
	for _, c := range checks {
		switch c.Status {
		case AuditBlocked:
			return AuditBlocked
		case AuditUnknown:
			status = AuditUnknown
		}
	}
	return status
}

func check(id string, status AuditStatus, detail string) AuditCheck {
	return AuditCheck{ID: id, Status: status, Detail: detail}
}
