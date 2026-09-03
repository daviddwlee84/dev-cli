package taskflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

var lifecycleSeed struct {
	sync.Once
	root string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if lifecycleSeed.root != "" {
		_ = os.RemoveAll(lifecycleSeed.root)
	}
	os.Exit(code)
}

func lifecycleFixtureSeed(t *testing.T) string {
	t.Helper()
	lifecycleSeed.Do(func() {
		lifecycleSeed.root, lifecycleSeed.err = os.MkdirTemp("", "dev-taskflow-fixture-seed-")
		if lifecycleSeed.err != nil {
			return
		}
		remote := filepath.Join(lifecycleSeed.root, "origin.git")
		repository := filepath.Join(lifecycleSeed.root, "example")
		if lifecycleSeed.err = os.MkdirAll(repository, 0o755); lifecycleSeed.err != nil {
			return
		}
		commands := []struct {
			dir  string
			args []string
		}{
			{lifecycleSeed.root, []string{"init", "--bare", remote}},
			{lifecycleSeed.root, []string{"init", "-b", "main", repository}},
			{repository, []string{"config", "user.email", "taskflow@example.test"}},
			{repository, []string{"config", "user.name", "Taskflow Test"}},
			{repository, []string{"add", "tracked.txt"}},
			{repository, []string{"commit", "-m", "initial"}},
			{repository, []string{"remote", "add", "origin", "../origin.git"}},
			{repository, []string{"push", "-u", "origin", "main"}},
			{repository, []string{"remote", "set-head", "origin", "main"}},
			{repository, []string{"branch", "feature", "main"}},
			{repository, []string{"push", "-u", "origin", "feature"}},
		}
		if writeErr := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("main\n"), 0o644); writeErr != nil {
			lifecycleSeed.err = writeErr
			return
		}
		for _, command := range commands {
			if lifecycleSeed.err = runLifecycleSeedGit(command.dir, command.args...); lifecycleSeed.err != nil {
				return
			}
		}
	})
	if lifecycleSeed.err != nil {
		t.Fatalf("create lifecycle fixture seed: %v", lifecycleSeed.err)
	}
	return lifecycleSeed.root
}

func runLifecycleSeedGit(dir string, args ...string) error {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if args[0] == "init" {
		command = exec.Command("git", args...)
		command.Dir = dir
	}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git in %s %s: %w: %s", dir, strings.Join(args, " "), err, output)
	}
	return nil
}

func copyLifecycleFixtureTree(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, info.Mode().Perm())
	})
}

type lifecycleObservedEmptyRuntime struct{}

func (lifecycleObservedEmptyRuntime) Name() string    { return "test" }
func (lifecycleObservedEmptyRuntime) Available() bool { return true }
func (lifecycleObservedEmptyRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{Handle: "test-runtime", Opened: true, Created: true}, nil
}
func (lifecycleObservedEmptyRuntime) Close(context.Context, string) error { return nil }
func (lifecycleObservedEmptyRuntime) List(context.Context) ([]runtime.Session, error) {
	return nil, nil
}
func (lifecycleObservedEmptyRuntime) Annotate(context.Context, string, map[string]string) error {
	return nil
}
func (lifecycleObservedEmptyRuntime) AgentActivities(context.Context) ([]runtime.AgentActivity, error) {
	return nil, nil
}

type lifecycleGitFixture struct {
	root      string
	repo      string
	remote    string
	worktree  string
	cfg       config.Config
	tasks     *task.Store
	artifacts *artifact.Store
	record    task.Record
	service   *Service
}

func newLifecycleGitFixture(t *testing.T, mode task.CheckoutMode, state task.State) *lifecycleGitFixture {
	t.Helper()
	root := t.TempDir()
	seed := lifecycleFixtureSeed(t)
	remote := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "example")
	if err := copyLifecycleFixtureTree(filepath.Join(seed, "origin.git"), remote); err != nil {
		t.Fatalf("copy lifecycle fixture remote: %v", err)
	}
	if err := copyLifecycleFixtureTree(filepath.Join(seed, "example"), repo); err != nil {
		t.Fatalf("copy lifecycle fixture repository: %v", err)
	}
	worktree := filepath.Join(root, "feature-checkout")
	branch := "feature"
	checkout := repo
	switch mode {
	case task.ModeWorktree:
		if state != task.Cold {
			mustGitCommand(t, repo, "worktree", "add", worktree, branch)
			checkout = worktree
		} else {
			worktree = ""
		}
	case task.ModeBranch:
		worktree = ""
		if state != task.Cold {
			mustGitCommand(t, repo, "switch", branch)
		}
	case task.ModeDirect:
		worktree = ""
		branch = "main"
	default:
		t.Fatalf("unsupported fixture mode %s", mode)
	}

	cfg := config.Default()
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Paths.WorktreeRoot = filepath.Join(root, "managed")
	cfg.Paths.WorktreePath = filepath.Join(root, "managed", "{{repo}}", "{{branch|slug}}")
	tasks := task.NewStore(cfg.TasksDir())
	artifacts := artifact.NewStore(filepath.Join(cfg.StateDir(), "artifact-intents", "v1"))
	candidate := task.Task{
		Name: "feature work", Repo: "example", RepoPath: repo,
		Branch: branch, Base: "main", WorktreePath: worktree,
		Mode: mode, State: state, Owner: "test-host", Next: "continue",
	}
	created, err := tasks.Create(context.Background(), &candidate)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	observedRuntime := lifecycleObservedEmptyRuntime{}
	service, err := NewLifecycleService(LifecycleConfig{
		Config: cfg, Tasks: tasks, Artifacts: artifacts,
		DefaultRuntime: func() runtime.Runtime { return observedRuntime },
		NamedRuntime:   func(string) runtime.Runtime { return observedRuntime },
		Host:           "test-host", CWD: root,
		CallerWorkspaceID: "test-caller-workspace", CallerPaneID: "test-caller-pane",
		Clock: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("NewLifecycleService: %v", err)
	}
	return &lifecycleGitFixture{
		root: root, repo: repo, remote: remote, worktree: checkout,
		cfg: cfg, tasks: tasks, artifacts: artifacts, record: *created, service: service,
	}
}

func (f *lifecycleGitFixture) request(t *testing.T, options ActionOptions) Request {
	t.Helper()
	record, err := f.tasks.GetRecord(f.record.Task.ID)
	if err != nil {
		t.Fatalf("load task record: %v", err)
	}
	f.record = *record
	repository, err := gitx.Discover(context.Background(), record.Task.RepoPath)
	if err != nil {
		t.Fatalf("discover fixture repository: %v", err)
	}
	mode := record.Task.EffectiveMode()
	checkout := record.Task.RepoPath
	if mode == task.ModeWorktree {
		checkout = record.Task.WorktreePath
	}
	locator := Locator{
		RepoKey: repository.GitCommonDir, RepositoryID: repository.GitCommonDir,
		GitCommonDir: repository.GitCommonDir,
		TaskID:       record.Task.ID, TaskRevision: record.Revision,
		RepoPath: record.Task.RepoPath, CheckoutPath: checkout,
		Branch: record.Task.Branch, Base: record.Task.Base,
		Mode: mode, State: record.Task.State,
	}
	if checkout != "" {
		status, statusErr := gitx.StatusOf(context.Background(), checkout)
		if statusErr != nil {
			t.Fatalf("fixture status: %v", statusErr)
		}
		locator.Upstream = status.Upstream
		locator.HeadOID = strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", "HEAD"))
		if record.Task.Base != "" {
			locator.BaseOID = strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", record.Task.Base+"^{commit}"))
		}
		if status.Upstream != "" {
			locator.UpstreamOID = strings.TrimSpace(mustGitCommand(t, checkout, "rev-parse", status.Upstream+"^{commit}"))
		}
	}
	request, err := NewRequest(locator, options)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return request
}

func TestLifecycleRealGitWarmParkPreservesDirtyBytes(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	dirty := filepath.Join(fixture.worktree, "tracked.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ParkWarmOptions{Next: "return later"}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability = %s; conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectUpdateTask}) {
		t.Fatalf("effects = %v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, readErr := os.ReadFile(dirty); readErr != nil || string(got) != "uncommitted\n" {
		t.Fatalf("dirty bytes = %q err=%v", got, readErr)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Warm || updated.Next != "return later" || updated.WorktreePath == "" {
		t.Fatalf("updated task = %+v", updated)
	}
	if len(result.Warnings()) == 0 || len(result.CompletedSteps()) != 1 {
		t.Fatalf("result warnings=%v steps=%+v", result.Warnings(), result.AttemptedSteps())
	}
}

func TestLifecycleRealGitWarmParkWIPAndPush(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), []byte("checkpoint\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ParkWarmOptions{
		Next: "run tests", CommitWIP: true, Push: true,
	}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectCommitWIP, EffectPushBranch, EffectUpdateTask}) {
		t.Fatalf("effects = %v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v; result=%+v", err, result.AttemptedSteps())
	}
	status, _ := gitx.StatusOf(context.Background(), fixture.worktree)
	if status.Dirty() || status.Ahead != 0 || !status.Published() {
		t.Fatalf("post-park status = %+v", status)
	}
	message := mustGitCommand(t, fixture.worktree, "log", "-1", "--format=%s")
	if !strings.Contains(message, "wip: checkpoint") || !strings.Contains(message, "run tests") {
		t.Fatalf("commit message = %q", message)
	}
	if len(result.CompletedSteps()) != 3 {
		t.Fatalf("steps = %+v", result.AttemptedSteps())
	}
}

func TestLifecycleRealGitColdWorktreePark(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	checkout := fixture.worktree
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ParkColdOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability = %s; conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectRemoveWorktree, EffectUpdateTask}) {
		t.Fatalf("effects = %v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v; result=%+v", err, result.AttemptedSteps())
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if !gitx.BranchExists(context.Background(), fixture.repo, "feature") {
		t.Fatal("cold parking deleted the branch")
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Cold || updated.WorktreePath != "" || updated.RuntimeHandle != "" {
		t.Fatalf("updated task = %+v", updated)
	}
}

func TestLifecycleRealGitBranchColdSwitch(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeBranch, task.Hot)
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ParkColdOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability = %s; conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectSwitchBase, EffectUpdateTask}) {
		t.Fatalf("effects = %v", got)
	}
	if _, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	status, _ := gitx.StatusOf(context.Background(), fixture.repo)
	if status.Branch != "main" {
		t.Fatalf("canonical branch = %q", status.Branch)
	}
	if !gitx.BranchExists(context.Background(), fixture.repo, "feature") {
		t.Fatal("branch cold park deleted feature")
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Cold {
		t.Fatalf("state = %s", updated.State)
	}
}

func TestLifecycleRealGitWarmResumeHandoff(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ResumeOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectOpenRuntime, EffectUpdateTask}) {
		t.Fatalf("effects = %v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	handoff, ok := result.Handoff()
	wantPath, _ := pathx.Canonical(fixture.worktree)
	if !ok || handoff.Kind != HandoffRuntime || handoff.Runtime != "test" || handoff.RuntimeHandle != "test-runtime" || handoff.Path != wantPath {
		t.Fatalf("handoff = %+v present=%t wantPath=%q", handoff, ok, wantPath)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot || updated.Owner != "test-host" {
		t.Fatalf("updated task = %+v", updated)
	}
}

func TestLifecycleRealGitWarmMissingWorktreeRebuildsRecordedPath(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
	recordedPath, err := pathx.Canonical(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	mustGitCommand(t, fixture.repo, "worktree", "remove", recordedPath)

	locator, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
	if err != nil {
		t.Fatalf("LocateTask: %v", err)
	}
	request, err := NewRequest(locator, ResumeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectCreateWorktree, EffectOpenRuntime, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v; steps=%+v", err, result.AttemptedSteps())
	}
	updated, err := fixture.tasks.Get(fixture.record.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != task.Hot || updated.WorktreePath != recordedPath {
		t.Fatalf("updated task=%+v want rebuilt path=%q", updated, recordedPath)
	}
	registered, err := gitx.ResolveRegisteredWorktree(context.Background(), fixture.repo, recordedPath)
	if err != nil || registered.Worktree.Branch != "feature" {
		t.Fatalf("rebuilt registration=%+v err=%v", registered, err)
	}
}

func TestLifecycleRealGitColdResumeRejectsLocalBranchMoveAfterPlan(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	locator, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
	if err != nil {
		t.Fatalf("LocateTask: %v", err)
	}
	request, err := NewRequest(locator, ResumeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	parent := mustGitCommand(t, fixture.repo, "rev-parse", "feature^{commit}")
	tree := mustGitCommand(t, fixture.repo, "rev-parse", "feature^{tree}")
	moved := mustGitCommand(t, fixture.repo, "commit-tree", tree, "-p", parent, "-m", "move feature tip")
	mustGitCommand(t, fixture.repo, "update-ref", "refs/heads/feature", moved, parent)

	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Apply error=%v steps=%+v, want ErrStalePlan", err, result.AttemptedSteps())
	}
	if len(result.AttemptedSteps()) != 0 {
		t.Fatalf("stale apply performed effects: %+v", result.AttemptedSteps())
	}
	if _, statErr := os.Stat(plan.Effects()[0].Target); !os.IsNotExist(statErr) {
		t.Fatalf("stale apply created checkout %q: %v", plan.Effects()[0].Target, statErr)
	}
}

func TestLifecycleRealGitColdResumeDoesNotRecreateMissingTaskBranchFromBase(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	mustGitCommand(t, fixture.repo, "branch", "-D", "feature")
	mustGitCommand(t, fixture.remote, "update-ref", "-d", "refs/heads/feature")
	mustGitCommand(t, fixture.repo, "tag", "feature", "main")
	locator, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
	if err != nil {
		t.Fatalf("LocateTask: %v", err)
	}
	request, err := NewRequest(locator, ResumeOptions{FetchRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Apply error=%v steps=%+v, want ErrStalePlan", err, result.AttemptedSteps())
	}
	updated, getErr := fixture.tasks.Get(fixture.record.Task.ID)
	if getErr != nil || updated.State != task.Cold || updated.WorktreePath != "" {
		t.Fatalf("missing branch resume changed task=%+v err=%v", updated, getErr)
	}
	if gitx.BranchExists(context.Background(), fixture.repo, "feature") {
		t.Fatal("missing task branch was recreated from the base")
	}
}

func TestLifecycleRealGitColdWorktreeRebuild(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	plan, err := fixture.service.Plan(context.Background(), fixture.request(t, ResumeOptions{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability = %s; conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectCreateWorktree, EffectOpenRuntime, EffectUpdateTask}) {
		t.Fatalf("effects = %v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v; steps=%+v recovery=%v", err, result.AttemptedSteps(), result.Recovery())
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	if updated.State != task.Hot || updated.WorktreePath == "" {
		t.Fatalf("updated task = %+v", updated)
	}
	registered, err := gitx.ResolveRegisteredWorktree(context.Background(), fixture.repo, updated.WorktreePath)
	if err != nil || !registered.IsLinkedWorktree() || registered.Worktree.Branch != "feature" {
		t.Fatalf("rebuilt registration = %+v err=%v", registered, err)
	}
	handoff, ok := result.Handoff()
	if !ok || handoff.Kind != HandoffRuntime || handoff.Runtime != "test" || handoff.RuntimeHandle != "test-runtime" || handoff.Path != updated.WorktreePath {
		t.Fatalf("handoff = %+v present=%t", handoff, ok)
	}
}

func TestLifecycleRealGitColdResumeReusesFreshExactWorktree(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Cold)
	external := filepath.Join(fixture.root, "external-feature")
	mustGitCommand(t, fixture.repo, "worktree", "add", external, "feature")
	request := fixture.request(t, ResumeOptions{})
	status, err := gitx.StatusOf(context.Background(), external)
	if err != nil {
		t.Fatal(err)
	}
	request.Locator.Upstream = status.Upstream
	request.Locator.HeadOID = mustGitCommand(t, external, "rev-parse", "HEAD")
	request.Locator.BaseOID = mustGitCommand(t, external, "rev-parse", "main^{commit}")
	request.Locator.UpstreamOID = mustGitCommand(t, external, "rev-parse", status.Upstream+"^{commit}")
	plan, err := fixture.service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Availability != AvailabilityReady {
		t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
	}
	if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectOpenRuntime, EffectUpdateTask}) {
		t.Fatalf("effects=%v", got)
	}
	result, err := fixture.service.Apply(context.Background(), plan, Approve(plan.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	updated, _ := fixture.tasks.Get(fixture.record.Task.ID)
	canonical, _ := pathx.Canonical(external)
	if updated.State != task.Hot || updated.WorktreePath != canonical {
		t.Fatalf("updated task=%+v want path=%q", updated, canonical)
	}
	if handoff, ok := result.Handoff(); !ok || handoff.Path != canonical {
		t.Fatalf("handoff=%+v present=%t", handoff, ok)
	}
}

func effectCodes(plan Plan) []EffectCode {
	effects := plan.Effects()
	codes := make([]EffectCode, len(effects))
	for index, effect := range effects {
		codes[index] = effect.Code
	}
	return codes
}

func mustGitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var command *exec.Cmd
	if len(args) > 0 && args[0] == "init" && len(args) >= 3 && args[1] == "--bare" {
		command = exec.Command("git", args...)
		command.Dir = dir
	} else if len(args) > 0 && args[0] == "init" && len(args) >= 3 && args[1] == "-b" {
		command = exec.Command("git", args...)
		command.Dir = dir
	} else {
		commandArgs := append([]string{"-C", dir}, args...)
		command = exec.Command("git", commandArgs...)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git in %s %s: %v\n%s", dir, strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
