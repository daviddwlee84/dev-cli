package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestDashboardStartWizardHandoffChoice(t *testing.T) {
	for _, choice := range []string{"y", "n"} {
		t.Run(choice, func(t *testing.T) {
			f := newStartFixture(t, runtime.None{})
			f.app.interactiveCheck = func() bool { return true }
			w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "start-direct", Repo: repo.Repo{Path: f.repo.Root}})
			w.SetStdin(strings.NewReader("dashboard work\n\n\n" + choice + "\ny\n"))
			w.SetStdout(&f.stdout)
			w.SetStderr(&f.stderr)
			if err := w.Run(); err != nil {
				t.Fatalf("%v\n%s", err, f.stdout.String())
			}
			if (w.Result().AfterExit != nil) != (choice == "y") {
				t.Fatalf("handoff choice %s: %+v", choice, w.Result())
			}
			if strings.Contains(f.stdout.String(), "cd ") {
				t.Fatal("shell handoff executed before dashboard exit")
			}
			tasks, err := f.app.Tasks.List()
			if err != nil || len(tasks) != 1 || tasks[0].EffectiveMode() != task.ModeDirect {
				t.Fatalf("tasks=%+v err=%v", tasks, err)
			}
		})
	}
}

func TestDashboardWorkflowRejectsChangedTask(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	if err := f.run("--task", "old", "--direct"); err != nil {
		t.Fatal(err)
	}
	tasks, _ := f.app.Tasks.List()
	selected := *tasks[0]
	current, err := f.app.Tasks.GetRecord(selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Task.Next = "changed"
	if _, err := f.app.Tasks.Update(t.Context(), &current.Task, current.Revision); err != nil {
		t.Fatal(err)
	}
	w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "done", Task: &selected})
	w.SetStdin(strings.NewReader(""))
	w.SetStdout(io.Discard)
	w.SetStderr(io.Discard)
	if err := w.Run(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
}

func TestDashboardDoneSharesDirectWizardAndDefersCD(t *testing.T) {
	f := newStartFixture(t, &activityRuntime{})
	if err := f.run("--task", "finish", "--direct"); err != nil {
		t.Fatal(err)
	}
	tasks, _ := f.app.Tasks.List()
	f.app.interactiveCheck = func() bool { return true }
	w := newTUIWorkflow(context.Background(), f.app, tui.WorkflowRequest{Action: "done", Task: tasks[0]})
	w.SetStdin(strings.NewReader("y\n"))
	w.SetStdout(&f.stdout)
	w.SetStderr(&f.stderr)
	if err := w.Run(); err != nil {
		t.Fatalf("%v\n%s", err, f.stdout.String())
	}
	finished, err := f.app.Tasks.Get(tasks[0].ID)
	if err != nil || finished.State != task.Done {
		t.Fatalf("%+v %v", finished, err)
	}
}

func TestDashboardCDHandoffRunsOnlyAfterExit(t *testing.T) {
	var out bytes.Buffer
	a := App{Out: &out}
	w := newTUIWorkflow(context.Background(), &a, tui.WorkflowRequest{})
	if err := w.app.cdDirective("/chosen"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || w.Result().AfterExit == nil {
		t.Fatal("handoff was not deferred")
	}
}

func TestDashboardSweepOnlyReapsSelectedMissingRepository(t *testing.T) {
	store := task.NewStore(t.TempDir())
	var selected *task.Task
	var otherID string
	for _, name := range []string{"selected", "other"} {
		tracked := &task.Task{Name: name, Repo: name, RepoPath: filepath.Join(t.TempDir(), "missing"), Branch: "main", Base: "main", State: task.Warm, Mode: task.ModeDirect}
		if err := store.Save(tracked); err != nil {
			t.Fatal(err)
		}
		if name == "selected" {
			selected = tracked
		} else {
			otherID = tracked.ID
		}
	}
	var out bytes.Buffer
	a := &App{Tasks: store, runtimeInstance: runtime.None{}, In: strings.NewReader("y\n"), Out: &out, Err: &out}
	w := newTUIWorkflow(t.Context(), a, tui.WorkflowRequest{Action: "sweep", Task: selected})
	if err := w.Run(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if _, err := store.Get(selected.ID); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("selected task remains: %v", err)
	}
	if _, err := store.Get(otherID); err != nil {
		t.Fatalf("other task affected: %v", err)
	}
	if strings.Contains(out.String(), otherID) {
		t.Fatalf("unrelated task was included:\n%s", out.String())
	}
}
