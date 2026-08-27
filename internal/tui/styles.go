// Package tui is the interactive dashboard behind a bare `dev`.
//
// It renders the same inventory that `dev ls` prints, from the same
// inventory.Collect call, so the two can never disagree. Its job is the part a
// static listing cannot do: let you act on what you are looking at without
// retyping a task name.
package tui

import "github.com/charmbracelet/lipgloss"

// Colours are chosen from the 256-colour cube rather than truecolour so the
// dashboard degrades sensibly in a basic terminal, and lipgloss adapts them to
// the background it detects.
var (
	styleTitle = lipgloss.NewStyle().Bold(true)
	styleDim   = lipgloss.NewStyle().Faint(true)
	styleHelp  = lipgloss.NewStyle().Faint(true)
	styleErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))

	styleSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	styleHeader   = lipgloss.NewStyle().Faint(true).Bold(true)

	styleHot  = lipgloss.NewStyle().Foreground(lipgloss.Color("209"))
	styleWarm = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleCold = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	styleDone = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))

	styleDirty = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	styleClean = lipgloss.NewStyle().Faint(true)
	styleLive  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	styleDrift = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
