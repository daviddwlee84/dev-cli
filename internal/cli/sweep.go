package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	retirement "github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

// suggestion is one proposed change, with the reason and the exact effect.
type suggestion struct {
	row    inventory.Row
	action string
	reason string
	// apply performs the change. nil means "report only".
	apply func() error
}

func newSweepCmd(app *App) *cobra.Command {
	var (
		apply     bool
		staleDays int
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Review stale tasks and drifted state, and act on them",
		Long: `Show which tasks have gone stale or drifted, and what dev would do about it.

Cleanup usually fails not because people are unwilling but because there is no
trustworthy way to be sure the work is recoverable. So sweep reports first and
only changes things with --apply, confirming each one unless you pass --yes.
Nothing here ever deletes uncommitted work.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			tasks, err := app.Tasks.List()
			if err != nil {
				return err
			}
			rt := app.Runtime()
			rows := inventory.Collect(ctx, tasks, rt, inventory.Options{})
			stale := time.Duration(staleDays) * 24 * time.Hour

			var sugg []suggestion
			for _, r := range rows {
				sugg = append(sugg, suggestFor(app, ctx, r, stale)...)
			}

			// Live sessions no task claims: the other half of a crowded sidebar.
			if sessions, err := rt.List(ctx); err == nil {
				if orphans := inventory.Orphans(sessions, rows); len(orphans) > 0 {
					fmt.Fprintf(app.Out, "\n%d live session(s) with no task recorded:\n", len(orphans))
					for _, s := range orphans {
						dir := "—"
						if len(s.Dirs) > 0 {
							dir = config.Contract(s.Dirs[0])
						}
						fmt.Fprintf(app.Out, "  %-24s %s\n", truncate(s.Label, 24), dir)
					}
					fmt.Fprintln(app.Out, "  → `dev start` in one of those directories to track it, or just close it.")
				}
			}

			if len(sugg) == 0 {
				fmt.Fprintln(app.Out, "\nNothing to sweep — every task matches its recorded state.")
				return nil
			}

			fmt.Fprintf(app.Out, "\n%d suggestion(s):\n\n", len(sugg))
			for _, s := range sugg {
				fmt.Fprintf(app.Out, "  %s %-28s %s\n", s.row.Task.State.Icon(),
					truncate(s.row.Task.Title(), 28), s.reason)
				fmt.Fprintf(app.Out, "     → %s\n", s.action)
			}

			if !apply {
				fmt.Fprintln(app.Out, "\nRe-run with --apply to act on these (each is confirmed individually).")
				return nil
			}

			in := bufio.NewReader(os.Stdin)
			for _, s := range sugg {
				if s.apply == nil {
					continue
				}
				if !yes && !confirm(app, in, s.action) {
					continue
				}
				if err := s.apply(); err != nil {
					app.warnf("%s: %v", s.row.Task.ID, err)
					continue
				}
				fmt.Fprintf(app.Out, "  done: %s\n", s.action)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "act on the suggestions instead of only reporting")
	f.IntVar(&staleDays, "stale-days", 14, "days without a commit before a task counts as stale")
	f.BoolVar(&yes, "yes", false, "with --apply, do not confirm each change")
	return cmd
}

func suggestFor(app *App, ctx context.Context, r inventory.Row, stale time.Duration) []suggestion {
	t := r.Task
	var out []suggestion

	switch {
	// A hot task with no live session is the commonest drift: the session was
	// closed (or the machine rebooted) without parking.
	case t.State == task.Hot && !r.Live():
		out = append(out, suggestion{
			row:    r,
			reason: fmt.Sprintf("hot but no live session, idle %s", humanAge(r.Age())),
			action: fmt.Sprintf("mark %s warm", t.ID),
			apply: func() error {
				t.State = task.Warm
				return app.Tasks.Save(t)
			},
		})

	// A warm task nobody has touched in weeks is a candidate for cold: keeping
	// the worktree costs disk and clutters every listing.
	case t.State == task.Warm && r.Age() > stale:
		reason := fmt.Sprintf("warm and untouched for %s", humanAge(r.Age()))
		if r.Status.Dirty() {
			out = append(out, suggestion{row: r, reason: reason + ", but has uncommitted work",
				action: "commit or `dev park --wip` before going cold — not changed automatically"})
			break
		}
		if !r.Status.Synced() {
			out = append(out, suggestion{row: r, reason: reason + fmt.Sprintf(", not pushed (%s)", r.Status.Summary()),
				action: fmt.Sprintf("push %s so it can go cold: dev park %s --cold --push", t.Branch, t.ID)})
			break
		}
		out = append(out, suggestion{
			row:    r,
			reason: reason + ", clean and pushed",
			action: fmt.Sprintf("go cold: remove %s (branch and remote keep the work)", config.Contract(r.Checkout)),
			apply: func() error {
				if t.WorktreePath != "" {
					if err := ensureArtifactsFinalized(app, t.WorktreePath); err != nil {
						return err
					}
					if err := safeRemoveWorktree(ctx, runtimeForTask(app, t), t.RepoPath, t.WorktreePath,
						false, false, false, 5*time.Second); err != nil {
						return err
					}
					t.WorktreePath = ""
				}
				t.State = task.Cold
				clearTaskRuntime(t)
				return app.Tasks.Save(t)
			},
		})

	// DONE means integrated; runtime/worktree cleanup is a separate,
	// externally coordinated retirement step.
	case t.State == task.Done:
		out = append(out, suggestion{
			row:    r,
			reason: "merged, cleanup pending",
			action: fmt.Sprintf("retire runtime/worktree for %s", t.ID),
			apply: func() error {
				if err := ensureArtifactsFinalized(app, checkoutOf(t)); err != nil {
					return err
				}
				target, rt, err := retirementTargetForTask(ctx, app, t)
				if err != nil {
					return err
				}
				service := &retirement.Service{Runtime: rt, Tasks: app.Tasks}
				_, err = service.Retire(ctx, retirement.Request{Target: target})
				return err
			},
		})
	}

	// Drift that is independent of the lifecycle stage.
	if r.WorktreeMissing && t.WorktreePath != "" {
		wtPath := t.WorktreePath
		out = append(out, suggestion{
			row:    r,
			reason: "records a worktree that git no longer knows about",
			action: fmt.Sprintf("clear the stale worktree path %s", config.Contract(wtPath)),
			apply: func() error {
				if err := gitx.PruneWorktrees(ctx, t.RepoPath); err != nil {
					return err
				}
				t.WorktreePath = ""
				if t.State == task.Hot || t.State == task.Warm {
					t.State = task.Cold
				}
				return app.Tasks.Save(t)
			},
		})
	}
	return out
}

func confirm(app *App, in *bufio.Reader, action string) bool {
	s := app.outStyle()
	prompt := s.prompt(action + "?")
	lower := strings.ToLower(action)
	if strings.Contains(lower, "delete") || strings.Contains(lower, "remove") ||
		strings.Contains(lower, "discard") || strings.Contains(lower, "drop") {
		prompt = s.danger(action + "?")
	}
	fmt.Fprintf(app.Out, "  %s %s ", prompt, s.dim("[y/N]"))
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
