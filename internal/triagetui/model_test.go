package triagetui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func key(value string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)} }
func TestTriageSelectionAndRefreshInvalidateApproval(t *testing.T) {
	m := New(Actions{})
	report := triage.Report{Items: []triage.Item{{ID: "one", Kind: "repo", Path: "/one", Actions: []triage.Action{{Name: "push", Availability: "candidate"}}}, {ID: "two", Kind: "try", Path: "/two", Deferred: "local"}}}
	n, _ := m.Update(Loaded{Report: report})
	m = n.(Model)
	if len(m.visible) != 1 {
		t.Fatal("deferred row visible by default")
	}
	n, _ = m.Update(key(" "))
	m = n.(Model)
	if !m.selected["one"] {
		t.Fatal("selection lost")
	}
	n, _ = m.Update(Loaded{Report: report})
	m = n.(Model)
	if len(m.selected) != 0 || m.batch != nil {
		t.Fatal("refresh retained authority")
	}
	n, _ = m.Update(key("d"))
	m = n.(Model)
	if len(m.visible) != 2 {
		t.Fatal("cannot inspect deferred items")
	}
}
func TestTriageEnterOnlyPreparesAndQuitWaitsForApply(t *testing.T) {
	preparedCalls, applyCalls := 0, 0
	m := New(Actions{Prepare: func(context.Context, []triage.Item, string) (triage.Batch, error) {
		preparedCalls++
		return triage.Batch{}, nil
	}, Apply: func(context.Context, triage.Batch, string) (triage.Ledger, error) {
		applyCalls++
		return triage.Ledger{}, nil
	}})
	n, _ := m.Update(Loaded{Report: triage.Report{Items: []triage.Item{{ID: "one", Kind: "repo", Actions: []triage.Action{{Name: "fetch", Availability: "candidate"}}}}}})
	m = n.(Model)
	n, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if cmd == nil {
		t.Fatal("no preview command")
	}
	cmd()
	if preparedCalls != 1 || applyCalls != 0 {
		t.Fatal("Enter applied")
	}
	stopped := false
	m.busy = "applying"
	m.stop = func() { stopped = true }
	n, cmd = m.Update(key("q"))
	m = n.(Model)
	if !stopped || !m.quit || cmd != nil {
		t.Fatal("quit interrupted or abandoned ledger")
	}
	if !strings.Contains(m.View(), "applying") {
		t.Fatal("missing progress")
	}
}

func TestTriageStaleLoadCannotReplaceNewGeneration(t *testing.T) {
	m := New(Actions{})
	m.generation = 3
	n, _ := m.Update(Loaded{Generation: 2, Report: triage.Report{Items: []triage.Item{{ID: "old"}}}})
	m = n.(Model)
	if len(m.report.Items) != 0 {
		t.Fatal("old load replaced current generation")
	}
	if got := fit("中文repo", 5); got != "中文…" {
		t.Fatalf("terminal width truncation = %q", got)
	}
}

func TestScopedChooserOffersTryActionsAndPreselectsCandidates(t *testing.T) {
	m := NewScoped(Actions{})
	report := triage.Report{Items: []triage.Item{
		{ID: "missing", Kind: "try", Presence: "missing", Actions: []triage.Action{{Name: "forget-try", Availability: "candidate"}}},
		{ID: "present", Kind: "try", Presence: "present", Actions: []triage.Action{{Name: "trash-try", Availability: "candidate"}}},
	}}
	n, _ := m.Update(Loaded{Report: report})
	m = n.(Model)
	if out := m.View(); !strings.Contains(out, "forget-try") || !strings.Contains(out, "trash-try") {
		t.Fatal(out)
	}
	n, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if m.action != "forget-try" || !m.selected["missing"] || m.selected["present"] || m.batch != nil {
		t.Fatalf("chooser: %+v", m)
	}
	n, _ = m.Update(key("A"))
	m = n.(Model)
	if out := m.View(); !strings.Contains(out, "trash-try") {
		t.Fatal("other scope actions disappeared", out)
	}
}

func TestMissingTryEnterOpensContextualActions(t *testing.T) {
	m := New(Actions{})
	n, _ := m.Update(Loaded{Report: triage.Report{Items: []triage.Item{{ID: "missing", Kind: "try", Presence: "missing", Actions: []triage.Action{{Name: "forget-try", Availability: "candidate"}}}}}})
	m = n.(Model)
	if strings.Contains(m.View(), "unknown") {
		t.Fatal("unknown activity looks like state")
	}
	n, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = n.(Model)
	if cmd != nil || !m.chooser || !strings.Contains(m.View(), "forget-try") {
		t.Fatal("missing Try has no useful next action")
	}
}
