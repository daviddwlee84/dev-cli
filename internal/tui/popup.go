package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type popupRect struct{ x, y, width, height int }

func (r popupRect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

func (m Model) sharedPopup() bool {
	return m.overlay.kind == overlayHelp || m.overlay.kind == overlayActionMenu
}

func (m Model) popupBounds() popupRect {
	w, h := max(0, m.width), max(0, m.height)
	if m.popupExpanded || w < 80 || h < 22 {
		return popupRect{width: w, height: h}
	}
	pw, ph := min(104, w-4), min(32, h-4)
	return popupRect{x: (w - pw) / 2, y: (h - ph) / 2, width: pw, height: ph}
}
func (m Model) popupContentSize() (int, int) {
	r := m.popupBounds()
	return max(1, r.width-4), max(1, r.height-2)
}
func (m Model) popupPoint(x, y int) (int, int) { r := m.popupBounds(); return r.x + 2 + x, r.y + 1 + y }

func popupFit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "")
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}

func (m Model) popupControls() (string, int, string, int) {
	r := m.popupBounds()
	closeLabel, expandLabel := "[Close]", "[Expand]"
	if m.popupExpanded {
		expandLabel = "[Restore]"
	}
	if r.width < 36 {
		closeLabel, expandLabel = "[x]", "[+]"
	}
	closeX := max(1, r.width-1-lipgloss.Width(closeLabel))
	expandX := max(1, closeX-1-lipgloss.Width(expandLabel))
	return closeLabel, closeX, expandLabel, expandX
}

// renderPopup composes an opaque panel over a bounded, dimmed dashboard. The
// same rectangle maps all input; background rows never receive modal events.
func (m Model) renderPopup(title, body string, scroll, total, visible, trackY int) string {
	r := m.popupBounds()
	if r.width == 0 || r.height == 0 {
		return ""
	}
	contentW, _ := m.popupContentSize()
	closeLabel, _, expandLabel, expandX := m.popupControls()
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	top := "┌" + popupFit(" "+title+" ", max(0, expandX-1)) + expandLabel + " " + closeLabel + "┐"
	top = popupFit(top, r.width)
	panel := make([]string, r.height)
	panel[0] = border.Render(top)
	bodyLines := strings.Split(body, "\n")
	for i := 1; i < r.height-1; i++ {
		line := ""
		if i-1 < len(bodyLines) {
			line = bodyLines[i-1]
		}
		mark := " "
		if total > visible && visible > 0 && i-1 >= trackY && i-1 < trackY+visible {
			position := i - 1 - trackY
			thumb := min(visible-1, max(0, scroll)*(visible-1)/max(1, total-visible))
			mark = "│"
			if position == thumb {
				mark = "█"
			}
		}
		panel[i] = popupFit(border.Render("│")+" "+popupFit(line, contentW)+border.Render(mark+"│"), r.width)
	}
	if r.height > 1 {
		panel[r.height-1] = popupFit(border.Render("└"+strings.Repeat("─", max(0, r.width-2))+"┘"), r.width)
	}
	background := strings.Split(m.renderDashboard(), "\n")
	result := make([]string, max(0, m.height))
	for y := range result {
		line := ""
		if y < len(background) {
			line = ansi.Strip(background[y])
		}
		line = popupFit(line, m.width)
		if y >= r.y && y < r.y+r.height {
			result[y] = styleDim.Render(ansi.Cut(line, 0, r.x)) + panel[y-r.y] + styleDim.Render(ansi.Cut(line, r.x+r.width, m.width))
		} else {
			result[y] = styleDim.Render(line)
		}
	}
	return strings.Join(result, "\n")
}

func (m Model) updatePopupMouse(event tea.MouseEvent) (tea.Model, tea.Cmd) {
	r := m.popupBounds()
	if event.Action == tea.MouseActionRelease {
		m.popupDragging = false
		return m, nil
	}
	if event.X < 0 || event.Y < 0 || event.X >= m.width || event.Y >= m.height {
		return m, nil
	}
	if r.width < 8 || r.height < 6 {
		return m, nil
	}
	press := event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft
	if !r.contains(event.X, event.Y) {
		if press {
			m.overlay = overlayState{}
		}
		return m, nil
	}
	if press && event.Y == r.y {
		_, closeX, _, expandX := m.popupControls()
		x := event.X - r.x
		if x >= closeX {
			m.overlay = overlayState{}
		} else if x >= expandX {
			m.popupExpanded = !m.popupExpanded
		}
		return m, nil
	}
	if event.X == r.x+r.width-2 && press || event.Action == tea.MouseActionMotion && m.popupDragging {
		m.popupDragging = true
		m.popupScrollTo(event.Y - r.y - 1)
		return m, nil
	}
	if event.Action == tea.MouseActionMotion {
		return m, nil
	}
	event.X -= r.x + 2
	event.Y -= r.y + 1
	contentW, contentH := m.popupContentSize()
	if event.X < 0 || event.Y < 0 || event.X >= contentW || event.Y >= contentH {
		return m, nil
	}
	if m.overlay.kind == overlayHelp {
		return m.updateHelpMouse(event)
	}
	return m.updateActionMenuMouse(event)
}

func (m *Model) popupScrollTo(y int) {
	if m.overlay.kind == overlayHelp {
		layout := m.helpLayout()
		loc := m.helpLocation()
		loc.scroll = max(0, min(layout.total-layout.visible, (y-layout.contentY)*max(0, layout.total-layout.visible)/max(1, layout.visible-1)))
		m.setHelpLocation(loc)
		return
	}
	layout := m.buildActionMenuLayout()
	visible, _, _ := m.actionMenuWindow()
	if m.overlay.body != "" && y < layout.firstOptionY {
		lines, _, _ := m.actionBodyWindow()
		m.overlay.scroll = max(0, min(len(lines)-1, y*len(lines)/max(1, layout.firstOptionY)))
	} else if len(visible) > 0 {
		_, h := m.popupContentSize()
		pos := max(0, min(len(visible)-1, (y-layout.firstOptionY)*(len(visible)-1)/max(1, h-layout.firstOptionY-2)))
		m.overlay.optionIndex = visible[pos]
	}
}
