package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newStartCmd(app *App) *cobra.Command {
	var (
		name        string
		branch      string
		base        string
		noWorktree  bool
		noProvision bool
		focus       bool
		next        string
	)
	cmd := &cobra.Command{
		Use:   "start [repo]",
		Short: "Begin a change stream: branch, worktree, runtime session and task entry",
		Long: `Start a new change stream.

One command does the four things that otherwise drift apart: it creates the
branch, creates a worktree for it at the configured path, opens a runtime
session on that checkout, and records the task so the work survives closing
the session.

With no repo argument, the repository containing the current directory is used.

Always pass --base for anything an agent runs unattended. Without it a new
branch starts from the current HEAD, so starting a task while standing on
feature/A silently builds on feature/A rather than on main.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()

			// Resolve the repository: an explicit argument, else the cwd.
			var repoPath, repoName, category string
			if len(args) == 1 {
				r, _, err := repo.Resolve(ctx, app.Cfg.ScanRoots(), args[0])
				if err != nil {
					return err
				}
				repoPath, repoName, category = r.Path, r.Name, r.Category
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				g, err := gitx.Discover(ctx, cwd)
				if err != nil {
					return fmt.Errorf("no repo argument and %s is not a git repository", config.Contract(cwd))
				}
				repoPath, repoName = g.MainRoot, g.Name
			}

			// Derive whichever of task-name and branch was not given.
			if name == "" && branch == "" {
				return errors.New("give the work a name: dev start <repo> --task <name>")
			}
			if branch == "" {
				branch = "feat/" + config.Slug(name)
			}
			if name == "" {
				name = branch
			}
			if base == "" {
				base = gitx.DefaultBranch(ctx, repoPath)
			}

			id := task.MakeID(repoName, branch)
			if existing, err := app.Tasks.Get(id); err == nil {
				return fmt.Errorf("task %s already exists (state %s) — use `dev resume %s`",
					existing.ID, existing.State, existing.ID)
			}

			t := &task.Task{
				Name:     name,
				Repo:     repoName,
				RepoPath: repoPath,
				Branch:   branch,
				Base:     base,
				State:    task.Hot,
				Owner:    config.Hostname(),
				Next:     next,
			}

			rt := app.Runtime()
			if noWorktree {
				// Working directly in the main checkout: create the branch
				// there and open a session on it.
				if !gitx.BranchExists(ctx, repoPath, branch) {
					if _, err := gitx.Run(ctx, repoPath, "branch", branch, base); err != nil {
						return err
					}
				}
				handle, err := rt.Open(ctx, repoPath, repoName+"/"+branch)
				if err != nil {
					app.warnf("could not open a runtime session: %v", err)
				}
				if rt.Name() != "none" {
					t.RuntimeHandle = handle
				}
			} else {
				m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
				res, err := m.Create(ctx, wt.CreateRequest{
					RepoPath:    repoPath,
					RepoName:    repoName,
					Branch:      branch,
					Base:        base,
					Category:    category,
					Label:       name,
					NoProvision: noProvision,
					Focus:       focus,
				})
				if err != nil {
					var exists *wt.ErrExists
					if errors.As(err, &exists) {
						return fmt.Errorf("%w\nreuse it with: dev resume %s", err, id)
					}
					return err
				}
				t.WorktreePath = res.Path
				t.RuntimeHandle = res.RuntimeHandle
				reportProvision(app, res)
			}

			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			annotate(app, rt, t)

			fmt.Fprintf(app.Out, "%s %s  %s on %s\n", task.Hot.Icon(), t.Name, t.Repo, t.Branch)
			if t.WorktreePath != "" {
				fmt.Fprintf(app.Out, "   worktree  %s\n", config.Contract(t.WorktreePath))
			}
			if t.RuntimeHandle != "" && rt.Name() != "none" {
				fmt.Fprintf(app.Out, "   session   %s %s\n", rt.Name(), t.RuntimeHandle)
			}
			if rt.Name() == "none" {
				app.cdDirective(checkoutOf(t))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "task", "t", "", "human name for this change stream")
	f.StringVarP(&branch, "branch", "b", "", "branch name (default: feat/<task-slug>)")
	f.StringVar(&base, "base", "", "ref the branch starts from (default: the repo's default branch)")
	f.StringVar(&next, "next", "", "the first next action to record")
	f.BoolVar(&noWorktree, "no-worktree", false, "work in the main checkout instead of a worktree")
	f.BoolVar(&noProvision, "no-provision", false, "skip dependency install and gitignored-file copying")
	f.BoolVar(&focus, "focus", false, "focus the new runtime session")
	return cmd
}

// checkoutOf is where a task's code lives right now.
func checkoutOf(t *task.Task) string {
	if t.WorktreePath != "" {
		return t.WorktreePath
	}
	return t.RepoPath
}

// annotate pushes the task's state and next action into the runtime as
// display-only metadata, so a sidebar can show why a session is open without
// anyone having to ask dev.
//
// For herdr this lands as workspace metadata tokens. A token only renders if
// the sidebar row layout in ~/.config/herdr/config.toml names it, e.g.
//
//	[ui.sidebar.spaces]
//	rows = [["state_icon", "workspace", "$stage"], ["branch", "git_status"], ["$next"]]
//
// Setting a token with no matching layout entry succeeds and shows nothing,
// which is why dev never treats a failure here as an error.
func annotate(app *App, rt runtime.Runtime, t *task.Task) {
	if t.RuntimeHandle == "" {
		return
	}
	kv := map[string]string{
		"stage": t.State.Label(),
		"next":  t.Next,
	}
	if err := rt.Annotate(ctxOf(), t.RuntimeHandle, kv); err != nil {
		app.warnf("could not annotate the runtime session: %v", err)
	}
}

func reportProvision(app *App, res *wt.CreateResult) {
	p := res.Provision
	if n := len(p.Copied); n > 0 {
		fmt.Fprintf(app.Err, "   copied    %d gitignored file(s): %s\n", n, strings.Join(p.Copied, ", "))
	}
	if n := len(p.Linked); n > 0 {
		fmt.Fprintf(app.Err, "   linked    %s\n", strings.Join(p.Linked, ", "))
	}
	for _, e := range p.Failures {
		fmt.Fprintf(app.Err, "   warning   %v\n", e)
	}
}
