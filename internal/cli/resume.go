package cli

import (
	"fmt"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newResumeCmd(app *App) *cobra.Command {
	var (
		noProvision bool
		fetch       bool
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "resume <task>",
		Short: "Pick a task back up, rebuilding whatever is missing",
		Long: `Make a parked task hot again.

Warm tasks still have their worktree, so this just reopens a runtime session.
Cold tasks do not: the worktree is rebuilt from the branch, which is why going
cold is safe in the first place — the branch, not the directory, is the task's
identity.

If the task is owned by another machine, resuming here takes ownership. Two
machines writing the same branch is the one way to make this workflow produce
a conflict, so dev asks before doing it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			t, err := app.Tasks.Resolve(args[0])
			if err != nil {
				return err
			}

			host := config.Hostname()
			if !t.OwnedBy(host) && !force {
				return fmt.Errorf("task %s is owned by %s.\n"+
					"Make sure that machine has pushed its work, then re-run with --force to take ownership",
					t.ID, t.Owner)
			}

			if fetch {
				if _, err := gitx.Run(ctx, t.RepoPath, "fetch", "--prune", "origin"); err != nil {
					app.warnf("fetch failed: %v", err)
				}
			}

			rt := app.Runtime()
			mode := t.EffectiveMode()
			checkout := checkoutOf(t)

			switch mode {
			case task.ModeWorktree:
				_, statErr := os.Stat(checkout)
				// Rebuild the worktree when it is gone — the cold-to-hot path.
				if t.WorktreePath == "" || statErr != nil {
					base := t.Base
					// Prefer the published branch: a cold task's work lives on
					// the remote, and a stale local ref could drop it.
					if remote := "origin/" + t.Branch; gitx.RefExists(ctx, t.RepoPath, remote) {
						base = remote
					}
					m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: app.Err}
					res, err := m.Create(ctx, wt.CreateRequest{
						RepoPath: t.RepoPath, RepoName: t.Repo, Branch: t.Branch,
						Base: base, Label: t.Title(), NoProvision: noProvision,
					})
					if err != nil {
						var exists *wt.ErrExists
						if asError(err, &exists) {
							t.WorktreePath = exists.Path
						} else {
							return err
						}
					} else {
						t.WorktreePath, t.RuntimeHandle = res.Path, res.RuntimeHandle
						reportProvision(app, res)
						fmt.Fprintf(app.Out, "   rebuilt    %s\n", config.Contract(res.Path))
					}
					checkout = t.WorktreePath
				}

			case task.ModeBranch:
				checkout = t.RepoPath
				st, err := gitx.StatusOf(ctx, checkout)
				if err != nil {
					return err
				}
				if st.Branch != t.Branch {
					if st.Dirty() {
						return fmt.Errorf("cannot switch the canonical checkout from %s to %s: %s",
							st.Branch, t.Branch, st.Breakdown())
					}
					if _, err := gitx.Run(ctx, checkout, "switch", t.Branch); err != nil {
						return err
					}
				}

			case task.ModeDirect:
				checkout = t.RepoPath
				st, err := gitx.StatusOf(ctx, checkout)
				if err != nil {
					return err
				}
				if st.Branch != t.Branch {
					return fmt.Errorf("direct task %s tracks %s, but the canonical checkout is on %s; "+
						"switch back explicitly or start a worktree task", t.Title(), t.Branch, st.Branch)
				}
			}
			if t.RuntimeHandle == "" {
				handle, err := openCheckout(ctx, rt, checkout, t.Title())
				if err != nil {
					app.warnf("could not open a runtime session: %v", err)
				}
				if rt.Name() != "none" {
					t.RuntimeHandle = handle
				}
			}

			t.State = task.Hot
			t.Owner = host
			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			annotate(app, rt, t)

			fmt.Fprintf(app.Out, "%s %s  %s on %s (%s)\n",
				task.Hot.Icon(), t.Title(), t.Repo, t.Branch, mode)
			if t.Next != "" {
				fmt.Fprintf(app.Out, "   next      %s\n", t.Next)
			}
			if t.AgentSession != "" {
				fmt.Fprintf(app.Out, "   agent     %s (resumable)\n", t.AgentSession)
			}
			if st, err := gitx.StatusOf(ctx, checkout); err == nil {
				fmt.Fprintf(app.Out, "   git       %s\n", st.Summary())
				if st.Behind > 0 {
					app.warnf("branch is %d behind upstream — `git pull --ff-only` before you start", st.Behind)
				}
			}
			if rt.Name() == "none" {
				app.cdDirective(checkout)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&noProvision, "no-provision", false, "skip dependency install when rebuilding a worktree")
	f.BoolVar(&fetch, "fetch", true, "fetch from origin first")
	f.BoolVar(&force, "force", false, "take ownership of a task owned by another machine")
	return cmd
}
