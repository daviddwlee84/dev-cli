package picker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestRenderCandidatesKeepsDuplicateItemsDistinct(t *testing.T) {
	items := []Item{
		{Value: "first", Label: "same", Description: "description"},
		{Value: "second", Label: "same", Description: "description"},
		{Value: "third", Label: "line\nwith\ttabs", Description: "clean\x00me"},
	}
	lines, byLine := renderCandidates(items)
	if len(lines) != len(items) || lines[0] == lines[1] {
		t.Fatalf("rendered lines = %q", lines)
	}
	for index, line := range lines {
		if got := byLine[line].Value; got != items[index].Value {
			t.Fatalf("line %d maps to %q, want %q", index, got, items[index].Value)
		}
		if strings.ContainsAny(line, "\r\n\x00\x1b") {
			t.Fatalf("unsafe rendered line %q", line)
		}
	}
}

func TestBuiltinModelFiltersNavigatesAndCancels(t *testing.T) {
	request := Request{Prompt: "Repository", Items: []Item{
		{Value: "one", Label: "alpha", Description: "service"},
		{Value: "two", Label: "beta", Description: "service"},
		{Value: "three", Label: "gamma", Description: "docs"},
	}}
	model := newBuiltinModel(request)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("service")})
	model = updated.(builtinModel)
	if len(model.visible) != 2 {
		t.Fatalf("visible = %v", model.visible)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(builtinModel)
	updated, _ = model.Update(struct{}{})
	model = updated.(builtinModel)
	if model.cursor != 1 {
		t.Fatalf("non-input message reset cursor to %d", model.cursor)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 40, Height: 2})
	model = updated.(builtinModel)
	if model.rows != 1 || model.cursor != 1 {
		t.Fatalf("resized model rows=%d cursor=%d", model.rows, model.cursor)
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(builtinModel)
	if command == nil || !model.done || model.canceled || model.selected.Value != "two" {
		t.Fatalf("selected model = %+v, command nil = %t", model, command == nil)
	}

	model = newBuiltinModel(request)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(builtinModel)
	if command == nil || !model.done || !model.canceled {
		t.Fatalf("canceled model = %+v, command nil = %t", model, command == nil)
	}
}

func TestBuiltinViewSanitizesControlsAndClipsDisplayColumns(t *testing.T) {
	model := newBuiltinModel(Request{Prompt: "Repo", Items: []Item{{
		Value: "one", Label: "界界\nrow\x1b[31m", Description: "description",
	}}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 4, Height: 8})
	view := updated.(builtinModel).View()
	if strings.ContainsAny(view, "\x1b\r") {
		t.Fatalf("view contains terminal controls: %q", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) < 2 || lipgloss.Width(lines[1]) > 4 {
		t.Fatalf("candidate row exceeds viewport: %q (width %d)", lines[1], lipgloss.Width(lines[1]))
	}
}

func TestUnavailableExternalPickerUsesBuiltin(t *testing.T) {
	var output bytes.Buffer
	selector := New(strings.NewReader("\x03"), &output, io.Discard, []string{"missing-picker"})
	selector.lookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}
	result, err := selector.Select(t.Context(), Request{
		Prompt: "Repository", Items: []Item{{Value: "one", Label: "one"}},
	})
	if !errors.Is(err, ErrCanceled) || result.Item.Value != "" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestExternalPickerReturnsExactItem(t *testing.T) {
	t.Setenv("DEV_PICKER_HELPER", "select-second")
	selector := helperSelector()
	request := Request{Prompt: "Repo", Items: []Item{
		{Value: "first-value", Label: "same"},
		{Value: "second-value", Label: "same"},
	}}
	result, err := selector.Select(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.Value != "second-value" {
		t.Fatalf("result = %+v", result)
	}
}

func TestExternalPickerCancellationCodes(t *testing.T) {
	for _, mode := range []string{"exit-1", "exit-130"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DEV_PICKER_HELPER", mode)
			result, err := helperSelector().Select(t.Context(), Request{Items: []Item{{Value: "v", Label: "item"}}})
			if !errors.Is(err, ErrCanceled) || result.Item.Value != "" {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
		})
	}
}

func TestExternalPickerRejectsUnknownOutput(t *testing.T) {
	t.Setenv("DEV_PICKER_HELPER", "unknown")
	_, err := helperSelector().Select(t.Context(), Request{Items: []Item{{Value: "v", Label: "item"}}})
	if err == nil || !strings.Contains(err.Error(), "unknown selection") {
		t.Fatalf("err = %v", err)
	}
}

func TestExternalPickerHonorsContext(t *testing.T) {
	t.Setenv("DEV_PICKER_HELPER", "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := helperSelector().Select(ctx, Request{Items: []Item{{Value: "v", Label: "item"}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func helperSelector() *Selector {
	return New(nil, io.Discard, io.Discard, []string{
		os.Args[0], "-test.run=^TestPickerHelperProcess$", "--", "{prompt}",
	})
}

func TestPickerHelperProcess(t *testing.T) {
	mode := os.Getenv("DEV_PICKER_HELPER")
	if mode == "" {
		return
	}
	body, _ := io.ReadAll(os.Stdin)
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	switch mode {
	case "select-second":
		fmt.Fprintln(os.Stdout, lines[1])
		os.Exit(0)
	case "exit-1":
		os.Exit(1)
	case "exit-130":
		os.Exit(130)
	case "unknown":
		fmt.Fprintln(os.Stdout, "not one of the candidates")
		os.Exit(0)
	case "wait":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(2)
	}
}
