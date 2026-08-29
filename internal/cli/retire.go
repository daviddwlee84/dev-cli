package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	retirement "github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newRetireCmd(app *App) *cobra.Command {
	var (
		closeUnknown    bool
		assumeNoRuntime bool
		deleteBranch    bool
		timeout         time.Duration
	)
	cmd := &cobra.Command{
		Use:   "retire [task-or-worktree]",
		Short: "Close runtime state and safely remove an integrated worktree",
		Long: `Retire local execution state after a change has been integrated.

Run this from outside the target worktree and runtime. dev re-resolves every
covering session, refuses active agents and mixed-purpose workspaces, waits for
runtime closure, revalidates Git state, and only then removes a linked worktree
without force. A task is deleted only after every requested cleanup step works.

Unknown runtime status fails closed. An external coordinator may acknowledge it
with --close-unknown; working, blocked and waiting agents are never overridden.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			target, rt, err := resolveRetirementTarget(ctx, app, args)
			if err != nil {
				return err
			}
			if err := ensureArtifactsFinalized(app, target.CheckoutPath); err != nil {
				return err
			}
			service := &retirement.Service{Runtime: rt, Tasks: app.Tasks}
			result, err := service.Retire(ctx, retirement.Request{
				Target: target,
				Safety: retirement.Options{
					CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime, Timeout: timeout,
				},
				DeleteBranch: deleteBranch,
			})
			if err != nil {
				return err
			}
			style := app.outStyle()
			fmt.Fprintf(app.Out, "%s %s\n", style.success("RETIRED"), config.Contract(target.CheckoutPath))
			fmt.Fprintf(app.Out, "   %s  %d session(s) closed\n", style.label("runtime"), result.ClosedSessions)
			if result.RemovedWorktree {
				fmt.Fprintf(app.Out, "   %s removed\n", style.label("worktree"))
			}
			if result.DeletedBranch {
				fmt.Fprintf(app.Out, "   %s   %s deleted\n", style.label("branch"), target.Branch)
			}
			if result.DeletedTask {
				fmt.Fprintf(app.Out, "   %s     %s reaped\n", style.label("task"), target.TaskID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&closeUnknown, "close-unknown", false, "allow an external caller to close unknown/empty runtime status")
	f.BoolVar(&assumeNoRuntime, "assume-no-runtime", false, "continue when runtime enumeration fails (external callers only)")
	f.BoolVar(&deleteBranch, "delete-branch", false, "delete the contained local branch after worktree removal")
	f.DurationVar(&timeout, "timeout", 5*time.Second, "maximum time to wait for runtime sessions to close")
	cmd.ValidArgsFunction = completeTasks(app, task.Done)
	return cmd
}

func resolveRetirementTarget(ctx context.Context, app *App, args []string) (retirement.Target, runtime.Runtime, error) {
	if len(args) == 1 {
		expanded := config.Expand(args[0])
		if info, statErr := os.Stat(expanded); statErr == nil && info.IsDir() {
			return retirementTargetForPath(ctx, app, expanded)
		}
		t, err := app.Tasks.Resolve(args[0])
		if err != nil {
			return retirement.Target{}, nil, err
		}
		return retirementTargetForTask(ctx, app, t)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return retirement.Target{}, nil, err
	}
	if t, taskErr := app.Tasks.FindByWorktree(cwd); taskErr == nil {
		return retirementTargetForTask(ctx, app, t)
	}
	return retirementTargetForPath(ctx, app, cwd)
}

func retirementTargetForTask(ctx context.Context, app *App, t *task.Task) (retirement.Target, runtime.Runtime, error) {
	if t.State != task.Done {
		return retirement.Target{}, nil, fmt.Errorf("task %s is %s, not merged; run dev done first", t.ID, t.State)
	}
	base := t.Base
	if base == "" {
		base = gitx.DefaultBranch(ctx, t.RepoPath)
	}
	checkout := t.RepoPath
	linked := false
	rt := runtimeForTask(app, t)
	if t.EffectiveMode() == task.ModeWorktree {
		if t.WorktreePath != "" {
			checkout, linked = t.WorktreePath, true
		} else {
			if t.RuntimeHandle != "" {
				return retirement.Target{}, nil, fmt.Errorf("task %s has a runtime handle but no worktree path; reconcile it before retirement", t.ID)
			}
			rt = runtime.None{}
		}
	}
	return retirement.Target{
		TaskID: t.ID, RepoPath: t.RepoPath, CheckoutPath: checkout,
		Branch: t.Branch, Base: base, LinkedWorktree: linked,
	}, rt, nil
}

func retirementTargetForPath(ctx context.Context, app *App, path string) (retirement.Target, runtime.Runtime, error) {
	repository, err := gitx.Discover(ctx, path)
	if err != nil {
		return retirement.Target{}, nil, fmt.Errorf("resolve worktree %s: %w", config.Contract(path), err)
	}
	if !repository.IsLinkedWorktree {
		return retirement.Target{}, nil, fmt.Errorf("%s is not a linked worktree; retire explicit paths only from their linked checkout", config.Contract(path))
	}
	status, err := gitx.StatusOf(ctx, repository.Root)
	if err != nil {
		return retirement.Target{}, nil, err
	}
	if status.Detached || status.Branch == "" {
		return retirement.Target{}, nil, fmt.Errorf("refusing to retire detached worktree %s; preserve or branch its commit first", config.Contract(path))
	}
	base := gitx.DefaultBranch(ctx, repository.MainRoot)
	return retirement.Target{
		RepoPath: repository.MainRoot, CheckoutPath: repository.Root,
		Branch: status.Branch, Base: base, LinkedWorktree: true,
	}, app.Runtime(), nil
}

func safeRemoveWorktree(ctx context.Context, rt runtime.Runtime, repoPath, path string, force bool,
	closeUnknown, assumeNoRuntime bool, timeout time.Duration,
) error {
	if _, err := retirement.CloseAndWait(ctx, rt, path, retirement.Options{
		CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime, Timeout: timeout,
	}); err != nil {
		return err
	}
	if dirty, status, err := wt.DirtyCheck(ctx, path); err != nil {
		return err
	} else if dirty && !force {
		return fmt.Errorf("%s has uncommitted changes (%s)", config.Contract(path), status.Breakdown())
	}
	return gitx.RemoveWorktree(ctx, repoPath, path, force)
}
