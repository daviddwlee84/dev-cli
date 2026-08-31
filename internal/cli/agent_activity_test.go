package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type activityRuntime struct {
	name              string
	activities        []runtime.AgentActivity
	activityErr       error
	activityCalls     int
	openResult        runtime.OpenResult
	openErr           error
	openCalls         int
	openLabels        []string
	sessions          []runtime.Session
	listErr           error
	closeErr          error
	closeCalls        []string
	activateErr       error
	activateCalls     []string
	runErr            error
	runCalls          []paneRunCall
	events            []string
	annotationHandles []string
	annotations       []map[string]string
}

func (r *activityRuntime) Name() string {
	if r.name != "" {
		return r.name
	}
	return "herdr"
}
func (r *activityRuntime) Available() bool { return true }
func (r *activityRuntime) Open(_ context.Context, _, label string) (runtime.OpenResult, error) {
	r.openCalls++
	r.openLabels = append(r.openLabels, label)
	return r.openResult, r.openErr
}
func (r *activityRuntime) OpenWorktree(_ context.Context, _, label string) (runtime.OpenResult, error) {
	r.openCalls++
	r.openLabels = append(r.openLabels, label)
	return r.openResult, r.openErr
}
func (r *activityRuntime) Close(_ context.Context, handle string) error {
	r.closeCalls = append(r.closeCalls, handle)
	return r.closeErr
}
func (r *activityRuntime) Activate(_ context.Context, handle string) error {
	r.activateCalls = append(r.activateCalls, handle)
	r.events = append(r.events, "activate")
	return r.activateErr
}
func (r *activityRuntime) RunInPane(_ context.Context, paneID, command string) error {
	r.runCalls = append(r.runCalls, paneRunCall{PaneID: paneID, Command: command})
	r.events = append(r.events, "run")
	return r.runErr
}
func (r *activityRuntime) List(context.Context) ([]runtime.Session, error) {
	return r.sessions, r.listErr
}
func (r *activityRuntime) Annotate(_ context.Context, handle string, kv map[string]string) error {
	r.annotationHandles = append(r.annotationHandles, handle)
	copy := make(map[string]string, len(kv))
	for k, v := range kv {
		copy[k] = v
	}
	r.annotations = append(r.annotations, copy)
	return nil
}
func (r *activityRuntime) AgentActivities(context.Context) ([]runtime.AgentActivity, error) {
	r.activityCalls++
	return r.activities, r.activityErr
}

type paneRunCall struct {
	PaneID  string
	Command string
}

type currentPaneRuntime struct {
	*activityRuntime
	currentPane  string
	currentErr   error
	currentCalls int
}

func (r *currentPaneRuntime) CurrentPaneID(context.Context) (string, error) {
	r.currentCalls++
	return r.currentPane, r.currentErr
}

func TestCheckoutAgentActivitiesUsesCanonicalWorktreeRoots(t *testing.T) {
	r := gittest.New(t)
	linked := filepath.Join(t.TempDir(), "linked")
	r.Git("branch", "feat/other")
	r.Git("worktree", "add", linked, "feat/other")
	other := gittest.New(t)

	rt := &activityRuntime{activities: []runtime.AgentActivity{
		{PaneID: "w1:p1", Agent: "claude", Status: "working", CWD: r.Root},
		{PaneID: "w1:p2", Agent: "claude", Status: "idle", CWD: r.Root},
		{PaneID: "w1:p3", Agent: "codex", Status: "done", CWD: filepath.Join(r.Root, "nested")},
		{PaneID: "w1:p4", Agent: "claude", Status: "unknown", CWD: linked},
		{PaneID: "w2:p1", Agent: "claude", Status: "blocked", CWD: other.Root},
	}}
	if err := os.MkdirAll(filepath.Join(r.Root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := checkoutAgentActivities(context.Background(), rt, r.Root, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].PaneID != "w1:p2" || got[1].PaneID != "w1:p3" {
		t.Fatalf("activities = %+v; want idle and done agents in the same worktree only", got)
	}
}

func TestGuardSharedCheckoutFailsClosedAndOverrideBypasses(t *testing.T) {
	r := gittest.New(t)
	rt := &activityRuntime{activities: []runtime.AgentActivity{{
		PaneID: "w1:p2", Agent: "claude", Name: "reviewer", Status: "unknown", CWD: r.Root,
	}}}
	app := &App{}
	if err := guardSharedCheckout(context.Background(), app, rt, r.Root); err == nil ||
		!strings.Contains(err.Error(), "reviewer (unknown, pane w1:p2)") {
		t.Fatalf("collision error = %v", err)
	}

	app.allowSharedCheckout = true
	calls := rt.activityCalls
	if err := guardSharedCheckout(context.Background(), app, rt, r.Root); err != nil {
		t.Fatalf("explicit override should allow the checkout: %v", err)
	}
	if rt.activityCalls != calls {
		t.Fatal("override should not query agent activity")
	}

	app.allowSharedCheckout = false
	rt.activities = nil
	rt.activityErr = errors.New("agent inventory unavailable")
	if err := guardSharedCheckout(context.Background(), app, rt, r.Root); err == nil ||
		!strings.Contains(err.Error(), "agent inventory unavailable") {
		t.Fatalf("activity lookup must fail closed: %v", err)
	}
}

func TestGuardResolvesMovedCallerPaneBeforeExclusion(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p-old")
	r := gittest.New(t)
	rt := &currentPaneRuntime{
		activityRuntime: &activityRuntime{activities: []runtime.AgentActivity{{
			PaneID: "w2:p-new", Agent: "claude", Status: "working", CWD: r.Root,
		}}},
		currentPane: "w2:p-new",
	}
	if err := guardSharedCheckout(context.Background(), &App{}, rt, r.Root); err != nil {
		t.Fatalf("moved caller's current pane should be excluded: %v", err)
	}
	if rt.currentCalls != 1 {
		t.Fatalf("current pane resolution calls = %d", rt.currentCalls)
	}

	rt.currentErr = errors.New("current pane unavailable")
	if err := guardSharedCheckout(context.Background(), &App{}, rt, r.Root); err == nil || !strings.Contains(err.Error(), "current pane unavailable") {
		t.Fatalf("current-pane lookup must fail closed: %v", err)
	}
}

func TestGuardTreatsEveryRecognizedAgentStateAsOccupied(t *testing.T) {
	r := gittest.New(t)
	for _, state := range []string{"working", "blocked", "idle", "done", "unknown"} {
		t.Run(state, func(t *testing.T) {
			rt := &activityRuntime{activities: []runtime.AgentActivity{{
				PaneID: "w1:p2", Agent: "claude", Status: state, CWD: r.Root,
			}}}
			if err := guardSharedCheckout(context.Background(), &App{}, rt, r.Root); err == nil {
				t.Fatalf("state %s was treated as available", state)
			}
		})
	}
}

func TestResumeExistingCheckoutRejectsAnotherAgent(t *testing.T) {
	r := gittest.New(t)
	rt := &activityRuntime{activities: []runtime.AgentActivity{{
		PaneID: "w1:p2", Agent: "claude", Status: "idle", CWD: r.Root,
	}}}
	var out, errOut bytes.Buffer
	store := task.NewStore(t.TempDir())
	if err := store.Save(&task.Task{
		Name: "direct", Repo: "repo", RepoPath: r.Root, Branch: "main", Base: "main",
		Mode: task.ModeDirect, State: task.Warm, Owner: config.Hostname(),
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Default(), Tasks: store, Out: &out, Err: &errOut, runtimeInstance: rt}
	cmd := newResumeCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"direct", "--fetch=false"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "already occupied") {
		t.Fatalf("resume collision error = %v", err)
	}
	if rt.openCalls != 0 {
		t.Fatal("resume opened a runtime after collision")
	}
}

func TestOccupiedRepoOpenNavigatesWithoutWriterGuard(t *testing.T) {
	r := gittest.New(t)
	rt := &activityRuntime{
		activities: []runtime.AgentActivity{{
			PaneID: "w1:p2", Agent: "claude", Status: "working", CWD: r.Root,
		}},
		openResult: runtime.OpenResult{Handle: "w1", Surface: "workspace", Opened: true},
	}
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{filepath.Dir(r.Root)}
	var out, errOut bytes.Buffer
	app := &App{Cfg: cfg, Tasks: task.NewStore(t.TempDir()), Out: &out, Err: &errOut, runtimeInstance: rt}
	cmd := newRepoOpenCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"repo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("occupied repo open should navigate to the owner: %v", err)
	}
	if rt.activityCalls != 0 || rt.openCalls != 1 || !reflect.DeepEqual(rt.activateCalls, []string{"w1"}) {
		t.Fatalf("repo open should bypass writer guard, open, and activate: activity=%d open=%d activate=%v", rt.activityCalls, rt.openCalls, rt.activateCalls)
	}
	if !strings.Contains(out.String(), "herdr w1") {
		t.Fatalf("repo open did not report reused workspace: %q", out.String())
	}
}

func TestStatusShowsEveryRecognizedAgentState(t *testing.T) {
	r := gittest.New(t)
	rt := &activityRuntime{activities: []runtime.AgentActivity{
		{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude", Name: "author", Status: "working", CWD: r.Root},
		{PaneID: "w1:p2", WorkspaceID: "w1", Agent: "codex", Status: "done", CWD: r.Root},
	}}
	var out, errOut bytes.Buffer
	app := &App{
		Cfg: config.Default(), Tasks: task.NewStore(t.TempDir()),
		Out: &out, Err: &errOut, runtimeInstance: rt,
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(r.Root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cmd := newStatusCmd(app)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"activity   author:working w1:p1 (w1)",
		"activity   codex:done w1:p2 (w1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
}
