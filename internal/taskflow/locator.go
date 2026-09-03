package taskflow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// locateExactTask builds a managed locator from one exact task record and a
// fresh view of its repository. Missing checkout evidence is retained as an
// expected path (or an intentionally empty COLD path) so Plan can explain the
// drift instead of losing the task before transition validation.
func (s *lifecycleService) locateExactTask(ctx context.Context, taskID string) (Locator, error) {
	if err := contextError(ctx); err != nil {
		return Locator{}, err
	}
	if !exactTaskID.MatchString(taskID) || filepath.Base(taskID) != taskID || taskID == "." || taskID == ".." {
		return Locator{}, fmt.Errorf("%w: invalid exact TaskID %q", ErrInvalidRequest, taskID)
	}

	record, err := s.tasks.GetRecord(taskID)
	if err != nil {
		return Locator{}, fmt.Errorf("%w: load exact task %q: %v", ErrInvalidRequest, taskID, err)
	}
	candidate := record.Task
	if err := candidate.Validate(); err != nil {
		return Locator{}, fmt.Errorf("%w: exact task %q is invalid: %v", ErrInvalidRequest, taskID, err)
	}
	mode := candidate.EffectiveMode()
	if !validMode(mode) || !validState(candidate.State) {
		return Locator{}, fmt.Errorf("%w: exact task %q has invalid mode or state", ErrInvalidRequest, taskID)
	}

	repository, err := s.gitDiscover(ctx, candidate.RepoPath)
	if err != nil {
		return Locator{}, fmt.Errorf("%w: discover exact repository for task %s: %v", ErrInvalidRequest, taskID, err)
	}
	repoPath, err := s.canonicalPath(repository.MainRoot)
	if err != nil {
		return Locator{}, fmt.Errorf("%w: canonicalize main checkout for task %s: %v", ErrInvalidRequest, taskID, err)
	}
	commonDir, err := s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		return Locator{}, fmt.Errorf("%w: canonicalize Git common directory for task %s: %v", ErrInvalidRequest, taskID, err)
	}
	recordedRepo, err := s.canonicalPath(candidate.RepoPath)
	if err != nil {
		return Locator{}, fmt.Errorf("%w: canonicalize recorded repository for task %s: %v", ErrInvalidRequest, taskID, err)
	}
	if recordedRepo != repoPath {
		return Locator{}, &StalePlanError{Reason: fmt.Sprintf(
			"recorded repository %q resolves to %q, but Git reports main checkout %q",
			candidate.RepoPath, recordedRepo, repoPath,
		)}
	}

	expectedCheckout := repoPath
	if mode == task.ModeWorktree {
		expectedCheckout = ""
		if candidate.WorktreePath != "" {
			expectedCheckout, err = s.canonicalPath(candidate.WorktreePath)
			if err != nil {
				return Locator{}, fmt.Errorf("%w: canonicalize expected checkout for task %s: %v", ErrInvalidRequest, taskID, err)
			}
		}
	}

	locator := Locator{
		RepoKey:      commonDir,
		RowKey:       taskID,
		RowKind:      "task",
		RepositoryID: commonDir,
		GitCommonDir: commonDir,
		TaskID:       taskID,
		TaskRevision: record.Revision,
		RepoPath:     repoPath,
		CheckoutPath: expectedCheckout,
		Branch:       candidate.Branch,
		Base:         candidate.Base,
		Remote:       "origin",
		Mode:         mode,
		State:        candidate.State,
	}

	registered, found := s.locateRegisteredCheckout(ctx, candidate, repoPath, expectedCheckout)
	if found {
		locator.RowKey = registered.Path
		locator.RowKind = "checkout"
		locator.HeadOID = strings.TrimSpace(registered.Worktree.Head)
		if status, statusErr := s.gitStatus(ctx, registered.Path); statusErr == nil {
			locator.Upstream = status.Upstream
			if head, headErr := s.resolveRefOID(ctx, registered.Path, "HEAD"); headErr == nil {
				locator.HeadOID = head
			}
		}
	}
	if locator.HeadOID == "" {
		locator.HeadOID, _ = s.resolveRefOID(ctx, repoPath, localBranchRef(candidate.Branch))
	}
	if locator.Upstream == "" && (!found || mode == task.ModeWorktree) {
		locator.Upstream = s.branchUpstream(ctx, repoPath, candidate.Branch)
	}
	locator.BaseOID, _ = s.resolveRefOID(ctx, repoPath, candidate.Base)
	locator.UpstreamOID, _ = s.resolveRefOID(ctx, repoPath, locator.Upstream)

	current, err := s.tasks.GetRecord(taskID)
	if err != nil {
		return Locator{}, &StalePlanError{
			ExpectedAuthorityFingerprint: record.Revision,
			Reason:                       "task record disappeared while locating",
		}
	}
	if current.Revision != record.Revision {
		return Locator{}, staleTaskRevision(record.Revision, current.Revision, "task record changed while locating")
	}
	return locator, nil
}

func (s *lifecycleService) locateRegisteredCheckout(
	ctx context.Context,
	candidate task.Task,
	repoPath string,
	expectedCheckout string,
) (gitx.RegisteredWorktree, bool) {
	if expectedCheckout != "" {
		registered, err := s.resolveWorktree(ctx, repoPath, expectedCheckout)
		return registered, err == nil
	}
	if candidate.EffectiveMode() != task.ModeWorktree || candidate.State != task.Cold {
		return gitx.RegisteredWorktree{}, false
	}

	worktrees, err := s.gitWorktrees(ctx, repoPath)
	if err != nil {
		return gitx.RegisteredWorktree{}, false
	}
	match := ""
	for _, worktree := range worktrees {
		if worktree.Branch != candidate.Branch {
			continue
		}
		if match != "" {
			return gitx.RegisteredWorktree{}, false
		}
		match = worktree.Path
	}
	if match == "" {
		return gitx.RegisteredWorktree{}, false
	}
	registered, err := s.resolveWorktree(ctx, repoPath, match)
	return registered, err == nil
}

func (s *lifecycleService) branchUpstream(ctx context.Context, repoPath, branch string) string {
	if branch == "" {
		return ""
	}
	value, err := s.gitRun(ctx, repoPath,
		"for-each-ref", "--format=%(upstream:short)", localBranchRef(branch),
	)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
