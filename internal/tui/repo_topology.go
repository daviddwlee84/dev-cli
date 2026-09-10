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
	if result, ok := msg.(repoTopologyMsg); ok {
		if result.generation == m.viewLoad(ViewRepos).generation {
			m.repos = append([]RepoRow(nil), m.repos...)
			for i := range m.repos {
				if m.repos[i].Repo.Path == result.path {
					m.repos[i].Topology, m.repos[i].TopologyErr, m.repos[i].TopologyPending = result.topology, result.err, false
				}
			}
		}
		return m, nil
	}
	next, command := m.update(msg)
	model, ok := next.(Model)
	if !ok || model.quitting || model.actions.LoadRepoTopology == nil {
		return next, command
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
