package tui

import (
	"context"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"strings"
	"testing"
)

func overlayKey(m Model, key tea.KeyMsg) Model { next, _ := m.updateOverlay(key); return next.(Model) }
func TestActionMenuFilterUsesVisibleIndicesAndFits(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.width = 70
	m.height = 13
	m.overlay = overlayState{kind: overlayActionMenu, subject: "repo"}
	for i := 0; i < 20; i++ {
		m.overlay.addOption(listActionOpen, "open repository")
	}
	m.overlay.addOption(listActionStats, "open activity heatmap")
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/STATS")})
	visible := m.visibleActions()
	if len(visible) != 1 || visible[0] != 20 {
		t.Fatal(visible)
	}
	index, ok := m.actionMenuOptionAt(3, m.buildActionMenuLayout().firstOptionY)
	if !ok || index != 20 {
		t.Fatalf("mouse index=%d ok=%v", index, ok)
	}
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !m.overlay.searching || len(m.visibleActions()) != 0 {
		t.Fatal("q should be searchable text")
	}
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay.kind != overlayActionMenu {
		t.Fatal("empty result executed")
	}
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay.kind != overlayActionMenu || m.overlay.searching {
		t.Fatal("first escape should clear search")
	}
	m.moveActionMenu(1)
	if lineCount(m.renderOverlay()) > m.height {
		t.Fatal(m.renderOverlay())
	}
}

func TestRepoProgressRejectsLateCacheAndKeepsFocus(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewRepos
	m.width = 100
	m.height = 30
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	a := RepoRow{Repo: repo.Repo{Name: "a", Path: "/a"}, Pending: "cached"}
	b := RepoRow{Repo: repo.Repo{Name: "b", Path: "/b"}, Pending: "loading"}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{a}, Valid: true})
	token := m.currentToken()
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "discovery", Repos: []RepoRow{b}, Valid: true})
	if token != m.currentToken() {
		t.Fatal("partial result moved focus")
	}
	var accepted bool
	m, accepted = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{{}}, Valid: true})
	if accepted {
		t.Fatal("late cache accepted")
	}
	a.Pending = ""
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Repos: []RepoRow{a}, Valid: true})
	if len(m.repos) != 1 || m.repos[0].Pending != "" {
		t.Fatal(m.repos)
	}
	m, accepted = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "enrichment", Repos: []RepoRow{b}, Valid: true})
	if accepted {
		t.Fatal("post-completion patch accepted")
	}
}

func TestStatsHistoryReadsFirstCancelsAndPreservesPriorPanelOnError(t *testing.T) {
	refreshCalls := 0
	actions := StatsActions{Read: func(context.Context, repo.Repo) (StatsPanel, error) {
		return StatsPanel{Repo: "a", Seconds: 42, Heatmap: "saved"}, nil
	}, Refresh: func(context.Context, repo.Repo, bool) (StatsPanel, error) {
		refreshCalls++
		return StatsPanel{}, errors.New("history unavailable")
	}}
	m := New(Actions{Stats: actions}, nil, nil)
	next, read := m.startStatsHistory(repo.Repo{Name: "a", Path: "/a"}, false, true)
	m = next.(Model)
	next, refresh := m.Update(read())
	m = next.(Model)
	if m.stats == nil || m.stats.Seconds != 42 || refreshCalls != 0 {
		t.Fatal("cached stats not shown before refresh")
	}
	next, _ = m.Update(refresh())
	m = next.(Model)
	if m.stats == nil || m.stats.Seconds != 42 || !strings.Contains(m.err.Error(), "unavailable") {
		t.Fatal("refresh lost cached panel")
	}
	generation := m.statsGeneration
	next, _ = m.updateStats(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	next, _ = m.Update(statsHistoryMsg{generation: generation, panel: StatsPanel{Repo: "late"}})
	m = next.(Model)
	if m.stats != nil {
		t.Fatal("late history result resurrected closed panel")
	}
}
