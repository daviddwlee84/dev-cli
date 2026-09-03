package picker

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/textmatch"
)

const defaultVisibleRows = 10

type builtinModel struct {
	input    textinput.Model
	items    []Item
	search   []string
	visible  []int
	cursor   int
	offset   int
	rows     int
	width    int
	done     bool
	canceled bool
	selected Item
}

func newBuiltinModel(request Request) builtinModel {
	input := textinput.New()
	input.Prompt = fmt.Sprintf("? %s: ", request.Prompt)
	input.Focus()
	items := append([]Item(nil), request.Items...)
	search := make([]string, len(items))
	for index, item := range items {
		search[index] = strings.ToLower(item.Label + " " + item.Description)
	}
	model := builtinModel{
		input:  input,
		items:  items,
		search: search,
		rows:   defaultVisibleRows,
	}
	model.refilter()
	return model
}

func (m builtinModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m builtinModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		available := msg.Height - 3
		switch {
		case available < 1:
			m.rows = 1
		case available < defaultVisibleRows:
			m.rows = available
		default:
			m.rows = defaultVisibleRows
		}
		m.keepVisible()
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			m.canceled = true
			m.input.Blur()
			return m, tea.Quit
		case tea.KeyEnter:
			if len(m.visible) == 0 {
				return m, nil
			}
			m.done = true
			m.selected = m.items[m.visible[m.cursor]]
			m.input.Blur()
			return m, tea.Quit
		case tea.KeyUp, tea.KeyCtrlP:
			m.move(-1)
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.move(1)
			return m, nil
		}
	}

	query := m.input.Value()
	var command tea.Cmd
	m.input, command = m.input.Update(message)
	if m.input.Value() != query {
		m.refilter()
	}
	return m, command
}

func (m builtinModel) View() string {
	if m.done {
		return "\n"
	}
	var out strings.Builder
	out.WriteString(m.input.View())
	out.WriteByte('\n')
	if len(m.visible) == 0 {
		out.WriteString("  No matches\n")
		return out.String()
	}
	end := m.offset + m.rows
	if end > len(m.visible) {
		end = len(m.visible)
	}
	for position := m.offset; position < end; position++ {
		item := m.items[m.visible[position]]
		marker := "  "
		if position == m.cursor {
			marker = "> "
		}
		label := cleanDisplay(item.Label)
		if label == "" {
			label = "(unnamed)"
		}
		line := marker + label
		if description := cleanDisplay(item.Description); description != "" {
			line += "  " + description
		}
		out.WriteString(clipColumns(line, m.width))
		out.WriteByte('\n')
	}
	return out.String()
}

func (m *builtinModel) refilter() {
	m.visible = m.visible[:0]
	query := strings.ToLower(m.input.Value())
	for index, haystack := range m.search {
		if textmatch.TermsFolded(haystack, query) {
			m.visible = append(m.visible, index)
		}
	}
	m.cursor = 0
	m.offset = 0
}

func (m *builtinModel) move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor >= len(m.visible) {
		m.cursor = 0
	}
	m.keepVisible()
}

func (m *builtinModel) keepVisible() {
	if m.rows <= 0 {
		m.rows = 1
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.rows {
		m.offset = m.cursor - m.rows + 1
	}
}

func clipColumns(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var out strings.Builder
	used := 0
	for _, r := range value {
		cellWidth := lipgloss.Width(string(r))
		if used+cellWidth > width-1 {
			break
		}
		out.WriteRune(r)
		used += cellWidth
	}
	return out.String() + "…"
}

func (s *Selector) selectBuiltin(ctx context.Context, request Request) (Result, error) {
	program := tea.NewProgram(
		newBuiltinModel(request),
		tea.WithInput(s.in),
		tea.WithOutput(s.out),
		tea.WithContext(ctx),
	)
	final, err := program.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if err != nil {
		return Result{}, fmt.Errorf("built-in picker: %w", err)
	}
	model, ok := final.(builtinModel)
	if !ok {
		return Result{}, fmt.Errorf("built-in picker returned an unexpected model")
	}
	if model.canceled {
		return Result{}, ErrCanceled
	}
	if !model.done {
		return Result{}, nil
	}
	return Result{Item: model.selected}, nil
}
