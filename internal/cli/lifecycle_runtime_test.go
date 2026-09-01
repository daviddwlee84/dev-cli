package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

func lifecycleConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Paths.WorktreeRoot = filepath.Join(t.TempDir(), "Worktrees")
	cfg.Paths.WorktreePath = "{{worktree_root}}/{{repo}}/{{branch|slug}}"
	cfg.Worktree.Include = nil
	cfg.Worktree.PostCreate = config.PostCreate{}
	return cfg
}

type blockingCloseRuntime struct {
	activityRuntime
	started chan struct{}
	release chan struct{}
}

func (r *blockingCloseRuntime) Close(_ context.Context, handle string) error {
	r.closeCalls = append(r.closeCalls, handle)
	close(r.started)
	<-r.release
	r.sessions = nil
	return nil
}

func TestParkHoldsTaskMutationLockThroughColdCleanup(t *testing.T) {
	r := gittest.New(t)
	r.WithRemote()
	cfg := lifecycleConfig(t)
	res, err := (&wt.Manager{Cfg: cfg}).Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/locked-cold", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.GitIn(res.Path, "push", "-u", "origin", "feat/locked-cold")
	rt := &blockingCloseRuntime{
		activityRuntime: activityRuntime{sessions: []runtime.Session{{Handle: "w7", Dirs: []string{res.Path}, AgentStatus: "idle"}}},
		started:         make(chan struct{}), release: make(chan struct{}),
	}
	store := task.NewStore(t.TempDir())
	tk := &task.Task{
		Name: "locked cold", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/locked-cold", Base: "main", WorktreePath: res.Path,
		Mode: task.ModeWorktree, State: task.Hot, Owner: config.Hostname(),
		RuntimeHandle: "w7", RuntimeName: "herdr",
	}
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: cfg, Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: rt}
	cmd := newParkCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{tk.ID, "--cold"})
	parkDone := make(chan error, 1)
	go func() { parkDone <- cmd.Execute() }()
	<-rt.started

	concurrent, err := task.NewStore(store.Dir).Get(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	concurrent.Next = "concurrent edit"
	editDone := make(chan error, 1)
	go func() { editDone <- task.NewStore(store.Dir).Save(concurrent) }()
	select {
	case err := <-editDone:
		t.Fatalf("concurrent task edit bypassed cold cleanup lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(rt.release)
	if err := <-parkDone; err != nil {
		t.Fatal(err)
	}
	if err := <-editDone; !errors.Is(err, task.ErrConflict) {
		t.Fatalf("post-cleanup stale edit = %v, want ErrConflict", err)
	}
	stored, err := store.Get(tk.ID)
	if err != nil || stored.State != task.Cold || stored.WorktreePath != "" {
		t.Fatalf("cold task = %+v, %v", stored, err)
	}
}

func TestParkColdCloseFailureKeepsCheckoutAndTaskRuntime(t *testing.T) {
	r := gittest.New(t)
	r.WithRemote()
	cfg := lifecycleConfig(t)
	res, err := (&wt.Manager{Cfg: cfg}).Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/close-failure", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.GitIn(res.Path, "push", "-u", "origin", "feat/close-failure")

	rt := &activityRuntime{
		sessions: []runtime.Session{{Handle: "w7", Dirs: []string{res.Path}, AgentStatus: "idle"}},
		closeErr: errors.New("close failed"),
	}
	store := task.NewStore(t.TempDir())
	tk := &task.Task{
		Name: "close failure", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/close-failure", Base: "main", WorktreePath: res.Path,
		Mode: task.ModeWorktree, State: task.Hot, Owner: config.Hostname(),
		RuntimeHandle: "w7", RuntimeName: "herdr",
	}
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: cfg, Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: rt}
	cmd := newParkCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{tk.ID, "--cold"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("cold close failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "README.md")); err != nil {
		t.Fatalf("cold park removed checkout after close failure: %v", err)
	}
	got, err := store.Get(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.Hot || got.WorktreePath != res.Path || got.RuntimeHandle != "w7" || got.RuntimeName != "herdr" {
		t.Fatalf("task changed after refused cold cleanup: %+v", got)
	}
}

func TestDoneIntegratesWithoutClosingRuntimeOrWorktree(t *testing.T) {
	r := gittest.New(t)
	cfg := lifecycleConfig(t)
	res, err := (&wt.Manager{Cfg: cfg}).Create(context.Background(), wt.CreateRequest{
		RepoPath: r.Root, Branch: "feat/done-close-failure", Base: "main", NoRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.GitIn(res.Path, "add", "feature.txt")
	r.GitIn(res.Path, "commit", "-m", "feat: done")

	rt := &activityRuntime{
		sessions: []runtime.Session{{Handle: "w7", Dirs: []string{res.Path}, AgentStatus: "idle"}},
		closeErr: errors.New("close failed"),
	}
	store := task.NewStore(t.TempDir())
	tk := &task.Task{
		Name: "done close failure", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/done-close-failure", Base: "main", WorktreePath: res.Path,
		Mode: task.ModeWorktree, State: task.Hot, Owner: config.Hostname(),
		RuntimeHandle: "w7", RuntimeName: "herdr",
	}
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: cfg, Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: rt}
	cmd := newDoneCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{tk.ID, "--ff"})
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("done integration = %v", err)
	}
	if len(rt.closeCalls) != 0 {
		t.Fatalf("done must not close runtime, got %v", rt.closeCalls)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "feature.txt")); err != nil {
		t.Fatalf("done removed worktree after close failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Root, "feature.txt")); err != nil {
		t.Fatalf("fast-forward should already be integrated: %v", err)
	}
	got, err := store.Get(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.Done || got.RuntimeHandle != "w7" || got.RuntimeName != "herdr" || got.WorktreePath != res.Path {
		t.Fatalf("merged task should retain retirement resources: %+v", got)
	}
}
