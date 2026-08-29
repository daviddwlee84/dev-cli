package tui

import tea "github.com/charmbracelet/bubbletea"

// FleetConfigEditedForTest builds the message the editor callback returns when
// the user leaves their editor, so a test can drive the validate-then-refresh
// path without spawning one. Keeping it here rather than exporting the message
// type leaves the production surface unchanged.
func FleetConfigEditedForTest(err error) tea.Msg { return fleetConfigEditedMsg{err: err} }
