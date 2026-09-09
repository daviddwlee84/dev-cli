// Package triagetui is the independent cross-repository triage interface.
package triagetui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/triage"
	"github.com/mattn/go-runewidth"
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
	generation            int
	actions               Actions
	report                triage.Report
	visible               []triage.Item
	selected              map[string]bool
	cursor, width, height int
	quick, deferred       bool
	kind, filter, action  string
	busy                  string
	message               string
	batch                 *triage.Batch
	input                 textinput.Model
	inputMode             string
	inputItem             triage.Item
	scroll                int
	stop                  context.CancelFunc
	lastLedger            *triage.Ledger
	overlay               []string
	quit                  bool
}

func New(a Actions) Model {
	input := textinput.New()
	input.CharLimit = 4096
	input.Width = 65
	return Model{generation: 1, actions: a, selected: map[string]bool{}, action: "fetch", kind: "all", width: 100, height: 30, input: input, busy: "Scanning local repositories…"}
}
func (m Model) Init() tea.Cmd { return m.load() }
func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		r, e := m.actions.Load(context.Background())
		return Loaded{Generation: m.generation, Report: r, Err: e}
	}
}
func (m *Model) filterRows() {
	m.visible = nil
	for _, i := range m.report.Items {
		if !m.deferred && i.Deferred != "" {
			continue
		}
		if m.kind != "all" && i.Kind != m.kind {
			continue
		}
		text := i.Name + " " + i.Path
		for _, f := range i.Findings {
			text += " " + f.Code + " " + f.Detail
		}
		if i.Branch != nil {
			text += " " + i.Branch.Ref
		}
		if !strings.Contains(strings.ToLower(text), strings.ToLower(m.filter)) {
			continue
		}
		if m.quick {
			if _, ok := i.Action(m.action); !ok {
				continue
			}
		}
		m.visible = append(m.visible, i)
	}
	triage.SortItems(m.visible, m.quick)
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}
func (m Model) current() (triage.Item, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return triage.Item{}, false
	}
	return m.visible[m.cursor], true
}
func (m Model) chosen() []triage.Item {
	items := []triage.Item{}
	for _, i := range m.report.Items {
		if m.selected[i.ID] {
			items = append(items, i)
		}
	}
	if len(items) == 0 {
		if i, ok := m.current(); ok {
			items = append(items, i)
		}
	}
	return items
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.input.Width = max(10, min(80, v.Width-8))
		return m, nil
	case Loaded:
		if v.Generation != 0 && v.Generation != m.generation {
			return m, nil
		}
		old, _ := m.current()
		m.busy = ""
		m.report = v.Report
		m.batch = nil
		m.selected = map[string]bool{}
		m.filterRows()
		for n, i := range m.visible {
			if i.ID == old.ID {
				m.cursor = n
			}
		}
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		return m, nil
	case prepared:
		if v.Generation != m.generation {
			return m, nil
		}
		m.busy = ""
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		} else {
			m.batch = &v.Batch
			m.scroll = 0
			if v.Batch.Token != "" {
				m.inputMode = "confirm"
				m.input.SetValue("")
				m.input.Focus()
			}
		}
		return m, nil
	case applied:
		m.busy = ""
		m.stop = nil
		m.batch = nil
		m.inputMode = ""
		m.lastLedger = &v.Ledger
		m.overlay = ledgerLines(v.Ledger)
		m.scroll = 0
		m.message = fmt.Sprintf("Results saved: %s", v.Ledger.Path)
		for _, o := range v.Ledger.Outcomes {
			if o.Status != "completed" {
				m.message += " · " + o.Status + ": " + o.Error
			}
		}
		if v.Err != nil {
			m.message += " · " + v.Err.Error()
		}
		if m.quit {
			return m, tea.Quit
		}
		m.busy = "Refreshing local facts…"
		m.generation++
		return m, m.load()
	case changed:
		m.busy = ""
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		m.busy = "Refreshing local facts…"
		m.generation++
		return m, m.load()
	case HandoffDone:
		if v.Err != nil {
			m.message = triage.SafeText(v.Err.Error())
		}
		m.busy = "Refreshing local facts…"
		m.generation++
		return m, m.load()
	case tea.KeyMsg:
		key := v.String()
		if m.busy != "" {
			if key == "q" || key == "ctrl+c" {
				if m.stop != nil {
					m.quit = true
					m.stop()
					m.message = "Will exit after the current operation returns"
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
			case "pgdown", "down", "j":
				m.scroll += max(1, m.height/2)
			case "pgup", "up", "k":
				m.scroll = max(0, m.scroll-max(1, m.height/2))
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		if m.batch != nil {
			if key == "esc" {
				m.batch = nil
				m.inputMode = ""
				return m, nil
			}
			if key == "pgdown" {
				m.scroll += max(1, m.height-10)
				return m, nil
			}
			if key == "pgup" {
				m.scroll = max(0, m.scroll-max(1, m.height-10))
				return m, nil
			}
			if m.inputMode == "confirm" {
				if key == "enter" {
					if m.input.Value() != m.batch.Token {
						m.message = "Confirmation does not match"
						return m, nil
					}
					return m.apply()
				}
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(v)
				return m, cmd
			}
			if key == "y" {
				return m.apply()
			}
			return m, nil
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
					m.busy = "Saving explicitly disposable directories…"
					dirs := []string{}
					for _, d := range strings.Split(value, ",") {
						if strings.TrimSpace(d) != "" {
							dirs = append(dirs, strings.TrimSpace(d))
						}
					}
					return m, func() tea.Msg { return changed{m.actions.Directories(context.Background(), item, dirs)} }
				}
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(v)
			return m, cmd
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.cursor = min(max(0, len(m.visible)-1), m.cursor+1)
		case "k", "up":
			m.cursor = max(0, m.cursor-1)
		case " ":
			if i, ok := m.current(); ok {
				m.selected[i.ID] = !m.selected[i.ID]
			}
		case "a":
			for _, i := range m.visible {
				if a, ok := i.Action(m.action); ok && a.Availability == "candidate" {
					m.selected[i.ID] = true
				}
			}
		case "n":
			m.selected = map[string]bool{}
		case "tab":
			m.quick = !m.quick
			m.cursor = 0
			m.selected = map[string]bool{}
			m.filterRows()
		case "g":
			switch m.kind {
			case "all":
				m.kind = "repo"
			case "repo":
				m.kind = "try"
			default:
				m.kind = "all"
			}
			m.cursor = 0
			m.filterRows()
		case "d":
			m.deferred = !m.deferred
			m.filterRows()
		case "r":
			m.busy = "Refreshing local facts…"
			m.busy = "Refreshing local facts…"
			m.generation++
			return m, m.load()
		case "/":
			m.inputMode = "filter"
			m.input.SetValue(m.filter)
			m.input.Focus()
			return m, textinput.Blink
		case "?":
			if i, ok := m.current(); ok {
				m.overlay = []string{i.Kind + " / " + i.Scope + " · " + i.Path}
				if i.Branch != nil {
					m.overlay = append(m.overlay, fmt.Sprintf("%s @ %s; upstream %s @ %s", i.Branch.Ref, i.Branch.OID, i.Branch.Upstream, i.Branch.UpstreamOID))
				}
				for _, f := range i.Findings {
					m.overlay = append(m.overlay, f.Code+": "+f.Detail)
				}
				for _, rt := range i.Runtimes {
					m.overlay = append(m.overlay, "runtime: "+rt.Backend+":"+rt.Handle+" "+rt.Label)
				}
				for _, a := range i.Actions {
					m.overlay = append(m.overlay, fmt.Sprintf("%s %s [%s] %s", a.Name, a.Remote, a.Availability, a.Reason))
				}
				for _, p := range i.Ignored {
					m.overlay = append(m.overlay, fmt.Sprintf("ignored: %s (%d bytes, disposable=%t)", p.Path, p.Bytes, p.Disposable))
				}
				m.scroll = 0
			}
		case "b":
			if m.lastLedger != nil {
				m.overlay = ledgerLines(*m.lastLedger)
				m.scroll = 0
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
				m.busy = "Saving triage intent…"
				return m, func() tea.Msg { return changed{m.actions.Intent(context.Background(), i, kind, until)} }
			}
		case "o", "e", "v":
			if i, ok := m.current(); ok && m.actions.Open != nil {
				kind := "flow"
				if key == "e" {
					kind = "shell"
				}
				if key == "v" {
					kind = "runtime"
				}
				return m, m.actions.Open(i, kind)
			}
		case "f", "p", "u", "w", "c", "t", "x":
			names := map[string]string{"f": "fetch", "p": "push", "u": "fast-forward", "w": "park-warm", "c": "park-cold", "t": "retire", "x": "remove-checkout"}
			m.action = names[key]
			m.selected = map[string]bool{}
			m.filterRows()
		case "enter":
			items := m.chosen()
			action := m.action
			m.busy = "Building exact plans…"
			return m, func() tea.Msg {
				b, e := m.actions.Prepare(context.Background(), items, action)
				return prepared{Generation: m.generation, Batch: b, Err: e}
			}
		}
	}
	return m, nil
}
func (m Model) apply() (tea.Model, tea.Cmd) {
	if m.batch == nil || m.batch.ReadyCount() == 0 {
		m.message = "No ready plans in this selection"
		return m, nil
	}
	b := *m.batch
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	m.busy = fmt.Sprintf("Applying %d plans; Esc stops the remaining queue…", b.ReadyCount())
	token := b.Token
	return m, func() tea.Msg { defer cancel(); l, e := m.actions.Apply(ctx, b, token); return applied{l, e} }
}

var title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var highlight = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229"))

func (m Model) View() string {
	var b strings.Builder
	view := "Find forgotten work"
	if m.quick {
		view = "Quick batch"
	}
	fmt.Fprintln(&b, title.Render("dev triage  /  "+view))
	fmt.Fprintf(&b, "%s · action: %s · %d selected · %d visible\n", m.kind, m.action, lenSelected(m.selected), len(m.visible))
	if m.busy != "" {
		fmt.Fprintln(&b, highlight.Render(m.busy))
	}
	if m.overlay != nil {
		start := min(m.scroll, max(0, len(m.overlay)-1))
		end := min(len(m.overlay), start+max(2, m.height-8))
		for _, line := range m.overlay[start:end] {
			fmt.Fprintln(&b, fit(triage.SafeText(line), m.width-2))
		}
		fmt.Fprintln(&b, "\nPgUp/PgDn scroll · Enter/Esc return")
		return b.String()
	}
	if m.batch != nil {
		fmt.Fprintf(&b, "\nPreview · %d ready · batch %s\n", m.batch.ReadyCount(), m.batch.ID)
		lines := []string{}
		for _, p := range m.batch.Previews() {
			state := "BLOCKED"
			if p.Ready {
				state = "READY"
			}
			lines = append(lines, fmt.Sprintf("[%s] %s · %s", state, p.Action, triage.SafeText(p.Path)))
			for _, e := range p.Effects {
				lines = append(lines, "  → "+triage.SafeText(e))
			}
			for _, r := range p.Reasons {
				lines = append(lines, "  ! "+triage.SafeText(r))
			}
			for _, d := range p.Discard {
				lines = append(lines, fmt.Sprintf("  discard ignored: %s (%d bytes)", triage.SafeText(d.Path), d.Bytes))
			}
		}
		start := min(m.scroll, max(0, len(lines)-1))
		end := min(len(lines), start+max(2, m.height-12))
		for _, line := range lines[start:end] {
			fmt.Fprintln(&b, fit(line, m.width-2))
		}
		if m.batch.Token != "" {
			fmt.Fprintf(&b, "\nType %s to approve the listed removals: %s\n", m.batch.Token, m.input.View())
		} else {
			fmt.Fprintln(&b, "\ny approve these exact plans · Esc return")
		}
		fmt.Fprintln(&b, muted.Render("PgUp/PgDn scroll · newly eligible actions require a new round"))
	} else {
		quadrants := [4]int{}
		for _, i := range m.report.Items {
			n := 0
			if !i.HasWork() {
				n += 2
			}
			if !i.Quick() {
				n++
			}
			quadrants[n]++
		}
		fmt.Fprintf(&b, "Work to save: %d batch / %d individual   Other: %d batch / %d individual\n", quadrants[0], quadrants[1], quadrants[2], quadrants[3])
		fmt.Fprintln(&b, muted.Render("Remote comparisons use cached refs. Fetch is an explicit separate round."))
		available := max(2, m.height-17)
		start := max(0, m.cursor-available+1)
		end := min(len(m.visible), start+available)
		for n := start; n < end; n++ {
			i := m.visible[n]
			mark := "[ ]"
			if m.selected[i.ID] {
				mark = "[x]"
			}
			name := i.Name
			if i.Branch != nil {
				name += " / " + strings.TrimPrefix(i.Branch.Ref, "refs/heads/")
			}
			codes := []string{}
			for _, f := range i.Findings {
				codes = append(codes, f.Code)
			}
			age := "unknown"
			if !i.LastActivity.IsZero() {
				age = i.LastActivity.Format("2006-01-02")
			}
			line := fmt.Sprintf("%s %-4s %-10s %-30s %s", mark, i.Kind, age, name, strings.Join(codes, ", "))
			line = fit(triage.SafeText(line), m.width-3)
			if n == m.cursor {
				fmt.Fprintln(&b, highlight.Render("› "+line))
			} else {
				fmt.Fprintln(&b, "  "+line)
			}
		}
		if i, ok := m.current(); ok {
			fmt.Fprintln(&b, "\n"+fit(triage.SafeText(i.Path), m.width-1))
			details := []string{}
			for _, f := range i.Findings {
				details = append(details, f.Detail)
			}
			fmt.Fprintln(&b, fit(triage.SafeText(strings.Join(details, " · ")), m.width-1))
			if len(i.Runtimes) > 0 {
				fmt.Fprintf(&b, "Runtime: %s / %s\n", i.Runtimes[0].Backend, triage.SafeText(i.Runtimes[0].Label))
			}
		}
		if m.inputMode != "" {
			prompt := "Filter: "
			if m.inputMode == "directories" {
				prompt = "Replace disposable directories (comma-separated; blank clears): "
			}
			fmt.Fprintln(&b, prompt+m.input.View())
		}
		fmt.Fprintln(&b, muted.Render("Tab views · g repo/Try · / filter · space select · a candidates · n clear · Enter preview"))
		fmt.Fprintln(&b, muted.Render("f fetch · p push · u fast-forward · w warm · c cold · t retire · x remove checkout"))
		fmt.Fprintln(&b, muted.Render("o individual flow · e shell · v runtime · L keep local · s snooze 7d · U clear intent"))
		fmt.Fprintln(&b, muted.Render("? details · b results · R disposable directories · d deferred · r refresh · q quit"))
	}
	if m.message != "" {
		fmt.Fprintln(&b, fit(triage.SafeText(m.message), m.width-1))
	}
	return b.String()
}
func lenSelected(s map[string]bool) int {
	n := 0
	for _, v := range s {
		if v {
			n++
		}
	}
	return n
}
func fit(s string, width int) string {
	if width < 1 {
		return ""
	}
	return runewidth.Truncate(s, width, "…")
}

func ledgerLines(l triage.Ledger) []string {
	lines := []string{"Batch results · " + l.BatchID, "Receipt: " + l.Path}
	for _, o := range l.Outcomes {
		lines = append(lines, fmt.Sprintf("[%s] %s · %s", o.Status, o.Action, o.Path))
		if o.Error != "" {
			lines = append(lines, "  "+o.Error)
		}
		for _, step := range o.Steps {
			lines = append(lines, fmt.Sprintf("  %s: %s %s", step.Status, step.Effect.Description, step.Failure))
		}
	}
	return lines
}
