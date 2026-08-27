package tui_test

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func row(id, name string, st task.State, next string) inventory.Row {
	return inventory.Row{
		Task: &task.Task{
			ID: id, Name: name, Repo: "demo", RepoPath: "/src/demo",
			Branch: "feat/" + id, State: st, Next: next,
		},
		CheckoutExists: true,
	}
}

// recorder captures which actions the dashboard triggered.
type recorder struct {
	opened []string
	parked []string
	nexts  map[string]string
}

func newActions(r *recorder, rows []inventory.Row) tui.Actions {
	r.nexts = map[string]string{}
	return tui.Actions{
		Runtime: runtime.None{},
		Reload: func(context.Context) ([]inventory.Row, error) {
			return rows, nil
		},
		Open: func(_ context.Context, t *task.Task) (string, error) {
			r.opened = append(r.opened, t.ID)
			return "opened " + t.ID, nil
		},
		Park: func(_ context.Context, t *task.Task, next string) (string, error) {
			r.parked = append(r.parked, t.ID)
			r.nexts[t.ID] = next
			return "parked " + t.ID, nil
		},
		SetNext: func(_ context.Context, t *task.Task, next string) error {
			r.nexts[t.ID] = next
			return nil
		},
	}
}

// send feeds a sequence of key presses through Update, running any command the
// model returns so the action callbacks actually fire — that is what makes
// these tests exercise the real key bindings rather than the model's fields.
func send(m tui.Model, msgs ...tea.Msg) tui.Model {
	var cur tea.Model = m
	for _, msg := range msgs {
		var cmd tea.Cmd
		cur, cmd = cur.Update(msg)
		// Follow the command chain, so an action's resulting message (and the
		// reload it triggers) is applied too.
		for i := 0; cmd != nil && i < 8; i++ {
			out := cmd()
			if out == nil {
				break
			}
			if _, isBatch := out.(tea.BatchMsg); isBatch {
				break
			}
			cur, cmd = cur.Update(out)
		}
	}
	return cur.(tui.Model)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(s string) []tea.KeyMsg {
	out := make([]tea.KeyMsg, 0, len(s))
	for _, r := range s {
		out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return out
}

func TestViewRendersTasksAndHelp(t *testing.T) {
	rows := []inventory.Row{
		row("a", "token refresh", task.Hot, "add the regression test"),
		row("b", "orderbook", task.Warm, ""),
	}
	m := tui.New(newActions(&recorder{}, rows), rows)
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 30})

	out := m.View()
	for _, want := range []string{"token refresh", "orderbook", "HOT", "WARM", "add the regression test"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "q quit") {
		t.Error("the footer should show the key bindings")
	}
}

func TestFilterByState(t *testing.T) {
	rows := []inventory.Row{
		row("a", "hot task", task.Hot, ""),
		row("b", "warm task", task.Warm, ""),
		row("c", "done task", task.Done, ""),
	}
	m := tui.New(newActions(&recorder{}, rows), rows)

	// Done tasks are hidden by default — a finished task is not work in progress.
	if strings.Contains(m.View(), "done task") {
		t.Error("done tasks should be hidden by default")
	}

	got := send(m, key("1")).View()
	if !strings.Contains(got, "hot task") || strings.Contains(got, "warm task") {
		t.Errorf("filter 1 should show only hot:\n%s", got)
	}
	got = send(m, key("2")).View()
	if !strings.Contains(got, "warm task") || strings.Contains(got, "hot task") {
		t.Errorf("filter 2 should show only warm:\n%s", got)
	}
	got = send(m, key("a")).View()
	if !strings.Contains(got, "done task") {
		t.Errorf("a should reveal done tasks:\n%s", got)
	}
}

func TestCursorStaysInBounds(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, ""), row("b", "two", task.Hot, "")}
	m := tui.New(newActions(&recorder{}, rows), rows)

	// Past the end and past the start; neither should panic or select nothing.
	m = send(m, key("down"), key("down"), key("down"), key("down"))
	if out := m.View(); !strings.Contains(out, "two") {
		t.Error("cursor overran the list")
	}
	m = send(m, key("up"), key("up"), key("up"))
	if out := m.View(); !strings.Contains(out, "one") {
		t.Error("cursor underran the list")
	}
}

func TestEnterOpensSelectedTask(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, ""), row("b", "second", task.Warm, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows)

	send(m, key("down"), key("enter"))
	if len(rec.opened) != 1 || rec.opened[0] != "b" {
		t.Errorf("enter should open the selected task, opened %v", rec.opened)
	}
}

func TestParkPromptsForNextAction(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows)

	m = send(m, key("p"))
	if !strings.Contains(m.View(), "park first") {
		t.Fatalf("p should open the park prompt:\n%s", m.View())
	}
	for _, k := range typeText("write the test") {
		m = send(m, k)
	}
	send(m, key("enter"))

	if len(rec.parked) != 1 || rec.parked[0] != "a" {
		t.Fatalf("park not triggered: %v", rec.parked)
	}
	if rec.nexts["a"] != "write the test" {
		t.Errorf("the typed next action should be passed through, got %q", rec.nexts["a"])
	}
}

func TestEscCancelsPrompt(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows)

	m = send(m, key("p"))
	m = send(m, key("esc"))
	if strings.Contains(m.View(), "park first") {
		t.Error("esc should close the prompt")
	}
	if len(rec.parked) != 0 {
		t.Error("esc must not park anything")
	}
}

func TestEditNextSeedsCurrentValue(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "existing note")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows)

	m = send(m, key("n"))
	if !strings.Contains(m.View(), "existing note") {
		t.Errorf("editing should start from the current value:\n%s", m.View())
	}
	send(m, key("enter"))
	if rec.nexts["a"] != "existing note" {
		t.Errorf("unchanged value should be saved as-is, got %q", rec.nexts["a"])
	}
}

func TestEmptyInventoryExplainsItself(t *testing.T) {
	m := tui.New(newActions(&recorder{}, nil), nil)
	out := m.View()
	if !strings.Contains(out, "Nothing to show") {
		t.Errorf("an empty dashboard should say what to do:\n%s", out)
	}
	// Acting on nothing must not panic.
	send(m, key("enter"), key("p"), key("n"))
}

func TestQuitStopsRendering(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	m := tui.New(newActions(&recorder{}, rows), rows)
	updated, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q should return a quit command")
	}
	if updated.View() != "" {
		t.Error("a quitting model should render nothing, so the terminal is left clean")
	}
}

func TestSummaryCountsStates(t *testing.T) {
	rows := []inventory.Row{
		row("a", "one", task.Hot, ""),
		row("b", "two", task.Hot, ""),
		row("c", "three", task.Warm, ""),
	}
	got := tui.New(newActions(&recorder{}, rows), rows).Summary()
	if !strings.Contains(got, "2 hot") || !strings.Contains(got, "1 warm") {
		t.Errorf("Summary = %q", got)
	}
	if got := tui.New(newActions(&recorder{}, nil), nil).Summary(); got != "no tasks" {
		t.Errorf("empty Summary = %q", got)
	}
}
