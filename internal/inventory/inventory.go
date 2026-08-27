// Package inventory joins the three sources that describe work in progress:
// the task registry (human intent), git (durable code state) and the runtime
// (what is live right now). It is the single code path behind `dev ls`,
// `dev status`, `dev sweep` and the TUI, so all four can never disagree.
package inventory

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Row is one task enriched with everything derived live.
type Row struct {
	Task *task.Task

	// Checkout is where the task's code currently lives: its worktree if it
	// has one, else the repo. Empty when nothing exists on disk.
	Checkout string
	// CheckoutExists reports whether that directory is actually present. A
	// cold task, or one whose worktree was removed behind dev's back, is not.
	CheckoutExists bool
	// WorktreeMissing flags a task that records a worktree path which is no
	// longer a registered git worktree — the drift `dev sweep` reports.
	WorktreeMissing bool

	// Status is the live git status of Checkout. Zero when there is nothing
	// on disk to inspect.
	Status    gitx.Status
	StatusErr error

	// LastCommit is the author time of HEAD, used to age a task.
	LastCommit time.Time

	// Session is the live runtime session hosting this checkout, if any.
	Session *runtime.Session
	// sessionsTracked reports whether the runtime can observe sessions at all.
	// With the "none" backend it cannot, so "hot but nothing live" is not
	// drift — it is the only state that backend can ever produce.
	sessionsTracked bool
}

// Live reports whether a runtime session is currently hosting this task.
func (r Row) Live() bool { return r.Session != nil }

// StateDrift reports a task whose recorded state disagrees with reality:
// hot with no live session, or warm/cold with one. It is what `dev sweep`
// turns into suggestions — dev reports drift and never silently rewrites it.
func (r Row) StateDrift() string {
	if r.sessionsTracked {
		switch r.Task.State {
		case task.Hot:
			if !r.Live() {
				return "no live session"
			}
		case task.Warm, task.Cold:
			if r.Live() {
				return "session is live"
			}
		}
	}
	if r.Task.State == task.Cold && r.CheckoutExists && r.Task.WorktreePath != "" {
		return "worktree still on disk"
	}
	if r.WorktreeMissing {
		return "worktree is gone"
	}
	return ""
}

// Age is how long since the task's last commit, falling back to when the entry
// was last written for a task with no commits of its own.
func (r Row) Age() time.Duration {
	ref := r.LastCommit
	if ref.IsZero() || r.Task.Updated.After(ref) {
		ref = r.Task.Updated
	}
	if ref.IsZero() {
		return 0
	}
	return time.Since(ref)
}

// Options tunes what a Collect call spends time on.
type Options struct {
	// SkipRuntime avoids querying the multiplexer, for callers that only need
	// git facts (and for tests).
	SkipRuntime bool
	// SkipGit avoids running git per task, for a fast listing.
	SkipGit bool
}

// Collect builds the enriched inventory. Git probes run concurrently: each is
// a handful of process spawns, and a machine with dozens of tasks would
// otherwise make `dev ls` feel slow.
func Collect(ctx context.Context, tasks []*task.Task, rt runtime.Runtime, opts Options) []Row {
	rows := make([]Row, len(tasks))

	var sessions []runtime.Session
	tracked := false
	if !opts.SkipRuntime && rt != nil && rt.Name() != "none" {
		if s, err := rt.List(ctx); err == nil {
			sessions, tracked = s, true
		}
	}

	var wg sync.WaitGroup
	// Bound concurrency: git is process-heavy and an unbounded fan-out over a
	// large inventory would thrash.
	sem := make(chan struct{}, 8)

	for i, t := range tasks {
		rows[i].Task = t
		rows[i].Checkout = checkoutOf(t)

		if opts.SkipGit || rows[i].Checkout == "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			enrich(ctx, &rows[i])
		}(i)
	}
	wg.Wait()

	for i := range rows {
		rows[i].Session = matchSession(sessions, rows[i])
		rows[i].sessionsTracked = tracked
	}
	return rows
}

func checkoutOf(t *task.Task) string {
	if t.WorktreePath != "" {
		return t.WorktreePath
	}
	return t.RepoPath
}

func enrich(ctx context.Context, r *Row) {
	info, err := os.Stat(r.Checkout)
	r.CheckoutExists = err == nil && info.IsDir()
	if !r.CheckoutExists {
		if r.Task.WorktreePath != "" {
			r.WorktreeMissing = true
		}
		return
	}
	if st, err := gitx.StatusOf(ctx, r.Checkout); err != nil {
		r.StatusErr = err
	} else {
		r.Status = st
	}
	if unix, _, err := gitx.LastCommit(ctx, r.Checkout); err == nil && unix > 0 {
		r.LastCommit = time.Unix(unix, 0)
	}
	// A recorded worktree that git no longer knows about is drift worth
	// surfacing: the directory may exist while the administrative entry is gone.
	if r.Task.WorktreePath != "" {
		if _, ok, err := gitx.WorktreeFor(ctx, r.Task.RepoPath, r.Task.Branch); err == nil && !ok {
			r.WorktreeMissing = true
		}
	}
}

// matchSession picks the runtime session hosting a row's checkout. An exact
// directory match is preferred over a containing one, so a worktree's own
// session always wins over the parent repo's.
func matchSession(sessions []runtime.Session, r Row) *runtime.Session {
	if r.Checkout == "" {
		return nil
	}
	var contains *runtime.Session
	for i := range sessions {
		for _, d := range sessions[i].Dirs {
			if d == r.Checkout {
				return &sessions[i]
			}
			if strings.HasPrefix(d, r.Checkout+"/") && contains == nil {
				contains = &sessions[i]
			}
		}
	}
	return contains
}

// Filter narrows rows by state and repo. An empty filter matches everything.
type Filter struct {
	States []task.State
	Repo   string
	// LiveOnly keeps only tasks with a runtime session.
	LiveOnly bool
	// DirtyOnly keeps only tasks with uncommitted work.
	DirtyOnly bool
}

// Apply returns the rows matching f.
func (f Filter) Apply(rows []Row) []Row {
	out := rows[:0:0]
	for _, r := range rows {
		if len(f.States) > 0 && !containsState(f.States, r.Task.State) {
			continue
		}
		if f.Repo != "" && !strings.Contains(strings.ToLower(r.Task.Repo), strings.ToLower(f.Repo)) {
			continue
		}
		if f.LiveOnly && !r.Live() {
			continue
		}
		if f.DirtyOnly && !r.Status.Dirty() {
			continue
		}
		out = append(out, r)
	}
	return out
}

func containsState(list []task.State, s task.State) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Orphans reports live runtime sessions that no task claims — the "I opened
// this and never wrote it down" case that makes a sidebar grow without bound.
func Orphans(sessions []runtime.Session, rows []Row) []runtime.Session {
	claimed := map[string]bool{}
	for _, r := range rows {
		if r.Session != nil {
			claimed[r.Session.Handle] = true
		}
	}
	var out []runtime.Session
	for _, s := range sessions {
		if !claimed[s.Handle] {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
