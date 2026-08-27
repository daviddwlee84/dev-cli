package cli

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive dashboard over the inventory",
		Long: `Browse and act on your work in progress.

Shows exactly what "dev ls" shows, from the same code path, plus the ability
to open, park and annotate a task without retyping its name.

  ↑↓ / jk   move          enter / o   open in the runtime
  p         park          n           edit the next action
  1 2 3     hot/warm/cold 0           clear the filter
  a         include done  r           refresh          q  quit`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(app)
		},
	}
}

// interactive reports whether a real terminal is attached. Piping `dev` into
// anything must produce the plain listing, not terminal control sequences.
func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func runTUI(app *App) error {
	ctx := ctxOf()
	rt := app.Runtime()

	reload := func(ctx context.Context) ([]inventory.Row, error) {
		tasks, err := app.Tasks.List()
		if err != nil {
			return nil, err
		}
		return inventory.Collect(ctx, tasks, rt, inventory.Options{}), nil
	}

	rows, err := reload(ctx)
	if err != nil {
		return err
	}

	actions := tui.Actions{
		Reload:  reload,
		Runtime: rt,

		// Open reuses the same worktree-aware path resume does, so a cold task
		// selected in the dashboard is rebuilt rather than reported broken.
		Open: func(ctx context.Context, t *task.Task) (string, error) {
			checkout := checkoutOf(t)
			if _, err := os.Stat(checkout); err != nil {
				return "", fmt.Errorf("%s has no checkout — run `dev resume %s`",
					t.Title(), t.ID)
			}
			handle, err := openCheckout(ctx, rt, checkout, t.Title())
			if err != nil {
				return "", err
			}
			t.State, t.RuntimeHandle, t.Owner = task.Hot, handle, config.Hostname()
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			annotate(app, rt, t)
			if rt.Name() == "none" {
				return "", nil
			}
			return fmt.Sprintf("%s open in %s (%s)", t.Title(), rt.Name(), handle), nil
		},

		Park: func(ctx context.Context, t *task.Task, next string) (string, error) {
			if next != "" {
				t.Next = next
			}
			if t.RuntimeHandle != "" {
				if err := rt.Close(ctx, t.RuntimeHandle); err != nil {
					return "", err
				}
				t.RuntimeHandle = ""
			}
			// The dashboard only ever parks warm: going cold removes a
			// checkout, which is too consequential for a single keystroke.
			t.State, t.Owner = task.Warm, config.Hostname()
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			return t.Title() + " parked warm — worktree and branch kept", nil
		},

		SetNext: func(ctx context.Context, t *task.Task, next string) error {
			t.Next = next
			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			annotate(app, rt, t)
			return nil
		},
	}

	model := tui.New(actions, rows)
	final, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	// A directory choice can only be honoured after the alternate screen is
	// torn down, and only by the shell wrapper.
	if m, ok := final.(tui.Model); ok {
		if dir := m.Chosen(); dir != "" {
			app.cdDirective(dir)
		}
	}
	return nil
}
