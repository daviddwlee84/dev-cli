package triagetui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func groupedModel() Model {
	m := New(Actions{})
	m.width, m.height = 120, 35
	r := triage.Report{Items: []triage.Item{{ID: "one-main", Name: "alpha", Kind: "repo", RepositoryID: "/one/.git", RepositoryPath: "/one", Path: "/one", Scope: "checkout", Complete: true}, {ID: "one-branch", Name: "alpha", Kind: "repo", RepositoryID: "/one/.git", RepositoryPath: "/one", Path: "/one", Scope: "branch", Complete: true}, {ID: "two", Name: "beta", Kind: "repo", RepositoryID: "/two/.git", RepositoryPath: "/two", Path: "/two", Scope: "checkout", Complete: true}}}
	n, _ := m.Update(Loaded{Report: r})
	return n.(Model)
}
func TestGroupedSelectionPartialAndVisibleAllToggle(t *testing.T) {
	m := groupedModel()
	if len(m.rows) != 2 {
		t.Fatal("unexpanded list repeats branch rows")
	}
	m.focusRow("repo:/one/.git")
	old := m
	m.toggleRow(m.cursor)
	if len(old.selected) != 0 || len(m.selected) != 2 {
		t.Fatal("selection identity or model copy broken")
	}
	m.expandCurrent(true)
	if len(m.rows) != 4 {
		t.Fatal("children missing")
	}
	m.focusRow("one-branch")
	m.toggleRow(m.cursor)
	if m.selectionState(m.rows[0].members) != "[-]" {
		t.Fatal("parent does not show partial selection")
	}
	m.filter = "beta"
	m.filterRows()
	m.toggleVisible()
	if !m.selected["two"] || !m.selected["one-main"] {
		t.Fatal("filtered select lost hidden targets")
	}
	m.toggleVisible()
	if m.selected["two"] || !m.selected["one-main"] {
		t.Fatal("Ctrl+A did not cancel only visible selection")
	}
}
func TestMouseFocusCheckboxAndDisclosureShareRenderedHits(t *testing.T) {
	m := groupedModel()
	frame := m.render()
	if !frame.fits {
		t.Fatal(frame.text)
	}
	var checkbox, disclosure hit
	for _, p := range frame.hits {
		if p.index == 0 && p.kind == "check" {
			checkbox = p
		}
		if p.index == 0 && p.kind == "expand" {
			disclosure = p
		}
	}
	click := func(x, y int) {
		n, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		m = n.(Model)
	}
	click(12, checkbox.y)
	if len(m.selected) != 0 {
		t.Fatal("row click unexpectedly selected")
	}
	click(checkbox.x, checkbox.y)
	if len(m.selected) != 2 {
		t.Fatal("checkbox did not select group")
	}
	click(disclosure.x, disclosure.y)
	if len(m.rows) != 4 {
		t.Fatal("disclosure did not expand")
	}
}
func TestResultSummaryAndDetailsDoNotDuplicateLedger(t *testing.T) {
	m := groupedModel()
	m.results = true
	m.busy = "Refreshing this scope…"
	m.lastLedger = &triage.Ledger{Path: "/private/receipt.json", Outcomes: []triage.Outcome{{ItemID: "one-main", Path: "/one", Action: "push", Status: "failed", Error: "Authentication failed", Steps: []taskflow.StepResult{{Diagnostic: &gitx.Diagnostic{Code: "authentication", Summary: "Authentication failed", Next: "Open a shell to authenticate", Details: "terminal prompts disabled", ExitCode: 128}}}}}}
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "1 failed") || strings.Contains(out, "/private/receipt.json") || strings.Contains(out, "terminal prompts disabled") {
		t.Fatal(out)
	}
	n, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	out = ansi.Strip(m.View())
	if !strings.Contains(out, "Open a shell") || !strings.Contains(out, "terminal prompts disabled") {
		t.Fatal(out)
	}
}
func TestOrganizerFramesFitNarrowAndASCII(t *testing.T) {
	for _, width := range []int{60, 90, 150} {
		m := groupedModel().WithASCII(true)
		m.width = width
		for _, line := range strings.Split(m.View(), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("line exceeds %d: %s", width, line)
			}
		}
		if len(strings.Split(m.View(), "\n")) > m.height {
			t.Fatal("frame clips clickable headers")
		}
		if strings.Contains(m.View(), "▸") || strings.Contains(m.View(), "↕") {
			t.Fatal("ASCII fallback retained icons")
		}
	}
}
