package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newDoneCmd(app *App) *cobra.Command {
	var (
		ff           bool
		pr           bool
		deleteBranch bool
		keepWorktree bool
		push         bool
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

With neither, dev only reports what it would do.

Cleanup is deliberately conservative: --ff closes the runtime and removes the
worktree unless --keep-worktree is explicit; --pr leaves task/runtime/worktree
active for review. The branch is deleted only with --delete-branch, and one with
unpushed commits is never deleted. Agent-done state never triggers cleanup.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			t, err := resolveTask(app, args)
			if err != nil {
				return err
			}
			checkout := checkoutOf(t)
			if _, err := os.Stat(checkout); err != nil {
				return fmt.Errorf("%s no longer exists — resume the task first", config.Contract(checkout))
			}

			st, err := gitx.StatusOf(ctx, checkout)
			if err != nil {
				return err
			}
			if st.Dirty() {
				return fmt.Errorf("%s has uncommitted changes: %s.\n"+
					"Commit them, or park the task with --wip, before finishing it",
					config.Contract(checkout), st.Breakdown())
			}

			mode := t.EffectiveMode()
			if mode == task.ModeDirect {
				if ff || pr || deleteBranch || keepWorktree {
					return fmt.Errorf("direct task %s is already on %s; it has no branch/worktree to integrate. "+
						"Run `dev done` without --ff/--pr/--delete-branch/--keep-worktree", t.Title(), t.Branch)
				}
				if push {
					if err := pushBranch(ctx, app, checkout, t.Branch); err != nil {
						return err
					}
				}
				runtimeForTask(app, t) // normalize empty handle/name provenance
				if t.RuntimeHandle != "" {
					if _, _, err := closeTaskRuntime(ctx, app, t, checkout); err != nil {
						app.warnf("could not close the runtime session: %v", err)
					}
				}
				t.State = task.Done
				if err := app.Tasks.Save(t); err != nil {
					return err
				}
				fmt.Fprintf(app.Out, "%s %s completed directly on %s\n", task.Done.Icon(), t.Title(), t.Branch)
				fmt.Fprintln(app.Out, "   no branch or worktree was created or removed")
				return nil
			}

			base := t.Base
			if base == "" {
				base = gitx.DefaultBranch(ctx, t.RepoPath)
			}
			if base == "" {
				return fmt.Errorf("cannot determine the base branch for %s — pass --base when starting a task", t.Repo)
			}

			switch {
			case ff && pr:
				return fmt.Errorf("--ff and --pr are alternatives; pick one")
			case pr:
				if err := openPR(ctx, app, t, checkout, base); err != nil {
					return err
				}
				// A PR hands integration to review; the task is not done yet,
				// so state and cleanup stay untouched.
				fmt.Fprintln(app.Out, "\nThe branch is under review — run `dev done --ff` or `dev sweep` after it merges.")
				return nil
			case ff:
				if err := fastForward(ctx, app, t, checkout, base); err != nil {
					return err
				}
			default:
				fmt.Fprintf(app.Out, "%s on %s (base %s, %s)\n", t.Title(), t.Branch, base, st.Summary())
				fmt.Fprintln(app.Out, "Nothing done. Choose an integration mode:")
				fmt.Fprintln(app.Out, "  dev done --ff    rebase onto "+base+" and fast-forward it")
				fmt.Fprintln(app.Out, "  dev done --pr    push and open a pull request")
				return nil
			}

			if push {
				if _, err := gitx.Run(ctx, t.RepoPath, "push", "origin", base); err != nil {
					app.warnf("could not push %s: %v", base, err)
				} else {
					fmt.Fprintf(app.Out, "   pushed     origin/%s\n", base)
				}
			}

			rt := runtimeForTask(app, t)
			if t.RuntimeHandle != "" {
				handle := t.RuntimeHandle
				resolved, _, closeErr := closeTaskRuntime(ctx, app, t, checkout)
				rt = resolved
				if closeErr != nil {
					if t.WorktreePath != "" && !keepWorktree {
						return fmt.Errorf("merged, but could not close %s session %s; worktree kept: %w", rt.Name(), handle, closeErr)
					}
					app.warnf("could not close the runtime session: %v", closeErr)
				}
			}
			if t.WorktreePath != "" && !keepWorktree {
				m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
				if err := m.Remove(ctx, wt.RemoveRequest{RepoPath: t.RepoPath, Path: t.WorktreePath}); err != nil {
					app.warnf("could not remove the worktree: %v", err)
				} else {
					t.WorktreePath = ""
				}
			}
			if deleteBranch {
				if err := deleteMergedBranch(ctx, app, t.RepoPath, t.Branch, base); err != nil {
					app.warnf("%v", err)
				}
			}

			t.State = task.Done
			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s %s merged into %s\n", task.Done.Icon(), t.Title(), base)
			fmt.Fprintf(app.Out, "   the task entry stays until `dev sweep --apply` reaps it\n")
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&ff, "ff", false, "rebase onto the base and fast-forward it")
	f.BoolVar(&pr, "pr", false, "push and open a pull/merge request instead of merging locally")
	f.BoolVar(&keepWorktree, "keep-worktree", false, "keep the worktree checkout")
	f.BoolVar(&push, "push", false, "push the resulting branch (direct mode pushes its current branch)")
	// Off by default: a branch is cheap to keep and expensive to recreate, and
	// "merged" is not always "finished" — work often continues on a branch
	// after its first integration.
	f.BoolVar(&deleteBranch, "delete-branch", false, "delete the branch once its commits are in the base")
	cmd.ValidArgsFunction = completeTasks(app, task.Hot, task.Warm)
	return cmd
}

func directPushHint(push bool) string {
	if push {
		return " --push"
	}
	return ""
}

// fastForward rebases the task branch onto its base and then moves the base
// forward, producing a linear history with no merge commit.
//
// merge --ff-only is the guardrail: if the rebase did not actually put the
// branch ahead of the base, the merge fails loudly instead of quietly creating
// a merge commit the user did not ask for.
func fastForward(ctx context.Context, app *App, t *task.Task, checkout, base string) error {
	fmt.Fprintf(app.Out, "   rebasing   %s onto %s\n", t.Branch, base)
	if _, err := gitx.Run(ctx, checkout, "rebase", base); err != nil {
		return fmt.Errorf("%w\n\nThe rebase left conflicts to resolve in %s.\n"+
			"Fix them, `git rebase --continue`, then re-run dev done --ff",
			err, config.Contract(checkout))
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
