package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayHelp
	overlayRepoForm
	overlayTryMenu
	overlayTryForm
	overlayTryConfirm
)

type formField struct {
	key   string
	label string
	input textinput.Model
}

// overlayState uses fixed arrays so copying Model also copies the mutable form
// and menu state. A slice or pointer would violate Bubble Tea's value semantics.
type overlayState struct {
	kind       overlayKind
	title      string
	target     TryRow
	repoTarget RepoRow
	action     TryAction

	options      [8]TryAction
	optionLabels [8]string
	optionCount  int
	optionIndex  int

	fields     [4]formField
	fieldCount int
	fieldIndex int
}

func (m Model) openHelpOverlay() Model {
	m.overlay = overlayState{kind: overlayHelp, title: "keyboard help"}
	m.err = nil
	return m
}

func (m Model) openTryMenu(row TryRow) Model {
	overlay := overlayState{kind: overlayTryMenu, title: "Try actions", target: row}
	overlay.addOption(TryMark, "edit tags and note")
	switch row.Item.Phase {
	case catalog.PhaseActive:
		overlay.addOption(TryDeprecate, "deprecate (metadata only)")
	case catalog.PhaseDeprecated:
		overlay.addOption(TryReactivate, "reactivate")
	}
	if row.Item.Phase != catalog.PhaseGraduated {
		switch row.LocationState() {
		case catalog.LocationPresent:
			overlay.addOption(TryArchive, "archive locally (reversible move)")
			overlay.addOption(TryGraduate, "graduate into a project")
		case catalog.LocationArchived:
			overlay.addOption(TryRestore, "restore from local archive")
			overlay.addOption(TryGraduate, "graduate archived Try")
		}
	}
	m.overlay = overlay
	m.err = nil
	return m
}

func (o *overlayState) addOption(action TryAction, label string) {
	if o.optionCount >= len(o.options) {
		return
	}
	o.options[o.optionCount] = action
	o.optionLabels[o.optionCount] = label
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

func (m Model) updateOverlay(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay.kind {
	case overlayHelp:
		switch message.String() {
		case "esc", "?", "q":
			m.overlay = overlayState{}
		}
		return m, nil

	case overlayTryMenu:
		switch message.String() {
		case "esc", "q":
			m.overlay = overlayState{}
			return m, nil
		case "j", "down":
			if m.overlay.optionCount > 0 {
				m.overlay.optionIndex = (m.overlay.optionIndex + 1) % m.overlay.optionCount
			}
			return m, nil
		case "k", "up":
			if m.overlay.optionCount > 0 {
				m.overlay.optionIndex = (m.overlay.optionIndex + m.overlay.optionCount - 1) % m.overlay.optionCount
			}
			return m, nil
		case "enter":
			if m.overlay.optionCount == 0 {
				return m, nil
			}
			action := m.overlay.options[m.overlay.optionIndex]
			row := m.overlay.target
			switch action {
			case TryMark, TryRestore, TryGraduate:
				return m.openTryForm(action, row)
			case TryArchive:
				return m.openTryConfirmation(action, row)
			case TryDeprecate, TryReactivate:
				m.overlay = overlayState{}
				m.status = string(action) + " in progress…"
				return m, m.applyTry(TryRequest{Action: action, ID: row.Item.ID})
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

	request := TryRequest{Action: m.overlay.action, ID: m.overlay.target.Item.ID}
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

func (m Model) renderOverlay() string {
	var builder strings.Builder
	builder.WriteString(styleTitle.Render("dev  "+strings.ToUpper(m.overlay.title)) + "\n\n")
	switch m.overlay.kind {
	case overlayHelp:
		builder.WriteString("  navigation\n")
		builder.WriteString("    j/k, arrows move · ctrl+d/u page · g/G first/last · tab/h/l switch view\n")
		builder.WriteString("    / filter · 0 clear · r reload · esc close/clear/quit · q quit\n\n")
		builder.WriteString("  TASKS   enter open · n add note · N notes · p park · c next · 1/2/3 state · a done\n")
		builder.WriteString("  REPOS   enter open · n add note · N notes · space worktrees · m metadata · y copy · s worktree task · d direct task · O/R sort\n")
		builder.WriteString("  FLEET   enter Herdr/SSH open · e edit remotes.toml · r refresh · read-only Git overview\n")
		builder.WriteString("  TRY     enter open · n create · space actions · a history · O/R sort\n")
		builder.WriteString("  REMOTE  enter open local · n/N notes when cloned · c clone\n")
		builder.WriteString("  SKILLS  a interactive add · c check updates · u update selected\n\n")
		builder.WriteString("  " + styleHelp.Render("? / esc / q close help"))

	case overlayTryMenu:
		builder.WriteString(fmt.Sprintf("  %s\n  %s\n\n", m.overlay.target.Item.DisplayName(), contract(m.overlay.target.Item.Live.CurrentPath)))
		for index := 0; index < m.overlay.optionCount; index++ {
			prefix := "  "
			line := fmt.Sprintf("%-12s %s", m.overlay.options[index], m.overlay.optionLabels[index])
			if index == m.overlay.optionIndex {
				prefix = "▸ "
				line = styleSelected.Render(line)
			}
			builder.WriteString(prefix + line + "\n")
		}
		builder.WriteString("\n  " + styleHelp.Render("j/k choose · enter continue · esc cancel"))

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
