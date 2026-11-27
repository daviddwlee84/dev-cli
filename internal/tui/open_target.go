package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// OpenTarget is one place Enter can open a checkout: the configured runtime,
// another installed backend or GUI, or a plain cd in the calling shell.
type OpenTarget struct {
	ID    string
	Label string
	// Detail is a short hint shown beside the label.
	Detail string
	// Default marks the target Enter confirms inside the picker.
	Default bool
}

// Open target IDs the dashboard itself understands. Others are opaque and
// interpreted by the injected Open* callbacks.
const (
	OpenTargetRuntime = "runtime"
	OpenTargetCD      = "cd"
)

type openTargetKey struct{}

// WithOpenTarget records the chosen Open target for the Open* callbacks.
func WithOpenTarget(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, openTargetKey{}, id)
}

// OpenTargetFrom returns the Open target chosen for this call, or "" for the
// configured runtime.
func OpenTargetFrom(ctx context.Context) string {
	id, _ := ctx.Value(openTargetKey{}).(string)
	return id
}

// beginOpen asks where to open when more than one target is available, and
// otherwise opens in the configured runtime at once. Fleet rows keep their
// own navigation and never ask.
func (m Model) beginOpen() (tea.Model, tea.Cmd) {
	if _, fleet := m.currentFleet(); fleet || m.actions.OpenTargets == nil || !m.openTargetsApply() {
		return m, m.openSelected()
	}
	targets := m.actions.OpenTargets()
	if len(targets) < 2 {
		id := ""
		if len(targets) == 1 {
			id = targets[0].ID
		}
		return m, m.openSelectedIn(id)
	}
	token, ok := m.currentSelectionToken()
	if !ok {
		return m, m.openSelected()
	}
	m.openTargets, m.openSelection = targets, token
	m.mode, m.err, m.status = modeOpenTarget, nil, ""
	return m, nil
}

// openTargetsApply limits the picker to rows that open a local directory.
func (m Model) openTargetsApply() bool {
	if _, ok := m.currentTask(); ok {
		return true
	}
	if _, ok := m.currentRepoItem(); ok {
		return true
	}
	if r, ok := m.currentTry(); ok {
		return r.Present()
	}
	if r, ok := m.currentRemote(); ok {
		return r.Cloned()
	}
	return false
}

func (m Model) updateOpenTarget(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	targets, token := m.openTargets, m.openSelection
	m.mode, m.openTargets, m.openSelection = modeList, nil, selectionToken{}
	key := msg.String()
	if key == "esc" || key == "q" {
		return m, nil
	}
	choice := -1
	switch key {
	case "enter", "o":
		choice = 0
		for i, t := range targets {
			if t.Default {
				choice = i
				break
			}
		}
	case "C":
		for i, t := range targets {
			if t.ID == OpenTargetCD {
				choice = i
			}
		}
	default:
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(targets) {
			choice = n - 1
		}
	}
	if choice < 0 {
		m.status = "open cancelled"
		return m, nil
	}
	if token.key == "" || !m.selectToken(token) {
		m.err = fmt.Errorf("selected row changed before choosing where to open it")
		return m, nil
	}
	m.status = "opening in " + targets[choice].Label + "…"
	return m, m.openSelectedIn(targets[choice].ID)
}

func (m Model) renderOpenTargets() string {
	var b strings.Builder
	b.WriteString("  " + styleTitle.Render("open in"))
	for i, t := range m.openTargets {
		line := fmt.Sprintf("\n  %d  %s", i+1, t.Label)
		if t.Detail != "" {
			line += "  " + styleDim.Render(t.Detail)
		}
		if t.Default {
			line += "  " + styleDim.Render("(enter)")
		}
		b.WriteString(line)
	}
	b.WriteString("\n  " + styleHelp.Render("1-9 choose · enter default · esc cancel"))
	return b.String()
}
