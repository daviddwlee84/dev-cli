package taskflow

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

var exactTaskID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (s *lifecycleService) planSpec(ctx context.Context, request Request) (PlanSpec, error) {
	if err := contextError(ctx); err != nil {
		return PlanSpec{}, err
	}
	if err := validateLifecycleLocator(request.Locator); err != nil {
		return PlanSpec{}, err
	}
	record, err := s.tasks.GetRecord(request.Locator.TaskID)
	if err != nil {
		return PlanSpec{}, fmt.Errorf("%w: load exact task %q: %v", ErrInvalidRequest, request.Locator.TaskID, err)
	}
	spec, _, err := s.observeAndSpecify(ctx, request, *record)
	if err != nil {
		return PlanSpec{}, err
	}
	current, err := s.tasks.GetRecord(request.Locator.TaskID)
	if err != nil {
		return PlanSpec{}, &StalePlanError{
			ExpectedAuthorityFingerprint: record.Revision,
			Reason:                       "task record disappeared while planning",
		}
	}
	if current.Revision != record.Revision {
		return PlanSpec{}, staleTaskRevision(record.Revision, current.Revision, "task record changed while planning")
	}
	return spec, nil
}

func (s *lifecycleService) observeAndSpecify(ctx context.Context, request Request, record task.Record) (PlanSpec, lifecycleObservation, error) {
	if err := validateRecordIdentity(request.Locator, record); err != nil {
		return PlanSpec{}, lifecycleObservation{}, err
	}
	observed, err := s.observeLifecycle(ctx, request, record)
	if err != nil {
		return PlanSpec{}, observed, err
	}
	var spec PlanSpec
	switch request.Action {
	case ParkWarm:
		spec = s.parkWarmSpec(request, observed)
	case ParkCold:
		spec = s.parkColdSpec(request, observed)
	case Resume:
		spec = s.resumeSpec(request, observed)
	case CompleteDirect:
		spec = s.completeDirectSpec(request, observed)
	case CompleteFF:
		spec = s.completeFFSpec(request, observed)
	case ReviewHandoff:
		spec = s.reviewHandoffSpec(request, observed)
	case VerifyMerged:
		spec = s.verifyMergedSpec(request, observed)
	default:
		return PlanSpec{}, observed, &HandlerUnavailableError{Action: request.Action, Stage: "plan"}
	}
	return spec, observed, nil
}

func (s *lifecycleService) freshPlan(ctx context.Context, request Request, record task.Record) (Plan, lifecycleObservation, error) {
	spec, observed, err := s.observeAndSpecify(ctx, request, record)
	if err != nil {
		return Plan{}, observed, err
	}
	plan, err := BuildPlan(request, spec)
	return plan, observed, err
}

func validateLifecycleLocator(locator Locator) error {
	switch {
	case locator.TaskID == "":
		return fmt.Errorf("%w: exact TaskID is required", ErrInvalidRequest)
	case !exactTaskID.MatchString(locator.TaskID) || filepath.Base(locator.TaskID) != locator.TaskID || locator.TaskID == "." || locator.TaskID == "..":
		return fmt.Errorf("%w: invalid exact TaskID %q", ErrInvalidRequest, locator.TaskID)
	case locator.TaskRevision == "":
		return fmt.Errorf("%w: exact task revision is required", ErrInvalidRequest)
	case locator.RepoPath == "":
		return fmt.Errorf("%w: exact repository path is required", ErrInvalidRequest)
	case locator.GitCommonDir == "":
		return fmt.Errorf("%w: exact Git common directory is required", ErrInvalidRequest)
	case locator.Branch == "":
		return fmt.Errorf("%w: exact task branch is required", ErrInvalidRequest)
	case !validMode(locator.Mode):
		return fmt.Errorf("%w: exact checkout mode is required", ErrInvalidRequest)
	case !validState(locator.State):
		return fmt.Errorf("%w: exact task state is required", ErrInvalidRequest)
	}
	return nil
}

func validateRecordIdentity(locator Locator, record task.Record) error {
	candidate := record.Task
	mode := candidate.EffectiveMode()
	if record.Revision != locator.TaskRevision {
		return staleTaskRevision(locator.TaskRevision, record.Revision, "task revision no longer matches the selected row")
	}
	checks := []struct {
		name     string
		expected string
		actual   string
	}{
		{"TaskID", locator.TaskID, candidate.ID},
		{"mode", string(locator.Mode), string(mode)},
		{"state", string(locator.State), string(candidate.State)},
		{"branch", locator.Branch, candidate.Branch},
		{"base", locator.Base, candidate.Base},
	}
	for _, check := range checks {
		if check.expected != check.actual {
			return &StalePlanError{Reason: fmt.Sprintf("task %s changed from %q to %q", check.name, check.expected, check.actual)}
		}
	}

	selectedRepo, err := pathx.Canonical(locator.RepoPath)
	if err != nil {
		return &StalePlanError{Reason: fmt.Sprintf("selected repository path %q is no longer canonicalizable: %v", locator.RepoPath, err)}
	}
	recordedRepo, err := pathx.Canonical(candidate.RepoPath)
	if err != nil {
		return &StalePlanError{Reason: fmt.Sprintf("recorded repository path %q is no longer canonicalizable: %v", candidate.RepoPath, err)}
	}
	if selectedRepo != recordedRepo {
		return &StalePlanError{Reason: fmt.Sprintf("task repository path changed from %q to %q", selectedRepo, recordedRepo)}
	}

	expectedCheckout := recordedRepo
	if mode == task.ModeWorktree {
		expectedCheckout = ""
		if candidate.WorktreePath != "" {
			expectedCheckout, err = pathx.Canonical(candidate.WorktreePath)
			if err != nil {
				return &StalePlanError{Reason: fmt.Sprintf("recorded checkout path %q is no longer canonicalizable: %v", candidate.WorktreePath, err)}
			}
		}
	}
	selectedCheckout := ""
	if locator.CheckoutPath != "" {
		selectedCheckout, err = pathx.Canonical(locator.CheckoutPath)
		if err != nil {
			return &StalePlanError{Reason: fmt.Sprintf("selected checkout path %q is no longer canonicalizable: %v", locator.CheckoutPath, err)}
		}
	}
	if selectedCheckout != expectedCheckout {
		return &StalePlanError{Reason: fmt.Sprintf("task checkout path changed from %q to %q", selectedCheckout, expectedCheckout)}
	}
	return nil
}

func staleTaskRevision(expected, actual, reason string) error {
	return &StalePlanError{
		ExpectedAuthorityFingerprint: expected,
		ActualAuthorityFingerprint:   actual,
		Reason:                       reason,
	}
}

func (s *lifecycleService) observeLifecycle(ctx context.Context, request Request, record task.Record) (lifecycleObservation, error) {
	observed := lifecycleObservation{record: record, task: record.Task, mode: record.Task.EffectiveMode()}
	observed.setCaller(s.host, s.cwd, s.callerWorkspace, s.callerPane)

	repository, err := s.gitDiscover(ctx, record.Task.RepoPath)
	if err != nil {
		return observed, fmt.Errorf("%w: discover exact repository for task %s: %v", ErrInvalidRequest, record.Task.ID, err)
	}
	observed.repo = repository
	observed.repoPath, err = s.canonicalPath(repository.MainRoot)
	if err != nil {
		return observed, fmt.Errorf("%w: canonicalize main checkout: %v", ErrInvalidRequest, err)
	}
	observed.gitCommonDir, err = s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		return observed, fmt.Errorf("%w: canonicalize Git common directory: %v", ErrInvalidRequest, err)
	}
	recordedRepo, err := s.canonicalPath(record.Task.RepoPath)
	if err != nil {
		return observed, fmt.Errorf("%w: canonicalize recorded repository: %v", ErrInvalidRequest, err)
	}
	if recordedRepo != observed.repoPath {
		return observed, &StalePlanError{Reason: fmt.Sprintf("recorded repository %q resolves to %q, but Git reports main checkout %q", record.Task.RepoPath, recordedRepo, observed.repoPath)}
	}
	requestedCommon, err := s.canonicalPath(request.Locator.GitCommonDir)
	if err != nil {
		return observed, fmt.Errorf("%w: canonicalize requested Git common directory: %v", ErrInvalidRequest, err)
	}
	if requestedCommon != observed.gitCommonDir {
		return observed, &StalePlanError{Reason: fmt.Sprintf("Git common directory changed from %q to %q", requestedCommon, observed.gitCommonDir)}
	}
	if filepath.IsAbs(request.Locator.RepoKey) {
		requestedKey, keyErr := s.canonicalPath(request.Locator.RepoKey)
		if keyErr != nil {
			return observed, fmt.Errorf("%w: canonicalize requested repository key: %v", ErrInvalidRequest, keyErr)
		}
		if requestedKey != observed.gitCommonDir {
			return observed, &StalePlanError{Reason: fmt.Sprintf("repository key changed from %q to %q", requestedKey, observed.gitCommonDir)}
		}
	}

	s.observeCheckout(ctx, request, &observed)
	if observed.hasCheckout() {
		observed.status, observed.statusErr = s.gitStatus(ctx, observed.checkout)
		if observed.statusErr == nil {
			observed.head, observed.headErr = s.gitRun(ctx, observed.checkout, "rev-parse", "HEAD")
			observed.head = strings.TrimSpace(observed.head)
			observed.baseOID, observed.baseOIDErr = s.resolveRefOID(ctx, observed.repoPath, observed.task.Base)
			observed.upstreamOID, observed.upstreamOIDErr = s.resolveRefOID(ctx, observed.repoPath, observed.status.Upstream)
			observed.operation, observed.inProgress, observed.operationErr = s.gitInProgress(ctx, observed.checkout)
		}
	}
	s.observeActionRefs(ctx, request, &observed)
	if err := validateLiveLocator(request.Locator, observed); err != nil {
		return observed, err
	}

	if isCompletionAction(request.Action) {
		s.observeCompletionFacts(ctx, request, &observed)
	}
	observed.runtime, observed.runtimeErr = s.runtimeFor(observed.task)
	if observed.runtimeErr == nil {
		observed.runtimeAvailable = observed.runtime.Available()
		s.observeActionRuntime(ctx, request, &observed)
	}
	return observed, nil
}

func (s *lifecycleService) observeCheckout(ctx context.Context, request Request, observed *lifecycleObservation) {
	switch observed.mode {
	case task.ModeBranch, task.ModeDirect:
		registered, err := s.resolveWorktree(ctx, observed.repoPath, observed.repoPath)
		observed.worktree, observed.worktreeErr = registered, err
		if err == nil {
			observed.worktreeFound = true
			observed.checkout = registered.Path
		}
	case task.ModeWorktree:
		if observed.task.WorktreePath != "" {
			registered, err := s.resolveWorktree(ctx, observed.repoPath, observed.task.WorktreePath)
			observed.worktree, observed.worktreeErr = registered, err
			if err == nil {
				observed.worktreeFound = true
				observed.checkout = registered.Path
			}
		}
		if observed.worktreeFound || request.Action != Resume ||
			(observed.task.State != task.Warm && observed.task.State != task.Cold) {
			return
		}
		worktrees, err := s.gitWorktrees(ctx, observed.repoPath)
		if err != nil {
			observed.worktreeErr = fmt.Errorf("list worktrees for parked-task resume: %w", err)
			return
		}
		var candidate string
		for _, worktree := range worktrees {
			if worktree.Branch == observed.task.Branch {
				observed.branchMatches++
				candidate = worktree.Path
			}
		}
		switch observed.branchMatches {
		case 0:
			if observed.worktreeErr == nil {
				observed.worktreeErr = fmt.Errorf("%w: branch %s has no checkout", gitx.ErrWorktreeNotFound, observed.task.Branch)
			}
		case 1:
			registered, resolveErr := s.resolveWorktree(ctx, observed.repoPath, candidate)
			observed.worktree, observed.worktreeErr = registered, resolveErr
			if resolveErr == nil {
				observed.worktreeFound = true
				observed.checkout = registered.Path
			}
		default:
			observed.worktreeErr = fmt.Errorf("%w: branch %s has %d registered checkouts", gitx.ErrWorktreeAmbiguous, observed.task.Branch, observed.branchMatches)
		}
	}
}

func validateLiveLocator(locator Locator, observed lifecycleObservation) error {
	if observed.statusErr == nil && observed.hasCheckout() {
		checks := []struct {
			name     string
			expected string
			actual   string
			required bool
		}{
			{"upstream", locator.Upstream, observed.status.Upstream, true},
			{"HEAD", locator.HeadOID, observed.head, locator.HeadOID != ""},
			{"base OID", locator.BaseOID, observed.baseOID, locator.BaseOID != ""},
			{"upstream OID", locator.UpstreamOID, observed.upstreamOID, locator.UpstreamOID != ""},
		}
		for _, check := range checks {
			if check.required && check.expected != check.actual {
				return &StalePlanError{Reason: fmt.Sprintf("selected %s changed from %q to %q", check.name, check.expected, check.actual)}
			}
		}
		return nil
	}
	if !observed.hasCheckout() && locator.HeadOID != "" {
		if observed.localBranchOIDErr != nil || locator.HeadOID != observed.localBranchOID {
			return &StalePlanError{Reason: fmt.Sprintf("selected local branch OID changed from %q to %q", locator.HeadOID, observed.localBranchOID)}
		}
	}
	if !observed.hasCheckout() && locator.UpstreamOID != "" && locator.Upstream == observed.remoteBranch {
		if observed.remoteBranchOIDErr != nil || locator.UpstreamOID != observed.remoteBranchOID {
			return &StalePlanError{Reason: fmt.Sprintf("selected remote branch OID changed from %q to %q", locator.UpstreamOID, observed.remoteBranchOID)}
		}
	}
	return nil
}

func (s *lifecycleService) observeActionRuntime(ctx context.Context, request Request, observed *lifecycleObservation) {
	switch options := request.Options.(type) {
	case ParkWarmOptions:
		if options.KeepSession || !observed.hasCheckout() {
			return
		}
		cleanupOptions := observed.cleanupOptions(options.CloseUnknown, options.AssumeNoRuntime, options.Timeout)
		observed.cleanup, observed.cleanupErr = s.inspectCleanup(ctx, observed.runtime, observed.checkout, cleanupOptions)
	case ParkColdOptions:
		if !observed.hasCheckout() {
			return
		}
		cleanupOptions := observed.cleanupOptions(options.CloseUnknown, options.AssumeNoRuntime, options.Timeout)
		observed.cleanup, observed.cleanupErr = s.inspectCleanup(ctx, observed.runtime, observed.checkout, cleanupOptions)
		if observed.mode == task.ModeWorktree && observed.isLinkedWorktree() {
			observed.artifact, observed.artifactErr = s.inspectArtifacts(ctx, s.artifacts, observed.checkout)
		}
	case ResumeOptions:
		if !observed.hasCheckout() {
			return
		}
		observed.occupancy, observed.occupancyErr = s.inspectOccupancy(ctx, observed.runtime, observed.checkout, runtime.OccupancyOptions{
			Profile:           runtime.OccupancyStrict,
			CallerWorkspaceID: s.callerWorkspace,
			CallerPaneID:      s.callerPane,
		})
		if s.writerOccupancyError(observed.occupancy, observed.occupancyErr) == nil {
			observed.savedRuntimeLive = savedHandleLive(observed.occupancy, observed.task.RuntimeHandle)
		}
	case CompleteDirectOptions, CompleteFFOptions, ReviewHandoffOptions, VerifyMergedOptions:
		if !observed.hasCheckout() {
			return
		}
		occupancyOptions := runtime.OccupancyOptions{
			Profile:           runtime.OccupancyStrict,
			CallerWorkspaceID: s.callerWorkspace,
			CallerPaneID:      s.callerPane,
		}
		observed.occupancy, observed.occupancyErr = s.inspectOccupancy(ctx, observed.runtime, observed.checkout, occupancyOptions)
		if request.Action == CompleteFF && observed.mode == task.ModeBranch {
			observed.integration.occupancy = observed.occupancy
			observed.integration.occupancyErr = observed.occupancyErr
		}
		if request.Action == CompleteFF && observed.mode == task.ModeWorktree && observed.integration.worktreeFound {
			observed.integration.occupancy, observed.integration.occupancyErr = s.inspectOccupancy(
				ctx, observed.runtime, observed.repoPath, occupancyOptions,
			)
		}
	}
}

func (s *lifecycleService) observeActionRefs(ctx context.Context, request Request, observed *lifecycleObservation) {
	observed.localBranchExists, observed.localBranchOIDErr = s.gitRefState(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	if observed.localBranchExists && observed.localBranchOIDErr == nil {
		observed.localBranchOID, observed.localBranchOIDErr = s.resolveRefOID(ctx, observed.repoPath, localBranchRef(observed.task.Branch))
	}
	observed.remoteBranch = "origin/" + observed.task.Branch
	observed.remoteBranchExists, observed.remoteBranchOIDErr = s.gitRefState(ctx, observed.repoPath, observed.remoteBranch)
	if observed.remoteBranchExists && observed.remoteBranchOIDErr == nil {
		observed.remoteBranchOID, observed.remoteBranchOIDErr = s.resolveRefOID(ctx, observed.repoPath, observed.remoteBranch)
	}

	switch request.Action {
	case ParkCold:
		if observed.mode == task.ModeBranch {
			observed.baseRef = observed.task.Base
			if observed.baseRef == "" {
				observed.baseRef = s.gitDefaultBranch(ctx, observed.repoPath)
			}
			if observed.baseRef != "" {
				observed.baseRefExists, observed.baseRefExistsErr = s.gitRefState(ctx, observed.repoPath, observed.baseRef)
			}
		}
	case Resume:
		observed.baseRef = observed.task.Base
		if observed.mode == task.ModeWorktree && !observed.hasCheckout() {
			switch {
			case observed.localBranchExists:
				observed.baseRef = observed.task.Branch
				observed.baseRefExists = true
				observed.baseRefExistsErr = observed.localBranchOIDErr
			case observed.remoteBranchOIDErr != nil:
				observed.baseRef = observed.remoteBranch
				observed.baseRefExistsErr = observed.remoteBranchOIDErr
			default:
				observed.baseRef = observed.remoteBranch
				observed.baseRefExists = observed.remoteBranchExists
			}
		} else {
			if observed.remoteBranchExists {
				observed.baseRef = observed.remoteBranch
			}
			if observed.baseRef != "" {
				observed.baseRefExists, observed.baseRefExistsErr = s.gitRefState(ctx, observed.repoPath, observed.baseRef)
			}
		}
		if observed.mode == task.ModeWorktree && !observed.hasCheckout() &&
			(observed.task.State == task.Warm || observed.task.State == task.Cold) {
			if observed.task.State == task.Warm && observed.task.WorktreePath != "" {
				observed.desiredWorktree, observed.desiredWorktreeErr = s.canonicalPath(observed.task.WorktreePath)
			} else {
				observed.desiredWorktree, observed.desiredWorktreeErr = s.renderWorktreePath(observed.task)
			}
			if observed.desiredWorktreeErr == nil {
				observed.desiredWorktreeErr = wt.ValidateTarget(observed.desiredWorktree, observed.repoPath)
			}
		}
	case CompleteDirect:
		observed.completionBaseRef = observed.task.Branch
	case CompleteFF, ReviewHandoff:
		observed.completionBaseRef = observed.task.Base
	case VerifyMerged:
		options := request.Options.(VerifyMergedOptions)
		observed.completionBaseRef = options.BaseRef
		if observed.completionBaseRef == "" {
			observed.completionBaseRef = observed.task.Base
		}
	}
}

func (s *lifecycleService) renderWorktreePath(candidate task.Task) (string, error) {
	values := config.Vars{
		"worktree_root": config.Expand(s.cfg.Paths.WorktreeRoot),
		"repo":          candidate.Repo,
		"repo_path":     candidate.RepoPath,
		"branch":        candidate.Branch,
		"category":      "",
		"host":          s.host,
		"date":          s.now().Format("2006-01-02"),
	}
	rendered, err := config.Render(s.cfg.Paths.WorktreePath, values)
	if err != nil {
		return "", err
	}
	return s.canonicalPath(config.Expand(rendered))
}

func (s *lifecycleService) baseAuthority(request Request, observed lifecycleObservation) map[string]string {
	authority := map[string]string{
		"task.id":                       observed.task.ID,
		"task.revision":                 observed.record.Revision,
		"task.mode":                     string(observed.mode),
		"task.state":                    string(observed.task.State),
		"task.repo":                     observed.task.Repo,
		"task.repo-path":                observed.task.RepoPath,
		"task.branch":                   observed.task.Branch,
		"task.base":                     observed.task.Base,
		"task.worktree-path":            observed.task.WorktreePath,
		"task.owner":                    observed.task.Owner,
		"task.runtime-name":             observed.task.RuntimeName,
		"task.runtime-handle":           observed.task.RuntimeHandle,
		"repo.main-path":                observed.repoPath,
		"repo.git-common-dir":           observed.gitCommonDir,
		"worktree.fingerprint":          worktreeAuthority(observed.worktree, observed.worktreeFound, observed.worktreeErr, observed.branchMatches),
		"worktree.present":              boolString(observed.worktreeFound),
		"worktree.path":                 observed.worktree.Path,
		"worktree.branch":               observed.worktree.Worktree.Branch,
		"worktree.head":                 observed.worktree.Worktree.Head,
		"worktree.main":                 boolString(observed.worktree.Worktree.Main),
		"worktree.locked":               boolString(observed.worktree.Worktree.Locked),
		"worktree.locked-reason":        observed.worktree.Worktree.LockedReason,
		"worktree.prunable":             boolString(observed.worktree.Worktree.Prunable),
		"worktree.prunable-reason":      observed.worktree.Worktree.PrunableReason,
		"git.status-error":              errorString(observed.statusErr),
		"git.branch":                    observed.status.Branch,
		"git.detached":                  boolString(observed.status.Detached),
		"git.upstream":                  observed.status.Upstream,
		"git.ahead":                     strconv.Itoa(observed.status.Ahead),
		"git.behind":                    strconv.Itoa(observed.status.Behind),
		"git.changed":                   strconv.Itoa(observed.status.Changed),
		"git.staged":                    strconv.Itoa(observed.status.Staged),
		"git.unstaged":                  strconv.Itoa(observed.status.Unstaged),
		"git.untracked":                 strconv.Itoa(observed.status.Untracked),
		"git.conflicted":                strconv.Itoa(observed.status.Conflicted),
		"git.head":                      observed.head,
		"git.head-error":                errorString(observed.headErr),
		"git.base-oid":                  observed.baseOID,
		"git.base-oid-error":            errorString(observed.baseOIDErr),
		"git.upstream-oid":              observed.upstreamOID,
		"git.upstream-oid-error":        errorString(observed.upstreamOIDErr),
		"git.operation":                 observed.operation,
		"git.operation-active":          boolString(observed.inProgress),
		"git.operation-error":           errorString(observed.operationErr),
		"git.local-branch-exists":       boolString(observed.localBranchExists),
		"git.local-branch-oid":          observed.localBranchOID,
		"git.local-branch-oid-error":    errorString(observed.localBranchOIDErr),
		"git.remote-branch":             observed.remoteBranch,
		"git.remote-branch-exists":      boolString(observed.remoteBranchExists),
		"git.remote-branch-oid":         observed.remoteBranchOID,
		"git.remote-branch-oid-error":   errorString(observed.remoteBranchOIDErr),
		"runtime.error":                 errorString(observed.runtimeErr),
		"runtime.backend":               runtimeName(observed.runtime),
		"runtime.available":             boolString(observed.runtimeAvailable),
		"runtime.cleanup-fingerprint":   cleanupAuthority(observed.cleanup, observed.cleanupErr),
		"runtime.cleanup-error":         errorString(observed.cleanupErr),
		"runtime.cleanup-sessions":      strconv.Itoa(len(observed.cleanup.Sessions)),
		"runtime.cleanup-blockers":      strings.Join(observed.cleanup.Blockers, "\x00"),
		"runtime.cleanup-caller":        boolString(observed.cleanup.CallerContained),
		"runtime.cleanup-unknown":       boolString(observed.cleanup.RuntimeUnknown),
		"runtime.occupancy-fingerprint": occupancyAuthority(observed.occupancy, observed.occupancyErr),
		"runtime.occupancy-error":       errorString(observed.occupancyErr),
		"runtime.occupancy-sessions":    strconv.Itoa(len(observed.occupancy.Sessions)),
		"runtime.occupancy-agents":      strconv.Itoa(len(observed.occupancy.Agents)),
		"runtime.saved-live":            boolString(observed.savedRuntimeLive),
		"artifact.fingerprint":          artifactAuthority(observed.artifact, observed.artifactErr),
		"artifact.ready":                boolString(observed.artifact.Ready()),
		"artifact.known-empty":          boolString(observed.artifact.KnownEmpty),
		"artifact.intent-count":         strconv.Itoa(len(observed.artifact.Intents)),
		"artifact.error":                errorString(observed.artifactErr),
		"finish.fingerprint":            observed.finish.Fingerprint,
		"finish.authority":              finishAuthority(observed.finish, observed.finishErr),
		"finish.error":                  errorString(observed.finishErr),
		"finish.base-only":              strconv.Itoa(observed.finish.Relation.BaseOnly),
		"finish.branch-only":            strconv.Itoa(observed.finish.Relation.BranchOnly),
		"finish.status-changed":         strconv.Itoa(observed.finish.Status.Changed),
		"finish.status-staged":          strconv.Itoa(observed.finish.Status.Staged),
		"finish.status-unstaged":        strconv.Itoa(observed.finish.Status.Unstaged),
		"finish.status-untracked":       strconv.Itoa(observed.finish.Status.Untracked),
		"finish.status-conflicted":      strconv.Itoa(observed.finish.Status.Conflicted),
		"finish.unique-dirty":           strconv.Itoa(observed.finish.UniqueDirty()),
		"finish.equivalent-dirty":       strconv.Itoa(observed.finish.EquivalentDirty()),
		"completion.base-ref":           observed.completionBaseRef,
		"completion.base-oid":           observed.completionBaseOID,
		"completion.base-oid-error":     errorString(observed.completionBaseOIDErr),
		"completion.expected-branch":    observed.completionBranch,
		"completion.branch-oid":         observed.completionBranchOID,
		"completion.proof-ref":          observed.proofRef,
		"completion.proof-oid":          observed.proofOID,
		"completion.proof-oid-error":    errorString(observed.proofOIDErr),
		"completion.proof-contained":    boolString(observed.proofContained),
		"completion.proof-error":        errorString(observed.proofErr),
		"completion.integration":        integrationAuthority(observed.integration),
		"review.kind":                   string(observed.reviewKind),
		"review.remote-url":             observed.reviewRemoteURL,
		"review.repository":             observed.reviewRepository,
		"review.provider-bin":           reviewProviderBin(observed.reviewProvider),
		"review.provider-error":         errorString(observed.reviewResolveErr),
		"review.provider-available":     boolString(observed.reviewAvailable),
		"resume.base-ref":               observed.baseRef,
		"resume.base-ref-exists":        boolString(observed.baseRefExists),
		"resume.base-ref-exists-error":  errorString(observed.baseRefExistsErr),
		"resume.desired-worktree":       observed.desiredWorktree,
		"resume.desired-worktree-error": errorString(observed.desiredWorktreeErr),
		"service.host":                  s.host,
		"service.allow-shared-checkout": boolString(s.allowSharedCheckout),
		"caller.cwd":                    s.cwd,
		"caller.workspace":              s.callerWorkspace,
		"caller.pane":                   s.callerPane,
	}
	authority["finish.change-count"] = strconv.Itoa(len(observed.finish.Changes))
	for index, change := range observed.finish.Changes {
		prefix := fmt.Sprintf("finish.change.%d.", index)
		authority[prefix+"path"] = change.DisplayPath()
		authority[prefix+"base-equivalent"] = boolString(change.BaseEquivalent)
	}
	appendOptionAuthority(authority, request.Options)
	return authority
}

func appendOptionAuthority(authority map[string]string, options ActionOptions) {
	switch value := options.(type) {
	case ParkWarmOptions:
		authority["option.next"] = value.Next
		authority["option.note"] = value.Note
		authority["option.commit-wip"] = boolString(value.CommitWIP)
		authority["option.push"] = boolString(value.Push)
		authority["option.keep-session"] = boolString(value.KeepSession)
		authority["option.close-unknown"] = boolString(value.CloseUnknown)
		authority["option.assume-no-runtime"] = boolString(value.AssumeNoRuntime)
		authority["option.timeout"] = value.Timeout.String()
	case ParkColdOptions:
		authority["option.next"] = value.Next
		authority["option.note"] = value.Note
		authority["option.commit-wip"] = boolString(value.CommitWIP)
		authority["option.push"] = boolString(value.Push)
		authority["option.close-unknown"] = boolString(value.CloseUnknown)
		authority["option.assume-no-runtime"] = boolString(value.AssumeNoRuntime)
		authority["option.timeout"] = value.Timeout.String()
	case ResumeOptions:
		authority["option.fetch-refs"] = boolString(value.FetchRefs)
		authority["option.no-provision"] = boolString(value.NoProvision)
		authority["option.take-ownership"] = boolString(value.TakeOwnership)
	case CompleteDirectOptions:
		appendCompletionOptionAuthority(authority, value.Dirty, value.CommitMessage)
		authority["option.push"] = boolString(value.Push)
	case CompleteFFOptions:
		appendCompletionOptionAuthority(authority, value.Dirty, value.CommitMessage)
		authority["option.push-base"] = boolString(value.PushBase)
		authority["option.integration-target-policy"] = string(value.IntegrationTargetPolicy)
	case ReviewHandoffOptions:
		appendCompletionOptionAuthority(authority, value.Dirty, value.CommitMessage)
		authority["option.draft"] = boolString(value.Draft)
		authority["option.title"] = value.Title
		authority["option.body"] = value.Body
	case VerifyMergedOptions:
		appendCompletionOptionAuthority(authority, value.Dirty, value.CommitMessage)
		authority["option.base-ref"] = value.BaseRef
		authority["option.squash-commit"] = value.SquashCommit
		authority["option.push-base"] = boolString(value.PushBase)
	}
}

func appendCompletionOptionAuthority(authority map[string]string, dirty DirtyPolicy, message string) {
	authority["option.dirty"] = string(dirty)
	authority["option.commit-message"] = message
}

func runtimeName(rt runtime.Runtime) string {
	if rt == nil {
		return ""
	}
	return rt.Name()
}

func (s *lifecycleService) resolveRefOID(ctx context.Context, dir, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	value, err := s.gitRun(ctx, dir, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(value), err
}
