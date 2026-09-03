package taskflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type taskInventoryLoader func() ([]task.Record, []task.Diagnostic, error)

type destructiveInspectInput struct {
	locator Locator
	base    string
	runtime runtime.Runtime
	rtErr   error
	cleanup retire.Options

	noCheckout       bool
	skipCleanup      bool
	inspectArtifacts bool
	inspectContent   bool
	inspectTasks     bool
	loadTasks        taskInventoryLoader
}

type destructiveTaskClaim struct {
	ID       string
	Revision string
	Reason   string
}

type destructiveObservation struct {
	locator Locator

	repo         gitx.Repo
	repoPath     string
	gitCommonDir string
	checkout     string
	repoErr      error

	worktree      gitx.RegisteredWorktree
	worktreeFound bool
	worktreeErr   error
	worktrees     []gitx.Worktree
	worktreesErr  error

	status             gitx.Status
	statusErr          error
	head               string
	headErr            error
	operation          string
	inProgress         bool
	operationErr       error
	contentFingerprint string
	contentErr         error

	branchRef    string
	branchExists bool
	branchOID    string
	branchOIDErr error
	baseRef      string
	baseExists   bool
	baseOID      string
	baseOIDErr   error
	contained    bool
	containedErr error

	artifact    artifact.ReadinessInspection
	artifactErr error

	runtime          runtime.Runtime
	runtimeErr       error
	runtimeAvailable bool
	cleanup          retire.Inspection
	cleanupErr       error

	harness   inventory.ClaudeHarnessEvidence
	isHarness bool

	taskRecords      []task.Record
	taskDiagnostics  []task.Diagnostic
	taskInventoryErr error
	taskClaims       []destructiveTaskClaim
	taskIncomplete   []string
}

func (s *lifecycleService) inspectDestructive(ctx context.Context, input destructiveInspectInput) (destructiveObservation, error) {
	observed := destructiveObservation{locator: input.locator, runtime: input.runtime, runtimeErr: input.rtErr}
	if err := contextError(ctx); err != nil {
		return observed, err
	}

	repository, err := s.gitDiscover(ctx, input.locator.RepoPath)
	if err != nil {
		observed.repoErr = err
		return observed, nil
	}
	observed.repo = repository
	observed.repoPath, err = s.canonicalPath(repository.MainRoot)
	if err != nil {
		observed.repoErr = fmt.Errorf("canonicalize Git main checkout: %w", err)
		return observed, nil
	}
	observed.gitCommonDir, err = s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		observed.repoErr = fmt.Errorf("canonicalize Git common directory: %w", err)
		return observed, nil
	}
	requestedRepo, err := s.canonicalPath(input.locator.RepoPath)
	if err != nil {
		return observed, fmt.Errorf("%w: canonicalize requested repository: %v", ErrInvalidRequest, err)
	}
	requestedCommon, err := s.canonicalPath(input.locator.GitCommonDir)
	if err != nil {
		return observed, fmt.Errorf("%w: canonicalize requested Git common directory: %v", ErrInvalidRequest, err)
	}
	if requestedRepo != observed.repoPath || requestedCommon != observed.gitCommonDir {
		return observed, &StalePlanError{Reason: fmt.Sprintf(
			"selected repository identity changed: main %q -> %q, common directory %q -> %q",
			requestedRepo, observed.repoPath, requestedCommon, observed.gitCommonDir,
		)}
	}
	if filepath.IsAbs(input.locator.RepoKey) {
		key, keyErr := s.canonicalPath(input.locator.RepoKey)
		if keyErr != nil {
			return observed, fmt.Errorf("%w: canonicalize repository key: %v", ErrInvalidRequest, keyErr)
		}
		if key != observed.gitCommonDir {
			return observed, &StalePlanError{Reason: "selected repository key no longer names the Git common directory"}
		}
	}

	observed.worktrees, observed.worktreesErr = s.gitWorktrees(ctx, observed.repoPath)
	if !input.noCheckout {
		observed.checkout, err = s.canonicalPath(input.locator.CheckoutPath)
		if err != nil {
			observed.worktreeErr = fmt.Errorf("canonicalize selected checkout: %w", err)
		} else {
			observed.worktree, observed.worktreeErr = s.resolveWorktree(ctx, observed.repoPath, observed.checkout)
			if observed.worktreeErr == nil {
				observed.worktreeFound = true
				observed.observeCheckoutFacts(ctx, s)
				if input.inspectContent && observed.statusErr == nil && observed.status.Dirty() {
					analysis, contentErr := s.analyzeFinish(ctx, observed.checkout, input.locator.Branch, input.locator.Branch)
					observed.contentFingerprint = analysis.Fingerprint
					observed.contentErr = contentErr
				}
				observed.harness, observed.isHarness = inventory.DetectClaudeHarnessWorktree(observed.repoPath, observed.checkout)
			}
		}
	}

	observed.observeRefs(ctx, s, input.locator.Branch, input.base)
	if input.inspectArtifacts && observed.worktreeFound {
		observed.artifact, observed.artifactErr = s.inspectArtifacts(ctx, s.artifacts, observed.checkout)
	}
	if !input.noCheckout && !input.skipCleanup {
		if observed.runtimeErr == nil {
			if observed.runtime == nil || strings.TrimSpace(observed.runtime.Name()) == "" {
				observed.runtimeErr = errors.New("destructive inspection has no runtime backend")
			} else {
				observed.runtimeAvailable = observed.runtime.Available()
				observed.cleanup, observed.cleanupErr = s.inspectCleanup(ctx, observed.runtime, observed.checkout, input.cleanup)
			}
		}
	}
	if input.inspectTasks {
		if input.loadTasks == nil {
			observed.taskInventoryErr = errors.New("task inventory loader is unavailable")
		} else {
			observed.taskRecords, observed.taskDiagnostics, observed.taskInventoryErr = input.loadTasks()
			if observed.taskInventoryErr == nil {
				s.inspectDestructiveTaskClaims(ctx, &observed)
			}
		}
	}
	return observed, nil
}

func (o *destructiveObservation) observeCheckoutFacts(ctx context.Context, s *lifecycleService) {
	o.status, o.statusErr = s.gitStatus(ctx, o.checkout)
	o.head, o.headErr = s.gitRun(ctx, o.checkout, "rev-parse", "--verify", "HEAD^{commit}")
	o.head = strings.TrimSpace(o.head)
	o.operation, o.inProgress, o.operationErr = s.gitInProgress(ctx, o.checkout)
}

func (o *destructiveObservation) observeRefs(ctx context.Context, s *lifecycleService, branch, base string) {
	o.branchRef = localBranchRef(branch)
	if o.repoPath != "" && o.branchRef != "" {
		o.branchExists, o.branchOIDErr = s.gitRefState(ctx, o.repoPath, o.branchRef)
		if o.branchExists && o.branchOIDErr == nil {
			o.branchOID, o.branchOIDErr = s.resolveRefOID(ctx, o.repoPath, o.branchRef)
		}
	}
	o.baseRef = localBranchRef(base)
	if o.repoPath != "" && o.baseRef != "" {
		o.baseExists, o.baseOIDErr = s.gitRefState(ctx, o.repoPath, o.baseRef)
		if o.baseExists && o.baseOIDErr == nil {
			o.baseOID, o.baseOIDErr = s.resolveRefOID(ctx, o.repoPath, o.baseRef)
		}
	}
	if o.branchExists && o.branchOIDErr == nil && o.branchOID != "" &&
		o.baseExists && o.baseOIDErr == nil && o.baseOID != "" {
		o.contained, o.containedErr = s.isAncestor(ctx, o.repoPath, o.branchOID, o.baseOID)
	}
}

func localBranchRef(branch string) string {
	if branch == "" {
		return ""
	}
	return "refs/heads/" + branch
}

func (s *lifecycleService) inspectDestructiveTaskClaims(ctx context.Context, observed *destructiveObservation) {
	for _, diagnostic := range observed.taskDiagnostics {
		observed.taskIncomplete = append(observed.taskIncomplete, diagnostic.Error())
	}
	for _, record := range observed.taskRecords {
		candidate := record.Task
		if err := candidate.Validate(); err != nil {
			observed.taskIncomplete = append(observed.taskIncomplete,
				fmt.Sprintf("task %s is invalid: %v", candidate.ID, err))
			continue
		}

		pathClaim := false
		if candidate.WorktreePath != "" {
			path, err := s.canonicalPath(candidate.WorktreePath)
			if err != nil {
				observed.taskIncomplete = append(observed.taskIncomplete,
					fmt.Sprintf("task %s checkout identity is unavailable: %v", candidate.ID, err))
			} else {
				pathClaim = path == observed.checkout
			}
		}

		branchClaim := false
		if candidate.Branch == observed.locator.Branch {
			repoMatches, complete, detail := s.taskRepositoryMatches(ctx, candidate, *observed)
			if !complete {
				observed.taskIncomplete = append(observed.taskIncomplete,
					fmt.Sprintf("task %s repository identity is unavailable: %s", candidate.ID, detail))
			} else {
				branchClaim = repoMatches
			}
		}

		if pathClaim {
			observed.taskClaims = append(observed.taskClaims, destructiveTaskClaim{
				ID: candidate.ID, Revision: record.Revision, Reason: "exact checkout path",
			})
		}
		if branchClaim {
			reason := "exact branch in selected repository"
			if pathClaim {
				reason = "exact checkout path and branch in selected repository"
				observed.taskClaims[len(observed.taskClaims)-1].Reason = reason
				continue
			}
			observed.taskClaims = append(observed.taskClaims, destructiveTaskClaim{
				ID: candidate.ID, Revision: record.Revision, Reason: reason,
			})
		}
	}
	sort.Slice(observed.taskClaims, func(i, j int) bool {
		if observed.taskClaims[i].ID != observed.taskClaims[j].ID {
			return observed.taskClaims[i].ID < observed.taskClaims[j].ID
		}
		return observed.taskClaims[i].Reason < observed.taskClaims[j].Reason
	})
	sort.Strings(observed.taskIncomplete)
}

func (s *lifecycleService) taskRepositoryMatches(ctx context.Context, candidate task.Task, observed destructiveObservation) (bool, bool, string) {
	candidatePath, err := s.canonicalPath(candidate.RepoPath)
	if err != nil {
		return false, false, err.Error()
	}
	if candidatePath == observed.repoPath {
		return true, true, ""
	}
	repository, err := s.gitDiscover(ctx, candidate.RepoPath)
	if err != nil {
		return false, false, err.Error()
	}
	commonDir, err := s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		return false, false, err.Error()
	}
	return commonDir == observed.gitCommonDir, true, ""
}

func (o destructiveObservation) branchCheckoutPaths(excludeTarget bool) ([]string, error) {
	if o.worktreesErr != nil {
		return nil, o.worktreesErr
	}
	var paths []string
	for _, worktree := range o.worktrees {
		if worktree.Bare || worktree.Branch != o.locator.Branch {
			continue
		}
		path := worktree.Path
		if canonical, err := filepath.Abs(path); err == nil {
			path = filepath.Clean(canonical)
		}
		if excludeTarget && o.checkout != "" {
			candidate, err := filepath.Abs(o.checkout)
			if err == nil && filepath.Clean(candidate) == path {
				continue
			}
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (o destructiveObservation) authority() map[string]string {
	claims := make([]string, 0, len(o.taskClaims))
	for _, claim := range o.taskClaims {
		claims = append(claims, strings.Join([]string{claim.ID, claim.Revision, claim.Reason}, "\x00"))
	}
	authority := map[string]string{
		"destructive.repo":                    o.repositoryAuthority(),
		"destructive.checkout":                o.checkoutAuthority(),
		"destructive.git":                     o.gitAuthority(),
		"destructive.refs":                    o.refsAuthority(),
		"destructive.artifact":                artifactAuthority(o.artifact, o.artifactErr),
		"destructive.runtime":                 cleanupAuthority(o.cleanup, o.cleanupErr),
		"destructive.runtime-backend":         runtimeName(o.runtime),
		"destructive.runtime-error":           errorString(o.runtimeErr),
		"destructive.runtime-available":       boolString(o.runtimeAvailable),
		"destructive.harness":                 boolString(o.isHarness),
		"destructive.harness-root":            o.harness.Root,
		"destructive.task-inventory":          o.taskInventoryAuthority(),
		"destructive.task-inventory-error":    errorString(o.taskInventoryErr),
		"destructive.task-claims":             strings.Join(claims, "\x01"),
		"destructive.task-incomplete":         strings.Join(o.taskIncomplete, "\x01"),
		"destructive.worktree-list-error":     errorString(o.worktreesErr),
		"destructive.worktree-list-authority": o.worktreeListAuthority(),
	}
	return authority
}

func (o destructiveObservation) repositoryAuthority() string {
	return authorityHash("taskflow-destructive-repository-v1",
		o.repoPath, o.gitCommonDir, o.repo.Root, o.repo.MainRoot, o.repo.GitDir,
		boolString(o.repo.Bare), errorString(o.repoErr),
	)
}

func (o destructiveObservation) checkoutAuthority() string {
	return authorityHash("taskflow-destructive-checkout-v1",
		o.checkout, worktreeAuthority(o.worktree, o.worktreeFound, o.worktreeErr, 0),
	)
}

func (o destructiveObservation) gitAuthority() string {
	return authorityHash("taskflow-destructive-git-v1",
		stringifyStatus(o.status), errorString(o.statusErr),
		o.head, errorString(o.headErr), o.operation, boolString(o.inProgress), errorString(o.operationErr),
		o.contentFingerprint, errorString(o.contentErr),
	)
}

func (o destructiveObservation) refsAuthority() string {
	return authorityHash("taskflow-destructive-refs-v1",
		o.branchRef, boolString(o.branchExists), o.branchOID, errorString(o.branchOIDErr),
		o.baseRef, boolString(o.baseExists), o.baseOID, errorString(o.baseOIDErr),
		boolString(o.contained), errorString(o.containedErr),
	)
}

func (o destructiveObservation) taskInventoryAuthority() string {
	values := []string{errorString(o.taskInventoryErr)}
	values = append(values, o.taskIncomplete...)
	for _, claim := range o.taskClaims {
		values = append(values, claim.ID, claim.Revision, claim.Reason)
	}
	return authorityHash("taskflow-destructive-task-inventory-v1", values...)
}

func (o destructiveObservation) worktreeListAuthority() string {
	values := []string{errorString(o.worktreesErr), strconv.Itoa(len(o.worktrees))}
	for _, worktree := range o.worktrees {
		values = append(values,
			worktree.Path, worktree.Head, worktree.Branch,
			boolString(worktree.Detached), boolString(worktree.Bare), boolString(worktree.Main),
			boolString(worktree.Locked), worktree.LockedReason,
			boolString(worktree.Prunable), worktree.PrunableReason,
		)
	}
	return authorityHash("taskflow-destructive-worktrees-v1", values...)
}

func artifactEvidenceFromInspection(inspection artifact.ReadinessInspection) string {
	if inspection.KnownEmpty {
		return "no artifact intents match the exact checkout"
	}
	states := make([]string, 0, len(inspection.Intents))
	for _, intent := range inspection.Intents {
		states = append(states, intent.Intent.ID+"="+string(intent.State))
	}
	if len(states) == 0 {
		return "artifact readiness is incomplete"
	}
	return strings.Join(states, ", ")
}

func mergeAuthority(base, additional map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(additional))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range additional {
		out[key] = value
	}
	return out
}

func destructiveConditionsReady(conditions []Condition) error {
	if availability := AvailabilityFor(conditions); availability != AvailabilityReady {
		var evidence []string
		for _, current := range conditions {
			if current.Requirement == RequirementRequired && current.Verdict != VerdictMet {
				evidence = append(evidence, string(current.Code)+": "+current.Evidence)
			}
		}
		return staleBoundary("destructive safety is no longer ready (" + strings.Join(evidence, "; ") + ")")
	}
	return nil
}

func compareAuthorityCategory(name, before, after string) error {
	if before != after {
		return staleBoundary(name + " authority changed")
	}
	return nil
}
