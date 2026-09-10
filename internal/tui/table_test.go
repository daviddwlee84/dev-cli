package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func TestColumnSortCyclesPreservingFocusAndUnknownLast(t *testing.T) {
	m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "beta", Path: "/b", CommonDir: "/b/.git"}, Usage: &diskusage.Usage{OwnedBytes: 20, Complete: true}}, {Repo: repo.Repo{Name: "alpha", Path: "/a", CommonDir: "/a/.git"}, Usage: &diskusage.Usage{OwnedBytes: 4, Complete: true}}, {Repo: repo.Repo{Name: "unknown", Path: "/u", CommonDir: "/u/.git"}}})
	m.view = ViewRepos
	m.width = 120
	m.height = 40
	for _, want := range []string{"alpha", "beta", ""} {
		focus := m.currentToken()
		n, _ := m.cycleTableSort("size")
		m = n.(Model)
		if want != "" {
			rows := m.visibleRepos()
			if rows[0].Repo.Name != want || rows[2].Repo.Name != "unknown" {
				t.Fatal(rows)
			}
		} else if m.tableSorts[int(ViewRepos)].column != "" {
			t.Fatal("third click did not reset")
		}
		if m.currentToken() != focus {
			t.Fatal("sort moved focus to another identity")
		}
	}
}
func TestColumnMouseUsesResponsiveRenderedHeader(t *testing.T) {
	for _, width := range []int{74, 100, 160} {
		m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "中文", Path: "/b"}}, {Repo: repo.Repo{Name: "alpha", Path: "/a"}}})
		m.view = ViewRepos
		m.width = width
		m.height = 40
		h := m.tableHeader()
		found := false
		for _, c := range h.columns {
			if c.key == "repo" {
				found = true
				n, _ := m.updateMouse(tea.MouseMsg{X: c.from, Y: 2 + h.line, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
				m = n.(Model)
			}
		}
		if !found || m.tableSorts[int(ViewRepos)].column != "repo" || !strings.Contains(ansi.Strip(m.renderCurrentList()), "↑") {
			t.Fatal(m.renderCurrentList())
		}
	}
}
func TestFleetHostSortGroupsWithoutReload(t *testing.T) {
	calls := 0
	m := New(Actions{ReloadFleet: func(context.Context) ([]FleetRow, error) { calls++; return nil, nil }}, nil, nil)
	m.view = ViewFleet
	m.fleet = []FleetRow{{Host: "z"}, {Host: "a"}, {Host: "z"}, {Host: "a"}}
	n, _ := m.cycleTableSort("host")
	m = n.(Model)
	rows := m.visibleFleet()
	if rows[0].Host != "a" || rows[1].Host != "a" || rows[2].Host != "z" || calls != 0 {
		t.Fatal(rows)
	}
}
func TestNumericTabsAndStateMenuAreSeparate(t *testing.T) {
	m := New(Actions{}, []inventory.Row{{Task: &task.Task{ID: "hot", State: task.Hot}}, {Task: &task.Task{ID: "warm", State: task.Warm}}}, nil)
	for n, want := range Views {
		next, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune('1' + n)}})
		m = next.(Model)
		if m.view != want || len(m.states) != 0 {
			t.Fatal("numeric navigation changed task state")
		}
	}
	m.view = ViewTasks
	n, _ := m.runListAction(listActionStateWarm)
	m = n.(Model)
	if len(m.visibleTasks()) != 1 || m.visibleTasks()[0].Task.ID != "warm" {
		t.Fatal("state filter menu failed")
	}
}
func TestFooterIsTwoLinesAndPreservesFailure(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.width = 120
	m.height = 35
	m.status = "Triage: 0 completed · 1 failed"
	m.statusSeverity = "error"
	if got := m.renderFooter(); lineCount(got) > 2 || strings.Contains(got, "1/2/3 state") || !strings.Contains(got, "1 failed") {
		t.Fatal(got)
	}
	l := triage.Ledger{Outcomes: []triage.Outcome{{Status: "failed", Action: "push", Error: "Authentication failed"}}}
	n, _ := m.Update(workflowMsg{result: WorkflowResult{Scoped: true, Status: "Push failed", Severity: "error", Ledger: &l}})
	m = n.(Model)
	if m.statusSeverity != "error" || m.lastTriageLedger == nil {
		t.Fatal("return discarded failure summary")
	}
}
