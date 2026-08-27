package cli

import (
	"fmt"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/spf13/cobra"
)

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the full context of the current directory",
		Long: `Answer "where am I and what is this?" for the current directory: which repo
and checkout, which branch and how it stands against upstream, which task it
belongs to, and whether a runtime session is hosting it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			g, err := gitx.Discover(ctx, cwd)
			if err != nil {
				fmt.Fprintf(app.Out, "directory  %s\n", config.Contract(cwd))
				fmt.Fprintln(app.Out, "repo       — not a git repository")
				return nil
			}

			kind := "main checkout"
			if g.IsLinkedWorktree {
				kind = "linked worktree"
			}
			fmt.Fprintf(app.Out, "repo       %s (%s)\n", g.Name, kind)
			fmt.Fprintf(app.Out, "checkout   %s\n", config.Contract(g.Root))
			if g.IsLinkedWorktree {
				fmt.Fprintf(app.Out, "main       %s\n", config.Contract(g.MainRoot))
			}

			if st, err := gitx.StatusOf(ctx, g.Root); err == nil {
				branch := st.Branch
				if st.Detached {
					branch = "(detached HEAD)"
				}
				fmt.Fprintf(app.Out, "branch     %s  %s\n", branch, st.Summary())
				if st.Upstream != "" {
					fmt.Fprintf(app.Out, "upstream   %s\n", st.Upstream)
				} else {
					fmt.Fprintf(app.Out, "upstream   — not published\n")
				}
				if st.Conflicted > 0 {
					fmt.Fprintf(app.Out, "conflicts  %d unmerged path(s) — resolve before anything else\n", st.Conflicted)
				}
			}
			if base := gitx.DefaultBranch(ctx, g.MainRoot); base != "" {
				fmt.Fprintf(app.Out, "default    %s\n", base)
			}
			if k := forge.Detect(ctx, g.MainRoot); k != forge.Unknown {
				fmt.Fprintf(app.Out, "forge      %s\n", k)
			}

			if list, err := gitx.Worktrees(ctx, g.MainRoot); err == nil && len(list) > 1 {
				fmt.Fprintf(app.Out, "worktrees  %d\n", len(list))
				for _, w := range list {
					marker := "  "
					if w.Path == g.Root {
						marker = "→ "
					}
					name := w.Branch
					if name == "" {
						name = "(detached)"
					}
					fmt.Fprintf(app.Out, "  %s%-28s %s\n", marker, name, config.Contract(w.Path))
				}
			}

			t, err := app.Tasks.FindByWorktree(g.Root)
			if err != nil {
				fmt.Fprintln(app.Out, "task       — not tracked; `dev start` to record it")
				return nil
			}
			fmt.Fprintf(app.Out, "task       %s %s (%s)\n", t.State.Icon(), t.Title(), t.ID)
			fmt.Fprintf(app.Out, "owner      %s\n", dash(t.Owner))
			fmt.Fprintf(app.Out, "next       %s\n", dash(t.Next))
			if t.Note != "" {
				fmt.Fprintf(app.Out, "note       %s\n", t.Note)
			}
			if t.AgentSession != "" {
				fmt.Fprintf(app.Out, "agent      %s\n", t.AgentSession)
			}
			if host := config.Hostname(); !t.OwnedBy(host) {
				app.warnf("this task is owned by %s — pushing from here can diverge the branch", t.Owner)
			}
			return nil
		},
	}
}
