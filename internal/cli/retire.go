package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/spf13/cobra"
)

type retireCommandTarget struct {
	Task *task.Task
	Path string
}

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
			target, err := resolveRetireCommandTarget(ctx, app, args)
			if err != nil {
				return err
			}
			if target.Task != nil {
				return retireTaskWithTaskflow(ctx, app, target.Task, flow.RetireOptions{
					CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime,
					DeleteBranch: deleteBranch, Timeout: timeout,
				}, cmd.Flags().Changed("delete-branch") && deleteBranch)
			}
			return retireUnmanagedPathCompatibility(ctx, app, target.Path, closeUnknown,
				assumeNoRuntime, deleteBranch, timeout)
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

func resolveRetireCommandTarget(ctx context.Context, app *App, args []string) (retireCommandTarget, error) {
	if len(args) == 1 {
		expanded := config.Expand(args[0])
		if info, statErr := os.Stat(expanded); statErr == nil && info.IsDir() {
			return retireCommandTargetForPath(ctx, app, expanded)
		}
		candidate, err := app.Tasks.Resolve(args[0])
		if err != nil {
			return retireCommandTarget{}, err
		}
		return retireCommandTarget{Task: candidate}, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return retireCommandTarget{}, err
	}
	return retireCommandTargetForPath(ctx, app, cwd)
}

func retireCommandTargetForPath(ctx context.Context, app *App, path string) (retireCommandTarget, error) {
	repository, err := gitx.Discover(ctx, path)
	if err != nil {
		return retireCommandTarget{}, fmt.Errorf("resolve worktree %s: %w", config.Contract(path), err)
	}
	candidate, err := exactTaskForRetirementCheckout(app, repository.Root)
	if err != nil {
		return retireCommandTarget{}, err
	}
	if candidate != nil {
		return retireCommandTarget{Task: candidate}, nil
	}
	return retireCommandTarget{Path: repository.Root}, nil
}

func exactTaskForRetirementCheckout(app *App, checkout string) (*task.Task, error) {
	canonicalCheckout, err := pathx.Canonical(checkout)
	if err != nil {
		return nil, fmt.Errorf("canonicalize retirement checkout %s: %w", config.Contract(checkout), err)
	}
	records, _, err := app.Tasks.ListRecords()
	if err != nil {
		return nil, err
	}
	var matched *task.Task
	for _, record := range records {
		candidate := record.Task
		if candidate.EffectiveMode() == task.ModeWorktree && candidate.WorktreePath == "" {
			// A COLD/DONE worktree task with no checkout does not claim the
			// canonical repository merely because checkoutOf falls back there.
			continue
		}
		candidateCheckout, canonicalErr := pathx.Canonical(checkoutOf(&candidate))
		if canonicalErr != nil || candidateCheckout != canonicalCheckout {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("checkout %s is claimed by multiple tasks (%s, %s); name one explicitly or reconcile task metadata",
				config.Contract(canonicalCheckout), matched.ID, candidate.ID)
		}
		matched = &candidate
	}
	return matched, nil
}

func retireTaskWithTaskflow(
	ctx context.Context,
	app *App,
	selected *task.Task,
	options flow.RetireOptions,
	authorizeBranchDeletion bool,
) error {
	session, err := newLifecycleSession(ctx, app, func() (*task.Task, error) { return selected, nil })
	if err != nil {
		return err
	}
	plan, err := session.plan(ctx, options)
	if err != nil {
		return err
	}
	approval := flow.Approve(plan.PlanID)
	if plan.Confirmation.Kind == flow.ConfirmationTyped {
		if !authorizeBranchDeletion {
			return fmt.Errorf("%s requires its exact typed confirmation", plan.Summary)
		}
		approval = flow.ApproveWithToken(plan.PlanID, plan.Confirmation.Token)
	}
	result, applyErr := session.apply(ctx, plan, approval)
	if applyErr != nil {
		renderLifecycleResult(app, plan, result)
		return presentLifecycleApplyError(plan, applyErr)
	}
	if err := session.requireRetired(app, result); err != nil {
		return err
	}

	style := app.outStyle()
	fmt.Fprintf(app.Out, "%s %s\n", style.success("RETIRED"), config.Contract(checkoutOf(selected)))
	renderLifecycleResult(app, plan, result)
	return nil
}

// retireUnmanagedPathCompatibility preserves the contained-only path contract
// while routing every mutation through taskflow's exact unmanaged transaction.
func retireUnmanagedPathCompatibility(
	ctx context.Context,
	app *App,
	path string,
	closeUnknown, assumeNoRuntime, deleteBranch bool,
	timeout time.Duration,
) error {
	repository, err := gitx.Discover(ctx, path)
	if err != nil {
		return fmt.Errorf("resolve worktree %s: %w", config.Contract(path), err)
	}
	if !repository.IsLinkedWorktree {
		return fmt.Errorf("%s is not a linked worktree; retire explicit paths only from their linked checkout", config.Contract(path))
	}
	locator, err := exactUnmanagedWorktreeLocator(ctx, repository.MainRoot, repository.Root)
	if err != nil {
		return err
	}
	base := gitx.DefaultBranch(ctx, repository.MainRoot)
	if base == "" {
		return fmt.Errorf("cannot prove unmanaged retirement without an explicit repository default branch")
	}
	execution, err := executeNonTaskLifecycle(ctx, app, locator, flow.RemoveCheckoutOptions{
		RequireContained: true, ContainmentBase: base, DeleteContainedBranch: deleteBranch,
		CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime, Timeout: timeout,
	}, deleteBranch)
	if err != nil {
		return err
	}
	if execution.Result.Milestone != flow.MilestoneNone {
		return fmt.Errorf("unmanaged retirement returned unexpected milestone %s", execution.Result.Milestone)
	}
	fmt.Fprintf(app.Out, "%s %s\n", app.outStyle().success("RETIRED"), config.Contract(locator.CheckoutPath))
	return nil
}
