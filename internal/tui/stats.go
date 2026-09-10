package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

type StatsActions struct {
	Read    func(context.Context, repo.Repo) (StatsPanel, error)
	Refresh func(context.Context, repo.Repo, bool) (StatsPanel, error)
}

type statsHistoryMsg struct {
	generation uint64
	panel      StatsPanel
	err        error
	refresh    bool
	force      bool
	ctx        context.Context
}

func (m Model) startStatsHistory(target repo.Repo, force, reset bool) (tea.Model, tea.Cmd) {
	if m.statsCancel != nil {
		m.statsCancel()
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.statsCancel = cancel
	m.statsGeneration++
	generation := m.statsGeneration
	m.statsTarget, m.mode, m.err = target, modeStats, nil
	m.statsRefreshing = true
	if reset {
		m.stats, m.statsScroll = nil, 0
	}
	return m, func() tea.Msg {
		panel, err := m.actions.Stats.Read(ctx, target)
		return statsHistoryMsg{generation: generation, panel: panel, err: err, refresh: true, force: force, ctx: ctx}
	}
}

func (m Model) acceptStatsHistory(msg statsHistoryMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeStats || msg.generation != m.statsGeneration {
		return m, nil
	}
	if msg.err == nil {
		m.stats = &msg.panel
	}
	m.err = msg.err
	if msg.refresh && m.actions.Stats.Refresh != nil {
		return m, func() tea.Msg {
			panel, err := m.actions.Stats.Refresh(msg.ctx, m.statsTarget, msg.force)
			return statsHistoryMsg{generation: msg.generation, panel: panel, err: err}
		}
	}
	m.statsRefreshing = false
	return m, nil
}
