package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder

	b.WriteString(styleTitle.Render("dev") + "  " + styleDim.Render(m.Summary()) + "\n\n")

	rows := m.visible()
	if len(rows) == 0 {
		b.WriteString(styleDim.Render("  Nothing to show. Press 0 to clear the filter, or a to include done tasks.\n"))
	} else {
		b.WriteString(m.renderTable(rows))
	}

	b.WriteString("\n")
	b.WriteString(m.renderDetail())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

// columns are sized against the terminal width so the table stays readable in
// a narrow pane without wrapping into nonsense.
func (m Model) renderTable(rows []inventory.Row) string {
	nameW, branchW, nextW := m.columnWidths()

	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-*s  %-6s  %-*s  %-8s  %-5s  %s",
		nameW, "TASK", "STATE", branchW, "BRANCH", "GIT", "AGE", "NEXT")) + "\n")

	for i, r := range rows {
		marker := "  "
		if i == m.cursor {
			marker = styleSelected.Render("▸ ")
		}
		line := fmt.Sprintf("%-*s  %-6s  %-*s  %-8s  %-5s  %s",
			nameW, pad(r.Task.Title(), nameW),
			r.Task.State.Label(),
			branchW, pad(r.Task.Branch, branchW),
			pad(gitColumn(r), 8),
			pad(shortAge(r), 5),
			pad(nextColumn(r), nextW),
		)
		if i == m.cursor {
			line = styleSelected.Render(line)
		} else {
			line = colourState(r.Task.State, line)
		}
		b.WriteString(marker + line + "\n")
	}
	return b.String()
}

func (m Model) columnWidths() (name, branch, next int) {
	// Fixed columns: marker, state, git, age and the separators between them.
	const fixed = 2 + 6 + 8 + 5 + 10
	avail := m.width - fixed
	if avail < 40 {
		avail = 40
	}
	name = clamp(avail*30/100, 12, 30)
	branch = clamp(avail*30/100, 12, 30)
	next = avail - name - branch
	if next < 10 {
		next = 10
	}
	return name, branch, next
}

// renderDetail shows everything about the selected task that does not fit in
// the table — the point of a dashboard over a listing.
func (m Model) renderDetail() string {
	switch m.mode {
	case modeEditNext:
		return "  " + styleTitle.Render("next action: ") + m.input.View() +
			"\n  " + styleHelp.Render("enter to save · esc to cancel")
	case modeConfirmPark:
		row, _ := m.current()
		return "  " + styleTitle.Render("park "+row.Task.Title()) +
			"\n  next: " + m.input.View() +
			"\n  " + styleHelp.Render("enter to park (session closes, worktree stays) · esc to cancel")
	}

	row, ok := m.current()
	if !ok {
		return ""
	}
	t := row.Task
	var lines []string
	lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("repo"), t.Repo))
	lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(row.Checkout)))
	if t.Owner != "" {
		lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("owner"), t.Owner))
	}
	if row.Session != nil {
		s := row.Session.Handle
		if row.Session.AgentStatus != "" {
			s += " · " + row.Session.AgentStatus
		}
		lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(s)))
	}
	if drift := row.StateDrift(); drift != "" {
		lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("drift"), styleDrift.Render(drift)))
	}
	if !row.CheckoutExists {
		lines = append(lines, "  "+styleDim.Render("note ")+
			styleDrift.Render("no checkout on disk — enter rebuilds it from the branch"))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) renderFooter() string {
	var status string
	switch {
	case m.err != nil:
		status = styleErr.Render("✗ " + m.err.Error())
	case m.status != "":
		status = styleOK.Render("✓ " + m.status)
	}

	filter := "all"
	if len(m.filter) == 1 {
		filter = string(m.filter[0])
	} else if m.showDone {
		filter = "all incl. done"
	}

	help := styleHelp.Render(
		"↑↓ move · enter open · p park · n next · r refresh · 1/2/3 hot/warm/cold · 0 clear · a done · q quit")

	var b strings.Builder
	if status != "" {
		b.WriteString("  " + status + "\n")
	}
	b.WriteString("  " + styleDim.Render("filter: "+filter) + "\n")
	b.WriteString("  " + help)
	return b.String()
}

func gitColumn(r inventory.Row) string {
	switch {
	case r.StatusErr != nil:
		return "?"
	case !r.CheckoutExists:
		return "no dir"
	}
	return r.Status.Summary()
}

func nextColumn(r inventory.Row) string {
	if r.Task.Next == "" {
		return styleDim.Render("—")
	}
	return r.Task.Next
}

func shortAge(r inventory.Row) string {
	d := r.Age()
	switch {
	case d <= 0:
		return "—"
	case d.Hours() < 24:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d.Hours() < 24*14:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d.Hours() < 24*60:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

func colourState(s task.State, line string) string {
	switch s {
	case task.Hot:
		return styleHot.Render(line)
	case task.Warm:
		return styleWarm.Render(line)
	case task.Cold:
		return styleCold.Render(line)
	case task.Done:
		return styleDone.Render(line)
	}
	return line
}

// pad truncates to n display columns, measuring the way lipgloss renders so
// CJK task names do not shift the table.
func pad(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > n-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
