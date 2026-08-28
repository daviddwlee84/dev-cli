package retire_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type mutatingRuntime struct {
	session runtime.Session
	close   func() error
	closed  bool
}

func (m *mutatingRuntime) Name() string    { return "mutating" }
func (m *mutatingRuntime) Available() bool { return true }
func (m *mutatingRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (m *mutatingRuntime) Close(context.Context, string) error {
	if err := m.close(); err != nil {
		return err
	}
	m.closed = true
	return nil
}
func (m *mutatingRuntime) List(context.Context) ([]runtime.Session, error) {
	if m.closed {
		return nil, nil
	}
	return []runtime.Session{m.session}, nil
}
func (m *mutatingRuntime) Annotate(context.Context, string, map[string]string) error { return nil }

func TestRetireRevalidatesTaskIdentityAfterRuntimeClose(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	worktree := filepath.Join(filepath.Dir(r.Root), "feature")
	r.Git("worktree", "add", "-b", "feat/retire", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.GitIn(worktree, "add", "feature.txt")
	r.GitIn(worktree, "commit", "-m", "feat: retire")
	r.Git("merge", "--ff-only", "feat/retire")

	store := task.NewStore(t.TempDir())
	record := &task.Task{
		ID: "repo__feat-retire", Name: "retire", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/retire", Base: "main", WorktreePath: worktree,
		Mode: task.ModeWorktree, State: task.Done,
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	rt := &mutatingRuntime{
		session: runtime.Session{Handle: "w1", Panes: []runtime.Pane{{ID: "p1", CWD: worktree, Agent: "codex", AgentStatus: "idle"}}},
		close: func() error {
			current, err := store.Get(record.ID)
			if err != nil {
				return err
			}
			current.State = task.Warm
			return store.Save(current)
		},
	}
	service := &retire.Service{Runtime: rt, Tasks: store}
	_, err := service.Retire(context.Background(), retire.Request{
		Target: retire.Target{
			TaskID: record.ID, RepoPath: r.Root, CheckoutPath: worktree,
			Branch: record.Branch, Base: record.Base, LinkedWorktree: true,
		},
		Safety: retire.Options{CWD: t.TempDir()},
	})
	if err == nil || !strings.Contains(err.Error(), "changed state") {
		t.Fatalf("task mutation should stop retirement: %v", err)
	}
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("worktree was removed after task identity changed: %v", statErr)
	}
}
