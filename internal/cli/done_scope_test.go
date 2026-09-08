package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type foregroundTestRuntime struct {
	*currentPaneRuntime
	processes     map[string]runtime.PaneProcessObservation
	processError  error
	closePaneHook func()
}

func (r *foregroundTestRuntime) ClosePane(ctx context.Context, id string) error {
	if err := r.currentPaneRuntime.ClosePane(ctx, id); err != nil {
		return err
	}
	if r.closePaneHook != nil {
		r.closePaneHook()
	}
	return nil
}

func (r *foregroundTestRuntime) InspectPaneProcesses(_ context.Context, id string) (runtime.PaneProcessObservation, error) {
	p := r.processes[id]
	p.PaneID = id
	return p, r.processError
}
func (r *foregroundTestRuntime) Close(_ context.Context, id string) error {
	r.closeCalls = append(r.closeCalls, id)
	var keep []runtime.Session
	for _, s := range r.sessions {
		if s.Handle != id {
			keep = append(keep, s)
		}
	}
	r.sessions = keep
	return r.closeErr
}

func newScopeFixture(t *testing.T) (*startFixture, *foregroundTestRuntime, *task.Task) {
	t.Helper()
	rt := &foregroundTestRuntime{currentPaneRuntime: &currentPaneRuntime{activityRuntime: &activityRuntime{name: "herdr"}, currentPane: "wP:p2"}, processes: make(map[string]runtime.PaneProcessObservation)}
	f := newStartFixture(t, rt)
	if err := f.run("--task", "scope", "--branch", "feat/scope", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	candidate, err := f.app.Tasks.Resolve("scope")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.WorktreePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(candidate.WorktreePath, "add", "feature.txt")
	f.repo.GitIn(candidate.WorktreePath, "commit", "-m", "feat: scope")
	t.Setenv("HERDR_WORKSPACE_ID", "wP")
	t.Setenv("HERDR_PANE_ID", "wP:p2")
	return f, rt, candidate
}

func scopeProcess(pid int, name, path string) runtime.PaneProcessObservation {
	state, group := runtime.ProcessCommand, pid
	if name == "zsh" {
		state = runtime.ProcessShell
		group = 10
		pid = 10
	}
	return runtime.PaneProcessObservation{State: state, ShellPID: 10, GroupID: group, Processes: []runtime.ForegroundProcess{{PID: pid, Name: name, CWD: path}}}
}

func TestDoneScopeParentAgentIsPreserved(t *testing.T) {
	f, rt, selected := newScopeFixture(t)
	rt.sessions = []runtime.Session{{Handle: "wH", WorkspaceIdentityObserved: true, WorkspaceCheckout: f.repo.Root, Panes: []runtime.Pane{{ID: "wH:p1", CWD: f.repo.Root, Agent: "codex", AgentStatus: "idle"}}}}
	rt.activities = []runtime.AgentActivity{{PaneID: "wH:p1", WorkspaceID: "wH", Agent: "codex", Status: "idle", CWD: f.repo.Root}}
	rt.processes["wH:p1"] = scopeProcess(20, "codex", f.repo.Root)
	before := f.repo.Git("rev-parse", "main")
	if err := runDoneForTest(f, "f\nq\n", true, "scope"); err != nil {
		t.Fatal(err)
	}
	if len(rt.closeCalls)+len(rt.closePaneCalls) != 0 || f.repo.Git("rev-parse", "main") != before {
		t.Fatal("parent cancel changed runtime or main")
	}
	stored, err := f.app.Tasks.Get(selected.ID)
	if err != nil || stored.State != task.Hot {
		t.Fatalf("task=%+v err=%v", stored, err)
	}
	if !strings.Contains(f.stdout.String(), "Parent checkout is preserved") || strings.Contains(f.stdout.String(), "Include closure") {
		t.Fatalf("output=%s", f.stdout.String())
	}
}

func TestDoneScopeFinalCancelDoesNotCloseTaskAgent(t *testing.T) {
	f, rt, selected := newScopeFixture(t)
	rt.sessions = []runtime.Session{{Handle: "wP", Panes: []runtime.Pane{{ID: "wP:p1", CWD: selected.WorktreePath, Agent: "codex", AgentStatus: "idle"}, {ID: "wP:p2", CWD: selected.WorktreePath}}}}
	rt.activities = []runtime.AgentActivity{{PaneID: "wP:p1", WorkspaceID: "wP", Agent: "codex", Status: "idle", CWD: selected.WorktreePath}}
	rt.processes["wP:p1"] = scopeProcess(20, "codex", selected.WorktreePath)
	rt.processes["wP:p2"] = scopeProcess(10, "zsh", selected.WorktreePath)
	if err := runDoneForTest(f, "f\ny\nn\n", true, "scope"); err != nil {
		t.Fatal(err)
	}
	if len(rt.closePaneCalls)+len(rt.closeCalls) != 0 {
		t.Fatal("cancel closed task agent")
	}
	if !strings.Contains(f.stdout.String(), "Canceled; nothing was changed") {
		t.Fatalf("output=%s", f.stdout.String())
	}
}

func TestDoneScopePostCloseFailureReportsCompletedClosure(t *testing.T) {
	f, rt, selected := newScopeFixture(t)
	rt.sessions = []runtime.Session{{Handle: "wP", Panes: []runtime.Pane{{ID: "wP:p1", CWD: selected.WorktreePath, Agent: "codex", AgentStatus: "idle"}, {ID: "wP:p2", CWD: selected.WorktreePath}}}}
	rt.activities = []runtime.AgentActivity{{PaneID: "wP:p1", WorkspaceID: "wP", Agent: "codex", Status: "idle", CWD: selected.WorktreePath}}
	rt.processes["wP:p1"] = scopeProcess(20, "codex", selected.WorktreePath)
	rt.processes["wP:p2"] = scopeProcess(10, "zsh", selected.WorktreePath)
	rt.closePaneHook = func() {
		if err := os.WriteFile(filepath.Join(selected.WorktreePath, "after-close.txt"), []byte("final writer output\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	before := f.repo.Git("rev-parse", "main")
	err := runDoneForTest(f, "f\ny\ny\n", true, "scope")
	if err == nil || len(rt.closePaneCalls) != 1 || f.repo.Git("rev-parse", "main") != before {
		t.Fatalf("err=%v closed=%v", err, rt.closePaneCalls)
	}
	if !strings.Contains(f.stdout.String(), "Herdr pane wP:p1 (task agent session ended)") || strings.Contains(f.stdout.String(), "nothing was changed") {
		t.Fatalf("incorrect partial output=%s", f.stdout.String())
	}
}

func TestDoneScopeConflictingTaskCannotAuthorizePaneClose(t *testing.T) {
	f, rt, selected := newScopeFixture(t)
	other := task.Task{Repo: selected.Repo, RepoPath: selected.RepoPath, Branch: "feat/other-owner", Base: "main", WorktreePath: selected.WorktreePath, Mode: task.ModeWorktree, State: task.Warm}
	if _, err := f.app.Tasks.Create(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	rt.sessions = []runtime.Session{{Handle: "wP", Panes: []runtime.Pane{{ID: "wP:p1", CWD: selected.WorktreePath, Agent: "codex", AgentStatus: "idle"}}}}
	rt.activities = []runtime.AgentActivity{{PaneID: "wP:p1", WorkspaceID: "wP", Agent: "codex", Status: "idle", CWD: selected.WorktreePath}}
	rt.processes["wP:p1"] = scopeProcess(20, "codex", selected.WorktreePath)
	err := runDoneForTest(f, "f\ny\ny\n", true, "scope")
	if err == nil || len(rt.closePaneCalls) != 0 {
		t.Fatalf("err=%v closed=%v", err, rt.closePaneCalls)
	}
}

func TestDoneScopeForegroundFFConsentKeepsPrograms(t *testing.T) {
	for _, choice := range []string{"y", "n"} {
		t.Run(choice, func(t *testing.T) {
			f, rt, _ := newScopeFixture(t)
			rt.sessions = []runtime.Session{{Handle: "wH", Panes: []runtime.Pane{{ID: "wH:p1", CWD: f.repo.Root}}}}
			rt.processes["wH:p1"] = scopeProcess(20, "nvim", f.repo.Root)
			before := f.repo.Git("rev-parse", "main")
			if err := runDoneForTest(f, "f\n"+choice+"\ny\nk\n", true, "scope"); err != nil {
				t.Fatal(err)
			}
			if len(rt.closePaneCalls)+len(rt.closeCalls) != 0 {
				t.Fatal("FF terminated a program")
			}
			if (f.repo.Git("rev-parse", "main") != before) != (choice == "y") {
				t.Fatal("FF ignored explicit program consent")
			}
			for _, want := range []string{"nvim", "PID 20", "programs will remain running", "Background jobs were not inspected"} {
				if !strings.Contains(f.stdout.String(), want) {
					t.Errorf("missing %s", want)
				}
			}
		})
	}
}

func TestDoneScopeYesDoesNotAuthorizeForegroundFF(t *testing.T) {
	f, rt, _ := newScopeFixture(t)
	rt.sessions = []runtime.Session{{Handle: "wH", Panes: []runtime.Pane{{ID: "wH:p1", CWD: f.repo.Root}}}}
	rt.processes["wH:p1"] = scopeProcess(20, "server", f.repo.Root)
	err := runDoneForTest(f, "", false, "scope", "--ff", "--yes")
	if err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("error=%v", err)
	}
}

func TestRetireScopeBothInteractiveEntrypoints(t *testing.T) {
	for _, entry := range []string{"done", "retire"} {
		t.Run(entry, func(t *testing.T) {
			f, rt, selected := newScopeFixture(t)
			f.repo.Git("merge", "--ff-only", selected.Branch)
			if entry == "retire" {
				selected.State = task.Done
				if err := f.app.Tasks.Save(selected); err != nil {
					t.Fatal(err)
				}
			}
			rt.currentPane = "outside:p1"
			t.Setenv("HERDR_WORKSPACE_ID", "outside")
			t.Setenv("HERDR_PANE_ID", "outside:p1")
			rt.sessions = []runtime.Session{{Handle: "wP", WorkspaceIdentityObserved: true, WorkspaceCheckout: selected.WorktreePath, WorkspaceLinked: true, Panes: []runtime.Pane{{ID: "wP:p1", TabID: "wP:t1", CWD: selected.WorktreePath}}}}
			rt.processes["wP:p1"] = scopeProcess(20, "server", selected.WorktreePath)
			var err error
			if entry == "done" {
				err = runDoneForTest(f, "y\nr\nCLOSE wP\ny\n", true, "scope")
			} else {
				f.app.In = strings.NewReader("CLOSE wP\ny\n")
				f.app.interactiveCheck = func() bool { return true }
				cmd := newRetireCmd(f.app)
				cmd.SilenceErrors = true
				cmd.SetArgs([]string{"scope"})
				err = cmd.Execute()
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(rt.closeCalls) != 1 || rt.closeCalls[0] != "wP" {
				t.Fatalf("closed=%v output=%s", rt.closeCalls, f.stdout.String())
			}
			if _, err := os.Stat(selected.WorktreePath); !os.IsNotExist(err) {
				t.Fatalf("checkout remains: %v", err)
			}
		})
	}
}

type scopeLineReader struct {
	lines []string
	index int
	hook  func(int)
}

func TestRetireScopeCallerTransitionAndReplacement(t *testing.T) {
	_, rt, selected := newScopeFixture(t)
	rt.sessions = []runtime.Session{{Handle: "wP", Panes: []runtime.Pane{{ID: "wP:p2", TabID: "wP:t2", TerminalID: "term-caller", CWD: selected.WorktreePath}}}}
	rt.processes["wP:p2"] = scopeProcess(os.Getpid(), "dev", selected.WorktreePath)
	preview, err := retire.InspectForExternalCoordinator(context.Background(), rt, selected.WorktreePath, retire.Options{})
	if err != nil {
		t.Fatal(err)
	}
	intent := retireHandoffIntent{Version: retireHandoffVersion}
	bindRetireCaller(&intent, preview)
	if intent.CallerPID != os.Getpid() || intent.CallerPaneID != "wP:p2" {
		t.Fatalf("caller not bound: %+v", intent)
	}
	expected := retireHandoffFingerprint(preview, intent.CallerPaneID)
	rt.currentPane = "outside:p1"
	t.Setenv("HERDR_WORKSPACE_ID", "outside")
	t.Setenv("HERDR_PANE_ID", "outside:p1")
	rt.processes["wP:p2"] = scopeProcess(10, "zsh", selected.WorktreePath)
	fresh, err := awaitRetireCaller(context.Background(), rt, selected.WorktreePath, intent)
	if err != nil || expected != retireHandoffFingerprint(fresh, intent.CallerPaneID) {
		t.Fatalf("valid shell transition: %v", err)
	}
	rt.processes["wP:p2"] = scopeProcess(70, "another-program", selected.WorktreePath)
	if _, err := awaitRetireCaller(context.Background(), rt, selected.WorktreePath, intent); err == nil {
		t.Fatal("new program inherited caller exemption")
	}
	if len(rt.closeCalls) != 0 {
		t.Fatal("caller validation closed a workspace")
	}
}

func TestDoneScopeProgramChangeDuringConsentIsStale(t *testing.T) {
	f, rt, _ := newScopeFixture(t)
	rt.sessions = []runtime.Session{{Handle: "wH", Panes: []runtime.Pane{{ID: "wH:p1", CWD: f.repo.Root}}}}
	rt.processes["wH:p1"] = scopeProcess(20, "nvim", f.repo.Root)
	before := f.repo.Git("rev-parse", "main")
	f.app.In = &scopeLineReader{lines: []string{"f\n", "y\n", "y\n"}, hook: func(i int) {
		if i == 2 {
			rt.processes["wH:p1"] = scopeProcess(21, "server", f.repo.Root)
		}
	}}
	f.app.interactiveCheck = func() bool { return true }
	cmd := newDoneCmd(f.app)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"scope"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "changed") || f.repo.Git("rev-parse", "main") != before {
		t.Fatalf("stale consent err=%v", err)
	}
}

func TestRetireScopeTaskChangeDuringFinalConfirmationIsStale(t *testing.T) {
	f, rt, selected := newScopeFixture(t)
	f.repo.Git("merge", "--ff-only", selected.Branch)
	selected.State = task.Done
	if err := f.app.Tasks.Save(selected); err != nil {
		t.Fatal(err)
	}
	rt.currentPane = "outside:p1"
	t.Setenv("HERDR_WORKSPACE_ID", "outside")
	t.Setenv("HERDR_PANE_ID", "outside:p1")
	rt.sessions = []runtime.Session{{Handle: "wP", Panes: []runtime.Pane{{ID: "wP:p1", CWD: selected.WorktreePath}}}}
	rt.processes["wP:p1"] = scopeProcess(20, "server", selected.WorktreePath)
	f.app.In = &scopeLineReader{lines: []string{"CLOSE wP\n", "y\n"}, hook: func(i int) {
		if i == 2 {
			selected.Next = "changed while confirmation was open"
			if err := f.app.Tasks.Save(selected); err != nil {
				t.Error(err)
			}
		}
	}}
	f.app.interactiveCheck = func() bool { return true }
	cmd := newRetireCmd(f.app)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"scope"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "stale") || len(rt.closeCalls) != 0 {
		t.Fatalf("err=%v closed=%v", err, rt.closeCalls)
	}
	if _, err := os.Stat(selected.WorktreePath); err != nil {
		t.Fatal("stale preview removed checkout")
	}
}

func (r *scopeLineReader) Read(p []byte) (int, error) {
	if r.index >= len(r.lines) {
		return 0, io.EOF
	}
	line := r.lines[r.index]
	r.index++
	if r.hook != nil {
		r.hook(r.index)
	}
	return copy(p, line), nil
}

func TestRetireScopeChangedProgramCannotInheritConsent(t *testing.T) {
	f, rt, selected := newScopeFixture(t)
	rt.currentPane = "outside:p1"
	t.Setenv("HERDR_PANE_ID", "outside:p1")
	t.Setenv("HERDR_WORKSPACE_ID", "outside")
	rt.sessions = []runtime.Session{{Handle: "wP", Panes: []runtime.Pane{{ID: "wP:p1", CWD: selected.WorktreePath}}}}
	rt.processes["wP:p1"] = scopeProcess(20, "server", selected.WorktreePath)
	preview, err := retire.InspectForExternalCoordinator(context.Background(), rt, selected.WorktreePath, retire.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.app.In = &scopeLineReader{lines: []string{"CLOSE wP\n", "y\n"}, hook: func(i int) {
		if i == 1 {
			rt.processes["wP:p1"] = scopeProcess(21, "new-server", selected.WorktreePath)
		}
	}}
	_, _, _, err = confirmRetirement(context.Background(), f.app, newPrompter(f.app), rt, preview, flow.RetireOptions{})
	if err == nil || !strings.Contains(err.Error(), "stale") || len(rt.closeCalls) != 0 {
		t.Fatalf("error=%v closed=%v", err, rt.closeCalls)
	}
}
