package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestSetTaskRuntimeNeverPersistsNonePseudoHandle(t *testing.T) {
	tk := &task.Task{}
	setTaskRuntime(tk, runtime.None{}, runtime.OpenResult{Handle: "/checkout"})
	if tk.RuntimeHandle != "" || tk.RuntimeName != "" {
		t.Fatalf("None pseudo-handle was persisted: %+v", tk)
	}

	rt := &activityRuntime{}
	setTaskRuntime(tk, rt, runtime.OpenResult{Handle: "w7", Opened: true})
	if tk.RuntimeHandle != "w7" || tk.RuntimeName != "herdr" {
		t.Fatalf("runtime provenance not persisted: %+v", tk)
	}
	clearTaskRuntime(tk)
	if tk.RuntimeHandle != "" || tk.RuntimeName != "" {
		t.Fatalf("runtime fields were not cleared together: %+v", tk)
	}
}

func TestCloseTaskRuntimeUsesRecordedBackend(t *testing.T) {
	r := gittest.New(t)
	herdr := &activityRuntime{name: "herdr", sessions: []runtime.Session{{Handle: "w7", Dirs: []string{r.Root}}}}
	tmux := &activityRuntime{name: "tmux"}
	app := &App{runtimeInstance: tmux, runtimesByName: map[string]runtime.Runtime{"herdr": herdr}}
	tk := &task.Task{RuntimeHandle: "w7", RuntimeName: "herdr"}

	resolved, closed, err := closeTaskRuntime(context.Background(), app, tk, r.Root)
	if err != nil || !closed || resolved != herdr {
		t.Fatalf("close result = %T %v %v", resolved, closed, err)
	}
	if len(herdr.closeCalls) != 1 || len(tmux.closeCalls) != 0 {
		t.Fatalf("close calls: herdr=%v tmux=%v", herdr.closeCalls, tmux.closeCalls)
	}
	if tk.RuntimeHandle != "" || tk.RuntimeName != "" {
		t.Fatalf("closed runtime fields not cleared: %+v", tk)
	}
}

func TestResumeReopensStaleHandleAndPersistsBackend(t *testing.T) {
	r := gittest.New(t)
	rt := &activityRuntime{openResult: runtime.OpenResult{Handle: "w8", Surface: "workspace", Opened: true, Created: true}}
	store := task.NewStore(t.TempDir())
	tk := &task.Task{
		Name: "direct", Repo: "repo", RepoPath: r.Root, Branch: "main", Base: "main",
		Mode: task.ModeDirect, State: task.Warm, Owner: config.Hostname(),
		RuntimeHandle: "w-stale", RuntimeName: "herdr",
	}
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := &App{Cfg: config.Default(), Tasks: store, Out: &out, Err: &errOut, runtimeInstance: rt}
	cmd := newResumeCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{tk.ID, "--fetch=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rt.openCalls != 1 || got.RuntimeHandle != "w8" || got.RuntimeName != "herdr" {
		t.Fatalf("stale handle was not reopened with provenance: calls=%d task=%+v", rt.openCalls, got)
	}
}

func TestResumeKeepsValidatedLiveHandle(t *testing.T) {
	r := gittest.New(t)
	rt := &activityRuntime{sessions: []runtime.Session{{Handle: "w7", Dirs: []string{filepath.Join(r.Root, "subdir")}}}}
	r.Write("subdir/.keep", "x")
	store := task.NewStore(t.TempDir())
	tk := &task.Task{
		Name: "direct", Repo: "repo", RepoPath: r.Root, Branch: "main", Base: "main",
		Mode: task.ModeDirect, State: task.Warm, Owner: config.Hostname(),
		RuntimeHandle: "w7", RuntimeName: "herdr",
	}
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Default(), Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: rt}
	cmd := newResumeCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{tk.ID, "--fetch=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if rt.openCalls != 0 {
		t.Fatalf("validated handle should not reopen, calls=%d", rt.openCalls)
	}
}

func TestResumeWithNoneDoesNotPersistPseudoHandle(t *testing.T) {
	r := gittest.New(t)
	store := task.NewStore(t.TempDir())
	tk := &task.Task{
		Name: "direct", Repo: "repo", RepoPath: r.Root, Branch: "main", Base: "main",
		Mode: task.ModeDirect, State: task.Warm, Owner: config.Hostname(),
	}
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Default(), Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: runtime.None{}}
	cmd := newResumeCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{tk.ID, "--fetch=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeHandle != "" || got.RuntimeName != "" {
		t.Fatalf("None pseudo-handle persisted after resume: %+v", got)
	}
}
