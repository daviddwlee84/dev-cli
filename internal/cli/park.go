package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
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

			var options taskflow.ActionOptions
			if cold {
				options = taskflow.ParkColdOptions{
					Next: next, Note: note, CommitWIP: wip, Push: push,
					CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime, Timeout: timeout,
				}
			} else {
				options = taskflow.ParkWarmOptions{
					Next: next, Note: note, CommitWIP: wip, Push: push, KeepSession: keepRT,
					CloseUnknown: closeUnknown, AssumeNoRuntime: assumeNoRuntime, Timeout: timeout,
				}
			}
			execution, err := executeTaskLifecycle(ctx, app, func() (*task.Task, error) {
				return resolveTask(app, args)
			}, options)
			if err != nil {
				return err
			}

			parked := &execution.Task
			style := app.outStyle()
			fmt.Fprintf(app.Out, "%s %s is %s", parked.State.Icon(), parked.Title(),
				style.taskStateFor(string(parked.State), parked.State.Label()))
			if parked.Next != "" {
				fmt.Fprintf(app.Out, " — %s %s", style.label("next:"), parked.Next)
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
	f.BoolVar(&closeUnknown, "close-unknown", false, "allow external closure of unknown runtime status")
	f.BoolVar(&assumeNoRuntime, "assume-no-runtime", false, "continue when runtime enumeration fails")
	f.DurationVar(&timeout, "timeout", 5*time.Second, "maximum time to wait for runtime closure")
	cmd.ValidArgsFunction = completeTasks(app, task.Hot, task.Warm)
	return cmd
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
