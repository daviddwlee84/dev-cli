package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

// candidate is something already on the machine that could become a task.
type candidate struct {
	Task   *task.Task
	Reason string
}

func newAdoptCmd(app *App) *cobra.Command {
	var (
		apply       bool
		yes         bool
		noWorktrees bool
		noSessions  bool
		noBranches  bool
		state       string
	)
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Import existing worktrees, sessions and branches as tasks",
		Long: `Bring what is already on this machine under dev's inventory.

There is nothing to migrate to start using dev — repositories are discovered
wherever your scan roots point, and no directory is moved or renamed. What is
worth importing is the work already in flight: linked worktrees created by
hand or by another tool, runtime sessions sitting in a repo, and local
branches that are ahead of their base.

Reports by default. Nothing is created without --apply, and nothing on disk is
ever moved, renamed or deleted by this command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()

			target, err := task.ParseState(state)
			if err != nil {
				return err
			}

			repos, err := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
			if err != nil {
				return err
			}
			existing, err := app.Tasks.List()
			if err != nil {
				return err
			}
			known := map[string]bool{}
			for _, t := range existing {
				known[t.ID] = true
			}

			rt := app.Runtime()
			var sessions []runtime.Session
			if !noSessions {
				sessions, _ = rt.List(ctx)
			}

			var found []candidate
			for _, r := range repos {
				if r.Bare {
					continue
				}
				found = append(found, scanRepo(ctx, app, r, sessions, known, target,
					!noWorktrees, !noSessions, !noBranches)...)
			}

			if len(found) == 0 {
				fmt.Fprintln(app.Out, "Nothing to adopt — everything in flight is already tracked.")
				return nil
			}

			sort.Slice(found, func(i, j int) bool {
				if found[i].Task.Repo != found[j].Task.Repo {
					return found[i].Task.Repo < found[j].Task.Repo
				}
				return found[i].Task.Branch < found[j].Task.Branch
			})

			fmt.Fprintf(app.Out, "%d candidate(s):\n\n", len(found))
			t := app.newTable("REPO", "BRANCH", "STATE", "WHY", "CHECKOUT")
			style := app.outStyle()
			for _, c := range found {
				t.Add(truncate(c.Task.Repo, 24), truncate(c.Task.Branch, 28),
					style.taskState(c.Task.State.Label()), c.Reason, config.Contract(checkoutOf(c.Task)))
			}
			t.Render(app.Out)

			if !apply {
				fmt.Fprintln(app.Out, "\nRe-run with --apply to record these as tasks.")
				fmt.Fprintln(app.Out, "Nothing on disk is moved or renamed either way.")
				return nil
			}

			in := bufio.NewReader(os.Stdin)
			adopted := 0
			for _, c := range found {
				if !yes && !confirm(app, in, fmt.Sprintf("adopt %s / %s", c.Task.Repo, c.Task.Branch)) {
					continue
				}
				if err := app.Tasks.Save(c.Task); err != nil {
					app.warnf("%s: %v", c.Task.Branch, err)
					continue
				}
				adopted++
			}
			fmt.Fprintf(app.Out, "\nadopted %d task(s) — see them with `dev ls`\n", adopted)
			if adopted > 0 {
				fmt.Fprintln(app.Out, "Give each one a next action so parking it stays cheap: dev park <task> --next \"…\"")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "record the candidates as tasks")
	f.BoolVar(&yes, "yes", false, "with --apply, do not confirm each one")
	f.BoolVar(&noWorktrees, "no-worktrees", false, "skip existing linked worktrees")
	f.BoolVar(&noSessions, "no-sessions", false, "skip live runtime sessions")
	f.BoolVar(&noBranches, "no-branches", false, "skip local branches ahead of their base")
	f.StringVar(&state, "state", string(task.Warm), "state to record adopted tasks in ("+task.JoinStates(", ")+")")
	registerFlagCompletion(cmd, "state", taskStateCompletions())
	return cmd
}

// scanRepo collects everything in one repository that looks like work in
// flight but is not yet recorded.
func scanRepo(ctx context.Context, app *App, r repo.Repo, sessions []runtime.Session,
	known map[string]bool, target task.State, doWorktrees, doSessions, doBranches bool) []candidate {

	var out []candidate
	seen := map[string]bool{}

	add := func(branch, worktree, reason string, state task.State) {
		if branch == "" || seen[branch] {
			return
		}
		id := task.MakeID(r.Name, branch)
		if known[id] {
			return
		}
		seen[branch] = true
		out = append(out, candidate{
			Reason: reason,
			Task: &task.Task{
				ID:           id,
				Name:         branch,
				Repo:         r.Name,
				RepoPath:     r.Path,
				Branch:       branch,
				Base:         gitx.DefaultBranch(ctx, r.Path),
				WorktreePath: worktree,
				State:        state,
				Owner:        config.Hostname(),
			},
		})
	}

	worktrees, err := gitx.Worktrees(ctx, r.Path)
	if err != nil {
		return nil
	}

	if doWorktrees {
		for _, w := range worktrees {
			if w.Main || w.Branch == "" || w.Prunable {
				continue
			}
			// A harness's turn-scoped worktree is not a change stream a human
			// tracks; adopting those would fill the inventory with noise that
			// the harness will delete on its own.
			if inventory.IsEphemeralWorktree(w.Path, w.Branch) {
				continue
			}
			state := target
			reason := "existing worktree"
			if sessionAt(sessions, w.Path) != nil {
				state, reason = task.Hot, "worktree with a live session"
			}
			add(w.Branch, w.Path, reason, state)
		}
	}

	if doSessions {
		for i := range sessions {
			for _, d := range sessions[i].Dirs {
				if d != r.Path {
					continue
				}
				st, err := gitx.StatusOf(ctx, r.Path)
				if err != nil || st.Branch == "" {
					continue
				}
				add(st.Branch, "", "live session in the main checkout", task.Hot)
			}
		}
	}

	if doBranches {
		for _, branch := range unmergedBranches(ctx, r.Path) {
			add(branch, worktreePathFor(worktrees, branch), "branch ahead of the base", target)
		}
	}
	return out
}

func sessionAt(sessions []runtime.Session, dir string) *runtime.Session {
	for i := range sessions {
		for _, d := range sessions[i].Dirs {
			if d == dir {
				return &sessions[i]
			}
		}
	}
	return nil
}

func worktreePathFor(worktrees []gitx.Worktree, branch string) string {
	for _, w := range worktrees {
		if w.Branch == branch && !w.Main {
			return w.Path
		}
	}
	return ""
}

// unmergedBranches lists local branches carrying commits the default branch
// does not — unfinished work that would otherwise be invisible to dev.
func unmergedBranches(ctx context.Context, repoPath string) []string {
	base := gitx.DefaultBranch(ctx, repoPath)
	if base == "" {
		return nil
	}
	out, err := gitx.Run(ctx, repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil
	}
	var branches []string
	for _, b := range strings.Split(out, "\n") {
		b = strings.TrimSpace(b)
		if b == "" || b == base || inventory.IsEphemeralWorktree("", b) {
			continue
		}
		// Contained in the base means the work has already landed.
		if _, err := gitx.Run(ctx, repoPath, "merge-base", "--is-ancestor", b, base); err == nil {
			continue
		}
		branches = append(branches, b)
	}
	return branches
}
