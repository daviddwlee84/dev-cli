// Package triagetui is the independent local-work organizer.
package triagetui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

type Actions struct {
	Load        func(context.Context) (triage.Report, error)
	Prepare     func(context.Context, []triage.Item, string) (triage.Batch, error)
	Apply       func(context.Context, triage.Batch, string) (triage.Ledger, error)
	Intent      func(context.Context, triage.Item, string, time.Time) error
	Directories func(context.Context, triage.Item, []string) error
	Open        func(triage.Item, string) tea.Cmd
}
type Loaded struct {
	Generation int
	Report     triage.Report
	Err        error
}
type prepared struct {
	Generation int
	Batch      triage.Batch
	Err        error
}
type applied struct {
	Ledger triage.Ledger
	Err    error
}
type changed struct{ Err error }
type HandoffDone struct{ Err error }

type Model struct {
	actions                     Actions
	generation                  int
	report                      triage.Report
	visible                     []triage.Item
	rows                        []treeRow
	selected                    map[string]bool
	expanded                    map[string]bool
	cursor, width, height       int
	kind, filter, action, scope string
	deferred                    bool
	busy, message               string
	chooser                     bool
	choiceCursor                int
	menuItems                   []triage.Item
	buttonFocus                 bool
	batch                       *triage.Batch
	input                       textinput.Model
	inputMode                   string
	inputItem                   triage.Item
	scroll                      int
	stop                        context.CancelFunc
	quit                        bool
	touched                     []triage.Item
	lastLedger                  *triage.Ledger
	results                     bool
	resultCursor                int
	overlay                     []string
	ascii                       bool
}

func New(a Actions) Model {
	input := textinput.New()
	input.CharLimit = 4096
	input.Width = 60
	return Model{actions: a, generation: 1, selected: map[string]bool{}, expanded: map[string]bool{}, kind: "all", scope: "All local repositories and Tries", width: 100, height: 32, input: input, busy: "Reading local work…"}
}
func NewScoped(a Actions) Model { return New(a) }
func (m Model) WithScope(scope string) Model {
	if scope != "" {
		m.scope = scope
	}
	return m
}
func (m Model) WithASCII(ascii bool) Model { m.ascii = ascii; return m }
func (m Model) Touched() []triage.Item     { return append([]triage.Item{}, m.touched...) }
func (m Model) LastLedger() *triage.Ledger {
	if m.lastLedger == nil {
		return nil
	}
	copy := *m.lastLedger
	copy.Outcomes = append([]triage.Outcome{}, copy.Outcomes...)
	return &copy
}
func (m Model) Init() tea.Cmd { return m.load() }
func (m Model) load() tea.Cmd {
	return func() tea.Msg { r, e := m.actions.Load(context.Background()); return Loaded{m.generation, r, e} }
}
func (m Model) current() (triage.Item, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return triage.Item{}, false
	}
	return m.rows[m.cursor].item, true
}
func (m Model) currentMembers() []triage.Item {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].members
}
func (m Model) chosen() []triage.Item {
	items := []triage.Item{}
	for _, i := range m.report.Items {
		if m.selected[i.ID] {
			items = append(items, i)
		}
	}
	return items
}
func (m Model) reload() (tea.Model, tea.Cmd) {
	m.generation++
	m.batch = nil
	m.busy = "Refreshing this scope…"
	return m, m.load()
}
func (m Model) openChooser() (tea.Model, tea.Cmd) {
	m.menuItems = m.chosen()
	if len(m.menuItems) == 0 {
		m.menuItems = append([]triage.Item{}, m.currentMembers()...)
	}
	m.chooser = true
	m.choiceCursor = 0
	m.message = ""
	return m, nil
}
func (m Model) prepareAction(action string) (tea.Model, tea.Cmd) {
	items := append([]triage.Item{}, m.menuItems...)
	m.action = action
	m.chooser = false
	m.busy = "Checking exact targets and effects…"
	return m, func() tea.Msg {
		b, e := m.actions.Prepare(context.Background(), items, action)
		return prepared{m.generation, b, e}
	}
}
func (m Model) apply() (tea.Model, tea.Cmd) {
	if m.batch == nil || m.batch.ReadyCount() == 0 {
		m.message = "No ready targets. Review blockers or go back."
		return m, nil
	}
	b := *m.batch
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	m.busy = fmt.Sprintf("Applying %d targets; Esc stops the remaining queue…", b.ReadyCount())
	return m, func() tea.Msg { defer cancel(); l, e := m.actions.Apply(ctx, b, b.Token); return applied{l, e} }
}
func (m Model) openItem(item triage.Item, kind string) (tea.Model, tea.Cmd) {
	if m.actions.Open == nil {
		return m, nil
	}
	m.touched = append(m.touched, item)
	return m, m.actions.Open(item, kind)
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.input.Width = max(10, min(65, v.Width-10))
		return m, nil
	case Loaded:
		if v.Generation != 0 && v.Generation != m.generation {
			return m, nil
		}
		key := m.rowKey()
		m.busy = ""
		m.report = v.Report
		m.batch = nil
		kept := map[string]bool{}
		for _, i := range v.Report.Items {
			if m.selected[i.ID] {
				kept[i.ID] = true
			}
		}
		m.selected = kept
		m.filterRows()
		m.focusRow(key)
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		if len(v.Report.Items) == 0 {
			for _, s := range v.Report.Sources {
				if !s.Complete {
					m.message = s.Detail
					break
				}
			}
		}
		return m, nil
	case prepared:
		if v.Generation != m.generation {
			return m, nil
		}
		m.busy = ""
		m.scroll = 0
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
			return m, nil
		}
		m.batch = &v.Batch
		m.inputMode = ""
		if v.Batch.Token != "" {
			m.inputMode = "confirm"
			m.input.SetValue("")
			m.input.Focus()
			return m, textinput.Blink
		}
		return m, nil
	case applied:
		for _, o := range v.Ledger.Outcomes {
			if o.Status == "skipped" || o.Status == "canceled" {
				continue
			}
			for _, i := range m.report.Items {
				if i.ID == o.ItemID {
					m.touched = append(m.touched, i)
				}
			}
		}
		m.stop = nil
		m.batch = nil
		m.inputMode = ""
		m.lastLedger = &v.Ledger
		m.results = true
		m.resultCursor = 0
		m.scroll = 0
		m.message = ""
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		if m.quit {
			return m, tea.Quit
		}
		return m.reload()
	case changed:
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		return m.reload()
	case HandoffDone:
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		return m.reload()
	case tea.MouseMsg:
		return m.updateMouse(tea.MouseEvent(v))
	case tea.KeyMsg:
		key := v.String()
		if m.busy != "" && !m.results {
			if key == "q" || key == "ctrl+c" {
				if m.stop != nil {
					m.quit = true
					m.stop()
				} else {
					return m, tea.Quit
				}
			}
			if key == "esc" && m.stop != nil {
				m.stop()
				m.message = "Stopping after the current operation"
			}
			return m, nil
		}
		if m.overlay != nil {
			switch key {
			case "esc", "enter":
				m.overlay = nil
				m.scroll = 0
			case "q", "ctrl+c":
				return m, tea.Quit
			case "j", "down":
				m.scroll++
			case "k", "up":
				m.scroll = max(0, m.scroll-1)
			case "pgdown":
				m.scroll += max(1, m.height/2)
			case "pgup":
				m.scroll = max(0, m.scroll-m.height/2)
			}
			return m, nil
		}
		if m.batch != nil {
			switch key {
			case "esc":
				m.batch = nil
				m.inputMode = ""
				m.scroll = 0
				return m, nil
			case "pgdown":
				m.scroll += max(1, m.height/2)
				return m, nil
			case "pgup":
				m.scroll = max(0, m.scroll-m.height/2)
				return m, nil
			}
			if m.inputMode == "confirm" {
				if key == "enter" {
					if m.input.Value() != m.batch.Token {
						m.message = "Type the displayed confirmation exactly"
						return m, nil
					}
					return m.apply()
				}
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(v)
				return m, cmd
			}
			if key == "y" || (key == "enter" && m.buttonFocus) {
				return m.apply()
			}
			if key == "tab" {
				m.buttonFocus = !m.buttonFocus
			}
			return m, nil
		}
		if m.results {
			return m.updateResults(key)
		}
		if m.inputMode != "" {
			if key == "esc" {
				m.inputMode = ""
				return m, nil
			}
			if key == "enter" {
				value, mode, item := m.input.Value(), m.inputMode, m.inputItem
				m.inputMode = ""
				if mode == "filter" {
					m.filter = value
					m.cursor = 0
					m.filterRows()
					return m, nil
				}
				if mode == "directories" {
					dirs := []string{}
					for _, d := range strings.Split(value, ",") {
						if strings.TrimSpace(d) != "" {
							dirs = append(dirs, strings.TrimSpace(d))
						}
					}
					m.busy = "Saving disposable-directory intent…"
					m.touched = append(m.touched, item)
					return m, func() tea.Msg { return changed{m.actions.Directories(context.Background(), item, dirs)} }
				}
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(v)
			return m, cmd
		}
		if m.chooser {
			choices := m.choices()
			switch key {
			case "esc":
				m.chooser = false
			case "q", "ctrl+c":
				return m, tea.Quit
			case "j", "down":
				m.choiceCursor = min(max(0, len(choices)-1), m.choiceCursor+1)
			case "k", "up":
				m.choiceCursor = max(0, m.choiceCursor-1)
			case "enter":
				if len(choices) > 0 {
					return m.prepareAction(choices[m.choiceCursor].name)
				}
			}
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.cursor = min(max(0, len(m.rows)-1), m.cursor+1)
			m.buttonFocus = false
		case "k", "up":
			m.cursor = max(0, m.cursor-1)
			m.buttonFocus = false
		case "pgdown":
			m.cursor = min(max(0, len(m.rows)-1), m.cursor+max(1, m.height/2))
		case "pgup":
			m.cursor = max(0, m.cursor-m.height/2)
		case " ":
			m.toggleRow(m.cursor)
		case "ctrl+a", "a":
			m.toggleVisible()
		case "n":
			m.selected = map[string]bool{}
		case "right", "l":
			m.expandCurrent(true)
		case "left", "h":
			m.expandCurrent(false)
		case "tab":
			m.buttonFocus = !m.buttonFocus
		case "enter":
			if m.buttonFocus {
				return m.openChooser()
			}
			m.showDetails()
		case "?":
			m.overlay = helpLines()
			m.scroll = 0
		case "A", "ctrl+o":
			return m.openChooser()
		case "g":
			switch m.kind {
			case "all":
				m.kind = "repo"
			case "repo":
				m.kind = "try"
			default:
				m.kind = "all"
			}
			m.filterRows()
		case "d":
			m.deferred = !m.deferred
			m.filterRows()
		case "/":
			m.inputMode = "filter"
			m.input.SetValue(m.filter)
			m.input.Focus()
			return m, textinput.Blink
		case "r":
			return m.reload()
		case "b":
			if m.lastLedger != nil {
				m.results = true
				m.resultCursor = 0
			}
		case "R":
			if i, ok := m.current(); ok {
				m.inputMode = "directories"
				m.inputItem = i
				m.input.SetValue(strings.Join(i.DisposableDirs, ", "))
				m.input.Focus()
				return m, textinput.Blink
			}
		case "L", "s", "U":
			if i, ok := m.current(); ok {
				kind := "local"
				until := time.Time{}
				if key == "s" {
					kind = "snooze"
					until = time.Now().Add(7 * 24 * time.Hour)
				}
				if key == "U" {
					kind = ""
				}
				m.touched = append(m.touched, i)
				m.busy = "Saving review intent…"
				return m, func() tea.Msg { return changed{m.actions.Intent(context.Background(), i, kind, until)} }
			}
		case "o", "e", "v":
			if i, ok := m.current(); ok {
				kind := "flow"
				if key == "e" {
					kind = "shell"
				}
				if key == "v" {
					kind = "runtime"
				}
				return m.openItem(i, kind)
			}
		}
	}
	return m, nil
}
func (m Model) updateResults(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.results = false
		m.message = ""
		m.scroll = 0
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.resultCursor = min(max(0, len(m.lastLedger.Outcomes)-1), m.resultCursor+1)
	case "k", "up":
		m.resultCursor = max(0, m.resultCursor-1)
	case "enter", "?":
		m.showResultDetails()
	case "r":
		m.results = false
		return m.reload()
	case "e":
		if m.busy != "" {
			return m, nil
		}
		if i, ok := m.resultItem(); ok {
			return m.openItem(i, "shell")
		}
	case "A", "ctrl+o":
		if m.busy != "" {
			return m, nil
		}
		if i, ok := m.resultItem(); ok {
			m.results = false
			m.menuItems = []triage.Item{i}
			m.chooser = true
			m.choiceCursor = 0
		}
	}
	return m, nil
}
func (m Model) resultItem() (triage.Item, bool) {
	if m.lastLedger == nil || m.resultCursor >= len(m.lastLedger.Outcomes) {
		return triage.Item{}, false
	}
	id := m.lastLedger.Outcomes[m.resultCursor].ItemID
	for _, i := range m.report.Items {
		if i.ID == id {
			return i, true
		}
	}
	for _, i := range m.touched {
		if i.ID == id {
			return i, true
		}
	}
	return triage.Item{}, false
}
