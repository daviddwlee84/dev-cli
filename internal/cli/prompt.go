package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

var errPromptCanceled = errors.New("prompt canceled")

type prompter struct {
	input io.Reader
	in    *bufio.Reader
	out   io.Writer
	style cliStyle
}

func newPrompter(app *App) *prompter {
	return &prompter{
		input: app.In,
		in:    bufio.NewReader(app.In),
		out:   app.Out,
		style: app.outStyle(),
	}
}

func (p *prompter) line(label, fallback string) (string, error) {
	return p.lineWithDisplayFallback(label, fallback, fallback)
}

func (p *prompter) lineWithDisplayFallback(label, fallback, displayFallback string) (string, error) {
	prefix := fmt.Sprintf("%s %s: ", p.style.prompt("?"), p.style.prompt(label))
	if displayFallback != "" {
		prefix = fmt.Sprintf("%s %s %s: ", p.style.prompt("?"), p.style.prompt(label), p.style.dim("["+displayFallback+"]"))
	}
	value, err := p.readLine(prefix)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (p *prompter) dangerLine(label string) (string, error) {
	prefix := fmt.Sprintf("%s %s: ", p.style.danger("?"), p.style.danger(label))
	line, err := p.readLine(prefix)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (p *prompter) choice(label, fallback, hint string, choices map[string]string) (string, error) {
	for {
		value, err := p.line(label, fallback)
		if err != nil {
			return "", err
		}
		value = strings.ToLower(value)
		if resolved, ok := choices[value]; ok {
			return resolved, nil
		}
		fmt.Fprintf(p.out, "  %s\n", p.style.warning("enter one of: "+hint))
	}
}

func (p *prompter) confirm(label string, defaultYes bool) (bool, error) {
	fallback := "y/N"
	if defaultYes {
		fallback = "Y/n"
	}
	for {
		prefix := fmt.Sprintf("%s %s %s: ", p.style.prompt("?"), p.style.prompt(label), p.style.dim("["+fallback+"]"))
		line, err := p.readLine(prefix)
		if err != nil {
			return false, err
		}
		value := strings.TrimSpace(line)
		switch strings.ToLower(value) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "":
			return defaultYes, nil
		default:
			fmt.Fprintln(p.out, "  "+p.style.warning("enter y or n"))
		}
	}
}

func (p *prompter) readLine(prefix string) (string, error) {
	if terminalPair(p.input, p.out) {
		return runLineEditor(p.input, p.out, prefix)
	}

	fmt.Fprint(p.out, prefix)
	line, err := p.in.ReadString('\n')
	if err != nil {
		return "", errPromptCanceled
	}
	return line, nil
}

type fileDescriptor interface {
	Fd() uintptr
}

func terminalPair(input io.Reader, output io.Writer) bool {
	in, inputOK := input.(fileDescriptor)
	out, outputOK := output.(fileDescriptor)
	return inputOK && outputOK && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

type lineEditorModel struct {
	prefix   string
	input    textinput.Model
	done     bool
	canceled bool
}

func newLineEditorModel(prefix string) lineEditorModel {
	input := textinput.New()
	input.Prompt = prefix
	input.Focus()
	return lineEditorModel{prefix: prefix, input: input}
}

func (m lineEditorModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m lineEditorModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyEnter:
			m.done = true
			m.input.Blur()
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			m.canceled = true
			m.input.Blur()
			return m, tea.Quit
		}
	}

	var command tea.Cmd
	m.input, command = m.input.Update(message)
	return m, command
}

func (m lineEditorModel) View() string {
	if m.done {
		// Leave the completed prompt in scrollback. Bubble Tea clears the line
		// containing its cursor when the renderer stops, so the trailing newline
		// moves that cursor below the submitted value first.
		return m.prefix + m.input.Value() + "\n"
	}
	return m.input.View()
}

func runLineEditor(input io.Reader, output io.Writer, prefix string) (string, error) {
	final, err := tea.NewProgram(
		newLineEditorModel(prefix),
		tea.WithInput(input),
		tea.WithOutput(output),
	).Run()
	if err != nil {
		return "", fmt.Errorf("prompt editor: %w", err)
	}
	model, ok := final.(lineEditorModel)
	if !ok {
		return "", errors.New("prompt editor returned an unexpected model")
	}
	if model.canceled {
		return "", errPromptCanceled
	}
	return model.input.Value(), nil
}
