package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repocontext"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// listHeight budgets rows from what the current detail and footer actually
// render. REPOS has more detail and more key hints than TASKS; the old fixed
// chrome allowance let the complete view exceed terminal height, so Bubble
// Tea scrolled the first lines and the top tab bar appeared to disappear.
func (m Model) listHeight() int {
	// Header + its blank line, per-view list preamble, blank before detail,
	// blank before footer. Detail and footer can each wrap/change by view and
	// terminal width.
	fixed := 2 + m.listPreambleLines() + 1 + lineCount(m.renderDetail()) + 1 + lineCount(m.renderFooter())
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

func (m Model) listPreambleLines() int {
	lines := 1 // the table header, or the one-line empty/loading state
	switch m.view {
	case ViewRemote:
		if m.viewLoad(ViewRemote).loading && len(m.visibleRemotes()) > 0 {
			lines++
		}
	case ViewSkills:
		if m.viewLoad(ViewSkills).loading && len(m.visibleSkills()) > 0 {
			lines++
		}
	case ViewMCP:
		if m.viewLoad(ViewMCP).loading && len(m.visibleMCP()) > 0 {
			lines++
		}
	}
	return lines
}

func lineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// View implements tea.Model.
func (m Model) View() (output string) {
	defer func() {
		m.trace.MarkOnce(perftrace.TUIInitialViewReturned, perftrace.Fields{View: perftrace.View(m.view.String())})
		m.signalFirstView()
	}()
	if m.quitting {
		return ""
	}
	if m.noteMode() {
		return m.renderNotes()
	}
	if m.overlay.kind != overlayNone {
		return m.renderOverlay()
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
	case ViewFleet:
		b.WriteString(m.renderFleet())
	case ViewTries:
		b.WriteString(m.renderTries())
	case ViewRemote:
		b.WriteString(m.renderRemotes())
	case ViewSkills:
		b.WriteString(m.renderSkills())
	case ViewMCP:
		b.WriteString(m.renderMCP())
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
	build := func(compact, summary bool) string {
		short := map[View]string{
			ViewTasks: "TSK", ViewRepos: "REP", ViewFleet: "FLT", ViewTries: "TRY",
			ViewRemote: "REM", ViewSkills: "SKL", ViewMCP: "MCP",
		}
		var tabs []string
		for _, view := range Views {
			name := strings.ToUpper(view.String())
			if compact {
				name = short[view]
			}
			label := name
			if !compact {
				label = " " + name + " "
			}
			if view == m.view {
				tabs = append(tabs, styleSelected.Render(label))
			} else {
				tabs = append(tabs, styleDim.Render(label))
			}
		}
		line := styleTitle.Render("dev") + "  " + strings.Join(tabs, styleDim.Render("│"))
		if summary {
			line += "   " + styleDim.Render(m.Summary())
		}
		if m.filter != "" {
			line += "   " + styleWarm.Render("/"+m.filter)
		}
		return line
	}
	line := build(false, true)
	if lipgloss.Width(line) > m.width {
		line = build(false, false)
	}
	if lipgloss.Width(line) > m.width {
		line = build(true, false)
	}
	if lipgloss.Width(line) > m.width {
		line = styleTitle.Render("dev") + "  " + styleSelected.Render(strings.ToUpper(m.view.String()))
		if m.filter != "" {
			available := m.width - lipgloss.Width(line) - 4
			if available > 1 {
				line += "   " + styleWarm.Render("/"+pad(m.filter, available-1))
			}
		}
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

const (
	taskRepoBreakpoint = 97
	taskRepoWidth      = 16
)

func (m Model) renderTasks() string {
	rows := m.visibleTasks()
	if len(rows) == 0 {
		return m.emptyTasks()
	}
	nameW, branchW, nextW := m.columnWidths()
	showRepo := m.width >= taskRepoBreakpoint

	headers := []string{
		fitCell("TASK", nameW),
		fitCell("STATE", 6),
	}
	if showRepo {
		headers = append(headers, fitCell("REPO", taskRepoWidth))
	}
	headers = append(headers,
		fitCell("BRANCH", branchW),
		fitCell("GIT", 16),
		fitCell("AGE", 5),
		fitCell("NEXT", nextW),
	)

	var b strings.Builder
	b.WriteString(styleHeader.Render("  "+strings.Join(headers, "  ")) + "\n")

	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		r := rows[i]
		cells := []string{
			fitCell(r.Task.Title(), nameW),
			fitCell(r.Task.State.Label(), 6),
		}
		if showRepo {
			cells = append(cells, fitCell(r.Task.Repo, taskRepoWidth))
		}
		cells = append(cells,
			fitCell(r.Task.Branch, branchW),
			fitCell(gitColumn(r), 16),
			fitCell(shortAge(r), 5),
			fitCell(nextColumn(r), nextW),
		)
		line := strings.Join(cells, "  ")
		b.WriteString(m.renderLine(i, line, colourState(r.Task.State, line)))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func (m Model) renderRepos() string {
	items := m.visibleRepoItems()
	if len(items) == 0 {
		if m.viewLoad(ViewRepos).loading {
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

	from, to := m.window(len(items))
	for i := from; i < to; i++ {
		item := items[i]
		r := item.Repo
		cells := make([]string, 0, len(columns))
		for _, c := range columns {
			cells = append(cells, fitCell(m.repoItemColumnValue(item, c.name), c.width))
		}
		line := strings.Join(cells, "  ")
		styled := line
		if checkout, child := item.checkout(); child {
			if checkout.Status.Dirty() {
				styled = styleDirty.Render(line)
			} else if checkout.Ownership == inventory.CheckoutExternal ||
				checkout.Ownership == inventory.CheckoutEphemeral {
				styled = styleDim.Render(line)
			}
		} else if r.Status.Dirty() {
			styled = styleDirty.Render(line)
		} else if len(r.Tasks) == 0 && !r.Live {
			styled = styleClean.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(items), from, to))
	return b.String()
}

func (m Model) repoItemColumnValue(item repoItem, name string) string {
	r := item.Repo
	checkout, child := item.checkout()
	if !child {
		if name == "repo" && r.Worktrees > 0 {
			marker := "▸ "
			if m.repoExpanded(r) {
				marker = "▾ "
			}
			return marker + r.Repo.Display()
		}
		if name == "live" {
			return parentLiveColumn(r)
		}
		return repoColumnValue(r, name)
	}

	switch name {
	case "repo":
		connector := "├─ "
		if item.CheckoutIndex == len(r.Context.Checkouts)-1 {
			connector = "└─ "
		}
		label := filepath.Base(checkout.Worktree.Path)
		if label == "." || label == string(filepath.Separator) || label == "" {
			label = checkout.Branch()
		}
		if checkout.Ownership == inventory.CheckoutExternal || checkout.Ownership == inventory.CheckoutEphemeral {
			label += " (" + string(checkout.Ownership) + ")"
		}
		return connector + label
	case "branch":
		if branch := checkout.Branch(); branch != "" {
			return branch
		}
		return "(detached)"
	case "git":
		switch {
		case checkout.Worktree.Prunable || !checkout.Exists:
			return "prunable"
		case checkout.Worktree.Locked:
			return "locked"
		case checkout.StatusErr != nil:
			return "?"
		default:
			return checkout.Status.Summary()
		}
	case "live":
		return checkoutLiveColumn(r.Runtime, checkout, r.Context.RuntimeErr)
	case "latest":
		return latestAge(checkout.LastActivity)
	case "worktrees":
		return "—"
	case "tasks":
		if r.Context.TaskErr != nil {
			return "?"
		}
		return taskStateSummary(checkout.Tasks)
	case "notes":
		if r.NoteCount > 0 {
			return fmt.Sprintf("%d", r.NoteCount)
		}
		return "—"
	case "category":
		return "—"
	case "path":
		return contract(checkout.Worktree.Path)
	}
	return ""
}

func parentLiveColumn(r RepoRow) string {
	if r.Context.RuntimeErr != nil {
		return "?"
	}
	sessions := r.Sessions()
	if len(sessions) == 0 {
		return repoColumnValue(r, "live")
	}
	if len(sessions) > 1 {
		return fmt.Sprintf("%s:%d live", r.Runtime, len(sessions))
	}
	status := sessions[0].AgentStatus
	if status != "" {
		return r.Runtime + ":" + status
	}
	return r.Runtime
}

func checkoutLiveColumn(runtimeName string, checkout inventory.RepoCheckout, runtimeErr error) string {
	if runtimeErr != nil {
		return "?"
	}
	if len(checkout.Sessions) == 0 {
		return "closed"
	}
	if len(checkout.Sessions) > 1 {
		return fmt.Sprintf("%s:%d live", runtimeName, len(checkout.Sessions))
	}
	if status := checkout.Sessions[0].AgentStatus; status != "" {
		return runtimeName + ":" + status
	}
	return runtimeName
}

func taskStateSummary(tasks []*task.Task) string {
	if len(tasks) == 0 {
		return "—"
	}
	counts := map[task.State]int{}
	for _, t := range tasks {
		counts[t.State]++
	}
	var parts []string
	for _, state := range task.States {
		if counts[state] > 0 {
			parts = append(parts, state.Icon()+fmt.Sprintf("%d", counts[state]))
		}
	}
	return strings.Join(parts, " ")
}

type repoColumnSpec struct {
	name, header string
	width, min   int
}

func (m Model) repoColumns() []repoColumnSpec {
	names := m.actions.RepoColumns
	if len(names) == 0 {
		names = []string{"repo", "branch", "git", "size", "live", "latest", "worktrees", "tasks"}
	}
	defaults := map[string]repoColumnSpec{
		"repo":      {"repo", "REPO", 28, 12},
		"branch":    {"branch", "BRANCH", 22, 10},
		"git":       {"git", "GIT", 16, 8},
		"remote":    {"remote", "REMOTE", 24, 10},
		"size":      {"size", "SIZE", 9, 6},
		"live":      {"live", "LIVE", 15, 7},
		"latest":    {"latest", "LATEST", 8, 6},
		"worktrees": {"worktrees", "WT", 3, 2},
		"tasks":     {"tasks", "TASKS", 18, 8},
		"notes":     {"notes", "NOTES", 6, 5},
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
	for _, preferred := range []string{"path", "remote", "tasks", "repo", "branch", "live", "git", "size"} {
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
	case "remote":
		if r.TopologyErr != nil {
			return "?"
		}
		return r.Topology.Summary()
	case "size":
		return sizeCell(r.Usage, r.SizeError, r.SizeTarget)
	case "live":
		if r.Context.RuntimeErr != nil {
			return "?"
		}
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
		if r.Context.TaskErr != nil {
			return "?"
		}
		if tasks := r.StateSummary(); tasks != "" {
			return tasks
		}
		return "—"
	case "notes":
		if r.NoteCount > 0 {
			return fmt.Sprintf("%d", r.NoteCount)
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
	if m.viewLoad(ViewRemote).loading && len(m.remotes) == 0 {
		return "  " + styleDim.Render("Loading repositories from forge CLIs…") + "\n"
	}
	rows := m.visibleRemotes()
	if len(rows) == 0 {
		if !m.viewLoad(ViewRemote).hasSnapshot {
			return "  " + styleDim.Render("Remote repositories load when this view is opened.") + "\n"
		}
		if m.filter != "" {
			return "  " + styleDim.Render("No remote repository matches /"+m.filter) + "\n"
		}
		return "  " + styleDim.Render("No remote repositories returned. Check forge authentication and `dev doctor`.") + "\n"
	}
	nameW := clamp(m.width*30/100, 18, 38)
	descW := m.width - nameW - 38
	if descW < 12 {
		descW = 12
	}

	var b strings.Builder
	if m.viewLoad(ViewRemote).loading {
		b.WriteString("  " + styleDim.Render("Showing cached repositories while refreshing…") + "\n")
	}
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-7s  %-*s  %-9s  %-12s  %-7s  %s",
		"FORGE", nameW, "REPOSITORY", "VIS", "UPDATED", "LOCAL", "DESCRIPTION")) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		r := rows[i]
		pending := m.remoteCloneTargets(r)
		local := "—"
		if pending {
			local = m.remoteCloneSpinner.View() + " repo"
			if m.remoteClone.phase == remoteCloneRunning {
				local = m.remoteCloneSpinner.View() + " clone"
			}
		} else if r.CloneProblemPath != "" {
			local = "inspect"
		} else if r.Cloned() {
			local = "yes"
			if r.LocalKind != "" {
				local = string(r.LocalKind)
				if r.LocalKind == "repository" {
					local = "repo"
				}
			}
		}
		vis := strings.ToLower(r.Repo.Visibility)
		if vis == "" {
			vis = "—"
		}
		line := fmt.Sprintf("%-7s  %-*s  %-9s  %-12s  %-7s  %s",
			r.Repo.Forge, nameW, pad(r.Repo.FullName, nameW),
			pad(vis, 9), pad(remoteAge(r), 12), pad(local, 7), pad(r.Repo.Description, descW))
		styled := line
		if r.Cloned() || pending {
			styled = styleLive.Render(line)
		} else if r.CloneProblemPath != "" {
			styled = styleDrift.Render(line)
		} else if r.Repo.Archived {
			styled = styleDim.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func (m Model) renderFleet() string {
	rows := m.visibleFleet()
	if len(rows) == 0 {
		if m.viewLoad(ViewFleet).loading {
			return "  " + styleDim.Render("Loading configured dev hosts…") + "\n"
		}
		if !m.viewLoad(ViewFleet).hasSnapshot {
			return "  " + styleDim.Render("Fleet repositories load when this view is opened.") + "\n"
		}
		if !m.showLocalFleet {
			for _, row := range m.fleet {
				if row.Local && matches(row.searchText(), m.filter) {
					return "  " + styleDim.Render("No remote fleet rows. Press a to include this machine.") + "\n"
				}
			}
		}
		return "  " + styleDim.Render("No fleet row matches the current filter.") + "\n"
	}
	hostW := clamp(m.width*14/100, 10, 22)
	repoW := clamp(m.width*24/100, 16, 34)
	pathW := m.width - hostW - repoW - 58
	if pathW < 12 {
		pathW = 12
	}
	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-*s  %-8s  %-*s  %-18s  %-14s  %-10s  %-8s  %s",
		hostW, "HOST", "STATE", repoW, "REPO", "BRANCH", "GIT", "LIVE", "TASKS", "PATH")) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		row := rows[i]
		repoName, branch, git, live, tasks, path := "—", "—", "—", "—", "—", row.Error
		if row.Repository != nil {
			repoName, branch, git, path = row.Repository.Display, row.Repository.Branch, row.Repository.Status.Summary(), contract(row.Repository.Path)
			if row.Repository.Live {
				live = row.Repository.Runtime
				if row.Repository.AgentStatus != "" {
					live += ":" + row.Repository.AgentStatus
				}
			}
			tasks = fleetTasks(row.Repository.Tasks.Hot, row.Repository.Tasks.Warm, row.Repository.Tasks.Cold, row.Repository.Tasks.Done)
		}
		state := string(row.State)
		if row.FromCache {
			state = "stale"
		}
		line := fmt.Sprintf("%-*s  %-8s  %-*s  %-18s  %-14s  %-10s  %-8s  %s",
			hostW, pad(row.Host, hostW), pad(state, 8), repoW, pad(repoName, repoW), pad(branch, 18),
			pad(git, 14), pad(live, 10), pad(tasks, 8), pad(path, pathW))
		styled := line
		if row.Repository != nil && row.Repository.Live {
			styled = styleLive.Render(line)
		} else if row.State != "ok" {
			styled = styleDrift.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}
func (m Model) renderSkills() string {
	state := m.viewLoad(ViewSkills)
	rows := m.visibleSkills()
	if state.loading && len(m.skills) == 0 {
		return "  " + styleDim.Render("Loading agent skills across repositories…") + "\n"
	}
	if len(rows) == 0 {
		if !state.hasSnapshot {
			return "  " + styleDim.Render("Agent skills load when this view is opened.") + "\n"
		}
		if m.filter != "" {
			return "  " + styleDim.Render("No agent skill matches /"+m.filter) + "\n"
		}
		return "  " + styleDim.Render("No agent skills found. Press a to open the installer.") + "\n"
	}

	var headers []string
	var values func(agentskill.Skill) []string
	switch {
	case m.width >= 110:
		repoW, scopeW, skillW, installW, updateW, agentW := 16, 7, 20, 9, 10, 20
		sourceW := max(12, m.width-96)
		headers = []string{fitCell("REPO", repoW), fitCell("SCOPE", scopeW), fitCell("SKILL", skillW), fitCell("INSTALL", installW), fitCell("UPDATE", updateW), fitCell("AGENTS", agentW), fitCell("SOURCE", sourceW)}
		values = func(row agentskill.Skill) []string {
			return []string{fitCell(dashCell(row.Repository), repoW), fitCell(string(row.Scope), scopeW), fitCell(row.Name, skillW), fitCell(skillInstallState(row), installW), fitCell(skillUpdateLabel(row.UpdateStatus), updateW), fitCell(skillAgentSummary(row.Agents), agentW), fitCell(skillSource(row), sourceW)}
		}
	case m.width >= 82:
		repoW, scopeW, installW, updateW := 16, 7, 9, 10
		flex := max(28, m.width-54)
		skillW, agentW := flex/2, flex-flex/2
		headers = []string{fitCell("REPO", repoW), fitCell("SCOPE", scopeW), fitCell("SKILL", skillW), fitCell("INSTALL", installW), fitCell("UPDATE", updateW), fitCell("AGENTS", agentW)}
		values = func(row agentskill.Skill) []string {
			return []string{fitCell(dashCell(row.Repository), repoW), fitCell(string(row.Scope), scopeW), fitCell(row.Name, skillW), fitCell(skillInstallState(row), installW), fitCell(skillUpdateLabel(row.UpdateStatus), updateW), fitCell(skillAgentSummary(row.Agents), agentW)}
		}
	default:
		repoW, updateW := 14, 10
		skillW := max(12, m.width-30)
		headers = []string{fitCell("REPO", repoW), fitCell("SKILL", skillW), fitCell("UPDATE", updateW)}
		values = func(row agentskill.Skill) []string {
			return []string{fitCell(dashCell(row.Repository), repoW), fitCell(row.Name, skillW), fitCell(skillUpdateLabel(row.UpdateStatus), updateW)}
		}
	}

	var b strings.Builder
	if state.loading {
		message := "Showing installed skills while refreshing…"
		if m.skillsChecking {
			message = "Showing installed skills while checking sources…"
		}
		b.WriteString("  " + styleDim.Render(message) + "\n")
	}
	b.WriteString(styleHeader.Render("  "+strings.Join(headers, "  ")) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		row := rows[i]
		line := strings.Join(values(row), "  ")
		styled := line
		switch row.UpdateStatus {
		case agentskill.UpdateAvailable, agentskill.UpdateMissing, agentskill.UpdateFailed:
			styled = styleDrift.Render(line)
		case agentskill.UpdateCurrent:
			styled = styleLive.Render(line)
		case agentskill.UpdateUnknown:
			styled = styleDim.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func skillInstallState(row agentskill.Skill) string {
	if row.Presence == agentskill.PresenceMissing {
		return "missing"
	}
	switch row.Integrity {
	case agentskill.IntegrityVerified:
		return "verified"
	case agentskill.IntegrityDrifted:
		return "drifted"
	default:
		return "present"
	}
}

func (m Model) renderMCP() string {
	state := m.viewLoad(ViewMCP)
	rows := m.visibleMCP()
	if state.loading && len(m.mcp) == 0 {
		return "  " + styleDim.Render("Loading static MCP declarations…") + "\n"
	}
	if len(rows) == 0 {
		if !state.hasSnapshot {
			return "  " + styleDim.Render("MCP declarations load when this view is opened.") + "\n"
		}
		if m.filter != "" {
			return "  " + styleDim.Render("No MCP declaration matches /"+m.filter) + "\n"
		}
		return "  " + styleDim.Render("No MCP server declarations found.") + "\n"
	}

	var headers []string
	var values func(agentmcp.Declaration) []string
	switch {
	case m.width >= 112:
		repoW, scopeW, agentW, transportW, stateW, sourceW := 16, 12, 12, 16, 10, 14
		serverW := max(18, m.width-94)
		headers = []string{fitCell("REPO", repoW), fitCell("SCOPE", scopeW), fitCell("AGENT", agentW), fitCell("SERVER", serverW), fitCell("TRANSPORT", transportW), fitCell("STATE", stateW), fitCell("SOURCE", sourceW)}
		values = func(row agentmcp.Declaration) []string {
			return []string{fitCell(dashCell(row.Repository), repoW), fitCell(string(row.Scope), scopeW), fitCell(string(row.Agent), agentW), fitCell(row.Name, serverW), fitCell(string(row.Transport), transportW), fitCell(mcpDeclarationState(row), stateW), fitCell(mcpSource(row), sourceW)}
		}
	case m.width >= 94:
		repoW, scopeW, agentW, transportW, stateW := 16, 12, 12, 16, 10
		serverW := max(16, m.width-78)
		headers = []string{fitCell("REPO", repoW), fitCell("SCOPE", scopeW), fitCell("AGENT", agentW), fitCell("SERVER", serverW), fitCell("TRANSPORT", transportW), fitCell("STATE", stateW)}
		values = func(row agentmcp.Declaration) []string {
			return []string{fitCell(dashCell(row.Repository), repoW), fitCell(string(row.Scope), scopeW), fitCell(string(row.Agent), agentW), fitCell(row.Name, serverW), fitCell(string(row.Transport), transportW), fitCell(mcpDeclarationState(row), stateW)}
		}
	default:
		repoW, agentW, stateW := 14, 12, 10
		serverW := max(12, m.width-44)
		headers = []string{fitCell("REPO", repoW), fitCell("AGENT", agentW), fitCell("SERVER", serverW), fitCell("STATE", stateW)}
		values = func(row agentmcp.Declaration) []string {
			return []string{fitCell(dashCell(row.Repository), repoW), fitCell(string(row.Agent), agentW), fitCell(row.Name, serverW), fitCell(mcpDeclarationState(row), stateW)}
		}
	}

	var b strings.Builder
	if state.loading {
		b.WriteString("  " + styleDim.Render("Showing MCP declarations while refreshing…") + "\n")
	}
	b.WriteString(styleHeader.Render("  "+strings.Join(headers, "  ")) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		row := rows[i]
		line := strings.Join(values(row), "  ")
		styled := line
		if row.Enabled != nil && !*row.Enabled {
			styled = styleDim.Render(line)
		} else if row.Enabled != nil && *row.Enabled {
			styled = styleLive.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func mcpSource(row agentmcp.Declaration) string {
	source := string(row.Source)
	if row.Plugin != "" {
		source += ":" + row.Plugin
	}
	return source
}

func dashCell(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func mcpPolicySummary(row agentmcp.Declaration) string {
	parts := make([]string, 0, len(row.Policies))
	for _, policy := range row.Policies {
		part := string(policy.Kind)
		if policy.Value != "" {
			part += ":" + policy.Value
		}
		if policy.Count > 0 {
			part += fmt.Sprintf(":%d", policy.Count)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func mcpCredentialSummary(row agentmcp.Declaration, maxWidth int) string {
	parts := make([]string, 0, len(row.Credentials))
	for index, credential := range row.Credentials {
		part := string(credential.Kind)
		if credential.Name != "" {
			part += ":" + credential.Name
		}
		candidate := strings.Join(append(append([]string(nil), parts...), part), ", ")
		if lipgloss.Width(candidate) > maxWidth {
			remaining := len(row.Credentials) - index
			if len(parts) == 0 {
				if remaining == 1 {
					return pad(part, maxWidth)
				}
				return pad(fmt.Sprintf("%s, +%d", part, remaining-1), maxWidth)
			}
			return pad(strings.Join(parts, ", ")+fmt.Sprintf(", +%d", remaining), maxWidth)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func fleetTasks(hot, warm, cold, done int) string {
	var parts []string
	for _, item := range []struct {
		label string
		n     int
	}{{"H", hot}, {"W", warm}, {"C", cold}, {"D", done}} {
		if item.n > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", item.label, item.n))
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

func skillUpdateLabel(status agentskill.UpdateStatus) string {
	switch status {
	case agentskill.UpdateAvailable:
		return "update"
	case agentskill.UpdateMissing:
		return "missing"
	case agentskill.UpdateUnknown:
		return "unknown"
	case agentskill.UpdateFailed:
		return "failed"
	default:
		return string(status)
	}
}

func skillAgentSummary(agents []string) string {
	if len(agents) == 0 {
		return "—"
	}
	if len(agents) <= 3 {
		return strings.Join(agents, ", ")
	}
	return strings.Join(agents[:3], ", ") + fmt.Sprintf(" +%d", len(agents)-3)
}

func skillSource(row agentskill.Skill) string {
	if row.Source != "" {
		return row.Source
	}
	return string(row.ManagedBy)
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
	if m.viewLoad(ViewTasks).loading {
		return "  " + styleDim.Render("Loading tasks, repositories, and experiments…") + "\n"
	}
	if len(m.rows) == 0 {
		return "  " + styleDim.Render("No tasks recorded yet.\n") +
			"  " + styleDim.Render("Press tab for the repository list, then s to start work on one.\n") +
			"  " + styleDim.Render("Or run `dev adopt` to import worktrees and branches already in flight.\n")
	}
	return "  " + styleDim.Render("Nothing matches. Press 0 to clear the filter, or a to include done tasks.\n")
}

func (m Model) columnWidths() (name, branch, next int) {
	fixed := 2 + 6 + 16 + 5 + 10
	if m.width >= taskRepoBreakpoint {
		fixed += taskRepoWidth + 2
	}
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
		r := m.remoteClonePrompt
		return "  " + styleTitle.Render("clone "+r.Repo.FullName) +
			"\n  to: " + contract(filepath.Join("<project_root>", r.Repo.Name)) +
			"\n  " + styleHelp.Render("enter clone and stay · o clone and open · esc cancel")
	case modeConfirmSkillUpdate:
		row := m.skillUpdateTarget
		return "  " + styleTitle.Render("update "+string(row.Scope)+" skill "+row.Name) +
			"\n  " + styleHelp.Render("enter to update this skill · esc to cancel")
	case modeCopy:
		return "  " + styleTitle.Render("copy ") +
			"y context · p path · b branch · s sessions · w worktree paths" +
			"\n  " + styleHelp.Render("press a second key · esc to cancel")
	}

	if row, ok := m.currentFleet(); ok {
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("host"), row.Host),
			fmt.Sprintf("  %s %s", styleDim.Render("state"), row.State),
		}
		if row.Repository != nil {
			lines = append(lines,
				fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(row.Repository.Path)),
				fmt.Sprintf("  %s %s", styleDim.Render("branch"), row.Repository.Branch),
				fmt.Sprintf("  %s %s", styleDim.Render("git  "), row.Repository.Status.Breakdown()),
			)
			if len(row.Repository.RemoteIdentities) > 0 {
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("remote"), strings.Join(row.Repository.RemoteIdentities, ", ")))
			}
			if row.Repository.Live {
				live := strings.TrimSpace(row.Repository.Runtime + " " + row.Repository.RuntimeHandle)
				if row.Repository.AgentStatus != "" {
					live += " · " + row.Repository.AgentStatus
				}
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(live)))
			}
		}
		if row.Error != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("note "), styleDrift.Render(row.Error)))
		}
		return strings.Join(lines, "\n") + "\n"
	}
	if row, ok := m.currentMCP(); ok {
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("repo"), dashCell(row.Repository)),
			fmt.Sprintf("  %s %s", styleDim.Render("scope "), row.Scope),
			fmt.Sprintf("  %s %s", styleDim.Render("agent "), row.Agent),
			fmt.Sprintf("  %s %s", styleDim.Render("config"), contract(row.ConfigPath)),
			fmt.Sprintf("  %s %s", styleDim.Render("source"), mcpSource(row)),
			fmt.Sprintf("  %s %s", styleDim.Render("state "), mcpDeclarationState(row)),
			fmt.Sprintf("  %s %s", styleDim.Render("type  "), row.Transport),
		}
		if row.LocalProjectPath != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("project"), contract(row.LocalProjectPath)))
		}
		if row.Required != nil {
			lines = append(lines, fmt.Sprintf("  %s %t", styleDim.Render("required"), *row.Required))
		}
		if row.Trusted != nil {
			lines = append(lines, fmt.Sprintf("  %s %t", styleDim.Render("trusted "), *row.Trusted))
		}
		if summary := mcpPolicySummary(row); summary != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("policies"), summary))
		}
		if row.Endpoint != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("target"), row.Endpoint))
		}
		if row.Command != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("command"), row.Command))
		}
		if row.ArgumentCount > 0 {
			lines = append(lines, fmt.Sprintf("  %s %d value(s) redacted", styleDim.Render("args  "), row.ArgumentCount))
		}
		credentialWidth := max(1, m.width-lipgloss.Width("  credentials "))
		if summary := mcpCredentialSummary(row, credentialWidth); summary != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("credentials"), summary))
		}
		if len(row.Redactions) > 0 {
			parts := make([]string, len(row.Redactions))
			for i, redaction := range row.Redactions {
				parts[i] = string(redaction)
			}
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("redacted"), strings.Join(parts, ", ")))
		}
		if len(row.Coverage) > 0 {
			parts := make([]string, len(row.Coverage))
			for i, item := range row.Coverage {
				parts[i] = string(item.Code)
			}
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("coverage"), strings.Join(parts, ", ")))
		}
		return strings.Join(lines, "\n") + "\n"
	}
	if row, ok := m.currentSkill(); ok {
		lines := []string{
			fmt.Sprintf("  %s %s", styleDim.Render("scope  "), row.Scope),
			fmt.Sprintf("  %s %s", styleDim.Render("root   "), contract(row.ScopeRoot)),
			fmt.Sprintf("  %s %s", styleDim.Render("path   "), contract(row.Path)),
			fmt.Sprintf("  %s %s", styleDim.Render("managed"), row.ManagedBy),
			fmt.Sprintf("  %s %s", styleDim.Render("agents "), strings.Join(row.Agents, ", ")),
			fmt.Sprintf("  %s %s", styleDim.Render("update "), row.UpdateStatus),
		}
		if row.Source != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("source "), row.Source))
		}
		if row.SourceURL != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("url    "), row.SourceURL))
		}
		if row.UpdateDetail != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("detail "), row.UpdateDetail))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	if r, ok := m.currentTry(); ok {
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(r.Item.Live.CurrentPath)),
			fmt.Sprintf("  %s %s · %s", styleDim.Render("state"), r.Item.Phase, r.Where()),
		}
		lines = append(lines, sizeDetailLines(r.Usage, r.SizeError, r.SizeTarget)...)
		if r.Item.Live.Status != nil && r.Item.Live.Status.Dirty() {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("git  "), r.Item.Live.Status.Breakdown()))
			if types := r.Item.Live.Status.TypeBreakdown(); types != "" {
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("types"), types))
			}
		} else if r.Item.Live.Repo != nil {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("git  "), tryGitSummary(r)))
		}
		if r.Item.Live.Repo != nil {
			if r.TopologyErr != nil {
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("remote"), styleErr.Render("unknown: "+r.TopologyErr.Error())))
			} else {
				remoteSummary := r.Topology.Summary()
				if !r.Topology.HasRemote() {
					remoteSummary = styleDrift.Render(remoteSummary + " — local Git has no remote backup")
				}
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("remote"), remoteSummary))
				if len(r.Topology.LocalOnlyBranches) > 0 {
					lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("local refs"),
						styleDrift.Render(strings.Join(r.Topology.LocalOnlyBranches, ", "))))
				}
			}
		}
		if len(r.Item.Tags) > 0 {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("tags"), strings.Join(r.Item.Tags, ", ")))
		}
		if r.Item.Note != "" {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("note"), r.Item.Note))
		}
		if r.Item.OriginURL != "" {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("origin"), r.Item.OriginURL))
		}
		if r.Live {
			live := r.Runtime + " " + r.RuntimeHandle
			if r.RuntimeStatus != "" {
				live += " · " + r.RuntimeStatus
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(live)))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	if r, ok := m.currentRemote(); ok {
		lines := []string{
			fmt.Sprintf("  %s %s", styleDim.Render("url  "), r.Repo.URL),
			fmt.Sprintf("  %s %s", styleDim.Render("branch"), r.Repo.DefaultBranch),
		}
		if m.remoteCloneTargets(r) {
			if r.LocalPath != "" {
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("local "), contract(r.LocalPath)))
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("asset "), "repository"))
			}
			lines = append(lines, fmt.Sprintf("  %s %s %s", styleDim.Render("state "),
				m.remoteCloneSpinner.View(), m.remoteCloneStatus()))
		} else if r.Cloned() {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("local "), contract(r.LocalPath)))
			if r.LocalKind != "" {
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("asset "), r.LocalKind))
			}
		} else if r.CloneProblemPath != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("inspect"), contract(r.CloneProblemPath)))
			lines = append(lines, "  "+styleDim.Render("action")+" "+
				styleDrift.Render("inspect or move this destination before retrying"))
		} else {
			lines = append(lines, "  "+styleDim.Render("local ")+" "+
				styleDim.Render("not cloned — press c"))
		}
		if r.Repo.Fork {
			lines = append(lines, "  "+styleDim.Render("kind  ")+" fork")
		}
		return strings.Join(lines, "\n") + "\n"
	}

	if item, ok := m.currentRepoItem(); ok && item.child() {
		checkout, _ := item.checkout()
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(checkout.Worktree.Path)),
			fmt.Sprintf("  %s %s", styleDim.Render("branch"), checkout.Branch()),
			fmt.Sprintf("  %s  %s", styleDim.Render("owner"), checkout.Ownership),
			fmt.Sprintf("  %s %s", styleDim.Render("ready"), repocontext.AssessLocal(item.Repo.Context, item.CheckoutIndex, config.Hostname()).Summary()),
		}
		if checkout.StatusErr != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("git"), styleErr.Render(checkout.StatusErr.Error())))
		} else if checkout.Status.Dirty() {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("git  "), checkout.Status.Breakdown()))
			if types := checkout.Status.TypeBreakdown(); types != "" {
				lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("types"), types))
			}
		}
		if checkout.Worktree.Locked {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("state"), styleDrift.Render("locked")))
		}
		if checkout.Worktree.Prunable || !checkout.Exists {
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("state"), styleDrift.Render("prunable; checkout missing")))
		}
		if item.Repo.Context.RuntimeErr != nil {
			lines = append(lines, "  "+styleDim.Render("live")+"  "+styleErr.Render("unavailable"))
		} else if len(checkout.Sessions) == 0 {
			lines = append(lines, "  "+styleDim.Render("live")+"  "+styleDim.Render("closed"))
		} else {
			for _, session := range checkout.Sessions {
				live := item.Repo.Runtime + " " + session.Handle
				if session.AgentStatus != "" {
					live += " · " + session.AgentStatus
				}
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(live)))
				if len(session.AgentSessions) > 0 {
					lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("agents"), strings.Join(session.AgentSessions, "  ")))
				}
			}
		}
		if item.Repo.Context.TaskErr != nil {
			lines = append(lines, "  "+styleDim.Render("tasks")+" "+styleErr.Render("unavailable"))
		} else if len(checkout.Tasks) > 0 {
			var names []string
			for _, t := range checkout.Tasks {
				names = append(names, t.State.Icon()+" "+t.Title())
			}
			lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("tasks"), strings.Join(names, "  ")))
		} else {
			lines = append(lines, "  "+styleDim.Render("tasks")+" "+styleDim.Render("untracked"))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	if r, ok := m.currentRepo(); ok {
		lines := []string{
			fmt.Sprintf("  %s  %s", styleDim.Render("path"), contract(r.Repo.Path)),
			fmt.Sprintf("  %s %s", styleDim.Render("ready"), repocontext.AssessLocal(r.Context, 0, config.Hostname()).Summary()),
		}
		lines = append(lines, sizeDetailLines(r.Usage, r.SizeError, r.SizeTarget)...)
		if r.Asset != nil {
			if len(r.Asset.Tags) > 0 {
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("tags"), strings.Join(r.Asset.Tags, ", ")))
			}
			if r.Asset.Note != "" {
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("note"), r.Asset.Note))
			}
		}
		if r.TopologyErr != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("remote"), styleErr.Render("unknown: "+r.TopologyErr.Error())))
		} else {
			remoteSummary := r.Topology.Summary()
			if !r.Topology.HasRemote() {
				remoteSummary = styleDrift.Render(remoteSummary + " — local Git has no remote backup")
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("remote"), remoteSummary))
			if len(r.Topology.LocalOnlyBranches) > 0 {
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("local refs"),
					styleDrift.Render(strings.Join(r.Topology.LocalOnlyBranches, ", "))))
			}
			if r.Topology.MultipleUpstreams() {
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("upstreams"),
					strings.Join(r.Topology.UpstreamRemotes, ", ")))
			}
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
		if r.Context.RuntimeErr != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleErr.Render("unavailable")))
		} else if sessions := r.Sessions(); len(sessions) > 0 {
			if len(sessions) == 1 {
				live := r.Runtime + " " + sessions[0].Handle
				if sessions[0].AgentStatus != "" {
					live += " · " + sessions[0].AgentStatus
				}
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(live)))
			} else {
				lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"),
					styleLive.Render(fmt.Sprintf("%d %s sessions", len(sessions), r.Runtime))))
			}
		} else if r.Live {
			live := r.Runtime + " " + r.RuntimeHandle
			if r.RuntimeStatus != "" {
				live += " · " + r.RuntimeStatus
			}
			lines = append(lines, fmt.Sprintf("  %s  %s", styleDim.Render("live"), styleLive.Render(live)))
		}
		if r.NoteCount > 0 && r.LatestNote != nil {
			lines = append(lines, fmt.Sprintf("  %s  %d · %s", styleDim.Render("notes"),
				r.NoteCount, r.LatestNote.Preview(maxInt(20, m.width-18))))
		}
		if r.Context.TaskErr != nil {
			lines = append(lines, "  "+styleDim.Render("tasks")+" "+styleErr.Render("unavailable"))
		} else if len(r.Tasks) > 0 {
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
	if count, latest := m.noteSummary(row.Task.RepoPath, row.Task.Repo); count > 0 && latest != nil {
		lines = append(lines, fmt.Sprintf("  %s  %d · %s", styleDim.Render("notes"),
			count, latest.Preview(maxInt(20, m.width-18))))
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
	if command := taskRecoveryCommand(row); command != "" {
		hint := "run `" + command + "`"
		if row.WorktreeMissing {
			hint += "; salvage anything it reports, then resume or reap"
		}
		lines = append(lines, fmt.Sprintf("  %s %s", styleDim.Render("action"), styleDrift.Render(hint)))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) renderFooter() string {
	viewErr, viewStatus := m.viewError(m.view), m.viewStatus(m.view)
	var status string
	switch {
	case m.err != nil:
		status = styleErr.Render("✗ " + m.err.Error())
	case m.remoteClone.active():
		status = styleLive.Render(m.remoteCloneSpinner.View() + " " + m.remoteCloneStatus())
	case viewErr != nil:
		status = styleErr.Render("✗ " + viewErr.Error())
	case m.status != "":
		status = styleOK.Render("✓ " + m.status)
	case viewStatus != "":
		status = styleOK.Render("✓ " + viewStatus)
	}

	var bindings []string
	if m.remoteClone.active() {
		bindings = []string{"q cancel clone", "tab view", "/ filter", "j/k move"}
		var b strings.Builder
		if status != "" {
			b.WriteString("  " + status + "\n")
		}
		b.WriteString("  " + styleHelp.Render(wrapBindings(bindings, m.width-4)))
		return b.String()
	}
	switch m.view {
	case ViewRepos:
		sortBy := m.actions.RepoSort
		if sortBy == "" {
			sortBy = "activity"
		}
		if m.actions.RepoReverse {
			sortBy += "↑"
		}
		if item, ok := m.currentRepoItem(); ok && item.child() {
			bindings = append(bindings, "enter open worktree", "space collapse")
		} else {
			bindings = append(bindings, "enter ad hoc", "space worktrees", "m metadata", "n add note", "N notes", "s worktree task", "d direct task")
		}
		bindings = append(bindings, "O sort:"+sortBy, "R reverse")
	case ViewFleet:
		if row, ok := m.currentFleet(); ok && row.Repository != nil {
			bindings = append(bindings, "enter remote open")
		}
		local := "hidden"
		if m.showLocalFleet {
			local = "shown"
		}
		bindings = append(bindings, "a local:"+local, "r refresh")
	case ViewTries:
		sortBy := m.trySort
		if sortBy == "" {
			sortBy = "activity"
		}
		if m.tryReverse {
			sortBy += "↑"
		}
		bindings = append(bindings, "enter open", "n new", "space actions", "a history",
			"O sort:"+sortBy, "R reverse")
	case ViewRemote:
		if m.remoteClone.active() {
			bindings = append(bindings, "q cancel clone")
		} else if r, ok := m.currentRemote(); ok && r.Cloned() {
			bindings = append(bindings, "enter open local", "n add note", "N notes")
		} else if r, ok := m.currentRemote(); ok && r.CloneProblemPath != "" {
			bindings = append(bindings, "inspect local destination")
		} else {
			bindings = append(bindings, "c clone")
		}
	case ViewSkills:
		bindings = append(bindings, "a add", "c check", "u update selected")
	case ViewMCP:
		bindings = append(bindings, "r reload declarations")
	default:
		if row, ok := m.currentTask(); ok {
			if command := taskRecoveryCommand(row); command != "" {
				bindings = append(bindings, "recovery "+command)
			} else {
				bindings = append(bindings, "enter open")
			}
		}
		bindings = append(bindings, "n add note", "N notes", "p park", "c next")
	}
	if m.view == ViewRepos {
		bindings = append(bindings, "y copy")
	}
	bindings = append(bindings, "tab view", "/ filter", "? help")
	if m.view != ViewSkills && m.view != ViewMCP {
		bindings = append(bindings, "H stats")
	}
	// `e` edits whichever configuration the current view is about.
	if m.view == ViewFleet {
		bindings = append(bindings, "e hosts")
	} else if m.view != ViewMCP {
		bindings = append(bindings, "e config")
	}
	if m.view != ViewSkills && m.view != ViewMCP {
		for _, t := range m.Tools() {
			bindings = append(bindings, t.Key+" "+t.Name)
		}
	}
	if m.view != ViewSkills && m.view != ViewMCP {
		bindings = append(bindings, "1/2/3 state")
	}
	bindings = append(bindings, "0 clear", "r reload", "q quit")

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
