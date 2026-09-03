package ephemeral

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

var serviceNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

type fakeSource struct {
	complete bool
	claims   []Claim
	calls    int
}

func (f *fakeSource) Collect(context.Context, SourceQuery) SourceResult {
	f.calls++
	return SourceResult{
		Provider: providerNameForTest, Complete: f.complete,
		Claims:       append([]Claim(nil), f.claims...),
		Capabilities: []Capability{{Name: "provider-metadata", Available: f.complete}},
	}
}

const providerNameForTest = "fixture-provider"

type fakeBackend struct {
	repository repositoryState
	targets    []Target
	git        map[string]GitFacts
	tasks      map[string]taskEvidence
	artifacts  map[string]artifactEvidence
	callers    map[string]callerEvidence
	runtimes   map[string]runtimeEvidence
	branches   []branchState

	worktreeErr error
	removeErr   error
	verifyErr   error
	deleteErr   error

	locks       int
	removes     []string
	verifies    []string
	deletes     []string
	branchCalls int
}

func (f *fakeBackend) discover(context.Context, string) (repositoryState, error) {
	return f.repository, nil
}

func (f *fakeBackend) worktrees(context.Context, repositoryState) ([]Target, error) {
	if f.worktreeErr != nil {
		return nil, f.worktreeErr
	}
	return append([]Target(nil), f.targets...), nil
}

func (f *fakeBackend) gitFacts(_ context.Context, _ repositoryState, target Target) (GitFacts, error) {
	return f.git[target.Path], nil
}

func (f *fakeBackend) taskEvidence(_ context.Context, _ repositoryState, target Target) (taskEvidence, error) {
	return f.tasks[target.Path], nil
}

func (f *fakeBackend) artifactEvidence(_ context.Context, target Target) (artifactEvidence, error) {
	return f.artifacts[target.Path], nil
}

func (f *fakeBackend) callerEvidence(target Target) (callerEvidence, error) {
	return f.callers[target.Path], nil
}

func (f *fakeBackend) runtimeEvidence(_ context.Context, target Target) (runtimeEvidence, error) {
	return f.runtimes[target.Path], nil
}

func (f *fakeBackend) branchState(context.Context, repositoryState, string, string) (branchState, error) {
	f.branchCalls++
	if len(f.branches) == 0 {
		return branchState{}, errors.New("no branch fixture")
	}
	state := f.branches[0]
	if len(f.branches) > 1 {
		f.branches = f.branches[1:]
	}
	return state, nil
}

func (f *fakeBackend) withCleanupLock(_ context.Context, _ string, operation func() error) error {
	f.locks++
	return operation()
}

func (f *fakeBackend) removeWorktree(_ context.Context, _ repositoryState, path string) error {
	f.removes = append(f.removes, path)
	return f.removeErr
}

func (f *fakeBackend) verifyRemoved(_ context.Context, _ repositoryState, path string) error {
	f.verifies = append(f.verifies, path)
	return f.verifyErr
}

func (f *fakeBackend) deleteBranch(_ context.Context, _ repositoryState, branch string) error {
	f.deletes = append(f.deletes, branch)
	return f.deleteErr
}

type runtimeFixture struct {
	name      string
	available bool
	sessions  []runtime.Session
}

func (r *runtimeFixture) Name() string    { return r.name }
func (r *runtimeFixture) Available() bool { return r.available }
func (r *runtimeFixture) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (r *runtimeFixture) Close(context.Context, string) error { return nil }
func (r *runtimeFixture) List(context.Context) ([]runtime.Session, error) {
	return append([]runtime.Session(nil), r.sessions...), nil
}
func (r *runtimeFixture) Annotate(context.Context, string, map[string]string) error { return nil }

type systemRuntimeEvidenceBackend struct {
	*fakeBackend
	system *systemBackend
}

func (b *systemRuntimeEvidenceBackend) runtimeEvidence(ctx context.Context, target Target) (runtimeEvidence, error) {
	return b.system.runtimeEvidence(ctx, target)
}

func TestEmptyPathLiveRuntimeSessionPreventsEligibilityAndRemoval(t *testing.T) {
	service, backend, _ := fixtureService(t)
	live := &runtimeFixture{name: "fixture", available: true, sessions: []runtime.Session{{Handle: "live-without-paths"}}}
	service.backend = &systemRuntimeEvidenceBackend{
		fakeBackend: backend,
		system:      newSystemBackend(ServiceOptions{Runtimes: []runtime.Runtime{live}}),
	}
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].Classification != Unknown ||
		report.Candidates[0].Safety.RuntimeKnown.Known || statusOf(AuditResult{Checks: report.Candidates[0].Checks}, CheckRuntimeKnown) != Unknown {
		t.Fatalf("empty-path live session did not make runtime evidence unknown: %+v", report)
	}
	result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != ApplySkippedChanged || len(backend.removes) != 0 {
		t.Fatalf("unknown runtime evidence allowed removal: result=%+v backend=%+v", result, backend)
	}
}

func TestSystemRuntimeEvidencePreservesNoneAndUnavailableHandling(t *testing.T) {
	target := Target{Path: t.TempDir()}
	unavailable := &runtimeFixture{
		name: "unavailable", available: false,
		sessions: []runtime.Session{{Handle: "unqueryable-without-paths"}},
	}
	backend := newSystemBackend(ServiceOptions{Runtimes: []runtime.Runtime{runtime.None{}, unavailable}})
	evidence, err := backend.runtimeEvidence(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Known.Known || !evidence.Known.Value || !evidence.Clear.Known || !evidence.Clear.Value || evidence.Covering != 0 {
		t.Fatalf("none/unavailable evidence = %+v", evidence)
	}
}

func TestReportIsSideEffectFreeSortedAndVersioned(t *testing.T) {
	service, backend, source := fixtureService(t)
	second := backend.targets[0]
	second.Path = "/repo/.claude/worktrees/a"
	second.Branch = "worktree-a"
	backend.targets = append(backend.targets, second)
	backend.git[second.Path] = eligibleGit(second)
	backend.tasks[second.Path] = knownNoTask()
	backend.artifacts[second.Path] = knownSafeArtifacts()
	backend.callers[second.Path] = knownCallerOutside()
	backend.runtimes[second.Path] = knownNoRuntime()
	claim := source.claims[0]
	claim.WorktreePath = second.Path
	claim.AgentID = "agent-a"
	claim.ObservedBranch, claim.ObservedHead = second.Branch, second.RegistryHead
	claim.ObservedGeneration = fakeRegistration(second)
	source.claims = append(source.claims, claim)

	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != SchemaVersion || report.Summary.Total != 2 || report.Summary.Eligible != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Candidates[0].Path != second.Path || report.Candidates[1].Path != backend.targets[0].Path {
		t.Fatalf("candidates not sorted: %+v", report.Candidates)
	}
	for _, candidate := range report.Candidates {
		if candidate.Fingerprint == "" || candidate.Classification != Eligible {
			t.Errorf("candidate = %+v", candidate)
		}
		if candidate.BranchDeletion.Requested || len(candidate.PlannedActions) != 2 || candidate.PlannedActions[1].Kind != "retain-branch" {
			t.Errorf("default action must retain branch: %+v", candidate)
		}
	}
	if backend.locks != 0 || len(backend.removes) != 0 || len(backend.deletes) != 0 || backend.branchCalls != 0 {
		t.Fatalf("report had side effects: %+v", backend)
	}
}

func TestReportIncludesHintWithoutClaimAsNotApplicableOrUnknown(t *testing.T) {
	service, _, source := fixtureService(t)
	source.claims = nil
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates[0].Classification != NotApplicable {
		t.Fatalf("complete provider absence = %+v", report.Candidates[0])
	}

	source.complete = false
	report, err = service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates[0].Classification != Unknown {
		t.Fatalf("incomplete provider absence = %+v", report.Candidates[0])
	}
}

func TestReportWorktreeInventoryFailureKeepsRegistrationUnknown(t *testing.T) {
	service, backend, _ := fixtureService(t)
	backend.worktreeErr = errors.New("unavailable")
	facts := backend.git[backend.targets[0].Path]
	facts.Registered = Fact{}
	facts.LinkedWorktree = Fact{}
	backend.git[backend.targets[0].Path] = facts
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].Classification != Unknown || report.Candidates[0].Git.Registered.Known {
		t.Fatalf("report = %+v", report)
	}
}

func TestReportEmptyCollectionsEncodeAsArrays(t *testing.T) {
	service, backend, source := fixtureService(t)
	backend.targets = nil
	source.claims = nil
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"candidates":[]`, `"diagnostics":[]`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("report JSON missing %s: %s", want, encoded)
		}
	}
}

func TestHugeStaleDaysNeverAuthorizeRecentProviderActivity(t *testing.T) {
	service, backend, source := fixtureService(t)
	source.claims[0].LastActivity = serviceNow.Add(-time.Hour)
	report, err := service.Report(t.Context(), ReportRequest{
		RepoPath: "/repo", StaleDays: int(^uint(0) >> 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := report.Candidates[0]
	if candidate.Classification == Eligible || statusOf(AuditResult{Checks: candidate.Checks}, CheckProviderInactive) != Unknown {
		t.Fatalf("overflowing stale-days authorized recent activity: %+v", candidate)
	}
	result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{candidate.Fingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].RemovedWorktree || len(backend.removes) != 0 {
		t.Fatalf("overflowing stale-days allowed removal: result=%+v backend=%+v", result, backend)
	}
}

func TestBranchDeletionRejectsLocalAliasesOfTargetBranch(t *testing.T) {
	for _, base := range []string{"refs/heads/worktree-z", "heads/worktree-z"} {
		t.Run(base, func(t *testing.T) {
			service, backend, _ := fixtureService(t)
			backend.branches = []branchState{
				{BaseHead: "head", BranchTip: "head", BranchOnly: 0},
				{BaseHead: "head", BranchTip: "head", BranchOnly: 0},
				{BaseHead: "head", BranchTip: "head", BranchOnly: 0},
			}
			report, err := service.Report(t.Context(), ReportRequest{
				RepoPath: "/repo", StaleDays: 14, BaseRef: base, BaseExplicit: true, DeleteBranches: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			candidate := report.Candidates[0]
			if candidate.BranchDeletion.Safe || candidate.PlannedActions[1].Kind != "retain-branch" {
				t.Fatalf("self-base alias was considered safe: %+v", candidate.BranchDeletion)
			}
			result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{candidate.Fingerprint}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Results) != 1 || !result.Results[0].RemovedWorktree || !result.Results[0].BranchRetained ||
				result.Results[0].DeletedBranch || len(backend.deletes) != 0 {
				t.Fatalf("self-base alias deleted branch: result=%+v backend=%+v", result, backend)
			}
		})
	}
}

func TestReportKeepsUniqueCommitsWhenBranchRetained(t *testing.T) {
	service, backend, _ := fixtureService(t)
	backend.branches = []branchState{{BaseHead: "base", BranchTip: "tip", BaseOnly: 0, BranchOnly: 3}}
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates[0].Classification != Eligible || backend.branchCalls != 0 {
		t.Fatalf("retained branch must not require containment: %+v", report.Candidates[0])
	}

	backend.branches = []branchState{{BaseHead: "base", BranchTip: "tip", BaseOnly: 0, BranchOnly: 3}}
	report, err = service.Report(t.Context(), ReportRequest{
		RepoPath: "/repo", StaleDays: 14, BaseRef: "main", BaseExplicit: true, DeleteBranches: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := report.Candidates[0]
	if candidate.Classification != Eligible || candidate.BranchDeletion.Safe || candidate.BranchDeletion.BranchOnly != 3 ||
		candidate.PlannedActions[1].Kind != "retain-branch" {
		t.Fatalf("unsafe branch deletion should not block worktree removal: %+v", candidate)
	}
}

func TestApplyRecollectsEveryProofAndSkipsChangedCandidates(t *testing.T) {
	tests := map[string]func(*fakeBackend, *fakeSource){
		"provider": func(_ *fakeBackend, source *fakeSource) {
			source.claims[0].AgentState = "progress"
			source.claims[0].AgentDone = KnownFact(false)
		},
		"provider identity": func(_ *fakeBackend, source *fakeSource) {
			source.claims[0].ObservedGeneration = "replayed-generation"
		},
		"registration":  func(backend *fakeBackend, _ *fakeSource) { backend.targets = nil },
		"branch":        func(backend *fakeBackend, _ *fakeSource) { backend.targets[0].Branch = "worktree-changed" },
		"registry head": func(backend *fakeBackend, _ *fakeSource) { backend.targets[0].RegistryHead = "changed" },
		"path": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.PathPresent = KnownFact(false)
			backend.git[backend.targets[0].Path] = facts
		},
		"live branch": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.BranchMatches = KnownFact(false)
			backend.git[backend.targets[0].Path] = facts
		},
		"common dir": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.CommonDirMatches = KnownFact(false)
			backend.git[backend.targets[0].Path] = facts
		},
		"live head": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.LiveHead = "changed"
			facts.HeadMatches = KnownFact(false)
			backend.git[backend.targets[0].Path] = facts
		},
		"lock": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.Unlocked = KnownFact(false)
			backend.git[backend.targets[0].Path] = facts
		},
		"prunable": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.NotPrunable = KnownFact(false)
			backend.git[backend.targets[0].Path] = facts
		},
		"dirty": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.Clean = KnownFact(false)
			facts.Unstaged = 1
			backend.git[backend.targets[0].Path] = facts
		},
		"ignored": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.IgnoredEmpty = KnownFact(false)
			facts.Ignored = 1
			backend.git[backend.targets[0].Path] = facts
		},
		"submodule": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.SubmodulesClean = KnownFact(false)
			facts.DirtySubmodules = 1
			backend.git[backend.targets[0].Path] = facts
		},
		"operation": func(backend *fakeBackend, _ *fakeSource) {
			facts := backend.git[backend.targets[0].Path]
			facts.NoGitOperation = KnownFact(false)
			facts.Operation = "MERGE_HEAD"
			backend.git[backend.targets[0].Path] = facts
		},
		"task": func(backend *fakeBackend, _ *fakeSource) {
			backend.tasks[backend.targets[0].Path] = taskEvidence{Unclaimed: KnownFact(false), Claims: 1, Fingerprint: "task"}
		},
		"artifact": func(backend *fakeBackend, _ *fakeSource) {
			backend.artifacts[backend.targets[0].Path] = artifactEvidence{Known: KnownFact(true), Safe: KnownFact(false), Intents: 1, Fingerprint: "artifact"}
		},
		"caller": func(backend *fakeBackend, _ *fakeSource) {
			backend.callers[backend.targets[0].Path] = callerEvidence{Outside: KnownFact(false), Fingerprint: "caller"}
		},
		"runtime": func(backend *fakeBackend, _ *fakeSource) {
			backend.runtimes[backend.targets[0].Path] = runtimeEvidence{Known: KnownFact(true), Clear: KnownFact(false), Covering: 1, Fingerprint: "runtime"}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			service, backend, source := fixtureService(t)
			report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
			if err != nil {
				t.Fatal(err)
			}
			service.beforeRevalidate = func() { mutate(backend, source) }
			result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Results) != 1 || result.Results[0].Status != ApplySkippedChanged {
				t.Fatalf("apply result = %+v", result)
			}
			if backend.locks != 1 || len(backend.removes) != 0 || len(backend.deletes) != 0 {
				t.Fatalf("changed proof caused mutation: %+v", backend)
			}
		})
	}
}

func TestApplyRemovesNonForceAndRetainsBranchByDefault(t *testing.T) {
	service, backend, _ := fixtureService(t)
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != ApplyRemoved || !result.Results[0].RemovedWorktree ||
		!result.Results[0].BranchRetained || result.Results[0].DeletedBranch {
		t.Fatalf("apply result = %+v", result)
	}
	if backend.locks != 1 || !reflect.DeepEqual(backend.removes, []string{backend.targets[0].Path}) ||
		!reflect.DeepEqual(backend.verifies, []string{backend.targets[0].Path}) || len(backend.deletes) != 0 {
		t.Fatalf("backend mutations = %+v", backend)
	}
}

func TestApplyBranchDeletionRevalidatesAfterRemoval(t *testing.T) {
	t.Run("safe", func(t *testing.T) {
		service, backend, _ := fixtureService(t)
		backend.branches = []branchState{
			{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
			{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
			{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
		}
		report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14, BaseRef: "main", BaseExplicit: true, DeleteBranches: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Results[0].Status != ApplyRemoved || !result.Results[0].DeletedBranch || result.Results[0].BranchRetained || len(backend.deletes) != 1 {
			t.Fatalf("result = %+v backend=%+v", result, backend)
		}
	})

	t.Run("tip changed after removal", func(t *testing.T) {
		service, backend, _ := fixtureService(t)
		backend.branches = []branchState{
			{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
			{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
			{BaseHead: "base", BranchTip: "changed", BaseOnly: 0, BranchOnly: 1},
		}
		report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14, BaseRef: "main", BaseExplicit: true, DeleteBranches: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Results[0].Status != ApplyPartial || !result.Results[0].RemovedWorktree || !result.Results[0].BranchRetained || len(backend.deletes) != 0 {
			t.Fatalf("result = %+v backend=%+v", result, backend)
		}
	})
}

func TestApplyRemovalFailureNeverAttemptsBranchDeletion(t *testing.T) {
	service, backend, _ := fixtureService(t)
	backend.removeErr = errors.New("plain remove refused")
	backend.branches = []branchState{
		{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
		{BaseHead: "base", BranchTip: "tip", BaseOnly: 1, BranchOnly: 0},
	}
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14, BaseRef: "main", BaseExplicit: true, DeleteBranches: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Results[0].Status != ApplyFailed || result.Results[0].RemovedWorktree || len(backend.deletes) != 0 {
		t.Fatalf("result = %+v backend=%+v", result, backend)
	}
}

func TestHistoricalKilledWorkflowAmbiguityRemainsBlockedOrUnknown(t *testing.T) {
	tests := map[string]func(*Claim){
		"source 1 resumed without result": func(claim *Claim) {
			claim.WorkflowState = "killed"
			claim.JournalResult = KnownFact(false)
			claim.NotResumed = KnownFact(false)
		},
		"source 2 progress child": func(claim *Claim) {
			claim.WorkflowState = "killed"
			claim.AgentState = "progress"
			claim.AgentDone = KnownFact(false)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			service, backend, source := fixtureService(t)
			mutate(&source.claims[0])
			source.claims[0].LastActivity = serviceNow.Add(-24 * time.Hour)
			facts := backend.git[backend.targets[0].Path]
			facts.Clean = KnownFact(false)
			facts.Unstaged = 1
			backend.git[backend.targets[0].Path] = facts
			report, err := service.Report(t.Context(), ReportRequest{RepoPath: "/repo", StaleDays: 14})
			if err != nil {
				t.Fatal(err)
			}
			candidate := report.Candidates[0]
			if candidate.Classification != Blocked || statusOf(AuditResult{Checks: candidate.Checks}, CheckClean) != Blocked ||
				statusOf(AuditResult{Checks: candidate.Checks}, CheckProviderInactive) != Blocked {
				t.Fatalf("historical candidate = %+v", candidate)
			}
		})
	}
}

func TestSystemServiceRefusesProviderIdentityWithoutLiveRegistrationGeneration(t *testing.T) {
	r := gittest.New(t)
	r.Write(".gitignore", ".claude/worktrees/\n")
	r.Git("add", ".gitignore")
	r.Git("commit", "-m", "test: ignore harness worktrees")
	worktree := filepath.Join(r.Root, ".claude", "worktrees", "wf_run-agent")
	if err := gitx.AddWorktree(t.Context(), r.Root, worktree, "worktree-wf_run-agent", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "unique.txt"), []byte("retained by branch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.GitIn(worktree, "add", "unique.txt")
	r.GitIn(worktree, "commit", "-m", "test: unique retained commit")
	metadataSentinel := filepath.Join(t.TempDir(), "provider-metadata.json")
	if err := os.WriteFile(metadataSentinel, []byte("private metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeRepo, err := gitx.Discover(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	head := r.GitIn(worktree, "rev-parse", "HEAD")

	source := &fakeSource{complete: true, claims: []Claim{{
		Provider: providerNameForTest, SessionID: "session", RunID: "wf_run", AgentID: "agent", WorktreePath: worktree,
		Owned: KnownFact(true), Unique: KnownFact(true), Mapping: KnownFact(true), GitIdentity: KnownFact(true),
		ObservedBranch: "worktree-wf_run-agent", ObservedHead: head,
		ObservedCommonDir: worktreeRepo.GitCommonDir, ObservedGeneration: "provider-generation",
		WorkflowState: "completed", WorkflowTerminal: KnownFact(true), AgentState: "done", AgentDone: KnownFact(true),
		JournalStarted: KnownFact(true), JournalResult: KnownFact(true), NotResumed: KnownFact(true),
		LastActivity: serviceNow.Add(-30 * 24 * time.Hour), LastActivityKnown: true,
	}}}
	service := NewService(source, ServiceOptions{
		Tasks: task.NewStore(t.TempDir()), Artifacts: artifact.NewStore(t.TempDir()), Runtimes: []runtime.Runtime{runtime.None{}},
	})
	service.now = func() time.Time { return serviceNow }
	report, err := service.Report(t.Context(), ReportRequest{RepoPath: r.Root, StaleDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].Classification != Unknown ||
		statusOf(AuditResult{Checks: report.Candidates[0].Checks}, CheckProviderIdentity) != Unknown {
		t.Fatalf("report = %+v", report)
	}
	result, err := service.Apply(t.Context(), ApplyRequest{Report: report, Fingerprints: []string{report.Candidates[0].Fingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != ApplySkippedChanged || result.Results[0].RemovedWorktree {
		t.Fatalf("apply = %+v", result)
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("report-only worktree disappeared: %v", err)
	}
	if !gitx.BranchExists(t.Context(), r.Root, "worktree-wf_run-agent") {
		t.Fatal("branch with unique commit was not retained")
	}
	if content, err := os.ReadFile(metadataSentinel); err != nil || string(content) != "private metadata" {
		t.Fatalf("provider metadata changed: %q, %v", content, err)
	}
}

func fixtureService(t *testing.T) (*Service, *fakeBackend, *fakeSource) {
	t.Helper()
	path := "/repo/.claude/worktrees/z"
	target := Target{
		Path: path, Branch: "worktree-z", RegistryHead: "head", Registered: true, Hint: true,
	}
	backend := &fakeBackend{
		repository: repositoryState{Identity: RepositoryIdentity{Root: "/repo", CommonDir: "/repo/.git", Name: "repo"}},
		targets:    []Target{target},
		git:        map[string]GitFacts{path: eligibleGit(target)},
		tasks:      map[string]taskEvidence{path: knownNoTask()},
		artifacts:  map[string]artifactEvidence{path: knownSafeArtifacts()},
		callers:    map[string]callerEvidence{path: knownCallerOutside()},
		runtimes:   map[string]runtimeEvidence{path: knownNoRuntime()},
	}
	source := &fakeSource{complete: true, claims: []Claim{{
		Provider: providerNameForTest, SessionID: "session", RunID: "wf_run", AgentID: "agent-z", WorktreePath: path,
		Owned: KnownFact(true), Unique: KnownFact(true), Mapping: KnownFact(true), GitIdentity: KnownFact(true),
		ObservedBranch: target.Branch, ObservedHead: target.RegistryHead, ObservedCommonDir: "/repo/.git", ObservedGeneration: fakeRegistration(target),
		WorkflowState: "completed", WorkflowTerminal: KnownFact(true), AgentState: "done", AgentDone: KnownFact(true),
		JournalStarted: KnownFact(true), JournalResult: KnownFact(true), NotResumed: KnownFact(true),
		LastActivity: serviceNow.Add(-30 * 24 * time.Hour), LastActivityKnown: true,
	}}}
	return &Service{source: source, backend: backend, now: func() time.Time { return serviceNow }}, backend, source
}

func eligibleGit(target Target) GitFacts {
	return GitFacts{
		Registered: KnownFact(true), PathPresent: KnownFact(true), LinkedWorktree: KnownFact(true), NonMain: KnownFact(true),
		BranchNamed: KnownFact(true), BranchMatches: KnownFact(true), CommonDirMatches: KnownFact(true), HeadMatches: KnownFact(true),
		Unlocked: KnownFact(true), NotPrunable: KnownFact(true), Clean: KnownFact(true), IgnoredEmpty: KnownFact(true),
		SubmodulesClean: KnownFact(true), NoGitOperation: KnownFact(true), LiveHead: target.RegistryHead,
		CommonDir: "/repo/.git", RegistrationGeneration: fakeRegistration(target), StateFingerprint: "git-clean",
	}
}

func fakeRegistration(target Target) string { return "generation:" + target.Branch }

func knownNoTask() taskEvidence {
	return taskEvidence{Unclaimed: KnownFact(true), Fingerprint: "no-task"}
}
func knownSafeArtifacts() artifactEvidence {
	return artifactEvidence{Known: KnownFact(true), Safe: KnownFact(true), Fingerprint: "no-artifacts"}
}
func knownCallerOutside() callerEvidence {
	return callerEvidence{Outside: KnownFact(true), Fingerprint: "caller-outside"}
}
func knownNoRuntime() runtimeEvidence {
	return runtimeEvidence{Known: KnownFact(true), Clear: KnownFact(true), Fingerprint: "runtime-clear"}
}
