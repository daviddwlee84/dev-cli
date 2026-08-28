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
		direct      bool
		branchOnly  bool
		noWorktree  bool // deprecated spelling of branch-only
		noProvision bool
		focus       bool
		next        string
	)
	cmd := &cobra.Command{
		Use:   "start [repo]",
		Short: "Track work directly, on a canonical branch, or in an isolated worktree",
		Long: `Start a tracked change stream in one of three explicit modes:

  default        create a branch + linked worktree, provision it, open a
                 runtime session, and record the task. Safest for work that
                 may be interrupted or run in parallel.
  --branch-only  create/switch a branch in the canonical checkout, with no
                 worktree. Lighter, but that checkout cannot host another
                 branch concurrently.
  --direct       track the branch already checked out in the canonical repo —
                 usually main — without creating either a branch or worktree.
                 Best for one-session ad-hoc changes.

For untracked ad-hoc navigation, use "dev repo open" or press Enter in the
TUI's REPOS view; that opens the project without creating any task at all.

With no repo argument, the repository containing the current directory is used.
Always pass --base for an unattended branch/worktree task.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			if direct && (branchOnly || noWorktree) {
				return errors.New("--direct and --branch-only are different modes; pick one")
			}
			branchOnly = branchOnly || noWorktree

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

			if name == "" {
				if branch != "" && !direct {
					name = branch
				} else {
					return errors.New("give the work a name: dev start <repo> --task <name>")
				}
			}

			mode := task.ModeWorktree
			switch {
			case direct:
				mode = task.ModeDirect
				st, err := gitx.StatusOf(ctx, repoPath)
				if err != nil {
					return err
				}
				if st.Detached || st.Branch == "" {
					return errors.New("--direct needs a named branch; this checkout has detached HEAD")
				}
				if branch != "" && branch != st.Branch {
					return fmt.Errorf("--direct tracks the branch already checked out (%s), not --branch %s; "+
						"omit --branch or use --branch-only", st.Branch, branch)
				}
				branch, base = st.Branch, st.Branch

			case branchOnly:
				mode = task.ModeBranch
				if branch == "" {
					branch = "feat/" + config.Slug(name)
				}
				if base == "" {
					base = gitx.DefaultBranch(ctx, repoPath)
				}

			default:
				if branch == "" {
					branch = "feat/" + config.Slug(name)
				}
				if base == "" {
					base = gitx.DefaultBranch(ctx, repoPath)
				}
			}

			id := task.MakeID(repoName, branch)
			if existing, err := app.Tasks.Get(id); err == nil && existing.State != task.Done {
				return fmt.Errorf("task %s already exists (state %s) — use `dev resume %s`",
					existing.ID, existing.State, existing.ID)
			}

			t := &task.Task{
				Name: name, Repo: repoName, RepoPath: repoPath,
				Branch: branch, Base: base, Mode: mode,
				State: task.Hot, Owner: config.Hostname(), Next: next,
			}
			rt := app.Runtime()

			switch mode {
			case task.ModeDirect:
				handle, err := rt.Open(ctx, repoPath, repoName+"/"+name)
				if err != nil {
					app.warnf("could not open a runtime session: %v", err)
				} else if rt.Name() != "none" {
					t.RuntimeHandle = handle
				}

			case task.ModeBranch:
				if gitx.BranchExists(ctx, repoPath, branch) {
					if _, err := gitx.Run(ctx, repoPath, "switch", branch); err != nil {
						return fmt.Errorf("switch to %s: %w", branch, err)
					}
				} else {
					if _, err := gitx.Run(ctx, repoPath, "switch", "-c", branch, base); err != nil {
						return fmt.Errorf("create and switch to %s from %s: %w", branch, base, err)
					}
				}
				handle, err := rt.Open(ctx, repoPath, repoName+"/"+branch)
				if err != nil {
					app.warnf("could not open a runtime session: %v", err)
				} else if rt.Name() != "none" {
					t.RuntimeHandle = handle
				}

			case task.ModeWorktree:
				m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
				res, err := m.Create(ctx, wt.CreateRequest{
					RepoPath: repoPath, RepoName: repoName, Branch: branch, Base: base,
					Category: category, Label: name, NoProvision: noProvision, Focus: focus,
				})
				if err != nil {
					var exists *wt.ErrExists
					if errors.As(err, &exists) {
						return fmt.Errorf("%w\nreuse it with: dev resume %s", err, id)
					}
					return err
				}
				t.WorktreePath, t.RuntimeHandle = res.Path, res.RuntimeHandle
				reportProvision(app, res)
			}

			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			annotate(app, rt, t)

			fmt.Fprintf(app.Out, "%s %s  %s on %s (%s)\n",
				task.Hot.Icon(), t.Name, t.Repo, t.Branch, t.Mode)
			if t.WorktreePath != "" {
				fmt.Fprintf(app.Out, "   worktree  %s\n", config.Contract(t.WorktreePath))
			}
			if t.RuntimeHandle != "" {
				fmt.Fprintf(app.Out, "   session   %s %s\n", rt.Name(), t.RuntimeHandle)
			}
			if rt.Name() == "none" {
				return app.cdDirective(checkoutOf(t))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&name, "task", "t", "", "human name for this change stream")
	f.StringVarP(&branch, "branch", "b", "", "branch name (default: feat/<task-slug>)")
	f.StringVar(&base, "base", "", "ref a new branch starts from (default: repo default branch)")
	f.StringVar(&next, "next", "", "the first next action to record")
	f.BoolVar(&direct, "direct", false, "track work on the currently checked-out branch; create no branch/worktree")
	f.BoolVar(&branchOnly, "branch-only", false, "create/switch a branch in the canonical checkout; no worktree")
	f.BoolVar(&noWorktree, "no-worktree", false, "deprecated alias for --branch-only")
	_ = f.MarkDeprecated("no-worktree", "use --branch-only (or --direct to stay on main)")
	f.BoolVar(&noProvision, "no-provision", false, "skip dependency install and ignored-file copying")
	f.BoolVar(&focus, "focus", false, "focus the new runtime session")
	cmd.ValidArgsFunction = completeRepos(app)
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
