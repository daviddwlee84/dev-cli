package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newParkCmd(app *App) *cobra.Command {
	var (
		next   string
		note   string
		wip    bool
		push   bool
		cold   bool
		keepRT bool
	)
	cmd := &cobra.Command{
		Use:   "park [task]",
		Short: "Stop working on a task without losing the thread",
		Long: `Park the current task (or the one named) so its runtime session can close.

Parking is the missing move that makes a crowded sidebar shrink. It records
what to do next, optionally checkpoints uncommitted work, and closes the
runtime session — while leaving the branch and the worktree exactly where they
are. Closing a session is not abandoning a task.

  warm (default)  worktree and branch stay; session closes. Back within days.
  cold (--cold)   work is committed and pushed, worktree removed. Reconstruct
                  it anywhere later with dev resume.

A checkpoint commit is preferred over git stash: a stash is invisible in the
log, easy to forget, and cannot be pushed — so it can never reach another
machine, which is exactly what parking needs to support.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			t, err := resolveTask(app, args)
			if err != nil {
				return err
			}

			mode := t.EffectiveMode()
			if cold && mode == task.ModeDirect {
				return fmt.Errorf("direct task %s uses the canonical checkout on %s, so it cannot go cold; "+
					"park it warm or finish it", t.Title(), t.Branch)
			}
			target := task.Warm
			if cold {
				target = task.Cold
			}
			checkout := checkoutOf(t)

			if next != "" {
				t.Next = next
			}
			if note != "" {
				t.Note = note
			}
			if t.Next == "" {
				app.warnf("no next action recorded — `dev park --next \"...\"` is what makes resuming cheap")
			}

			// Checkpoint before anything is closed or removed.
			if _, statErr := os.Stat(checkout); statErr == nil {
				st, err := gitx.StatusOf(ctx, checkout)
				if err != nil {
					return err
				}
				switch {
				case wip && st.Dirty():
					msg := "wip: checkpoint"
					if t.Next != "" {
						msg = "wip: checkpoint — " + t.Next
					}
					made, err := gitx.WipCommit(ctx, checkout, msg)
					if err != nil {
						return err
					}
					if made {
						fmt.Fprintf(app.Out, "   committed  %s\n", msg)
					}
				case st.Dirty() && cold:
					return fmt.Errorf("%s has uncommitted changes; going cold removes the worktree.\n"+
						"Commit them, or re-run with --wip to checkpoint automatically",
						config.Contract(checkout))
				case st.Dirty():
					app.warnf("%s has uncommitted changes (kept in place; --wip checkpoints them)",
						config.Contract(checkout))
				}

				if push {
					if err := pushBranch(ctx, app, checkout, t.Branch); err != nil {
						return err
					}
				}
				if cold {
					st, err = gitx.StatusOf(ctx, checkout)
					if err != nil {
						return err
					}
					if !st.Published() || st.Ahead > 0 {
						return fmt.Errorf("%s is not fully pushed (%s); a cold task must be reconstructible "+
							"from the remote.\nRun `dev park --cold --push`", t.Branch, st.Summary())
					}
				}
			}

			rt := app.Runtime()
			if !keepRT && t.RuntimeHandle != "" {
				if err := rt.Close(ctx, t.RuntimeHandle); err != nil {
					app.warnf("could not close the runtime session: %v", err)
				} else {
					fmt.Fprintf(app.Out, "   closed     %s session %s\n", rt.Name(), t.RuntimeHandle)
				}
				t.RuntimeHandle = ""
			}

			if cold && t.WorktreePath != "" {
				m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
				if err := m.Remove(ctx, wt.RemoveRequest{
					RepoPath: t.RepoPath, Path: t.WorktreePath,
				}); err != nil {
					return err
				}
				// The path is cleared, but the branch stays: that is what makes
				// the task reconstructible rather than lost.
				t.WorktreePath = ""
			}
			if cold && mode == task.ModeBranch {
				// Free the canonical checkout for other work. The branch remains
				// local + remote and resume switches back to it.
				base := t.Base
				if base == "" {
					base = gitx.DefaultBranch(ctx, t.RepoPath)
				}
				if base == "" {
					return fmt.Errorf("task is pushed, but its base branch is unknown")
				}
				if _, err := gitx.Run(ctx, t.RepoPath, "switch", base); err != nil {
					return fmt.Errorf("task is pushed, but could not switch canonical checkout back to %s: %w",
						base, err)
				}
			}

			t.State = target
			t.Owner = config.Hostname()
			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s %s is %s", target.Icon(), t.Title(), target.Label())
			if t.Next != "" {
				fmt.Fprintf(app.Out, " — next: %s", t.Next)
			}
			fmt.Fprintln(app.Out)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&next, "next", "n", "", "what to do when you come back")
	f.StringVar(&note, "note", "", "free-form note")
	f.BoolVar(&wip, "wip", false, "checkpoint uncommitted work as a wip: commit")
	f.BoolVar(&push, "push", false, "push the branch so another machine can pick it up")
	f.BoolVar(&cold, "cold", false, "go cold: remove the worktree after confirming everything is pushed")
	f.BoolVar(&keepRT, "keep-session", false, "leave the runtime session open")
	cmd.ValidArgsFunction = completeTasks(app, task.Hot, task.Warm)
	return cmd
}

// pushBranch publishes a branch, setting upstream on first push.
func pushBranch(ctx context.Context, app *App, dir, branch string) error {
	if _, err := gitx.Run(ctx, dir, "push", "--set-upstream", "origin", branch); err != nil {
		return fmt.Errorf("push %s: %w", branch, err)
	}
	fmt.Fprintf(app.Out, "   pushed     origin/%s\n", branch)
	return nil
}

// resolveTask picks the task an argument names, or infers it from the current
// directory when no argument is given.
func resolveTask(app *App, args []string) (*task.Task, error) {
	if len(args) == 1 {
		return app.Tasks.Resolve(args[0])
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	t, err := app.Tasks.FindByWorktree(cwd)
	if err != nil {
		return nil, fmt.Errorf("no task for %s — name one explicitly, or run this inside a task's checkout",
			config.Contract(cwd))
	}
	return t, nil
}
