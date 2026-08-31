package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func exactStartRuntime() *activityRuntime {
	return &activityRuntime{openResult: runtime.OpenResult{
		Handle: "w-child", Surface: "worktree", Opened: true,
		Created: true, RootPaneID: "w-child:p1",
	}}
}

func TestStartRunDispatchesToExactRootPaneAndFocusesAfterward(t *testing.T) {
	rt := exactStartRuntime()
	f := newStartFixture(t, rt)
	command := `specstory run codex -c 'codex --token secret-value'`

	if err := f.run("--task", "command task", "--base", "main", "--run", command, "--focus"); err != nil {
		t.Fatal(err)
	}
	wantCalls := []paneRunCall{{PaneID: "w-child:p1", Command: command}}
	if !reflect.DeepEqual(rt.runCalls, wantCalls) {
		t.Fatalf("pane run calls = %+v, want %+v", rt.runCalls, wantCalls)
	}
	if !reflect.DeepEqual(rt.events, []string{"run", "activate"}) {
		t.Fatalf("run/focus ordering = %v", rt.events)
	}
	if strings.Contains(f.stdout.String(), command) || !strings.Contains(f.stdout.String(), "command   dispatched to w-child:p1") {
		t.Fatalf("human output should acknowledge without echoing command: %q", f.stdout.String())
	}
	taskBytes, err := os.ReadFile(filepath.Join(f.app.Tasks.Dir, task.MakeID("repo", "feat/command-task")+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(taskBytes), command) || strings.Contains(string(taskBytes), "secret-value") {
		t.Fatalf("task persistence leaked command: %q", taskBytes)
	}
	for _, annotation := range rt.annotations {
		for _, value := range annotation {
			if strings.Contains(value, "secret-value") {
				t.Fatalf("runtime annotation leaked command: %v", annotation)
			}
		}
	}
}

func TestStartRunStaysDetachedWithoutFocus(t *testing.T) {
	rt := exactStartRuntime()
	f := newStartFixture(t, rt)
	if err := f.run("--task", "background command", "--base", "main", "--run", "just test"); err != nil {
		t.Fatal(err)
	}
	if len(rt.runCalls) != 1 || len(rt.activateCalls) != 0 {
		t.Fatalf("run=%v activate=%v", rt.runCalls, rt.activateCalls)
	}
}

func TestStartRunRejectsUnsupportedModesAndRuntimesBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		rt   *activityRuntime
		args []string
	}{
		{name: "blank", rt: exactStartRuntime(), args: []string{"--task", "blank", "--base", "main", "--run", "  "}},
		{name: "json", rt: exactStartRuntime(), args: []string{"--task", "json", "--base", "main", "--run", "just test", "--json"}},
		{name: "direct", rt: exactStartRuntime(), args: []string{"--task", "direct", "--direct", "--run", "just test"}},
		{name: "branch", rt: exactStartRuntime(), args: []string{"--task", "branch", "--branch-only", "--base", "main", "--run", "just test"}},
		{name: "tmux", rt: &activityRuntime{name: "tmux"}, args: []string{"--task", "tmux", "--base", "main", "--run", "just test"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartFixture(t, tc.rt)
			if err := f.run(tc.args...); err == nil {
				t.Fatal("expected --run validation error")
			}
			if tasks, err := f.app.Tasks.List(); err != nil || len(tasks) != 0 {
				t.Fatalf("tasks after validation failure = %v, %v", tasks, err)
			}
			if tc.rt.openCalls != 0 || len(tc.rt.runCalls) != 0 {
				t.Fatalf("runtime mutated before validation: open=%d run=%v", tc.rt.openCalls, tc.rt.runCalls)
			}
			if got := f.repo.Git("branch", "--show-current"); got != "main" {
				t.Fatalf("validation switched branch to %s", got)
			}
			if gitx.BranchExists(context.Background(), f.repo.Root, "feat/"+tc.name) {
				t.Fatalf("validation created branch feat/%s", tc.name)
			}
		})
	}
}

func TestStartRunFailsClosedWhenExactPaneIsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		opened runtime.OpenResult
	}{
		{name: "reuse", opened: runtime.OpenResult{Handle: "w7", Surface: "worktree", Opened: true}},
		{name: "fallback", opened: runtime.OpenResult{Handle: "w8", Surface: "workspace", Opened: true, Created: true, RootPaneID: "w8:p1"}},
		{name: "missing pane", opened: runtime.OpenResult{Handle: "w9", Surface: "worktree", Opened: true, Created: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &activityRuntime{openResult: tc.opened}
			f := newStartFixture(t, rt)
			err := f.run("--task", tc.name, "--base", "main", "--run", "secret command")
			if err == nil || !strings.Contains(err.Error(), "was created") || !strings.Contains(err.Error(), "was not dispatched") {
				t.Fatalf("unavailable pane error = %v", err)
			}
			if strings.Contains(err.Error(), "secret command") {
				t.Fatalf("error leaked command: %v", err)
			}
			if len(rt.runCalls) != 0 || len(rt.activateCalls) != 0 {
				t.Fatalf("unlaunchable target was used: run=%v activate=%v", rt.runCalls, rt.activateCalls)
			}
			taskID := task.MakeID("repo", "feat/"+strings.ReplaceAll(tc.name, " ", "-"))
			saved, getErr := f.app.Tasks.Get(taskID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if _, statErr := os.Stat(saved.WorktreePath); statErr != nil {
				t.Fatalf("worktree should survive dispatch failure: %v", statErr)
			}
		})
	}
}

func TestStartRunDispatchFailurePreservesTaskAndSkipsFocus(t *testing.T) {
	rt := exactStartRuntime()
	rt.runErr = errors.New("send denied")
	f := newStartFixture(t, rt)
	err := f.run("--task", "send failure", "--base", "main", "--run", "just test", "--focus")
	if err == nil || !strings.Contains(err.Error(), "send denied") {
		t.Fatalf("dispatch error = %v", err)
	}
	if len(rt.runCalls) != 1 || len(rt.activateCalls) != 0 {
		t.Fatalf("run=%v activate=%v", rt.runCalls, rt.activateCalls)
	}
	saved, getErr := f.app.Tasks.Get(task.MakeID("repo", "feat/send-failure"))
	if getErr != nil {
		t.Fatal(getErr)
	}
	if _, statErr := os.Stat(saved.WorktreePath); statErr != nil {
		t.Fatalf("worktree should survive dispatch failure: %v", statErr)
	}
}
