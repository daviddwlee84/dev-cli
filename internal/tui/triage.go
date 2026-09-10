package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func trySelectionKey(r TryRow) string {
	if r.Item.ID != "" {
		return "id:" + r.Item.ID
	}
	return "path:" + r.Item.Live.CurrentPath
}

// openTriage hands an explicit scope to the independent organizer. Dashboard
// navigation and row opening never depend on selections made in another UI.
func (m Model) openTriage(scope string) (tea.Model, tea.Cmd) {
	request := WorkflowRequest{Action: "triage", ScopeLabel: scope, Selection: []triage.Target{}, ShowAllTries: m.showAllTries, LocalGeneration: m.localGeneration}
	if scope == "all" {
		request.AllLocal = true
		request.ScopeLabel = "All local repositories and Tries"
		return m.runWorkflow(request)
	}
	if m.view == ViewRepos {
		var rows []RepoRow
		if scope == "current" {
			if r, ok := m.currentRepoItem(); ok {
				rows = append(rows, r.Repo)
			}
		} else {
			rows = m.visibleRepos()
		}
		for _, r := range rows {
			t := triage.Target{Path: r.Repo.Path, RepositoryID: r.Repo.CommonDir, Kind: "repo"}
			if r.Asset != nil {
				t.CatalogID = r.Asset.ID
			}
			request.Selection = append(request.Selection, t)
			request.Snapshots = append(request.Snapshots, triage.RepositorySnapshot{Repo: r.Repo, Context: r.Context, Topology: r.Topology, TopologyErr: r.TopologyErr, Asset: r.Asset, ObservedAt: r.ObservedAt})
		}
	} else if m.view == ViewTries {
		var rows []TryRow
		if scope == "current" {
			if r, ok := m.currentTry(); ok {
				rows = append(rows, r)
			}
		} else {
			rows = m.visibleTries()
		}
		for _, r := range rows {
			if r.Item.Live.CurrentPath == "" {
				continue
			}
			t := triage.Target{Path: r.Item.Live.CurrentPath, CatalogID: r.Item.ID, Kind: "try"}
			if r.Item.Live.Repo != nil {
				t.RepositoryID = r.Item.Live.Repo.GitCommonDir
			}
			request.Selection = append(request.Selection, t)
		}
	}
	if len(request.Selection) == 0 {
		m.status = "No local items in this scope"
		return m, nil
	}
	request.ScopeLabel = fmt.Sprintf("%s · %s · %d items", strings.ToUpper(m.view.String()), scope, len(request.Selection))
	return m.runWorkflow(request)
}

// TriageDelta replaces only affected local identities. Unrelated snapshots and
// disk measurements remain intact; a newer local cycle supersedes the return.
type TriageDelta struct {
	Generation                         uint64
	Targets                            []triage.Target
	Repos                              []RepoRow
	Tries                              []TryRow
	Tasks                              []inventory.Row
	ReposValid, TriesValid, TasksValid bool
}

func (m Model) applyTriageDelta(d TriageDelta) (tea.Model, tea.Cmd) {
	if d.Generation != m.localGeneration {
		m.status = "Triage returned after a newer refresh; retained the newer snapshot"
		return m, nil
	}
	focus, focused := m.currentSelectionToken()
	related := func(path, common, id string) bool {
		canonical, _ := pathx.Canonical(path)
		for _, t := range d.Targets {
			targetPath, _ := pathx.Canonical(t.Path)
			if (path != "" && canonical != "" && targetPath == canonical) || (common != "" && t.RepositoryID == common) || (id != "" && t.CatalogID == id) {
				return true
			}
		}
		return false
	}
	oldSizes := []diskusage.Target{}
	for _, r := range m.repos {
		if related(r.Repo.Path, r.Repo.CommonDir, "") {
			oldSizes = append(oldSizes, r.SizeTarget)
		}
	}
	for _, r := range m.tries {
		if related(r.Item.Live.CurrentPath, "", r.Item.ID) {
			oldSizes = append(oldSizes, r.SizeTarget)
		}
	}
	m.finishLocalLoad()
	m.localGeneration++
	for _, view := range []View{ViewRepos, ViewTries, ViewTasks} {
		if cancel := m.loadCancels[int(view)]; cancel != nil {
			cancel()
		}
		m.loads[int(view)].generation++
		m.loads[int(view)].loading = false
		valid := (view == ViewRepos && d.ReposValid) || (view == ViewTries && d.TriesValid) || (view == ViewTasks && d.TasksValid)
		if !valid || !m.loads[int(view)].hasSnapshot {
			m.invalidateView(view)
			m.loads[int(view)].actionable = false
		}
	}
	if d.ReposValid {
		rows := []RepoRow{}
		for _, r := range m.repos {
			if !related(r.Repo.Path, r.Repo.CommonDir, "") {
				rows = append(rows, r)
			}
		}
		m.repos = append(rows, d.Repos...)
	}
	if d.TriesValid {
		rows := []TryRow{}
		for _, r := range m.tries {
			if !related(r.Item.Live.CurrentPath, "", r.Item.ID) {
				rows = append(rows, r)
			}
		}
		m.tries = append(rows, d.Tries...)
	}
	if d.TasksValid {
		rows := []inventory.Row{}
		for _, r := range m.rows {
			if r.Task == nil || (!related(r.Task.RepoPath, "", "") && !related(r.Checkout, "", "")) {
				rows = append(rows, r)
			}
		}
		m.rows = append(rows, d.Tasks...)
	}
	m.matchRemoteLocals()
	if focused {
		m.selectToken(focus)
	}
	m.setAt(m.at())
	targets := []diskusage.Target{}
	for _, r := range d.Repos {
		if r.SizeTarget.Checkout != "" {
			targets = append(targets, r.SizeTarget)
		}
	}
	for _, r := range d.Tries {
		if r.Present() && r.SizeTarget.Checkout != "" {
			targets = append(targets, r.SizeTarget)
		}
	}
	if m.actions.Sizes.Invalidate != nil {
		if err := m.actions.Sizes.Invalidate(oldSizes...); err != nil {
			m.err = err
		}
	}
	return m.beginScopedSizes(targets, oldSizes)
}
