package tui

import tea "github.com/charmbracelet/bubbletea"

const mouseWheelRows = 3

// mouseFrameFits fails closed when Bubble Tea would drop top lines from an
// over-height frame. Screen-relative mouse coordinates cannot safely target
// logical header/list rows once that clipping occurs.
func (m Model) mouseFrameFits() bool {
	if m.height <= 0 {
		return false
	}
	if m.overlay.kind == overlayActionMenu {
		return lineCount(m.renderOverlay()) <= m.height
	}

	total := 2 + lineCount(m.renderCurrentList()) + 1 + lineCount(m.renderDetail()) + 1 + lineCount(m.renderFooter())
	return total <= m.height
}

func (m Model) updateMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(message)
	if event.Shift || event.Alt || event.Ctrl {
		return m, nil
	}
	verticalWheel := event.Button == tea.MouseButtonWheelUp || event.Button == tea.MouseButtonWheelDown
	if m.overlay.kind == overlayActionMenu {
		menuPress := event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft
		if (!verticalWheel && !menuPress) || !m.mouseFrameFits() {
			return m, nil
		}
		return m.updateActionMenuMouse(event)
	}
	if m.noteMode() || m.overlay.kind != overlayNone || m.mode != modeList {
		return m, nil
	}
	rowPress := event.Action == tea.MouseActionPress &&
		(event.Button == tea.MouseButtonLeft || event.Button == tea.MouseButtonRight)
	if (!verticalWheel && !rowPress) || !m.mouseFrameFits() {
		return m, nil
	}

	if verticalWheel {
		if !m.mouseOverList(event.X, event.Y) {
			return m, nil
		}
		delta := mouseWheelRows
		if event.Button == tea.MouseButtonWheelUp {
			delta = -delta
		}
		m.setAt(m.at() + delta)
		return m, nil
	}
	if event.Action != tea.MouseActionPress {
		return m, nil
	}

	if event.Button == tea.MouseButtonLeft && event.Y == 0 {
		for _, hit := range m.buildHeaderLayout().tabs {
			if event.X < hit.from || event.X >= hit.to {
				continue
			}
			if m.view == hit.view {
				return m, nil
			}
			m.view = hit.view
			return m.afterViewSwitch()
		}
		return m, nil
	}

	row, ok := m.mouseRow(event.X, event.Y)
	if !ok {
		return m, nil
	}
	switch event.Button {
	case tea.MouseButtonLeft:
		m.setAt(row)
	case tea.MouseButtonRight:
		m.setAt(row)
		if !m.remoteClone.active() {
			m = m.openActionMenu()
		}
	}
	return m, nil
}

func (m Model) mouseRow(x, y int) (int, bool) {
	if x < 0 || x >= m.width {
		return 0, false
	}
	count := m.count()
	from, to := m.window(count)
	first := 2 + m.listPreambleLines()
	if y < first || y >= first+(to-from) {
		return 0, false
	}
	return from + y - first, true
}

func (m Model) mouseOverList(x, y int) bool {
	if x < 0 || x >= m.width || m.count() == 0 {
		return false
	}
	from, to := m.window(m.count())
	first := 2 + m.listPreambleLines()
	return y >= 2 && y < first+(to-from)
}

func (m Model) updateActionMenuMouse(event tea.MouseEvent) (tea.Model, tea.Cmd) {
	if event.Button == tea.MouseButtonWheelUp || event.Button == tea.MouseButtonWheelDown {
		if m.overlay.optionCount == 0 {
			return m, nil
		}
		delta := 1
		if event.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		m.moveActionMenu(delta)
		return m, nil
	}
	if event.Action != tea.MouseActionPress || event.Button != tea.MouseButtonLeft {
		return m, nil
	}
	index, ok := m.actionMenuOptionAt(event.X, event.Y)
	if !ok {
		m.overlay = overlayState{}
		return m, nil
	}
	m.overlay.optionIndex = index
	return m.runOverlayAction()
}
