package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) noteMode() bool {
	return m.mode == modeNoteBrowse || m.mode == modeNoteAdd ||
		m.mode == modeNoteSearch || m.mode == modeNoteConfirmDelete
}

func (m Model) renderNotes() string {
	var b strings.Builder
	title := m.noteTarget.Name()
	if title == "" {
		title = "repository"
	}
	fmt.Fprintf(&b, "%s\n\n", styleTitle.Render(fmt.Sprintf("dev  NOTES  %s  ·  %d notes", title, len(m.notes))))

	switch m.mode {
	case modeNoteAdd:
		b.WriteString("  " + styleTitle.Render("quick thought") + "\n\n")
		b.WriteString("  " + m.input.View() + "\n\n")
		if m.err != nil {
			b.WriteString("  " + styleErr.Render("✗ "+m.err.Error()) + "\n\n")
		}
		b.WriteString("  " + styleHelp.Render("enter save · esc cancel"))
		return b.String()

	case modeNoteSearch:
		b.WriteString("  " + styleTitle.Render("search") + "\n\n")
		b.WriteString("  " + m.input.View() + "\n\n")
		b.WriteString("  " + styleHelp.Render("enter search · esc clear and return"))
		return b.String()

	case modeNoteConfirmDelete:
		if n, ok := m.currentNote(); ok {
			b.WriteString("  " + styleErr.Render("Delete note "+n.ID[:8]+"?") + "\n")
			b.WriteString("  " + n.Preview(maxInt(20, m.width-6)) + "\n\n")
		}
		b.WriteString("  " + styleHelp.Render("y delete · n / esc cancel"))
		return b.String()
	}

	if m.noteQuery != "" {
		b.WriteString("  " + styleDim.Render("search: "+m.noteQuery) + "\n\n")
	}
	if m.noteLoading {
		b.WriteString("  " + styleDim.Render("Loading notes…") + "\n")
		return b.String()
	}
	if m.err != nil {
		b.WriteString("  " + styleErr.Render("✗ "+m.err.Error()) + "\n")
	}
	if len(m.notes) == 0 {
		b.WriteString("  " + styleDim.Render("No notes. Press a or n to add one.") + "\n\n")
		b.WriteString("  " + styleHelp.Render("a/n add · / search · N/esc back"))
		return b.String()
	}

	idW, whenW, tagsW := 8, 6, clamp(m.width/5, 10, 24)
	thoughtW := m.width - idW - whenW - tagsW - 12
	if thoughtW < 20 {
		thoughtW = 20
	}
	fmt.Fprintf(&b, "  %s  %s  %s  %s\n",
		styleHeader.Render(fitCell("ID", idW)),
		styleHeader.Render(fitCell("WHEN", whenW)),
		styleHeader.Render(fitCell("TAGS", tagsW)),
		styleHeader.Render(fitCell("THOUGHT", thoughtW)))

	maxRows := m.height - 11
	if m.noteExpanded {
		maxRows -= 6
	}
	if maxRows < 3 {
		maxRows = 3
	}
	from, to := noteWindow(len(m.notes), m.noteCursor, maxRows)
	for i := from; i < to; i++ {
		n := m.notes[i]
		tags := strings.Join(n.Tags, ",")
		if tags == "" {
			tags = "—"
		}
		line := strings.Join([]string{
			fitCell(n.ID[:8], idW), fitCell(noteAge(n.Created), whenW),
			fitCell(tags, tagsW), fitCell(n.Preview(thoughtW), thoughtW),
		}, "  ")
		if i == m.noteCursor {
			b.WriteString(styleSelected.Render("▸ ") + styleSelected.Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	if len(m.notes) > maxRows {
		fmt.Fprintf(&b, "  %s\n", styleDim.Render(fmt.Sprintf("… %d–%d of %d", from+1, to, len(m.notes))))
	}
	if m.noteExpanded {
		if n, ok := m.currentNote(); ok {
			body := lipgloss.NewStyle().Width(maxInt(20, m.width-4)).Render(strings.TrimSpace(n.Body))
			body = limitLines(body, maxInt(3, m.height/3))
			b.WriteString("\n  " + styleDim.Render(n.ID+" · "+n.Updated.Format(time.RFC3339)) + "\n")
			b.WriteString("  " + strings.ReplaceAll(body, "\n", "\n  ") + "\n")
		}
	}
	b.WriteString("\n  " + styleHelp.Render("j/k move · / search · enter expand · a add · e edit · d delete · N/esc back"))
	return b.String()
}

func noteWindow(total, cursor, height int) (from, to int) {
	if total <= height {
		return 0, total
	}
	from = cursor - height/2
	if from < 0 {
		from = 0
	}
	if from+height > total {
		from = total - height
	}
	return from, from + height
}

func noteAge(created time.Time) string {
	d := time.Since(created)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	}
}

func limitLines(text string, limit int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= limit {
		return text
	}
	return strings.Join(lines[:limit], "\n") + "\n…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
