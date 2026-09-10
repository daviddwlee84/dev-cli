package cli

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/triage"
	"github.com/daviddwlee84/dev-cli/internal/triagetui"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func runTriageUI(ctx context.Context, app *App, s *triage.Service, opts triage.Options) (triagetui.Model, error) {
	tui.SetColorEnabled(app.outStyle().enabled)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var loaded atomic.Bool
	a := triagetui.Actions{
		Load: func(context.Context) (triage.Report, error) {
			current := opts
			if loaded.Swap(true) {
				current.Snapshots = nil
			}
			return s.Collect(ctx, current)
		},
		Prepare: s.Prepare,
		Apply: func(ctx context.Context, b triage.Batch, token string) (triage.Ledger, error) {
			return s.Apply(ctx, b, b.ID, token, nil)
		},
		Intent: s.Store.SetIntent,
		Directories: func(ctx context.Context, item triage.Item, dirs []string) error {
			return s.Store.SetDisposable(ctx, item.RepositoryID, dirs)
		},
		Open: func(item triage.Item, kind string) tea.Cmd {
			return tea.Exec(&triageProcess{app: *app, item: item, kind: kind}, func(err error) tea.Msg { return triagetui.HandoffDone{Err: err} })
		},
	}
	m := triagetui.New(a)
	if opts.Selection != nil {
		m = triagetui.NewScoped(a)
	}
	m = m.WithScope(opts.ScopeLabel).WithASCII(os.Getenv("TERM") == "dumb")
	result, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithInput(app.In), tea.WithOutput(app.Out)).Run()
	if final, ok := result.(triagetui.Model); ok {
		return final, err
	}
	return m, err
}

func (w *tuiWorkflow) runTriage() error {
	w.result.Scoped = true
	if len(w.request.Selection) == 0 && !w.request.AllLocal {
		return errors.New("dashboard triage requires an explicit local selection")
	}
	s, err := newTriageServiceWithCoverage(&w.app, true)
	if err != nil {
		return err
	}
	opts := triage.Options{Selection: w.request.Selection, Snapshots: w.request.Snapshots, All: w.request.ShowAllTries, ScopeLabel: w.request.ScopeLabel}
	if w.request.AllLocal {
		opts.Selection = nil
	}
	m, err := runTriageUI(w.ctx, &w.app, s, opts)
	if ledger := m.LastLedger(); ledger != nil {
		w.result.Ledger = ledger
		w.result.Status, w.result.Severity = triage.SummarizeLedger(*ledger)
	}
	if len(m.Touched()) == 0 {
		return err
	}
	delta, refreshErr := refreshTriageScope(w.ctx, &w.app, w.request, m.Touched())
	w.result.Local = &delta
	if w.result.Status == "" {
		w.result.Status = "Returned from triage; affected items refreshed"
		w.result.Severity = "info"
	}
	return errors.Join(err, refreshErr)
}

// refreshTriageScope reads only affected Git repositories/Try paths. Global
// metadata and runtime lists may establish references without root discovery.
func refreshTriageScope(ctx context.Context, app *App, request tui.WorkflowRequest, touched []triage.Item) (tui.TriageDelta, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	d := tui.TriageDelta{Generation: request.LocalGeneration}
	seen := map[string]bool{}
	for _, i := range touched {
		target := triage.Target{Path: i.RepositoryPath, RepositoryID: i.RepositoryID, Kind: "repo"}
		if i.Kind == "try" {
			target.Path, target.Kind, target.CatalogID = i.Path, "try", i.CatalogID
		}
		if target.Path == "" {
			continue
		}
		key := target.Kind + ":" + target.Path
		if !seen[key] {
			d.Targets = append(d.Targets, target)
			seen[key] = true
		}
	}
	paths := []string{}
	repositories := []repo.Repo{}
	for _, t := range d.Targets {
		paths = append(paths, t.Path)
		if t.Kind != "repo" {
			continue
		}
		g, err := gitx.Discover(ctx, t.Path)
		if err == nil {
			r := repo.Repo{Path: g.MainRoot, RealPath: g.MainRoot, MainRoot: g.MainRoot, CommonDir: g.GitCommonDir, GitDir: g.GitDir, Name: g.Name, HasGit: true, Bare: g.Bare}
			for _, snapshot := range request.Snapshots {
				if snapshot.Repo.CommonDir == g.GitCommonDir {
					r = snapshot.Repo
					r.RealPath, r.MainRoot, r.GitDir, r.HasGit, r.Bare = g.MainRoot, g.MainRoot, g.GitDir, true, g.Bare
					break
				}
			}
			repositories = append(repositories, r)
		} else {
			r := repo.Repo{Path: t.Path, MainRoot: t.Path, CommonDir: t.RepositoryID, HasGit: true}
			for _, snapshot := range request.Snapshots {
				if snapshot.Repo.CommonDir == t.RepositoryID {
					r = snapshot.Repo
				}
			}
			repositories = append(repositories, r)
		}
	}
	rt := app.Runtime()
	sessions, runtimeErr := rt.List(ctx)
	tasks, diagnostics, tasksErr := app.Tasks.ListWithDiagnostics()
	for _, diagnostic := range diagnostics {
		tasksErr = errors.Join(tasksErr, diagnostic)
	}
	var reposErr, triesErr error
	if tasksErr == nil {
		d.Repos, reposErr = collectReposWithOptions(ctx, app, rt, repoCollectOptions{Repos: repositories, ReposSet: true, Tasks: tasks, TasksSet: true, Sessions: sessions, SessionsSet: true, SessionsErr: runtimeErr, IncludeTries: true})
		d.ReposValid = reposErr == nil
	}
	d.Tries, triesErr = collectTriesWithOptions(ctx, app, rt, experiment.ListOptions{All: request.ShowAllTries, Paths: paths}, sessions, true)
	for n := range d.Tries {
		d.Tries[n].RuntimeErr = runtimeErr
	}
	d.TriesValid = triesErr == nil
	related := func(p string) bool {
		if p == "" {
			return false
		}
		canonical, err := pathx.Canonical(p)
		if err != nil {
			return false
		}
		for _, target := range d.Targets {
			base, _ := pathx.Canonical(target.Path)
			if canonical == base {
				return true
			}
		}
		return false
	}
	if tasksErr == nil {
		selected := []*task.Task{}
		for _, t := range tasks {
			if related(t.RepoPath) || related(t.WorktreePath) {
				selected = append(selected, t)
			}
		}
		d.Tasks = inventory.Collect(ctx, selected, rt, inventory.Options{Sessions: sessions, SessionsSet: true, SessionsTracked: runtimeErr == nil && rt.Name() != "none", Limiter: inventory.NewLimiter(4)})
		d.TasksValid = ctx.Err() == nil
	}
	return d, errors.Join(reposErr, triesErr, tasksErr, runtimeErr, ctx.Err())
}
