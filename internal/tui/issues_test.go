package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tuiissue"
)

func TestIssuesAccessibleInAllEmptyViews(t *testing.T) {
	for _, view := range Views {
		t.Run(view.String(), func(t *testing.T) {
			m := New(Actions{}, nil, nil)
			m.view = view
			m.err = errors.New("test diagnostic")
			menu := m.openActionMenu()
			index := -1
			for i := 0; i < menu.overlay.optionCount; i++ {
				if menu.overlay.options[i].action == listActionIssues {
					index = i
				}
			}
			if index < 0 {
				t.Fatal("no issues action on empty view")
			}
			menu.overlay.optionIndex = index
			next, _ := menu.runOverlayAction()
			m = next.(Model)
			if len(m.issues.snapshot) == 0 {
				t.Fatal("error was lost when action menu opened")
			}
			m = m.openIssueDetails(m.issues.snapshot[0].ID)
			if !strings.Contains(m.overlay.body, "test diagnostic") || !strings.Contains(strings.ToLower(m.overlay.body), "recheck") {
				t.Fatal(m.overlay.body)
			}
		})
	}
}

func TestIssuesRetainFinalBatchFailuresWithoutProgress(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	m.sshUI.generation = 4
	failure := &machineregistry.PathError{Path: "/state/registry", Reason: "untrusted owner", Owner: "42"}
	message := sshEventMsg{generation: 4, kind: "tested", testResult: SSHTestResult{Completed: 1, Outcomes: []SSHTestProgress{{ProfileID: "profile", Err: failure}}}}
	before := m
	m.captureIssueResult(before, message)
	issues := m.collectedIssues(false)
	if len(issues) != 1 || issues[0].Code != "registry-path" {
		t.Fatalf("final result lost guided recovery: %+v", issues)
	}
}
func TestIssueScopeAndStableIdentity(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	m.rememberIssue(tuiissue.New("tasks", "notes", "task", "read", "note unavailable"))
	m.viewErrors[ViewSSH] = &machineregistry.PathError{Path: "/state/machines", Reason: "wrong owner"}
	if got := m.collectedIssues(false); len(got) != 1 || got[0].Code != "registry-path" {
		t.Fatalf("%+v", got)
	}
	next, _ := m.openIssues(true)
	m = next.(Model)
	if len(m.issues.snapshot) != 2 {
		t.Fatalf("%+v", m.issues.snapshot)
	}
	original := m
	m.rememberIssue(tuiissue.New("tasks", "notes", "task", "read", "new details"))
	if original.issues.history[0].Detail != "note unavailable" {
		t.Fatal("value-copied history changed")
	}
}
func TestIssueInspectionIgnoresClosedOrOldMenu(t *testing.T) {
	m := New(Actions{Issues: IssueActions{Inspect: func(context.Context, View) []tuiissue.Issue {
		return []tuiissue.Issue{tuiissue.MissingDependency("ssh", "ssh")}
	}}}, nil, nil)
	m.view = ViewSSH
	next, command := m.openIssues(false)
	m = next.(Model)
	message := command()
	m.overlay = overlayState{}
	next, _, handled := m.updateIssues(message)
	m = next.(Model)
	if !handled || m.overlay.kind != overlayNone || len(m.issues.snapshot) != 0 {
		t.Fatal("late results reopened or replaced menu")
	}
}
func TestIssueListPaginatesAndRetainsFullDiagnostic(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	for i := 0; i < 155; i++ {
		m.rememberIssue(tuiissue.New("ssh", "observation", fmt.Sprint(i), "failed", fmt.Sprint(i)+strings.Repeat(" detail", 100)))
	}
	next, _ := m.openIssues(false)
	m = next.(Model)
	if m.overlay.optionCount > 80 || len(m.issues.snapshot) != 155 {
		t.Fatal("issues truncated")
	}
	next, _ = m.runIssueAction(listActionIssuesNext, actionOption{})
	m = next.(Model)
	if m.issues.page != 1 || !strings.Contains(m.overlay.options[2].label, "70") {
		t.Fatalf("page=%d %+v", m.issues.page, m.overlay.options[2])
	}
	issue := m.issues.snapshot[70]
	m = m.openIssueDetails(issue.ID)
	next, _ = m.runIssueAction(listActionIssueFull, actionOption{})
	m = next.(Model)
	if !strings.Contains(m.overlay.body, issue.Detail) {
		t.Fatal("detail truncated")
	}
}

type testRecoveryCommand struct{ runs *int }

func (c testRecoveryCommand) Run() error        { *c.runs++; return nil }
func (testRecoveryCommand) SetStdin(io.Reader)  {}
func (testRecoveryCommand) SetStdout(io.Writer) {}
func (testRecoveryCommand) SetStderr(io.Writer) {}
func TestIssuePreparationNeverExecutesAndCancellationDropsAuthority(t *testing.T) {
	runs := 0
	m := New(Actions{Issues: IssueActions{Prepare: func(context.Context, tuiissue.Issue, tuiissue.Action) (*IssueExecution, error) {
		return &IssueExecution{Preview: "chmod exact reviewed path", Command: testRecoveryCommand{&runs}}, nil
	}}}, nil, nil)
	m.view = ViewSSH
	issue := tuiissue.FromError("ssh", "registry", "", &machineregistry.PathError{Path: "/state"})
	m.issues.snapshot = []tuiissue.Issue{issue}
	m.issues.selected = issue.ID
	next, prepare := m.runIssueAction(listActionIssueAction, actionOption{issueAction: issue.Actions[1]})
	m = next.(Model)
	prepared := prepare()
	next, _, _ = m.updateIssues(prepared)
	m = next.(Model)
	if runs != 0 || m.overlay.title != "Review recovery action" {
		t.Fatal("preparation executed or omitted review")
	}
	// Esc does not confirm or execute. A stale completion cannot reopen it.
	next, _ = m.updateOverlay(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if runs != 0 || m.overlay.kind != overlayNone {
		t.Fatal("cancel ran recovery")
	}
}
func TestIssueRecheckNeverRepeatsPartialMutationOrQueriesRemote(t *testing.T) {
	queries := 0
	cache := 0
	m := New(Actions{ReloadRemote: func(context.Context) ([]RemoteRow, error) { queries++; return nil, nil }, LoadRemoteCache: func(context.Context) RemoteCacheResult { cache++; return RemoteCacheResult{} }}, nil, nil)
	m.view = ViewRemote
	next, command := m.recheckIssueView("remote")
	m = next.(Model)
	if command != nil {
		command()
	}
	if queries != 0 || cache != 1 {
		t.Fatalf("network=%d cache=%d", queries, cache)
	}
}

func TestIssueRecoverySelectsExactTaskAndRejectsChangedRevision(t *testing.T) {
	request := WorkflowRequest{}
	first := &task.Task{ID: "first", Name: "first", Repo: "repo", RepoPath: "/repo", State: task.Hot}
	second := &task.Task{ID: "second", Name: "second", Repo: "repo", RepoPath: "/repo", State: task.Hot}
	m := New(Actions{Workflow: func(_ context.Context, r WorkflowRequest) (Workflow, error) {
		request = r
		return nil, errors.New("workflow fixture")
	}}, []inventory.Row{{Task: first, WorktreeMissing: true}, {Task: second, WorktreeMissing: true}}, nil)
	m.view = ViewTasks
	issue := m.bindIssueTarget(tuiissue.New("tasks", "task lifecycle", second.ID, "checkout-unavailable", "missing"))
	m.filter = "first"
	_, _ = m.runIssueSourceAction(issue, tuiissue.TaskRecovery)
	if request.Action != "sweep" || request.Task != second {
		t.Fatalf("wrong task: %+v", request)
	}
	replacement := *second
	replacement.Name = "renamed"
	m.rows[1].Task = &replacement
	request = WorkflowRequest{}
	next, _ := m.runIssueSourceAction(issue, tuiissue.TaskRecovery)
	if request.Action != "" || next.(Model).err == nil {
		t.Fatal("changed task reached workflow")
	}
}

func TestNativeInventoryIssueDetailsClearAfterSuccessfulReload(t *testing.T) {
	diagnostic := tuiissue.New("mcp", "native inventory", "/config/mcp.json", "invalid_json", "/config/mcp.json\ninvalid JSON")
	m := New(Actions{ReloadMCP: func(context.Context, CapabilityScope) ([]agentmcp.Declaration, error) {
		return []agentmcp.Declaration{}, LoadWarning{Message: "one source failed", Issues: []tuiissue.Issue{diagnostic}}
	}}, nil, nil)
	m.view = ViewMCP
	m.beginViewLoad(ViewMCP, loadRefresh)
	msg := m.reloadMCP()()
	next, _ := m.Update(msg)
	m = next.(Model)
	issues := m.collectedIssues(false)
	if len(issues) != 1 || issues[0].Code != "invalid_json" || !strings.Contains(issues[0].Detail, "/config/mcp.json") {
		t.Fatalf("%+v", issues)
	}
	next, _ = m.Update(mcpMsg{generation: m.viewLoad(ViewMCP).generation, rows: []agentmcp.Declaration{}, valid: true})
	m = next.(Model)
	if got := m.collectedIssues(false); len(got) != 0 {
		t.Fatalf("resolved issue retained: %+v", got)
	}
}

func TestSSHDialogFailureRetainsTypedGuidanceAfterClose(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	m.sshUI.generation = 7
	failure := &machineregistry.PathError{Path: "/state/registry", Reason: "untrusted owner", Owner: "42"}
	msg := sshEventMsg{generation: 7, kind: "prepared", err: failure}
	before := m
	m.captureIssueResult(before, msg)
	m.sshUI.dialog = sshDialog{}
	issues := m.collectedIssues(false)
	if len(issues) != 1 || issues[0].Code != "registry-path" || issues[0].Actions[1].ID != tuiissue.RegistryPermissions {
		t.Fatalf("%+v", issues)
	}
	m.captureIssueResult(before, sshEventMsg{generation: 6, kind: "tested", err: errors.New("stale")})
	if len(m.issues.history) != 1 {
		t.Fatal("stale dialog failure was retained")
	}
}
