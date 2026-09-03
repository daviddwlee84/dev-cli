package cli

import (
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/spf13/cobra"
)

func newResumeCmd(app *App) *cobra.Command {
	var (
		noProvision bool
		fetch       bool
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "resume <task>",
		Short: "Pick a task back up, rebuilding whatever is missing",
		Long: `Make a parked task hot again.

Warm tasks still have their worktree, so this just reopens a runtime session.
Cold tasks do not: the worktree is rebuilt from the branch, which is why going
cold is safe in the first place — the branch, not the directory, is the task's
identity.

If the task is owned by another machine, resuming here takes ownership. Two
machines writing the same branch is the one way to make this workflow produce
a conflict, so dev asks before doing it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			execution, err := executeTaskLifecycle(ctx, app, func() (*task.Task, error) {
				return app.Tasks.Resolve(args[0])
			}, taskflow.ResumeOptions{
				FetchRefs: fetch, NoProvision: noProvision, TakeOwnership: force,
			})
			if err != nil {
				return err
			}

			resumed := &execution.Task
			handoff, ok := execution.Result.Handoff()
			if !ok || handoff.Path == "" {
				return fmt.Errorf("task %s is HOT, but resume returned no activation handoff", resumed.ID)
			}
			checkout := handoff.Path
			var activationRuntime runtime.Runtime
			switch handoff.Kind {
			case taskflow.HandoffDirectory:
				if resumed.RuntimeName != "" || resumed.RuntimeHandle != "" {
					return fmt.Errorf("task %s returned a directory handoff with persisted runtime %s/%s",
						resumed.ID, resumed.RuntimeName, resumed.RuntimeHandle)
				}
			case taskflow.HandoffRuntime:
				activationRuntime = app.runtimeNamed(handoff.Runtime)
				if activationRuntime == nil || activationRuntime.Name() != handoff.Runtime {
					return fmt.Errorf("resume handoff backend %q is unavailable", handoff.Runtime)
				}
				if resumed.RuntimeName != handoff.Runtime || resumed.RuntimeHandle != handoff.RuntimeHandle {
					return fmt.Errorf("task %s persisted runtime %s/%s, but resume handed off %s/%s",
						resumed.ID, resumed.RuntimeName, resumed.RuntimeHandle, handoff.Runtime, handoff.RuntimeHandle)
				}
				annotate(app, activationRuntime, resumed)
			default:
				return fmt.Errorf("task %s is HOT, but resume returned unsupported %s handoff", resumed.ID, handoff.Kind)
			}

			mode := resumed.EffectiveMode()
			style := app.outStyle()
			fmt.Fprintf(app.Out, "%s %s  %s on %s (%s)\n",
				task.Hot.Icon(), resumed.Title(), resumed.Repo, resumed.Branch, style.dim(string(mode)))
			if resumed.Next != "" {
				fmt.Fprintf(app.Out, "   %s      %s\n", style.label("next"), resumed.Next)
			}
			if resumed.AgentSession != "" {
				fmt.Fprintf(app.Out, "   %s     %s (resumable)\n", style.label("agent"), resumed.AgentSession)
			}
			if st, statusErr := gitx.StatusOf(ctx, checkout); statusErr == nil {
				fmt.Fprintf(app.Out, "   %s       %s\n", style.label("git"), style.git(st.Summary()))
				if st.Behind > 0 {
					app.warnf("branch is %d behind upstream — `git pull --ff-only` before you start", st.Behind)
				}
			}
			if handoff.Kind == taskflow.HandoffDirectory {
				return app.cdDirective(checkout)
			}
			return activateRuntime(ctx, activationRuntime, handoff.RuntimeHandle)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&noProvision, "no-provision", false, "skip dependency install when rebuilding a worktree")
	f.BoolVar(&fetch, "fetch", true, "fetch from origin first")
	f.BoolVar(&force, "force", false, "take ownership of a task owned by another machine")
	cmd.ValidArgsFunction = completeTasks(app, task.Warm, task.Cold)
	return cmd
}
