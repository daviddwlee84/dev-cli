package retire_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

type processRuntime struct {
	*fakeRuntime
	process runtime.PaneProcessObservation
	err     error
}

func (r *processRuntime) InspectPaneProcesses(_ context.Context, id string) (runtime.PaneProcessObservation, error) {
	p := r.process
	p.PaneID = id
	return p, r.err
}

func TestRetireProgramsRequireExactConsentAndKeepParent(t *testing.T) {
	ctx := context.Background()
	target := t.TempDir()
	rt := &processRuntime{fakeRuntime: &fakeRuntime{sessions: []runtime.Session{{Handle: "w1", Panes: []runtime.Pane{{ID: "w1:p1", CWD: target}}}}}, process: runtime.PaneProcessObservation{State: runtime.ProcessCommand, ShellPID: 10, GroupID: 20, Processes: []runtime.ForegroundProcess{{PID: 20, Name: "nvim", CWD: target}}}}
	opts := retire.Options{CWD: t.TempDir(), CloseUnknown: true}
	preview, err := retire.Inspect(ctx, rt, target, opts)
	if err != nil || preview.Ready() || len(preview.ProcessSessions) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	opts.ProcessClosures = retire.ProcessClosuresFor(preview.Sessions[0])
	ready, err := retire.Inspect(ctx, rt, target, opts)
	if err != nil || !ready.Ready() {
		t.Fatalf("approved=%+v err=%v", ready, err)
	}
	rt.process.Processes = []runtime.ForegroundProcess{{PID: 21, Name: "server", CWD: target}}
	changed, err := retire.Inspect(ctx, rt, target, opts)
	if err != nil || changed.Ready() {
		t.Fatalf("new program inherited consent: %+v err=%v", changed, err)
	}
	rt.fakeRuntime.sessions[0].WorkspaceIdentityObserved = true
	rt.fakeRuntime.sessions[0].WorkspaceCheckout = target
	parent, err := retire.Inspect(ctx, rt, target, opts)
	if err != nil || parent.Ready() || !strings.Contains(strings.Join(parent.Blockers, " "), "parent/canonical") {
		t.Fatalf("parent=%+v err=%v", parent, err)
	}
}

func TestRetireForegroundShellAndUnknownAreDifferent(t *testing.T) {
	target := t.TempDir()
	rt := &processRuntime{fakeRuntime: &fakeRuntime{sessions: []runtime.Session{{Handle: "w1", AgentStatus: "unknown", Panes: []runtime.Pane{{ID: "w1:p1", CWD: target}}}}}, process: runtime.PaneProcessObservation{State: runtime.ProcessShell, ShellPID: 10, GroupID: 10, Processes: []runtime.ForegroundProcess{{PID: 10, Name: "zsh", CWD: target}}}}
	preview, err := retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir()})
	if err != nil || !preview.Ready() || len(preview.UnknownSessions) != 0 {
		t.Fatalf("shell=%+v err=%v", preview, err)
	}
	rt.err = errors.New("probe failed")
	preview, err = retire.Inspect(context.Background(), rt, target, retire.Options{CWD: t.TempDir(), CloseUnknown: true})
	if err != nil || preview.Ready() {
		t.Fatalf("unknown inspection accepted: %+v %v", preview, err)
	}
}

func TestCloseAndWaitRejectsNewPaneBeforeClosing(t *testing.T) {
	target := t.TempDir()
	first := runtime.Session{Handle: "w1", AgentStatus: "idle", Panes: []runtime.Pane{{ID: "w1:p1", CWD: target}}}
	changed := first
	changed.Panes = append(append([]runtime.Pane(nil), first.Panes...), runtime.Pane{ID: "w1:p2", CWD: target})
	rt := &sequenceRuntime{lists: [][]runtime.Session{{first}, {changed}}}
	_, err := retire.CloseAndWait(context.Background(), rt, target, retire.Options{CWD: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "stale") || len(rt.closed) != 0 {
		t.Fatalf("err=%v closed=%v", err, rt.closed)
	}
}
