package cli

import (
	"fmt"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/repocontext"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/spf13/cobra"
)

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the full context of the current directory",
		Long: `Answer "where am I and what is this?" for the current directory: which repo
and checkout, which branch and how it stands against upstream, which task it
belongs to, whether a runtime session is hosting it, and which recognized Herdr
agents occupy this canonical Git worktree.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			style := app.outStyle()
			field := func(label, value string) {
				fmt.Fprintf(app.Out, "%s%s\n", style.label(fmt.Sprintf("%-11s", label)), value)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			g, err := gitx.Discover(ctx, cwd)
			if err != nil {
				field("directory", config.Contract(cwd))
				field("repo", style.warning("— not a git repository"))
				return nil
			}

			selectedPath := g.Root
			if selectedPath == "" {
				selectedPath = g.MainRoot
			}
			local := collectLocalRepoContext(ctx, app, repo.Repo{
				Name: g.Name, Path: g.MainRoot, RealPath: g.MainRoot,
				GitDir: g.GitCommonDir, CommonDir: g.GitCommonDir,
				MainRoot: g.MainRoot, HasGit: true, Bare: g.Bare,
			}, selectedPath, false)

			kind := "main checkout"
			if g.IsLinkedWorktree {
				kind = "linked worktree"
			}
			field("repo", fmt.Sprintf("%s (%s)", g.Name, kind))
			field("checkout", config.Contract(selectedPath))
			if g.IsLinkedWorktree {
				field("main", config.Contract(g.MainRoot))
			}

			var selected *inventory.RepoCheckout
			if local.SelectedCheckout >= 0 && local.SelectedCheckout < len(local.Context.Checkouts) {
				selected = &local.Context.Checkouts[local.SelectedCheckout]
			}
			if selected == nil {
				field("branch", style.warning("— checkout unavailable"))
			} else if g.Bare {
				field("branch", "(bare repository)")
			} else if selected.StatusErr != nil {
				field("branch", style.warning("— Git status unavailable"))
				app.warnf("could not inspect checkout status: %v", selected.StatusErr)
			} else {
				st := selected.Status
				branch := st.Branch
				if st.Detached {
					branch = "(detached HEAD)"
				}
				field("branch", fmt.Sprintf("%s  %s", branch, style.git(st.Summary())))
				if st.Dirty() {
					field("changes", style.warning(st.Breakdown()))
					if types := st.TypeBreakdown(); types != "" {
						field("types", style.warning(types))
					}
				}
				if st.Upstream != "" {
					field("upstream", st.Upstream)
				} else {
					field("upstream", style.warning("— not published"))
				}
				if st.Conflicted > 0 {
					field("conflicts", style.danger(fmt.Sprintf("%d unmerged path(s) — resolve before anything else", st.Conflicted)))
				}
			}
			field("readiness", repocontext.AssessLocal(local.Context, local.SelectedCheckout, config.Hostname()).Summary())
			if base := gitx.DefaultBranch(ctx, g.MainRoot); base != "" {
				field("default", base)
			}
			if k := forge.Detect(ctx, g.MainRoot); k != forge.Unknown {
				field("forge", fmt.Sprint(k))
			}

			if local.Context.WorktreeErr == nil && len(local.Context.Checkouts) > 1 {
				field("worktrees", fmt.Sprint(len(local.Context.Checkouts)))
				for index, checkout := range local.Context.Checkouts {
					marker := "  "
					if index == local.SelectedCheckout {
						marker = "→ "
					}
					name := checkout.Branch()
					if name == "" {
						name = "(detached)"
					}
					fmt.Fprintf(app.Out, "  %s%-28s %s\n", marker, name, config.Contract(checkout.Worktree.Path))
				}
			} else if local.Context.WorktreeErr != nil {
				app.warnf("could not inspect linked worktrees: %v", local.Context.WorktreeErr)
			}

			rt := app.Runtime()
			if _, ok := rt.(runtime.AgentActivityLister); ok {
				activities, err := checkoutAgentActivities(ctx, rt, selectedPath, "")
				if err != nil {
					app.warnf("could not inspect live agent activity: %v", err)
				} else if len(activities) == 0 {
					field("activities", style.dim("— none"))
				} else {
					for _, activity := range activities {
						name := activity.Name
						if name == "" {
							name = activity.Agent
						}
						field("activity", style.success(fmt.Sprintf("%s:%s %s (%s)",
							name, activity.Status, activity.PaneID, activity.WorkspaceID)))
					}
				}
			}

			if local.Context.TaskErr != nil {
				field("task", style.warning("— task inventory unavailable"))
				app.warnf("could not inspect tracked tasks: %v", local.Context.TaskErr)
				return nil
			}
			if selected == nil || len(selected.Tasks) == 0 {
				field("task", style.warning("— not tracked; `dev start` to record it"))
				return nil
			}
			t := selected.Tasks[0]
			field("task", fmt.Sprintf("%s %s (%s)", style.taskStateFor(t.State.Label(), t.State.Icon()), t.Title(), t.ID))
			field("owner", dash(t.Owner))
			field("next", dash(t.Next))
			if t.Note != "" {
				field("note", t.Note)
			}
			if t.AgentSession != "" {
				field("agent", style.success(t.AgentSession))
			}
			if host := config.Hostname(); !t.OwnedBy(host) {
				app.warnf("this task is owned by %s — pushing from here can diverge the branch", t.Owner)
			}
			return nil
		},
	}
}
