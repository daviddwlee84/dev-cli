package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestScopedHerdrOpensCanonicalAndLinkedCheckoutsWithoutFocus(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, reused := range []bool{false, true} {
			t.Run(fmt.Sprintf("linked=%v/reused=%v", linked, reused), func(t *testing.T) {
				path := "/repo"
				if linked {
					path = "/worktrees/repo/topic"
				}
				result := fmt.Sprintf(`{"result":{"workspace":{"workspace_id":"w7","agent_status":"working"},"already_open":%v`, reused)
				if !reused {
					result += `,"root_pane":{"pane_id":"w7:p1","workspace_id":"w7"}`
				}
				result += `}}`
				h := scriptedHerdr(t, herdrCall{args: []string{"--session", "agents", "worktree", "open", "--cwd", "/repo", "--path", path, "--no-focus"}, out: result})
				h.worktreeSource = func(context.Context, string) string { return "/repo" }
				scoped, _ := h.WithSession("agents")
				opened, err := scoped.OpenWorktree(t.Context(), path, "")
				if err != nil || opened.Handle != "w7" || opened.Created == reused || opened.Surface != "worktree" {
					t.Fatalf("open=%+v / %v", opened, err)
				}
				if reused && opened.RootPaneID != "" {
					t.Fatal("reused recognized workspace claimed a newly-created pane")
				}
			})
		}
	}
}

func TestScopedHerdrRetainsUncertainWorkspacesWithoutFallbackOrClose(t *testing.T) {
	for _, test := range []struct {
		name, out string
		err       error
	}{
		{"incomplete", `{"result":{"workspace":{"workspace_id":"w7"},"already_open":false}}`, nil},
		{"malformed", `{`, nil},
		{"interrupted", "", errors.New("connection interrupted after request")},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := scriptedHerdr(t, herdrCall{args: []string{"--session", "agents", "worktree", "open", "--cwd", "/repo", "--path", "/repo", "--no-focus"}, out: test.out, err: test.err})
			h.worktreeSource = func(context.Context, string) string { return "/repo" }
			scoped, _ := h.WithSession("agents")
			if _, err := scoped.OpenWorktree(t.Context(), "/repo", ""); err == nil || !strings.Contains(err.Error(), "retained") {
				t.Fatalf("uncertain result was accepted or rolled back: %v", err)
			}
		})
	}
	h := scriptedHerdr(t,
		herdrCall{args: []string{"--session", "agents", "workspace", "list"}, out: `{"result":{"workspaces":[]}}`},
		herdrCall{args: []string{"--session", "agents", "pane", "list"}, out: `{"result":{"panes":[]}}`},
		herdrCall{args: []string{"--session", "agents", "workspace", "create", "--cwd", "/repo", "--no-focus"}, out: `{"result":{"workspace":{"workspace_id":"w8"}}}`},
	)
	scoped, _ := h.WithSession("agents")
	if _, err := scoped.Open(t.Context(), "/repo", ""); err == nil || !strings.Contains(err.Error(), "retained incomplete workspace w8") {
		t.Fatalf("generic creation was not retained: %v", err)
	}
}

func TestScopedHerdrVerifiesExactWorkspaceCoverage(t *testing.T) {
	for _, test := range []struct {
		name, workspaces, panes string
		valid                   bool
	}{
		{"wrong-handle", `[{"workspace_id":"w8"}]`, `[{"workspace_id":"w8","cwd":"/repo"}]`, false},
		{"wrong-path", `[{"workspace_id":"w7"}]`, `[{"workspace_id":"w7","cwd":"/other"}]`, false},
		{"unknown-panes", `[{"workspace_id":"w7"}]`, `null`, false},
		{"duplicate-handle", `[{"workspace_id":"w7"},{"workspace_id":"w7"}]`, `[{"workspace_id":"w7","cwd":"/repo"}]`, false},
		{"recognized-owner", `[{"workspace_id":"w7","agent_status":"working"}]`, `[{"workspace_id":"w7","cwd":"/repo","agent":"codex","agent_status":"working"}]`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := scriptedHerdr(t,
				herdrCall{args: []string{"--session", "agents", "workspace", "list"}, out: `{"result":{"workspaces":` + test.workspaces + `}}`},
				herdrCall{args: []string{"--session", "agents", "pane", "list"}, out: `{"result":{"panes":` + test.panes + `}}`},
			)
			scoped, _ := h.WithSession("agents")
			if err := scoped.VerifyWorkspace(t.Context(), "w7", "/repo"); (err == nil) != test.valid {
				t.Fatalf("coverage valid=%v: %v", test.valid, err)
			}
		})
	}
}

func TestScopedHerdrCheckAndPrepareNeverFocus(t *testing.T) {
	h := scriptedHerdr(t,
		herdrCall{args: []string{"--session", "agents", "workspace", "list"}, out: `{"result":{"workspaces":[]}}`},
		herdrCall{args: []string{"--session", "agents", "pane", "list"}, out: `{"result":{"panes":[]}}`},
		herdrCall{args: []string{"--session", "agents", "worktree", "open", "--cwd", "/repo", "--path", "/repo", "--no-focus", "--label", "Repository"}, out: `{"result":{"workspace":{"workspace_id":"w7"},"root_pane":{"pane_id":"w7:p1","workspace_id":"w7"},"already_open":false}}`},
	)
	h.worktreeSource = func(context.Context, string) string { return "/repo" }
	scoped, err := h.WithSession("agents")
	if err != nil {
		t.Fatal(err)
	}
	if h.session != "" {
		t.Fatal("session setting changed ordinary local adapter")
	}
	if err := scoped.CheckSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	opened, err := scoped.OpenWorktree(t.Context(), "/repo", "Repository")
	if err != nil || opened.Handle != "w7" || !opened.Created {
		t.Fatalf("open = %+v / %v", opened, err)
	}
}

func TestScopedHerdrRejectsMissingExplicitParentAndMissingInventories(t *testing.T) {
	h := scriptedHerdr(t)
	h.worktreeSource = func(context.Context, string) string { return "" }
	scoped, err := h.WithSession("agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.OpenWorktree(t.Context(), "/repo", "repo"); err == nil {
		t.Fatal("scoped preparation inferred a caller cwd")
	}
	if err := scoped.Focus(t.Context(), "w7"); err == nil {
		t.Fatal("scoped preparation permitted server-wide focus")
	}
	if err := scoped.Activate(t.Context(), "w7"); err == nil {
		t.Fatal("scoped preparation permitted a client attach")
	}
	h = scriptedHerdr(t, herdrCall{args: []string{"--session", "agents", "workspace", "list"}, out: `{"result":{}}`})
	scoped, _ = h.WithSession("agents")
	if err := scoped.CheckSession(t.Context()); err == nil {
		t.Fatal("missing inventory reported session ready")
	}
}

func TestScopedHerdrEnvironmentRemovesCallerRoutingOnly(t *testing.T) {
	for _, session := range []string{"0", "-agents", "Project.A_1", strings.Repeat("a", 64)} {
		if _, err := NewHerdr().WithSession(session); err != nil {
			t.Fatalf("rejected native-valid session %q: %v", session, err)
		}
	}
	input := []string{"PATH=/bin", "HERDR_SESSION=caller", "HERDR_SOCKET_PATH=/caller", "HERDR_CLIENT_SOCKET_PATH=/client", "HERDR_ENV=1", "HERDR_WORKSPACE_ID=w1", "HERDR_TAB_ID=t1", "HERDR_PANE_ID=p1", "HERDR_PANE_RUNTIME_ID=r1", "HERDR_ACTIVE_ENDPOINT=local", "HERDR_CONFIG_PATH=/native/config"}
	if got := scopedHerdrEnvironment(input); !reflect.DeepEqual(got, []string{"PATH=/bin", "HERDR_ENV=1", "HERDR_CONFIG_PATH=/native/config"}) {
		t.Fatalf("environment = %v", got)
	}
	if goruntime.GOOS == "windows" {
		return
	}
	file := filepath.Join(t.TempDir(), "herdr")
	program := "#!/bin/sh\n[ -z \"${HERDR_SOCKET_PATH+x}${HERDR_CLIENT_SOCKET_PATH+x}${HERDR_SESSION+x}\" ] || exit 91\n[ \"$HERDR_ENV\" = caller ] || exit 94\n[ \"$1\" = --session ] && [ \"$2\" = agents ] || exit 92\ncase \"$3 $4\" in\n'workspace list') printf '{\"result\":{\"workspaces\":[]}}';;\n'pane list') printf '{\"result\":{\"panes\":[]}}';;\n*) exit 93;;\nesac\n"
	if err := os.WriteFile(file, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, variable := range []string{"HERDR_SOCKET_PATH", "HERDR_CLIENT_SOCKET_PATH", "HERDR_SESSION", "HERDR_ENV"} {
		t.Setenv(variable, "caller")
	}
	h := NewHerdr()
	h.bin = file
	scoped, _ := h.WithSession("agents")
	if err := scoped.CheckSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"", ".", "..", "a/b", "with space", strings.Repeat("a", 65)} {
		if _, err := h.WithSession(session); err == nil {
			t.Fatalf("accepted session %q", session)
		}
	}
}
