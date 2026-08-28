package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

func newDoneCmd(app *App) *cobra.Command {
	var (
		ff           bool
		pr           bool
		deleteBranch bool
		keepWorktree bool
		push         bool
		dirtyPolicy  string
		message      string
		yes          bool
	)
	cmd := &cobra.Command{
		Use:   "done [task]",
		Short: "Integrate a finished change stream and clean up",
		Long: `Finish a task: integrate the branch, then remove what it leaves behind.

Two integration modes, matching the two shapes a branch's history takes:

  --ff    rebase onto the base and fast-forward it. Use when the branch's
          commits are worth keeping in the base's history.
  --pr    push and open a pull/merge request with the detected forge CLI, leaving the
          merge to review and CI. Use when someone (or something) should look
          at it first.

For branch/worktree tasks, omitting both modes opens a finish wizard on an
interactive terminal. A non-interactive caller still gets a report and must
pass --ff or --pr explicitly. Direct tasks need no integration mode.

A dirty checkout is analyzed against the base, not rejected as one opaque
condition. Interactive use can commit everything, discard everything, or
cancel. Unique content requires typing DROP before discard. Scripts select an
explicit --dirty policy; destructive discard also requires --yes.

Cleanup is deliberately conservative: --ff closes the runtime and removes the
worktree unless --keep-worktree is explicit; --pr leaves task/runtime/worktree
active for review. The branch is deleted only with --delete-branch, and one with
unpushed commits is never deleted. Agent-done state never triggers cleanup.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ff && pr {
				return fmt.Errorf("--ff and --pr are alternatives; pick one")
			}
			integration := doneIntegrationNone
			if ff {
				integration = doneIntegrationFF
			}
			if pr {
				integration = doneIntegrationPR
			}
			return runDone(ctxOf(), app, args, doneOptions{
				Integration: integration, DirtyPolicy: doneDirtyPolicy(dirtyPolicy),
				Message: message, Yes: yes, DeleteBranch: deleteBranch,
				KeepWorktree: keepWorktree, Push: push,
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&ff, "ff", false, "rebase onto the base and fast-forward it")
	f.BoolVar(&pr, "pr", false, "push and open a pull/merge request instead of merging locally")
	f.BoolVar(&keepWorktree, "keep-worktree", false, "keep the worktree checkout")
	f.BoolVar(&push, "push", false, "push the resulting branch (direct mode pushes its current branch)")
	f.StringVar(&dirtyPolicy, "dirty", string(doneDirtyAuto), "dirty checkout policy: auto, fail, commit or discard")
	f.StringVarP(&message, "message", "m", "", "commit message for --dirty=commit")
	f.BoolVarP(&yes, "yes", "y", false, "confirm the selected finish plan (required for non-interactive discard)")
	registerFlagCompletion(cmd, "dirty", fixedCompletions("auto", "fail", "commit", "discard"))
	// Off by default: a branch is cheap to keep and expensive to recreate, and
	// "merged" is not always "finished" — work often continues on a branch
	// after its first integration.
	f.BoolVar(&deleteBranch, "delete-branch", false, "delete the branch once its commits are in the base")
	cmd.ValidArgsFunction = completeTasks(app, task.Hot, task.Warm)
	return cmd
}

// fastForward rebases the task branch onto its base and then moves the base
// forward, producing a linear history with no merge commit.
//
// merge --ff-only is the guardrail: if the rebase did not actually put the
// branch ahead of the base, the merge fails loudly instead of quietly creating
// a merge commit the user did not ask for.
func fastForward(ctx context.Context, app *App, t *task.Task, checkout, base string) error {
	relation, err := gitx.AnalyzeFinish(ctx, checkout, base, t.Branch)
	if err != nil {
		return err
	}
	if relation.Relation.Contained() {
		fmt.Fprintf(app.Out, "   integrated  %s is already contained in %s\n", t.Branch, base)
		return nil
	}
	if relation.Relation.BaseOnly > 0 {
		fmt.Fprintf(app.Out, "   rebasing   %s onto %s\n", t.Branch, base)
		if _, err := gitx.Run(ctx, checkout, "rebase", base); err != nil {
			return fmt.Errorf("%w\n\nThe rebase left conflicts to resolve in %s.\n"+
				"Fix them, `git rebase --continue`, then re-run dev done --ff",
				err, config.Contract(checkout))
		}
	}
	// The base branch lives in the main checkout, not the worktree.
	fmt.Fprintf(app.Out, "   merging    %s into %s (fast-forward only)\n", t.Branch, base)
	if _, err := gitx.Run(ctx, t.RepoPath, "switch", base); err != nil {
		return err
	}
	if _, err := gitx.Run(ctx, t.RepoPath, "merge", "--ff-only", t.Branch); err != nil {
		return fmt.Errorf("%w\n\n%s could not be fast-forwarded. Something else moved it; "+
			"rebase again or integrate by hand", err, base)
	}
	return nil
}

// deleteMergedBranch removes a branch only when git agrees its commits are already
// contained in the base — the check that makes cleanup safe.
func deleteMergedBranch(ctx context.Context, app *App, repoPath, branch, base string) error {
	if _, err := gitx.Run(ctx, repoPath, "merge-base", "--is-ancestor", branch, base); err != nil {
		return fmt.Errorf("keeping branch %s: it has commits not in %s", branch, base)
	}
	if _, err := gitx.Run(ctx, repoPath, "branch", "-d", branch); err != nil {
		return fmt.Errorf("could not delete %s: %w", branch, err)
	}
	fmt.Fprintf(app.Out, "   deleted    branch %s\n", branch)
	return nil
}

// openPR pushes the branch and asks the forge CLI to open a pull/merge
// request. When no forge CLI is available dev prints the push result and the
// URL to open by hand, rather than failing: the branch being published is the
// part that actually matters.
func openPR(ctx context.Context, app *App, t *task.Task, checkout, base string) error {
	if err := pushBranch(ctx, app, checkout, t.Branch); err != nil {
		return err
	}
	kind := forge.Detect(ctx, t.RepoPath)
	f, err := forge.For(kind)
	if err != nil || !f.Available() {
		app.warnf("no forge CLI for this remote — the branch is pushed; open the request in your browser")
		return nil
	}
	url, err := f.CreatePR(ctx, checkout, forge.PRRequest{
		Base: base, Head: t.Branch, Fill: true,
	})
	if err != nil {
		return err
	}
	if url != "" {
		fmt.Fprintf(app.Out, "   opened     %s\n", url)
	}
	return nil
}
