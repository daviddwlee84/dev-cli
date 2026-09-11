package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayHelp
	overlayRepoForm
	overlayActionMenu
	overlayTryForm
	overlayTryConfirm
	overlayTriageReceipt
)

type formField struct {
	key   string
	label string
	input textinput.Model
}

type actionOption struct {
	tool   string
	column string
	action listAction
	label  string
}

// overlayState uses fixed arrays so copying Model also copies the mutable form
// and menu state. A slice or pointer would violate Bubble Tea's value semantics.
type overlayState struct {
	registration uint64
	scroll       int
	body         string
	kind         overlayKind
	title        string
	subject      string
	detail       string
	selection    selectionToken
	target       TryRow
	repoTarget   RepoRow
	action       TryAction

	searching   bool
	search      textinput.Model
	options     [48]actionOption
	optionCount int
	optionIndex int

	fields     [4]formField
	fieldCount int
	fieldIndex int
}

func (m Model) openHelpOverlay() Model {
	m.overlay = overlayState{kind: overlayHelp, title: "input help"}
	m.err = nil
	return m
}

func (o *overlayState) addOption(action listAction, label string) {
	if o.optionCount >= len(o.options) {
		return
	}
	o.options[o.optionCount] = actionOption{action: action, label: label}
	o.optionCount++
}

func (m Model) openRepoForm(row RepoRow) (Model, tea.Cmd) {
	overlay := overlayState{kind: overlayRepoForm, title: "mark " + row.Repo.Display(), repoTarget: row}
	tags, note := "", ""
	if row.Asset != nil {
		tags = strings.Join(row.Asset.Tags, ", ")
		note = row.Asset.Note
	}
	overlay.addField("tags", "tags", "comma or space separated", tags)
	overlay.addField("note", "note", "optional note", note)
	m.overlay = overlay
	m.err = nil
	return m.focusOverlayField(0)
}

func (m Model) openTryForm(action TryAction, row TryRow) (Model, tea.Cmd) {
	overlay := overlayState{kind: overlayTryForm, action: action, target: row}
	switch action {
	case TryCreate:
		overlay.title = "new Try"
		overlay.addField("name", "name", "experiment name", "")
		overlay.addField("clone", "clone", "optional repository URL/ref", "")
		overlay.addField("git", "git init", "yes or no", "yes")
	case TryMark:
		overlay.title = "mark " + row.Item.DisplayName()
		overlay.addField("tags", "tags", "comma or space separated", strings.Join(row.Item.Tags, ", "))
		overlay.addField("note", "note", "optional note", row.Item.Note)
	case TryRestore:
		overlay.title = "restore " + row.Item.DisplayName()
		overlay.addField("to", "to", "optional basename/path under tries_root", "")
	case TryGraduate:
		overlay.title = "graduate " + row.Item.DisplayName()
		overlay.addField("category", "category", "optional project category", "")
		overlay.addField("name", "name", "optional project name", "")
	default:
		m.err = fmt.Errorf("Try action %q has no form", action)
		return m, nil
	}
	m.overlay = overlay
	m.err = nil
	return m.focusOverlayField(0)
}

func (m Model) openTryConfirmation(action TryAction, row TryRow) (Model, tea.Cmd) {
	overlay := overlayState{
		kind: overlayTryConfirm, title: "confirm " + string(action),
		target: row, action: action,
	}
	overlay.addField("confirm", "confirm", "type YES exactly", "")
	m.overlay = overlay
	m.err = nil
	return m.focusOverlayField(0)
}

func (o *overlayState) addField(key, label, placeholder, value string) {
	if o.fieldCount >= len(o.fields) {
		return
	}
	input := textinput.New()
	input.CharLimit = 300
	input.Placeholder = placeholder
	input.SetValue(value)
	input.CursorEnd()
	o.fields[o.fieldCount] = formField{key: key, label: label, input: input}
	o.fieldCount++
}

func (m Model) focusOverlayField(index int) (Model, tea.Cmd) {
	if m.overlay.fieldCount == 0 {
		return m, nil
	}
	if index < 0 {
		index = m.overlay.fieldCount - 1
	}
	if index >= m.overlay.fieldCount {
		index = 0
	}
	for fieldIndex := 0; fieldIndex < m.overlay.fieldCount; fieldIndex++ {
		m.overlay.fields[fieldIndex].input.Blur()
	}
	m.overlay.fieldIndex = index
	return m, m.overlay.fields[index].input.Focus()
}

func (m Model) visibleActions() []int {
	var out []int
	for i := 0; i < m.overlay.optionCount; i++ {
		option := m.overlay.options[i]
		haystack := strings.ToLower(option.label)
		if option.action == listActionStats {
			haystack += " stats heatmap activity"
		}
		match := true
		for _, term := range strings.Fields(strings.ToLower(m.overlay.search.Value())) {
			if !strings.Contains(haystack, term) {
				match = false
				break
			}
		}
		if match {
			out = append(out, i)
		}
	}
	return out
}

func (m *Model) moveActionMenu(delta int) {
	visible := m.visibleActions()
	if len(visible) == 0 {
		return
	}
	pos := 0
	for i, index := range visible {
		if index == m.overlay.optionIndex {
			pos = i
			break
		}
	}
	m.overlay.optionIndex = visible[(pos+delta+len(visible))%len(visible)]
}

func (m Model) actionMenuWindow() ([]int, int, int) {
	visible := m.visibleActions()
	rows := max(1, m.height-m.buildActionMenuLayout().firstOptionY-3)
	position := 0
	for i, index := range visible {
		if index == m.overlay.optionIndex {
			position = i
			break
		}
	}
	from := max(0, position-rows+1)
	return visible, from, min(len(visible), from+rows)
}

func (m Model) updateOverlay(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay.kind {
	case overlayTriageReceipt:
		if message.String() == "esc" || message.String() == "enter" || message.String() == "q" {
			m.overlay = overlayState{}
		}
		if message.String() == "j" || message.String() == "down" {
			m.overlay.scroll++
		}
		if message.String() == "k" || message.String() == "up" {
			m.overlay.scroll = max(0, m.overlay.scroll-1)
		}
		return m, nil
	case overlayHelp:
		switch message.String() {
		case "esc", "?", "q":
			m.overlay = overlayState{}
		}
		return m, nil

	case overlayActionMenu:
		if !m.overlay.searching && message.Type == tea.KeyRunes && strings.HasPrefix(message.String(), "/") {
			m.overlay.search = textinput.New()
			m.overlay.search.Prompt = "/ "
			m.overlay.search.CharLimit = 200
			m.overlay.search.Width = max(10, m.width-5)
			m.overlay.search.SetValue(strings.TrimPrefix(message.String(), "/"))
			m.overlay.searching = true
			if visible := m.visibleActions(); len(visible) > 0 {
				m.overlay.optionIndex = visible[0]
			}
			return m, m.overlay.search.Focus()
		}

		if m.overlay.searching {
			switch message.String() {
			case "esc":
				m.overlay.searching = false
				m.overlay.search.SetValue("")
				m.overlay.search.Blur()
				return m, nil
			case "up", "down", "enter":
			default:
				var cmd tea.Cmd
				m.overlay.search, cmd = m.overlay.search.Update(message)
				visible := m.visibleActions()
				found := false
				for _, i := range visible {
					if i == m.overlay.optionIndex {
						found = true
					}
				}
				if !found && len(visible) > 0 {
					m.overlay.optionIndex = visible[0]
				}
				return m, cmd
			}
		}
		switch message.String() {
		case "/":
			m.overlay.search = textinput.New()
			m.overlay.search.Prompt = "/ "
			m.overlay.search.CharLimit = 200
			m.overlay.search.Width = max(10, m.width-5)
			m.overlay.searching = true
			return m, m.overlay.search.Focus()
		case "esc", "q":
			m.overlay = overlayState{}
		case "pgdown", "ctrl+d":
			m.scrollActionBody(max(1, m.height/2))
		case "pgup", "ctrl+u":
			m.scrollActionBody(-max(1, m.height/2))
		case "j", "down":
			m.moveActionMenu(1)
		case "k", "up":
			m.moveActionMenu(-1)
		case "enter":
			if len(m.visibleActions()) > 0 {
				return m.runOverlayAction()
			}
		}
		return m, nil

	case overlayRepoForm, overlayTryForm, overlayTryConfirm:
		switch message.String() {
		case "esc":
			m.overlay = overlayState{}
			m.err = nil
			return m, nil
		case "tab", "down":
			return m.focusOverlayField(m.overlay.fieldIndex + 1)
		case "shift+tab", "up":
			return m.focusOverlayField(m.overlay.fieldIndex - 1)
		case "enter":
			return m.submitTryOverlay()
		}
		index := m.overlay.fieldIndex
		input, command := m.overlay.fields[index].input.Update(message)
		m.overlay.fields[index].input = input
		return m, command
	}
	return m, nil
}

func (m Model) submitTryOverlay() (tea.Model, tea.Cmd) {
	value := func(key string) string {
		for index := 0; index < m.overlay.fieldCount; index++ {
			if m.overlay.fields[index].key == key {
				return strings.TrimSpace(m.overlay.fields[index].input.Value())
			}
		}
		return ""
	}
	if m.overlay.kind == overlayRepoForm {
		row := m.overlay.repoTarget
		tags := splitTagInput(value("tags"))
		note := value("note")
		m.overlay = overlayState{}
		m.err = nil
		m.status = "updating repository metadata…"
		return m, m.applyRepoPatch(row, tags, note)
	}

	request := TryRequest{Action: m.overlay.action, ID: m.overlay.target.reference()}
	switch m.overlay.action {
	case TryCreate:
		request.Name, request.Clone = value("name"), value("clone")
		if request.Name == "" && request.Clone == "" {
			m.err = fmt.Errorf("a new Try needs a name or clone reference")
			return m, nil
		}
		gitInit, ok := parseBoolInput(value("git"), true)
		if !ok {
			m.err = fmt.Errorf("git init must be yes or no")
			return m, nil
		}
		request.NoGit = !gitInit
	case TryMark:
		request.Tags = splitTagInput(value("tags"))
		request.Note = value("note")
	case TryRestore:
		request.To = value("to")
	case TryGraduate:
		request.Category, request.Name = value("category"), value("name")
	case TryArchive:
		if value("confirm") != "YES" {
			m.err = fmt.Errorf("confirmation must be exactly YES")
			return m, nil
		}
	default:
		m.err = fmt.Errorf("unsupported Try form action %q", m.overlay.action)
		return m, nil
	}
	m.overlay = overlayState{}
	m.err = nil
	m.status = string(request.Action) + " in progress…"
	return m, m.applyTry(request)
}

type actionMenuLayout struct {
	heading      string
	firstOptionY int
}

func (m Model) buildActionMenuLayout() actionMenuLayout {
	var builder strings.Builder
	builder.WriteString("  " + fitCell(m.overlay.subject, max(1, m.width-2)) + "\n")
	first := 4 // title, blank, subject, blank
	if m.overlay.detail != "" {
		builder.WriteString("  " + fitCell(m.overlay.detail, max(1, m.width-2)) + "\n")
		first++
	}
	if m.overlay.body != "" {
		lines, start, end := m.actionBodyWindow()
		for _, line := range lines[start:end] {
			builder.WriteString("  " + line + "\n")
			first++
		}
		if len(lines) > end || start > 0 {
			builder.WriteString("  " + fitCell(fmt.Sprintf("Details %d–%d/%d · wheel here / PgUp/PgDn", start+1, end, len(lines)), max(1, m.width-2)) + "\n")
			first++
		}
	}
	builder.WriteString("\n")
	if m.overlay.searching {
		builder.WriteString("  " + m.overlay.search.View() + "\n")
		first++
	}
	return actionMenuLayout{heading: builder.String(), firstOptionY: first}
}

func (m Model) actionBodyWindow() ([]string, int, int) {
	lines := strings.Split(ansi.Hardwrap(m.overlay.body, max(1, m.width-2), true), "\n")
	capacity := max(1, m.height-10)
	start := min(max(0, m.overlay.scroll), max(0, len(lines)-capacity))
	return lines, start, min(len(lines), start+capacity)
}

func (m *Model) scrollActionBody(delta int) {
	if m.overlay.body == "" {
		return
	}
	lines, start, end := m.actionBodyWindow()
	m.overlay.scroll = min(max(0, start+delta), max(0, len(lines)-(end-start)))
}

func (m Model) actionMenuOptionAt(x, y int) (int, bool) {
	if x < 0 || x >= m.width {
		return 0, false
	}
	layout := m.buildActionMenuLayout()
	visible, from, to := m.actionMenuWindow()
	position := y - layout.firstOptionY + from
	if position < from || position >= to {
		return 0, false
	}
	index := visible[position]
	if x < 0 || x >= 2+lipgloss.Width(m.overlay.options[index].label) {
		return 0, false
	}
	return index, true
}

func (m Model) renderOverlay() string {
	var builder strings.Builder
	builder.WriteString(styleTitle.Render("dev  "+strings.ToUpper(m.overlay.title)) + "\n\n")
	switch m.overlay.kind {
	case overlayTriageReceipt:
		lines := strings.Split(ansi.Hardwrap(m.overlay.body, max(1, m.width-2), true), "\n")
		start := min(m.overlay.scroll, max(0, len(lines)-1))
		for _, line := range lines[start:min(len(lines), start+max(1, m.height-6))] {
			builder.WriteString(fitCell(line, max(1, m.width-2)) + "\n")
		}
		builder.WriteString("\n↑/↓ scroll · Esc / Enter return")
	case overlayHelp:
		builder.WriteString("  navigation\n")
		builder.WriteString("    j/k, arrows move · ctrl+d/u page · g/G first/last · tab/h/l switch view\n")
		builder.WriteString("    click row/tab selects · click selected row / right click / ctrl+o actions · wheel 3 rows\n")
		builder.WriteString("    / filter · 0 clear · r reload · esc close/clear/quit · q quit\n\n")
		builder.WriteString("  1–7 switch views · Ctrl+O actions · / filter actions · Enter opens the current item\n")
		builder.WriteString("  TASKS   enter open · n add note · N notes · p park · c next · ctrl+o state filter · a show done · space actions\n")
		builder.WriteString("  REPOS   enter open · n new repo · a add note · N notes · space worktrees · m metadata · y copy · s worktree task · d direct task · O/R sort\n")
		builder.WriteString("  FLEET   enter Herdr/SSH open · e edit remotes.toml · r refresh · read-only Git overview\n")
		builder.WriteString("  TRY     enter open · n create · space actions · a history · O/R sort\n")
		builder.WriteString("  REMOTE  enter open local · n/N notes when cloned · c clone (enter stay / o open) · y u copy clone URL\n")
		builder.WriteString("  SKILLS  a add · c check · u update · e open file · y copy · A context/all scope\n")
		builder.WriteString("  MCP     static declarations only · e open config · y copy · A context/all scope · r reload\n\n")
		builder.WriteString("  " + styleHelp.Render("? / esc / q close help"))

	case overlayActionMenu:
		layout := m.buildActionMenuLayout()
		builder.WriteString(layout.heading)
		visible, from, to := m.actionMenuWindow()
		for _, index := range visible[from:to] {
			prefix := "  "
			line := m.overlay.options[index].label
			if index == m.overlay.optionIndex {
				prefix = "▸ "
				line = styleSelected.Render(line)
			}
			builder.WriteString(prefix + fitCell(line, max(1, m.width-2)) + "\n")
		}
		if len(visible) == 0 {
			builder.WriteString("  No matching actions\n")
		}
		builder.WriteString("\n  " + fitCell(fmt.Sprintf("%d/%d · ", len(visible), m.overlay.optionCount)+styleHelp.Render("↑/↓ choose · / filter · Enter · Esc back"), max(1, m.width-2)))

	case overlayRepoForm, overlayTryForm, overlayTryConfirm:
		if m.overlay.target.Item.ID != "" {
			builder.WriteString(fmt.Sprintf("  %s\n  %s\n\n", m.overlay.target.Item.DisplayName(), contract(m.overlay.target.Item.Live.CurrentPath)))
		} else if m.overlay.repoTarget.Repo.Path != "" {
			builder.WriteString(fmt.Sprintf("  %s\n  %s\n\n", m.overlay.repoTarget.Repo.Display(), contract(m.overlay.repoTarget.Repo.Path)))
		}
		for index := 0; index < m.overlay.fieldCount; index++ {
			marker := "  "
			if index == m.overlay.fieldIndex {
				marker = "▸ "
			}
			builder.WriteString(fmt.Sprintf("%s%-10s %s\n", marker, m.overlay.fields[index].label+":", m.overlay.fields[index].input.View()))
		}
		if m.err != nil {
			builder.WriteString("\n  " + styleErr.Render("✗ "+m.err.Error()))
		}
		builder.WriteString("\n\n  " + styleHelp.Render("tab field · enter submit · esc cancel"))
	}
	return builder.String()
}
