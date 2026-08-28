package retire

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Target is the live Git/task identity retirement must revalidate before every
// destructive stage.
type Target struct {
	TaskID         string
	RepoPath       string
	CheckoutPath   string
	Branch         string
	Base           string
	LinkedWorktree bool
}

// Request is one externally coordinated retirement attempt.
type Request struct {
	Target       Target
	Safety       Options
	DeleteBranch bool
}

// Result records which terminal resources were removed.
type Result struct {
	ClosedSessions  int
	RemovedWorktree bool
	DeletedBranch   bool
	DeletedTask     bool
}

// Service owns cleanup policy; every caller must use a freshly selected runtime.
type Service struct {
	Runtime runtime.Runtime
	Tasks   *task.Store
}

// Retire closes live runtime surfaces, waits for them to release the checkout,
// then removes only a validated linked worktree and finally its task record.
func (s *Service) Retire(ctx context.Context, req Request) (Result, error) {
	target, err := s.validateTarget(ctx, req.Target)
	if err != nil {
		return Result{}, err
	}
	inspection, err := CloseAndWait(ctx, s.Runtime, target.CheckoutPath, req.Safety)
	if err != nil {
		return Result{}, err
	}
	result := Result{ClosedSessions: inspection.ClosedSessions}

	// Reality may change while a runtime is draining. Never carry the earlier
	// worktree/dirty/ancestry proof across that boundary.
	target, err = s.validateTarget(ctx, target)
	if err != nil {
		return result, fmt.Errorf("revalidate after runtime close: %w", err)
	}
	finalInspection, err := Inspect(ctx, s.Runtime, target.CheckoutPath, req.Safety)
	if err != nil {
		return result, fmt.Errorf("reinspect after runtime close: %w", err)
	}
	if !finalInspection.Ready() || len(finalInspection.Sessions) > 0 {
		return result, fmt.Errorf("runtime reclaimed the checkout after closure; refusing worktree removal")
	}
	if target.LinkedWorktree {
		if err := gitx.RemoveWorktree(ctx, target.RepoPath, target.CheckoutPath, false); err != nil {
			return result, err
		}
		result.RemovedWorktree = true
	}
	if req.DeleteBranch && target.Branch != "" && target.Branch != target.Base && gitx.BranchExists(ctx, target.RepoPath, target.Branch) {
		if _, err := gitx.Run(ctx, target.RepoPath, "branch", "-d", target.Branch); err != nil {
			return result, fmt.Errorf("delete merged branch %s: %w", target.Branch, err)
		}
		result.DeletedBranch = true
	}
	if target.TaskID != "" && s.Tasks != nil {
		if err := s.validateTaskIdentity(target); err != nil {
			return result, fmt.Errorf("revalidate task before deletion: %w", err)
		}
		if err := s.Tasks.Delete(target.TaskID); err != nil {
			return result, fmt.Errorf("delete retired task %s: %w", target.TaskID, err)
		}
		result.DeletedTask = true
	}
	return result, nil
}

func (s *Service) validateTarget(ctx context.Context, target Target) (Target, error) {
	if target.RepoPath == "" || target.CheckoutPath == "" {
		return Target{}, fmt.Errorf("retirement target needs repository and checkout paths")
	}
	var err error
	target.RepoPath, err = pathx.Canonical(target.RepoPath)
	if err != nil {
		return Target{}, fmt.Errorf("canonicalize repository: %w", err)
	}
	target.CheckoutPath, err = pathx.Canonical(target.CheckoutPath)
	if err != nil {
		return Target{}, fmt.Errorf("canonicalize checkout: %w", err)
	}
	if target.Base == "" {
		return Target{}, fmt.Errorf("cannot prove integration without a base branch")
	}
	if err := s.validateTaskIdentity(target); err != nil {
		return Target{}, err
	}
	if _, statErr := os.Stat(target.CheckoutPath); statErr == nil {
		if operation, active, operationErr := gitx.InProgress(target.CheckoutPath); operationErr != nil {
			return Target{}, operationErr
		} else if active {
			return Target{}, fmt.Errorf("refusing retirement while Git operation %s is in progress", operation)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Target{}, statErr
	}
	if target.LinkedWorktree && target.Branch == "" {
		return Target{}, fmt.Errorf("refusing to retire a detached linked worktree")
	}
	if target.Branch != "" && target.Branch != target.Base {
		if !gitx.BranchExists(ctx, target.RepoPath, target.Branch) {
			if target.TaskID == "" {
				return Target{}, fmt.Errorf("branch %s no longer exists; only a persisted done task may reconcile it", target.Branch)
			}
		} else if _, err := gitx.Run(ctx, target.RepoPath, "merge-base", "--is-ancestor", target.Branch, target.Base); err != nil {
			return Target{}, fmt.Errorf("branch %s is not contained in %s", target.Branch, target.Base)
		}
	}

	if !target.LinkedWorktree {
		return target, nil
	}
	worktrees, err := gitx.Worktrees(ctx, target.RepoPath)
	if err != nil {
		return Target{}, err
	}
	registered := false
	for _, worktree := range worktrees {
		path, canonicalErr := pathx.Canonical(worktree.Path)
		if canonicalErr != nil || path != target.CheckoutPath {
			continue
		}
		registered = true
		if worktree.Main {
			return Target{}, fmt.Errorf("refusing to retire main checkout %s", target.CheckoutPath)
		}
		if worktree.Branch != target.Branch {
			return Target{}, fmt.Errorf("worktree %s is on %s, expected %s", target.CheckoutPath, worktree.Branch, target.Branch)
		}
		break
	}
	if !registered {
		if _, statErr := os.Stat(target.CheckoutPath); errors.Is(statErr, os.ErrNotExist) {
			if err := gitx.PruneWorktrees(ctx, target.RepoPath); err != nil {
				return Target{}, err
			}
			target.LinkedWorktree = false
			return target, nil
		}
		return Target{}, fmt.Errorf("path %s exists but Git no longer registers it; preserve orphaned artifacts before cleanup", target.CheckoutPath)
	}
	dirty, status, err := wtDirty(ctx, target.CheckoutPath)
	if err != nil {
		return Target{}, err
	}
	if dirty {
		return Target{}, fmt.Errorf("worktree %s is not clean: %s", target.CheckoutPath, status.Breakdown())
	}
	return target, nil
}

func (s *Service) validateTaskIdentity(target Target) error {
	if target.TaskID == "" {
		return nil
	}
	if s.Tasks == nil {
		return fmt.Errorf("retirement target %s needs its task store for identity revalidation", target.TaskID)
	}
	current, err := s.Tasks.Get(target.TaskID)
	if err != nil {
		return fmt.Errorf("reload retirement task %s: %w", target.TaskID, err)
	}
	if current.State != task.Done {
		return fmt.Errorf("task %s changed state to %s during retirement", target.TaskID, current.State)
	}
	repoPath, err := pathx.Canonical(current.RepoPath)
	if err != nil {
		return err
	}
	checkoutPath := current.RepoPath
	if current.EffectiveMode() == task.ModeWorktree && current.WorktreePath != "" {
		checkoutPath = current.WorktreePath
	}
	checkoutPath, err = pathx.Canonical(checkoutPath)
	if err != nil {
		return err
	}
	if repoPath != target.RepoPath || checkoutPath != target.CheckoutPath || current.Branch != target.Branch {
		return fmt.Errorf("task %s checkout identity changed during retirement", target.TaskID)
	}
	if current.Base != "" && current.Base != target.Base {
		return fmt.Errorf("task %s base changed from %s to %s during retirement", target.TaskID, target.Base, current.Base)
	}
	return nil
}

func wtDirty(ctx context.Context, path string) (bool, gitx.Status, error) {
	status, err := gitx.StatusOf(ctx, path)
	if err != nil {
		return false, status, err
	}
	return status.Dirty(), status, nil
}
