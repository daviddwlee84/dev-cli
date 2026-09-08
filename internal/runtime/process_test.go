package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestHerdrForegroundClassificationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		state      ProcessState
	}{
		{"shell", `"shell_pid":11,"foreground_process_group_id":11,"foreground_processes":[{"pid":11,"name":"zsh","cwd":"/repo"}]`, ProcessShell},
		{"editor", `"shell_pid":11,"foreground_process_group_id":22,"foreground_processes":[{"pid":22,"name":"nvim","cwd":"/repo","argv":["nvim","private-argument-value"]}]`, ProcessCommand},
		{"shell-with-child", `"shell_pid":11,"foreground_process_group_id":11,"foreground_processes":[{"pid":11,"name":"zsh"},{"pid":22,"name":"make"}]`, ProcessCommand},
		{"empty", `"foreground_processes":[]`, ProcessUnknown},
		{"no-group", `"shell_pid":11,"foreground_processes":[{"pid":22,"name":"nvim"}]`, ProcessUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scriptedHerdr(t, herdrCall{args: []string{"pane", "process-info", "--pane", "w1:p1"}, out: `{"result":{"process_info":{"pane_id":"w1:p1",` + tc.body + `}}}`})
			got, err := h.InspectPaneProcesses(context.Background(), "w1:p1")
			if err != nil || got.State != tc.state {
				t.Fatalf("state=%s err=%v", got.State, err)
			}
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "private-argument-value") {
				t.Fatal("raw argv escaped into observation")
			}
		})
	}
}

func TestForegroundCallerMustBeExactPIDAndUnrecognized(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		pid   int
		state ProcessState
	}{
		{"exact", "", 22, ProcessCaller}, {"other-pid", "", 33, ProcessCommand}, {"agent", "codex", 22, ProcessCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scriptedHerdr(t, herdrCall{args: []string{"pane", "process-info", "--pane", "w1:p1"}, out: `{"result":{"process_info":{"pane_id":"w1:p1","shell_pid":11,"foreground_process_group_id":22,"foreground_processes":[{"pid":22,"name":"dev","cwd":"/repo"}]}}}`})
			p := observePaneProcess(context.Background(), h, Pane{ID: "w1:p1", Agent: tc.agent}, "w1:p1", tc.pid)
			if p.Process.State != tc.state {
				t.Fatalf("process=%+v", p.Process)
			}
		})
	}
}

func TestForegroundProbeErrorsAndWrongPaneFailClosed(t *testing.T) {
	for _, call := range []herdrCall{
		{args: []string{"pane", "process-info", "--pane", "w1:p1"}, err: errors.New("sensitive raw argv")},
		{args: []string{"pane", "process-info", "--pane", "w1:p1"}, out: `{"result":{"process_info":{"pane_id":"w2:p9","shell_pid":11,"foreground_process_group_id":11,"foreground_processes":[{"pid":11,"name":"zsh"}]}}}`},
	} {
		h := scriptedHerdr(t, call)
		p := observePaneProcess(context.Background(), h, Pane{ID: "w1:p1"}, "", 0)
		if p.Process.State != ProcessUnknown || strings.Contains(p.Process.Error, "sensitive") {
			t.Fatalf("process=%+v", p.Process)
		}
	}
}

func TestWorkspaceProtectionUsesDeclaredCheckout(t *testing.T) {
	target := t.TempDir()
	for _, tc := range []struct {
		name      string
		session   Session
		protected bool
	}{
		{"parent", Session{WorkspaceIdentityObserved: true, WorkspaceCheckout: target}, true},
		{"other", Session{WorkspaceIdentityObserved: true, WorkspaceCheckout: t.TempDir(), WorkspaceLinked: true}, true},
		{"unknown", Session{WorkspaceIdentityObserved: true}, true},
		{"target", Session{WorkspaceIdentityObserved: true, WorkspaceCheckout: target, WorkspaceLinked: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (WorkspaceProtection(tc.session, target) != "") != tc.protected {
				t.Fatal("wrong protection")
			}
		})
	}
}

func TestForegroundExcludesOnlyTheExactInspectionSubprocess(t *testing.T) {
	for _, extra := range []bool{false, true} {
		h := NewHerdr()
		h.processProbe = func(_ context.Context, args ...string) ([]byte, int, error) {
			programs := `{"pid":22,"name":"dev","cwd":"/repo"},{"pid":99,"name":"herdr","cwd":"/repo"}`
			if extra {
				programs += `,{"pid":33,"name":"herdr","cwd":"/repo"}`
			}
			return []byte(`{"result":{"process_info":{"pane_id":"w1:p1","shell_pid":11,"foreground_process_group_id":22,"foreground_processes":[` + programs + `]}}}`), 99, nil
		}
		p := observePaneProcess(context.Background(), h, Pane{ID: "w1:p1"}, "w1:p1", 22)
		want := ProcessCaller
		if extra {
			want = ProcessCommand
		}
		if p.Process.State != want {
			t.Fatalf("extra=%v observation=%+v", extra, p.Process)
		}
		for _, process := range p.Process.Processes {
			if process.PID == 99 {
				t.Fatal("inspection subprocess was not excluded")
			}
		}
	}
}

// Opt-in read-only protocol check. Compile this package's test binary and run
// it directly from an otherwise idle Herdr shell pane, without a go wrapper.
func TestForegroundLiveCaller(t *testing.T) {
	if os.Getenv("DEV_TEST_LIVE_HERDR") != "1" {
		t.Skip("requires explicit opt-in from an idle Herdr shell pane")
	}
	if os.Getenv("HERDR_ENV") != "1" {
		t.Fatal("live caller check requires Herdr")
	}
	h := NewHerdr()
	id, err := h.CurrentPaneID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := h.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		for _, pane := range session.Panes {
			if pane.ID != id {
				continue
			}
			observed := observePaneProcess(context.Background(), h, pane, id, os.Getpid())
			if observed.Process == nil || observed.Process.State != ProcessCaller {
				t.Fatalf("live caller classification=%+v", observed.Process)
			}
			t.Logf("live caller %s: %s; %d foreground process(es)", id, observed.Process.State, len(observed.Process.Processes))
			return
		}
	}
	t.Fatal("live caller pane was not enumerated")
}
