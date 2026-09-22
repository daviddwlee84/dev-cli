package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ReleaseObservation is presentation data supplied by the CLI's release service.
// Newer is already compared with the running build; the dashboard never infers
// that an unknown or development build is current.
type ReleaseObservation struct {
	Latest    string
	CheckedAt time.Time
	Newer     bool
	Stale     bool
	Err       error
}

// ReleaseActions retains the local startup observation and one bounded check.
// Check must honor its context and the current update-check configuration.
type ReleaseActions struct {
	Cached ReleaseObservation
	Check  func(context.Context) ReleaseObservation
}

type releaseUIState struct {
	version     string
	enabled     bool
	observation ReleaseObservation
	generation  uint64
	attempted   bool
	loading     bool
	cancel      context.CancelFunc
}

type releaseCheckReadyMsg struct{ generation uint64 }

type releaseObservationMsg struct {
	generation  uint64
	observation ReleaseObservation
}

// WithVersion supplies the running build independently of optional networking.
func (m Model) WithVersion(version string, enabled bool) Model {
	m.release = releaseUIState{
		version: version, enabled: enabled, generation: 1,
		observation: m.actions.Release.Cached,
		attempted:   !m.actions.Release.Cached.Stale,
	}
	return m
}

func (m Model) requestReleaseCheck() tea.Cmd {
	if !m.release.enabled || m.release.attempted || m.release.loading || m.actions.Release.Check == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-m.baseContext().Done():
			return nil
		case <-m.firstViewReady:
			return releaseCheckReadyMsg{generation: m.release.generation}
		}
	}
}

func (m Model) startReleaseCheck(msg releaseCheckReadyMsg) (Model, tea.Cmd) {
	if msg.generation != m.release.generation || !m.release.enabled || m.release.attempted || m.release.loading || m.actions.Release.Check == nil {
		return m, nil
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.release.cancel, m.release.loading, m.release.attempted = cancel, true, true
	check, generation := m.actions.Release.Check, m.release.generation
	return m, func() tea.Msg {
		observation := check(ctx)
		if err := ctx.Err(); err != nil {
			observation.Err = err
		}
		return releaseObservationMsg{generation: generation, observation: observation}
	}
}

func (m Model) acceptReleaseObservation(msg releaseObservationMsg) (Model, tea.Cmd) {
	if msg.generation != m.release.generation || !m.release.enabled || !m.release.loading {
		return m, nil
	}
	if m.release.cancel != nil {
		m.release.cancel()
	}
	m.release.cancel, m.release.loading = nil, false
	if !errors.Is(msg.observation.Err, context.Canceled) {
		m.release.observation = msg.observation
	}
	return m, nil
}

// applyReleaseChecks is called only after a config generation is accepted. An
// old callback cannot republish a hint after checks have been disabled. Unrelated
// reloads retain completed observations instead of checking the network again.
func (m *Model) applyReleaseChecks(enabled bool) tea.Cmd {
	retry := m.release.loading || enabled != m.release.enabled
	if m.release.cancel != nil {
		m.release.cancel()
	}
	m.release.cancel, m.release.loading = nil, false
	m.release.generation++
	m.release.enabled = enabled
	if retry {
		m.release.attempted = false
	}
	return m.requestReleaseCheck()
}

func (m *Model) resumeReleaseCheck() tea.Cmd {
	interrupted := m.release.loading
	if m.release.cancel != nil {
		m.release.cancel()
	}
	m.release.cancel, m.release.loading = nil, false
	m.release.generation++
	if interrupted {
		m.release.attempted = false
	}
	// A native terminal handoff starts a new Bubble Tea Program. Even resumed
	// checks must wait for its first frame, using a fresh channel/once pair.
	m.firstViewReady, m.firstViewOnce = make(chan struct{}), &sync.Once{}
	return m.requestReleaseCheck()
}

func (m Model) renderReleaseStatus() string {
	if m.release.version == "" {
		return ""
	}
	text := "dev " + m.release.version
	observation := m.release.observation
	if m.release.enabled && observation.Newer && observation.Latest != "" {
		text += " · " + observation.Latest + " available"
		if observation.Stale {
			text += " (cached)"
		}
		text += " · dev upgrade"
	}
	return "  " + fitCell(styleDim.Render(strings.ReplaceAll(text, "\n", " ")), max(1, m.width-4))
}
