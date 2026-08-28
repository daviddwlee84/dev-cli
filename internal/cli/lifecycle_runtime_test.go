package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		sessions: []runtime.Session{{Handle: "w7", Dirs: []string{res.Path}}},
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
	if err == nil || !strings.Contains(err.Error(), "refusing cold cleanup") {
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

func TestDoneCloseFailureKeepsIntegratedWorktreeAndRuntime(t *testing.T) {
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
		sessions: []runtime.Session{{Handle: "w7", Dirs: []string{res.Path}}},
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
	if err == nil || !strings.Contains(err.Error(), "worktree kept") {
		t.Fatalf("done close failure = %v", err)
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
	if got.State != task.Hot || got.RuntimeHandle != "w7" || got.RuntimeName != "herdr" {
		t.Fatalf("task should remain reconcilable after close failure: %+v", got)
	}
}
