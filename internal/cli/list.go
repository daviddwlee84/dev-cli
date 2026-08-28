package cli

import (
	"encoding/json"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

type listOptions struct {
	states    []string
	repo      string
	jsonOut   bool
	live      bool
	dirty     bool
	showAll   bool
	noRuntime bool
}

func newListCmd(app *App) *cobra.Command {
	var o listOptions
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List work in progress across every repo",
		Long: `Show every change stream dev knows about, enriched with live git and
runtime state.

This is the answer to "what am I working on?" — the question a terminal
multiplexer's sidebar gets abused for. Only the state, owner and next action
come from dev's registry; branch, dirty, ahead/behind and the live session are
derived on every run, so the listing cannot go stale.

By default finished tasks are hidden; pass --all to include them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(app, o)
		},
	}
	f := cmd.Flags()
	f.StringSliceVarP(&o.states, "state", "s", nil, "only these states ("+task.JoinStates(", ")+")")
	f.StringVarP(&o.repo, "repo", "r", "", "only tasks whose repo name contains this")
	f.BoolVar(&o.jsonOut, "json", false, "emit JSON for scripting")
	f.BoolVar(&o.live, "live", false, "only tasks with a running runtime session")
	f.BoolVar(&o.dirty, "dirty", false, "only tasks with uncommitted changes")
	f.BoolVarP(&o.showAll, "all", "a", false, "include done tasks")
	f.BoolVar(&o.noRuntime, "no-session", false, "skip the runtime query (faster)")
	registerFlagCompletion(cmd, "state", taskStateSliceCompletions())
	registerFlagCompletion(cmd, "repo", completeTaskRepoNameFlag(app))
	return cmd
}

func runList(app *App, o listOptions) error {
	ctx := ctxOf()
	tasks, err := app.Tasks.List()
	if err != nil {
		return err
	}

	filter := inventory.Filter{Repo: o.repo, LiveOnly: o.live, DirtyOnly: o.dirty}
	for _, s := range o.states {
		st, err := task.ParseState(s)
		if err != nil {
			return err
		}
		filter.States = append(filter.States, st)
	}
	if len(filter.States) == 0 && !o.showAll {
		filter.States = []task.State{task.Hot, task.Warm, task.Cold}
	}

	rt := app.Runtime()
	rows := inventory.Collect(ctx, tasks, rt, inventory.Options{SkipRuntime: o.noRuntime})
	rows = filter.Apply(rows)

	if o.jsonOut {
		return emitJSON(app, rows, rt.Name())
	}

	if len(rows) == 0 {
		if len(tasks) == 0 {
			fmt.Fprintln(app.Out, "No tasks yet. Start one with:  dev start <repo> --task <name>")
		} else {
			fmt.Fprintln(app.Out, "No tasks match that filter. Try `dev ls --all`.")
		}
		return nil
	}

	t := app.newTable("", "TASK", "STATE", "REPO", "BRANCH", "GIT", "AGE", "SESSION", "NEXT")
	s := app.outStyle()
	for _, r := range rows {
		session := "—"
		if r.Live() {
			session = rt.Name()
			if r.Session.AgentStatus != "" {
				session += ":" + r.Session.AgentStatus
			}
			session = s.success(session)
		}
		gitCol := r.Status.Summary()
		switch {
		case r.StatusErr != nil:
			gitCol = "?"
		case !r.CheckoutExists:
			gitCol = "no checkout"
		}
		stateLabel := r.Task.State.Label()
		if r.Task.State == task.Done {
			stateLabel = "MERGED*"
		}
		t.Add(
			s.taskStateFor(r.Task.State.Label(), r.Task.State.Icon()),
			truncate(r.Task.Title(), 28),
			s.taskState(stateLabel),
			truncate(r.Task.Repo, 20),
			truncate(r.Task.Branch, 28),
			s.git(gitCol),
			humanAge(r.Age()),
			session,
			truncate(dash(r.Task.Next), 40),
		)
	}
	t.Render(app.Out)

	// Surface drift as a hint rather than rewriting state behind the user's
	// back: dev reports, the user decides.
	var drifted int
	for _, r := range rows {
		if r.StateDrift() != "" {
			drifted++
		}
	}
	if drifted > 0 {
		fmt.Fprintf(app.Err, "\n%s\n", app.errStyle().warning(fmt.Sprintf("%d task(s) drifted from their recorded state — run `dev sweep` to review.", drifted)))
	}
	return nil
}

// jsonRow is the stable shape of `dev ls --json`. It is a contract for other
// tools (and for the multi-host aggregation `ssh host dev ls --json` enables),
// so fields are added but never renamed.
type jsonRow struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Repo           string   `json:"repo"`
	RepoPath       string   `json:"repo_path"`
	Branch         string   `json:"branch"`
	Base           string   `json:"base,omitempty"`
	WorktreePath   string   `json:"worktree_path,omitempty"`
	Checkout       string   `json:"checkout,omitempty"`
	State          string   `json:"state"`
	Milestone      string   `json:"milestone"`
	CleanupPending bool     `json:"cleanup_pending"`
	ArtifactStatus string   `json:"artifact_status,omitempty"`
	RetireBlockers []string `json:"retirement_blockers,omitempty"`
	Mode           string   `json:"mode"`
	Owner          string   `json:"owner,omitempty"`
	Next           string   `json:"next,omitempty"`
	Note           string   `json:"note,omitempty"`
	Tags           []string `json:"tags,omitempty"`

	CheckoutExists bool   `json:"checkout_exists"`
	Dirty          bool   `json:"dirty"`
	Changed        int    `json:"changed"`
	Staged         int    `json:"staged"`
	Unstaged       int    `json:"unstaged"`
	Untracked      int    `json:"untracked"`
	Conflicted     int    `json:"conflicted"`
	Added          int    `json:"added"`
	Modified       int    `json:"modified"`
	Deleted        int    `json:"deleted"`
	Renamed        int    `json:"renamed"`
	Ahead          int    `json:"ahead"`
	Behind         int    `json:"behind"`
	Upstream       string `json:"upstream,omitempty"`
	GitSummary     string `json:"git_summary"`

	Live          bool     `json:"live"`
	Runtime       string   `json:"runtime,omitempty"`
	RuntimeHandle string   `json:"runtime_handle,omitempty"`
	AgentStatus   string   `json:"agent_status,omitempty"`
	AgentSessions []string `json:"agent_sessions,omitempty"`

	Drift      string `json:"drift,omitempty"`
	AgeSeconds int64  `json:"age_seconds"`
	Updated    string `json:"updated,omitempty"`
}

func taskMilestone(r inventory.Row) string {
	if r.Task.State == task.Done {
		return "merged"
	}
	return "working"
}

func retirementBlockers(r inventory.Row) []string {
	if r.Task.State != task.Done {
		return nil
	}
	var blockers []string
	if r.Live() {
		blockers = append(blockers, "runtime-live")
	}
	if r.Task.WorktreePath != "" {
		if r.WorktreeMissing {
			blockers = append(blockers, "worktree-registration-missing")
		} else {
			blockers = append(blockers, "worktree-present")
		}
	}
	if r.Status.Dirty() {
		blockers = append(blockers, "checkout-dirty")
	}
	return blockers
}

func emitJSON(app *App, rows []inventory.Row, runtimeName string) error {
	artifactByWorktree, err := artifactStatuses(app)
	if err != nil {
		return err
	}
	out := make([]jsonRow, 0, len(rows))
	for _, r := range rows {
		j := jsonRow{
			ID:             r.Task.ID,
			Name:           r.Task.Title(),
			Repo:           r.Task.Repo,
			RepoPath:       config.Contract(r.Task.RepoPath),
			Branch:         r.Task.Branch,
			Base:           r.Task.Base,
			WorktreePath:   config.Contract(r.Task.WorktreePath),
			Checkout:       config.Contract(r.Checkout),
			State:          string(r.Task.State),
			Milestone:      taskMilestone(r),
			CleanupPending: r.Task.State == task.Done,
			RetireBlockers: retirementBlockers(r),
			Mode:           string(r.Task.EffectiveMode()),
			Owner:          r.Task.Owner,
			Next:           r.Task.Next,
			Note:           r.Task.Note,
			Tags:           r.Task.Tags,
			CheckoutExists: r.CheckoutExists,
			Dirty:          r.Status.Dirty(),
			Changed:        r.Status.Changed,
			Staged:         r.Status.Staged,
			Unstaged:       r.Status.Unstaged,
			Untracked:      r.Status.Untracked,
			Conflicted:     r.Status.Conflicted,
			Added:          r.Status.Added,
			Modified:       r.Status.Modified,
			Deleted:        r.Status.Deleted,
			Renamed:        r.Status.Renamed,
			Ahead:          r.Status.Ahead,
			Behind:         r.Status.Behind,
			Upstream:       r.Status.Upstream,
			GitSummary:     r.Status.Summary(),
			Live:           r.Live(),
			Drift:          r.StateDrift(),
			AgeSeconds:     int64(r.Age().Seconds()),
		}
		j.ArtifactStatus = artifactStatusForPath(artifactByWorktree, r.Checkout)
		if j.ArtifactStatus != "" && j.ArtifactStatus != "finalized" {
			j.RetireBlockers = append(j.RetireBlockers, "artifact-"+j.ArtifactStatus)
		}
		if !r.Task.Updated.IsZero() {
			j.Updated = r.Task.Updated.Format("2006-01-02T15:04:05Z")
		}
		if r.Session != nil {
			j.Runtime = runtimeName
			j.RuntimeHandle = r.Session.Handle
			j.AgentStatus = r.Session.AgentStatus
			j.AgentSessions = r.Session.AgentSessions
		}
		if r.Task.WorktreePath == "" {
			j.WorktreePath = ""
		}
		out = append(out, j)
	}
	enc := json.NewEncoder(app.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
