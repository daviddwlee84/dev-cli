package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	helpdocs "github.com/daviddwlee84/dev-cli/internal/help"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func helpTestKey(m Model, key string) Model {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	switch key {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestHelpBrowserScopeAndGuideAreReadOnly(t *testing.T) {
	m := New(Actions{LoadRepoTopology: func(context.Context, repo.Repo) (gitx.RecoveryTopology, error) {
		t.Fatal("Help probed Git")
		return gitx.RecoveryTopology{}, nil
	}, ReloadRemote: func(context.Context) ([]RemoteRow, error) { t.Fatal("Help loaded a remote page"); return nil, nil }}, nil, startupRows())
	m.view = ViewRepos
	m.setAt(1)
	m.repos[1].TopologyPending = true
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = next.(Model)
	if cmd != nil || m.help.view != ViewRepos || m.help.section != helpKeys {
		t.Fatal("Help opening changed sources or context")
	}
	before := m.help.snapshot
	m = helpTestKey(m, "2")
	if !strings.Contains(m.View(), "Selected row") || !strings.Contains(m.help.snapshot, "beta") {
		t.Fatal("missing selection explanation")
	}
	m.repos = append([]RepoRow(nil), m.repos...)
	m.repos[1].Status.Untracked = 9
	if m.help.snapshot != before {
		t.Fatal("snapshot changed during Help")
	}
	m = helpTestKey(m, "v")
	next, _ = m.helpActivate(helpTarget{valid: true, kind: helpScopeScreen, scope: int(ViewRemote)})
	m = next.(Model)
	if m.view != ViewRepos || m.at() != 1 || m.help.view != ViewRemote {
		t.Fatal("scope changed actual dashboard")
	}
	m.actions.LoadRepoTopology = nil
	m = helpTestKey(m, "q")
	if m.overlay.kind != overlayNone || m.quitting || m.view != ViewRepos || m.at() != 1 {
		t.Fatal("closing Help changed dashboard")
	}
}

func TestHelpBrowserSearchLayersAliasesAndTyping(t *testing.T) {
	m := New(Actions{}, nil, startupRows())
	m.view = ViewRepos
	m = m.openHelpOverlay()
	loc := m.helpLocation()
	loc.query = "untracked"
	m.setHelpLocation(loc)
	guide := false
	for _, r := range m.helpLayout().rows {
		if r.target.valid {
			if r.target.kind == helpTopicScreen {
				t.Fatal("manual mixed into TUI search")
			}
			if r.target.entry.View != ViewRepos {
				t.Fatal("other page in scoped search")
			}
			guide = guide || r.target.entry.Guide
		}
	}
	if !guide {
		t.Fatal("Guide not searchable from Keys")
	}
	loc.query = "ctrl+p"
	m.setHelpLocation(loc)
	found := false
	for _, r := range m.helpLayout().rows {
		found = found || r.target.entry.Title == "Move selection"
	}
	if !found {
		t.Fatal("keyboard alias not searchable")
	}
	loc.query = ""
	m.setHelpLocation(loc)
	m = helpTestKey(m, "/qf3")
	if !m.help.editing || m.helpLocation().query != "qf3" || m.popupExpanded || m.help.section != helpKeys || m.overlay.kind != overlayHelp {
		t.Fatal("search letters executed controls")
	}
	m = helpTestKey(m, "esc")
	if m.help.editing || m.helpLocation().query == "" {
		t.Fatal("first escape lost query")
	}
	m = helpTestKey(m, "esc")
	if m.helpLocation().query != "" || m.overlay.kind != overlayHelp {
		t.Fatal("second escape did not clear")
	}
	m = helpTestKey(m, "esc")
	if m.overlay.kind != overlayNone {
		t.Fatal("root escape did not close")
	}
}

func TestHelpBrowserManualSearchFindAndBack(t *testing.T) {
	m := New(Actions{}, nil, nil).openHelpOverlay()
	m.help.section = helpManual
	loc := helpLocation{query: "untracked", scroll: 5}
	m.setHelpLocation(loc)
	var target helpTarget
	for _, row := range m.helpLayout().rows {
		if row.target.topic == "git-status" {
			target = row.target
			break
		}
	}
	if !target.valid {
		t.Fatal("manual result missing")
	}
	next, _ := m.helpActivate(target)
	m = next.(Model)
	if m.helpLocation().screen != helpTopicScreen {
		t.Fatal("topic did not open")
	}
	m = helpTestKey(m, "/staged")
	m = helpTestKey(m, "enter")
	if m.help.editing || m.helpLocation().scroll < 0 {
		t.Fatal("article find failed")
	}
	first := m.helpLocation().scroll
	m = helpTestKey(m, "n")
	if m.helpLocation().scroll == first {
		t.Fatal("next match did not move")
	}
	m = helpTestKey(m, "esc")
	m = helpTestKey(m, "esc")
	if got := m.helpLocation(); got.screen != helpHome || got.query != loc.query || got.scroll != loc.scroll {
		t.Fatalf("lost index position: %+v", got)
	}
}

func TestPopupBoundsPreserveDisplayCells(t *testing.T) {
	for _, size := range [][2]int{{160, 60}, {100, 30}, {60, 18}, {40, 8}, {5, 4}, {1, 1}} {
		m := New(Actions{}, nil, nil)
		m.width, m.height = size[0], size[1]
		m = m.openHelpOverlay()
		for _, expanded := range []bool{false, true} {
			m.popupExpanded = expanded
			lines := strings.Split(m.View(), "\n")
			if len(lines) != m.height {
				t.Fatalf("%v height=%d", size, len(lines))
			}
			for _, line := range lines {
				if lipgloss.Width(line) != m.width {
					t.Fatalf("%v line width=%d: %q", size, lipgloss.Width(line), ansi.Strip(line))
				}
			}
		}
	}
}

func TestPopupTouchPanScrollbarCloseAndFooter(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.width, m.height = 60, 18
	m = m.openHelpOverlay()
	m.help.section = helpManual
	body := "# Wide\n```text\n" + strings.Repeat("寬", 100) + "\n" + strings.Repeat("line\n", 80) + "```"
	m.help.topics = []helpdocs.Topic{{Name: "wide", Title: "Wide", Body: body}}
	next, _ := m.helpActivate(helpTarget{valid: true, kind: helpTopicScreen, topic: "wide"})
	m = next.(Model)
	f := m.helpLayout()
	footer := helpFooterLine(f, m.helpLocation())
	index := strings.Index(footer, "[→]")
	if index < 0 {
		t.Fatal("no touch pan")
	}
	x := lipgloss.Width(footer[:index])
	_, h := m.popupContentSize()
	m, _ = applyMouse(m, popupMouse(m, x, h-1, tea.MouseButtonLeft, tea.MouseActionPress))
	if m.helpLocation().xOffset == 0 {
		t.Fatal("touch pan failed")
	}
	r := m.popupBounds()
	m, _ = applyMouse(m, mouseMessage(r.x+r.width-2, r.y+1+f.contentY+f.visible-1, tea.MouseButtonLeft, tea.MouseActionPress))
	if m.helpLocation().scroll == 0 {
		t.Fatal("scrollbar did not jump")
	}
	m, _ = applyMouse(m, mouseMessage(0, 0, tea.MouseButtonLeft, tea.MouseActionRelease))
	if m.popupDragging {
		t.Fatal("release did not stop drag")
	}
	_, closeX, _, _ := m.popupControls()
	m, _ = applyMouse(m, mouseMessage(r.x+closeX, r.y, tea.MouseButtonLeft, tea.MouseActionPress))
	if m.overlay.kind != overlayNone || m.quitting {
		t.Fatal("close passed through")
	}
	m = New(Actions{}, mouseTaskRows(2), nil)
	lines := strings.Split(m.renderDashboard(), "\n")
	y := len(lines) - 1
	x = -1
	for i := 0; i < m.width; i++ {
		if m.footerHelpAt(i, y) {
			x = i
			break
		}
	}
	if x < 0 {
		t.Fatal("footer help missing")
	}
	m, _ = applyMouse(m, mouseMessage(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
	if m.overlay.kind != overlayHelp {
		t.Fatal("footer did not open Help")
	}
}

func TestPopupExpansionResetsForNewDialog(t *testing.T) {
	m := New(Actions{}, mouseTaskRows(1), nil)
	m = helpTestKey(m, "?")
	m = helpTestKey(m, "f")
	if !m.popupExpanded {
		t.Fatal("expand failed")
	}
	m = helpTestKey(m, "q")
	m = helpTestKey(m, "?")
	if m.popupExpanded {
		t.Fatal("new dialog retained expansion")
	}
}

func TestPopupClippedActionsCannotBeActivated(t *testing.T) {
	m := outsideStartupModel()
	m.width, m.height = 40, 8
	m.actions.Discovery.Apply = func(context.Context, repo.DiscoveryRegistrationPlan) (string, error) {
		t.Fatal("clipped confirmation was applied")
		return "", nil
	}
	m.overlay = overlayState{kind: overlayActionMenu, registration: 1, title: "Review", subject: "Review the path", body: strings.Repeat("long/path/", 80)}
	m.overlay.addOption(listActionRegistrationConfirm, "confirm and save")
	m.overlay.addOption(listActionRegistrationCancel, "cancel")
	if view := ansi.Strip(m.View()); strings.Contains(view, "confirm and save") || !strings.Contains(view, "Enlarge terminal") {
		t.Fatalf("unexpected short-screen preview:\n%s", view)
	}
	_, height := m.popupContentSize()
	for _, y := range []int{m.buildActionMenuLayout().firstOptionY, height - 1, height} {
		next, cmd := m.Update(popupMouse(m, 3, y, tea.MouseButtonLeft, tea.MouseActionPress))
		got := next.(Model)
		if cmd != nil || got.startupRepo.applying || got.overlay.title != "Review" {
			t.Fatalf("click at clipped option/footer/border y=%d dispatched an action", y)
		}
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || next.(Model).startupRepo.applying {
		t.Fatal("Enter dispatched a clipped action")
	}
	// Resizing makes the same confirmation visible and actionable again.
	m.height = 16
	if !strings.Contains(ansi.Strip(m.View()), "confirm and save") {
		t.Fatal("resize did not reveal the action")
	}
	next, cmd = m.Update(popupMouse(m, 3, m.buildActionMenuLayout().firstOptionY, tea.MouseButtonLeft, tea.MouseActionPress))
	if cmd == nil || !next.(Model).startupRepo.applying {
		t.Fatal("visible confirmation was disabled")
	}
}

func TestHelpNavigationSelectsFirstEntry(t *testing.T) {
	m := New(Actions{}, nil, startupRows()).openHelpOverlay()
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyShiftTab},
		popupMouse(m, 11, 0, tea.MouseButtonLeft, tea.MouseActionPress),
	} {
		next, _ := m.Update(msg)
		got := helpTestKey(next.(Model), "enter")
		if got.helpLocation().screen == helpHome {
			t.Fatalf("navigation %v left no selected entry", msg)
		}
	}
	m = helpTestKey(m, "v")
	next, _ := m.helpActivate(helpTarget{valid: true, kind: helpScopeScreen, scope: int(ViewRepos)})
	got := helpTestKey(next.(Model), "enter")
	if got.helpLocation().screen != helpEntryScreen {
		t.Fatal("changing scope left no selected entry")
	}
}
