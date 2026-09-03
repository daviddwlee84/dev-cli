package retire_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

type fakeRuntime struct {
	sessions        []runtime.Session
	listErr         error
	closeErr        error
	closed          []string
	pollsAfterClose int
}

type sequenceRuntime struct {
	lists  [][]runtime.Session
	closed []string
}

type activityFakeRuntime struct {
	*fakeRuntime
	activities  []runtime.AgentActivity
	activityErr error
}

func (r *activityFakeRuntime) AgentActivities(context.Context) ([]runtime.AgentActivity, error) {
	return r.activities, r.activityErr
}

type currentPaneFakeRuntime struct {
	*fakeRuntime
	currentPane  string
	currentErr   error
	currentCalls int
}

func (r *currentPaneFakeRuntime) CurrentPaneID(context.Context) (string, error) {
	r.currentCalls++
	return r.currentPane, r.currentErr
}

func (s *sequenceRuntime) Name() string    { return "sequence" }
func (s *sequenceRuntime) Available() bool { return true }
func (s *sequenceRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (s *sequenceRuntime) Close(_ context.Context, handle string) error {
	s.closed = append(s.closed, handle)
	return nil
}
func (s *sequenceRuntime) List(context.Context) ([]runtime.Session, error) {
	if len(s.lists) == 0 {
		return nil, nil
	}
	current := s.lists[0]
	if len(s.lists) > 1 {
		s.lists = s.lists[1:]
	}
	return current, nil
}
func (s *sequenceRuntime) Annotate(context.Context, string, map[string]string) error { return nil }

func (f *fakeRuntime) Name() string    { return "fake" }
func (f *fakeRuntime) Available() bool { return true }
func (f *fakeRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (f *fakeRuntime) Close(_ context.Context, handle string) error {
	f.closed = append(f.closed, handle)
	return f.closeErr
}
func (f *fakeRuntime) List(context.Context) ([]runtime.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.closed) > 0 {
		if f.pollsAfterClose > 0 {
			f.pollsAfterClose--
			return f.sessions, nil
		}
		return nil, nil
	}
	return f.sessions, nil
}
func (f *fakeRuntime) Annotate(context.Context, string, map[string]string) error { return nil }

func TestInspectRefusesCallerInsideEvenWithAcknowledgements(t *testing.T) {
	target := t.TempDir()
	inside := filepath.Join(target, "src")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	inspection, err := retire.Inspect(context.Background(), &fakeRuntime{}, target, retire.Options{
		CWD: inside, CloseUnknown: true, AssumeNoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Ready() || !contains(inspection.Blockers, "caller is inside") {
		t.Fatalf("caller containment should block: %+v", inspection)
	}
}

func TestInspectRefusesCallerRuntimeAndMixedWorkspace(t *testing.T) {
	target := t.TempDir()
	other := t.TempDir()
	rt := &fakeRuntime{sessions: []runtime.Session{{
		Handle: "w2", AgentStatus: "idle",
		Panes: []runtime.Pane{{ID: "w2:p1", CWD: target, Agent: "codex", AgentStatus: "idle"}, {ID: "w2:p2", CWD: other}},
	}}}
	inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), CallerWorkspaceID: "w2", CloseUnknown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Ready() || !contains(inspection.Blockers, "caller runtime") || !contains(inspection.Blockers, "outside the target") {
		t.Fatalf("caller and mixed workspace should block: %+v", inspection)
	}
}

func TestInspectResolvesMovedCallerPaneAndFailsClosedOnResolutionError(t *testing.T) {
	target := t.TempDir()
	rt := &currentPaneFakeRuntime{
		fakeRuntime: runtimeAt(target, "idle"),
		currentPane: "w1:p1",
	}
	inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), CallerWorkspaceID: "old-workspace", CallerPaneID: "old:pane",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rt.currentCalls != 1 || inspection.Ready() || !inspection.CallerContained || !contains(inspection.Blockers, "caller runtime") {
		t.Fatalf("resolved caller pane must block cleanup: calls=%d inspection=%+v", rt.currentCalls, inspection)
	}

	rt.currentErr = errors.New("current pane unavailable")
	if _, err := retire.Inspect(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), CallerPaneID: "old:pane", AssumeNoRuntime: true,
	}); err == nil || !strings.Contains(err.Error(), "current pane unavailable") {
		t.Fatalf("current-pane resolution failure must not be acknowledged as a list failure: %v", err)
	}
}

func TestInspectKeepsShellCWDWhenForegroundMovesOutside(t *testing.T) {
	target := t.TempDir()
	other := t.TempDir()
	rt := &fakeRuntime{sessions: []runtime.Session{{
		Handle: "w3", AgentStatus: "idle",
		Panes: []runtime.Pane{{ID: "w3:p1", CWD: other, ShellCWD: target, Agent: "codex", AgentStatus: "idle"}},
	}}}
	inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Ready() || len(inspection.Sessions) != 1 || !contains(inspection.Blockers, "outside the target") {
		t.Fatalf("shell cwd plus external foreground must be mixed and covering: %+v", inspection)
	}
}

func TestInspectAgentStatusPolicy(t *testing.T) {
	target := t.TempDir()
	for _, status := range []string{"working", "blocked", "waiting"} {
		t.Run(status, func(t *testing.T) {
			rt := runtimeAt(target, status)
			inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir(), CloseUnknown: true})
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Ready() || !contains(inspection.Blockers, status) {
				t.Fatalf("%s should block: %+v", status, inspection)
			}
		})
	}
	unknown := runtimeAt(target, "unknown")
	blocked, err := retire.Inspect(context.Background(), unknown, target, retire.Options{CWD: t.TempDir()})
	if err != nil || blocked.Ready() {
		t.Fatalf("unknown should fail closed: %+v, %v", blocked, err)
	}
	allowed, err := retire.Inspect(context.Background(), unknown, target, retire.Options{CWD: t.TempDir(), CloseUnknown: true})
	if err != nil || !allowed.Ready() {
		t.Fatalf("external acknowledgement should allow unknown: %+v, %v", allowed, err)
	}
}

func TestInspectAgentActivityUsesCleanupStatusMatrix(t *testing.T) {
	target := t.TempDir()
	for _, tc := range []struct {
		status       string
		closeUnknown bool
		wantReady    bool
	}{
		{status: "working"},
		{status: "blocked"},
		{status: "waiting"},
		{status: "idle", wantReady: true},
		{status: "done", wantReady: true},
		{status: "unknown"},
		{status: "unknown", closeUnknown: true, wantReady: true},
		{status: "paused"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			base := runtimeAt(target, "idle")
			rt := &activityFakeRuntime{
				fakeRuntime: base,
				activities: []runtime.AgentActivity{{
					PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude", Status: tc.status, CWD: target,
				}},
			}
			inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{
				CWD: t.TempDir(), CloseUnknown: tc.closeUnknown,
			})
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Ready() != tc.wantReady {
				t.Fatalf("cleanup status %q ready = %v, want %v: %+v", tc.status, inspection.Ready(), tc.wantReady, inspection)
			}
		})
	}
}

func TestInspectAgentActivityFailureAndUncorrelatedAgentFailClosed(t *testing.T) {
	target := t.TempDir()
	activityErr := errors.New("agent inventory unavailable")
	rt := &activityFakeRuntime{fakeRuntime: runtimeAt(target, "idle"), activityErr: activityErr}
	if _, err := retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir()}); err == nil || !strings.Contains(err.Error(), activityErr.Error()) {
		t.Fatalf("agent activity failure must fail closed: %v", err)
	}

	rt.activityErr = nil
	rt.activities = []runtime.AgentActivity{{
		PaneID: "missing:p1", WorkspaceID: "missing", Agent: "claude", Status: "idle", CWD: target,
	}}
	inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Ready() || !contains(inspection.Blockers, "without a closeable runtime session") {
		t.Fatalf("uncorrelated idle agent cannot be closed safely: %+v", inspection)
	}
}

func TestCloseAndWaitPollsUntilReleased(t *testing.T) {
	target := t.TempDir()
	rt := runtimeAt(target, "idle")
	rt.pollsAfterClose = 2
	inspection, err := retire.CloseAndWait(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), PollInterval: time.Millisecond, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.closed) != 1 || rt.closed[0] != "w1" || len(inspection.Sessions) != 0 {
		t.Fatalf("close result: closed=%v inspection=%+v", rt.closed, inspection)
	}
}

func TestCloseAndWaitFailureNeverClaimsRelease(t *testing.T) {
	target := t.TempDir()
	rt := runtimeAt(target, "idle")
	rt.closeErr = errors.New("refused")
	if _, err := retire.CloseAndWait(context.Background(), rt, target, retire.Options{CWD: t.TempDir()}); err == nil {
		t.Fatal("close failure should stop retirement")
	}

	rt = runtimeAt(target, "idle")
	rt.pollsAfterClose = 100
	if _, err := retire.CloseAndWait(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), PollInterval: time.Millisecond, Timeout: 3 * time.Millisecond,
	}); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("persistent session should time out, got %v", err)
	}
}

func TestCloseAndWaitReinspectsBeforeClosing(t *testing.T) {
	target := t.TempDir()
	idle := runtimeAt(target, "idle").sessions
	working := runtimeAt(target, "working").sessions
	rt := &sequenceRuntime{lists: [][]runtime.Session{idle, working}}
	if _, err := retire.CloseAndWait(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), PollInterval: time.Millisecond, Timeout: time.Second,
	}); err == nil || !strings.Contains(err.Error(), "state changed") {
		t.Fatalf("fresh working state should block close: %v", err)
	}
	if len(rt.closed) != 0 {
		t.Fatalf("stale idle inspection closed runtime: %v", rt.closed)
	}
}

func TestInspectRuntimeFailureRequiresAcknowledgement(t *testing.T) {
	target := t.TempDir()
	rt := &fakeRuntime{listErr: errors.New("offline")}
	if _, err := retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir()}); err == nil {
		t.Fatal("runtime inventory failure should fail closed")
	}
	inspection, err := retire.Inspect(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), AssumeNoRuntime: true,
	})
	if err != nil || !inspection.RuntimeUnknown || !inspection.Ready() {
		t.Fatalf("explicit external acknowledgement should proceed: %+v, %v", inspection, err)
	}

	active := &activityFakeRuntime{
		fakeRuntime: &fakeRuntime{listErr: errors.New("offline")},
		activities: []runtime.AgentActivity{{
			PaneID: "p-active", WorkspaceID: "w-active", Agent: "claude", Status: "working", CWD: target,
		}},
	}
	inspection, err = retire.Inspect(context.Background(), active, target, retire.Options{
		CWD: t.TempDir(), AssumeNoRuntime: true,
	})
	if err != nil || !inspection.RuntimeUnknown || inspection.Ready() ||
		!contains(inspection.Blockers, "recognized agent") {
		t.Fatalf("assume-no-runtime ignored independent active-agent evidence: %+v, %v", inspection, err)
	}

	rt = &fakeRuntime{sessions: []runtime.Session{{
		Handle: "w1", Panes: []runtime.Pane{{ID: "w1:p1", CWD: "\x00"}},
	}}}
	if _, err := retire.Inspect(context.Background(), rt, target, retire.Options{
		CWD: t.TempDir(), AssumeNoRuntime: true,
	}); err == nil {
		t.Fatal("assume-no-runtime must not bypass a canonical coverage failure")
	}
}

func TestInspectRuntimeNoneRequiresExplicitAcknowledgement(t *testing.T) {
	target := t.TempDir()
	inspection, err := retire.Inspect(context.Background(), runtime.None{}, target, retire.Options{CWD: t.TempDir()})
	if err != nil || !inspection.RuntimeUnknown || inspection.Ready() || !contains(inspection.Blockers, "cannot enumerate") {
		t.Fatalf("runtime none default=%+v err=%v", inspection, err)
	}
	inspection, err = retire.Inspect(context.Background(), runtime.None{}, target, retire.Options{
		CWD: t.TempDir(), AssumeNoRuntime: true,
	})
	if err != nil || !inspection.RuntimeUnknown || !inspection.Ready() {
		t.Fatalf("runtime none acknowledgement=%+v err=%v", inspection, err)
	}
}

func runtimeAt(path, status string) *fakeRuntime {
	return &fakeRuntime{sessions: []runtime.Session{{
		Handle: "w1", AgentStatus: status,
		Panes: []runtime.Pane{{ID: "w1:p1", CWD: path, Agent: "claude", AgentStatus: status}},
	}}}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
