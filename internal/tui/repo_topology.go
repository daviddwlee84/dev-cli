package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"time"
)

type repoTopologyMsg struct {
	generation uint64
	path       string
	topology   gitx.RecoveryTopology
	err        error
}

// Update schedules selected-row recovery details separately from bulk Git status.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	fleetFocus := selectionToken{}
	if m.hostFleetEnabled() {
		switch msg.(type) {
		case tea.KeyMsg, tea.MouseMsg:
		default:
			focusModel := m
			focusModel.view = ViewFleet
			fleetFocus = focusModel.currentToken()
		}
	}
	if result, ok := msg.(repoTopologyMsg); ok {
		if result.generation == m.viewLoad(ViewRepos).generation {
			m.repos = append([]RepoRow(nil), m.repos...)
			for i := range m.repos {
				if m.repos[i].Repo.Path == result.path {
					m.repos[i].Topology, m.repos[i].TopologyErr, m.repos[i].TopologyPending = result.topology, result.err, false
				}
			}
		}
		if m.hostFleetEnabled() {
			m.syncFleetLocal()
			m.restoreFleetFocus(fleetFocus)
		}
		return m, nil
	}
	var focus selectionToken
	var path string
	preserveFocus := false
	switch msg.(type) {
	case localMsg, reposMsg, reloadMsg, sizeMsg, configMsg, triesMsg, noteListMsg:
		preserveFocus = true
		focusModel := m
		focusModel.view = ViewRepos
		focus, path = focusModel.currentToken(), focusModel.repoFocusPath()
	}
	next, command := m.update(msg)
	model, ok := next.(Model)
	if !ok || model.quitting {
		return next, command
	}
	if model.filter != m.filter {
		model.fleetTree.searchQuery = model.filter
		model.fleetTree.searchExpansions = nil
	}
	if !m.sharedPopup() || !model.sharedPopup() {
		model.popupExpanded = false
		model.popupDragging = false
	}
	if preserveFocus {
		if model.hostFleetEnabled() {
			model.syncFleetLocal()
		}
		focusModel := model
		focusModel.view = ViewRepos
		focusModel.restoreRepoFocus(focus, path)
		model.repoCursor = focusModel.repoCursor
	}
	if model.hostFleetEnabled() && fleetFocus.key != "" {
		model.restoreFleetFocus(fleetFocus)
	}
	model.finishRegistrationReload()
	model.focusStartupRepo()
	if model.actions.LoadRepoTopology == nil || model.overlay.kind == overlayHelp {
		return model, command
	}
	row, ok := model.currentRepo()
	if !ok || row.Pending != "" || !row.TopologyPending {
		return model, command
	}
	for _, path := range model.topologyRequested {
		if path == row.Repo.Path {
			return model, command
		}
	}
	model.topologyRequested = append(append([]string(nil), model.topologyRequested...), row.Repo.Path)
	generation := model.viewLoad(ViewRepos).generation
	load := func() tea.Msg {
		ctx, cancel := context.WithTimeout(model.baseContext(), 10*time.Second)
		defer cancel()
		topology, err := model.actions.LoadRepoTopology(ctx, row.Repo)
		return repoTopologyMsg{generation, row.Repo.Path, topology, err}
	}
	return model, tea.Batch(command, load)
}
