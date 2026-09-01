package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	retirement "github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

func newParkCmd(app *App) *cobra.Command {
	var (
		next            string
		note            string
		wip             bool
		push            bool
		cold            bool
		keepRT          bool
		closeUnknown    bool
		assumeNoRuntime bool
		timeout         time.Duration
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
  cold (--cold)   work is committed and pushed, session closed, worktree
                  removed. Reconstruct it anywhere later with dev resume.

--cold and --keep-session are incompatible: a runtime cannot stay pointed at a
checkout that cold parking removes.

A checkpoint commit is preferred over git stash: a stash is invisible in the
log, easy to forget, and cannot be pushed — so it can never reach another
machine, which is exactly what parking needs to support.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			if cold && keepRT {
				return fmt.Errorf("--cold and --keep-session cannot be combined: a session must not remain pointed at a removed checkout")
			}
			t, err := resolveTask(app, args)
			if err != nil {
				return err
			}
			expectedRevision := t.Revision()
			return app.Tasks.WithMutation(ctx, func() error {
				current, err := app.Tasks.Get(t.ID)
				if err != nil {
					return err
				}
				if current.Revision() != expectedRevision {
					return fmt.Errorf("task %s: %w", t.ID, task.ErrConflict)
				}
				t = current

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
							fmt.Fprintf(app.Out, "   %s  %s\n", app.outStyle().label("committed"), msg)
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

				rt := runtimeForTask(app, t)
				if cold && t.WorktreePath != "" {
					if err := ensureArtifactsFinalized(app, t.WorktreePath); err != nil {
						return err
					}
					if err := safeRemoveWorktree(ctx, rt, t.RepoPath, t.WorktreePath, false,
						closeUnknown, assumeNoRuntime, timeout); err != nil {
						return err
					}
					// The path is cleared, but the branch stays: that is what makes
					// the task reconstructible rather than lost.
					t.WorktreePath = ""
					clearTaskRuntime(t)
				} else if !keepRT && t.RuntimeHandle != "" {
					inspection, inspectErr := retirement.Inspect(ctx, rt, checkout, retirement.Options{})
					if inspectErr != nil {
						return inspectErr
					}
					if inspection.CallerContained {
						app.warnf("runtime left active because the caller is inside it; exit normally, then let sweep reconcile")
					} else {
						closed, closeErr := retirement.CloseAndWait(ctx, rt, checkout, retirement.Options{
							CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime, Timeout: timeout,
						})
						if closeErr != nil {
							return closeErr
						}
						if closed.ClosedSessions > 0 {
							fmt.Fprintf(app.Out, "   %s     %d %s session(s)\n", app.outStyle().label("closed"), closed.ClosedSessions, rt.Name())
						}
						clearTaskRuntime(t)
					}
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
				if err := app.Tasks.SaveIfRevisionUnderLock(t, expectedRevision); err != nil {
					return err
				}
				style := app.outStyle()
				fmt.Fprintf(app.Out, "%s %s is %s", target.Icon(), t.Title(), style.taskStateFor(string(target), target.Label()))
				if t.Next != "" {
					fmt.Fprintf(app.Out, " — %s %s", style.label("next:"), t.Next)
				}
				fmt.Fprintln(app.Out)
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVarP(&next, "next", "n", "", "what to do when you come back")
	f.StringVar(&note, "note", "", "free-form note")
	f.BoolVar(&wip, "wip", false, "checkpoint uncommitted work as a wip: commit")
	f.BoolVar(&push, "push", false, "push the branch so another machine can pick it up")
	f.BoolVar(&cold, "cold", false, "go cold: remove the worktree after confirming everything is pushed")
	f.BoolVar(&keepRT, "keep-session", false, "leave the runtime session open")
	f.BoolVar(&closeUnknown, "close-unknown", false, "allow external closure of unknown runtime status")
	f.BoolVar(&assumeNoRuntime, "assume-no-runtime", false, "continue when runtime enumeration fails")
	f.DurationVar(&timeout, "timeout", 5*time.Second, "maximum time to wait for runtime closure")
	cmd.ValidArgsFunction = completeTasks(app, task.Hot, task.Warm)
	return cmd
}

// pushBranch publishes a branch, setting upstream on first push.
func pushBranch(ctx context.Context, app *App, dir, branch string) error {
	if _, err := gitx.Run(ctx, dir, "push", "--set-upstream", "origin", branch); err != nil {
		return fmt.Errorf("push %s: %w", branch, err)
	}
	fmt.Fprintf(app.Out, "   %s     origin/%s\n", app.outStyle().label("pushed"), branch)
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
