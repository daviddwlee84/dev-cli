package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The dashboard resolved its own palette independently of dev's --color flag,
// so `dev --color never` left it fully colored. Rendering goes through
// lipgloss's default renderer, which SetColorEnabled switches.
//
// The profile is forced first: a test binary's stdout is not a terminal, so
// termenv would report Ascii either way and the assertion would hold for the
// wrong reason.
func TestSetColorEnabledStripsDashboardColor(t *testing.T) {
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.ColorProfile()) })

	lipgloss.SetColorProfile(termenv.TrueColor)
	colored := map[string]string{
		"hot":      styleHot.Render("HOT"),
		"drift":    styleDrift.Render("drift"),
		"selected": styleSelected.Render("row"),
		"ok":       styleOK.Render("clean"),
	}
	for name, got := range colored {
		if !strings.Contains(got, "\x1b[") {
			t.Fatalf("%s produced no color even with a color profile forced: %q", name, got)
		}
	}

	SetColorEnabled(false)
	for name := range colored {
		var got string
		switch name {
		case "hot":
			got = styleHot.Render("HOT")
		case "drift":
			got = styleDrift.Render("drift")
		case "selected":
			got = styleSelected.Render("row")
		case "ok":
			got = styleOK.Render("clean")
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("%s still emitted ANSI with color disabled: %q", name, got)
		}
	}
}
