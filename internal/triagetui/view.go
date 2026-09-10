package triagetui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

var (
	title     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	muted     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	highlight = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229"))
	danger    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warning   = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	success   = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
)

type hit struct {
	kind              string
	x, y, w, h, index int
}
type frame struct {
	text string
	hits []hit
	fits bool
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}
func padded(s string, w int) string {
	s = fit(s, w)
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}
func (m Model) glyph(s string) string {
	if !m.ascii {
		return s
	}
	r := strings.NewReplacer("→", ">", "↑", "^", "↓", "v", "↕", "sync", "●", "*", "✓", "ok", "×", "x", "▸", ">", "▾", "v", "—", "-", "…", "...", "·", "|")
	return r.Replace(s)
}
func (m Model) View() string { return m.render().text }
func (m Model) render() frame {
	f := frame{}
	w, h := max(20, m.width-2), max(10, m.height)
	lines := []string{}
	add := func(s string) { lines = append(lines, fit(m.glyph(s), w)) }
	add(title.Render("dev triage  /  Organize local work"))
	add(muted.Render("Scope: " + triage.SafeText(m.scope)))
	stage := 0
	if m.chooser {
		stage = 1
	}
	if m.batch != nil || m.stop != nil {
		stage = 2
	}
	if m.results {
		stage = 3
	}
	stages := []string{"1 Select items", "2 Choose action", "3 Review / apply", "4 Results"}
	if w < 90 {
		stages = []string{"1 Select", "2 Action", "3 Preview", "4 Results"}
	}
	for n := range stages {
		if n == stage {
			stages[n] = title.Render(stages[n])
		} else {
			stages[n] = muted.Render(stages[n])
		}
	}
	add(strings.Join(stages, "  →  "))
	status := m.message
	if m.busy != "" {
		status = m.busy
	}
	add(warning.Render(triage.SafeText(status)))
	available := max(1, h-9)
	switch {
	case m.overlay != nil:
		add(title.Render("Details"))
		wrapped := []string{}
		for _, line := range m.overlay {
			wrapped = append(wrapped, strings.Split(ansi.Hardwrap(triage.SafeText(line), w, true), "\n")...)
		}
		start := min(m.scroll, max(0, len(wrapped)-1))
		for _, line := range wrapped[start:min(len(wrapped), start+available)] {
			add(triage.SafeText(line))
		}
		add(muted.Render("↑/↓ / wheel scroll · Enter / Esc return"))
	case m.batch != nil:
		previews := m.batch.Previews()
		add(highlight.Render(fmt.Sprintf("%s  ·  %d ready / %d reviewed", actionLabel(m.action), m.batch.ReadyCount(), len(previews))))
		body := []string{}
		for _, p := range previews {
			label := danger.Render("! BLOCKED")
			if p.Ready {
				label = success.Render("✓ READY")
			}
			body = append(body, label+"  "+triage.SafeText(p.Path))
			for _, e := range p.Effects {
				body = append(body, "  → "+triage.SafeText(e))
			}
			for _, r := range p.Reasons {
				body = append(body, warning.Render("  ! "+triage.SafeText(r)))
			}
			for _, d := range p.Discard {
				body = append(body, warning.Render(fmt.Sprintf("  Discard ignored: %s (%d bytes)", d.Path, d.Bytes)))
			}
		}
		start := min(m.scroll, max(0, len(body)-1))
		for _, line := range body[start:min(len(body), start+available-1)] {
			add(line)
		}
		if m.batch.Token != "" {
			add("Type " + highlight.Render(m.batch.Token) + "  " + m.input.View())
			add(muted.Render("Enter confirms exact token · Esc back · PgUp/PgDn scroll"))
		} else {
			y := len(lines)
			add(success.Render("[ Apply reviewed targets ]") + "  [ Back ]")
			f.hits = append(f.hits, hit{"apply", 0, y, 27, 1, 0}, hit{"back", 29, y, 8, 1, 0})
			add(muted.Render("y apply · Tab / Enter button · Esc back · PgUp/PgDn scroll"))
		}
	case m.results && m.lastLedger != nil:
		summary, severity := triage.SummarizeLedger(*m.lastLedger)
		style := success
		if severity == "error" {
			style = danger
		} else if severity == "warning" {
			style = warning
		}
		add(style.Render(summary))
		add(muted.Render("   RESULT       ITEM / ACTION                       REASON"))
		outcomes := m.lastLedger.Outcomes
		start := max(0, m.resultCursor-available+1)
		for n := start; n < min(len(outcomes), start+available); n++ {
			o := outcomes[n]
			mark := "  "
			if n == m.resultCursor {
				mark = "▸ "
			}
			state := success.Render("✓ done")
			if o.Status != "completed" {
				state = warning.Render("! " + o.Status)
				if o.Status == "failed" || o.Status == "stale" {
					state = danger.Render("× " + o.Status)
				}
			}
			name := filepath.Base(o.Path) + " / " + actionLabel(o.Action)
			reason := o.Error
			if reason == "" {
				reason = "Completed"
			}
			y := len(lines)
			add(mark + padded(state, 13) + " " + padded(triage.SafeText(name), max(22, w/3)) + " " + triage.SafeText(reason))
			f.hits = append(f.hits, hit{"result", 0, y, w, 1, n})
		}
		y := len(lines)
		add("[ Details ]  [ Back to work ]  [ Open shell ]")
		f.hits = append(f.hits, hit{"result-details", 0, y, 11, 1, 0}, hit{"back", 13, y, 16, 1, 0}, hit{"shell", 31, y, 14, 1, 0})
		add(muted.Render("Enter details · Esc work list · Ctrl+O new action · r recheck · q return"))
	case m.chooser:
		selected, _ := m.selectionCounts()
		scope := "current item"
		if selected > 0 {
			scope = fmt.Sprintf("%d selected targets", selected)
		}
		add(title.Render("Choose action for " + scope))
		choices := m.choices()
		start := max(0, m.choiceCursor-available+1)
		if len(choices) == 0 {
			add("No batch action applies here.")
			add("Return to inspect details, open a shell (e), or use the individual flow (o).")
		}
		for n := start; n < min(len(choices), start+available-1); n++ {
			c := choices[n]
			mark := "  "
			if n == m.choiceCursor {
				mark = "▸ "
			}
			y := len(lines)
			label := fmt.Sprintf("%s%-38s %d candidates · %d blocked", mark, actionLabel(c.name), c.candidates, c.blocked)
			if n == m.choiceCursor {
				label = highlight.Render(label)
			}
			add(label)
			f.hits = append(f.hits, hit{"choice", 0, y, w, 1, n})
		}
		if len(choices) > 0 {
			add(muted.Render(actionHint(choices[m.choiceCursor].name)))
		}
		add(muted.Render("Enter / click previews exact targets · Esc back · q return"))
	default:
		count, hidden := m.selectionCounts()
		add(fmt.Sprintf("%s · %d targets selected (%d hidden) · %d repos / Tries", m.kind, count, hidden, m.groupCount()))
		wide := w >= 110
		leftW := w
		if wide {
			leftW = w * 3 / 5
		}
		add(muted.Render(padded("    ITEM", max(20, leftW/2)) + "  WORK TO REVIEW"))
		start := max(0, m.cursor-available+1)
		end := min(len(m.rows), start+available)
		right := []string{}
		if wide {
			right = details(m.currentMembers())
			if len(right) > available {
				right = append(right[:available-1], "Enter for all details")
			}
		}
		for n := start; n < end; n++ {
			row := m.rows[n]
			mark := "  "
			if n == m.cursor {
				mark = "▸ "
			}
			arrow := "▸"
			if m.expanded[row.key] {
				arrow = "▾"
			}
			if row.child {
				arrow = " "
			}
			name := rowName(row)
			if row.child {
				name = "  " + name
			}
			prefix := mark + m.selectionState(row.members) + " " + arrow + " "
			line := prefix + padded(triage.SafeText(name), max(12, leftW/2-8)) + " "
			badges := []string{}
			for _, fact := range facts(row.members) {
				style := muted
				if strings.HasPrefix(fact, "!") || strings.Contains(fact, "sync") {
					style = warning
				}
				if strings.HasPrefix(fact, "?") {
					style = warning
				}
				if strings.HasPrefix(fact, "●") {
					style = title
				}
				badges = append(badges, style.Render(fact))
			}
			line += strings.Join(badges, "  ")
			if n == m.cursor {
				line = lipgloss.NewStyle().Bold(true).Render(line)
			}
			y := len(lines)
			if wide {
				detail := ""
				if n-start < len(right) {
					detail = triage.SafeText(right[n-start])
				}
				line = padded(line, leftW) + " │ " + muted.Render(detail)
			}
			add(line)
			f.hits = append(f.hits, hit{"row", 0, y, leftW, 1, n}, hit{"check", 2, y, 3, 1, n}, hit{"expand", 6, y, 1, 1, n})
		}
		if len(m.rows) == 0 {
			add("No items match this scope. Use / to change the filter or r to refresh.")
		}
		if m.inputMode != "" {
			add(m.inputMode + ": " + m.input.View())
		} else {
			y := len(lines)
			button := "[ Choose action… ]"
			if m.buttonFocus {
				button = highlight.Render(button)
			}
			add(button + "  [ Clear selection ]")
			f.hits = append(f.hits, hit{"actions", 0, y, 18, 1, 0}, hit{"clear", 20, y, 19, 1, 0})
		}
		add(muted.Render("Select  Space / click · Ctrl+A all / none · ←/→ expand · Enter details"))
		add(muted.Render("Actions  Ctrl+O · / filter · ? help · r refresh · q return"))
	}
	f.fits = len(lines) <= m.height && m.height > 0
	if len(lines) > h {
		lines = lines[:h]
	}
	f.text = strings.Join(lines, "\n")
	return f
}
func (m Model) groupCount() int {
	n := 0
	for _, r := range m.rows {
		if !r.child {
			n++
		}
	}
	return n
}
func (m Model) updateMouse(e tea.MouseEvent) (tea.Model, tea.Cmd) {
	if (m.busy != "" && !m.results) || e.Alt || e.Ctrl || e.Shift || !m.render().fits {
		return m, nil
	}
	if e.Button == tea.MouseButtonWheelUp || e.Button == tea.MouseButtonWheelDown {
		delta := 3
		if e.Button == tea.MouseButtonWheelUp {
			delta = -3
		}
		switch {
		case m.overlay != nil || m.batch != nil:
			m.scroll = max(0, m.scroll+delta)
		case m.results:
			m.resultCursor = max(0, min(len(m.lastLedger.Outcomes)-1, m.resultCursor+delta))
		case m.chooser:
			m.choiceCursor = max(0, min(len(m.choices())-1, m.choiceCursor+delta))
		default:
			m.cursor = max(0, min(len(m.rows)-1, m.cursor+delta))
		}
		return m, nil
	}
	if e.Action != tea.MouseActionPress || (e.Button != tea.MouseButtonLeft && e.Button != tea.MouseButtonRight) {
		return m, nil
	}
	hits := m.render().hits
	for n := len(hits) - 1; n >= 0; n-- {
		p := hits[n]
		if e.X < p.x || e.X >= p.x+p.w || e.Y < p.y || e.Y >= p.y+p.h {
			continue
		}
		if e.Button == tea.MouseButtonRight && p.kind != "row" && p.kind != "check" && p.kind != "expand" {
			return m, nil
		}
		switch p.kind {
		case "row", "check", "expand":
			m.cursor = p.index
			if e.Button == tea.MouseButtonRight {
				return m.openChooser()
			}
			if p.kind == "check" {
				m.toggleRow(p.index)
			}
			if p.kind == "expand" {
				m.expandCurrent(!m.expanded[m.rows[p.index].key])
			}
		case "actions":
			return m.openChooser()
		case "clear":
			m.selected = map[string]bool{}
		case "choice":
			m.choiceCursor = p.index
			return m.prepareAction(m.choices()[p.index].name)
		case "apply":
			return m.apply()
		case "back":
			m.batch = nil
			m.results = false
			m.inputMode = ""
			m.scroll = 0
		case "result":
			m.resultCursor = p.index
		case "result-details":
			m.showResultDetails()
		case "shell":
			if m.busy != "" {
				return m, nil
			}
			if i, ok := m.resultItem(); ok {
				return m.openItem(i, "shell")
			}
		}
		return m, nil
	}
	return m, nil
}
