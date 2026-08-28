package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
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
		jsonOut     bool
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

In an interactive terminal, omitting the task name opens a context-aware
wizard for repo, task, mode, branch, base and next action. Defaults are shown
inline, and the final summary must be confirmed before anything is created.
Pipes and --json never prompt: pass --task for unattended use.

With no repo argument, the repository containing the current directory is used.
Always pass --base for an unattended branch/worktree task.

Herdr-aware direct/branch starts refuse a checkout occupied by another agent.
Use the root --allow-shared-checkout override only after coordinating disjoint
file ownership. --json emits one pure creation object; only a new first-class
Herdr worktree with its exact returned root pane is launchable. Worktree labels
use repo/branch so Herdr can show native nested repository provenance.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			if direct && (branchOnly || noWorktree) {
				return errors.New("--direct and --branch-only are different modes; pick one")
			}
			branchOnly = branchOnly || noWorktree
			mode := task.ModeWorktree
			switch {
			case direct:
				mode = task.ModeDirect
			case branchOnly:
				mode = task.ModeBranch
			}
			req := startRequest{
				Name: name, Branch: branch, Base: base, Next: next, Mode: mode,
				ModeExplicit:   direct || branchOnly,
				BranchExplicit: cmd.Flags().Changed("branch"),
				BaseExplicit:   cmd.Flags().Changed("base"),
				NextExplicit:   cmd.Flags().Changed("next"),
				NoProvision:    noProvision, Focus: focus,
			}
			if len(args) == 1 {
				req.RepoRef, req.RepoExplicit = args[0], true
			}
			if req.Name == "" && req.Branch != "" && req.Mode != task.ModeDirect {
				req.Name = req.Branch
			}

			var spec *startSpec
			var err error
			if req.Name == "" {
				if jsonOut || !app.interactive() {
					return errors.New("give the work a name: dev start <repo> --task <name>")
				}
				var confirmed bool
				spec, confirmed, err = runStartWizard(ctx, app, req)
				if errors.Is(err, errStartCanceled) {
					fmt.Fprintln(app.Out, "Canceled; nothing was created.")
					return nil
				}
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Fprintln(app.Out, "Canceled; nothing was created.")
					return nil
				}
			} else {
				spec, err = buildStartSpec(ctx, app, req)
				if err != nil {
					return decorateStartError(err)
				}
			}

			result, err := executeStartSpec(ctx, app, spec, app.Err)
			if err != nil {
				return decorateStartError(err)
			}
			if result.Worktree != nil {
				reportProvision(app, result.Worktree)
			}

			if jsonOut {
				return emitStartJSON(app, result.Task, result.Runtime.Name(), result.Opened)
			}
			fmt.Fprintf(app.Out, "%s %s  %s on %s (%s)\n",
				task.Hot.Icon(), result.Task.Name, result.Task.Repo, result.Task.Branch, result.Task.Mode)
			if result.Task.WorktreePath != "" {
				fmt.Fprintf(app.Out, "   worktree  %s\n", config.Contract(result.Task.WorktreePath))
			}
			if result.Task.RuntimeHandle != "" {
				fmt.Fprintf(app.Out, "   session   %s %s\n", result.Runtime.Name(), result.Task.RuntimeHandle)
			}
			if result.Runtime.Name() == "none" {
				return app.cdDirective(checkoutOf(result.Task))
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
	f.BoolVar(&jsonOut, "json", false, "emit one machine-readable creation result")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func decorateStartError(err error) error {
	var worktreeExists *wt.ErrExists
	if errors.As(err, &worktreeExists) {
		return fmt.Errorf("%w\nopen it with: dev wt open %s", err, worktreeExists.Branch)
	}
	return err
}

type startRuntimeJSON struct {
	Name       string `json:"name"`
	Handle     string `json:"handle"`
	Surface    string `json:"surface"`
	Opened     bool   `json:"opened"`
	Created    bool   `json:"created"`
	RootPaneID string `json:"root_pane_id"`
}

type startJSON struct {
	TaskID       string           `json:"task_id"`
	Repo         string           `json:"repo"`
	RepoPath     string           `json:"repo_path"`
	Branch       string           `json:"branch"`
	Base         string           `json:"base"`
	Mode         string           `json:"mode"`
	WorktreePath string           `json:"worktree_path"`
	Checkout     string           `json:"checkout"`
	Runtime      startRuntimeJSON `json:"runtime"`
}

func emitStartJSON(app *App, t *task.Task, runtimeName string, opened runtime.OpenResult) error {
	repoPath, err := filepath.Abs(t.RepoPath)
	if err != nil {
		return err
	}
	worktreePath := ""
	if t.WorktreePath != "" {
		worktreePath, err = filepath.Abs(t.WorktreePath)
		if err != nil {
			return err
		}
	}
	checkout, err := filepath.Abs(checkoutOf(t))
	if err != nil {
		return err
	}

	rootPaneID := ""
	if t.EffectiveMode() == task.ModeWorktree && runtimeName == "herdr" &&
		opened.Surface == "worktree" && opened.Opened && opened.Created {
		rootPaneID = opened.RootPaneID
	}
	return json.NewEncoder(app.Out).Encode(startJSON{
		TaskID: t.ID, Repo: t.Repo, RepoPath: repoPath, Branch: t.Branch, Base: t.Base,
		Mode: string(t.EffectiveMode()), WorktreePath: worktreePath, Checkout: checkout,
		Runtime: startRuntimeJSON{
			Name: runtimeName, Handle: opened.Handle, Surface: opened.Surface,
			Opened: opened.Opened, Created: opened.Created, RootPaneID: rootPaneID,
		},
	})
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
