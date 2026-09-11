package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	helpdocs "github.com/daviddwlee84/dev-cli/internal/help"
)

type helpSection uint8

const (
	helpKeys helpSection = iota
	helpGuide
	helpManual
)

type helpScreen uint8

const (
	helpHome helpScreen = iota
	helpEntryScreen
	helpTopicScreen
	helpScopeScreen
)

type helpLocation struct {
	screen                 helpScreen
	query, topic           string
	scroll, index, xOffset int
	entry                  helpEntry
}
type helpBrowser struct {
	hasSelection bool
	origin, view View
	all          bool
	section      helpSection
	pages        [3]helpLocation
	history      [3][8]helpLocation
	depth        [3]int
	input        textinput.Model
	editing      bool
	snapshot     string
	topics       []helpdocs.Topic // immutable embedded snapshot
	err          error
}
type helpTarget struct {
	kind       helpScreen
	entry      helpEntry
	topic      string
	sourceLine int
	scope      int
	valid      bool
}
type helpRow struct {
	text       string
	target     helpTarget
	sourceLine int
}
type helpFrame struct {
	header                                    []string
	rows                                      []helpRow
	contentY, visible, total, searchY, scopeY int
	footer                                    string
}

func (m Model) helpLocation() helpLocation        { return m.help.pages[m.help.section] }
func (m *Model) setHelpLocation(loc helpLocation) { m.help.pages[m.help.section] = loc }
func (m *Model) setHelpSection(section helpSection) {
	m.help.section = section
	m.help.editing = false
	if loc := m.helpLocation(); loc.screen == helpHome && loc.index == 0 {
		m.selectFirstHelpTarget()
	}
}
func (m *Model) pushHelpLocation(loc helpLocation) {
	s := m.help.section
	d := m.help.depth[s]
	if d < 8 {
		m.help.history[s][d] = m.helpLocation()
		m.help.depth[s]++
	}
	m.setHelpLocation(loc)
	m.help.editing = false
	if loc.screen == helpScopeScreen {
		m.selectFirstHelpTarget()
	}
}
func (m *Model) helpBack() {
	s := m.help.section
	if m.help.depth[s] > 0 {
		m.help.depth[s]--
		m.setHelpLocation(m.help.history[s][m.help.depth[s]])
	} else {
		m.overlay = overlayState{}
	}
	m.help.editing = false
}

func helpMatches(query, text string) bool {
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(strings.ToLower(text), term) {
			return false
		}
	}
	return true
}

func helpEntrySearchText(e helpEntry) string {
	text := e.Key + " " + e.Title + " " + e.Description + " " + e.Group
	for _, alias := range []struct{ key, value string }{{"↑", "up"}, {"↓", "down"}, {"←", "left"}, {"→", "right"}, {"Ctrl+N/P", "ctrl+n ctrl+p"}, {"Ctrl+D/U", "ctrl+d ctrl+u"}, {"PgDn/PgUp", "pgdown pgup page down page up"}} {
		if strings.Contains(e.Key, alias.key) {
			text += " " + alias.value
		}
	}
	return text
}
func helpWrapped(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

func (m Model) helpEntries(guide bool) []helpEntry {
	views := []View{m.help.view}
	if m.help.all {
		views = Views
	}
	var result []helpEntry
	seen := map[string]bool{}
	for _, view := range views {
		entries := m.helpKeyEntries(view)
		if guide {
			entries = helpGuideEntries(view)
			rank := func(group string) int {
				switch group {
				case "Using this view":
					return 0
				case "Columns":
					return 1
				case "Colors and symbols":
					return 2
				default:
					return 3
				}
			}
			sort.SliceStable(entries, func(i, j int) bool { return rank(entries[i].Group) < rank(entries[j].Group) })
		}
		for _, e := range entries {
			key := e.ID
			if e.Group == "Navigation" {
				key = e.Key + e.Title
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, e)
		}
	}
	return result
}

func (m Model) helpRows(width int) []helpRow {
	loc := m.helpLocation()
	var rows []helpRow
	appendText := func(text string) {
		for _, line := range helpWrapped(text, width) {
			rows = append(rows, helpRow{text: line})
		}
	}
	appendTarget := func(text string, target helpTarget) {
		for _, line := range helpWrapped(text, max(1, width-2)) {
			rows = append(rows, helpRow{text: "  " + line, target: target})
		}
	}
	if loc.screen == helpScopeScreen {
		appendText("Choose a help scope; the dashboard stays on its current view.")
		if helpMatches(loc.query, "All views") {
			appendTarget("All views", helpTarget{valid: true, kind: helpScopeScreen, scope: -1})
		}
		for _, v := range Views {
			if helpMatches(loc.query, v.String()) {
				appendTarget(strings.ToUpper(v.String()), helpTarget{valid: true, kind: helpScopeScreen, scope: int(v)})
			}
		}
		return rows
	}
	if loc.screen == helpEntryScreen {
		appendText(styleTitle.Render(loc.entry.Title))
		if loc.entry.Key != "" {
			appendText(styleSelected.Render(loc.entry.Key))
		}
		appendText("")
		appendText(loc.entry.Description)
		if loc.entry.Topic != "" {
			appendText("")
			appendTarget("Read more: dev help "+loc.entry.Topic, helpTarget{valid: true, kind: helpTopicScreen, topic: loc.entry.Topic})
		}
		return rows
	}
	if loc.screen == helpTopicScreen {
		body := m.helpArticleBody()
		inCode := false
		for n, line := range strings.Split(body, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "```") {
				inCode = !inCode
				rows = append(rows, helpRow{text: styleDim.Render(line), sourceLine: n})
				continue
			}
			if inCode || strings.HasPrefix(trim, "|") {
				rows = append(rows, helpRow{text: line, sourceLine: n})
				continue
			}
			if strings.HasPrefix(trim, "#") {
				line = styleTitle.Render(strings.TrimSpace(strings.TrimLeft(trim, "#")))
			}
			for _, wrapped := range helpWrapped(line, width) {
				rows = append(rows, helpRow{text: wrapped, sourceLine: n})
			}
		}
		return rows
	}
	if m.help.section == helpManual {
		if m.help.err != nil {
			appendText("Manual unavailable: " + m.help.err.Error())
			return rows
		}
		if loc.query == "" || helpMatches(loc.query, "workflow TL;DR "+helpdocs.WorkflowTLDR) {
			appendTarget("Workflow TL;DR  ·  dev help", helpTarget{valid: true, kind: helpTopicScreen, topic: "@workflow"})
			appendText("")
		}
		hits := helpdocs.SearchTopics(m.help.topics, loc.query)
		if loc.query == "" && !m.help.all {
			related := helpRelatedTopics(m.help.view)
			rank := func(name string) int {
				for i, n := range related {
					if n == name {
						return i
					}
				}
				return len(related)
			}
			sort.SliceStable(hits, func(i, j int) bool { return rank(hits[i].Topic.Name) < rank(hits[j].Topic.Name) })
		}
		for _, hit := range hits {
			t := helpTarget{valid: true, kind: helpTopicScreen, topic: hit.Topic.Name, sourceLine: hit.Line}
			appendTarget(styleTitle.Render(hit.Topic.Name)+" · "+hit.Topic.Title, t)
			if hit.Heading != "" {
				appendText("    " + hit.Heading)
			}
			appendText("    " + hit.Snippet)
			appendText("")
		}
		if len(rows) == 0 {
			appendText("No matching manual topics.")
		}
		return rows
	}
	if loc.query == "" {
		if !m.help.all {
			tldr := helpWrapped("TL;DR  "+helpTLDR(m.help.view), width)
			if len(tldr) > 2 {
				tldr = tldr[:2]
				tldr[1] = ansi.Truncate(tldr[1], max(1, width-1), "") + "…"
			}
			for _, line := range tldr {
				rows = append(rows, helpRow{text: line})
			}
			appendText("")
		}
		if m.help.section == helpGuide && !m.help.all && m.help.view == m.help.origin && m.help.hasSelection {
			appendText(styleTitle.Render("Selected row · captured when Help opened"))
			lines := strings.Split(m.help.snapshot, "\n")
			for _, line := range lines[:min(6, len(lines))] {
				appendText(line)
			}
			appendTarget("Selected row details…", helpTarget{valid: true, kind: helpEntryScreen, entry: helpEntry{Title: "Selected row at Help opening", Description: m.help.snapshot}})
			appendText("")
		}
	}
	entries := m.helpEntries(m.help.section == helpGuide)
	if loc.query != "" {
		entries = append(m.helpEntries(false), m.helpEntries(true)...)
	}
	lastGroup := ""
	count := 0
	for _, e := range entries {
		if !helpMatches(loc.query, helpEntrySearchText(e)) {
			continue
		}
		group := e.Group
		if m.help.all {
			group = strings.ToUpper(e.View.String()) + " · " + group
		}
		if loc.query != "" {
			if e.Guide {
				group = "Guide · " + group
			} else {
				group = "Keys · " + group
			}
		}
		if group != lastGroup {
			if count > 0 {
				appendText("")
			}
			appendText(styleTitle.Render(group))
			lastGroup = group
		}
		target := helpTarget{valid: true, kind: helpEntryScreen, entry: e}
		if !e.Guide && e.Key != "" && width >= 56 {
			keyWidth := min(30, width/3)
			keys := helpWrapped(e.Key, keyWidth)
			titles := helpWrapped(e.Title, width-keyWidth-4)
			for i := 0; i < max(len(keys), len(titles)); i++ {
				key, title := "", ""
				if i < len(keys) {
					key = keys[i]
				}
				if i < len(titles) {
					title = titles[i]
				}
				rows = append(rows, helpRow{text: "  " + styleSelected.Render(popupFit(key, keyWidth)) + "  " + title, target: target})
			}
		} else {
			label := helpLegendTitle(e.Title)
			if e.Key != "" {
				label = styleSelected.Render(e.Key) + "  " + label
			}
			appendTarget(label, target)
		}
		if e.Guide {
			appendText("    " + e.Description)
		}
		count++
	}
	if count == 0 {
		appendText("No matching TUI help. Switch to Manual for broader workflows.")
	}
	return rows
}

func (m Model) helpArticleBody() string {
	loc := m.helpLocation()
	if loc.topic == "@workflow" {
		return "```text\n" + helpdocs.WorkflowTLDR + "\n```"
	}
	for _, topic := range m.help.topics {
		if topic.Name == loc.topic {
			return topic.Body
		}
	}
	return "Manual topic unavailable."
}

func helpLegendTitle(title string) string {
	for _, tone := range []struct {
		name  string
		style lipgloss.Style
	}{{"Cyan/blue", styleSelected}, {"Orange", styleDirty}, {"Coral", styleDrift}, {"Green", styleLive}, {"gray", styleClean}, {"Gray", styleClean}} {
		title = strings.ReplaceAll(title, tone.name, tone.style.Render(tone.name))
	}
	return title
}

func (m Model) helpLayout() helpFrame {
	w, h := m.popupContentSize()
	loc := m.helpLocation()
	scope := strings.ToUpper(m.help.view.String())
	if m.help.all {
		scope = "All views"
	}
	tabs := []string{"[1 Keys]", "[2 Guide]", "[3 Manual]"}
	tabs[m.help.section] = styleSelected.Render(tabs[m.help.section])
	f := helpFrame{header: []string{strings.Join(tabs, "  "), "View: [" + scope + " ▾]  · v"}, scopeY: 1, searchY: 2}
	label := "/ Search TUI help…"
	if m.help.section == helpManual {
		label = "/ Search manual…"
	}
	if loc.screen == helpTopicScreen {
		label = "/ Find in article…"
	}
	if m.help.editing {
		input := m.help.input
		input.Width = max(1, w-3)
		label = input.View()
	} else if loc.query != "" {
		label = "/ " + loc.query
	}
	f.header = append(f.header, label, "")
	if loc.screen != helpHome {
		f.header[1] = "[Back]  " + scope
		if loc.screen == helpTopicScreen {
			f.header[1] = "[Back]  CLI: dev help"
			if loc.topic != "@workflow" {
				f.header[1] += " " + loc.topic
			}
		}
	}
	f.contentY = len(f.header)
	f.visible = max(1, h-f.contentY-1)
	f.rows = m.helpRows(w)
	f.total = len(f.rows)
	f.footer = "j/k scroll · / search · v view · f expand · Esc back"
	if loc.screen == helpHome {
		f.footer = "j/k choose · Enter details · / search · Esc close"
	}
	if loc.screen == helpTopicScreen {
		f.footer = "[←] [→] · j/k scroll · n/N match · Esc back"
	}
	if m.help.editing {
		f.footer = "Enter search · Esc stop editing; Esc again clears"
	}
	return f
}

func helpHighlight(text, query string) string {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return text
	}
	for i := range terms {
		terms[i] = regexp.QuoteMeta(terms[i])
	}
	re, err := regexp.Compile("(?i)" + strings.Join(terms, "|"))
	if err != nil {
		return text
	}
	return re.ReplaceAllStringFunc(ansi.Strip(text), func(s string) string { return styleSelected.Render(s) })
}

func (m Model) renderHelpPopup() string {
	f := m.helpLayout()
	loc := m.helpLocation()
	w, h := m.popupContentSize()
	start := min(max(0, loc.scroll), max(0, f.total-f.visible))
	lines := append([]string(nil), f.header...)
	for i := 0; i < f.visible; i++ {
		line := ""
		n := start + i
		if n < len(f.rows) {
			line = helpHighlight(f.rows[n].text, loc.query)
			if n == loc.index && f.rows[n].target.valid {
				line = styleSelected.Render("▸ ") + strings.TrimPrefix(line, "  ")
			}
			line = ansi.Cut(line, loc.xOffset, loc.xOffset+w)
		}
		lines = append(lines, line)
	}
	if len(lines) >= h {
		lines = lines[:max(0, h-1)]
	}
	lines = append(lines, helpFooterLine(f, loc))
	title := "Help · " + strings.ToUpper(m.help.view.String())
	if m.help.all {
		title = "Help · All views"
	}
	return m.renderPopup(title, strings.Join(lines, "\n"), start, f.total, f.visible, f.contentY)
}

func helpFooterLine(f helpFrame, loc helpLocation) string {
	start := min(max(0, loc.scroll), max(0, f.total-f.visible))
	return fmt.Sprintf("%d–%d/%d · %s", min(start+1, f.total), min(start+f.visible, f.total), f.total, f.footer)
}

func (m *Model) selectFirstHelpTarget() {
	loc := m.helpLocation()
	for i, row := range m.helpLayout().rows {
		if row.target.valid {
			loc.index = i
			m.setHelpLocation(loc)
			return
		}
	}
}

func (m *Model) helpScroll(delta int) {
	f := m.helpLayout()
	loc := m.helpLocation()
	loc.scroll = min(max(0, loc.scroll+delta), max(0, f.total-f.visible))
	if loc.index < loc.scroll || loc.index >= loc.scroll+f.visible {
		for i := loc.scroll; i < min(f.total, loc.scroll+f.visible); i++ {
			if f.rows[i].target.valid {
				loc.index = i
				break
			}
		}
	}
	m.setHelpLocation(loc)
}
func (m *Model) helpMove(delta int) {
	f := m.helpLayout()
	loc := m.helpLocation()
	n := loc.index + delta
	var current helpTarget
	if loc.index >= 0 && loc.index < len(f.rows) {
		current = f.rows[loc.index].target
	}
	for n >= 0 && n < len(f.rows) {
		if f.rows[n].target.valid && f.rows[n].target != current {
			loc.index = n
			if n < loc.scroll {
				loc.scroll = n
			}
			if n >= loc.scroll+f.visible {
				loc.scroll = n - f.visible + 1
			}
			m.setHelpLocation(loc)
			return
		}
		n += delta
	}
	m.helpScroll(delta)
}

func (m Model) helpActivate(target helpTarget) (tea.Model, tea.Cmd) {
	if !target.valid {
		return m, nil
	}
	if target.kind == helpScopeScreen {
		m.helpBack()
		m.help.all = target.scope < 0
		if target.scope >= 0 {
			m.help.view = View(target.scope)
		}
		for i := range m.help.pages {
			m.help.pages[i] = helpLocation{}
		}
		m.help.depth = [3]int{}
		m.setHelpSection(m.help.section)
		return m, nil
	}
	loc := helpLocation{screen: target.kind, entry: target.entry, topic: target.topic}
	if target.kind == helpTopicScreen && m.help.section == helpManual && m.helpLocation().screen == helpHome {
		loc.query = m.helpLocation().query
	}
	m.pushHelpLocation(loc)
	if target.kind == helpTopicScreen && target.sourceLine > 0 {
		f := m.helpLayout()
		for i, row := range f.rows {
			if row.sourceLine >= target.sourceLine {
				loc.scroll = i
				break
			}
		}
		m.setHelpLocation(loc)
	}
	return m, nil
}

func (m *Model) helpFindNext(delta int) {
	loc := m.helpLocation()
	if loc.query == "" {
		return
	}
	f := m.helpLayout()
	var matches []int
	first := map[int]int{}
	for i, row := range f.rows {
		if _, ok := first[row.sourceLine]; !ok {
			first[row.sourceLine] = i
		}
	}
	for n, line := range strings.Split(m.helpArticleBody(), "\n") {
		if helpMatches(loc.query, line) {
			if i, ok := first[n]; ok {
				matches = append(matches, i)
			}
		}
	}
	if len(matches) == 0 {
		return
	}
	chosen := matches[0]
	if delta > 0 {
		for _, i := range matches {
			if i > loc.scroll {
				chosen = i
				break
			}
		}
	} else {
		chosen = matches[len(matches)-1]
		for i := len(matches) - 1; i >= 0; i-- {
			if matches[i] < loc.scroll {
				chosen = matches[i]
				break
			}
		}
	}
	loc.scroll = chosen
	m.setHelpLocation(loc)
}

func (m Model) focusHelpSearch() (tea.Model, tea.Cmd) {
	loc := m.helpLocation()
	m.help.input = textinput.New()
	m.help.input.Prompt = "/ "
	m.help.input.CharLimit = 200
	m.help.input.SetValue(loc.query)
	m.help.editing = true
	return m, m.help.input.Focus()
}

func (m Model) updateHelp(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	loc := m.helpLocation()
	key := message.String()
	if m.help.editing {
		switch key {
		case "esc":
			m.help.editing = false
			m.help.input.Blur()
			return m, nil
		case "enter":
			m.help.editing = false
			m.help.input.Blur()
			if loc.screen == helpTopicScreen {
				loc.scroll = -1
				m.setHelpLocation(loc)
				m.helpFindNext(1)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.help.input, cmd = m.help.input.Update(message)
		loc.query = m.help.input.Value()
		loc.scroll, loc.index, loc.xOffset = 0, 0, 0
		m.setHelpLocation(loc)
		if loc.screen != helpTopicScreen {
			m.selectFirstHelpTarget()
		}
		return m, cmd
	}
	switch key {
	case "?", "q":
		m.overlay = overlayState{}
	case "esc":
		if loc.query != "" {
			loc.query = ""
			loc.scroll, loc.index = 0, 0
			m.setHelpLocation(loc)
		} else {
			m.helpBack()
		}
	case "1", "2", "3":
		m.setHelpSection(helpSection(key[0] - '1'))
	case "tab":
		m.setHelpSection((m.help.section + 1) % 3)
	case "shift+tab":
		m.setHelpSection((m.help.section + 2) % 3)
	case "v":
		if loc.screen == helpScopeScreen {
			m.helpBack()
		} else {
			m.pushHelpLocation(helpLocation{screen: helpScopeScreen})
		}
	case "f":
		m.popupExpanded = !m.popupExpanded
	case "/":
		return m.focusHelpSearch()
	case "down", "j":
		if loc.screen == helpTopicScreen || loc.screen == helpEntryScreen {
			m.helpScroll(1)
		} else {
			m.helpMove(1)
		}
	case "up", "k":
		if loc.screen == helpTopicScreen || loc.screen == helpEntryScreen {
			m.helpScroll(-1)
		} else {
			m.helpMove(-1)
		}
	case "pgdown", "ctrl+d":
		m.helpScroll(max(1, m.helpLayout().visible/2))
	case "pgup", "ctrl+u":
		m.helpScroll(-max(1, m.helpLayout().visible/2))
	case "home", "g":
		loc.scroll, loc.index = 0, 0
		m.setHelpLocation(loc)
	case "end", "G":
		f := m.helpLayout()
		loc.scroll = max(0, f.total-f.visible)
		loc.index = max(0, f.total-1)
		m.setHelpLocation(loc)
	case "left", "h":
		loc.xOffset = max(0, loc.xOffset-8)
		m.setHelpLocation(loc)
	case "right", "l":
		w, _ := m.popupContentSize()
		widest := 0
		for _, r := range m.helpLayout().rows {
			widest = max(widest, lipgloss.Width(r.text))
		}
		loc.xOffset = min(max(0, widest-w), loc.xOffset+8)
		m.setHelpLocation(loc)
	case "n":
		if loc.screen == helpTopicScreen {
			m.helpFindNext(1)
		}
	case "N":
		if loc.screen == helpTopicScreen {
			m.helpFindNext(-1)
		}
	case "enter":
		f := m.helpLayout()
		if loc.index < len(f.rows) {
			return m.helpActivate(f.rows[loc.index].target)
		}
	default:
		if message.Type == tea.KeyRunes && strings.HasPrefix(key, "/") {
			next, cmd := m.focusHelpSearch()
			m = next.(Model)
			loc.query = strings.TrimPrefix(key, "/")
			m.help.input.SetValue(loc.query)
			loc.scroll, loc.index = 0, 0
			m.setHelpLocation(loc)
			m.selectFirstHelpTarget()
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) updateHelpMouse(event tea.MouseEvent) (tea.Model, tea.Cmd) {
	if event.Button == tea.MouseButtonWheelDown {
		m.helpScroll(mouseWheelRows)
		return m, nil
	}
	if event.Button == tea.MouseButtonWheelUp {
		m.helpScroll(-mouseWheelRows)
		return m, nil
	}
	if event.Button != tea.MouseButtonLeft || event.Action != tea.MouseActionPress {
		return m, nil
	}
	f := m.helpLayout()
	loc := m.helpLocation()
	_, height := m.popupContentSize()
	if event.Y == height-1 && loc.screen == helpTopicScreen {
		width, _ := m.popupContentSize()
		line := popupFit(helpFooterLine(f, loc), width)
		for _, control := range []struct {
			label string
			key   tea.KeyType
		}{{"[←]", tea.KeyLeft}, {"[→]", tea.KeyRight}} {
			if i := strings.Index(line, control.label); i >= 0 {
				x := lipgloss.Width(line[:i])
				if event.X >= x && event.X < x+3 {
					return m.updateHelp(tea.KeyMsg{Type: control.key})
				}
			}
		}
		return m, nil
	}
	if event.Y == 0 {
		starts := []int{0, 10, 21}
		ends := []int{8, 19, 31}
		for i := range starts {
			if event.X >= starts[i] && event.X < ends[i] {
				m.setHelpSection(helpSection(i))
				return m, nil
			}
		}
	}
	if event.Y == f.scopeY {
		if loc.screen != helpHome {
			m.helpBack()
		} else {
			m.pushHelpLocation(helpLocation{screen: helpScopeScreen})
		}
		return m, nil
	}
	if event.Y == f.searchY {
		return m.focusHelpSearch()
	}
	start := min(max(0, loc.scroll), max(0, f.total-f.visible))
	n := start + event.Y - f.contentY
	if event.Y >= f.contentY && event.Y < f.contentY+f.visible && n < len(f.rows) {
		loc.index = n
		m.setHelpLocation(loc)
		return m.helpActivate(f.rows[n].target)
	}
	return m, nil
}
