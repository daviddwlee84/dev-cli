package tui

import (
	"context"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func mouseMessage(x, y int, button tea.MouseButton, action tea.MouseAction) tea.MouseMsg {
	return tea.MouseMsg(tea.MouseEvent{X: x, Y: y, Button: button, Action: action})
}

func applyMouse(model Model, message tea.MouseMsg) (Model, tea.Cmd) {
	updated, command := model.Update(message)
	return updated.(Model), command
}

func mouseTaskRows(count int) []inventory.Row {
	rows := make([]inventory.Row, count)
	for i := range rows {
		rows[i] = inventory.Row{
			Task:     &task.Task{ID: fmt.Sprintf("task-%02d", i), Name: fmt.Sprintf("task-%02d", i), State: task.Hot},
			Checkout: fmt.Sprintf("/tmp/task-%02d", i), CheckoutExists: true,
		}
	}
	return rows
}

func TestMouseClicksResponsiveTabSpans(t *testing.T) {
	for _, width := range []int{100, 42} {
		model := New(Actions{}, nil, nil)
		model.width = width
		layout := model.buildHeaderLayout()
		if len(layout.tabs) != len(Views) {
			t.Fatalf("width %d rendered %d tabs, want %d", width, len(layout.tabs), len(Views))
		}
		for _, hit := range layout.tabs {
			candidate := model
			candidate, _ = applyMouse(candidate, mouseMessage((hit.from+hit.to)/2, 0, tea.MouseButtonLeft, tea.MouseActionPress))
			if candidate.view != hit.view {
				t.Errorf("width %d click [%d,%d) selected %s, want %s", width, hit.from, hit.to, candidate.view, hit.view)
			}
		}
	}

	model := New(Actions{}, nil, nil)
	model.width = 9
	layout := model.buildHeaderLayout()
	if len(layout.tabs) != 1 || layout.tabs[0].view != ViewTasks {
		t.Fatalf("current-only layout tabs = %+v", layout.tabs)
	}
	model, _ = applyMouse(model, mouseMessage(model.width-1, 0, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.view != ViewTasks {
		t.Fatalf("click outside current-only tab changed view to %s", model.view)
	}
}

func TestMouseSelectsOnlyVisibleDataRows(t *testing.T) {
	rows := mouseTaskRows(20)
	opened := 0
	model := New(Actions{Open: func(context.Context, *task.Task) (OpenResult, error) {
		opened++
		return OpenResult{}, nil
	}}, rows, nil)
	model.height = 18
	model.setAt(10)
	from, _ := model.window(len(rows))
	firstY := 2 + model.listPreambleLines()

	model, _ = applyMouse(model, mouseMessage(4, firstY+1, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.at() != from+1 || opened != 0 {
		t.Fatalf("row click selected=%d want=%d opened=%d", model.at(), from+1, opened)
	}
	selected := model.at()
	for _, message := range []tea.MouseMsg{
		mouseMessage(4, firstY, tea.MouseButtonLeft, tea.MouseActionRelease),
		mouseMessage(4, firstY, tea.MouseButtonLeft, tea.MouseActionMotion),
		mouseMessage(4, 2, tea.MouseButtonLeft, tea.MouseActionPress),
		mouseMessage(4, firstY+model.listHeight()+2, tea.MouseButtonLeft, tea.MouseActionPress),
	} {
		model, _ = applyMouse(model, message)
	}
	if model.at() != selected || opened != 0 {
		t.Fatalf("non-row mouse input changed selection=%d opened=%d", model.at(), opened)
	}
	model, _ = applyMouse(model, mouseMessage(4, firstY+1, tea.MouseButtonLeft, tea.MouseActionPress))
	if opened != 0 {
		t.Fatalf("repeated clicks inferred double-click open: %d", opened)
	}
}

func TestMouseAccountsForCapabilityLoadingPreamble(t *testing.T) {
	model := New(Actions{}, nil, nil)
	model.view = ViewSkills
	model.skills = []agentskill.Skill{{Name: "first"}, {Name: "second"}}
	model.beginViewLoad(ViewSkills, loadRefresh)
	if got := model.listPreambleLines(); got != 2 {
		t.Fatalf("loading preamble lines = %d", got)
	}

	model.setAt(1)
	model, _ = applyMouse(model, mouseMessage(3, 3, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.at() != 1 {
		t.Fatalf("table header click changed capability selection to %d", model.at())
	}
	model, _ = applyMouse(model, mouseMessage(3, 4, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.at() != 0 {
		t.Fatalf("first capability data row selected %d", model.at())
	}
}

func TestMouseWheelMovesThreeRowsOnlyOverList(t *testing.T) {
	model := New(Actions{}, mouseTaskRows(10), nil)
	model, _ = applyMouse(model, mouseMessage(3, 2, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if model.at() != 3 {
		t.Fatalf("wheel down selected %d, want 3", model.at())
	}
	model, _ = applyMouse(model, mouseMessage(3, 2, tea.MouseButtonWheelUp, tea.MouseActionPress))
	model, _ = applyMouse(model, mouseMessage(3, 2, tea.MouseButtonWheelUp, tea.MouseActionPress))
	if model.at() != 0 {
		t.Fatalf("wheel up did not clamp: %d", model.at())
	}
	model, _ = applyMouse(model, mouseMessage(3, 2, tea.MouseButtonWheelRight, tea.MouseActionPress))
	model, _ = applyMouse(model, mouseMessage(3, 20, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if model.at() != 0 {
		t.Fatalf("horizontal/outside wheel changed selection: %d", model.at())
	}
}

func TestMouseRightClickSelectsThenRunsActionMenu(t *testing.T) {
	rows := mouseTaskRows(2)
	var opened []string
	model := New(Actions{Open: func(_ context.Context, selected *task.Task) (OpenResult, error) {
		opened = append(opened, selected.ID)
		return OpenResult{}, nil
	}}, rows, nil)
	firstY := 2 + model.listPreambleLines()
	model, _ = applyMouse(model, mouseMessage(3, firstY+1, tea.MouseButtonRight, tea.MouseActionPress))
	if model.at() != 1 || model.overlay.kind != overlayActionMenu {
		t.Fatalf("right click selected=%d overlay=%v", model.at(), model.overlay.kind)
	}

	firstOptionY := 4
	if model.overlay.detail != "" {
		firstOptionY++
	}
	var command tea.Cmd
	model, command = applyMouse(model, mouseMessage(3, firstOptionY, tea.MouseButtonLeft, tea.MouseActionPress))
	if command == nil {
		t.Fatal("clicking open menu option returned no command")
	}
	_ = command()
	if len(opened) != 1 || opened[0] != "task-01" {
		t.Fatalf("opened tasks = %v", opened)
	}
	if model.overlay.kind != overlayNone {
		t.Fatalf("action menu remained open: %v", model.overlay.kind)
	}
}

func TestMouseActionMenuWheelOutsideClickAndModalIsolation(t *testing.T) {
	rows := mouseTaskRows(1)
	model := New(Actions{
		Open:    func(context.Context, *task.Task) (OpenResult, error) { return OpenResult{}, nil },
		SetNext: func(context.Context, *task.Task, string) error { return nil },
		Park:    func(context.Context, *task.Task, string) (string, error) { return "", nil },
	}, rows, nil).openActionMenu()
	model, _ = applyMouse(model, mouseMessage(2, 2, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if model.overlay.optionIndex != 1 {
		t.Fatalf("menu wheel selected option %d", model.overlay.optionIndex)
	}
	model, _ = applyMouse(model, mouseMessage(2, 0, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.overlay.kind != overlayNone {
		t.Fatalf("outside click did not close menu: %v", model.overlay.kind)
	}

	model = New(Actions{Open: func(context.Context, *task.Task) (OpenResult, error) {
		return OpenResult{}, nil
	}}, rows, nil).openActionMenu()
	firstOptionY := model.buildActionMenuLayout().firstOptionY
	model, command := applyMouse(model, mouseMessage(100, firstOptionY, tea.MouseButtonLeft, tea.MouseActionPress))
	if command != nil || model.overlay.kind != overlayNone {
		t.Fatalf("blank right margin executed menu option: command=%v overlay=%v", command, model.overlay.kind)
	}

	model = New(Actions{}, rows, nil).openHelpOverlay()
	model.setAt(0)
	model, _ = applyMouse(model, mouseMessage(2, 3, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.overlay.kind != overlayHelp || model.at() != 0 {
		t.Fatalf("help overlay passed click through: overlay=%v selected=%d", model.overlay.kind, model.at())
	}
}

func TestMouseFailsClosedWhenFrameIsTopClipped(t *testing.T) {
	model := New(Actions{}, mouseTaskRows(20), nil)
	model.width, model.height = 80, 8
	model.setAt(10)
	if model.mouseFrameFits() {
		t.Fatal("tiny overflowing frame unexpectedly considered mouse-safe")
	}
	layout := model.buildHeaderLayout()
	repos := layout.tabs[1]
	model, _ = applyMouse(model, mouseMessage((repos.from+repos.to)/2, 0, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.view != ViewTasks {
		t.Fatalf("clipped frame treated visible Y=0 as a tab: %s", model.view)
	}
	model, _ = applyMouse(model, mouseMessage(3, 3, tea.MouseButtonLeft, tea.MouseActionPress))
	if model.at() != 10 {
		t.Fatalf("clipped frame treated detail as list row: %d", model.at())
	}
}

func TestMouseIgnoresModifiedClicksAndCloneContextActions(t *testing.T) {
	rows := mouseTaskRows(2)
	model := New(Actions{Open: func(context.Context, *task.Task) (OpenResult, error) {
		return OpenResult{}, nil
	}}, rows, nil)
	firstY := 2 + model.listPreambleLines()
	modified := tea.MouseMsg(tea.MouseEvent{
		X: 2, Y: firstY + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Shift: true,
	})
	model, _ = applyMouse(model, modified)
	if model.at() != 0 {
		t.Fatalf("modified click selected row %d", model.at())
	}

	model.remoteClone.phase = remoteCloneRunning
	model, _ = applyMouse(model, mouseMessage(2, firstY+1, tea.MouseButtonRight, tea.MouseActionPress))
	if model.at() != 1 || model.overlay.kind != overlayNone {
		t.Fatalf("clone-time right click selected=%d overlay=%v", model.at(), model.overlay.kind)
	}
}
