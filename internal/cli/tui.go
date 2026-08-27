package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newTUICmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive dashboard over the inventory",
		Long: `Browse and act on your work in progress.

Shows exactly what "dev ls" shows, from the same code path, plus the ability
to open, park and annotate a task without retyping its name.

Two lists, switched with tab:

  TASKS   change streams dev is tracking — what am I working on
  REPOS   every repository under the scan roots — what do I have here

Navigation is vim-style, with arrows alongside:

  j k        move                 ctrl+d ctrl+u   half a page
  g G        top / bottom         h l / tab       previous / next view
  /          filter as you type   esc             clear, then quit

Actions depend on the list:

  enter      open in the runtime
  p          park a task          c    edit its next action
  s          start a task in the selected repository
  1 2 3      hot / warm / cold    0    clear filters    a  include done
  r          refresh              q    quit

External tools are configured, not fixed — see [[tui.tools]] in the config,
and "dev tui tools" for what is bound here. They run in the selected row's checkout;
the dashboard suspends while they do and redraws afterwards, and bindings for
programs that are not installed are not offered.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(app)
		},
	}
	cmd.AddCommand(newTUIToolsCmd(app))
	return cmd
}

func newTUIToolsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List the external tool bindings and whether each one works here",
		Long: `Show which programs the dashboard will hand the terminal to.

Bindings come from [[tui.tools]] in the config; with none configured, the
built-in set applies. A configured list replaces the defaults entirely, so
what is bound is always exactly what is written down.

Add your own — an editor, a script, anything you already reach for:

    [[tui.tools]]
    key  = "V"
    name = "nvim"
    run  = "nvim ."

    [[tui.tools]]
    key  = "B"
    name = "vibe"
    run  = "vibe"

The command runs through your shell in the selected row's checkout, so
arguments, environment variables and your own scripts all behave as typed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configured := len(app.Cfg.TUI.Tools) > 0
			source := "built-in defaults"
			if configured {
				source = config.Contract(app.Cfg.Source)
			}
			fmt.Fprintf(app.Out, "tool bindings from %s\n\n", source)

			t := NewTable("KEY", "NAME", "RUNS", "AVAILABLE")
			for _, tool := range externalTools(app) {
				status := "yes"
				if tool.Available != nil && !tool.Available() {
					status = "no — not on PATH"
				}
				// Command is [shell, -c, run]; show the run string itself.
				run := tool.Command[len(tool.Command)-1]
				t.Add(tool.Key, tool.Name, run, status)
			}
			t.Render(app.Out)

			if !configured {
				fmt.Fprintln(app.Out, "\nOverride them with [[tui.tools]] in your config; see \"dev tui tools --help\".")
			}
			fmt.Fprintln(app.Err, "\nA tool cannot take a key the dashboard already uses; dev reports the clash on load.")
			return nil
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
	reloadRepos := func(ctx context.Context) ([]tui.RepoRow, error) {
		return collectRepos(ctx, app, rt)
	}

	rows, err := reload(ctx)
	if err != nil {
		return err
	}
	repos, err := reloadRepos(ctx)
	if err != nil {
		app.warnf("could not list repositories: %v", err)
	}

	actions := tui.Actions{
		Reload:      reload,
		ReloadRepos: reloadRepos,
		Runtime:     rt,
		Tools:       externalTools(app),

		// Open reuses the same paths the commands take, so a cold task
		// selected here is rebuilt rather than reported broken.
		Open: func(ctx context.Context, t *task.Task) (string, error) {
			checkout := checkoutOf(t)
			if _, err := os.Stat(checkout); err != nil {
				return "", fmt.Errorf("%s has no checkout — run `dev resume %s`", t.Title(), t.ID)
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

		OpenRepo: func(ctx context.Context, r tui.RepoRow) (string, error) {
			handle, err := openCheckout(ctx, rt, r.Repo.Path, r.Repo.Name)
			if err != nil {
				return "", err
			}
			if rt.Name() == "none" {
				return "", nil
			}
			return fmt.Sprintf("%s open in %s (%s)", r.Repo.Name, rt.Name(), handle), nil
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
			// The dashboard only parks warm: going cold removes a checkout,
			// which is too consequential for a single keystroke.
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

		// Start is the bridge from "here are my repositories" to "here is what
		// I am working on" — the step that otherwise means dropping out of the
		// dashboard to type a command.
		Start: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			branch := "feat/" + config.Slug(name)
			base := gitx.DefaultBranch(ctx, r.Repo.Path)
			id := task.MakeID(r.Repo.Name, branch)
			if _, err := app.Tasks.Get(id); err == nil {
				return "", fmt.Errorf("%s already exists", id)
			}

			m := &wt.Manager{Cfg: app.Cfg, Runtime: rt}
			res, err := m.Create(ctx, wt.CreateRequest{
				RepoPath: r.Repo.Path, RepoName: r.Repo.Name, Branch: branch,
				Base: base, Category: r.Repo.Category, Label: name,
			})
			if err != nil {
				return "", err
			}
			t := &task.Task{
				Name: name, Repo: r.Repo.Name, RepoPath: r.Repo.Path,
				Branch: branch, Base: base, WorktreePath: res.Path,
				State: task.Hot, Owner: config.Hostname(), RuntimeHandle: res.RuntimeHandle,
			}
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			annotate(app, rt, t)
			return fmt.Sprintf("started %s on %s", name, branch), nil
		},
	}

	model := tui.New(actions, rows, repos)
	final, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	// A directory choice can only be honoured once the alternate screen is
	// torn down, and only by the shell wrapper.
	if m, ok := final.(tui.Model); ok {
		if dir := m.Chosen(); dir != "" {
			app.cdDirective(dir)
		}
	}
	return nil
}

// collectRepos builds the repository view: what exists, plus how much of it is
// in flight.
func collectRepos(ctx context.Context, app *App, rt runtime.Runtime) ([]tui.RepoRow, error) {
	repos, err := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
	if err != nil {
		return nil, err
	}
	tasks, err := app.Tasks.List()
	if err != nil {
		return nil, err
	}
	byRepo := map[string][]*task.Task{}
	for _, t := range tasks {
		byRepo[t.RepoPath] = append(byRepo[t.RepoPath], t)
	}
	sessions, _ := rt.List(ctx)

	out := make([]tui.RepoRow, 0, len(repos))
	for _, r := range repos {
		row := tui.RepoRow{Repo: r, Tasks: byRepo[r.Path]}
		if !r.Bare {
			if st, err := gitx.StatusOf(ctx, r.Path); err == nil {
				row.Status = st
			}
			if list, err := gitx.Worktrees(ctx, r.Path); err == nil && len(list) > 0 {
				row.Worktrees = len(list) - 1
			}
		}
		for _, s := range sessions {
			if s.Covers(r.Path) {
				row.Live = true
				break
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// externalTools resolves the configured tool bindings.
//
// Configurable rather than fixed because which program you reach for is
// personal, and because the useful ones are often your own scripts and
// aliases, not just the well-known tools.
func externalTools(app *App) []tui.Tool {
	var out []tui.Tool
	for _, t := range app.Cfg.EffectiveTools() {
		run := t.Run
		name := t.Name
		if name == "" {
			name = firstWord(run)
		}
		out = append(out, tui.Tool{
			Key:  t.Key,
			Name: name,
			// Run through a shell so aliases-as-scripts, arguments and
			// environment variables in the command all behave as typed.
			Command:   []string{shellPath(), "-c", run},
			Available: commandRunnable(run),
		})
	}
	return out
}

// commandRunnable resolves the command's first word on PATH, expanding a
// leading environment variable so "$EDITOR ." checks the editor itself.
func commandRunnable(run string) func() bool {
	return func() bool {
		word := firstWord(run)
		if strings.HasPrefix(word, "$") {
			word = os.Getenv(strings.TrimPrefix(word, "$"))
			if word == "" {
				return false
			}
			word = firstWord(word)
		}
		if word == "" {
			return false
		}
		if filepath.IsAbs(word) {
			_, err := os.Stat(word)
			return err == nil
		}
		_, err := exec.LookPath(word)
		return err == nil
	}
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}
