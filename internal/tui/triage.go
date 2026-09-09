package tui

import (
	"fmt"
	"slices"
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
func (m Model) triageSelectionCount() int {
	switch m.view {
	case ViewRepos:
		return len(m.repoSelections)
	case ViewTries:
		return len(m.trySelections)
	}
	return 0
}
func toggled(keys []string, key string) []string {
	result := slices.Clone(keys)
	if n := slices.Index(result, key); n >= 0 {
		return slices.Delete(result, n, n+1)
	}
	return append(result, key)
}
func (m Model) toggleTriageSelection() Model {
	if m.actions.Workflow == nil {
		return m
	}
	switch m.view {
	case ViewRepos:
		if row, ok := m.currentRepoItem(); ok {
			m.repoSelections = toggled(m.repoSelections, repoKey(row.Repo))
		}
	case ViewTries:
		if row, ok := m.currentTry(); ok {
			m.trySelections = toggled(m.trySelections, trySelectionKey(row))
		}
	}
	return m
}
func (m Model) selectVisibleTriage() Model {
	if m.actions.Workflow == nil {
		return m
	}
	switch m.view {
	case ViewRepos:
		m.repoSelections = slices.Clone(m.repoSelections)
		for _, row := range m.visibleRepoItems() {
			key := repoKey(row.Repo)
			if !slices.Contains(m.repoSelections, key) {
				m.repoSelections = append(m.repoSelections, key)
			}
		}
	case ViewTries:
		m.trySelections = slices.Clone(m.trySelections)
		for _, row := range m.visibleTries() {
			key := trySelectionKey(row)
			if !slices.Contains(m.trySelections, key) {
				m.trySelections = append(m.trySelections, key)
			}
		}
	}
	return m
}
func (m Model) selectionSummary() string {
	n := m.triageSelectionCount()
	if n == 0 {
		return ""
	}
	visible := map[string]bool{}
	if m.view == ViewRepos {
		for _, row := range m.visibleRepoItems() {
			if slices.Contains(m.repoSelections, repoKey(row.Repo)) {
				visible[repoKey(row.Repo)] = true
			}
		}
	}
	if m.view == ViewTries {
		for _, row := range m.visibleTries() {
			if slices.Contains(m.trySelections, trySelectionKey(row)) {
				visible[trySelectionKey(row)] = true
			}
		}
	}
	return fmt.Sprintf("%d selected (%d hidden) · Enter triage · Ctrl+O clear selection", n, n-len(visible))
}
func (m Model) repoSelectionMark(r RepoRow) string {
	if slices.Contains(m.repoSelections, repoKey(r)) {
		return "[x] "
	}
	return "[ ] "
}
func (m Model) trySelectionMark(r TryRow) string {
	if slices.Contains(m.trySelections, trySelectionKey(r)) {
		return "[x] "
	}
	return "[ ] "
}

func (m Model) openTriageSelection(single bool) (tea.Model, tea.Cmd) {
	request := WorkflowRequest{Action: "triage", Selection: []triage.Target{}, ShowAllTries: m.showAllTries, LocalGeneration: m.localGeneration}
	if m.view == ViewRepos {
		rows := []RepoRow{}
		if single {
			if row, ok := m.currentRepoItem(); ok {
				rows = append(rows, row.Repo)
			}
		} else {
			for _, row := range m.repos {
				if slices.Contains(m.repoSelections, repoKey(row)) {
					rows = append(rows, row)
				}
			}
		}
		for _, row := range rows {
			target := triage.Target{Path: row.Repo.Path, RepositoryID: row.Repo.CommonDir, Kind: "repo"}
			if row.Asset != nil {
				target.CatalogID = row.Asset.ID
			}
			request.Selection = append(request.Selection, target)
			request.Snapshots = append(request.Snapshots, triage.RepositorySnapshot{Repo: row.Repo, Context: row.Context, Topology: row.Topology, TopologyErr: row.TopologyErr, Asset: row.Asset, ObservedAt: row.ObservedAt})
		}
	} else if m.view == ViewTries {
		rows := []TryRow{}
		if single {
			if row, ok := m.currentTry(); ok {
				rows = append(rows, row)
			}
		} else {
			for _, row := range m.tries {
				if slices.Contains(m.trySelections, trySelectionKey(row)) {
					rows = append(rows, row)
				}
			}
		}
		for _, row := range rows {
			target := triage.Target{Path: row.Item.Live.CurrentPath, CatalogID: row.Item.ID, Kind: "try"}
			if row.Item.Live.Repo != nil {
				target.RepositoryID = row.Item.Live.Repo.GitCommonDir
			}
			if target.Path != "" {
				request.Selection = append(request.Selection, target)
			}
		}
	}
	if len(request.Selection) == 0 {
		m.status = "No local targets in this selection; refresh or inspect its history"
		return m, nil
	}
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
	m.pruneTriageSelections()
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

func (m *Model) pruneTriageSelections() {
	repos, tries := []string{}, []string{}
	for _, r := range m.repos {
		if slices.Contains(m.repoSelections, repoKey(r)) && !slices.Contains(repos, repoKey(r)) {
			repos = append(repos, repoKey(r))
		}
	}
	for _, r := range m.tries {
		if slices.Contains(m.trySelections, trySelectionKey(r)) && r.Where() != "evicted" {
			tries = append(tries, trySelectionKey(r))
		}
	}
	m.repoSelections, m.trySelections = repos, tries
}

func triageSummary(r RepoRow) string {
	parts := []string{}
	if r.Status.Dirty() {
		parts = append(parts, "unsaved")
	}
	if r.Status.Ahead > 0 || r.Status.Behind > 0 {
		parts = append(parts, "sync pending")
	}
	if r.Live || len(r.Sessions()) > 0 {
		parts = append(parts, "in use")
	}
	parts = append(parts, "all branches / ignored contents: inspect in triage")
	return strings.Join(parts, " · ")
}
