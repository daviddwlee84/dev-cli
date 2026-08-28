package cli

import (
	"fmt"
	"os"
	"path/filepath"
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
  Claude Code    harness-owned turn-scoped subagent isolation in
                 .claude/worktrees/ (keep that gitignored); not a history
                 relocation guarantee.
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
		newWtPlanCmd(app),
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

			t := app.newTable("", "BRANCH", "PATH", "GIT", "SESSION")
			style := app.outStyle()
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
				if session != "—" {
					session = style.success(session)
				}
				t.Add(marker, branch, config.Contract(w.Path), style.git(gitCol), session)
			}
			t.Render(app.Out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
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
Without --base, dev uses the repository's default branch. Pass it explicitly
for unattended use so the intended committed starting point is visible in the
command.`,
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
			if res.Runtime.Handle != "" && res.RuntimeName != "none" {
				fmt.Fprintf(app.Out, "   session   %s %s\n", res.RuntimeName, res.Runtime.Handle)
			}
			if track {
				fmt.Fprintln(app.Err, "note: `dev start` also records a task, which is what makes the work survive closing the session")
			}
			if rt.Name() == "none" {
				return app.cdDirective(res.Path)
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
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
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
			opened, err := openCheckout(ctx, rt, w.Path, repoName+"/"+w.Branch)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s", w.Branch, config.Contract(w.Path))
			if rt.Name() != "none" {
				fmt.Fprintf(app.Out, "  (%s %s)", rt.Name(), opened.Handle)
			}
			fmt.Fprintln(app.Out)
			if rt.Name() == "none" {
				return app.cdDirective(w.Path)
			}
			return activateRuntime(ctx, rt, opened.Handle)
		},
	}
	cmd.Flags().StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	cmd.ValidArgsFunction = completeWorktrees(app, true)
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
				t.WorktreePath = ""
				clearTaskRuntime(t)
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
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	cmd.ValidArgsFunction = completeWorktrees(app, false)
	return cmd
}

func newWtProvisionCmd(app *App) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "provision [path]",
		Short: "Re-run provisioning for an existing worktree",
		Long: `Bring an existing checkout up to a working state again.

Copies the gitignored files, applies the dependency strategy and runs the
post-create commands — useful after adding a new .env, or when a worktree was
created with --no-provision.

Use "dev wt plan" to see what it would do first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			target, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				target = config.Expand(args[0])
			}
			g, err := gitx.Discover(ctx, target)
			if err != nil {
				return err
			}

			set := wt.SettingsFor(app.Cfg, g.MainRoot)
			plan := wt.BuildPlan(ctx, set, g.MainRoot)
			if dryRun {
				renderPlan(app, plan, g.MainRoot)
				return nil
			}

			p := &wt.Provisioner{
				Settings: set,
				Timeout:  app.Cfg.Worktree.ProvisionTimeout.Duration,
				Log:      app.Err,
			}
			res, err := p.Apply(ctx, plan, g.MainRoot, g.Root)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "provisioned %s\n", config.Contract(g.Root))
			if len(res.Copied) > 0 {
				fmt.Fprintf(app.Out, "  copied   %s\n", strings.Join(res.Copied, ", "))
			}
			if len(res.Linked) > 0 {
				fmt.Fprintf(app.Out, "  linked   %s\n", strings.Join(res.Linked, ", "))
			}
			if len(res.Ran) > 0 {
				fmt.Fprintf(app.Out, "  ran      %s\n", strings.Join(res.Ran, ", "))
			}
			for _, s := range res.Skipped {
				fmt.Fprintf(app.Err, "  skipped  %s\n", s)
			}
			for _, e := range res.Failures {
				app.warnf("%v", e)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan instead of applying it")
	return cmd
}

func newWtPlanCmd(app *App) *cobra.Command {
	var (
		repoRef string
		write   bool
	)
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what a new worktree of this repo would be provisioned with",
		Long: `Report the project types detected in this repository and exactly what
provisioning a new worktree would do — which gitignored files get copied,
which dependency directories are copied or shared, and which install commands
run.

A worktree that comes up broken is the commonest reason people abandon
worktrees, and the config alone cannot answer "what will this actually do":
that depends on which lockfiles exist, which tools are installed here, and
which files are genuinely gitignored. This computes all of it and changes
nothing.

With --write, seed a .dev.toml from what was detected, so the repository can
commit its own setup and every machine gets the same one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			repoPath, _, err := repoContext(app, repoRef)
			if err != nil {
				return err
			}
			set := wt.SettingsFor(app.Cfg, repoPath)
			plan := wt.BuildPlan(ctx, set, repoPath)

			if write {
				return writeRepoTemplate(app, repoPath, plan, set)
			}
			renderPlan(app, plan, repoPath)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&repoRef, "repo", "r", "", "repository (default: the current one)")
	f.BoolVar(&write, "write", false, "seed a .dev.toml in the repository from what was detected")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	return cmd
}

// renderPlan prints a provisioning plan as a table plus its warnings.
func renderPlan(app *App, plan wt.Plan, repoPath string) {
	fmt.Fprintf(app.Out, "%s\n\n", config.Contract(repoPath))

	if len(plan.Ecosystems) == 0 {
		fmt.Fprintln(app.Out, "No project type detected — only gitignored files will be carried over.")
	} else {
		t := app.newTable("PROJECT", "MANAGER", "FROM", "DEPENDENCIES", "TOOL")
		style := app.outStyle()
		for _, e := range plan.Ecosystems {
			deps := "—"
			if len(e.DepDirs) > 0 {
				deps = strings.Join(e.DepDirs, ", ")
			} else {
				deps = "global cache"
			}
			tool := "installed"
			if !e.ToolInstalled() {
				tool = style.danger("MISSING: " + e.Tool)
			} else {
				tool = style.success(tool)
			}
			t.Add(e.Name, e.Manager, e.Marker, deps, tool)
		}
		t.Render(app.Out)
	}

	fmt.Fprintln(app.Out)
	if len(plan.Steps) == 0 {
		fmt.Fprintln(app.Out, "Nothing to provision.")
	} else {
		st := app.newTable("", "ACTION", "WHAT", "WHY")
		style := app.outStyle()
		for _, s := range plan.Steps {
			mark := "✓"
			if s.Skipped {
				mark = style.dim("·")
			} else {
				mark = style.success(mark)
			}
			st.Add(mark, string(s.Kind), truncate(s.What, 34), s.Why)
		}
		st.Render(app.Out)
	}

	for _, w := range plan.Warnings {
		fmt.Fprintf(app.Err, "\n%s %s\n", app.errStyle().warning("warning:"), w)
	}
	if plan.Empty() {
		fmt.Fprintln(app.Err, "\nNothing would run. If a new worktree comes up broken, add what it needs to "+
			"worktree.post_create, or commit a .dev.toml with `dev wt plan --write`.")
	}
}

// writeRepoTemplate seeds a .dev.toml from the detected plan.
//
// Committing it is what makes a project's worktree setup reproducible: the
// next machine, and the next person, get the same one without having to
// rediscover it.
func writeRepoTemplate(app *App, repoPath string, plan wt.Plan, set wt.Settings) error {
	path := filepath.Join(repoPath, wt.OverrideFilename)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists — edit it directly", config.Contract(path))
	}

	var b strings.Builder
	b.WriteString("# dev worktree setup for this repository.\n")
	b.WriteString("#\n")
	b.WriteString("# Committed on purpose: a worktree is a clean checkout, so without this a\n")
	b.WriteString("# new one comes up with no dependencies and none of the gitignored files the\n")
	b.WriteString("# project needs. Seeded by `dev wt plan --write`; edit freely.\n\n")
	b.WriteString("[worktree]\n")

	b.WriteString("# Gitignored files to carry into a new worktree. Only files that are BOTH\n")
	b.WriteString("# listed here AND gitignored are copied — a tracked file is already in the\n")
	b.WriteString("# checkout on the correct branch.\n")
	b.WriteString("include = [")
	for i, inc := range set.Include {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", inc)
	}
	b.WriteString("]\n\n")

	if len(plan.Ecosystems) > 0 {
		b.WriteString("# Detected in this repository:\n")
		for _, e := range plan.Ecosystems {
			fmt.Fprintf(&b, "#   %-8s %-8s (%s) → %s\n", e.Name, e.Manager, e.Marker, e.Install)
		}
		b.WriteString("#\n")
		b.WriteString("# \"auto\" runs those install commands. Replace with an explicit list if this\n")
		b.WriteString("# project needs something else:  post_create = [\"make bootstrap\"]\n")
		b.WriteString("post_create = \"auto\"\n\n")

		b.WriteString("# How each project type gets its dependencies:\n")
		b.WriteString("#   reinstall  run the install command (correct, the default)\n")
		b.WriteString("#   copy       duplicate the directory from the source checkout (fast)\n")
		b.WriteString("#   link       share one directory between checkouts (fastest, risky)\n")
		b.WriteString("#   skip       leave the worktree without dependencies\n")
		b.WriteString("[worktree.strategies]\n")
		for _, e := range plan.Ecosystems {
			switch {
			case len(e.DepDirs) == 0:
				fmt.Fprintf(&b, "# %-8s = \"reinstall\"  # %s uses a global cache; nothing to copy\n",
					e.Name, e.Manager)
			case e.CopySafe:
				fmt.Fprintf(&b, "%-10s = \"reinstall\"  # \"copy\" is sound here and much faster\n", e.Name)
			default:
				fmt.Fprintf(&b, "%-10s = \"reinstall\"  # copy/link unsupported: %s\n", e.Name, e.Hazard)
			}
		}
	} else {
		b.WriteString("# No project type detected. Add the commands a fresh checkout needs:\n")
		b.WriteString("# post_create = [\"make bootstrap\"]\n")
		b.WriteString("post_create = \"auto\"\n")
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "wrote %s\n", config.Contract(path))
	fmt.Fprintln(app.Out, "Commit it so every machine provisions worktrees the same way.")
	return nil
}
