package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// listHeight budgets rows from what the current detail and footer actually
// render. REPOS has more detail and more key hints than TASKS; the old fixed
// chrome allowance let the complete view exceed terminal height, so Bubble
// Tea scrolled the first lines and the top tab bar appeared to disappear.
func (m Model) listHeight() int {
	// Header + its blank line, table header, blank before detail, blank before
	// footer. Detail and footer can each wrap/change by view and terminal width.
	fixed := 2 + 1 + 1 + lineCount(m.renderDetail()) + 1 + lineCount(m.renderFooter())
	h := m.height - fixed
	if h < 3 {
		return 3
	}
	// A long list adds the "… x–y of n" scroll note.
	if m.count() > h && h > 3 {
		h--
	}
	return h
}

func lineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.mode == modeStats {
		return m.renderStats()
	}
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	switch m.view {
	case ViewRepos:
		b.WriteString(m.renderRepos())
	case ViewRemote:
		b.WriteString(m.renderRemotes())
	default:
		b.WriteString(m.renderTasks())
	}

	b.WriteString("\n")
	b.WriteString(m.renderDetail())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m Model) renderStats() string {
	var b strings.Builder
	title := "repo activity"
	if m.stats != nil && m.stats.Repo != "" {
		title = m.stats.Repo + " activity"
	}
	b.WriteString(styleTitle.Render("dev  HEATMAP  "+title) + "\n\n")
	if m.err != nil {
		b.WriteString("  " + styleErr.Render("✗ "+m.err.Error()) + "\n")
	} else if m.stats == nil {
		b.WriteString("  " + styleDim.Render("Loading activity…") + "\n")
	} else if m.stats.Seconds == 0 {
		b.WriteString("  " + styleDim.Render("No activity recorded for this repository.\n"))
		b.WriteString("  " + styleDim.Render("Press b to backfill only this repo from Git history.\n"))
	} else {
		b.WriteString(m.stats.Heatmap)
		fmt.Fprintf(&b, "\n  %s   %d active days   %s → %s\n",
			styleOK.Render(humanSeconds(m.stats.Seconds)), m.stats.ActiveDays,
			m.stats.Since.Format("2006-01-02"), m.stats.Until.Format("2006-01-02"))
	}
	b.WriteString("\n  " + styleHelp.Render("b backfill this repo · r reread stats · H / esc back · q quit"))
	return b.String()
}

func humanSeconds(seconds int) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
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
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-*s  %-6s  %-*s  %-16s  %-5s  %s",
		nameW, "TASK", "STATE", branchW, "BRANCH", "GIT", "AGE", "NEXT")) + "\n")

	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		r := rows[i]
		line := fmt.Sprintf("%-*s  %-6s  %-*s  %-16s  %-5s  %s",
			nameW, pad(r.Task.Title(), nameW),
			r.Task.State.Label(),
			branchW, pad(r.Task.Branch, branchW),
			pad(gitColumn(r), 16),
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
		if m.loadingLocal {
			return "  " + styleDim.Render("Loading local repositories…") + "\n"
		}
		return "  " + styleDim.Render("No repositories found. Check paths.scan_roots in your config,"+
			" or run `dev config init`.\n")
	}
	columns := m.repoColumns()

	var b strings.Builder
	var headers []string
	for _, c := range columns {
		headers = append(headers, fitCell(c.header, c.width))
	}
	b.WriteString(styleHeader.Render("  "+strings.Join(headers, "  ")) + "\n")

	from, to := m.window(len(repos))
	for i := from; i < to; i++ {
		r := repos[i]
		cells := make([]string, 0, len(columns))
		for _, c := range columns {
			cells = append(cells, fitCell(repoColumnValue(r, c.name), c.width))
		}
		line := strings.Join(cells, "  ")
		styled := line
		if r.Status.Dirty() {
			styled = styleDirty.Render(line)
		} else if len(r.Tasks) == 0 && !r.Live {
			styled = styleClean.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(repos), from, to))
	return b.String()
}

type repoColumnSpec struct {
	name, header string
	width, min   int
}

func (m Model) repoColumns() []repoColumnSpec {
	names := m.actions.RepoColumns
	if len(names) == 0 {
		names = []string{"repo", "branch", "git", "live", "latest", "worktrees", "tasks"}
	}
	defaults := map[string]repoColumnSpec{
		"repo":      {"repo", "REPO", 28, 12},
		"branch":    {"branch", "BRANCH", 22, 10},
		"git":       {"git", "GIT", 16, 8},
		"live":      {"live", "LIVE", 15, 7},
		"latest":    {"latest", "LATEST", 8, 6},
		"worktrees": {"worktrees", "WT", 3, 2},
		"tasks":     {"tasks", "TASKS", 18, 8},
		"category":  {"category", "CATEGORY", 14, 8},
		"path":      {"path", "PATH", 30, 12},
	}
	columns := make([]repoColumnSpec, 0, len(names))
	for _, name := range names {
		if c, ok := defaults[name]; ok {
			columns = append(columns, c)
		}
	}
	// Shrink flexible columns to fit. Configuration controls what exists and in
	// what order; width adapts to the current pane.
	total := 2 + 2*(len(columns)-1)
	for _, c := range columns {
		total += c.width
	}
	for _, preferred := range []string{"path", "tasks", "repo", "branch", "live", "git"} {
		for i := range columns {
			if columns[i].name != preferred {
				continue
			}
			// Exhaust the least-important flexible column before touching
			// the next. Round-robin shrinking made LIVE unreadable while REPO
			// and TASKS still had plenty of space.
			for total > m.width && columns[i].width > columns[i].min {
				columns[i].width--
				total--
			}
		}
		if total <= m.width {
			break
		}
	}
	return columns
}

func repoColumnValue(r RepoRow, name string) string {
	switch name {
	case "repo":
		return r.Repo.Display()
	case "branch":
		if r.Repo.Bare {
			return "(bare)"
		}
		return r.Status.Branch
	case "git":
		if r.Repo.Bare {
			return "—"
		}
		return r.Status.Summary()
	case "live":
		if !r.Live {
			return "—"
		}
		if r.RuntimeStatus != "" {
			return r.Runtime + ":" + r.RuntimeStatus
		}
		return r.Runtime
	case "latest":
		return latestAge(r.LastActivity)
	case "worktrees":
		if r.Worktrees > 0 {
			return fmt.Sprintf("%d", r.Worktrees)
		}
		return "—"
	case "tasks":
		if tasks := r.StateSummary(); tasks != "" {
			return tasks
		}
		return "—"
	case "category":
		if r.Repo.Category != "" {
			return r.Repo.Category
		}
		return "—"
	case "path":
		return contract(r.Repo.Path)
	}
	return ""
}

func latestAge(at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	d := time.Since(at)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

func fitCell(s string, width int) string {
	s = pad(s, width)
	if n := lipgloss.Width(s); n < width {
		s += strings.Repeat(" ", width-n)
	}
	return s
}

func (m Model) renderRemotes() string {
	if m.remotesLoading {
		return "  " + styleDim.Render("Loading repositories from gh and glab…") + "\n"
	}
	rows := m.visibleRemotes()
	if len(rows) == 0 {
		if !m.remotesLoaded {
			return "  " + styleDim.Render("Remote repositories load when this view is opened.") + "\n"
		}
		if m.filter != "" {
			return "  " + styleDim.Render("No remote repository matches /"+m.filter) + "\n"
		}
		return "  " + styleDim.Render("No remote repositories returned. Check `gh auth status` and `glab auth status`.") + "\n"
	}
	nameW := clamp(m.width*30/100, 18, 38)
	descW := m.width - nameW - 38
	if descW < 12 {
		descW = 12
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-7s  %-*s  %-9s  %-12s  %-7s  %s",
		"FORGE", nameW, "REPOSITORY", "VIS", "UPDATED", "LOCAL", "DESCRIPTION")) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		r := rows[i]
		local := "—"
		if r.Cloned() {
			local = "yes"
		}
		vis := strings.ToLower(r.Repo.Visibility)
		if vis == "" {
			vis = "—"
		}
		line := fmt.Sprintf("%-7s  %-*s  %-9s  %-12s  %-7s  %s",
			r.Repo.Forge, nameW, pad(r.Repo.FullName, nameW),
			pad(vis, 9), pad(remoteAge(r), 12), pad(local, 7), pad(r.Repo.Description, descW))
		styled := line
		if r.Cloned() {
			styled = styleLive.Render(line)
		} else if r.Repo.Archived {
			styled = styleDim.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func remoteAge(r RemoteRow) string {
	if r.Repo.UpdatedAt.IsZero() {
		return "—"
	}
	d := time.Since(r.Repo.UpdatedAt)
	switch {
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
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
	if m.loadingLocal {
		return "  " + styleDim.Render("Loading tasks and local repositories…") + "\n"
	}
	if len(m.rows) == 0 {
		return "  " + styleDim.Render("No tasks recorded yet.\n") +
			"  " + styleDim.Render("Press tab for the repository list, then s to start work on one.\n") +
			"  " + styleDim.Render("Or run `dev adopt` to import worktrees and branches already in flight.\n")
	}
	return "  " + styleDim.Render("Nothing matches. Press 0 to clear the filter, or a to include done tasks.\n")
}

func (m Model) columnWidths() (name, branch, next int) {
	const fixed = 2 + 6 + 16 + 5 + 10
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
	case modeStartDirect:
		r, _ := m.currentRepo()
		return "  " + styleTitle.Render("track direct work in "+r.Repo.Name) +
			"\n  name: " + m.input.View() +
			"\n  " + styleHelp.Render("enter to track the current branch; no branch/worktree is created · esc to cancel")
	case modeConfirmClone:
		r, _ := m.currentRemote()
		return "  " + styleTitle.Render("clone "+r.Repo.FullName) +
			"\n  to: " + contract(filepath.Join("<project_root>", r.Repo.Name)) +
			"\n  " + styleHelp.Render("enter to clone into project_root · esc to cancel")
	}

	if r, ok := m.currentRemote(); ok {
		lines := []string{
			fmt.Sprintf("  %s %s", styleDim.Render("url  "), r.Repo.URL),
			fmt.Sprintf("  %s %s", styleDim.Render("branch"), r.Repo.DefaultBranch),
		}
		if r.Cloned() {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("local "), contract(r.LocalPath)))
		} else {
			lines = append(lines, "  "+styleDim.Render("local ")+" "+
				styleDim.Render("not cloned — press c"))
		}
		if r.Repo.Fork {
			lines = append(lines, "  "+styleDim.Render("kind  ")+" fork")
		}
		return strings.Join(lines, "\n") + "\n"
	}

	if r, ok := m.currentRepo(); ok {
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(r.Repo.Path)),
		}
		if r.Status.Dirty() {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("git  "), r.Status.Breakdown()))
			if types := r.Status.TypeBreakdown(); types != "" {
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("types"), types))
			}
		}
		if r.Repo.Category != "" {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("group"), r.Repo.Category))
		}
		if r.Live {
			live := r.Runtime + " " + r.RuntimeHandle
			if r.RuntimeStatus != "" {
				live += " · " + r.RuntimeStatus
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(live)))
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
		fmt.Sprintf("  %s  %s", styleDim.Render("mode"), t.EffectiveMode()),
	}
	if row.Status.Dirty() {
		lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("git  "), row.Status.Breakdown()))
		if types := row.Status.TypeBreakdown(); types != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("types"), types))
		}
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
	switch m.view {
	case ViewRepos:
		sortBy := m.actions.RepoSort
		if sortBy == "" {
			sortBy = "activity"
		}
		if m.actions.RepoReverse {
			sortBy += "↑"
		}
		bindings = append(bindings, "enter ad hoc", "s worktree task", "d direct task",
			"O sort:"+sortBy, "R reverse")
	case ViewRemote:
		if r, ok := m.currentRemote(); ok && r.Cloned() {
			bindings = append(bindings, "enter open local")
		} else {
			bindings = append(bindings, "c clone")
		}
	default:
		bindings = append(bindings, "enter open", "p park", "c next")
	}
	bindings = append(bindings, "tab view", "/ filter", "H stats", "e config")
	for _, t := range m.Tools() {
		bindings = append(bindings, t.Key+" "+t.Name)
	}
	bindings = append(bindings, "1/2/3 state", "0 clear", "r reload", "q quit")

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
