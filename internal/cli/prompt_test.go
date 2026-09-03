package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/handoff"
	"github.com/daviddwlee84/dev-cli/internal/promptkit"
)

func TestExecutePromptFailsBeforeRuntimeCollection(t *testing.T) {
	run := config.AgentLauncher{Command: []string{"run-agent"}, Input: "stdin"}
	open := config.AgentLauncher{
		Command: []string{"open-agent", handoff.PromptFilePlaceholder}, Input: "file",
	}
	collectCalls := 0
	fakeRuntime := &activityRuntime{}
	collect := func(ctx context.Context) (promptkit.Snapshot, error) {
		collectCalls++
		_, _ = fakeRuntime.List(ctx)
		return promptkit.Snapshot{
			Scope: "machine", WorkingDirectory: t.TempDir(), Context: map[string]any{},
		}, nil
	}

	cfg := config.Default()
	cfg.Agents = []config.Agent{
		{Name: "open-default", Default: true, Open: open},
		{Name: "runner", Run: run},
	}
	app := &App{Cfg: cfg, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: fakeRuntime}
	if err := executePrompt(app, promptRun, "", true, promptkit.RecipeSessionClose, collect); err == nil ||
		!strings.Contains(err.Error(), `agent "open-default" has no [agent.run] launcher`) {
		t.Fatalf("missing-mode error = %v", err)
	}
	if collectCalls != 0 {
		t.Fatalf("runtime collection ran %d time(s) before launcher resolution", collectCalls)
	}

	collectCalls = 0
	cfg.Agents = []config.Agent{{Name: "interactive", Default: true, Open: open}}
	app = &App{
		Cfg: cfg, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: fakeRuntime,
		interactiveCheck: func() bool { return false },
	}
	if err := executePrompt(app, promptOpen, "", false, promptkit.RecipeSessionClose, collect); err == nil ||
		!strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("TTY error = %v", err)
	}
	if collectCalls != 0 {
		t.Fatalf("runtime collection ran %d time(s) before the TTY check", collectCalls)
	}
}

func TestLineEditorSupportsInlineNavigationAndDeletion(t *testing.T) {
	model := newLineEditorModel("? Value: ")
	for _, message := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("ac")},
		{Type: tea.KeyLeft},
		{Type: tea.KeyRunes, Runes: []rune("b")},
		{Type: tea.KeyHome},
		{Type: tea.KeyRunes, Runes: []rune("^")},
		{Type: tea.KeyEnd},
		{Type: tea.KeyRunes, Runes: []rune("$")},
		{Type: tea.KeyLeft},
		{Type: tea.KeyLeft},
		{Type: tea.KeyDelete},
		{Type: tea.KeyBackspace},
		{Type: tea.KeyRight},
		{Type: tea.KeyRunes, Runes: []rune("!")},
	} {
		updated, _ := model.Update(message)
		var ok bool
		model, ok = updated.(lineEditorModel)
		if !ok {
			t.Fatalf("Update returned %T", updated)
		}
	}

	if got := model.input.Value(); got != "^a$!" {
		t.Fatalf("edited value = %q, want %q", got, "^a$!")
	}
}

func TestLineEditorSubmitKeepsPromptAndFinalValue(t *testing.T) {
	model := newLineEditorModel("? Value: ")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("answer")})
	model = updated.(lineEditorModel)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(lineEditorModel)

	if !model.done || model.canceled {
		t.Fatalf("submitted model state = done %v, canceled %v", model.done, model.canceled)
	}
	if got := model.View(); got != "? Value: answer\n" {
		t.Fatalf("final view = %q", got)
	}
	if command == nil {
		t.Fatal("submit did not return a quit command")
	}
}

func TestLineEditorCancelKeys(t *testing.T) {
	for name, key := range map[string]tea.KeyType{
		"escape":    tea.KeyEsc,
		"control-c": tea.KeyCtrlC,
	} {
		t.Run(name, func(t *testing.T) {
			model := newLineEditorModel("? Value: ")
			updated, command := model.Update(tea.KeyMsg{Type: key})
			model = updated.(lineEditorModel)
			if !model.done || !model.canceled || command == nil {
				t.Fatalf("canceled model state = done %v, canceled %v, command %v", model.done, model.canceled, command != nil)
			}
		})
	}
}

func TestPrompterStringReaderKeepsBufferedLineBehavior(t *testing.T) {
	var output bytes.Buffer
	app := &App{In: strings.NewReader("edited\n"), Out: &output, colorMode: colorNever}
	value, err := newPrompter(app).line("Value", "default")
	if err != nil {
		t.Fatal(err)
	}
	if value != "edited" {
		t.Fatalf("value = %q", value)
	}
	if got := output.String(); got != "? Value [default]: " {
		t.Fatalf("prompt = %q", got)
	}
}

func TestPrompterCanRedactDisplayFallbackWithoutChangingValue(t *testing.T) {
	var output bytes.Buffer
	app := &App{In: strings.NewReader("\n"), Out: &output, colorMode: colorNever}
	value, err := newPrompter(app).lineWithDisplayFallback(
		"Source", "https://user:secret@example.test/repo.git", "https://example.test/repo.git",
	)
	if err != nil {
		t.Fatal(err)
	}
	if value != "https://user:secret@example.test/repo.git" {
		t.Fatalf("value = %q", value)
	}
	if strings.Contains(output.String(), "secret") || output.String() != "? Source [https://example.test/repo.git]: " {
		t.Fatalf("prompt = %q", output.String())
	}
}
