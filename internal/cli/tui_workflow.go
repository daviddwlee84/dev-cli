package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

type tuiWorkflow struct {
	ctx     context.Context
	app     App
	request tui.WorkflowRequest
	result  tui.WorkflowResult
}

func newTUIWorkflow(ctx context.Context, app *App, request tui.WorkflowRequest) *tuiWorkflow {
	w := &tuiWorkflow{ctx: ctx, app: *app, request: request}
	if request.Task != nil {
		selected := *request.Task
		w.request.Task = &selected
		w.app.workflowTask = &selected
	}
	w.app.workflowHandoff = func(handoff func() error) error {
		w.result.AfterExit = handoff
		return nil
	}
	return w
}

func (w *tuiWorkflow) SetStdin(in io.Reader)      { w.app.In = in }
func (w *tuiWorkflow) SetStdout(out io.Writer)    { w.app.Out = out }
func (w *tuiWorkflow) SetStderr(out io.Writer)    { w.app.Err = out }
func (w *tuiWorkflow) Result() tui.WorkflowResult { return w.result }

func (w *tuiWorkflow) Run() error {
	err := w.run()
	if errors.Is(err, errPromptCanceled) {
		w.result.Status = "canceled"
		return nil
	}
	if err == nil && w.result.Status == "" {
		w.result.Status = "returned from " + w.request.Action
	}
	return err
}

func (w *tuiWorkflow) run() error {
	a, request := &w.app, w.request
	if request.Task != nil {
		current, err := a.Tasks.GetRecord(request.Task.ID)
		if err != nil {
			return err
		}
		if err := a.checkWorkflowTask(&current.Task); err != nil {
			return err
		}
		a.workflowRecordRevision = current.Revision
	}
	var cmd *cobra.Command
	var args []string
	switch request.Action {
	case "start-worktree", "start-direct":
		a.startHandoffPrompt = true
		mode := task.ModeWorktree
		if request.Action == "start-direct" {
			mode = task.ModeDirect
		}
		spec, confirmed, err := runStartWizard(w.ctx, a, startRequest{RepoRef: request.Repo.Path, RepoExplicit: true, Mode: mode, Focus: true})
		if err != nil {
			return err
		}
		if !confirmed {
			return errPromptCanceled
		}
		result, err := executeStartSpec(w.ctx, a, spec, a.Err)
		if err != nil {
			return err
		}
		if result.Worktree != nil {
			reportProvision(a, result.Worktree)
		}
		w.result.Status = "created " + result.Task.Title()
		if spec.Focus {
			if result.Runtime.Name() == "none" {
				return a.cdDirective(checkoutOf(result.Task))
			}
			return a.activate(w.ctx, result.Runtime, result.Task.RuntimeHandle)
		}
		return nil
	case "done", "resume", "retire", "sweep":
		if request.Task == nil {
			return errors.New("task selection is required")
		}
		args = []string{request.Task.ID}
		switch request.Action {
		case "done":
			cmd = newDoneCmd(a)
		case "resume":
			cmd = newResumeCmd(a)
		case "retire":
			cmd = newRetireCmd(a)
		case "sweep":
			cmd = newSweepCmd(a)
			args = []string{"--task", request.Task.ID, "--apply"}
		}
	case "browse":
		return runBrowseWorkflow(w.ctx, a, request)
	case "delete-try", "delete-try-permanently", "restore-removed-try":
		return runTryRemovalWorkflow(w.ctx, a, request)
	default:
		return fmt.Errorf("unknown workflow %q", request.Action)
	}
	// Use the same command adapter without another root Load or global Cobra
	// initializer. The dashboard already owns the accepted config/runtime.
	if err := cmd.ParseFlags(args); err != nil {
		return err
	}
	args = cmd.Flags().Args()
	if cmd.Args != nil {
		if err := cmd.Args(cmd, args); err != nil {
			return err
		}
	}
	cmd.SetContext(w.ctx)
	return cmd.RunE(cmd, args)
}

func (a *App) checkWorkflowTask(t *task.Task) error {
	if expected := a.workflowTask; expected != nil &&
		(t == nil || t.ID != expected.ID || t.Revision() != expected.Revision()) {
		return errors.New("selected task changed; reload the dashboard and choose again")
	}
	return nil
}

func (a *App) activate(ctx context.Context, rt runtime.Runtime, handle string) error {
	if a.workflowHandoff != nil {
		return a.workflowHandoff(func() error { return activateRuntime(ctx, rt, handle) })
	}
	return activateRuntime(ctx, rt, handle)
}
