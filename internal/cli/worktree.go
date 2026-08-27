package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

const worktreeLong = `Manage the linked worktrees dev owns.

Who owns which worktree — the rule dev encodes, so nobody has to improvise:

  dev            anything you might come back to tomorrow: features, fixes,
                 experiments, cross-machine handoffs. Placed at
                 paths.worktree_path, outside the repo.
  Claude Code    turn-scoped subagent isolation that dies with the turn, in
                 .claude/worktrees/ (keep that gitignored).
  herdr          dev does not call "herdr worktree create". It creates the
                 worktree with git and asks herdr to open it, so the path
                 policy holds on machines without herdr too.

A new worktree is a clean checkout: no node_modules, no .venv, none of the
gitignored env files the project needs. dev provisions it — see the config's
[worktree] section, or a repo's own .dev.toml.`

func newWorktreeCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "wt",
		Aliases: []string{"worktree"},
		Short:   "Create, list, open and remove worktrees",
		Long:    worktreeLong,
	}
	cmd.AddCommand(
		newWtListCmd(app),
		newWtCreateCmd(app),
		newWtOpenCmd(app),
		newWtRemoveCmd(app),
		newWtProvisionCmd(app),
	)
	return cmd
}

// repoContext resolves the repository to operate on: an explicit --repo, else
// the current directory.
func repoContext(app *App, ref string) (path, name string, err error) {
	ctx := ctxOf()
	if ref != "" {
		r, _, err := resolveRepoRef(app, ref)
		if err != nil {
			return "", "", err
		}
		return r.Path, r.Name, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	g, err := gitx.Discover(ctx, cwd)
	if err != nil {
		return "", "", fmt.Errorf("%s is not a git repository — pass --repo",
			config.Contract(cwd))
	}
	return g.MainRoot, g.Name, nil
}

func newWtListCmd(app *App) *cobra.Command {
	var repoRef string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the worktrees of a repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			repoPath, _, err := repoContext(app, repoRef)
			if err != nil {
				return err
			}
			list, err := gitx.Worktrees(ctx, repoPath)
			if err != nil {
				return err
			}
			rt := app.Runtime()
			sessions, _ := rt.List(ctx)

			t := NewTable("", "BRANCH", "PATH", "GIT", "SESSION")
			for _, w := range list {
				marker := "  "
				if w.Main {
					marker = "★ " // the canonical checkout
				}
				branch := w.Branch
				if branch == "" {
					branch = "(detached)"
				}
				gitCol := "—"
				if st, err := gitx.StatusOf(ctx, w.Path); err == nil {
					gitCol = st.Summary()
				}
				switch {
				case w.Prunable:
					gitCol = "prunable"
				case w.Locked:
					gitCol = "locked"
				}
				session := "—"
				for _, s := range sessions {
					for _, d := range s.Dirs {
						if d == w.Path {
							session = rt.Name() + " " + s.Handle
						}
					}
				}
				t.Add(marker, branch, config.Contract(w.Path), gitCol, session)
			}
			t.Render(app.Out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	return cmd
}

func newWtCreateCmd(app *App) *cobra.Command {
	var (
		repoRef     string
		base        string
		path        string
		label       string
		noProvision bool
		noRuntime   bool
		track       bool
	)
	cmd := &cobra.Command{
		Use:   "create <branch>",
		Short: "Create a worktree at the configured path and provision it",
		Long: `Create a linked worktree for a branch.

If the branch exists it is checked out; otherwise it is created from --base.
Always pass --base for unattended use: without it a new branch starts from the
current HEAD, so creating a worktree while standing on feature/A silently
builds on feature/A.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			repoPath, repoName, err := repoContext(app, repoRef)
			if err != nil {
				return err
			}
			branch := args[0]
			if base == "" {
				base = gitx.DefaultBranch(ctx, repoPath)
			}

			rt := app.Runtime()
			m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
			res, err := m.Create(ctx, wt.CreateRequest{
				RepoPath:    repoPath,
				RepoName:    repoName,
				Branch:      branch,
				Base:        base,
				Path:        path,
				Label:       label,
				NoProvision: noProvision,
				NoRuntime:   noRuntime,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s\n", branch, config.Contract(res.Path))
			reportProvision(app, res)
			if res.RuntimeHandle != "" {
				fmt.Fprintf(app.Out, "   session   %s %s\n", res.RuntimeName, res.RuntimeHandle)
			}
			if track {
				fmt.Fprintln(app.Err, "note: `dev start` also records a task, which is what makes the work survive closing the session")
			}
			if rt.Name() == "none" {
				app.cdDirective(res.Path)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	f.StringVar(&base, "base", "", "ref a new branch starts from")
	f.StringVar(&path, "path", "", "override the templated location")
	f.StringVar(&label, "label", "", "runtime session label")
	f.BoolVar(&noProvision, "no-provision", false, "skip dependency install and gitignored-file copying")
	f.BoolVar(&noRuntime, "no-session", false, "do not open a runtime session")
	f.BoolVar(&track, "track", true, "hint about recording a task")
	_ = f.MarkHidden("track")
	return cmd
}

func newWtOpenCmd(app *App) *cobra.Command {
	var repoRef string
	cmd := &cobra.Command{
		Use:   "open <branch>",
		Short: "Open an existing worktree in the runtime",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			repoPath, repoName, err := repoContext(app, repoRef)
			if err != nil {
				return err
			}
			w, ok, err := gitx.WorktreeFor(ctx, repoPath, args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no worktree for branch %q — create one with `dev wt create %s`", args[0], args[0])
			}
			rt := app.Runtime()
			handle, err := openCheckout(ctx, rt, w.Path, repoName+"/"+w.Branch)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s", w.Branch, config.Contract(w.Path))
			if rt.Name() != "none" {
				fmt.Fprintf(app.Out, "  (%s %s)", rt.Name(), handle)
			}
			fmt.Fprintln(app.Out)
			if rt.Name() == "none" {
				app.cdDirective(w.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	return cmd
}

func newWtRemoveCmd(app *App) *cobra.Command {
	var (
		repoRef string
		force   bool
	)
	cmd := &cobra.Command{
		Use:     "rm <branch>",
		Aliases: []string{"remove"},
		Short:   "Remove a worktree checkout (never the branch)",
		Long: `Remove a worktree's checkout.

The branch is never deleted: removing a checkout and abandoning a change
stream are different decisions, and conflating them is how work gets lost.
A checkout with uncommitted changes needs an explicit --force.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			repoPath, _, err := repoContext(app, repoRef)
			if err != nil {
				return err
			}
			w, ok, err := gitx.WorktreeFor(ctx, repoPath, args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no worktree for branch %q", args[0])
			}
			if w.Main {
				return fmt.Errorf("%s is the main checkout, not a linked worktree", config.Contract(w.Path))
			}
			if dirty, st, err := wt.DirtyCheck(ctx, w.Path); err == nil && dirty && !force {
				return fmt.Errorf("%s has uncommitted changes (%s).\n"+
					"Commit them, or re-run with --force to discard them",
					config.Contract(w.Path), st.Summary())
			}

			// Close a runtime session pointing at the checkout first, so the
			// multiplexer is not left on a deleted directory.
			rt := app.Runtime()
			var handle string
			if sessions, err := rt.List(ctx); err == nil {
				for _, s := range sessions {
					for _, d := range s.Dirs {
						if d == w.Path {
							handle = s.Handle
						}
					}
				}
			}
			m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
			if err := m.Remove(ctx, wt.RemoveRequest{
				RepoPath: repoPath, Path: w.Path, Force: force, RuntimeHandle: handle,
			}); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "removed %s (branch %s kept)\n", config.Contract(w.Path), w.Branch)

			// Keep the registry honest about what is now gone.
			if t, err := app.Tasks.FindByWorktree(w.Path); err == nil {
				t.WorktreePath, t.RuntimeHandle = "", ""
				if err := app.Tasks.Save(t); err == nil {
					fmt.Fprintf(app.Out, "task %s updated — the branch still has the work\n", t.ID)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	f.BoolVarP(&force, "force", "f", false, "remove even with uncommitted changes")
	return cmd
}

func newWtProvisionCmd(app *App) *cobra.Command {
	var repoRef string
	cmd := &cobra.Command{
		Use:   "provision [path]",
		Short: "Re-run provisioning for an existing worktree",
		Long: `Copy gitignored files, create the configured symlinks and run the post-create
commands again for a checkout — useful after adding a new .env, or when a
worktree was created with --no-provision.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			target := ""
			if len(args) == 1 {
				target = config.Expand(args[0])
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				target = cwd
			}
			g, err := gitx.Discover(ctx, target)
			if err != nil {
				return err
			}
			p := &wt.Provisioner{
				Include: app.Cfg.Worktree.Include,
				Link:    app.Cfg.Worktree.Link,
				Cmds:    app.Cfg.Worktree.PostCreate,
				Timeout: app.Cfg.Worktree.ProvisionTimeout.Duration,
				Log:     app.Err,
			}
			if o, ok := wt.LoadRepoOverride(g.MainRoot); ok {
				p.ApplyOverride(o)
			}
			res, err := p.Provision(ctx, g.MainRoot, g.Root)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "provisioned %s\n", config.Contract(g.Root))
			if len(res.Copied) > 0 {
				fmt.Fprintf(app.Out, "  copied  %s\n", strings.Join(res.Copied, ", "))
			}
			if len(res.Ran) > 0 {
				fmt.Fprintf(app.Out, "  ran     %s\n", strings.Join(res.Ran, ", "))
			}
			for _, e := range res.Failures {
				app.warnf("%v", e)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	_ = cmd.Flags().MarkHidden("repo")
	return cmd
}
