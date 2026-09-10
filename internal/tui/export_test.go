package tui

import tea "github.com/charmbracelet/bubbletea"

// FleetConfigEditedForTest builds the message the editor callback returns when
// the user leaves their editor, so a test can drive the validate-then-refresh
// path without spawning one. Keeping it here rather than exporting the message
// type leaves the production surface unchanged.
func FleetConfigEditedForTest(err error) tea.Msg { return fleetConfigEditedMsg{err: err} }

// WithReposForTest applies a fresh local repository snapshot without exposing
// model internals in the production API.
func (m Model) WithReposForTest(rows []RepoRow) Model {
	m.repos = append([]RepoRow(nil), rows...)
	m.matchRemoteLocals()
	return m
}

func (m Model) ToolsMenuForTest() Model { n, _ := m.runListAction(listActionTools); return n.(Model) }
func (m Model) StatusDetailsForTest() Model {
	n, _ := m.runListAction(listActionStatusDetails)
	return n.(Model)
}
