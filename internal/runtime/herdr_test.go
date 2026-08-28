package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

type herdrCall struct {
	args []string
	out  string
	err  error
}

func scriptedHerdr(t *testing.T, calls ...herdrCall) *Herdr {
	t.Helper()
	index := 0
	h := NewHerdr()
	h.runCommand = func(_ context.Context, args ...string) ([]byte, error) {
		t.Helper()
		if index >= len(calls) {
			t.Fatalf("unexpected herdr call: %v", args)
		}
		call := calls[index]
		index++
		if !reflect.DeepEqual(args, call.args) {
			t.Fatalf("herdr call %d = %v, want %v", index, args, call.args)
		}
		return []byte(call.out), call.err
	}
	t.Cleanup(func() {
		if index != len(calls) {
			t.Errorf("used %d herdr calls, want %d", index, len(calls))
		}
	})
	return h
}

func TestHerdrListRetainsPaneIdentityAndForegroundCWD(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[{"workspace_id":"w1","label":"dev","agent_status":"idle"}]}}`},
		herdrCall{args: []string{"pane", "list"}, out: `{"id":"2","result":{"panes":[` +
			`{"pane_id":"w1:p1","workspace_id":"w1","cwd":"/old","foreground_cwd":"/repo","agent":"claude","agent_status":"idle","agent_session":{"agent":"claude","value":"abc"}},` +
			`{"pane_id":"w1:p2","workspace_id":"w1","cwd":"/repo/sub"}` +
			`]}}`},
	)
	sessions, err := h.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].Panes) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	pane := sessions[0].Panes[0]
	if pane.ID != "w1:p1" || pane.CWD != "/repo" || pane.ShellCWD != "/old" || pane.AgentStatus != "idle" || pane.AgentSession != "claude:abc" {
		t.Errorf("pane = %+v", pane)
	}
	if !sessions[0].Covers("/repo") {
		t.Errorf("foreground and nested cwd should cover repo: %+v", sessions[0])
	}
}

func TestHerdrOpenReturnsExactCreatedRootPane(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[]}}`},
		herdrCall{args: []string{"pane", "list"}, out: `{"id":"2","result":{"panes":[]}}`},
		herdrCall{args: []string{"workspace", "create", "--cwd", "/repo", "--no-focus", "--label", "child"}, out: `{"id":"3","result":{"workspace":{"workspace_id":"w7"},"tab":{"tab_id":"w7:t1"},"root_pane":{"pane_id":"w7:p12","workspace_id":"w7"}}}`},
	)

	got, err := h.Open(context.Background(), "/repo", "child")
	if err != nil {
		t.Fatal(err)
	}
	want := OpenResult{Handle: "w7", Surface: "workspace", Opened: true, Created: true, RootPaneID: "w7:p12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Open = %+v, want %+v", got, want)
	}
}

func TestHerdrOpenReuseStaysDetachedAndHasNoLaunchablePane(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[{"workspace_id":"w4"}]}}`},
		herdrCall{args: []string{"pane", "list"}, out: `{"id":"2","result":{"panes":[{"pane_id":"w4:p9","workspace_id":"w4","cwd":"/repo"}]}}`},
	)

	got, err := h.Open(context.Background(), "/repo", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if got.Handle != "w4" || got.Surface != "workspace" || !got.Opened || got.Created || got.RootPaneID != "" {
		t.Fatalf("reused Open = %+v", got)
	}
}

func TestHerdrOpenUsesSubdirectoryPane(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[{"workspace_id":"w4"}]}}`},
		herdrCall{args: []string{"pane", "list"}, out: `{"id":"2","result":{"panes":[{"pane_id":"w4:p9","workspace_id":"w4","cwd":"/repo/subdir"}]}}`},
	)
	got, err := h.Open(context.Background(), "/repo", "ignored")
	if err != nil || got.Handle != "w4" || got.Created {
		t.Fatalf("subdirectory reuse = %+v, %v", got, err)
	}
}

func TestHerdrActivateInsideFocusesWithoutAttachingAnotherClient(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	h := scriptedHerdr(t, herdrCall{
		args: []string{"workspace", "focus", "w4"}, out: `{"id":"1","result":{}}`,
	})
	if err := h.Activate(context.Background(), "w4"); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrActivatePropagatesFocusFailure(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	h := scriptedHerdr(t, herdrCall{
		args: []string{"workspace", "focus", "w4"}, err: errors.New("focus denied"),
	})
	if err := h.Activate(context.Background(), "w4"); err == nil || !strings.Contains(err.Error(), "focus denied") {
		t.Fatalf("Activate error = %v", err)
	}
}

func TestHerdrActivateOutsideFocusesThenAttaches(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	record := filepath.Join(t.TempDir(), "record")
	script := filepath.Join(t.TempDir(), "herdr")
	body := "#!/bin/sh\nif [ \"$1\" = workspace ]; then printf '{\"result\":{}}'; else printf attach > \"$DEV_TEST_RECORD\"; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_TEST_RECORD", record)
	h := NewHerdr()
	h.bin = script
	if err := h.Activate(context.Background(), "w4"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record)
	if err != nil || string(got) != "attach" {
		t.Fatalf("outside attach record = %q, %v", got, err)
	}
}

func TestHerdrOpenPaneListFailureDoesNotCreateDuplicate(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[{"workspace_id":"w4"}]}}`},
		herdrCall{args: []string{"pane", "list"}, err: errors.New("pane inventory unavailable")},
	)
	got, err := h.Open(context.Background(), "/repo", "child")
	if err == nil || !strings.Contains(err.Error(), "pane inventory unavailable") || got != (OpenResult{}) {
		t.Fatalf("pane-list failure = %+v, %v", got, err)
	}
}

func TestHerdrOpenWorktreeReturnsExactRootPane(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"worktree", "open", "--path", "/wt/repo", "--no-focus", "--label", "child"},
		out:  `{"id":"1","result":{"workspace":{"workspace_id":"w7"},"worktree":{"open_workspace_id":"w7"},"root_pane":{"pane_id":"w7:p12","workspace_id":"w7"},"already_open":false}}`,
	})

	got, err := h.OpenWorktree(context.Background(), "/wt/repo", "child")
	if err != nil {
		t.Fatal(err)
	}
	want := OpenResult{Handle: "w7", Surface: "worktree", Opened: true, Created: true, RootPaneID: "w7:p12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenWorktree = %+v, want %+v", got, want)
	}
}

func TestHerdrParentWorktreeSourceUsesPorcelainParentPath(t *testing.T) {
	r := gittest.New(t)
	linked := filepath.Join(t.TempDir(), "linked")
	r.Git("branch", "feat/linked")
	r.Git("worktree", "add", linked, "feat/linked")
	want, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := NewHerdr().parentWorktreeSource(context.Background(), linked); got != filepath.Clean(want) {
		t.Fatalf("parent source = %q, want %q", got, want)
	}
}

func TestHerdrParentWorktreeSourceKeepsBareHubPath(t *testing.T) {
	r := gittest.New(t)
	bare := r.WithRemote()
	linked := filepath.Join(t.TempDir(), "bare-linked")
	r.GitIn(filepath.Dir(bare), "--git-dir", bare, "worktree", "add", "-b", "feat/bare", linked, "main")
	want, err := filepath.EvalSymlinks(bare)
	if err != nil {
		t.Fatal(err)
	}
	if got := NewHerdr().parentWorktreeSource(context.Background(), linked); got != filepath.Clean(want) {
		t.Fatalf("bare parent source = %q, want %q", got, want)
	}
}

func TestHerdrOpenWorktreePinsNativeParentSource(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"worktree", "open", "--cwd", "/repo", "--path", "/repo-wt", "--no-focus", "--label", "repo/feat/x"},
		out:  `{"id":"1","result":{"workspace":{"workspace_id":"w7"},"root_pane":{"pane_id":"w7:p1","workspace_id":"w7"},"already_open":false}}`,
	})
	h.worktreeSource = func(context.Context, string) string { return "/repo" }
	got, err := h.OpenWorktree(context.Background(), "/repo-wt", "repo/feat/x")
	if err != nil || got.Surface != "worktree" || !got.Created || got.RootPaneID != "w7:p1" {
		t.Fatalf("OpenWorktree = %+v, %v", got, err)
	}
}

func TestHerdrOpenWorktreeRequiresExplicitReuseMarker(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"worktree", "open", "--path", "/wt/repo", "--no-focus"},
		out:  `{"id":"1","result":{"workspace":{"workspace_id":"w7"},"root_pane":{"pane_id":"w7:p1","workspace_id":"w7"}}}`,
	})
	got, err := h.OpenWorktree(context.Background(), "/wt/repo", "")
	if err == nil || !strings.Contains(err.Error(), "does not say whether") || got != (OpenResult{}) {
		t.Fatalf("missing reuse marker result = %+v, %v", got, err)
	}
}

func TestHerdrOpenWorktreeReuseHasNoLaunchablePane(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"worktree", "open", "--path", "/wt/repo", "--no-focus"},
		out:  `{"id":"1","result":{"worktree":{"open_workspace_id":"w8"},"root_pane":{"pane_id":"w8:p1","workspace_id":"w8"},"already_open":true}}`,
	})
	got, err := h.OpenWorktree(context.Background(), "/wt/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Handle != "w8" || got.Surface != "worktree" || !got.Opened || got.Created || got.RootPaneID != "" {
		t.Fatalf("reused worktree = %+v", got)
	}
}

func TestHerdrOpenWorktreeRejectsUncorrelatedRootPane(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{
			args: []string{"worktree", "open", "--path", "/wt/repo", "--no-focus"},
			out:  `{"id":"1","result":{"workspace":{"workspace_id":"w7"},"root_pane":{"pane_id":"w8:p1","workspace_id":"w8"},"already_open":false}}`,
		},
		herdrCall{args: []string{"workspace", "close", "w7"}, out: ""},
	)
	got, err := h.OpenWorktree(context.Background(), "/wt/repo", "")
	if err == nil || !strings.Contains(err.Error(), "no correlated root pane") || got != (OpenResult{}) {
		t.Fatalf("uncorrelated root result = %+v, %v", got, err)
	}
}

func TestHerdrOpenWorktreeRejectsNewWorkspaceWithoutRootPane(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{
			args: []string{"worktree", "open", "--path", "/wt/repo", "--no-focus"},
			out:  `{"id":"1","result":{"workspace":{"workspace_id":"w7"},"already_open":false}}`,
		},
		herdrCall{args: []string{"workspace", "close", "w7"}, out: ""},
	)
	got, err := h.OpenWorktree(context.Background(), "/wt/repo", "")
	if err == nil || !strings.Contains(err.Error(), "no correlated root pane") || got != (OpenResult{}) {
		t.Fatalf("malformed new worktree result = %+v, %v", got, err)
	}
}

func TestHerdrWorktreeFallbackNeverAdvertisesPane(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"worktree", "open", "--path", "/wt/repo", "--no-focus", "--label", "child"}, err: errors.New("unsupported")},
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"2","result":{"workspaces":[]}}`},
		herdrCall{args: []string{"pane", "list"}, out: `{"id":"3","result":{"panes":[]}}`},
		herdrCall{args: []string{"workspace", "create", "--cwd", "/wt/repo", "--no-focus", "--label", "child"}, out: `{"id":"4","result":{"workspace":{"workspace_id":"w9"},"root_pane":{"pane_id":"w9:p1","workspace_id":"w9"}}}`},
	)
	got, err := h.OpenWorktree(context.Background(), "/wt/repo", "child")
	if err != nil {
		t.Fatal(err)
	}
	if got.Handle != "w9" || got.Surface != "workspace" || !got.Created || got.RootPaneID != "" {
		t.Fatalf("fallback = %+v", got)
	}
}

func TestHerdrMissingRootPaneFailsClosed(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[]}}`},
		herdrCall{args: []string{"pane", "list"}, out: `{"id":"2","result":{"panes":[]}}`},
		herdrCall{args: []string{"workspace", "create", "--cwd", "/repo", "--no-focus"}, out: `{"id":"3","result":{"workspace":{"workspace_id":"w7"}}}`},
		herdrCall{args: []string{"workspace", "close", "w7"}, out: ""},
	)
	got, err := h.Open(context.Background(), "/repo", "")
	if err == nil || !strings.Contains(err.Error(), "no correlated root pane") {
		t.Fatalf("missing root pane must fail closed: result=%+v err=%v", got, err)
	}
	if got != (OpenResult{}) {
		t.Fatalf("failed create returned partial result: %+v", got)
	}
}

func TestHerdrRejectsMissingHandleAndMalformedOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{name: "missing handle", out: `{"id":"3","result":{"root_pane":{"pane_id":"w7:p1","workspace_id":"w7"}}}`},
		{name: "malformed", out: `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scriptedHerdr(t,
				herdrCall{args: []string{"workspace", "list"}, out: `{"id":"1","result":{"workspaces":[]}}`},
				herdrCall{args: []string{"pane", "list"}, out: `{"id":"2","result":{"panes":[]}}`},
				herdrCall{args: []string{"workspace", "create", "--cwd", "/repo", "--no-focus"}, out: tc.out},
			)
			if _, err := h.Open(context.Background(), "/repo", ""); err == nil {
				t.Fatal("malformed successful response must fail")
			}
		})
	}
}

func TestHerdrCurrentPaneResolvesMovedCallerWithoutFocus(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"pane", "current", "--current"},
		out:  `{"id":"1","result":{"pane":{"pane_id":"w2:p9","workspace_id":"w2"}}}`,
	})
	got, err := h.CurrentPaneID(context.Background())
	if err != nil || got != "w2:p9" {
		t.Fatalf("CurrentPaneID = %q, %v", got, err)
	}
}

func TestHerdrAgentActivitiesUseForegroundCWDAndAllStates(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"agent", "list"},
		out: `{"id":"1","result":{"agents":[` +
			`{"agent":"claude","name":"reviewer","agent_status":"done","cwd":"/old","foreground_cwd":"/repo","pane_id":"w1:p2","workspace_id":"w1"},` +
			`{"agent":"codex","agent_status":"unknown","cwd":"/repo2","pane_id":"w2:p1","workspace_id":"w2"}` +
			`]}}`,
	})
	got, err := h.AgentActivities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CWD != "/repo" || got[0].Status != "done" || got[1].Status != "unknown" {
		t.Fatalf("AgentActivities = %+v", got)
	}
}

func TestHerdrAgentActivitiesRejectRecognizedAgentWithoutCWD(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"agent", "list"},
		out:  `{"id":"1","result":{"agents":[{"agent":"claude","pane_id":"w1:p2","agent_status":"unknown"}]}}`,
	})
	if got, err := h.AgentActivities(context.Background()); err == nil || !strings.Contains(err.Error(), "has no cwd") || got != nil {
		t.Fatalf("missing agent cwd result = %+v, %v", got, err)
	}
}

func TestHerdrAgentActivitiesRejectMissingArray(t *testing.T) {
	for _, result := range []string{`{}`, `{"agents":null}`} {
		h := scriptedHerdr(t, herdrCall{
			args: []string{"agent", "list"},
			out:  `{"id":"1","result":` + result + `}`,
		})
		if got, err := h.AgentActivities(context.Background()); err == nil || !strings.Contains(err.Error(), "no agents array") || got != nil {
			t.Fatalf("missing agents result = %+v, %v", got, err)
		}
	}
}

func TestHerdrAcceptsEmptyMutationOutput(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"workspace", "report-metadata", "w7", "--source", "dev", "--token", "stage=HOT"},
		out:  "",
	})
	if err := h.Annotate(context.Background(), "w7", map[string]string{"stage": "HOT"}); err != nil {
		t.Fatalf("empty successful mutation output should be accepted: %v", err)
	}
}

func TestHerdrEnvelopeErrorIsReported(t *testing.T) {
	h := scriptedHerdr(t, herdrCall{
		args: []string{"agent", "list"},
		out:  `{"id":"1","error":{"code":"unavailable","message":"server stopped"}}`,
	})
	_, err := h.AgentActivities(context.Background())
	if err == nil || !strings.Contains(err.Error(), "server stopped (unavailable)") {
		t.Fatalf("AgentActivities error = %v", err)
	}
}
