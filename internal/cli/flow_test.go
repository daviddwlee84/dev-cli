package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/flowtui"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type flowStaticResolver struct {
	rt    runtime.Runtime
	err   error
	calls int
}

func (r *flowStaticResolver) Resolve(context.Context) (runtime.Runtime, error) {
	r.calls++
	return r.rt, r.err
}

type flowTestRuntime struct {
	mu            sync.Mutex
	name          string
	available     bool
	sessions      []runtime.Session
	listErr       error
	listCalls     int
	listStarted   chan struct{}
	listCanceled  chan struct{}
	startOnce     sync.Once
	cancelOnce    sync.Once
	blockList     bool
	activated     []string
	activateCheck func() error
}

func (r *flowTestRuntime) Name() string {
	if r.name == "" {
		return "flow-test"
	}
	return r.name
}

func (r *flowTestRuntime) Available() bool { return r.available }
func (r *flowTestRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (r *flowTestRuntime) Close(context.Context, string) error { return nil }
func (r *flowTestRuntime) Annotate(context.Context, string, map[string]string) error {
	return nil
}
func (r *flowTestRuntime) List(ctx context.Context) ([]runtime.Session, error) {
	r.mu.Lock()
	r.listCalls++
	r.mu.Unlock()
	if r.blockList {
		r.startOnce.Do(func() {
			if r.listStarted != nil {
				close(r.listStarted)
			}
		})
		<-ctx.Done()
		r.cancelOnce.Do(func() {
			if r.listCanceled != nil {
				close(r.listCanceled)
			}
		})
		return nil, ctx.Err()
	}
	return append([]runtime.Session(nil), r.sessions...), r.listErr
}
func (r *flowTestRuntime) Activate(_ context.Context, handle string) error {
	if r.activateCheck != nil {
		if err := r.activateCheck(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.activated = append(r.activated, handle)
	r.mu.Unlock()
	return nil
}
func (r *flowTestRuntime) lists() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func newFlowTestApp(t *testing.T, roots []string, rt runtime.Runtime) *App {
	t.Helper()
	state := t.TempDir()
	cfg := config.Default()
	cfg.Paths.ScanRoots = append([]string(nil), roots...)
	cfg.Paths.RepoPaths = nil
	cfg.Paths.StateDir = state
	cfg.Paths.WorktreeRoot = filepath.Join(state, "worktrees")
	cfg.Paths.WorktreePath = "{{worktree_root}}/{{repo}}/{{branch|slug}}"
	cfg.Runtime.Backend = "none"
	if rt == nil {
		rt = runtime.None{}
	}
	return &App{
		Cfg: cfg, Tasks: task.NewStore(cfg.TasksDir()),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
		runtimeInstance: rt,
	}
}

func registerFlowTestRepository(t *testing.T, loader *flowLoader, path, focus string) flowRepository {
	t.Helper()
	discovered, err := gitx.Discover(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := availableFlowRepository(discovered, nil, focus)
	if err != nil {
		t.Fatal(err)
	}
	loader.registerRepository(repository, true)
	return repository
}

func saveFlowTask(t *testing.T, store *task.Store, tracked task.Task) task.Task {
	t.Helper()
	if err := store.Save(&tracked); err != nil {
		t.Fatal(err)
	}
	return tracked
}

func TestFlowLaunchResolvesCanonicalRepositoryAndExactCurrentSurface(t *testing.T) {
	repository := gittest.New(t)
	linked := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(context.Background(), repository.Root, linked, "feat/linked", "main"); err != nil {
		t.Fatal(err)
	}
	mainNested := filepath.Join(repository.Root, "pkg", "nested")
	linkedNested := filepath.Join(linked, "pkg", "nested")
	for _, path := range []string{mainNested, linkedNested} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	app := newFlowTestApp(t, nil, runtime.None{})

	for _, test := range []struct {
		name string
		cwd  string
		want string
	}{
		{name: "nested canonical", cwd: mainNested, want: repository.Root},
		{name: "nested linked", cwd: linkedNested, want: linked},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultFlowCommandDeps()
			deps.getwd = func() (string, error) { return test.cwd, nil }
			launch, err := resolveFlowLaunch(context.Background(), app, "", deps)
			if err != nil {
				t.Fatal(err)
			}
			mainCanonical, _ := pathx.Canonical(repository.Root)
			focusCanonical, _ := pathx.Canonical(test.want)
			if launch.preselected == nil || launch.repository == nil {
				t.Fatal("current repository was not preselected")
			}
			if launch.preselected.Path != mainCanonical || launch.preselected.FocusTarget != focusCanonical {
				t.Fatalf("preselection = %+v, want main=%q focus=%q", *launch.preselected, mainCanonical, focusCanonical)
			}
			if launch.preselected.RepoKey == "" || launch.preselected.RepoKey != launch.repository.repository.CommonDir {
				t.Fatalf("repository identity = %+v", *launch.preselected)
			}
		})
	}
}

func TestFlowLaunchOutsideGitUsesPickerAndExplicitReferenceWins(t *testing.T) {
	repository := gittest.New(t)
	outside := t.TempDir()
	app := newFlowTestApp(t, nil, runtime.None{})

	outsideDeps := defaultFlowCommandDeps()
	outsideDeps.getwd = func() (string, error) { return outside, nil }
	launch, err := resolveFlowLaunch(context.Background(), app, "", outsideDeps)
	if err != nil {
		t.Fatal(err)
	}
	if launch.preselected != nil || launch.repository != nil {
		t.Fatalf("outside Git should open picker, got %+v", launch)
	}

	var resolvedRef string
	var discoveredPaths []string
	explicitDeps := defaultFlowCommandDeps()
	explicitDeps.getwd = func() (string, error) { return outside, nil }
	explicitDeps.resolve = func(_ context.Context, _ []string, ref string) (repo.Repo, []repo.Repo, error) {
		resolvedRef = ref
		return repo.Repo{Name: "chosen", Path: repository.Root, HasGit: true}, nil, nil
	}
	explicitDeps.discover = func(ctx context.Context, path string) (gitx.Repo, error) {
		discoveredPaths = append(discoveredPaths, path)
		return gitx.Discover(ctx, path)
	}
	launch, err = resolveFlowLaunch(context.Background(), app, "chosen", explicitDeps)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedRef != "chosen" || len(discoveredPaths) != 1 || discoveredPaths[0] != repository.Root {
		t.Fatalf("explicit resolution ref=%q paths=%q", resolvedRef, discoveredPaths)
	}
	if launch.preselected == nil || launch.preselected.Name != "chosen" {
		t.Fatalf("explicit repository not selected: %+v", launch.preselected)
	}
}

func TestFlowLaunchSupportsUnconfiguredCurrentRepository(t *testing.T) {
	repository := gittest.New(t)
	app := newFlowTestApp(t, []string{t.TempDir()}, runtime.None{})
	deps := defaultFlowCommandDeps()
	deps.getwd = func() (string, error) { return repository.Root, nil }
	launch, err := resolveFlowLaunch(context.Background(), app, "", deps)
	if err != nil {
		t.Fatal(err)
	}
	if launch.preselected == nil || !launch.preselected.Available {
		t.Fatalf("unconfigured current repo was not inspectable: %+v", launch.preselected)
	}
}

func TestFlowRepositoryPickerMergesCommonDirectoryIdentityAndTaskOnlyRepositories(t *testing.T) {
	first := gittest.New(t)
	second := gittest.New(t)
	for _, branch := range []string{"feat/cold", "feat/done"} {
		first.Git("branch", branch)
	}
	rt := &flowTestRuntime{name: "none", available: true}
	app := newFlowTestApp(t, []string{filepath.Dir(first.Root), filepath.Dir(second.Root)}, rt)
	cold := saveFlowTask(t, app.Tasks, task.Task{
		ID: "first-cold", Name: "cold", Repo: "repo", RepoPath: first.Root,
		Branch: "feat/cold", Base: "main", Mode: task.ModeWorktree, State: task.Cold,
	})
	done := saveFlowTask(t, app.Tasks, task.Task{
		ID: "first-done", Name: "done", Repo: "repo", RepoPath: first.Root,
		Branch: "feat/done", Base: "main", Mode: task.ModeWorktree, State: task.Done,
	})
	missing := saveFlowTask(t, app.Tasks, task.Task{
		ID: "missing-cold", Name: "missing", Repo: "repo", RepoPath: filepath.Join(t.TempDir(), "gone"),
		Branch: "feat/missing", Base: "main", Mode: task.ModeWorktree, State: task.Cold,
	})

	loader := newFlowLoader(app, &flowStaticResolver{rt: rt}, first.Root)
	rows, err := loader.ListRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("picker rows = %d, want two same-name clones plus unavailable task repo: %+v", len(rows), rows)
	}
	availableKeys := map[string]bool{}
	var firstRow, unavailable flowtui.RepositoryRow
	firstCanonical, _ := pathx.Canonical(first.Root)
	for _, row := range rows {
		if row.Available {
			availableKeys[row.RepoKey] = true
			if row.Path == firstCanonical {
				firstRow = row
			}
		} else {
			unavailable = row
		}
	}
	if len(availableKeys) != 2 {
		t.Fatalf("same-name clones were basename-deduplicated: %+v", rows)
	}
	if unavailable.RepoKey == "" || unavailable.Available || !strings.Contains(unavailable.Error, "unavailable") {
		t.Fatalf("unavailable task repository row = %+v", unavailable)
	}

	snapshot, err := loader.LoadRepository(context.Background(), firstRow.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, row := range snapshot.Surfaces.Values() {
		seen[row.RowKey]++
	}
	if seen[cold.ID] != 1 || seen[done.ID] != 1 {
		t.Fatalf("COLD/DONE task-only rows = %+v", seen)
	}
	unavailableSnapshot, err := loader.LoadRepository(context.Background(), unavailable.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	if unavailableSnapshot.Repository.Available || unavailableSnapshot.Authoritative() || unavailableSnapshot.Surfaces.Len() != 1 ||
		unavailableSnapshot.Surfaces.Values()[0].RowKey != missing.ID {
		t.Fatalf("unavailable snapshot = %+v rows=%+v", unavailableSnapshot, unavailableSnapshot.Surfaces.Values())
	}
}

func TestFlowLoadCachesTaskRepositoryAndLocatorGitObservations(t *testing.T) {
	repository := gittest.New(t)
	rt := &flowTestRuntime{name: "flow-test", available: true}
	app := newFlowTestApp(t, nil, rt)
	const tasks = 6
	for index := 0; index < tasks; index++ {
		branch := fmt.Sprintf("feat/cold-%d", index)
		repository.Git("branch", branch)
		saveFlowTask(t, app.Tasks, task.Task{
			ID: fmt.Sprintf("cold-%d", index), Name: branch, Repo: "repo", RepoPath: repository.Root,
			Branch: branch, Base: "main", Mode: task.ModeWorktree, State: task.Cold,
		})
	}
	loader := newFlowLoader(app, &flowStaticResolver{rt: rt}, repository.Root)
	registered := registerFlowTestRepository(t, loader, repository.Root, "")
	discoverCalls, gitCalls := 0, 0
	loader.discover = func(ctx context.Context, path string) (gitx.Repo, error) {
		discoverCalls++
		return gitx.Discover(ctx, path)
	}
	loader.gitRun = func(ctx context.Context, path string, args ...string) (string, error) {
		gitCalls++
		return gitx.Run(ctx, path, args...)
	}

	snapshot, err := loader.LoadRepository(context.Background(), registered.row.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	if discoverCalls != 0 {
		t.Fatalf("same-path task loading repeated repository discovery %d times", discoverCalls)
	}
	if gitCalls > 2*tasks+1 {
		t.Fatalf("managed locator projection made %d Git calls for %d tasks", gitCalls, tasks)
	}
	managed := 0
	for _, row := range snapshot.Surfaces.Values() {
		if row.State == task.Cold && row.Locator.TaskRevision != "" {
			managed++
		}
	}
	if managed != tasks {
		t.Fatalf("managed task-only rows=%d want=%d rows=%+v", managed, tasks, snapshot.Surfaces.Values())
	}
}

func TestFlowProjectionIncludesEveryCheckoutOnceAndHonorsKindPrecedence(t *testing.T) {
	repository := gittest.New(t)
	managedPath := filepath.Join(t.TempDir(), "managed")
	externalPath := filepath.Join(t.TempDir(), "external")
	harnessPath := filepath.Join(repository.Root, ".claude", "worktrees", "turn-1")
	if err := os.MkdirAll(filepath.Dir(harnessPath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, branch string }{
		{managedPath, "feat/managed"}, {externalPath, "feat/external"}, {harnessPath, "worktree-turn-1"},
	} {
		if err := gitx.AddWorktree(context.Background(), repository.Root, item.path, item.branch, "main"); err != nil {
			t.Fatal(err)
		}
	}
	rt := &flowTestRuntime{name: "flow-test", available: true}
	app := newFlowTestApp(t, nil, rt)
	saveFlowTask(t, app.Tasks, task.Task{
		ID: "managed", Name: "managed", Repo: "repo", RepoPath: repository.Root,
		Branch: "feat/managed", Base: "main", WorktreePath: managedPath,
		Mode: task.ModeWorktree, State: task.Warm,
	})
	loader := newFlowLoader(app, &flowStaticResolver{rt: rt}, repository.Root)
	registered := registerFlowTestRepository(t, loader, repository.Root, managedPath)

	snapshot, err := loader.LoadRepository(context.Background(), registered.row.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Surfaces.Len() != 4 {
		t.Fatalf("surface count = %d, want every Git row exactly once", snapshot.Surfaces.Len())
	}
	byPath := map[string]flowtui.SurfaceRow{}
	seen := map[string]int{}
	for _, row := range snapshot.Surfaces.Values() {
		seen[row.RowKey]++
		byPath[row.Path] = row
	}
	for key, count := range seen {
		if count != 1 {
			t.Errorf("row %q appears %d times", key, count)
		}
	}
	mainCanonical, _ := pathx.Canonical(repository.Root)
	managedCanonical, _ := pathx.Canonical(managedPath)
	externalCanonical, _ := pathx.Canonical(externalPath)
	harnessCanonical, _ := pathx.Canonical(harnessPath)
	for path, kind := range map[string]flowtui.SurfaceKind{
		mainCanonical: flowtui.SurfaceCanonical, managedCanonical: flowtui.SurfaceManaged,
		externalCanonical: flowtui.SurfaceUnmanaged, harnessCanonical: flowtui.SurfaceHarness,
	} {
		if row, ok := byPath[path]; !ok || row.Kind != kind {
			t.Errorf("path %s row=%+v want kind=%s", path, row, kind)
		}
	}
	assertFlowActionIDs(t, byPath[managedCanonical], "park-warm", "park-cold", "park-cold-push", "resume", "complete-ff", "review-handoff", "verify-merged", "remote-fetch", "remote-review", "remote-both")
	assertFlowActionIDs(t, byPath[externalCanonical], "adopt", "remove-checkout", "remote-fetch", "remote-review", "remote-both")
	assertFlowActionIDs(t, byPath[harnessCanonical], "remote-fetch", "remote-review", "remote-both")

	saveFlowTask(t, app.Tasks, task.Task{
		ID: "managed-duplicate", Name: "duplicate", Repo: "repo", RepoPath: repository.Root,
		Branch: "feat/managed", Base: "main", WorktreePath: managedPath,
		Mode: task.ModeWorktree, State: task.Hot,
	})
	saveFlowTask(t, app.Tasks, task.Task{
		ID: "harness-claim", Name: "harness", Repo: "repo", RepoPath: repository.Root,
		Branch: "worktree-turn-1", Base: "main", WorktreePath: harnessPath,
		Mode: task.ModeWorktree, State: task.Hot,
	})
	snapshot, err = loader.LoadRepository(context.Background(), registered.row.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	byPath = map[string]flowtui.SurfaceRow{}
	for _, row := range snapshot.Surfaces.Values() {
		byPath[row.Path] = row
	}
	if byPath[managedCanonical].Kind != flowtui.SurfaceConflict || !containsFlowLine(byPath[managedCanonical].Conflicts.Values(), "multiple-task-claims") {
		t.Errorf("duplicate binding did not win as conflict: %+v", byPath[managedCanonical])
	}
	if byPath[harnessCanonical].Kind != flowtui.SurfaceConflict || !containsFlowLine(byPath[harnessCanonical].Conflicts.Values(), "harness-task-conflict") {
		t.Errorf("harness/task conflict precedence = %+v", byPath[harnessCanonical])
	}
	assertFlowActionIDs(t, byPath[managedCanonical], "remote-fetch", "remote-review", "remote-both")
	assertFlowActionIDs(t, byPath[harnessCanonical], "remote-fetch", "remote-review", "remote-both")
}

func TestFlowTaskDiagnosticsAndWorktreeFailuresAreNonAuthoritative(t *testing.T) {
	repository := gittest.New(t)
	externalPath := filepath.Join(t.TempDir(), "external")
	if err := gitx.AddWorktree(context.Background(), repository.Root, externalPath, "feat/external", "main"); err != nil {
		t.Fatal(err)
	}
	rt := &flowTestRuntime{name: "flow-test", available: true}
	app := newFlowTestApp(t, nil, rt)
	if err := os.MkdirAll(app.Tasks.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.Tasks.Dir, "broken.toml"), []byte("state = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := newFlowLoader(app, &flowStaticResolver{rt: rt}, repository.Root)
	registered := registerFlowTestRepository(t, loader, repository.Root, "")
	snapshot, err := loader.LoadRepository(context.Background(), registered.row.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Authoritative() || !strings.Contains(snapshot.Error, "task diagnostic") {
		t.Fatalf("task diagnostic was silently treated as authoritative: %+v", snapshot)
	}
	externalCanonical, _ := pathx.Canonical(externalPath)
	for _, row := range snapshot.Surfaces.Values() {
		if row.Path == externalCanonical {
			assertFlowActionIDs(t, row, "remote-fetch", "remote-review", "remote-both")
		}
	}

	brokenPath := filepath.Join(t.TempDir(), "missing-main")
	broken := registered
	broken.row.Path = brokenPath
	broken.repository.Path = brokenPath
	broken.repository.MainRoot = brokenPath
	loader.registerRepository(broken, true)
	snapshot, err = loader.LoadRepository(context.Background(), broken.row.RepoKey)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Authoritative() || !strings.Contains(snapshot.Error, "worktree inventory") {
		t.Fatalf("worktree failure became a silent empty snapshot: %+v", snapshot)
	}
}

func TestFlowRuntimeNoneFailureAndObservedEmptyRemainDistinct(t *testing.T) {
	repository := gittest.New(t)
	for _, test := range []struct {
		name          string
		runtime       *flowTestRuntime
		wantSnapshot  string
		wantEvidence  string
		dontEvidence  string
		authoritative bool
	}{
		{
			name: "none", runtime: &flowTestRuntime{name: "none", available: true},
			wantEvidence: "unobserved; state is not known closed", authoritative: true,
		},
		{
			name: "list error", runtime: &flowTestRuntime{name: "flow-test", available: true, listErr: errors.New("runtime list broke")},
			wantSnapshot: "runtime list broke", wantEvidence: "runtime observation failed", dontEvidence: "no covering sessions",
		},
		{
			name: "observed empty", runtime: &flowTestRuntime{name: "flow-test", available: true},
			wantEvidence: "observed; no covering sessions", authoritative: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newFlowTestApp(t, nil, test.runtime)
			resolver := &flowStaticResolver{rt: test.runtime}
			loader := newFlowLoader(app, resolver, repository.Root)
			registered := registerFlowTestRepository(t, loader, repository.Root, "")
			snapshot, err := loader.LoadRepository(context.Background(), registered.row.RepoKey)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantSnapshot != "" && !strings.Contains(snapshot.Error, test.wantSnapshot) {
				t.Errorf("snapshot error %q does not contain %q", snapshot.Error, test.wantSnapshot)
			}
			if snapshot.Authoritative() != test.authoritative {
				t.Errorf("authoritative=%v want %v error=%q", snapshot.Authoritative(), test.authoritative, snapshot.Error)
			}
			evidence := strings.Join(snapshot.Surfaces.Values()[0].Evidence.Values(), "\n")
			if !strings.Contains(evidence, test.wantEvidence) || (test.dontEvidence != "" && strings.Contains(evidence, test.dontEvidence)) {
				t.Errorf("runtime evidence:\n%s", evidence)
			}
			if test.runtime.lists() != 1 {
				t.Errorf("runtime List calls = %d, want one snapshot", test.runtime.lists())
			}
		})
	}
}

func TestFlowManagedActionMatrixAndOptionsNeverSetExpertOverrides(t *testing.T) {
	tests := []struct {
		mode  task.CheckoutMode
		state task.State
		ids   []string
	}{
		{task.ModeWorktree, task.Hot, []string{"park-warm", "park-cold", "park-cold-push", "complete-ff", "review-handoff", "verify-merged"}},
		{task.ModeWorktree, task.Warm, []string{"park-warm", "park-cold", "park-cold-push", "resume", "complete-ff", "review-handoff", "verify-merged"}},
		{task.ModeWorktree, task.Cold, []string{"resume"}},
		{task.ModeWorktree, task.Done, []string{"retire-keep-branch", "retire-delete-branch"}},
		{task.ModeBranch, task.Hot, []string{"park-warm", "park-cold", "park-cold-push", "complete-ff", "review-handoff", "verify-merged"}},
		{task.ModeBranch, task.Warm, []string{"park-warm", "park-cold", "park-cold-push", "resume", "complete-ff", "review-handoff", "verify-merged"}},
		{task.ModeBranch, task.Cold, []string{"resume"}},
		{task.ModeBranch, task.Done, []string{"retire-keep-branch", "retire-delete-branch"}},
		{task.ModeDirect, task.Hot, []string{"park-warm", "complete-direct"}},
		{task.ModeDirect, task.Warm, []string{"park-warm", "resume", "complete-direct"}},
		{task.ModeDirect, task.Cold, nil},
		{task.ModeDirect, task.Done, []string{"retire-keep-branch"}},
	}
	for _, test := range tests {
		t.Run(string(test.mode)+"/"+string(test.state), func(t *testing.T) {
			tracked := task.Task{Mode: test.mode, State: test.state, Branch: "feature", Base: "main"}
			if test.mode == task.ModeDirect {
				tracked.Branch, tracked.Base = "main", "main"
			}
			choices := managedFlowChoices(tracked)
			got := flowChoiceIDs(choices)
			if strings.Join(got, ",") != strings.Join(test.ids, ",") {
				t.Fatalf("action IDs = %v, want %v", got, test.ids)
			}
			for _, choice := range choices {
				assertSafeFlowOptions(t, choice.Options())
			}
		})
	}

	unmanaged := unmanagedFlowChoices("main")
	if got := flowChoiceIDs(unmanaged); strings.Join(got, ",") != "adopt,remove-checkout" {
		t.Fatalf("unmanaged choices = %v", got)
	}
	for _, choice := range append(unmanaged, remoteFlowChoices()...) {
		assertSafeFlowOptions(t, choice.Options())
	}
	remote := remoteFlowChoices()
	if got := flowChoiceIDs(remote); strings.Join(got, ",") != "remote-fetch,remote-review,remote-both" {
		t.Fatalf("remote variants = %v", got)
	}
}

func assertSafeFlowOptions(t *testing.T, options taskflow.ActionOptions) {
	t.Helper()
	switch value := options.(type) {
	case taskflow.ParkWarmOptions:
		if value.CommitWIP || value.Push || value.KeepSession || value.CloseUnknown || value.AssumeNoRuntime {
			t.Fatalf("unsafe ParkWarm options: %+v", value)
		}
	case taskflow.ParkColdOptions:
		if value.CommitWIP || value.CloseUnknown || value.AssumeNoRuntime {
			t.Fatalf("unsafe ParkCold options: %+v", value)
		}
	case taskflow.ResumeOptions:
		if !value.FetchRefs || value.TakeOwnership || value.NoProvision {
			t.Fatalf("unsafe Resume options: %+v", value)
		}
	case taskflow.CompleteDirectOptions:
		if value.Dirty != taskflow.DirtyFail || value.CommitMessage != "" || value.Push {
			t.Fatalf("unsafe CompleteDirect options: %+v", value)
		}
	case taskflow.CompleteFFOptions:
		if value.Dirty != taskflow.DirtyFail || value.CommitMessage != "" || value.PushBase {
			t.Fatalf("unsafe CompleteFF options: %+v", value)
		}
	case taskflow.ReviewHandoffOptions:
		if value.Dirty != taskflow.DirtyFail || value.CommitMessage != "" {
			t.Fatalf("unsafe ReviewHandoff options: %+v", value)
		}
	case taskflow.VerifyMergedOptions:
		if value.Dirty != taskflow.DirtyFail || value.CommitMessage != "" || value.PushBase {
			t.Fatalf("unsafe VerifyMerged options: %+v", value)
		}
	case taskflow.RetireOptions:
		if value.CloseUnknown || value.AssumeNoRuntime {
			t.Fatalf("unsafe Retire options: %+v", value)
		}
	case taskflow.AdoptOptions:
		if value.Mode != task.ModeWorktree || value.Base == "" || value.Owner != "" || value.State != "" {
			t.Fatalf("unsafe Adopt options: %+v", value)
		}
	case taskflow.RemoveCheckoutOptions:
		if value.DiscardDirty || value.CloseUnknown || value.AssumeNoRuntime {
			t.Fatalf("unsafe RemoveCheckout options: %+v", value)
		}
	case taskflow.RefreshRemoteOptions:
		if !value.FetchRefs && !value.QueryReview {
			t.Fatalf("empty remote options: %+v", value)
		}
	default:
		t.Fatalf("unexpected flow options %T", options)
	}
}

func TestFlowPlanApplyRequireExactRepositoryRowActionLocatorOptionsAndPlan(t *testing.T) {
	rt := &flowTestRuntime{name: "flow-test", available: true}
	app := newFlowTestApp(t, nil, rt)
	resolver := &flowStaticResolver{rt: rt}
	loader := newFlowLoader(app, resolver, t.TempDir())
	var serviceBuilds, applies int
	remote := taskflow.RemoteObservation{RemoteName: "origin", Head: "feat/exact", Base: "main"}
	loader.service = func(got runtime.Runtime) (*taskflow.Service, error) {
		serviceBuilds++
		if got != rt {
			t.Fatalf("service received runtime %T %v, want pinned runtime", got, got)
		}
		return taskflow.NewService(taskflow.Handlers{
			taskflow.RefreshRemote: {
				Plan: func(context.Context, taskflow.Request) (taskflow.PlanSpec, error) {
					return taskflow.PlanSpec{Summary: "exact remote plan"}, nil
				},
				Apply: func(context.Context, taskflow.Plan) (taskflow.Result, error) {
					applies++
					return taskflow.NewResult(taskflow.ResultSpec{Remote: &remote}), nil
				},
			},
		}), nil
	}
	locator := taskflow.Locator{
		RepoKey: "/repo/.git", RowKey: "/repo/wt", RowKind: "checkout",
		RepositoryID: "/repo/.git", GitCommonDir: "/repo/.git",
		RepoPath: "/repo", CheckoutPath: "/repo/wt", Branch: "feat/exact",
		Base: "main", HeadOID: strings.Repeat("a", 40), Remote: "origin",
	}
	options := taskflow.RefreshRemoteOptions{FetchRefs: true}
	request, err := taskflow.NewRequest(locator, options)
	if err != nil {
		t.Fatal(err)
	}
	target := flowTarget{repoKey: locator.RepoKey, rowKey: locator.RowKey, actionID: "remote-fetch", request: request}
	loader.targets[locator.RepoKey] = map[string]flowTarget{
		flowTargetKey(target.repoKey, target.rowKey, target.actionID): target,
	}
	plan, err := loader.Plan(context.Background(), target.repoKey, target.rowKey, target.actionID, locator, options)
	if err != nil {
		t.Fatal(err)
	}

	tamperedLocator := locator
	tamperedLocator.Branch = "feat/other"
	tamperedPlan := plan
	tamperedPlan.Summary = "changed"
	attempts := []struct {
		name                 string
		repoKey, row, action string
		locator              taskflow.Locator
		options              taskflow.ActionOptions
		plan                 taskflow.Plan
	}{
		{name: "repository", repoKey: "/other", row: target.rowKey, action: target.actionID, locator: locator, options: options, plan: plan},
		{name: "row", repoKey: target.repoKey, row: "/other", action: target.actionID, locator: locator, options: options, plan: plan},
		{name: "action ID", repoKey: target.repoKey, row: target.rowKey, action: "remote-both", locator: locator, options: options, plan: plan},
		{name: "locator", repoKey: target.repoKey, row: target.rowKey, action: target.actionID, locator: tamperedLocator, options: options, plan: plan},
		{name: "options", repoKey: target.repoKey, row: target.rowKey, action: target.actionID, locator: locator, options: taskflow.RefreshRemoteOptions{QueryReview: true}, plan: plan},
		{name: "plan", repoKey: target.repoKey, row: target.rowKey, action: target.actionID, locator: locator, options: options, plan: tamperedPlan},
	}
	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			if _, err := loader.Apply(context.Background(), attempt.repoKey, attempt.row, attempt.action,
				attempt.locator, attempt.options, attempt.plan, taskflow.Approve(attempt.plan.PlanID)); err == nil {
				t.Fatal("tampered Apply was accepted")
			}
		})
	}
	if applies != 0 || serviceBuilds != 1 {
		t.Fatalf("tampered calls reached service: applies=%d service builds=%d", applies, serviceBuilds)
	}

	result, err := loader.Apply(context.Background(), target.repoKey, target.rowKey, target.actionID,
		locator, options, plan, taskflow.Approve(plan.PlanID))
	if err != nil {
		t.Fatal(err)
	}
	if applies != 1 || serviceBuilds != 2 {
		t.Fatalf("exact Apply calls: applies=%d service builds=%d", applies, serviceBuilds)
	}
	if observed, ok := result.RemoteObservation(); !ok || observed.Head != remote.Head {
		t.Fatalf("result remote observation = %+v, %v", observed, ok)
	}
	if retained, ok := loader.remote[target.repoKey]; !ok || retained.Head != remote.Head {
		t.Fatalf("run-local remote observation not retained: %+v, %v", retained, ok)
	}
}

func TestFlowCommandRegistrationHelpAndNonTTYRefusal(t *testing.T) {
	app := newFlowTestApp(t, nil, runtime.None{})
	app.interactiveCheck = func() bool { return true }
	root := newRootCommand(app)
	command, _, err := root.Find([]string{"flow"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "flow" || command.Use != "flow [repo]" || !strings.Contains(command.Short, "Preview") {
		t.Fatalf("flow command = %q %q", command.Use, command.Short)
	}
	if !fullScreenInvocation(command, app) {
		t.Fatal("flow is not classified as a full-screen invocation")
	}

	var out, errOut bytes.Buffer
	nonTTY := newFlowTestApp(t, nil, runtime.None{})
	nonTTY.Out, nonTTY.Err = &out, &errOut
	nonTTY.interactiveCheck = func() bool { return false }
	deps := flowCommandDeps{
		getwd: func() (string, error) { t.Fatal("non-TTY flow resolved cwd"); return "", nil },
		discover: func(context.Context, string) (gitx.Repo, error) {
			t.Fatal("non-TTY flow ran Git")
			return gitx.Repo{}, nil
		},
		resolve: func(context.Context, []string, string) (repo.Repo, []repo.Repo, error) {
			t.Fatal("non-TTY flow resolved repositories")
			return repo.Repo{}, nil, nil
		},
		runProgram: func(tea.Model, ...tea.ProgramOption) (tea.Model, error) {
			t.Fatal("non-TTY flow entered Bubble Tea")
			return nil, nil
		},
	}
	err = newFlowCmdWithDeps(nonTTY, deps).RunE(command, nil)
	if err == nil || err.Error() != flowNonTTYMessage {
		t.Fatalf("non-TTY error = %v", err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(errOut.String(), "\x1b") || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("non-TTY flow emitted terminal bytes: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

type flowFinalModel struct {
	handoff taskflow.Handoff
	has     bool
}

func (m flowFinalModel) Init() tea.Cmd { return nil }
func (m flowFinalModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}
func (m flowFinalModel) View() string { return "" }
func (m flowFinalModel) Handoff() (taskflow.Handoff, bool) {
	return m.handoff, m.has
}

func TestFlowRunUsesAltScreenThenHonorsDirectoryAndRuntimeHandoffs(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		app := newFlowTestApp(t, nil, runtime.None{})
		var out bytes.Buffer
		app.Out = &out
		path := filepath.Join(t.TempDir(), "checkout")
		err := runFlow(app, flowLaunch{cwd: t.TempDir()}, func(_ tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
			if len(options) != 1 {
				t.Fatalf("program options = %d, want WithAltScreen", len(options))
			}
			return flowFinalModel{handoff: taskflow.Handoff{Kind: taskflow.HandoffDirectory, Path: path}, has: true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "cd ") || !strings.Contains(out.String(), path) {
			t.Fatalf("directory handoff output = %q", out.String())
		}
	})

	t.Run("backend-qualified runtime after trace", func(t *testing.T) {
		tracePath := filepath.Join(t.TempDir(), "trace.json")
		rt := &flowTestRuntime{name: "flow-test", available: true}
		rt.activateCheck = func() error {
			if _, err := os.Stat(tracePath); err != nil {
				return errors.New("activation ran before trace finalization")
			}
			return nil
		}
		app := newFlowTestApp(t, nil, rt)
		app.Out, app.Err = io.Discard, io.Discard
		app.trace = perftrace.New(32)
		app.tracePath = tracePath
		app.traceOnce = &sync.Once{}
		err := runFlow(app, flowLaunch{cwd: t.TempDir()}, func(_ tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
			if len(options) != 1 {
				t.Fatalf("program options = %d, want WithAltScreen", len(options))
			}
			return flowFinalModel{handoff: taskflow.Handoff{
				Kind: taskflow.HandoffRuntime, Runtime: "flow-test", RuntimeHandle: "workspace-1",
			}, has: true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rt.activated) != 1 || rt.activated[0] != "workspace-1" {
			t.Fatalf("runtime activations = %v", rt.activated)
		}
	})
}

func TestFlowRunCancelsRepositoryReadAfterProgramReturns(t *testing.T) {
	repository := gittest.New(t)
	started, canceled := make(chan struct{}), make(chan struct{})
	rt := &flowTestRuntime{
		name: "flow-test", available: true, blockList: true,
		listStarted: started, listCanceled: canceled,
	}
	app := newFlowTestApp(t, nil, rt)
	discovered, err := gitx.Discover(context.Background(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := availableFlowRepository(discovered, nil, repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	row := selected.row
	launch := flowLaunch{cwd: repository.Root, repository: &selected, preselected: &row}
	err = runFlow(app, launch, func(model tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
		command := model.Init()
		if command == nil {
			t.Fatal("preselected flow did not schedule a repository load")
		}
		go command()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("repository load did not reach runtime List")
		}
		return flowFinalModel{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("flow read context was not canceled after Program.Run")
	}
}

func assertFlowActionIDs(t *testing.T, row flowtui.SurfaceRow, want ...string) {
	t.Helper()
	got := flowChoiceIDs(row.Actions.Values())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("row %s actions = %v, want %v", row.RowKey, got, want)
	}
}

func flowChoiceIDs(choices []flowtui.ActionChoice) []string {
	ids := make([]string, 0, len(choices))
	for _, choice := range choices {
		ids = append(ids, choice.ID)
	}
	return ids
}

func containsFlowLine(lines []string, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
