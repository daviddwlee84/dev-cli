package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// SnippetQuery is an explicit network request. Ordinary dashboard filtering
// stays local; Text is sent only after the content-search action is submitted.
type SnippetQuery struct {
	Provider string
	Project  string
	Text     string
	Content  bool
}

type SnippetRow struct {
	Key, Provider, Host, ID, Project string
	Title, Description, Owner, URL   string
	Visibility                       string
	UpdatedAt                        time.Time
	Files                            []string
}

type SnippetResult struct {
	Rows     []SnippetRow
	Complete bool
	Warning  string
}

// SnippetActions adapts the same services and create wizard as the CLI. The
// dashboard neither interprets snippet content nor turns snippets into repos.
type SnippetActions struct {
	Load   func(context.Context, SnippetQuery) (SnippetResult, error)
	Open   func(context.Context, SnippetRow) error
	Create func(SnippetQuery) (*exec.Cmd, error)
}

type snippetUIState struct {
	enabled     bool
	query       SnippetQuery
	result      SnippetResult
	filter      string
	cursor      int
	order       tableSort
	generation  uint64
	loading     bool
	hasSnapshot bool
	stale       bool
	cancel      context.CancelFunc
	err         error
	status      string
}

type snippetsLoadedMsg struct {
	generation uint64
	query      SnippetQuery
	result     SnippetResult
	err        error
}

type snippetCreatedMsg struct{ err error }

func snippetDisplayText(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func (m Model) snippetsActive() bool { return m.view == ViewRemote && m.snippets.enabled }

func (m Model) activeFilter() string {
	if m.snippetsActive() {
		return m.snippets.filter
	}
	return m.filter
}

func (m *Model) setActiveFilter(value string) {
	if m.snippetsActive() {
		m.snippets.filter = value
	} else {
		m.filter = value
	}
}

func (m Model) visibleSnippets() []SnippetRow {
	var rows []SnippetRow
	for _, row := range m.snippets.result.Rows {
		text := strings.ToLower(strings.Join([]string{row.Provider, row.Host, row.ID, row.Project,
			row.Title, row.Description, row.Owner, row.Visibility, strings.Join(row.Files, " ")}, " "))
		match := true
		for _, term := range strings.Fields(strings.ToLower(m.snippets.filter)) {
			if !strings.Contains(text, term) {
				match = false
				break
			}
		}
		if match {
			rows = append(rows, row)
		}
	}
	sortModel := m
	sortModel.view, sortModel.snippets.enabled = ViewRemote, true
	return applyColumnSort(sortModel, rows, func(row SnippetRow, column string) sortCell {
		switch column {
		case "forge":
			return textCell(row.Provider)
		case "snippet":
			return textCell(row.Title)
		case "owner":
			return textCell(row.Owner)
		case "vis":
			return textCell(row.Visibility)
		case "updated":
			return timeCell(row.UpdatedAt)
		}
		return sortCell{}
	})
}

func (m Model) currentSnippet() (SnippetRow, bool) {
	if !m.snippetsActive() {
		return SnippetRow{}, false
	}
	rows := m.visibleSnippets()
	if m.snippets.cursor < 0 || m.snippets.cursor >= len(rows) {
		return SnippetRow{}, false
	}
	return rows[m.snippets.cursor], true
}

func (m *Model) cancelSnippetLoad() {
	if m.snippets.cancel != nil {
		m.snippets.cancel()
		m.snippets.cancel = nil
	}
	if m.snippets.loading {
		m.snippets.generation++
		m.snippets.loading = false
		m.snippets.stale = true
		m.snippets.status = "Snippet request canceled; refresh to retry."
	}
}

func (m Model) loadSnippets() (tea.Model, tea.Cmd) {
	m.cancelSnippetLoad()
	if m.actions.Snippets.Load == nil {
		m.snippets.err = fmt.Errorf("snippet listing is unavailable in this dashboard session")
		return m, nil
	}
	m.snippets.generation++
	m.snippets.loading, m.snippets.err, m.snippets.status = true, nil, "Loading snippet metadata…"
	if m.snippets.query.Content {
		m.snippets.status = "Searching snippet contents…"
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.snippets.cancel = cancel
	load, query, generation := m.actions.Snippets.Load, m.snippets.query, m.snippets.generation
	return m, func() tea.Msg {
		result, err := load(ctx, query)
		return snippetsLoadedMsg{generation: generation, query: query, result: result, err: err}
	}
}

func (m Model) acceptSnippets(msg snippetsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.snippets.loading || msg.generation != m.snippets.generation || msg.query != m.snippets.query {
		return m, nil
	}
	if m.snippets.cancel != nil {
		m.snippets.cancel()
		m.snippets.cancel = nil
	}
	focusModel := m
	focusModel.view = ViewRemote
	focus, selected := focusModel.currentSnippet()
	m.snippets.loading, m.snippets.err = false, msg.err
	if msg.err != nil {
		m.snippets.err = errors.New(snippetDisplayText(msg.err.Error()))
	}
	m.snippets.stale = msg.err != nil || !msg.result.Complete
	if msg.err == nil || msg.result.Rows != nil {
		msg.result.Rows = append([]SnippetRow(nil), msg.result.Rows...)
		for index := range msg.result.Rows {
			row := &msg.result.Rows[index]
			row.Title, row.Description, row.Owner = snippetDisplayText(row.Title), snippetDisplayText(row.Description), snippetDisplayText(row.Owner)
			row.Files = append([]string(nil), row.Files...)
			for file := range row.Files {
				row.Files[file] = snippetDisplayText(row.Files[file])
			}
		}
		m.snippets.result, m.snippets.hasSnapshot = msg.result, true
	}
	m.snippets.status = snippetDisplayText(msg.result.Warning)
	if !msg.result.Complete && msg.err == nil && m.snippets.status == "" {
		m.snippets.status = "Partial snippet inventory; some providers or content could not be read."
	}
	if errors.Is(msg.err, context.Canceled) {
		m.snippets.err = nil
		m.snippets.status = "Snippet request canceled; refresh to retry."
	}
	if selected {
		for index, row := range m.visibleSnippets() {
			if row.Key == focus.Key {
				m.snippets.cursor = index
				break
			}
		}
	}
	if m.snippetsActive() {
		m.setAt(m.at())
	}
	return m, nil
}

func (m Model) changeSnippetQuery(query SnippetQuery) (tea.Model, tea.Cmd) {
	m.cancelSnippetLoad()
	m.snippets.query = query
	m.snippets.result, m.snippets.hasSnapshot = SnippetResult{}, false
	m.snippets.cursor, m.snippets.filter = 0, ""
	m.err, m.status = nil, ""
	return m.loadSnippets()
}

func (m Model) runSnippetAction(action listAction) (tea.Model, tea.Cmd) {
	switch action {
	case listActionRemoteContent:
		m.snippets.enabled = !m.snippets.enabled
		m.err, m.status = nil, ""
		if !m.snippets.enabled {
			m.cancelSnippetLoad()
			return m.afterViewSwitch()
		}
		if !m.snippets.hasSnapshot || m.snippets.stale {
			return m.loadSnippets()
		}
		return m, nil
	case listActionSnippetProvider:
		menu := overlayState{kind: overlayActionMenu, title: "Snippet provider"}
		menu.addOption(listActionSnippetAll, "All configured providers")
		menu.addOption(listActionSnippetGitHub, "GitHub Gists")
		menu.addOption(listActionSnippetGitLab, "GitLab snippets")
		m.overlay = menu
		return m, nil
	case listActionSnippetAll, listActionSnippetGitHub, listActionSnippetGitLab:
		query := m.snippets.query
		query.Provider = map[listAction]string{listActionSnippetAll: "all", listActionSnippetGitHub: "github", listActionSnippetGitLab: "gitlab"}[action]
		if query.Provider != "gitlab" {
			query.Project = ""
		}
		return m.changeSnippetQuery(query)
	case listActionSnippetProject:
		return m.prompt(modeSnippetProject, m.snippets.query.Project, "GitLab project path; blank for personal snippets")
	case listActionSnippetSearch:
		return m.prompt(modeSnippetSearch, m.snippets.query.Text, "search file contents (explicit network request)")
	case listActionSnippetClearSearch:
		query := m.snippets.query
		query.Text, query.Content = "", false
		return m.changeSnippetQuery(query)
	case listActionSnippetCancel:
		m.cancelSnippetLoad()
		return m, nil
	case listActionSnippetCreate:
		process, err := m.actions.Snippets.Create(m.snippets.query)
		if err != nil {
			m.err = err
			return m, nil
		}
		if process == nil {
			m.err = fmt.Errorf("snippet wizard returned no process")
			return m, nil
		}
		m.cancelSnippetLoad()
		m.status = "Opening snippet creation wizard…"
		return m, runExecProcess(process, func(err error) tea.Msg { return snippetCreatedMsg{err: err} })
	case listActionOpen:
		row, ok := m.currentSnippet()
		if !ok {
			return m, nil
		}
		open, ctx := m.actions.Snippets.Open, m.baseContext()
		return m, func() tea.Msg { return copyMsg{status: "Opened snippet in browser", err: open(ctx, row)} }
	case listActionCopy:
		row, ok := m.currentSnippet()
		if !ok {
			return m, nil
		}
		copy := m.actions.Copy
		return m, func() tea.Msg { return copyMsg{status: "Copied snippet URL", err: copy(row.URL)} }
	}
	return m, nil
}

func (m Model) updateSnippetPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		query, value := m.snippets.query, strings.TrimSpace(m.input.Value())
		if m.mode == modeSnippetSearch && value == "" {
			m.err = fmt.Errorf("enter a content search query, or Esc to cancel")
			return m, nil
		}
		if m.mode == modeSnippetProject {
			query.Provider, query.Project = "gitlab", value
		} else {
			query.Text, query.Content = value, true
		}
		m.mode = modeList
		m.input.Blur()
		return m.changeSnippetQuery(query)
	}
	var command tea.Cmd
	m.input, command = m.input.Update(msg)
	return m, command
}

func (m Model) renderSnippets() string {
	provider := m.snippets.query.Provider
	if provider == "" {
		provider = "all"
	}
	scope := provider
	if m.snippets.query.Project != "" {
		scope += " · project " + m.snippets.query.Project
	}
	if m.snippets.query.Content {
		scope += " · content: " + m.snippets.query.Text
	}
	if m.snippets.hasSnapshot && !m.snippets.result.Complete {
		scope += " · partial results"
	} else if m.snippets.hasSnapshot && m.snippets.stale {
		scope += " · stale snapshot"
	}
	var b strings.Builder
	b.WriteString("  " + fitCell(styleDim.Render("Snippets · "+snippetDisplayText(scope)+" · Ctrl+O to show repositories"), max(1, m.width-4)) + "\n")
	rows := m.visibleSnippets()
	if len(rows) == 0 {
		message := "No snippets returned. Use Ctrl+O for provider/project scope or create a snippet."
		switch {
		case m.snippets.loading:
			message = m.snippets.status
		case !m.snippets.hasSnapshot:
			message = "Snippet inventory unavailable; refresh or choose another provider."
		case m.snippets.filter != "":
			message = "No snippet metadata matches /" + m.snippets.filter
		}
		b.WriteString("  " + fitCell(styleDim.Render(message), max(1, m.width-4)) + "\n")
		return b.String()
	}
	if m.snippets.loading {
		b.WriteString("  " + fitCell(styleDim.Render(m.snippets.status+" Showing the previous snapshot."), max(1, m.width-4)) + "\n")
	}
	nameW := max(8, m.width-24)
	if m.width >= 96 {
		nameW = m.width - 52
	} else if m.width >= 64 {
		nameW = m.width - 40
	}
	headers := []string{fitCell("FORGE", 7), fitCell("SNIPPET", nameW)}
	if m.width >= 64 {
		headers = append(headers, fitCell("OWNER", 14))
	}
	headers = append(headers, fitCell("VIS", 9))
	if m.width >= 96 {
		headers = append(headers, fitCell("UPDATED", 10))
	}
	b.WriteString(styleHeader.Render("  "+strings.Join(headers, "  ")) + "\n")
	from, to := m.window(len(rows))
	for index := from; index < to; index++ {
		row := rows[index]
		updated := "—"
		if !row.UpdatedAt.IsZero() {
			updated = row.UpdatedAt.Format("2006-01-02")
		}
		values := []string{fitCell(snippetDisplayText(row.Provider), 7), fitCell(snippetDisplayText(row.Title), nameW)}
		if m.width >= 64 {
			values = append(values, fitCell(dashCell(snippetDisplayText(row.Owner)), 14))
		}
		values = append(values, fitCell(dashCell(snippetDisplayText(row.Visibility)), 9))
		if m.width >= 96 {
			values = append(values, fitCell(updated, 10))
		}
		line := strings.Join(values, "  ")
		b.WriteString(m.renderLine(index, line, line))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func (m Model) renderSnippetDetail() string {
	if m.mode == modeSnippetProject || m.mode == modeSnippetSearch {
		label := "GitLab project "
		if m.mode == modeSnippetSearch {
			label = "Search contents "
		}
		return "  " + styleTitle.Render(label) + m.input.View() + "\n  " + styleHelp.Render("Enter requests network data · Esc cancels")
	}
	row, ok := m.currentSnippet()
	if !ok {
		return ""
	}
	lines := []string{"  url   " + fitCell(snippetDisplayText(row.URL), max(1, m.width-8)), "  files " + fitCell(snippetDisplayText(strings.Join(row.Files, ", ")), max(1, m.width-8))}
	if row.Description != "" {
		lines = append(lines, "  "+fitCell(row.Description, max(1, m.width-4)))
	}
	return strings.Join(lines, "\n")
}
