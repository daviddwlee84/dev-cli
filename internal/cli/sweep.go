package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	retirement "github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/spf13/cobra"
)

// suggestion is one proposed change, with the reason and the exact effect.
type suggestion struct {
	row    inventory.Row
	action string
	reason string
	// apply performs the change. nil means "report only".
	apply func() error
}

type sweepRetireOptions struct {
	closeUnknown    bool
	assumeNoRuntime bool
	deleteBranches  bool
}

func newSweepCmd(app *App) *cobra.Command {
	var (
		apply           bool
		staleDays       int
		yes             bool
		mergedWorktrees bool
		baseRef         string
		closeUnknown    bool
		assumeNoRuntime bool
		deleteBranches  bool
	)
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Review stale tasks and drifted state, and act on them",
		Long: `Show which tasks have gone stale or drifted, and what dev would do about it.

Cleanup usually fails not because people are unwilling but because there is no
trustworthy way to be sure the work is recoverable. So sweep reports first and
only changes things with --apply, confirming each one unless you pass --yes.
Nothing here ever deletes uncommitted work.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			style := app.outStyle()
			tasks, err := app.Tasks.List()
			if err != nil {
				return err
			}
			rt := app.Runtime()
			rows := inventory.Collect(ctx, tasks, rt, inventory.Options{})
			stale := time.Duration(staleDays) * 24 * time.Hour
			retireOptions := sweepRetireOptions{
				closeUnknown: closeUnknown, assumeNoRuntime: assumeNoRuntime, deleteBranches: deleteBranches,
			}

			var sugg []suggestion
			if mergedWorktrees {
				merged, err := suggestMergedWorktrees(app, ctx, rows, baseRef, retireOptions)
				if err != nil {
					return err
				}
				sugg = append(sugg, merged...)
			} else {
				for _, r := range rows {
					sugg = append(sugg, suggestFor(app, ctx, r, stale, retireOptions)...)
				}
			}

			// Live sessions no task claims: the other half of a crowded sidebar.
			if !mergedWorktrees {
				if sessions, err := rt.List(ctx); err == nil {
					if orphans := inventory.Orphans(sessions, rows); len(orphans) > 0 {
						fmt.Fprintf(app.Out, "\n%d live session(s) with no task recorded:\n", len(orphans))
						for _, s := range orphans {
							dir := "—"
							if len(s.Dirs) > 0 {
								dir = config.Contract(s.Dirs[0])
							}
							fmt.Fprintf(app.Out, "  %-24s %s\n", truncate(s.Label, 24), style.dim(dir))
						}
						fmt.Fprintln(app.Out, style.dim("  → `dev start` in one of those directories to track it, or just close it."))
					}
				}
			}

			if len(sugg) == 0 {
				fmt.Fprintln(app.Out, style.success("\nNothing to sweep")+" — no task drift or eligible merged worktree was found.")
				return nil
			}

			fmt.Fprintf(app.Out, "\n%s\n\n", style.title(fmt.Sprintf("%d suggestion(s):", len(sugg))))
			for _, s := range sugg {
				fmt.Fprintf(app.Out, "  %s %-28s %s\n", s.row.Task.State.Icon(),
					truncate(s.row.Task.Title(), 28), style.warning(s.reason))
				fmt.Fprintf(app.Out, "     → %s\n", s.action)
			}

			if !apply {
				fmt.Fprintln(app.Out, style.dim("\nRe-run with --apply to act on these (each is confirmed individually)."))
				return nil
			}

			in := bufio.NewReader(os.Stdin)
			for _, s := range sugg {
				if s.apply == nil {
					continue
				}
				if !yes && !confirm(app, in, s.action) {
					continue
				}
				if err := s.apply(); err != nil {
					app.warnf("%s: %v", s.row.Task.ID, err)
					continue
				}
				fmt.Fprintf(app.Out, "  %s %s\n", style.success("done:"), s.action)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "act on the suggestions instead of only reporting")
	f.IntVar(&staleDays, "stale-days", 14, "days without a commit before a task counts as stale")
	f.BoolVar(&yes, "yes", false, "with --apply, do not confirm each change")
	f.BoolVar(&mergedWorktrees, "merged-worktrees", false, "focus on linked worktrees whose branches are contained in the main branch")
	f.StringVar(&baseRef, "base", "", "containment base for --merged-worktrees (default: the repository default branch)")
	f.BoolVar(&closeUnknown, "close-unknown", false, "allow external closure of unknown runtime status during retirement")
	f.BoolVar(&assumeNoRuntime, "assume-no-runtime", false, "continue when runtime enumeration fails during retirement")
	f.BoolVar(&deleteBranches, "delete-branches", false, "also delete contained local branches after worktree retirement")
	return cmd
}

func suggestFor(app *App, ctx context.Context, r inventory.Row, stale time.Duration, retireOptions sweepRetireOptions) []suggestion {
	t := r.Task
	var out []suggestion
	current, currentErr := app.Tasks.GetRecord(t.ID)
	if currentErr != nil || !reflect.DeepEqual(current.Task, *t) {
		detail := "task changed after the sweep inventory snapshot"
		if currentErr != nil {
			detail = "task could not be reloaded after the sweep inventory snapshot: " + currentErr.Error()
		}
		return []suggestion{{
			row: r, reason: detail,
			action: "rerun sweep to inspect the current task revision — not changed automatically",
		}}
	}
	revision := current.Revision

	// A task whose repository is gone cannot be finished, resumed, parked or
	// retired: every one of those resolves the repository first, and `dev done`
	// refuses outright once the checkout is missing. Nothing else in this
	// function reaches it either — the dead-branch rule below excludes direct
	// mode, and the stale-worktree rule needs a recorded worktree path. Left
	// alone, the record is unreachable by any command in the binary.
	//
	// Reaping removes dev's record of intent and nothing else; the branch, the
	// commits and the remote are untouched. A live session proves the directory
	// is there, so it is the one condition that rules this out.
	if t.RepoPath != "" && !r.Live() && !dirExists(t.RepoPath) {
		out = append(out, suggestion{
			row:    r,
			reason: fmt.Sprintf("repository %s no longer exists", config.Contract(t.RepoPath)),
			action: fmt.Sprintf("reap the task entry %s (Git keeps any work; this drops only dev's record)", t.ID),
			apply:  func() error { return sweepDeleteTask(ctx, app.Tasks, t.ID, revision) },
		})
		return out
	}

	out = append(out, orphanSuggestions(app, r)...)

	switch {
	// A hot task with no live session is the commonest drift: the session was
	// closed (or the machine rebooted) without parking.
	case t.State == task.Hot && r.StateDrift() == "no live session":
		reason := fmt.Sprintf("hot but no live session, idle %s", humanAge(r.Age()))
		session, plan, planErr := sweepTaskPlan(ctx, app, t.ID, flow.ParkWarmOptions{})
		if planErr != nil || plan.Availability != flow.AvailabilityReady {
			out = append(out, suggestion{row: r, reason: reason + ", but guarded warm parking is blocked",
				action: sweepPlanBlocker(plan, planErr) + " — not changed automatically"})
			break
		}
		out = append(out, suggestion{
			row: r, reason: reason, action: fmt.Sprintf("mark %s warm", t.ID),
			apply: func() error {
				result, err := session.apply(ctx, plan, flow.Approve(plan.PlanID))
				renderLifecycleResult(app, plan, result)
				return err
			},
		})

	// A warm task nobody has touched in weeks is a candidate for cold: keeping
	// the worktree costs disk and clutters every listing.
	case t.State == task.Warm && r.Age() > stale:
		reason := fmt.Sprintf("warm and untouched for %s", humanAge(r.Age()))
		if t.EffectiveMode() == task.ModeDirect {
			out = append(out, suggestion{row: r, reason: reason + ", but direct tasks cannot go cold",
				action: "keep it warm or finish it — not changed automatically"})
			break
		}
		if r.Status.Dirty() {
			out = append(out, suggestion{row: r, reason: reason + ", but has uncommitted work",
				action: "commit or `dev park --wip` before going cold — not changed automatically"})
			break
		}
		if !r.Status.Synced() {
			out = append(out, suggestion{row: r, reason: reason + fmt.Sprintf(", not pushed (%s)", r.Status.Summary()),
				action: fmt.Sprintf("push %s so it can go cold: dev park %s --cold --push", t.Branch, t.ID)})
			break
		}
		session, plan, planErr := sweepTaskPlan(ctx, app, t.ID, flow.ParkColdOptions{})
		if planErr != nil || plan.Availability != flow.AvailabilityReady {
			detail := sweepPlanBlocker(plan, planErr)
			out = append(out, suggestion{row: r, reason: reason + ", but guarded cold parking is blocked",
				action: detail + " — not changed automatically"})
			break
		}
		out = append(out, suggestion{
			row:    r,
			reason: reason + ", clean and pushed",
			action: fmt.Sprintf("go cold: remove %s (branch and remote keep the work)", config.Contract(r.Checkout)),
			apply: func() error {
				result, err := session.apply(ctx, plan, flow.Approve(plan.PlanID))
				renderLifecycleResult(app, plan, result)
				return err
			},
		})

	// DONE means integrated; runtime/worktree cleanup is a separate,
	// externally coordinated retirement step.
	case t.State == task.Done:
		action := fmt.Sprintf("retire runtime/worktree for %s", t.ID)
		if retireOptions.deleteBranches && t.Branch != "" && t.Branch != t.Base {
			action += fmt.Sprintf(" and delete %s", t.Branch)
		}
		session, plan, planErr := sweepTaskPlan(ctx, app, t.ID, flow.RetireOptions{
			CloseUnknown: retireOptions.closeUnknown, AssumeNoRuntime: retireOptions.assumeNoRuntime,
			DeleteBranch: retireOptions.deleteBranches,
		})
		if planErr != nil || plan.Availability != flow.AvailabilityReady {
			out = append(out, suggestion{row: r, reason: "merged, but guarded retirement is blocked",
				action: sweepPlanBlocker(plan, planErr) + " — not changed automatically"})
			break
		}
		out = append(out, suggestion{
			row: r, reason: "merged, cleanup pending", action: action,
			apply: func() error {
				approval := flow.Approve(plan.PlanID)
				if plan.Confirmation.Kind == flow.ConfirmationTyped {
					approval = flow.ApproveWithToken(plan.PlanID, plan.Confirmation.Token)
				}
				result, err := session.apply(ctx, plan, approval)
				renderLifecycleResult(app, plan, result)
				return err
			},
		})
	}

	// A branch-backed task whose branch git no longer has is dead: the branch
	// was deleted after integration, so the record holds intent for work that
	// no longer exists anywhere. It cannot be finished, resumed, or retired,
	// because every one of those paths resolves the branch first.
	noCheckout := t.WorktreePath == "" || r.WorktreeMissing
	if noCheckout && t.Branch != "" && t.RepoPath != "" &&
		t.EffectiveMode() != task.ModeDirect && !gitx.BranchExists(ctx, t.RepoPath, t.Branch) {
		out = append(out, suggestion{
			row:    r,
			reason: fmt.Sprintf("branch %s no longer exists", t.Branch),
			action: fmt.Sprintf("reap the task entry %s", t.ID),
			apply:  func() error { return sweepDeleteTask(ctx, app.Tasks, t.ID, revision) },
		})
		return out
	}

	// A cold task keeps no checkout by definition: that is what going cold
	// means. One still on disk is drift inventory already reports and nothing
	// could act on.
	if t.State == task.Cold && t.WorktreePath != "" && r.CheckoutExists && !r.WorktreeMissing &&
		!r.Live() && !r.Status.Dirty() && r.Status.Synced() {
		out = append(out, suggestion{
			row:    r,
			reason: "cold, but its worktree is still on disk",
			action: fmt.Sprintf("reconcile %s with an exact guarded plan — not changed automatically", config.Contract(t.WorktreePath)),
		})
	}

	// Drift that is independent of the lifecycle stage.
	if r.WorktreeMissing && t.WorktreePath != "" {
		wtPath := t.WorktreePath
		out = append(out, suggestion{
			row:    r,
			reason: "records a worktree that git no longer knows about",
			action: fmt.Sprintf("inspect repository-wide prune scope before clearing %s — not changed automatically", config.Contract(wtPath)),
		})
	}
	return out
}

func sweepTaskPlan(ctx context.Context, app *App, id string, options flow.ActionOptions) (lifecycleSession, flow.Plan, error) {
	session, err := newLifecycleSession(ctx, app, func() (*task.Task, error) {
		return app.Tasks.Get(id)
	})
	if err != nil {
		return lifecycleSession{}, flow.Plan{}, err
	}
	plan, err := session.plan(ctx, options)
	return session, plan, err
}

func sweepPlanBlocker(plan flow.Plan, err error) string {
	if err != nil {
		return err.Error()
	}
	var parts []string
	for _, condition := range append(plan.InputConditions(), plan.BlockingConditions()...) {
		parts = append(parts, string(condition.Code)+": "+condition.Evidence)
	}
	if len(parts) == 0 {
		return "guarded plan is " + string(plan.Availability)
	}
	return strings.Join(parts, "; ")
}

func sweepDeleteTask(ctx context.Context, store *task.Store, id, revision string) error {
	if revision == "" {
		return fmt.Errorf("task %s had no report-time revision; rerun sweep", id)
	}
	return store.DeleteIfRevision(ctx, id, revision)
}

// dirExists reports whether path is a directory that can be read right now.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// orphanSuggestions handles the residue of a worktree removed while its
// transcript writer was still running: the path exists, Git has no record of
// it, and it holds only agent artifacts. Removing it is offered only once every
// file in it is proven byte-identical to one the repository already has.
func orphanSuggestions(app *App, r inventory.Row) []suggestion {
	t := r.Task
	if t.WorktreePath == "" || !r.WorktreeMissing || !r.CheckoutExists {
		return nil
	}
	orphan, ok, err := retirement.InspectOrphan(t.WorktreePath)
	if err != nil || !ok {
		return nil
	}
	unsalvaged, err := retirement.Unsalvaged(orphan, t.RepoPath)
	if err != nil {
		return nil
	}
	if len(unsalvaged) > 0 {
		return []suggestion{{
			row: r,
			reason: fmt.Sprintf("%s is an abandoned agent workspace holding %d file(s) the repository does not have",
				config.Contract(t.WorktreePath), len(unsalvaged)),
			action: fmt.Sprintf("salvage %s first — not removed automatically", strings.Join(unsalvaged, ", ")),
		}}
	}
	return []suggestion{{
		row:    r,
		reason: fmt.Sprintf("%s is an abandoned agent workspace git does not know", config.Contract(t.WorktreePath)),
		action: fmt.Sprintf("remove the empty shell %s (its %d artifact file(s) are already in the repository)",
			config.Contract(t.WorktreePath), len(orphan.Files)),
		apply: func() error { return os.RemoveAll(t.WorktreePath) },
	}}
}

func suggestMergedWorktrees(app *App, ctx context.Context, rows []inventory.Row, requestedBase string, options sweepRetireOptions) ([]suggestion, error) {
	repository, err := gitx.Discover(ctx, mustGetwd())
	if err != nil {
		return nil, fmt.Errorf("--merged-worktrees requires a Git repository: %w", err)
	}
	if repository.IsLinkedWorktree {
		return nil, fmt.Errorf("--merged-worktrees must run from the canonical checkout, not linked worktree %s", config.Contract(repository.Root))
	}
	status, err := gitx.StatusOf(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	base := requestedBase
	if base == "" {
		base = gitx.DefaultBranch(ctx, repository.Root)
		if status.Branch != base {
			return nil, fmt.Errorf("--merged-worktrees defaults to %s; switch the canonical checkout to it or pass --base explicitly", base)
		}
	}
	if !gitx.RefExists(ctx, repository.Root, base) {
		return nil, fmt.Errorf("merged-worktree base %s does not resolve to a commit", base)
	}

	claimed := make(map[string]inventory.Row)
	for _, row := range rows {
		if row.Task == nil || row.Checkout == "" {
			continue
		}
		canonical, canonicalErr := pathx.Canonical(row.Checkout)
		if canonicalErr == nil {
			claimed[canonical] = row
		}
	}
	worktrees, err := gitx.Worktrees(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	var suggestions []suggestion
	for _, worktree := range worktrees {
		if worktree.Main || worktree.Bare || worktree.Detached || worktree.Branch == "" || worktree.Branch == base {
			continue
		}
		if _, err := gitx.Run(ctx, repository.Root, "merge-base", "--is-ancestor", worktree.Branch, base); err != nil {
			continue
		}
		canonical, err := pathx.Canonical(worktree.Path)
		if err != nil {
			continue
		}
		if row, ok := claimed[canonical]; ok {
			if row.Task.State == task.Done {
				suggestions = append(suggestions, suggestFor(app, ctx, row, 0, options)...)
				continue
			}
			suggestions = append(suggestions, mergedWorktreeBlocker(row, base,
				fmt.Sprintf("task is %s; verify integration with `dev done --merged` first", row.Task.State)))
			continue
		}

		displayTask := &task.Task{
			ID: task.MakeID(repository.Name, worktree.Branch), Name: worktree.Branch,
			Repo: repository.Name, RepoPath: repository.MainRoot, Branch: worktree.Branch,
			Base: base, WorktreePath: canonical, Mode: task.ModeWorktree, State: task.Done,
		}
		row := inventory.Row{Task: displayTask, Checkout: canonical, CheckoutExists: true}
		if inventory.IsClaudeHarnessWorktree(repository.Root, canonical) {
			suggestions = append(suggestions, mergedWorktreeBlocker(row, base,
				"checkout is owned by the Claude harness"))
			continue
		}
		locator, locateErr := exactUnmanagedWorktreeLocator(ctx, repository.MainRoot, canonical)
		if locateErr != nil {
			suggestions = append(suggestions, mergedWorktreeBlocker(row, base, locateErr.Error()))
			continue
		}
		service, serviceErr := newCLILifecycleService(app)
		if serviceErr != nil {
			suggestions = append(suggestions, mergedWorktreeBlocker(row, base, serviceErr.Error()))
			continue
		}
		request, requestErr := flow.NewRequest(locator, flow.RemoveCheckoutOptions{
			RequireContained: true, ContainmentBase: base,
			DeleteContainedBranch: options.deleteBranches,
			CloseUnknown:          options.closeUnknown, AssumeNoRuntime: options.assumeNoRuntime,
		})
		if requestErr != nil {
			suggestions = append(suggestions, mergedWorktreeBlocker(row, base, requestErr.Error()))
			continue
		}
		plan, planErr := service.Plan(ctx, request)
		if planErr != nil || plan.Availability != flow.AvailabilityReady {
			suggestions = append(suggestions, mergedWorktreeBlocker(row, base, sweepPlanBlocker(plan, planErr)))
			continue
		}
		branch := worktree.Branch
		action := fmt.Sprintf("remove merged unmanaged worktree %s (branch %s kept)",
			config.Contract(canonical), branch)
		if options.deleteBranches {
			action = fmt.Sprintf("remove merged unmanaged worktree %s and delete contained branch %s",
				config.Contract(canonical), branch)
		}
		suggestions = append(suggestions, suggestion{
			row: row, reason: fmt.Sprintf("untracked worktree branch is contained in %s", base), action: action,
			apply: func() error {
				approval := flow.Approve(plan.PlanID)
				if plan.Confirmation.Kind == flow.ConfirmationTyped {
					approval = flow.ApproveWithToken(plan.PlanID, plan.Confirmation.Token)
				}
				result, err := service.Apply(ctx, plan, approval)
				renderLifecycleResult(app, plan, result)
				return err
			},
		})
	}
	return suggestions, nil
}

func mergedWorktreeBlocker(row inventory.Row, base, blocker string) suggestion {
	return suggestion{
		row: row, reason: fmt.Sprintf("branch is contained in %s, but retirement is blocked", base),
		action: blocker + " — not changed automatically",
	}
}

func confirm(app *App, in *bufio.Reader, action string) bool {
	s := app.outStyle()
	prompt := s.prompt(action + "?")
	lower := strings.ToLower(action)
	if strings.Contains(lower, "delete") || strings.Contains(lower, "remove") ||
		strings.Contains(lower, "discard") || strings.Contains(lower, "drop") {
		prompt = s.danger(action + "?")
	}
	fmt.Fprintf(app.Out, "  %s %s ", prompt, s.dim("[y/N]"))
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
