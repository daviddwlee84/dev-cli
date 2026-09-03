package ephemeral

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type repositoryState struct {
	Identity RepositoryIdentity
	Linked   bool
}

type taskEvidence struct {
	Unclaimed   Fact
	Claims      int
	Fingerprint string
}

type artifactEvidence struct {
	Known       Fact
	Safe        Fact
	Intents     int
	Fingerprint string
}

type callerEvidence struct {
	Outside     Fact
	Fingerprint string
}

type runtimeEvidence struct {
	Known       Fact
	Clear       Fact
	Covering    int
	Fingerprint string
}

type branchState struct {
	BaseHead   string
	BranchTip  string
	BaseOnly   int
	BranchOnly int
}

type liveBackend interface {
	discover(context.Context, string) (repositoryState, error)
	worktrees(context.Context, repositoryState) ([]Target, error)
	gitFacts(context.Context, repositoryState, Target) (GitFacts, error)
	taskEvidence(context.Context, repositoryState, Target) (taskEvidence, error)
	artifactEvidence(context.Context, Target) (artifactEvidence, error)
	callerEvidence(Target) (callerEvidence, error)
	runtimeEvidence(context.Context, Target) (runtimeEvidence, error)
	branchState(context.Context, repositoryState, string, string) (branchState, error)
	withCleanupLock(context.Context, string, func() error) error
	removeWorktree(context.Context, repositoryState, string) error
	verifyRemoved(context.Context, repositoryState, string) error
	deleteBranch(context.Context, repositoryState, string) error
}

// ServiceOptions wires the independently re-loadable safety inventories used by
// the system backend.
type ServiceOptions struct {
	Tasks           *task.Store
	Artifacts       *artifact.Store
	Runtimes        []runtime.Runtime
	RuntimeDisabled bool
}

// Service owns the provider-neutral report-before-apply policy.
type Service struct {
	source  OwnershipSource
	backend liveBackend
	now     func() time.Time

	// beforeRevalidate is a package-private deterministic race seam. It runs only
	// after the cleanup lock has been acquired.
	beforeRevalidate func()
}

func NewService(source OwnershipSource, options ServiceOptions) *Service {
	return &Service{
		source:  source,
		backend: newSystemBackend(options),
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Report(ctx context.Context, request ReportRequest) (Report, error) {
	if request.StaleDays < 1 {
		return Report{}, fmt.Errorf("stale-days must be at least 1")
	}
	if s == nil || s.backend == nil {
		return Report{}, fmt.Errorf("ephemeral service has no live-state backend")
	}
	repository, err := s.backend.discover(ctx, request.RepoPath)
	if err != nil {
		return Report{}, fmt.Errorf("discover canonical repository: %w", err)
	}
	if repository.Identity.Bare {
		return Report{}, fmt.Errorf("ephemeral worktree sweep requires a non-bare canonical checkout")
	}
	if repository.Linked {
		return Report{}, fmt.Errorf("ephemeral worktree sweep must run from the canonical checkout")
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	report := Report{
		SchemaVersion: SchemaVersion, GeneratedAt: now, Repository: repository.Identity,
		StaleDays: request.StaleDays, BaseRef: request.BaseRef, BaseExplicit: request.BaseExplicit,
		DeleteBranches: request.DeleteBranches, Capabilities: []Capability{}, Diagnostics: []Diagnostic{}, Candidates: []Candidate{},
	}

	targets, worktreeErr := s.backend.worktrees(ctx, repository)
	worktreesKnown := worktreeErr == nil
	report.Capabilities = append(report.Capabilities, Capability{
		Name: "git-worktree-inventory", Available: worktreesKnown, Detail: availabilityDetail(worktreesKnown),
	})
	if worktreeErr != nil {
		report.Diagnostics = append(report.Diagnostics, diagnostic("git", "worktree-inventory-failed", "Git worktree inventory could not be verified"))
		targets = nil
	}
	for i := range targets {
		targets[i].Registered = true
		targets[i].RegistrationKnown = true
	}

	source := SourceResult{Complete: false}
	if s.source != nil {
		source = s.source.Collect(ctx, SourceQuery{Repository: repository.Identity, Targets: targets, Now: now})
	} else {
		source.Diagnostics = append(source.Diagnostics, diagnostic("provider", "provider-unavailable", "provider ownership metadata is unavailable"))
	}
	report.Capabilities = append(report.Capabilities, source.Capabilities...)
	report.Diagnostics = append(report.Diagnostics, source.Diagnostics...)

	byPath := make(map[string]Target, len(targets))
	for _, target := range targets {
		byPath[target.Path] = target
	}
	claimedPaths := make(map[string]bool)
	for _, claim := range source.Claims {
		target, exists := byPath[claim.WorktreePath]
		if !exists {
			target = Target{Path: claim.WorktreePath, Registered: false, RegistrationKnown: worktreesKnown, Hint: true}
		}
		claimedPaths[claim.WorktreePath] = true
		candidate, diagnostics, states := s.collectCandidate(ctx, repository, target, &claim, source.Complete, worktreesKnown, request, now)
		report.Candidates = append(report.Candidates, candidate)
		report.Diagnostics = append(report.Diagnostics, diagnostics...)
		report.Capabilities = mergeEvidenceCapabilities(report.Capabilities, states)
	}
	for _, target := range targets {
		if !target.Hint || claimedPaths[target.Path] {
			continue
		}
		candidate, diagnostics, states := s.collectCandidate(ctx, repository, target, nil, source.Complete, worktreesKnown, request, now)
		report.Candidates = append(report.Candidates, candidate)
		report.Diagnostics = append(report.Diagnostics, diagnostics...)
		report.Capabilities = mergeEvidenceCapabilities(report.Capabilities, states)
	}

	sort.Slice(report.Candidates, func(i, j int) bool {
		left, right := report.Candidates[i], report.Candidates[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.RunID != right.RunID {
			return left.RunID < right.RunID
		}
		return left.AgentID < right.AgentID
	})
	report.Capabilities = stableCapabilities(report.Capabilities)
	report.Diagnostics = stableDiagnostics(report.Diagnostics)
	for _, candidate := range report.Candidates {
		report.Summary.Total++
		switch candidate.Classification {
		case Eligible:
			report.Summary.Eligible++
		case Blocked:
			report.Summary.Blocked++
		case Unknown:
			report.Summary.Unknown++
		case NotApplicable:
			report.Summary.NotApplicable++
		}
	}
	return report, nil
}

type evidenceCapabilities struct {
	tasks, artifacts, caller, runtime bool
}

func (s *Service) collectCandidate(ctx context.Context, repository repositoryState, target Target, claim *Claim,
	sourceComplete, worktreesKnown bool, request ReportRequest, now time.Time) (Candidate, []Diagnostic, evidenceCapabilities) {

	candidate := Candidate{Path: target.Path, Branch: target.Branch, RegistryHead: target.RegistryHead}
	var diagnostics []Diagnostic
	states := evidenceCapabilities{tasks: true, artifacts: true, caller: true, runtime: true}
	providerOwned := Fact{}
	providerUnique, providerMapping, providerIdentity := Fact{}, Fact{}, Fact{}
	workflowTerminal, agentDone, journalStarted, journalResult, notResumed, inactive := Fact{}, Fact{}, Fact{}, Fact{}, Fact{}, Fact{}
	if claim != nil {
		candidate.Provider, candidate.SessionID, candidate.RunID, candidate.AgentID = claim.Provider, claim.SessionID, claim.RunID, claim.AgentID
		candidate.ProviderBranch, candidate.ProviderHead = claim.ObservedBranch, claim.ObservedHead
		candidate.ProviderCommonDir, candidate.ProviderGeneration = claim.ObservedCommonDir, claim.ObservedGeneration
		candidate.WorkflowState, candidate.AgentState = claim.WorkflowState, claim.AgentState
		candidate.LastActivity, candidate.LastActivityKnown = claim.LastActivity, claim.LastActivityKnown
		providerOwned, providerUnique, providerMapping = claim.Owned, claim.Unique, claim.Mapping
		workflowTerminal, agentDone = claim.WorkflowTerminal, claim.AgentDone
		journalStarted, journalResult, notResumed = claim.JournalStarted, claim.JournalResult, claim.NotResumed
		if claim.LastActivityKnown {
			inactive = providerInactive(now, claim.LastActivity, request.StaleDays)
		}
	} else if sourceComplete {
		providerOwned = KnownFact(false)
	}

	gitFacts, err := s.backend.gitFacts(ctx, repository, target)
	if err != nil {
		diagnostics = append(diagnostics, diagnostic("git", "checkout-inspection-failed", "checkout Git facts could not be verified"))
		gitFacts = mergeStructuralGitFacts(gitFacts, target, worktreesKnown)
	}
	if claim != nil && claim.GitIdentity.Known {
		if !claim.GitIdentity.Value {
			providerIdentity = KnownFact(false)
		} else if claim.ObservedBranch == "" || claim.ObservedHead == "" || claim.ObservedCommonDir == "" ||
			claim.ObservedGeneration == "" || target.Branch == "" || target.RegistryHead == "" ||
			gitFacts.LiveHead == "" || gitFacts.CommonDir == "" || gitFacts.RegistrationGeneration == "" {
			providerIdentity = Fact{}
		} else {
			providerIdentity = KnownFact(
				claim.ObservedBranch == target.Branch &&
					claim.ObservedHead == target.RegistryHead && claim.ObservedHead == gitFacts.LiveHead &&
					claim.ObservedCommonDir == repository.Identity.CommonDir && claim.ObservedCommonDir == gitFacts.CommonDir &&
					claim.ObservedGeneration == gitFacts.RegistrationGeneration,
			)
		}
	}
	candidate.Git = gitFacts

	tasks, err := s.backend.taskEvidence(ctx, repository, target)
	if err != nil {
		states.tasks = false
		diagnostics = append(diagnostics, diagnostic("task", "task-inventory-failed", "task claims could not be verified"))
	}
	artifacts, err := s.backend.artifactEvidence(ctx, target)
	if err != nil {
		states.artifacts = false
		diagnostics = append(diagnostics, diagnostic("artifact", "artifact-inventory-failed", "artifact intents could not be verified"))
	}
	caller, err := s.backend.callerEvidence(target)
	if err != nil {
		states.caller = false
		diagnostics = append(diagnostics, diagnostic("caller", "caller-containment-failed", "caller containment could not be verified"))
	}
	runtimeFacts, err := s.backend.runtimeEvidence(ctx, target)
	if err != nil {
		states.runtime = false
		diagnostics = append(diagnostics, diagnostic("runtime", "runtime-inventory-failed", "runtime coverage could not be verified"))
	}
	candidate.Safety = SafetyFacts{
		TaskUnclaimed: tasks.Unclaimed, TaskClaims: tasks.Claims, TaskFingerprint: tasks.Fingerprint,
		ArtifactsKnown: artifacts.Known, ArtifactsSafe: artifacts.Safe, ArtifactIntents: artifacts.Intents, ArtifactFingerprint: artifacts.Fingerprint,
		CallerOutside: caller.Outside, CallerFingerprint: caller.Fingerprint,
		RuntimeKnown: runtimeFacts.Known, RuntimeClear: runtimeFacts.Clear,
		CoveringSessions: runtimeFacts.Covering, RuntimeFingerprint: runtimeFacts.Fingerprint,
	}

	audit := Audit(AuditInput{
		ProviderOwned: providerOwned, ProviderUnique: providerUnique, ProviderMapping: providerMapping,
		ProviderIdentity: providerIdentity, WorkflowState: candidate.WorkflowState, WorkflowTerminal: workflowTerminal,
		AgentState: candidate.AgentState, AgentDone: agentDone,
		JournalStarted: journalStarted, JournalResult: journalResult, NotResumed: notResumed, ProviderInactive: inactive,
		Registered: gitFacts.Registered, PathPresent: gitFacts.PathPresent, LinkedWorktree: gitFacts.LinkedWorktree,
		NonMain: gitFacts.NonMain, BranchNamed: gitFacts.BranchNamed, BranchMatches: gitFacts.BranchMatches,
		CommonDirMatches: gitFacts.CommonDirMatches,
		HeadMatches:      gitFacts.HeadMatches, Unlocked: gitFacts.Unlocked, NotPrunable: gitFacts.NotPrunable,
		Clean: gitFacts.Clean, IgnoredEmpty: gitFacts.IgnoredEmpty, SubmodulesClean: gitFacts.SubmodulesClean,
		NoGitOperation: gitFacts.NoGitOperation, TaskUnclaimed: tasks.Unclaimed,
		ArtifactsKnown: artifacts.Known, ArtifactsSafe: artifacts.Safe, CallerOutside: caller.Outside,
		RuntimeKnown: runtimeFacts.Known, RuntimeClear: runtimeFacts.Clear,
	})
	candidate.Classification, candidate.Checks = audit.Classification, audit.Checks
	candidate.BranchDeletion = s.auditBranchDeletion(ctx, repository, target, request)
	candidate.PlannedActions = plannedActions(candidate)
	candidate.Fingerprint = candidateFingerprint(candidate)
	return candidate, diagnostics, states
}

func providerInactive(now, lastActivity time.Time, staleDays int) Fact {
	if staleDays < 1 {
		return Fact{}
	}
	const day = 24 * time.Hour
	maxDays := uint64((time.Duration(1<<63 - 1)) / day)
	if uint64(staleDays) > maxDays {
		return Fact{}
	}
	if lastActivity.After(now) {
		return KnownFact(false)
	}
	return KnownFact(now.Sub(lastActivity) >= time.Duration(staleDays)*day)
}

func structuralGitFacts(target Target, worktreesKnown bool) GitFacts {
	facts := GitFacts{NonMain: KnownFact(!target.Main)}
	if worktreesKnown && target.RegistrationKnown {
		facts.Registered = KnownFact(target.Registered)
		facts.LinkedWorktree = KnownFact(target.Registered && !target.Main && !target.Bare)
		facts.BranchNamed = KnownFact(target.Branch != "" && !target.Detached)
		facts.Unlocked = KnownFact(!target.Locked)
		facts.NotPrunable = KnownFact(!target.Prunable)
	}
	return facts
}

func mergeStructuralGitFacts(facts GitFacts, target Target, worktreesKnown bool) GitFacts {
	fallback := structuralGitFacts(target, worktreesKnown)
	for current, replacement := range map[*Fact]Fact{
		&facts.Registered: fallback.Registered, &facts.LinkedWorktree: fallback.LinkedWorktree,
		&facts.NonMain: fallback.NonMain, &facts.BranchNamed: fallback.BranchNamed,
		&facts.Unlocked: fallback.Unlocked, &facts.NotPrunable: fallback.NotPrunable,
	} {
		if !current.Known {
			*current = replacement
		}
	}
	return facts
}

func (s *Service) auditBranchDeletion(ctx context.Context, repository repositoryState, target Target, request ReportRequest) BranchDeletion {
	result := BranchDeletion{Requested: request.DeleteBranches, BaseExplicit: request.BaseExplicit, BaseRef: request.BaseRef, Checks: []Check{}}
	if !request.DeleteBranches {
		return result
	}
	result.Checks = append(result.Checks, branchCheck("base-explicit", request.BaseExplicit && request.BaseRef != "", "an explicit base was supplied", "branch deletion requires an explicit base"))
	distinctBase := target.Branch != "" && !localBranchAlias(request.BaseRef, target.Branch)
	result.Checks = append(result.Checks, branchCheck("branch-distinct-from-base", distinctBase, "branch is named and differs from base", "branch is absent or equals the base"))
	if !request.BaseExplicit || request.BaseRef == "" || !distinctBase {
		result.Checks = append(result.Checks,
			Check{ID: "base-and-tip-resolve", Classification: Unknown, Detail: "base and branch tips were not resolved"},
			Check{ID: "branch-contained", Classification: Unknown, Detail: "branch containment is unknown"},
			Check{ID: "zero-unique-commits", Classification: Unknown, Detail: "unique commit count is unknown"})
		return result
	}
	state, err := s.backend.branchState(ctx, repository, request.BaseRef, target.Branch)
	if err != nil {
		result.Checks = append(result.Checks,
			Check{ID: "base-and-tip-resolve", Classification: Unknown, Detail: "base or branch tip could not be resolved"},
			Check{ID: "branch-contained", Classification: Unknown, Detail: "branch containment is unknown"},
			Check{ID: "zero-unique-commits", Classification: Unknown, Detail: "unique commit count is unknown"})
		return result
	}
	result.BaseHead, result.BranchTip = state.BaseHead, state.BranchTip
	result.RelationKnown, result.BaseOnly, result.BranchOnly = true, state.BaseOnly, state.BranchOnly
	result.Checks = append(result.Checks,
		Check{ID: "base-and-tip-resolve", Classification: Eligible, Detail: "base and branch tips resolve"},
		branchCheck("branch-contained", state.BranchOnly == 0, "branch is contained in the explicit base", "branch is not contained in the explicit base"),
		branchCheck("zero-unique-commits", state.BranchOnly == 0, "branch has zero commits unique from base", "branch has commits unique from base"))
	result.Safe = checksEligible(result.Checks)
	return result
}

func localBranchAlias(baseRef, branch string) bool {
	if branch == "" {
		return false
	}
	return baseRef == branch || baseRef == "refs/heads/"+branch || baseRef == "heads/"+branch
}

func branchCheck(id string, value bool, yes, no string) Check {
	if value {
		return Check{ID: id, Classification: Eligible, Detail: yes}
	}
	return Check{ID: id, Classification: Blocked, Detail: no}
}

func checksEligible(checks []Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Classification != Eligible {
			return false
		}
	}
	return true
}

func plannedActions(candidate Candidate) []PlannedAction {
	removeStatus := "blocked"
	removeDetail := "candidate is not eligible; report only"
	if candidate.Classification == Eligible {
		removeStatus, removeDetail = "planned", "remove with plain non-force git worktree remove after locked revalidation"
	}
	actions := []PlannedAction{{Kind: "remove-worktree", Target: candidate.Path, Status: removeStatus, Detail: removeDetail}}
	if candidate.Classification == Eligible && candidate.BranchDeletion.Requested && candidate.BranchDeletion.Safe {
		actions = append(actions, PlannedAction{Kind: "delete-branch", Target: candidate.Branch, Status: "planned", Detail: "delete with git branch -d after separate post-removal revalidation"})
	} else {
		detail := "branch is retained by default"
		if candidate.BranchDeletion.Requested {
			detail = "candidate or branch deletion proof is not eligible; retain the branch"
		}
		actions = append(actions, PlannedAction{Kind: "retain-branch", Target: candidate.Branch, Status: "planned", Detail: detail})
	}
	return actions
}

func candidateFingerprint(candidate Candidate) string {
	candidate.Fingerprint = ""
	data, err := json.Marshal(candidate)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Service) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	if request.Report.SchemaVersion != SchemaVersion {
		return ApplyResult{}, fmt.Errorf("unsupported ephemeral report schema %d", request.Report.SchemaVersion)
	}
	if len(request.Fingerprints) == 0 {
		return ApplyResult{SchemaVersion: SchemaVersion, Repository: request.Report.Repository, Results: []ApplyCandidateResult{}}, nil
	}
	repository, err := s.backend.discover(ctx, request.Report.Repository.Root)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("rediscover canonical repository: %w", err)
	}
	if repository.Linked || repository.Identity.Bare || repository.Identity.Root != request.Report.Repository.Root ||
		repository.Identity.CommonDir != request.Report.Repository.CommonDir {
		return ApplyResult{}, fmt.Errorf("repository identity changed since the report")
	}
	selected := make(map[string]bool, len(request.Fingerprints))
	for _, fingerprint := range request.Fingerprints {
		selected[fingerprint] = true
	}
	result := ApplyResult{SchemaVersion: SchemaVersion, Repository: repository.Identity, Results: []ApplyCandidateResult{}}
	err = s.backend.withCleanupLock(ctx, repository.Identity.CommonDir, func() error {
		if s.beforeRevalidate != nil {
			s.beforeRevalidate()
		}
		for _, prior := range request.Report.Candidates {
			if !selected[prior.Fingerprint] {
				continue
			}
			fresh, err := s.Report(ctx, ReportRequest{
				RepoPath: request.Report.Repository.Root, StaleDays: request.Report.StaleDays,
				BaseRef: request.Report.BaseRef, BaseExplicit: request.Report.BaseExplicit,
				DeleteBranches: request.Report.DeleteBranches,
			})
			if err != nil {
				return fmt.Errorf("recollect cleanup report: %w", err)
			}
			current, exists := findCandidate(fresh.Candidates, candidateKey(prior))
			item := ApplyCandidateResult{Path: prior.Path, Branch: prior.Branch, BranchRetained: prior.Branch != ""}
			if prior.Classification != Eligible || !exists || current.Classification != Eligible || current.Fingerprint != prior.Fingerprint {
				item.Status = ApplySkippedChanged
				item.Detail = "candidate evidence changed after the report; nothing was removed"
				result.Results = append(result.Results, item)
				continue
			}
			if err := s.backend.removeWorktree(ctx, repository, prior.Path); err != nil {
				item.Status = ApplyFailed
				item.Detail = "plain non-force worktree removal was refused"
				result.Results = append(result.Results, item)
				continue
			}
			item.RemovedWorktree = true
			if err := s.backend.verifyRemoved(ctx, repository, prior.Path); err != nil {
				item.Status = ApplyFailed
				item.Detail = "worktree removal could not be verified; branch was retained"
				result.Results = append(result.Results, item)
				continue
			}
			if prior.BranchDeletion.Requested {
				if !prior.BranchDeletion.Safe || !current.BranchDeletion.Safe {
					item.Status = ApplyPartial
					item.Detail = "worktree removed; branch retained because deletion proof was unsafe"
					result.Results = append(result.Results, item)
					continue
				}
				state, stateErr := s.backend.branchState(ctx, repository, prior.BranchDeletion.BaseRef, prior.Branch)
				if stateErr != nil || state.BaseHead != prior.BranchDeletion.BaseHead || state.BranchTip != prior.BranchDeletion.BranchTip || state.BranchOnly != 0 {
					item.Status = ApplyPartial
					item.Detail = "worktree removed; branch or base proof changed, so the branch was retained"
					result.Results = append(result.Results, item)
					continue
				}
				if err := s.backend.deleteBranch(ctx, repository, prior.Branch); err != nil {
					item.Status = ApplyPartial
					item.Detail = "worktree removed; safe branch -d did not complete, so the branch was retained"
					result.Results = append(result.Results, item)
					continue
				}
				item.DeletedBranch, item.BranchRetained = true, false
			}
			item.Status = ApplyRemoved
			item.Detail = "worktree removed after locked revalidation"
			result.Results = append(result.Results, item)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func findCandidate(candidates []Candidate, key string) (Candidate, bool) {
	for _, candidate := range candidates {
		if candidateKey(candidate) == key {
			return candidate, true
		}
	}
	return Candidate{}, false
}

func candidateKey(candidate Candidate) string {
	return strings.Join([]string{candidate.Provider, candidate.SessionID, candidate.RunID, candidate.AgentID, candidate.Path}, "\x00")
}

func availabilityDetail(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}

func diagnostic(source, code, message string) Diagnostic {
	return Diagnostic{Source: source, Code: code, Message: message}
}

func mergeEvidenceCapabilities(existing []Capability, states evidenceCapabilities) []Capability {
	return append(existing,
		Capability{Name: "task-inventory", Available: states.tasks, Detail: availabilityDetail(states.tasks)},
		Capability{Name: "artifact-inventory", Available: states.artifacts, Detail: availabilityDetail(states.artifacts)},
		Capability{Name: "caller-containment", Available: states.caller, Detail: availabilityDetail(states.caller)},
		Capability{Name: "runtime-inventory", Available: states.runtime, Detail: availabilityDetail(states.runtime)},
	)
}

func stableCapabilities(values []Capability) []Capability {
	byName := make(map[string]Capability)
	for _, value := range values {
		if value.Name == "" {
			continue
		}
		current, exists := byName[value.Name]
		if !exists {
			byName[value.Name] = value
			continue
		}
		current.Available = current.Available && value.Available
		if !current.Available {
			current.Detail = "unavailable"
		}
		byName[value.Name] = current
	}
	out := make([]Capability, 0, len(byName))
	for _, value := range byName {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func stableDiagnostics(values []Diagnostic) []Diagnostic {
	seen := make(map[string]bool)
	out := make([]Diagnostic, 0, len(values))
	for _, value := range values {
		key := value.Source + "\x00" + value.Code + "\x00" + value.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Message < out[j].Message
	})
	return out
}
