package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Actions is the set of operations the dashboard can trigger. The TUI owns no
// domain logic of its own — it calls back into the same code paths the
// non-interactive commands use, so behaviour cannot diverge between them.
type Actions struct {
	// Reload re-reads the inventory.
	Reload func(ctx context.Context) ([]inventory.Row, error)
	// Open makes a task live and returns a short description of what happened.
	Open func(ctx context.Context, t *task.Task) (string, error)
	// Park closes a task's session and records its next action.
	Park func(ctx context.Context, t *task.Task, next string) (string, error)
	// SetNext records a task's next action without changing anything else.
	SetNext func(ctx context.Context, t *task.Task, next string) error
	// Runtime names the active backend, for the session column.
	Runtime runtime.Runtime
}

type mode int

const (
	modeList mode = iota
	modeEditNext
	modeConfirmPark
)

// Model is the dashboard state.
type Model struct {
	actions Actions
	rows    []inventory.Row
	cursor  int
	// filter restricts which states are shown; nil shows the default set.
	filter   []task.State
	showDone bool

	mode  mode
	input textinput.Model

	status string
	err    error

	width, height int
	quitting      bool
	// chosen is the directory to cd into on exit, when the user opened a task
	// under a runtime that cannot host sessions itself.
	chosen string
}

// New builds the dashboard.
func New(actions Actions, rows []inventory.Row) Model {
	in := textinput.New()
	in.Placeholder = "what to do when you come back"
	in.CharLimit = 200

	return Model{
		actions: actions,
		rows:    rows,
		input:   in,
		width:   100,
		height:  24,
	}
}

// Chosen reports a directory the caller should cd into after the program
// exits, or "" when there is none.
func (m Model) Chosen() string { return m.chosen }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return textinput.Blink }

// reloadMsg carries a refreshed inventory.
type reloadMsg struct {
	rows []inventory.Row
	err  error
}

// actionMsg carries the outcome of an operation.
type actionMsg struct {
	status string
	cd     string
	err    error
}

func (m Model) reload() tea.Cmd {
	return func() tea.Msg {
		rows, err := m.actions.Reload(context.Background())
		return reloadMsg{rows: rows, err: err}
	}
}

// visible applies the current filter.
func (m Model) visible() []inventory.Row {
	var out []inventory.Row
	for _, r := range m.rows {
		if len(m.filter) > 0 {
			if !containsState(m.filter, r.Task.State) {
				continue
			}
		} else if r.Task.State == task.Done && !m.showDone {
			continue
		}
		out = append(out, r)
	}
	return out
}

func containsState(list []task.State, s task.State) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (m Model) current() (inventory.Row, bool) {
	rows := m.visible()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return inventory.Row{}, false
	}
	return rows[m.cursor], true
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case reloadMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.rows, m.err = msg.rows, nil
		m.clampCursor()
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.status = nil, msg.status
		if msg.cd != "" {
			// Opening under a runtime with no sessions means the only useful
			// outcome is putting the user in the directory, which requires
			// leaving the alternate screen first.
			m.chosen, m.quitting = msg.cd, true
			return m, tea.Quit
		}
		return m, m.reload()

	case tea.KeyMsg:
		switch m.mode {
		case modeEditNext:
			return m.updateEditNext(msg)
		case modeConfirmPark:
			return m.updateConfirmPark(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.visible()
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(rows) - 1

	case "r":
		m.status = "refreshing…"
		return m, m.reload()

	case "1":
		m.filter, m.cursor = []task.State{task.Hot}, 0
	case "2":
		m.filter, m.cursor = []task.State{task.Warm}, 0
	case "3":
		m.filter, m.cursor = []task.State{task.Cold}, 0
	case "0":
		m.filter, m.cursor = nil, 0
	case "a":
		m.showDone, m.filter, m.cursor = !m.showDone, nil, 0

	case "enter", "o":
		row, ok := m.current()
		if !ok {
			return m, nil
		}
		t := row.Task
		return m, func() tea.Msg {
			status, err := m.actions.Open(context.Background(), t)
			cd := ""
			if m.actions.Runtime != nil && m.actions.Runtime.Name() == "none" {
				cd = checkoutOf(t)
			}
			return actionMsg{status: status, cd: cd, err: err}
		}

	case "p":
		if _, ok := m.current(); !ok {
			return m, nil
		}
		m.mode = modeConfirmPark
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink

	case "n":
		row, ok := m.current()
		if !ok {
			return m, nil
		}
		m.mode = modeEditNext
		m.input.SetValue(row.Task.Next)
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink
	}
	m.clampCursor()
	return m, nil
}

func (m Model) updateEditNext(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		row, ok := m.current()
		m.mode = modeList
		m.input.Blur()
		if !ok {
			return m, nil
		}
		next := strings.TrimSpace(m.input.Value())
		t := row.Task
		return m, func() tea.Msg {
			if err := m.actions.SetNext(context.Background(), t, next); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{status: "next action recorded for " + t.Title()}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) updateConfirmPark(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		row, ok := m.current()
		m.mode = modeList
		m.input.Blur()
		if !ok {
			return m, nil
		}
		next := strings.TrimSpace(m.input.Value())
		t := row.Task
		return m, func() tea.Msg {
			status, err := m.actions.Park(context.Background(), t, next)
			return actionMsg{status: status, err: err}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) clampCursor() {
	n := len(m.visible())
	switch {
	case n == 0:
		m.cursor = 0
	case m.cursor >= n:
		m.cursor = n - 1
	case m.cursor < 0:
		m.cursor = 0
	}
}

func checkoutOf(t *task.Task) string {
	if t.WorktreePath != "" {
		return t.WorktreePath
	}
	return t.RepoPath
}

// Summary counts the states, for the header line.
func (m Model) Summary() string {
	counts := map[task.State]int{}
	for _, r := range m.rows {
		counts[r.Task.State]++
	}
	var parts []string
	for _, s := range task.States {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d %s", s.Icon(), counts[s], s))
		}
	}
	sort.SliceStable(parts, func(i, j int) bool { return i < j })
	if len(parts) == 0 {
		return "no tasks"
	}
	return strings.Join(parts, "   ")
}

// contract shortens a path for display.
func contract(p string) string { return config.Contract(p) }
