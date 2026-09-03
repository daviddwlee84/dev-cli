package ephemeral

const (
	CheckProviderOwned    = "provider-owned"
	CheckProviderUnique   = "provider-claim-unique"
	CheckProviderMapping  = "provider-mapping"
	CheckProviderIdentity = "provider-git-identity"
	CheckWorkflowTerminal = "workflow-terminal"
	CheckAgentDone        = "agent-done"
	CheckJournalStarted   = "journal-started"
	CheckJournalResult    = "journal-result"
	CheckNotResumed       = "agent-not-resumed"
	CheckProviderInactive = "provider-inactive"
	CheckRegistered       = "registered"
	CheckPathPresent      = "path-present"
	CheckLinkedWorktree   = "linked-worktree"
	CheckNonMain          = "non-main"
	CheckBranchNamed      = "branch-named"
	CheckBranchMatches    = "branch-matches"
	CheckCommonDir        = "common-dir-matches"
	CheckHeadMatches      = "head-matches"
	CheckUnlocked         = "worktree-unlocked"
	CheckNotPrunable      = "worktree-not-prunable"
	CheckClean            = "clean"
	CheckIgnoredEmpty     = "ignored-empty"
	CheckSubmodulesClean  = "submodules-clean"
	CheckNoGitOperation   = "no-git-operation"
	CheckTaskUnclaimed    = "task-unclaimed"
	CheckArtifactsKnown   = "artifacts-known"
	CheckArtifactsSafe    = "artifacts-safe"
	CheckCallerOutside    = "caller-outside"
	CheckRuntimeKnown     = "runtime-known"
	CheckRuntimeClear     = "runtime-clear"
)

// Audit applies pure V1 policy to already-collected facts. Known blockers
// outrank unknown observations. A structurally absent/prunable/non-provider
// target is report-only and therefore not applicable rather than eligible.
func Audit(in AuditInput) AuditResult {
	checks := []Check{
		booleanCheck(CheckProviderOwned, in.ProviderOwned, "provider ownership is verified", "candidate is not owned by the provider", "provider ownership was not verified", NotApplicable),
		booleanCheck(CheckProviderUnique, in.ProviderUnique, "provider claim is unique", "provider claims are duplicated or ambiguous", "provider claim uniqueness is unknown", Blocked),
		booleanCheck(CheckProviderMapping, in.ProviderMapping, "provider mapping exactly matches", "provider mapping does not match", "provider mapping is unknown", Blocked),
		booleanCheck(CheckProviderIdentity, in.ProviderIdentity, "provider-observed Git identity matches the live registration", "provider-observed Git identity does not match the live registration", "provider metadata does not bind branch, HEAD, common-dir, and registration identity", Blocked),
		v1UnknownCheck(CheckWorkflowTerminal, in.WorkflowTerminal, "workflow is terminal", "workflow is non-terminal or has ambiguous terminal state", "workflow terminal state is unknown"),
		v1UnknownCheck(CheckAgentDone, in.AgentDone, "agent state is done", "agent state is not done", "agent state is unknown"),
		v1UnknownCheck(CheckJournalStarted, in.JournalStarted, "matching started record exists", "matching started record is absent", "matching started record is unknown"),
		v1UnknownCheck(CheckJournalResult, in.JournalResult, "matching result record exists", "matching result record is absent", "matching result record is unknown"),
		v1UnknownCheck(CheckNotResumed, in.NotResumed, "same-ID resumed transcript is absent", "same-ID resumed transcript exists", "same-ID resume state is unknown"),
		booleanCheck(CheckProviderInactive, in.ProviderInactive, "provider activity is older than the threshold", "provider activity is newer than the threshold", "provider activity time is unknown", Blocked),
		booleanCheck(CheckRegistered, in.Registered, "checkout is registered", "provider path is not registered", "worktree registration is unknown", NotApplicable),
		booleanCheck(CheckPathPresent, in.PathPresent, "checkout path is present", "checkout path is missing", "checkout path presence is unknown", NotApplicable),
		booleanCheck(CheckLinkedWorktree, in.LinkedWorktree, "checkout is a linked worktree", "checkout is not a linked worktree", "linked-worktree identity is unknown", NotApplicable),
		booleanCheck(CheckNonMain, in.NonMain, "checkout is not canonical", "canonical checkout removal is not applicable", "canonical-checkout identity is unknown", NotApplicable),
		booleanCheck(CheckBranchNamed, in.BranchNamed, "worktree has a named branch", "worktree is detached or unnamed", "branch identity is unknown", Blocked),
		booleanCheck(CheckBranchMatches, in.BranchMatches, "registry and live branch agree", "registry and live branch differ", "live branch agreement is unknown", Blocked),
		booleanCheck(CheckCommonDir, in.CommonDirMatches, "checkout common-dir matches the repository", "checkout belongs to another common-dir", "checkout common-dir is unknown", Blocked),
		booleanCheck(CheckHeadMatches, in.HeadMatches, "registry and live HEAD agree", "registry and live HEAD differ", "live HEAD agreement is unknown", Blocked),
		booleanCheck(CheckUnlocked, in.Unlocked, "worktree is unlocked", "worktree is locked", "worktree lock state is unknown", Blocked),
		booleanCheck(CheckNotPrunable, in.NotPrunable, "worktree registration is not prunable", "worktree registration is prunable", "prunable state is unknown", NotApplicable),
		booleanCheck(CheckClean, in.Clean, "checkout has no staged, unstaged, conflicted, or untracked paths", "checkout has staged, unstaged, conflicted, or untracked paths", "checkout cleanliness is unknown", Blocked),
		booleanCheck(CheckIgnoredEmpty, in.IgnoredEmpty, "checkout has no ignored files", "checkout contains ignored files", "ignored-file inventory is unknown", Blocked),
		booleanCheck(CheckSubmodulesClean, in.SubmodulesClean, "submodule state is clean", "submodule state is dirty or conflicted", "submodule state is unknown", Blocked),
		booleanCheck(CheckNoGitOperation, in.NoGitOperation, "no Git operation is in progress", "a Git operation is in progress", "Git operation state is unknown", Blocked),
		booleanCheck(CheckTaskUnclaimed, in.TaskUnclaimed, "no task claims the checkout or branch", "a persisted task claims the checkout or branch", "task claims are unknown", Blocked),
		knownCheck(CheckArtifactsKnown, in.ArtifactsKnown, "artifact inventory is known", "artifact inventory is unknown"),
		booleanCheck(CheckArtifactsSafe, in.ArtifactsSafe, "all artifact intents are discarded or finalized and reachable", "an artifact intent is not discarded or finalized and reachable", "artifact safety is unknown", Blocked),
		booleanCheck(CheckCallerOutside, in.CallerOutside, "caller is outside the target", "caller is inside the target", "caller containment is unknown", Blocked),
		knownCheck(CheckRuntimeKnown, in.RuntimeKnown, "every available runtime was inventoried", "runtime inventory is unknown"),
		booleanCheck(CheckRuntimeClear, in.RuntimeClear, "no runtime session covers the target", "a runtime session covers the target", "runtime coverage is unknown", Blocked),
	}

	// Explicit report-only structural outcomes dominate ordinary eligibility but
	// never turn unknown provider evidence into not-applicable.
	for _, id := range []string{CheckProviderOwned, CheckRegistered, CheckPathPresent, CheckLinkedWorktree, CheckNonMain, CheckNotPrunable} {
		if status(checks, id) == NotApplicable {
			return AuditResult{Classification: NotApplicable, Checks: checks}
		}
	}
	return AuditResult{Classification: aggregate(checks), Checks: checks}
}

func booleanCheck(id string, fact Fact, yes, no, unknown string, falseClass Classification) Check {
	if !fact.Known {
		return Check{ID: id, Classification: Unknown, Detail: unknown}
	}
	if fact.Value {
		return Check{ID: id, Classification: Eligible, Detail: yes}
	}
	return Check{ID: id, Classification: falseClass, Detail: no}
}

func knownCheck(id string, fact Fact, yes, unknown string) Check {
	if !fact.Known {
		return Check{ID: id, Classification: Unknown, Detail: unknown}
	}
	if !fact.Value {
		return Check{ID: id, Classification: Unknown, Detail: unknown}
	}
	return Check{ID: id, Classification: Eligible, Detail: yes}
}

// V1 refuses to distinguish a known absent liveness record from uncertainty:
// both require stronger upstream evidence and have no attestation bypass.
func v1UnknownCheck(id string, fact Fact, yes, no, unknown string) Check {
	if !fact.Known {
		return Check{ID: id, Classification: Unknown, Detail: unknown}
	}
	if !fact.Value {
		return Check{ID: id, Classification: Unknown, Detail: no}
	}
	return Check{ID: id, Classification: Eligible, Detail: yes}
}

func aggregate(checks []Check) Classification {
	result := Eligible
	for _, check := range checks {
		switch check.Classification {
		case Blocked:
			return Blocked
		case Unknown:
			result = Unknown
		}
	}
	return result
}

func status(checks []Check, id string) Classification {
	for _, check := range checks {
		if check.ID == id {
			return check.Classification
		}
	}
	return Unknown
}
