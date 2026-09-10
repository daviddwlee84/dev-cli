package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

// WorkflowRequest pins the row selected before the terminal is suspended.
// Policy and all prompts belong to the CLI's shared workflow runners.
type WorkflowRequest struct {
	AllLocal        bool
	ScopeLabel      string
	Selection       []triage.Target
	Snapshots       []triage.RepositorySnapshot
	LocalGeneration uint64
	ShowAllTries    bool
	Action          string
	Task            *task.Task
	Repo            repo.Repo
	Try             TryRow
	Remote          *forge.RemoteRepo
	Path            string
}

type WorkflowResult struct {
	Severity  string
	Ledger    *triage.Ledger
	Scoped    bool
	Local     *TriageDelta
	Status    string
	AfterExit func() error
}

type Workflow interface {
	tea.ExecCommand
	Result() WorkflowResult
}

type workflowMsg struct {
	result WorkflowResult
	err    error
}

func (m Model) runWorkflow(request WorkflowRequest) (tea.Model, tea.Cmd) {
	if m.actions.Workflow == nil {
		return m, nil
	}
	workflow, err := m.actions.Workflow(context.Background(), request)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.err, m.status = nil, "opening "+request.Action+"…"
	return m, tea.Exec(workflow, func(err error) tea.Msg {
		return afterExec(workflowMsg{result: workflow.Result(), err: err})
	})
}

// AfterExit returns a handoff which requires the alternate screen to be gone.
func (m Model) AfterExit() func() error { return m.afterExit }
