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
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
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

func TestParkNamedDoneOrColdRejectedBeforeEffects(t *testing.T) {
	for _, state := range []task.State{task.Done, task.Cold} {
		t.Run(string(state), func(t *testing.T) {
			r := gittest.New(t)
			branch := "feat/park-" + string(state)
			r.Git("branch", branch, "main")
			cfg := lifecycleConfig(t)
			cfg.Paths.StateDir = filepath.Join(t.TempDir(), "state")
			store := task.NewStore(cfg.TasksDir())
			candidate := &task.Task{
				Name: "park " + string(state), Repo: "repo", RepoPath: r.Root,
				Branch: branch, Base: "main", Mode: task.ModeWorktree,
				State: state, Owner: config.Hostname(),
			}
			if err := store.Save(candidate); err != nil {
				t.Fatal(err)
			}
			before, err := store.GetRecord(candidate.ID)
			if err != nil {
				t.Fatal(err)
			}
			head := r.Git("rev-parse", "HEAD")
			rt := &activityRuntime{}
			app := &App{Cfg: cfg, Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: rt}
			cmd := newParkCmd(app)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{candidate.ID})
			err = cmd.Execute()
			if !errors.Is(err, taskflow.ErrInvalidTransition) {
				t.Fatalf("named %s park error = %v, want ErrInvalidTransition", state, err)
			}
			after, getErr := store.GetRecord(candidate.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if after.Revision != before.Revision || after.Task.State != state {
				t.Fatalf("named %s park changed task: before=%+v after=%+v", state, before, after)
			}
			if got := r.Git("rev-parse", "HEAD"); got != head {
				t.Fatalf("named %s park changed Git HEAD from %s to %s", state, head, got)
			}
			if rt.openCalls != 0 || len(rt.closeCalls) != 0 || rt.activityCalls != 0 {
				t.Fatalf("named %s park reached runtime effects: open=%d close=%v activity=%d",
					state, rt.openCalls, rt.closeCalls, rt.activityCalls)
			}
		})
	}
}

func TestResumeNamedDoneOrHotRejectedBeforeEffects(t *testing.T) {
	for _, state := range []task.State{task.Done, task.Hot} {
		t.Run(string(state), func(t *testing.T) {
			r := gittest.New(t)
			cfg := lifecycleConfig(t)
			cfg.Paths.StateDir = filepath.Join(t.TempDir(), "state")
			store := task.NewStore(cfg.TasksDir())
			candidate := &task.Task{
				Name: "resume " + string(state), Repo: "repo", RepoPath: r.Root,
				Branch: "main", Base: "main", Mode: task.ModeDirect,
				State: state, Owner: config.Hostname(),
			}
			if err := store.Save(candidate); err != nil {
				t.Fatal(err)
			}
			before, err := store.GetRecord(candidate.ID)
			if err != nil {
				t.Fatal(err)
			}
			head := r.Git("rev-parse", "HEAD")
			rt := &activityRuntime{}
			app := &App{Cfg: cfg, Tasks: store, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, runtimeInstance: rt}
			cmd := newResumeCmd(app)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{candidate.ID, "--fetch=false"})
			err = cmd.Execute()
			if !errors.Is(err, taskflow.ErrInvalidTransition) {
				t.Fatalf("named %s resume error = %v, want ErrInvalidTransition", state, err)
			}
			after, getErr := store.GetRecord(candidate.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if after.Revision != before.Revision || after.Task.State != state {
				t.Fatalf("named %s resume changed task: before=%+v after=%+v", state, before, after)
			}
			if got := r.Git("rev-parse", "HEAD"); got != head {
				t.Fatalf("named %s resume changed Git HEAD from %s to %s", state, head, got)
			}
			if rt.openCalls != 0 || len(rt.closeCalls) != 0 || rt.activityCalls != 0 {
				t.Fatalf("named %s resume reached runtime effects: open=%d close=%v activity=%d",
					state, rt.openCalls, rt.closeCalls, rt.activityCalls)
			}
		})
	}
}

type lifecycleCommandFixture struct {
	repo   *gittest.Repo
	store  *task.Store
	app    *App
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

func newLifecycleCommandFixture(t *testing.T, rt runtime.Runtime) *lifecycleCommandFixture {
	t.Helper()
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("TMUX_PANE", "")
	cfg := lifecycleConfig(t)
	cfg.Paths.StateDir = filepath.Join(t.TempDir(), "state")
	store := task.NewStore(cfg.TasksDir())
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	if rt == nil {
		rt = &activityRuntime{name: "test"}
	}
	return &lifecycleCommandFixture{
		repo: gittest.New(t), store: store, out: out, errOut: errOut,
		app: &App{Cfg: cfg, Tasks: store, Out: out, Err: errOut, runtimeInstance: rt},
	}
}

func addLifecycleLinkedCheckout(t *testing.T, repository *gittest.Repo, branch string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "checkout")
	repository.Git("branch", branch, "main")
	repository.Git("worktree", "add", path, branch)
	return path
}

func runFocusedRetire(app *App, args ...string) error {
	cmd := newRetireCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func runFocusedWtRemove(app *App, args ...string) error {
	cmd := newWtRemoveCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func assertTaskAbsent(t *testing.T, store *task.Store, id string) {
	t.Helper()
	if _, err := store.GetRecord(id); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("task %s lookup = %v, want ErrNotFound", id, err)
	}
}

func assertCheckoutRegistered(t *testing.T, repoPath, checkout string) {
	t.Helper()
	if _, err := gitx.ResolveRegisteredWorktree(context.Background(), repoPath, checkout); err != nil {
		t.Fatalf("checkout %s is not registered: %v", checkout, err)
	}
}

func TestRetireTaskflowWorktreeSuccessRendersLedgerAndRetiredSummary(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/retire-success"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	candidate := &task.Task{
		Name: "retire success", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Done, Owner: config.Hostname(),
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}

	if err := runFocusedRetire(fixture.app, candidate.ID); err != nil {
		t.Fatalf("retire task: %v\nstderr: %s", err, fixture.errOut.String())
	}
	assertTaskAbsent(t, fixture.store, candidate.ID)
	if _, err := gitx.ResolveRegisteredWorktree(context.Background(), fixture.repo.Root, checkout); !errors.Is(err, gitx.ErrWorktreeNotFound) {
		t.Fatalf("retired checkout lookup = %v, want ErrWorktreeNotFound", err)
	}
	if !gitx.BranchExists(context.Background(), fixture.repo.Root, branch) {
		t.Fatal("default task retirement deleted the retained branch")
	}
	for _, want := range []string{"RETIRED", "removed", checkout, "task", candidate.ID, "reaped"} {
		if !strings.Contains(fixture.out.String(), want) {
			t.Errorf("retire output missing %q:\n%s", want, fixture.out.String())
		}
	}
}

func TestRetireTaskflowNoCheckoutDirectAndBranchModes(t *testing.T) {
	tests := []struct {
		name   string
		mode   task.CheckoutMode
		branch string
	}{
		{name: "done worktree without checkout", mode: task.ModeWorktree, branch: "feat/no-checkout"},
		{name: "direct", mode: task.ModeDirect, branch: "main"},
		{name: "branch", mode: task.ModeBranch, branch: "feat/branch-mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newLifecycleCommandFixture(t, nil)
			if tt.branch != "main" {
				fixture.repo.Git("branch", tt.branch, "main")
			}
			candidate := &task.Task{
				Name: tt.name, Repo: "repo", RepoPath: fixture.repo.Root,
				Branch: tt.branch, Base: "main", Mode: tt.mode, State: task.Done,
				Owner: config.Hostname(),
			}
			if err := fixture.store.Save(candidate); err != nil {
				t.Fatal(err)
			}
			if err := runFocusedRetire(fixture.app, candidate.ID); err != nil {
				t.Fatalf("retire %s: %v\nstderr: %s", tt.name, err, fixture.errOut.String())
			}
			assertTaskAbsent(t, fixture.store, candidate.ID)
			if _, err := os.Stat(filepath.Join(fixture.repo.Root, "README.md")); err != nil {
				t.Fatalf("retirement removed canonical checkout: %v", err)
			}
			if !gitx.BranchExists(context.Background(), fixture.repo.Root, tt.branch) {
				t.Fatalf("retirement deleted retained branch %s", tt.branch)
			}
			if !strings.Contains(fixture.out.String(), "RETIRED") || !strings.Contains(fixture.out.String(), candidate.ID+" reaped") {
				t.Fatalf("retirement output:\n%s", fixture.out.String())
			}
		})
	}
}

func TestRetireTaskflowExplicitDeleteBranchUsesTypedPlanToken(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/delete-after-retire"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	candidate := &task.Task{
		Name: "delete branch", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Done, Owner: config.Hostname(),
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}

	if err := runFocusedRetire(fixture.app, candidate.ID, "--delete-branch"); err != nil {
		t.Fatalf("retire with branch deletion: %v\nstderr: %s", err, fixture.errOut.String())
	}
	assertTaskAbsent(t, fixture.store, candidate.ID)
	if gitx.BranchExists(context.Background(), fixture.repo.Root, branch) {
		t.Fatal("explicitly deleted contained branch still exists")
	}
	if !strings.Contains(fixture.out.String(), "branch") || !strings.Contains(fixture.out.String(), branch+" deleted") {
		t.Fatalf("branch deletion step not rendered:\n%s", fixture.out.String())
	}
}

type stagedCloseRuntime struct {
	*activityRuntime
	failAt int
	calls  int
}

func (r *stagedCloseRuntime) Close(_ context.Context, handle string) error {
	r.calls++
	r.closeCalls = append(r.closeCalls, handle)
	if r.failAt > 0 && r.calls == r.failAt {
		return errors.New("injected runtime close failure")
	}
	for index, session := range r.sessions {
		if session.Handle == handle {
			r.sessions = append(r.sessions[:index], r.sessions[index+1:]...)
			break
		}
	}
	return nil
}

func TestRetireTaskflowPartialRuntimeErrorKeepsTaskAndCheckout(t *testing.T) {
	baseRuntime := &activityRuntime{}
	rt := &stagedCloseRuntime{activityRuntime: baseRuntime, failAt: 2}
	fixture := newLifecycleCommandFixture(t, rt)
	branch := "feat/partial-retire"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	rt.sessions = []runtime.Session{
		{Handle: "w1", Dirs: []string{checkout}, AgentStatus: "idle"},
		{Handle: "w2", Dirs: []string{checkout}, AgentStatus: "done"},
	}
	candidate := &task.Task{
		Name: "partial retire", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Done, Owner: config.Hostname(),
		RuntimeName: "herdr", RuntimeHandle: "w1",
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}

	err := runFocusedRetire(fixture.app, candidate.ID, "--timeout", "1s")
	if err == nil || !strings.Contains(err.Error(), "injected runtime close failure") {
		t.Fatalf("partial retire error = %v", err)
	}
	if len(rt.closeCalls) != 2 {
		t.Fatalf("runtime close calls = %v, want one completed then one failed", rt.closeCalls)
	}
	if _, err := fixture.store.GetRecord(candidate.ID); err != nil {
		t.Fatalf("partial retirement removed task: %v", err)
	}
	assertCheckoutRegistered(t, fixture.repo.Root, checkout)
	if !strings.Contains(fixture.errOut.String(), "failed") || !strings.Contains(fixture.errOut.String(), "recovery") {
		t.Fatalf("partial ledger/recovery not rendered:\n%s", fixture.errOut.String())
	}
}

func TestRetireTaskflowRejectsInvalidStateBeforeEffects(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/not-done"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	candidate := &task.Task{
		Name: "not done", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Hot, Owner: config.Hostname(),
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.store.GetRecord(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}

	err = runFocusedRetire(fixture.app, candidate.ID)
	if !errors.Is(err, taskflow.ErrInvalidTransition) || !strings.Contains(err.Error(), "run dev done first") {
		t.Fatalf("invalid-state retirement error = %v", err)
	}
	after, getErr := fixture.store.GetRecord(candidate.ID)
	if getErr != nil || after.Revision != before.Revision {
		t.Fatalf("invalid-state retirement changed task: before=%+v after=%+v err=%v", before, after, getErr)
	}
	assertCheckoutRegistered(t, fixture.repo.Root, checkout)
}

func TestRetireExactPathMappedToDoneTaskUsesTaskflow(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/path-mapped"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	candidate := &task.Task{
		Name: "path mapped", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Done, Owner: config.Hostname(),
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}

	if err := runFocusedRetire(fixture.app, checkout); err != nil {
		t.Fatalf("retire mapped path: %v\nstderr: %s", err, fixture.errOut.String())
	}
	assertTaskAbsent(t, fixture.store, candidate.ID)
	if !strings.Contains(fixture.out.String(), candidate.ID+" reaped") {
		t.Fatalf("mapped path did not render taskflow task reap:\n%s", fixture.out.String())
	}
}

func TestRetireCanonicalPathDoesNotMatchDoneTaskWithoutCheckout(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/no-checkout"
	fixture.repo.Git("branch", branch)
	candidate := &task.Task{
		Name: "no checkout", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", Mode: task.ModeWorktree,
		State: task.Done, Owner: config.Hostname(),
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}

	err := runFocusedRetire(fixture.app, fixture.repo.Root)
	if err == nil || !strings.Contains(err.Error(), "not a linked worktree") {
		t.Fatalf("canonical path retirement error = %v", err)
	}
	persisted, getErr := fixture.store.Get(candidate.ID)
	if getErr != nil || persisted.State != task.Done || persisted.WorktreePath != "" {
		t.Fatalf("canonical path reaped unrelated no-checkout task: %+v, %v", persisted, getErr)
	}
}

func TestRetireUnmanagedPathCompatibilityPreservesContainmentAndBranchDefaults(t *testing.T) {
	t.Run("retains contained branch by default", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/unmanaged-retain"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		if err := runFocusedRetire(fixture.app, checkout); err != nil {
			t.Fatalf("retire unmanaged path: %v", err)
		}
		if !gitx.BranchExists(context.Background(), fixture.repo.Root, branch) {
			t.Fatal("unmanaged path retirement deleted branch by default")
		}
		if !strings.Contains(fixture.out.String(), "RETIRED") || !strings.Contains(fixture.out.String(), "worktree") {
			t.Fatalf("legacy unmanaged output changed:\n%s", fixture.out.String())
		}
	})

	t.Run("explicitly deletes contained branch", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/unmanaged-delete"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		if err := runFocusedRetire(fixture.app, checkout, "--delete-branch"); err != nil {
			t.Fatalf("retire unmanaged path with branch deletion: %v", err)
		}
		if gitx.BranchExists(context.Background(), fixture.repo.Root, branch) {
			t.Fatal("explicit unmanaged branch deletion did not run")
		}
	})

	t.Run("refuses uncontained branch", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/unmanaged-uncontained"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		if err := os.WriteFile(filepath.Join(checkout, "unique.txt"), []byte("unique\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fixture.repo.GitIn(checkout, "add", "unique.txt")
		fixture.repo.GitIn(checkout, "commit", "-m", "test: unique unmanaged commit")
		err := runFocusedRetire(fixture.app, checkout)
		if err == nil || !strings.Contains(err.Error(), "not contained") {
			t.Fatalf("uncontained unmanaged retirement error = %v", err)
		}
		assertCheckoutRegistered(t, fixture.repo.Root, checkout)
		if !gitx.BranchExists(context.Background(), fixture.repo.Root, branch) {
			t.Fatal("refused unmanaged retirement deleted branch")
		}
	})
}

func TestWtRemoveTaskflowCleanAndForcedRemovalPreserveBranch(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/wt-clean"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		head := fixture.repo.Git("rev-parse", branch)
		if err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root); err != nil {
			t.Fatalf("wt rm clean: %v\nstderr: %s", err, fixture.errOut.String())
		}
		if got := fixture.repo.Git("rev-parse", branch); got != head {
			t.Fatalf("preserved branch moved from %s to %s", head, got)
		}
		if _, err := gitx.ResolveRegisteredWorktree(context.Background(), fixture.repo.Root, checkout); !errors.Is(err, gitx.ErrWorktreeNotFound) {
			t.Fatalf("clean removed checkout lookup = %v", err)
		}
		if !strings.Contains(fixture.out.String(), "branch "+branch+" kept") {
			t.Fatalf("branch-kept summary missing:\n%s", fixture.out.String())
		}
	})

	t.Run("force uses typed discard", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/wt-force"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		head := fixture.repo.Git("rev-parse", branch)
		if err := os.WriteFile(filepath.Join(checkout, "discard-me.txt"), []byte("only copy\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root, "--force"); err != nil {
			t.Fatalf("wt rm --force: %v\nstderr: %s", err, fixture.errOut.String())
		}
		if got := fixture.repo.Git("rev-parse", branch); got != head {
			t.Fatalf("forced removal moved branch from %s to %s", head, got)
		}
		if !strings.Contains(fixture.out.String(), "discarded") || !strings.Contains(fixture.out.String(), "branch "+branch+" kept") {
			t.Fatalf("typed discard/removal ledger missing:\n%s", fixture.out.String())
		}
	})
}

func TestWtRemoveRejectsAmbiguousDuplicateBranchCheckouts(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/wt-duplicate"
	first := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	second := filepath.Join(filepath.Dir(fixture.repo.Root), "duplicate-checkout")
	fixture.repo.Git("worktree", "add", "--force", second, branch)

	err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root)
	if err == nil || !strings.Contains(err.Error(), "has 2 registered worktrees") ||
		!strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("ambiguous worktree removal error = %v", err)
	}
	assertCheckoutRegistered(t, fixture.repo.Root, first)
	assertCheckoutRegistered(t, fixture.repo.Root, second)
}

func TestWtRemoveTaskClaimBlocksWithoutMetadataDrift(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/wt-claimed"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	candidate := &task.Task{
		Name: "claimed", Repo: "repo", RepoPath: fixture.repo.Root,
		Branch: branch, Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Warm, Owner: config.Hostname(),
		RuntimeName: "herdr", RuntimeHandle: "w1",
	}
	if err := fixture.store.Save(candidate); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.store.GetRecord(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}

	err = runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root)
	if err == nil || !strings.Contains(err.Error(), "claimed by task metadata") ||
		!strings.Contains(err.Error(), "park/dev retire") || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("task-claim removal error = %v", err)
	}
	after, getErr := fixture.store.GetRecord(candidate.ID)
	if getErr != nil || after.Revision != before.Revision || after.Task.WorktreePath != checkout || after.Task.State != task.Warm {
		t.Fatalf("blocked wt rm drifted metadata: before=%+v after=%+v err=%v", before, after, getErr)
	}
	assertCheckoutRegistered(t, fixture.repo.Root, checkout)
}

func TestWtRemoveCorruptInventoryBlocks(t *testing.T) {
	fixture := newLifecycleCommandFixture(t, nil)
	branch := "feat/wt-corrupt-inventory"
	checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
	if err := os.MkdirAll(fixture.store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.store.Dir, "broken.toml"), []byte("state = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root)
	if err == nil || !strings.Contains(err.Error(), "task inventory is incomplete") || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("corrupt-inventory removal error = %v", err)
	}
	assertCheckoutRegistered(t, fixture.repo.Root, checkout)
}

func TestWtRemoveHarnessCallerActiveUnknownAndCanonicalBlock(t *testing.T) {
	t.Run("harness", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/wt-harness"
		checkout := filepath.Join(fixture.repo.Root, ".claude", "worktrees", "harness")
		if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
			t.Fatal(err)
		}
		fixture.repo.Git("branch", branch, "main")
		fixture.repo.Git("worktree", "add", checkout, branch)
		err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root)
		if err == nil || !strings.Contains(err.Error(), "harness") {
			t.Fatalf("harness removal error = %v", err)
		}
		assertCheckoutRegistered(t, fixture.repo.Root, checkout)
	})

	t.Run("caller", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		branch := "feat/wt-caller"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(checkout); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(cwd) })
		err = runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root)
		if err == nil || !strings.Contains(err.Error(), "caller is inside") {
			t.Fatalf("caller-contained removal error = %v", err)
		}
		assertCheckoutRegistered(t, fixture.repo.Root, checkout)
	})

	for _, status := range []string{"working", "unknown"} {
		t.Run(status+" agent", func(t *testing.T) {
			rt := &activityRuntime{}
			fixture := newLifecycleCommandFixture(t, rt)
			branch := "feat/wt-" + status
			checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
			rt.sessions = []runtime.Session{{Handle: "w1", Dirs: []string{checkout}, AgentStatus: status}}
			err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), status) {
				t.Fatalf("%s-agent removal error = %v", status, err)
			}
			if len(rt.closeCalls) != 0 {
				t.Fatalf("blocked %s agent was closed: %v", status, rt.closeCalls)
			}
			assertCheckoutRegistered(t, fixture.repo.Root, checkout)
		})
	}

	t.Run("canonical", func(t *testing.T) {
		fixture := newLifecycleCommandFixture(t, nil)
		err := runFocusedWtRemove(fixture.app, "main", "--repo", fixture.repo.Root)
		if err == nil || !strings.Contains(err.Error(), "main=true") {
			t.Fatalf("canonical removal error = %v", err)
		}
		assertCheckoutRegistered(t, fixture.repo.Root, fixture.repo.Root)
	})
}

func TestWtRemoveRuntimeCompatibilityAcknowledgements(t *testing.T) {
	t.Run("close unknown", func(t *testing.T) {
		rt := &stagedCloseRuntime{activityRuntime: &activityRuntime{}}
		fixture := newLifecycleCommandFixture(t, rt)
		branch := "feat/wt-close-unknown"
		checkout := addLifecycleLinkedCheckout(t, fixture.repo, branch)
		rt.sessions = []runtime.Session{{Handle: "w1", Dirs: []string{checkout}, AgentStatus: "unknown"}}
		if err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root,
			"--close-unknown", "--timeout", "1s"); err != nil {
			t.Fatalf("close-unknown removal: %v\nstderr: %s", err, fixture.errOut.String())
		}
		if len(rt.closeCalls) != 1 || rt.closeCalls[0] != "w1" {
			t.Fatalf("close-unknown calls = %v", rt.closeCalls)
		}
	})

	t.Run("assume no runtime", func(t *testing.T) {
		rt := &activityRuntime{listErr: errors.New("runtime inventory unavailable")}
		fixture := newLifecycleCommandFixture(t, rt)
		branch := "feat/wt-assume-none"
		addLifecycleLinkedCheckout(t, fixture.repo, branch)
		if err := runFocusedWtRemove(fixture.app, branch, "--repo", fixture.repo.Root,
			"--assume-no-runtime"); err != nil {
			t.Fatalf("assume-no-runtime removal: %v\nstderr: %s", err, fixture.errOut.String())
		}
	})
}
