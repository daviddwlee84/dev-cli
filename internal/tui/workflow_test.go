package tui_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type fakeWorkflow struct{}

func (*fakeWorkflow) Run() error                 { return nil }
func (*fakeWorkflow) SetStdin(io.Reader)         {}
func (*fakeWorkflow) SetStdout(io.Writer)        {}
func (*fakeWorkflow) SetStderr(io.Writer)        {}
func (*fakeWorkflow) Result() tui.WorkflowResult { return tui.WorkflowResult{} }

func TestTaskSpaceExposesLegalWorkflowActions(t *testing.T) {
	for _, state := range []task.State{task.Hot, task.Warm, task.Cold, task.Done} {
		t.Run(string(state), func(t *testing.T) {
			r := row("task", "selected", state, "")
			r.CheckoutExists = true
			actions := newActions(&recorder{}, []inventory.Row{r})
			actions.Workflow = func(context.Context, tui.WorkflowRequest) (tui.Workflow, error) { return &fakeWorkflow{}, nil }
			m := tui.New(actions, []inventory.Row{r}, nil)
			if state == task.Done {
				m = send(m, key("a"))
			}
			m = send(m, key(" "))
			out := m.View()
			if !strings.Contains(out, "inspect and recover this task") {
				t.Fatal(out)
			}
			if strings.Contains(out, "finish task") != (state == task.Hot || state == task.Warm) {
				t.Fatal(out)
			}
			if strings.Contains(out, "retire task") != (state == task.Done) {
				t.Fatal(out)
			}
		})
	}
}

func TestMissingTaskOffersRecoveryWithoutFinish(t *testing.T) {
	r := row("task", "missing", task.Hot, "")
	r.CheckoutExists = false
	actions := newActions(&recorder{}, []inventory.Row{r})
	actions.Workflow = func(context.Context, tui.WorkflowRequest) (tui.Workflow, error) { return &fakeWorkflow{}, nil }
	m := send(tui.New(actions, []inventory.Row{r}, nil), key("ctrl+o"))
	if out := m.View(); strings.Contains(out, "finish task") || !strings.Contains(out, "recover this task") {
		t.Fatal(out)
	}
}

func TestTrashedFolderNeedsReassociationBeforeOpen(t *testing.T) {
	row := tryRow("retained", "retained", catalog.PhaseActive, catalog.LocationEvicted)
	row.Item.Live.Present = true
	row.Item.Live.CurrentPath = "/restored"
	if row.Present() {
		t.Fatal("unassociated restored bytes became an openable Try")
	}
}
