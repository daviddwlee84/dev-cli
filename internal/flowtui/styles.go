package flowtui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleMuted    = lipgloss.NewStyle().Faint(true)
	styleSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	styleGood     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	styleBad      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleHot      = lipgloss.NewStyle().Foreground(lipgloss.Color("209"))
	styleWarm     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleCold     = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	styleDone     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
)

// SetColorEnabled switches the package renderer between the terminal profile
// and ASCII. Status text remains present in either mode.
func SetColorEnabled(enabled bool) {
	if enabled {
		lipgloss.SetColorProfile(termenv.ColorProfile())
		return
	}
	lipgloss.SetColorProfile(termenv.Ascii)
}
