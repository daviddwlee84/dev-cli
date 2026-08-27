package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// chrome is the number of lines the header, detail pane and footer take, so
// the list knows how much room it has left.
const chrome = 12

func (m Model) listHeight() int {
	h := m.height - chrome
	if h < 3 {
		return 3
	}
	return h
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	if m.view == ViewRepos {
		b.WriteString(m.renderRepos())
	} else {
		b.WriteString(m.renderTasks())
	}

	b.WriteString("\n")
	b.WriteString(m.renderDetail())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

// renderHeader shows the tab strip, so which list is showing — and that there
// is another one — is never in doubt.
func (m Model) renderHeader() string {
	var tabs []string
	for _, v := range Views {
		label := " " + strings.ToUpper(v.String()) + " "
		if v == m.view {
			tabs = append(tabs, styleSelected.Render(label))
		} else {
			tabs = append(tabs, styleDim.Render(label))
		}
	}
	line := styleTitle.Render("dev") + "  " + strings.Join(tabs, styleDim.Render("│")) +
		"   " + styleDim.Render(m.Summary())
	if m.filter != "" {
		line += "   " + styleWarm.Render("/"+m.filter)
	}
	return line + "\n"
}

// window returns the slice of a list visible around the cursor, and the offset
// it starts at, so a long inventory scrolls instead of overflowing.
func (m Model) window(n int) (from, to int) {
	h := m.listHeight()
	if n <= h {
		return 0, n
	}
	from = m.at() - h/2
	if from < 0 {
		from = 0
	}
	if from+h > n {
		from = n - h
	}
	return from, from + h
}

func (m Model) renderTasks() string {
	rows := m.visibleTasks()
	if len(rows) == 0 {
		return m.emptyTasks()
	}
	nameW, branchW, nextW := m.columnWidths()

	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-*s  %-6s  %-*s  %-8s  %-5s  %s",
		nameW, "TASK", "STATE", branchW, "BRANCH", "GIT", "AGE", "NEXT")) + "\n")

	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		r := rows[i]
		line := fmt.Sprintf("%-*s  %-6s  %-*s  %-8s  %-5s  %s",
			nameW, pad(r.Task.Title(), nameW),
			r.Task.State.Label(),
			branchW, pad(r.Task.Branch, branchW),
			pad(gitColumn(r), 8),
			pad(shortAge(r), 5),
			pad(nextColumn(r), nextW),
		)
		b.WriteString(m.renderLine(i, line, colourState(r.Task.State, line)))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func (m Model) renderRepos() string {
	repos := m.visibleRepos()
	if len(repos) == 0 {
		return "  " + styleDim.Render("No repositories found. Check paths.scan_roots in your config,"+
			" or run `dev config init`.\n")
	}
	nameW := clamp(m.width*30/100, 16, 34)
	branchW := clamp(m.width*22/100, 12, 28)

	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-*s  %-*s  %-8s  %-4s  %s",
		nameW, "REPO", branchW, "BRANCH", "GIT", "WT", "TASKS")) + "\n")

	from, to := m.window(len(repos))
	for i := from; i < to; i++ {
		r := repos[i]
		wt := "—"
		if r.Worktrees > 0 {
			wt = fmt.Sprintf("%d", r.Worktrees)
		}
		tasks := r.StateSummary()
		if tasks == "" {
			tasks = styleDim.Render("—")
		}
		if r.Live {
			tasks = styleLive.Render("●") + " " + tasks
		}
		line := fmt.Sprintf("%-*s  %-*s  %-8s  %-4s  %s",
			nameW, pad(r.Repo.Display(), nameW),
			branchW, pad(r.Status.Branch, branchW),
			pad(r.Status.Summary(), 8),
			pad(wt, 4),
			tasks,
		)
		plain := line
		if r.Status.Dirty() {
			plain = styleDirty.Render(line)
		} else if len(r.Tasks) == 0 {
			plain = styleClean.Render(line)
		}
		b.WriteString(m.renderLine(i, line, plain))
	}
	b.WriteString(m.scrollNote(len(repos), from, to))
	return b.String()
}

// renderLine applies the cursor marker and styling to one row.
func (m Model) renderLine(i int, plain, styled string) string {
	if i == m.at() {
		return styleSelected.Render("▸ ") + styleSelected.Render(plain) + "\n"
	}
	return "  " + styled + "\n"
}

func (m Model) scrollNote(total, from, to int) string {
	if total <= to-from {
		return ""
	}
	return "  " + styleDim.Render(fmt.Sprintf("… %d–%d of %d", from+1, to, total)) + "\n"
}

func (m Model) emptyTasks() string {
	if len(m.rows) == 0 {
		return "  " + styleDim.Render("No tasks recorded yet.\n") +
			"  " + styleDim.Render("Press tab for the repository list, then s to start work on one.\n") +
			"  " + styleDim.Render("Or run `dev adopt` to import worktrees and branches already in flight.\n")
	}
	return "  " + styleDim.Render("Nothing matches. Press 0 to clear the filter, or a to include done tasks.\n")
}

func (m Model) columnWidths() (name, branch, next int) {
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

// renderDetail shows what does not fit in the table — the reason to have a
// dashboard rather than a listing.
func (m Model) renderDetail() string {
	switch m.mode {
	case modeFilter:
		return "  " + styleTitle.Render("filter ") + m.input.View() +
			"\n  " + styleHelp.Render("enter to keep it · esc to clear")
	case modeEditNext:
		return "  " + styleTitle.Render("next action ") + m.input.View() +
			"\n  " + styleHelp.Render("enter to save · esc to cancel")
	case modeConfirmPark:
		row, _ := m.currentTask()
		return "  " + styleTitle.Render("park "+row.Task.Title()) +
			"\n  next: " + m.input.View() +
			"\n  " + styleHelp.Render("enter to park (session closes, worktree stays) · esc to cancel")
	case modeStartTask:
		r, _ := m.currentRepo()
		return "  " + styleTitle.Render("start work in "+r.Repo.Name) +
			"\n  name: " + m.input.View() +
			"\n  " + styleHelp.Render("enter to create the branch, worktree and session · esc to cancel")
	}

	if r, ok := m.currentRepo(); ok {
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(r.Repo.Path)),
		}
		if r.Repo.Category != "" {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("group"), r.Repo.Category))
		}
		if len(r.Tasks) > 0 {
			var names []string
			for _, t := range r.Tasks {
				names = append(names, t.State.Icon()+" "+t.Title())
			}
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("tasks"), strings.Join(names, "  ")))
		} else {
			lines = append(lines, "  "+styleDim.Render("tasks")+" "+
				styleDim.Render("none — press s to start one here"))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	row, ok := m.currentTask()
	if !ok {
		return ""
	}
	t := row.Task
	lines := []string{
		fmt.Sprintf("  %s  %s", styleDim.Render("repo"), t.Repo),
		fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(row.Checkout)),
	}
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

	var bindings []string
	if m.view == ViewRepos {
		bindings = append(bindings, "enter open", "s start task")
	} else {
		bindings = append(bindings, "enter open", "p park", "c next")
	}
	bindings = append(bindings, "tab view", "/ filter")
	for _, t := range m.Tools() {
		bindings = append(bindings, t.Key+" "+t.Name)
	}
	bindings = append(bindings, "1/2/3 state", "0 clear", "r refresh", "q quit")

	var b strings.Builder
	if status != "" {
		b.WriteString("  " + status + "\n")
	}
	b.WriteString("  " + styleHelp.Render(wrapBindings(bindings, m.width-4)))
	return b.String()
}

// wrapBindings lays the key hints out over as many lines as the width needs,
// rather than truncating them — a hint you cannot read is not a hint.
func wrapBindings(items []string, width int) string {
	if width < 20 {
		width = 20
	}
	var lines []string
	cur := ""
	for _, it := range items {
		candidate := it
		if cur != "" {
			candidate = cur + " · " + it
		}
		if lipgloss.Width(candidate) > width && cur != "" {
			lines = append(lines, cur)
			cur = it
			continue
		}
		cur = candidate
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n  ")
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
// CJK repo and task names do not shift the table.
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
