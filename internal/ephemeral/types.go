// Package ephemeral audits and removes provider-verified, turn-scoped Git
// worktrees. Provider metadata is evidence, never authority by itself: mutation
// is allowed only after every independent Git, task, artifact, caller, and
// runtime proof is freshly re-collected under a repository-scoped lock.
package ephemeral

import (
	"context"
	"time"
)

const SchemaVersion = 1

type Classification string

const (
	Eligible      Classification = "eligible"
	Blocked       Classification = "blocked"
	Unknown       Classification = "unknown"
	NotApplicable Classification = "not-applicable"
)

// Fact is a three-state boolean. The zero value is unknown so omitted evidence
// can never accidentally authorize cleanup.
type Fact struct {
	Known bool `json:"known"`
	Value bool `json:"value"`
}

func KnownFact(value bool) Fact { return Fact{Known: true, Value: value} }

// RepositoryIdentity is the canonical repository scope of a report.
type RepositoryIdentity struct {
	Root      string `json:"root"`
	CommonDir string `json:"common_dir"`
	Name      string `json:"name"`
	Bare      bool   `json:"bare"`
}

// Target is one candidate checkout. Hint is discovery-only and is never used
// as proof that a provider owns the path.
type Target struct {
	Path              string `json:"path"`
	Branch            string `json:"branch,omitempty"`
	RegistryHead      string `json:"registry_head,omitempty"`
	Main              bool   `json:"main"`
	Bare              bool   `json:"bare"`
	Detached          bool   `json:"detached"`
	Locked            bool   `json:"locked"`
	LockedReason      string `json:"locked_reason,omitempty"`
	Prunable          bool   `json:"prunable"`
	PrunableReason    string `json:"prunable_reason,omitempty"`
	Registered        bool   `json:"registered"`
	RegistrationKnown bool   `json:"registration_known"`
	Hint              bool   `json:"hint"`
}

// SourceQuery limits provider inspection to one repository and its current Git
// candidates. Now is supplied so future timestamps can be rejected
// deterministically.
type SourceQuery struct {
	Repository RepositoryIdentity
	Targets    []Target
	Now        time.Time
}

// Claim is the normalized, privacy-preserving evidence for one provider-owned
// checkout. It contains identifiers, states, and timestamps only; providers
// must never place prompts, scripts, logs, transcript content, or result bodies
// here.
type Claim struct {
	Provider           string    `json:"provider"`
	SessionID          string    `json:"session_id"`
	RunID              string    `json:"run_id"`
	AgentID            string    `json:"agent_id"`
	WorktreePath       string    `json:"worktree_path"`
	Owned              Fact      `json:"owned"`
	Unique             Fact      `json:"unique"`
	Mapping            Fact      `json:"mapping"`
	GitIdentity        Fact      `json:"git_identity"`
	ObservedBranch     string    `json:"observed_branch,omitempty"`
	ObservedHead       string    `json:"observed_head,omitempty"`
	ObservedCommonDir  string    `json:"observed_common_dir,omitempty"`
	ObservedGeneration string    `json:"observed_generation,omitempty"`
	WorkflowState      string    `json:"workflow_state"`
	WorkflowTerminal   Fact      `json:"workflow_terminal"`
	AgentState         string    `json:"agent_state"`
	AgentDone          Fact      `json:"agent_done"`
	JournalStarted     Fact      `json:"journal_started"`
	JournalResult      Fact      `json:"journal_result"`
	NotResumed         Fact      `json:"not_resumed"`
	LastActivity       time.Time `json:"last_activity,omitempty"`
	LastActivityKnown  bool      `json:"last_activity_known"`
}

// Capability and Diagnostic are stable, bounded report metadata. Diagnostics
// identify a class of failure without echoing private provider content.
type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type Diagnostic struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SourceResult is a complete provider scan. Complete=false means absence of a
// claim is unknown rather than proof that a candidate is not provider-owned.
type SourceResult struct {
	Provider     string
	Complete     bool
	Claims       []Claim
	Capabilities []Capability
	Diagnostics  []Diagnostic
}

// OwnershipSource adapts one provider's bounded metadata into normalized
// claims. Implementations are report-only and must not mutate provider state.
type OwnershipSource interface {
	Collect(context.Context, SourceQuery) SourceResult
}

// GitFacts records every checkout observation used by the audit and stable
// fingerprint. Counts are included instead of paths so reports cannot leak
// filenames from a checkout.
type GitFacts struct {
	Registered             Fact   `json:"registered"`
	PathPresent            Fact   `json:"path_present"`
	LinkedWorktree         Fact   `json:"linked_worktree"`
	NonMain                Fact   `json:"non_main"`
	BranchNamed            Fact   `json:"branch_named"`
	BranchMatches          Fact   `json:"branch_matches"`
	CommonDirMatches       Fact   `json:"common_dir_matches"`
	HeadMatches            Fact   `json:"head_matches"`
	Unlocked               Fact   `json:"unlocked"`
	NotPrunable            Fact   `json:"not_prunable"`
	Clean                  Fact   `json:"clean"`
	IgnoredEmpty           Fact   `json:"ignored_empty"`
	SubmodulesClean        Fact   `json:"submodules_clean"`
	NoGitOperation         Fact   `json:"no_git_operation"`
	LiveHead               string `json:"live_head,omitempty"`
	CommonDir              string `json:"common_dir,omitempty"`
	RegistrationGeneration string `json:"registration_generation,omitempty"`
	Staged                 int    `json:"staged"`
	Unstaged               int    `json:"unstaged"`
	Conflicted             int    `json:"conflicted"`
	Untracked              int    `json:"untracked"`
	Ignored                int    `json:"ignored"`
	DirtySubmodules        int    `json:"dirty_submodules"`
	Operation              string `json:"operation,omitempty"`
	StateFingerprint       string `json:"state_fingerprint,omitempty"`
}

// SafetyFacts joins non-provider ownership systems without exposing their
// private records.
type SafetyFacts struct {
	TaskUnclaimed       Fact   `json:"task_unclaimed"`
	TaskClaims          int    `json:"task_claims"`
	ArtifactsKnown      Fact   `json:"artifacts_known"`
	ArtifactsSafe       Fact   `json:"artifacts_safe"`
	ArtifactIntents     int    `json:"artifact_intents"`
	CallerOutside       Fact   `json:"caller_outside"`
	RuntimeKnown        Fact   `json:"runtime_known"`
	RuntimeClear        Fact   `json:"runtime_clear"`
	CoveringSessions    int    `json:"covering_sessions"`
	TaskFingerprint     string `json:"task_fingerprint,omitempty"`
	ArtifactFingerprint string `json:"artifact_fingerprint,omitempty"`
	CallerFingerprint   string `json:"caller_fingerprint,omitempty"`
	RuntimeFingerprint  string `json:"runtime_fingerprint,omitempty"`
}

// AuditInput contains only already-collected facts. Audit performs no I/O.
type AuditInput struct {
	ProviderOwned    Fact
	ProviderUnique   Fact
	ProviderMapping  Fact
	ProviderIdentity Fact
	WorkflowState    string
	WorkflowTerminal Fact
	AgentState       string
	AgentDone        Fact
	JournalStarted   Fact
	JournalResult    Fact
	NotResumed       Fact
	ProviderInactive Fact

	Registered       Fact
	PathPresent      Fact
	LinkedWorktree   Fact
	NonMain          Fact
	BranchNamed      Fact
	BranchMatches    Fact
	CommonDirMatches Fact
	HeadMatches      Fact
	Unlocked         Fact
	NotPrunable      Fact
	Clean            Fact
	IgnoredEmpty     Fact
	SubmodulesClean  Fact
	NoGitOperation   Fact
	TaskUnclaimed    Fact
	ArtifactsKnown   Fact
	ArtifactsSafe    Fact
	CallerOutside    Fact
	RuntimeKnown     Fact
	RuntimeClear     Fact
}

type Check struct {
	ID             string         `json:"id"`
	Classification Classification `json:"classification"`
	Detail         string         `json:"detail"`
}

type AuditResult struct {
	Classification Classification `json:"classification"`
	Checks         []Check        `json:"checks"`
}

// BranchDeletion is a separate action audit. It never controls whether the
// clean worktree itself may be removed, because retaining the named branch
// preserves unique commits.
type BranchDeletion struct {
	Requested     bool    `json:"requested"`
	BaseExplicit  bool    `json:"base_explicit"`
	BaseRef       string  `json:"base_ref,omitempty"`
	BaseHead      string  `json:"base_head,omitempty"`
	BranchTip     string  `json:"branch_tip,omitempty"`
	RelationKnown bool    `json:"relation_known"`
	BaseOnly      int     `json:"base_only"`
	BranchOnly    int     `json:"branch_only"`
	Safe          bool    `json:"safe"`
	Checks        []Check `json:"checks"`
}

type PlannedAction struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Candidate struct {
	Provider           string          `json:"provider,omitempty"`
	SessionID          string          `json:"session_id,omitempty"`
	RunID              string          `json:"run_id,omitempty"`
	AgentID            string          `json:"agent_id,omitempty"`
	ProviderBranch     string          `json:"provider_branch,omitempty"`
	ProviderHead       string          `json:"provider_head,omitempty"`
	ProviderCommonDir  string          `json:"provider_common_dir,omitempty"`
	ProviderGeneration string          `json:"provider_generation,omitempty"`
	WorkflowState      string          `json:"workflow_state,omitempty"`
	AgentState         string          `json:"agent_state,omitempty"`
	LastActivity       time.Time       `json:"last_activity,omitempty"`
	LastActivityKnown  bool            `json:"last_activity_known"`
	Path               string          `json:"path"`
	Branch             string          `json:"branch,omitempty"`
	RegistryHead       string          `json:"registry_head,omitempty"`
	Git                GitFacts        `json:"git"`
	Safety             SafetyFacts     `json:"safety"`
	Classification     Classification  `json:"classification"`
	Checks             []Check         `json:"checks"`
	BranchDeletion     BranchDeletion  `json:"branch_deletion"`
	PlannedActions     []PlannedAction `json:"planned_actions"`
	Fingerprint        string          `json:"fingerprint"`
}

type Summary struct {
	Total         int `json:"total"`
	Eligible      int `json:"eligible"`
	Blocked       int `json:"blocked"`
	Unknown       int `json:"unknown"`
	NotApplicable int `json:"not_applicable"`
}

type Report struct {
	SchemaVersion  int                `json:"schema_version"`
	GeneratedAt    time.Time          `json:"generated_at"`
	Repository     RepositoryIdentity `json:"repository"`
	StaleDays      int                `json:"stale_days"`
	BaseRef        string             `json:"base_ref,omitempty"`
	BaseExplicit   bool               `json:"base_explicit"`
	DeleteBranches bool               `json:"delete_branches"`
	Capabilities   []Capability       `json:"capabilities"`
	Diagnostics    []Diagnostic       `json:"diagnostics"`
	Candidates     []Candidate        `json:"candidates"`
	Summary        Summary            `json:"summary"`
}

type ApplyStatus string

const (
	ApplyRemoved        ApplyStatus = "removed"
	ApplyPartial        ApplyStatus = "partial"
	ApplySkippedChanged ApplyStatus = "skipped-changed"
	ApplyFailed         ApplyStatus = "failed"
)

type ApplyCandidateResult struct {
	Path            string      `json:"path"`
	Branch          string      `json:"branch,omitempty"`
	Status          ApplyStatus `json:"status"`
	RemovedWorktree bool        `json:"removed_worktree"`
	DeletedBranch   bool        `json:"deleted_branch"`
	BranchRetained  bool        `json:"branch_retained"`
	Detail          string      `json:"detail"`
}

type ApplyResult struct {
	SchemaVersion int                    `json:"schema_version"`
	Repository    RepositoryIdentity     `json:"repository"`
	Results       []ApplyCandidateResult `json:"results"`
}

type ReportRequest struct {
	RepoPath       string
	StaleDays      int
	BaseRef        string
	BaseExplicit   bool
	DeleteBranches bool
}

type ApplyRequest struct {
	Report       Report
	Fingerprints []string
}
