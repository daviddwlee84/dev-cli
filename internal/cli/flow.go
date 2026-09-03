package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/flowtui"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/spf13/cobra"
)

const flowNonTTYMessage = "`dev flow` requires an interactive terminal; use `dev repo context [repo]` or `dev ls --all --json`"

type flowProgramRunner func(tea.Model, ...tea.ProgramOption) (tea.Model, error)

type flowCommandDeps struct {
	getwd      func() (string, error)
	discover   func(context.Context, string) (gitx.Repo, error)
	resolve    func(context.Context, []string, string) (repo.Repo, []repo.Repo, error)
	runProgram flowProgramRunner
}

func defaultFlowCommandDeps() flowCommandDeps {
	return flowCommandDeps{
		getwd:    os.Getwd,
		discover: gitx.Discover,
		resolve:  repo.Resolve,
		runProgram: func(model tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
			return tea.NewProgram(model, options...).Run()
		},
	}
}

type flowLaunch struct {
	cwd         string
	repository  *flowRepository
	preselected *flowtui.RepositoryRow
}

func newFlowCmd(app *App) *cobra.Command {
	return newFlowCmdWithDeps(app, defaultFlowCommandDeps())
}

func newFlowCmdWithDeps(app *App, deps flowCommandDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "flow [repo]",
		Short: "Preview: inspect and run guarded repository lifecycle actions",
		Long: `Preview the repository lifecycle state machine in a full-screen interface.

Without an argument, a checkout opens its canonical repository and focuses the
exact current worktree. Outside Git, dev opens an asynchronous repository
picker. An explicit repository reference always overrides the current directory.

Local refreshes do not contact remotes. Enter first builds an exact guarded plan;
only the plan's second confirmation can apply it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.interactive() {
				return errors.New(flowNonTTYMessage)
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			launch, err := resolveFlowLaunch(ctxOf(), app, ref, deps)
			if err != nil {
				return err
			}
			return runFlow(app, launch, deps.runProgram)
		},
	}
}

func resolveFlowLaunch(ctx context.Context, app *App, ref string, deps flowCommandDeps) (flowLaunch, error) {
	var launch flowLaunch
	if deps.getwd == nil || deps.discover == nil || deps.resolve == nil {
		return launch, errors.New("flow command dependencies are incomplete")
	}
	cwd, err := deps.getwd()
	if err != nil {
		return launch, fmt.Errorf("resolve flow caller directory: %w", err)
	}
	launch.cwd = cwd

	if ref != "" {
		resolved, _, err := deps.resolve(ctx, app.Cfg.DiscoveryRoots(), ref)
		if err != nil {
			return launch, err
		}
		queryPath := resolved.Path
		if queryPath == "" {
			queryPath = resolved.RealPath
		}
		discovered, err := deps.discover(ctx, queryPath)
		if err != nil {
			return launch, fmt.Errorf("discover explicit repository %s: %w", config.Contract(queryPath), err)
		}
		repository, err := availableFlowRepository(discovered, &resolved, discovered.Root)
		if err != nil {
			return launch, err
		}
		launch.repository = &repository
		row := repository.row
		launch.preselected = &row
		return launch, nil
	}

	discovered, err := deps.discover(ctx, cwd)
	if errors.Is(err, gitx.ErrNotARepo) {
		return launch, nil
	}
	if err != nil {
		return launch, fmt.Errorf("discover current repository from %s: %w", config.Contract(cwd), err)
	}
	repository, err := availableFlowRepository(discovered, nil, discovered.Root)
	if err != nil {
		return launch, err
	}
	launch.repository = &repository
	row := repository.row
	launch.preselected = &row
	return launch, nil
}

func runFlow(app *App, launch flowLaunch, runProgram flowProgramRunner) error {
	if runProgram == nil {
		return errors.New("flow program runner is unavailable")
	}
	flowtui.SetColorEnabled(app.outStyle().enabled)
	app.traceTUI = true
	runCtx, cancelRun := context.WithCancel(context.Background())
	finishSetup := app.trace.Start(perftrace.TUISetup, perftrace.Fields{})
	runtimeResolver := newTUIRuntimeResolver(app)
	loader := newFlowLoader(app, runtimeResolver, launch.cwd)
	if launch.repository != nil {
		loader.registerRepository(*launch.repository, true)
	}
	actions := loader.actions()
	if app.deferredReleaseRefresh {
		actions.AfterFirstView = func(context.Context) { app.refreshReleaseDetached() }
	}
	model := flowtui.New(actions, launch.preselected).WithContext(runCtx)
	finishSetup(perftrace.OutcomeSuccess)
	app.trace.Mark(perftrace.TUIProgramRunBegin, perftrace.Fields{})
	final, err := runProgram(model, tea.WithAltScreen())
	cancelRun()
	app.finishTrace()
	if err != nil {
		return err
	}
	withHandoff, ok := final.(interface {
		Handoff() (taskflow.Handoff, bool)
	})
	if !ok {
		return nil
	}
	handoff, ok := withHandoff.Handoff()
	if !ok {
		return nil
	}
	return honorFlowHandoff(app, runtimeResolver, handoff)
}

func honorFlowHandoff(app *App, resolver flowRuntimeResolver, handoff taskflow.Handoff) error {
	switch handoff.Kind {
	case taskflow.HandoffDirectory:
		if handoff.Path == "" {
			return errors.New("flow returned an empty directory handoff")
		}
		return app.cdDirective(handoff.Path)
	case taskflow.HandoffRuntime:
		if handoff.Runtime == "" || handoff.RuntimeHandle == "" {
			return errors.New("flow returned an incomplete runtime handoff")
		}
		var rt runtime.Runtime
		if resolver != nil {
			resolved, err := resolver.Resolve(ctxOf())
			if err != nil {
				return err
			}
			if resolved != nil && resolved.Name() == handoff.Runtime {
				rt = resolved
			}
		}
		if rt == nil {
			rt = app.runtimeNamed(handoff.Runtime)
		}
		if rt == nil || rt.Name() != handoff.Runtime {
			return fmt.Errorf("flow handoff backend %q is unavailable", handoff.Runtime)
		}
		return activateRuntime(ctxOf(), rt, handoff.RuntimeHandle)
	case taskflow.HandoffURL:
		if handoff.URL == "" {
			return errors.New("flow returned an empty URL handoff")
		}
		_, err := fmt.Fprintln(app.Out, handoff.URL)
		return err
	default:
		return fmt.Errorf("flow returned unsupported handoff kind %q", handoff.Kind)
	}
}

type flowRuntimeResolver interface {
	Resolve(context.Context) (runtime.Runtime, error)
}

type flowRepository struct {
	row              flowtui.RepositoryRow
	repository       repo.Repo
	available        bool
	unavailableTasks []task.Record
}

func availableFlowRepository(discovered gitx.Repo, preferred *repo.Repo, focus string) (flowRepository, error) {
	commonDir, err := pathx.Canonical(discovered.GitCommonDir)
	if err != nil {
		return flowRepository{}, fmt.Errorf("canonicalize flow repository identity: %w", err)
	}
	mainRoot := discovered.MainRoot
	if mainRoot == "" && discovered.Bare {
		mainRoot = discovered.GitCommonDir
	}
	mainRoot, err = pathx.Canonical(mainRoot)
	if err != nil {
		return flowRepository{}, fmt.Errorf("canonicalize flow repository root: %w", err)
	}
	focusTarget := ""
	if focus != "" {
		focusTarget, err = pathx.Canonical(focus)
		if err != nil {
			return flowRepository{}, fmt.Errorf("canonicalize flow focus: %w", err)
		}
	}

	r := repo.Repo{
		Name: discovered.Name, Path: mainRoot, RealPath: mainRoot,
		CommonDir: commonDir, MainRoot: mainRoot, Bare: discovered.Bare, HasGit: true,
	}
	name := discovered.Name
	if preferred != nil {
		if preferred.Name != "" {
			r.Name = preferred.Name
		}
		r.Root = preferred.Root
		r.Category = preferred.Category
		name = preferred.Display()
		if name == "" {
			name = r.Name
		}
	}
	return flowRepository{
		row: flowtui.RepositoryRow{
			RepoKey: commonDir, Name: name, Path: mainRoot, Available: true,
			FocusTarget: focusTarget,
		},
		repository: r,
		available:  true,
	}, nil
}

func unavailableFlowRepository(record task.Record, cause error) flowRepository {
	path := record.Task.RepoPath
	if canonical, err := pathx.Canonical(path); err == nil {
		path = canonical
	} else if absolute, absErr := filepath.Abs(path); absErr == nil {
		path = filepath.Clean(absolute)
	}
	name := record.Task.Repo
	if name == "" {
		name = filepath.Base(path)
	}
	message := "repository is unavailable"
	if cause != nil {
		message += ": " + cause.Error()
	}
	return flowRepository{
		row: flowtui.RepositoryRow{
			RepoKey: "unavailable:" + path, Name: name, Path: path,
			Available: false, Error: message,
		},
		available:        false,
		unavailableTasks: []task.Record{record},
	}
}

type flowTarget struct {
	repoKey  string
	rowKey   string
	actionID string
	request  taskflow.Request
}

type flowApprovedPlan struct {
	target flowTarget
	plan   taskflow.Plan
}

type flowLoader struct {
	app      *App
	runtime  flowRuntimeResolver
	limiter  *inventory.Limiter
	cwd      string
	now      func() time.Time
	discover func(context.Context, string) (gitx.Repo, error)
	gitRun   func(context.Context, string, ...string) (string, error)
	listRepo func(context.Context, []string, repo.Options) ([]repo.Repo, error)
	inspect  func(context.Context, *artifact.Store, string) (artifact.ReadinessInspection, error)
	base     func(context.Context, string) string
	service  func(runtime.Runtime) (*taskflow.Service, error)

	mu           sync.Mutex
	repositories map[string]flowRepository
	repoPathKeys map[string]string
	pinned       map[string]bool
	targets      map[string]map[string]flowTarget
	approved     map[string]flowApprovedPlan
	remote       map[string]taskflow.RemoteObservation
}

func newFlowLoader(app *App, resolver flowRuntimeResolver, cwd string) *flowLoader {
	loader := &flowLoader{
		app:          app,
		runtime:      resolver,
		limiter:      inventory.NewLimiter(8),
		cwd:          cwd,
		now:          time.Now,
		discover:     gitx.Discover,
		gitRun:       gitx.Run,
		listRepo:     repo.Discover,
		inspect:      artifact.InspectReadiness,
		base:         gitx.DefaultBranch,
		repositories: make(map[string]flowRepository),
		repoPathKeys: make(map[string]string),
		pinned:       make(map[string]bool),
		targets:      make(map[string]map[string]flowTarget),
		approved:     make(map[string]flowApprovedPlan),
		remote:       make(map[string]taskflow.RemoteObservation),
	}
	loader.service = func(rt runtime.Runtime) (*taskflow.Service, error) {
		return newFlowLifecycleService(app, rt, cwd)
	}
	return loader
}

func newFlowLifecycleService(app *App, resolved runtime.Runtime, cwd string) (*taskflow.Service, error) {
	if resolved == nil {
		return nil, errors.New("flow runtime resolver returned no backend")
	}
	return taskflow.NewLifecycleService(taskflow.LifecycleConfig{
		Config: app.Cfg, Tasks: app.Tasks, Artifacts: artifactStore(app),
		DefaultRuntime: func() runtime.Runtime { return resolved },
		NamedRuntime: func(name string) runtime.Runtime {
			if name == "" || name == resolved.Name() {
				return resolved
			}
			return app.runtimeNamed(name)
		},
		Host: config.Hostname(), CWD: cwd,
		// The flow preview deliberately never inherits the expert shared-writer
		// override. Its normal choices must remain fail-closed.
		AllowSharedCheckout: false,
		Logf: func(format string, args ...any) {
			if app.Err != nil {
				fmt.Fprintf(app.Err, format+"\n", args...)
			}
		},
	})
}

func (l *flowLoader) registerRepository(repository flowRepository, pinned bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.repositories[repository.row.RepoKey] = repository
	if repository.available && repository.repository.MainRoot != "" {
		l.repoPathKeys[repository.repository.MainRoot] = repository.row.RepoKey
	}
	if pinned {
		l.pinned[repository.row.RepoKey] = true
	}
}

func (l *flowLoader) actions() flowtui.Actions {
	return flowtui.Actions{
		ListRepositories: l.ListRepositories,
		LoadRepository:   l.LoadRepository,
		Plan:             l.Plan,
		Apply:            l.Apply,
	}
}

func (l *flowLoader) ListRepositories(ctx context.Context) ([]flowtui.RepositoryRow, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	records, _, taskErr := l.app.Tasks.ListRecords()
	if taskErr != nil {
		return nil, fmt.Errorf("list task-referenced repositories: %w", taskErr)
	}
	discovered, err := l.listRepo(ctx, l.app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
	if err != nil {
		return nil, err
	}

	fresh := make(map[string]flowRepository)
	pathKeys := make(map[string]string)
	l.mu.Lock()
	for key := range l.pinned {
		if existing, ok := l.repositories[key]; ok {
			fresh[key] = existing
			if existing.available && existing.repository.MainRoot != "" {
				pathKeys[existing.repository.MainRoot] = key
			}
		}
	}
	l.mu.Unlock()

	for index := range discovered {
		candidate := discovered[index]
		gitRepo, discoverErr := l.discover(ctx, candidate.Path)
		if discoverErr != nil {
			continue
		}
		entry, convertErr := availableFlowRepository(gitRepo, &candidate, "")
		if convertErr != nil {
			continue
		}
		if _, exists := fresh[entry.row.RepoKey]; !exists {
			fresh[entry.row.RepoKey] = entry
		}
		pathKeys[candidate.Path] = entry.row.RepoKey
	}

	pathResults := make(map[string]struct {
		repository flowRepository
		err        error
	})
	for _, record := range records {
		path := record.Task.RepoPath
		resolved, seen := pathResults[path]
		if !seen {
			gitRepo, discoverErr := l.discover(ctx, path)
			if discoverErr == nil {
				resolved.repository, discoverErr = availableFlowRepository(gitRepo, nil, "")
			}
			resolved.err = discoverErr
			pathResults[path] = resolved
		}
		if resolved.err == nil {
			if _, exists := fresh[resolved.repository.row.RepoKey]; !exists {
				fresh[resolved.repository.row.RepoKey] = resolved.repository
			}
			pathKeys[path] = resolved.repository.row.RepoKey
			continue
		}
		entry := unavailableFlowRepository(record, resolved.err)
		if existing, exists := fresh[entry.row.RepoKey]; exists {
			existing.unavailableTasks = appendUniqueFlowTaskRecord(existing.unavailableTasks, record)
			fresh[entry.row.RepoKey] = existing
		} else {
			fresh[entry.row.RepoKey] = entry
		}
	}

	rows := make([]flowtui.RepositoryRow, 0, len(fresh))
	for _, entry := range fresh {
		rows = append(rows, entry.row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := strings.ToLower(rows[i].Name), strings.ToLower(rows[j].Name)
		if left != right {
			return left < right
		}
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].RepoKey < rows[j].RepoKey
	})
	l.mu.Lock()
	l.repositories = fresh
	l.repoPathKeys = pathKeys
	l.mu.Unlock()
	return rows, nil
}

func appendUniqueFlowTaskRecord(records []task.Record, candidate task.Record) []task.Record {
	for _, record := range records {
		if record.Task.ID == candidate.Task.ID {
			return records
		}
	}
	return append(records, candidate)
}

func (l *flowLoader) LoadRepository(ctx context.Context, repoKey string) (flowtui.Snapshot, error) {
	if err := contextError(ctx); err != nil {
		return flowtui.Snapshot{}, err
	}
	l.mu.Lock()
	selected, ok := l.repositories[repoKey]
	pathKeys := make(map[string]string, len(l.repoPathKeys))
	for path, key := range l.repoPathKeys {
		pathKeys[path] = key
	}
	l.mu.Unlock()
	if !ok {
		return flowtui.Snapshot{}, fmt.Errorf("repository %q is not in the current flow inventory", repoKey)
	}
	if !selected.available {
		return l.unavailableSnapshot(selected), nil
	}

	records, diagnostics, taskErr := l.app.Tasks.ListRecords()
	selectedTasks := make([]*task.Task, 0, len(records))
	selectedRecords := make(map[string]task.Record)
	type pathResult struct {
		repoKey string
		err     error
	}
	resolvedPaths := make(map[string]pathResult)
	for index := range records {
		record := records[index]
		path := record.Task.RepoPath
		key, cached := pathKeys[path]
		if !cached {
			resolved, seen := resolvedPaths[path]
			if !seen {
				repository, discoverErr := l.discover(ctx, path)
				if discoverErr == nil {
					resolved.repoKey, discoverErr = pathx.Canonical(repository.GitCommonDir)
				}
				resolved.err = discoverErr
				resolvedPaths[path] = resolved
			}
			if resolved.err != nil {
				continue
			}
			key = resolved.repoKey
		}
		if key != repoKey {
			continue
		}
		copy := record.Task
		copy.Tags = append([]string(nil), record.Task.Tags...)
		selectedTasks = append(selectedTasks, &copy)
		record.Task = copy
		selectedRecords[record.Task.ID] = record
	}

	rt, err := l.runtime.Resolve(ctx)
	if err != nil {
		return flowtui.Snapshot{}, err
	}
	if rt == nil {
		return flowtui.Snapshot{}, errors.New("flow runtime resolver returned no backend")
	}
	sessions, runtimeErr := rt.List(ctx)
	runtimeObserved := runtimeErr == nil && rt.Name() != "none"
	contextSnapshot := inventory.CollectRepoContextWithOptions(ctx, selected.repository, selectedTasks, inventory.RepoContextOptions{
		Runtime: rt.Name(), Sessions: sessions, RuntimeObserved: runtimeObserved,
		RuntimeErr: runtimeErr, Limiter: l.limiter,
	})
	if err := contextError(ctx); err != nil {
		return flowtui.Snapshot{}, err
	}

	base := ""
	if queryPath := flowRepositoryQueryPath(contextSnapshot); queryPath != "" {
		base = l.base(ctx, queryPath)
	}
	taskComplete := taskErr == nil && len(diagnostics) == 0
	locatorBuilder := newFlowManagedLocatorBuilder(l.gitRun, contextSnapshot, selectedRecords)
	rows, targets, projectionIssues := l.projectRows(ctx, contextSnapshot, locatorBuilder, taskComplete, base)

	issues := make([]string, 0, 8)
	if taskErr != nil {
		issues = append(issues, "task inventory: "+taskErr.Error())
	}
	for _, diagnostic := range diagnostics {
		issues = append(issues, "task diagnostic: "+diagnostic.Error())
	}
	if contextSnapshot.IdentityErr != nil {
		issues = append(issues, "repository identity: "+contextSnapshot.IdentityErr.Error())
	}
	if contextSnapshot.WorktreeErr != nil {
		issues = append(issues, "worktree inventory: "+contextSnapshot.WorktreeErr.Error())
	}
	if rt.Name() != "none" && runtimeErr != nil {
		issues = append(issues, "runtime observation: "+runtimeErr.Error())
	}
	for _, checkout := range contextSnapshot.Checkouts {
		if checkout.StatusErr != nil {
			issues = append(issues, "Git status "+checkout.Worktree.Path+": "+checkout.StatusErr.Error())
		}
	}
	issues = append(issues, projectionIssues...)
	issues = uniqueStrings(issues)

	freshness := flowtui.FreshnessFresh
	if len(issues) > 0 {
		freshness = flowtui.FreshnessStale
	}
	repositoryRow := selected.row
	repositoryRow.Available = contextSnapshot.RepositoryID == repoKey && contextSnapshot.IdentityErr == nil
	if contextSnapshot.RepositoryID != "" && contextSnapshot.RepositoryID != repoKey {
		issues = append(issues, fmt.Sprintf("repository identity changed from %s to %s", repoKey, contextSnapshot.RepositoryID))
		repositoryRow.Available = false
		freshness = flowtui.FreshnessStale
	}
	snapshot := flowtui.Snapshot{
		Repository: repositoryRow,
		Surfaces:   flowtui.NewSurfaceList(rows...),
		ObservedAt: l.now().UTC(),
		Freshness:  freshness,
		Error:      strings.Join(uniqueStrings(issues), "; "),
	}
	l.mu.Lock()
	l.targets[repoKey] = targets
	if remote, exists := l.remote[repoKey]; exists {
		snapshot.Remote = flowtui.SomeRemoteObservation(remote)
	}
	l.mu.Unlock()
	return snapshot, nil
}

func (l *flowLoader) unavailableSnapshot(selected flowRepository) flowtui.Snapshot {
	rows := make([]flowtui.SurfaceRow, 0, len(selected.unavailableTasks))
	for _, record := range selected.unavailableTasks {
		tracked := record.Task
		rows = append(rows, flowtui.SurfaceRow{
			RowKey: tracked.ID, Kind: flowtui.SurfaceTaskOnly,
			Label: tracked.Title(), Branch: tracked.Branch, Base: tracked.Base,
			Mode: tracked.EffectiveMode(), State: tracked.State,
			Drift: flowtui.NewLines("repository-unavailable: " + selected.row.Error),
			Evidence: flowtui.NewLines(
				"recorded repository path: "+tracked.RepoPath,
				fmt.Sprintf("task: %s mode=%s state=%s", tracked.ID, tracked.EffectiveMode(), tracked.State),
			),
		})
	}
	l.mu.Lock()
	l.targets[selected.row.RepoKey] = map[string]flowTarget{}
	l.mu.Unlock()
	return flowtui.Snapshot{
		Repository: selected.row, Surfaces: flowtui.NewSurfaceList(rows...),
		ObservedAt: l.now().UTC(), Freshness: flowtui.FreshnessStale,
		Error: selected.row.Error,
	}
}

func flowRepositoryQueryPath(contextSnapshot inventory.RepoContext) string {
	if main, ok := contextSnapshot.Main(); ok && main.Worktree.Path != "" {
		return main.Worktree.Path
	}
	for _, candidate := range []string{
		contextSnapshot.Repo.MainRoot,
		contextSnapshot.Repo.Path,
		contextSnapshot.Repo.RealPath,
		contextSnapshot.Repo.CommonDir,
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

type flowLocatorValue struct {
	value string
	err   error
}

type flowManagedLocatorBuilder struct {
	run       func(context.Context, string, ...string) (string, error)
	repoPath  string
	repoKey   string
	records   map[string]task.Record
	refs      map[string]flowLocatorValue
	upstreams map[string]flowLocatorValue
}

func newFlowManagedLocatorBuilder(
	run func(context.Context, string, ...string) (string, error),
	contextSnapshot inventory.RepoContext,
	records map[string]task.Record,
) *flowManagedLocatorBuilder {
	return &flowManagedLocatorBuilder{
		run: run, repoPath: flowRepositoryQueryPath(contextSnapshot), repoKey: contextSnapshot.RepositoryID,
		records: records, refs: make(map[string]flowLocatorValue), upstreams: make(map[string]flowLocatorValue),
	}
}

func (b *flowManagedLocatorBuilder) locate(ctx context.Context, row inventory.RepoContextRow, taskID string) (taskflow.Locator, error) {
	record, ok := b.records[taskID]
	if !ok {
		return taskflow.Locator{}, fmt.Errorf("task %s is absent from the loaded revision snapshot", taskID)
	}
	candidate := record.Task
	mode := candidate.EffectiveMode()
	locator := taskflow.Locator{
		RepoKey: b.repoKey, RowKey: taskID, RowKind: "task",
		RepositoryID: b.repoKey, GitCommonDir: b.repoKey,
		TaskID: taskID, TaskRevision: record.Revision,
		RepoPath: b.repoPath, Branch: candidate.Branch, Base: candidate.Base, Remote: "origin",
		Mode: mode, State: candidate.State,
	}
	if mode == task.ModeWorktree {
		if candidate.WorktreePath != "" {
			expected, err := pathx.Canonical(candidate.WorktreePath)
			if err != nil {
				return taskflow.Locator{}, fmt.Errorf("canonicalize task %s checkout: %w", taskID, err)
			}
			locator.CheckoutPath = expected
		}
	} else {
		locator.CheckoutPath = b.repoPath
	}
	if row.Checkout != nil {
		locator.RowKey, locator.RowKind = row.Checkout.ID, "checkout"
		locator.CheckoutPath = row.Checkout.ID
		locator.HeadOID = strings.TrimSpace(row.Checkout.Worktree.Head)
		if row.Checkout.StatusErr == nil {
			locator.Upstream = row.Checkout.Status.Upstream
		}
	}
	if locator.HeadOID == "" {
		locator.HeadOID, _ = b.refOID(ctx, "refs/heads/"+candidate.Branch)
	}
	if locator.Upstream == "" && (row.Checkout == nil || mode == task.ModeWorktree) {
		locator.Upstream, _ = b.branchUpstream(ctx, candidate.Branch)
	}
	locator.BaseOID, _ = b.refOID(ctx, candidate.Base)
	locator.UpstreamOID, _ = b.refOID(ctx, locator.Upstream)
	return locator, nil
}

func (b *flowManagedLocatorBuilder) refOID(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if cached, ok := b.refs[ref]; ok {
		return cached.value, cached.err
	}
	value, err := b.run(ctx, b.repoPath, "rev-parse", "--verify", ref+"^{commit}")
	value = strings.TrimSpace(value)
	b.refs[ref] = flowLocatorValue{value: value, err: err}
	return value, err
}

func (b *flowManagedLocatorBuilder) branchUpstream(ctx context.Context, branch string) (string, error) {
	if branch == "" {
		return "", nil
	}
	if cached, ok := b.upstreams[branch]; ok {
		return cached.value, cached.err
	}
	value, err := b.run(ctx, b.repoPath, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	value = strings.TrimSpace(value)
	b.upstreams[branch] = flowLocatorValue{value: value, err: err}
	return value, err
}

func (l *flowLoader) projectRows(
	ctx context.Context,
	contextSnapshot inventory.RepoContext,
	locatorBuilder *flowManagedLocatorBuilder,
	taskInventoryComplete bool,
	defaultBase string,
) ([]flowtui.SurfaceRow, map[string]flowTarget, []string) {
	rows := make([]flowtui.SurfaceRow, 0, len(contextSnapshot.Rows))
	targets := make(map[string]flowTarget)
	var issues []string
	for _, contextRow := range contextSnapshot.Rows {
		projected, rowIssues := l.projectRow(ctx, contextSnapshot, contextRow, locatorBuilder, taskInventoryComplete, defaultBase)
		issues = append(issues, rowIssues...)

		validChoices := make([]flowtui.ActionChoice, 0, projected.Actions.Len())
		for _, choice := range projected.Actions.Values() {
			request, err := taskflow.NewRequest(projected.Locator, choice.Options())
			if err != nil {
				issues = append(issues, fmt.Sprintf("row %s action %s: %v", projected.RowKey, choice.ID, err))
				continue
			}
			target := flowTarget{
				repoKey: contextSnapshot.RepositoryID, rowKey: projected.RowKey,
				actionID: choice.ID, request: request,
			}
			targets[flowTargetKey(target.repoKey, target.rowKey, target.actionID)] = target
			validChoices = append(validChoices, choice)
		}
		projected.Actions = flowtui.NewActionList(validChoices...)
		rows = append(rows, projected)
	}
	return rows, targets, issues
}

func (l *flowLoader) projectRow(
	ctx context.Context,
	contextSnapshot inventory.RepoContext,
	contextRow inventory.RepoContextRow,
	locatorBuilder *flowManagedLocatorBuilder,
	taskInventoryComplete bool,
	defaultBase string,
) (flowtui.SurfaceRow, []string) {
	drift := topologyReasonLines(contextRow.DriftReasons)
	conflicts := topologyReasonLines(contextRow.ConflictReasons)
	var issues []string
	var tracked *task.Task
	if contextRow.TaskOnly() {
		tracked = contextRow.Task
	} else if contextRow.Checkout != nil && len(contextRow.Checkout.TaskBindings) == 1 {
		tracked = contextRow.Checkout.TaskBindings[0].Task
	}

	var managedLocator taskflow.Locator
	managedExact := false
	if tracked != nil {
		if locatorBuilder == nil {
			err := errors.New("managed locator builder is unavailable")
			conflicts = append(conflicts, "locator-error: "+err.Error())
			issues = append(issues, "managed locator "+tracked.ID+": "+err.Error())
		} else {
			locator, err := locatorBuilder.locate(ctx, contextRow, tracked.ID)
			if err != nil {
				conflicts = append(conflicts, "locator-error: "+err.Error())
				issues = append(issues, "managed locator "+tracked.ID+": "+err.Error())
			} else if err := validateProjectedManagedLocator(contextSnapshot, contextRow, tracked, locator); err != nil {
				conflicts = append(conflicts, "locator-mismatch: "+err.Error())
				issues = append(issues, "managed locator "+tracked.ID+": "+err.Error())
			} else {
				managedLocator, managedExact = locator, true
			}
		}
	}

	row := flowtui.SurfaceRow{
		RowKey: contextRow.ID,
		Kind:   flowtui.SurfaceTaskOnly,
		Drift:  flowtui.NewLines(uniqueStrings(drift)...),
		Base:   defaultBase,
	}
	if tracked != nil {
		row.Label = tracked.Title()
		row.Branch = tracked.Branch
		row.Base = tracked.Base
		row.Mode = tracked.EffectiveMode()
		row.State = tracked.State
		row.Locator = managedLocator
	}

	if contextRow.Checkout != nil {
		checkout := contextRow.Checkout
		row.Path = checkout.ID
		row.Branch = checkout.Branch()
		row.Kind = classifyFlowCheckout(*checkout, tracked, managedExact, conflicts)
		row.Label = flowCheckoutLabel(*checkout, tracked, row.Kind)
		exactGitLocator := flowGitLocator(contextSnapshot, *checkout, defaultBase)
		if !managedExact {
			row.Locator = exactGitLocator
		}
		row.Evidence = flowtui.NewLines(flowCheckoutEvidence(contextSnapshot, *checkout)...)
		if checkout.Exists && !checkout.Worktree.Bare && !checkout.Worktree.Prunable {
			inspection, err := l.inspect(ctx, artifactStore(l.app), checkout.Worktree.Path)
			row.Evidence = flowtui.NewLines(append(row.Evidence.Values(), artifactSummary(inspection, err))...)
			if err != nil {
				issues = append(issues, "artifact observation "+checkout.Worktree.Path+": "+err.Error())
			}
		}
	} else if tracked != nil {
		evidence := append([]string{
			"repository path: " + tracked.RepoPath,
			"checkout: no registered checkout is bound to this task",
		}, flowTaskEvidence(tracked)...)
		evidence = append(evidence, flowTaskOnlyRuntimeEvidence(contextSnapshot)...)
		if tracked.WorktreePath != "" {
			evidence = append(evidence, "recorded checkout: "+tracked.WorktreePath)
		}
		row.Evidence = flowtui.NewLines(evidence...)
	}
	if tracked != nil && contextRow.Checkout != nil {
		row.Evidence = flowtui.NewLines(append(row.Evidence.Values(), flowTaskEvidence(tracked)...)...)
	}

	conflicts = uniqueStrings(conflicts)
	row.Conflicts = flowtui.NewLines(conflicts...)
	if managedExact && len(conflicts) == 0 {
		row.Actions = flowtui.NewActionList(managedFlowChoices(*tracked)...)
	} else if row.Kind == flowtui.SurfaceUnmanaged && taskInventoryComplete && exactFlowLocator(row.Locator) {
		row.Actions = flowtui.NewActionList(unmanagedFlowChoices(defaultBase)...)
	}
	if exactFlowRemoteLocator(row.Locator) {
		choices := append(row.Actions.Values(), remoteFlowChoices()...)
		row.Actions = flowtui.NewActionList(choices...)
	}
	return row, issues
}

func validateProjectedManagedLocator(contextSnapshot inventory.RepoContext, row inventory.RepoContextRow, tracked *task.Task, locator taskflow.Locator) error {
	if locator.RepoKey != contextSnapshot.RepositoryID || locator.RepositoryID != contextSnapshot.RepositoryID ||
		locator.GitCommonDir != contextSnapshot.RepositoryID {
		return errors.New("locator repository identity differs from inventory")
	}
	if locator.TaskID != tracked.ID || locator.Mode != tracked.EffectiveMode() || locator.State != tracked.State {
		return errors.New("locator task identity, mode, or state differs from inventory")
	}
	if row.Checkout != nil && locator.CheckoutPath != row.Checkout.ID {
		return fmt.Errorf("locator checkout %q differs from row %q", locator.CheckoutPath, row.Checkout.ID)
	}
	return nil
}

func classifyFlowCheckout(checkout inventory.RepoCheckout, tracked *task.Task, managedExact bool, conflicts []string) flowtui.SurfaceKind {
	switch {
	case checkout.Worktree.Main:
		return flowtui.SurfaceCanonical
	case len(conflicts) > 0:
		return flowtui.SurfaceConflict
	case checkout.HarnessEvidence != nil:
		return flowtui.SurfaceHarness
	case tracked != nil && managedExact:
		return flowtui.SurfaceManaged
	default:
		return flowtui.SurfaceUnmanaged
	}
}

func flowCheckoutLabel(checkout inventory.RepoCheckout, tracked *task.Task, kind flowtui.SurfaceKind) string {
	branch := checkout.Branch()
	if branch == "" {
		branch = "detached HEAD"
	}
	if tracked != nil {
		return tracked.Title() + " · " + branch
	}
	switch kind {
	case flowtui.SurfaceCanonical:
		return "Main · " + branch
	case flowtui.SurfaceHarness:
		return "Harness · " + branch
	case flowtui.SurfaceConflict:
		return "Conflict · " + branch
	default:
		return branch
	}
}

func flowGitLocator(contextSnapshot inventory.RepoContext, checkout inventory.RepoCheckout, base string) taskflow.Locator {
	return taskflow.Locator{
		RepoKey: contextSnapshot.RepositoryID, RowKey: checkout.ID, RowKind: "checkout",
		RepositoryID: contextSnapshot.RepositoryID, GitCommonDir: contextSnapshot.RepositoryID,
		RepoPath: flowRepositoryQueryPath(contextSnapshot), CheckoutPath: checkout.ID,
		Branch: checkout.Branch(), Base: base, Upstream: checkout.Status.Upstream, Remote: "origin",
		HeadOID: checkout.Worktree.Head,
	}
}

func exactFlowLocator(locator taskflow.Locator) bool {
	return locator.RepoKey != "" && locator.RepositoryID != "" && locator.GitCommonDir != "" &&
		locator.RepoPath != "" && locator.CheckoutPath != "" && locator.Branch != "" && locator.HeadOID != ""
}

func exactFlowRemoteLocator(locator taskflow.Locator) bool {
	return locator.RepoKey != "" && locator.GitCommonDir != "" && locator.RepoPath != "" &&
		locator.Branch != "" && locator.HeadOID != ""
}

func flowCheckoutEvidence(contextSnapshot inventory.RepoContext, checkout inventory.RepoCheckout) []string {
	branch := checkout.Branch()
	if branch == "" {
		branch = "(detached)"
	}
	lines := []string{
		"path: " + checkout.Worktree.Path,
		"branch: " + branch,
		fmt.Sprintf("registered: Main=%t locked=%t prunable=%t bare=%t detached=%t HEAD=%s",
			checkout.Worktree.Main, checkout.Worktree.Locked, checkout.Worktree.Prunable,
			checkout.Worktree.Bare, checkout.Worktree.Detached, emptyEvidence(checkout.Worktree.Head)),
	}
	if checkout.Worktree.Locked {
		lines = append(lines, "registered lock reason: "+emptyEvidence(checkout.Worktree.LockedReason))
	}
	if checkout.Worktree.Prunable {
		lines = append(lines, "registered prunable reason: "+emptyEvidence(checkout.Worktree.PrunableReason))
	}
	switch {
	case checkout.StatusErr != nil:
		lines = append(lines, "git: ERROR "+checkout.StatusErr.Error())
	case checkout.Worktree.Bare:
		lines = append(lines, "git: status unobserved (bare repository)")
	case checkout.Worktree.Prunable || !checkout.Exists:
		lines = append(lines, "git: status unobserved (checkout unavailable)")
	default:
		cleanliness := "clean"
		if checkout.Status.Dirty() {
			cleanliness = "dirty — " + checkout.Status.Breakdown()
		}
		lines = append(lines,
			"git: "+cleanliness,
			"upstream: "+emptyEvidence(checkout.Status.Upstream),
			fmt.Sprintf("ahead=%d behind=%d conflicted=%d", checkout.Status.Ahead, checkout.Status.Behind, checkout.Status.Conflicted),
		)
	}

	observation := checkout.RuntimeObservation
	if observation.State == "" {
		observation = contextSnapshot.RuntimeObservation
	}
	switch observation.State {
	case inventory.ObservationAvailable:
		if len(checkout.Sessions) == 0 {
			lines = append(lines, "runtime "+emptyEvidence(contextSnapshot.Runtime)+": observed; no covering sessions")
		} else {
			lines = append(lines, "runtime "+emptyEvidence(contextSnapshot.Runtime)+": observed")
			for _, session := range checkout.Sessions {
				status := session.AgentStatus
				if status == "" {
					status = "unknown"
				}
				line := fmt.Sprintf("runtime session: %s:%s agent-state=%s", contextSnapshot.Runtime, emptyEvidence(session.Handle), status)
				if len(session.AgentSessions) > 0 {
					line += " agents=" + strings.Join(session.AgentSessions, ",")
				}
				lines = append(lines, line)
				for _, pane := range session.Panes {
					if pane.Agent == "" && pane.AgentSession == "" && pane.AgentStatus == "" {
						continue
					}
					paneStatus := pane.AgentStatus
					if paneStatus == "" {
						paneStatus = "unknown"
					}
					lines = append(lines, fmt.Sprintf("runtime agent pane: %s agent=%s state=%s session=%s",
						emptyEvidence(pane.ID), emptyEvidence(pane.Agent), paneStatus, emptyEvidence(pane.AgentSession)))
				}
			}
		}
	case inventory.ObservationFailed:
		message := "runtime observation failed"
		if observation.Err != nil {
			message += ": " + observation.Err.Error()
		}
		lines = append(lines, message)
	default:
		lines = append(lines, "runtime "+emptyEvidence(contextSnapshot.Runtime)+": unobserved; state is not known closed")
	}
	return lines
}

func flowTaskOnlyRuntimeEvidence(contextSnapshot inventory.RepoContext) []string {
	observation := contextSnapshot.RuntimeObservation
	switch observation.State {
	case inventory.ObservationAvailable:
		return []string{"runtime " + emptyEvidence(contextSnapshot.Runtime) + ": observed; task has no checkout whose coverage can be established"}
	case inventory.ObservationFailed:
		message := "runtime observation failed"
		if observation.Err != nil {
			message += ": " + observation.Err.Error()
		}
		return []string{message}
	default:
		return []string{"runtime " + emptyEvidence(contextSnapshot.Runtime) + ": unobserved; state is not known closed"}
	}
}

func flowTaskEvidence(tracked *task.Task) []string {
	if tracked == nil {
		return nil
	}
	lines := []string{
		fmt.Sprintf("task: %s mode=%s state=%s", tracked.ID, tracked.EffectiveMode(), tracked.State),
		"task owner: " + emptyEvidence(tracked.Owner),
		"task next: " + emptyEvidence(tracked.Next),
		"base: " + emptyEvidence(tracked.Base),
	}
	if tracked.RuntimeName != "" || tracked.RuntimeHandle != "" {
		lines = append(lines, fmt.Sprintf("task runtime hint: %s:%s", emptyEvidence(tracked.RuntimeName), emptyEvidence(tracked.RuntimeHandle)))
	}
	return lines
}

func topologyReasonLines(reasons []inventory.TopologyReason) []string {
	lines := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		lines = append(lines, string(reason.Code)+": "+reason.Detail)
	}
	return uniqueStrings(lines)
}

func artifactSummary(inspection artifact.ReadinessInspection, err error) string {
	if err != nil {
		return "artifact: ERROR " + err.Error()
	}
	if inspection.KnownEmpty && len(inspection.Intents) == 0 {
		return "artifact: ready; no intents"
	}
	counts := make(map[artifact.ReadinessState]int)
	for _, intent := range inspection.Intents {
		counts[intent.State]++
	}
	states := make([]string, 0, len(counts))
	for state, count := range counts {
		states = append(states, fmt.Sprintf("%s=%d", state, count))
	}
	sort.Strings(states)
	readiness := "blocked or unknown"
	if inspection.Ready() {
		readiness = "ready"
	}
	if len(states) == 0 {
		states = append(states, "no classified intents")
	}
	return "artifact: " + readiness + "; " + strings.Join(states, ", ")
}

func managedFlowChoices(tracked task.Task) []flowtui.ActionChoice {
	choice := func(id, label, description string, options taskflow.ActionOptions) flowtui.ActionChoice {
		return flowtui.NewActionChoice(id, label, description, options)
	}
	candidates := []flowtui.ActionChoice{
		choice("park-warm", "Park Warm", "Retain the checkout and branch while recording WARM intent.", taskflow.ParkWarmOptions{}),
		choice("park-cold", "Park Cold", "Require committed, pushed, reconstructible work before checkout cleanup.", taskflow.ParkColdOptions{}),
		choice("park-cold-push", "Park Cold + Push", "Push the branch, then park COLD only when every guard is ready.", taskflow.ParkColdOptions{Push: true}),
		choice("resume", "Resume", "Fetch refs and rebuild or reopen the exact task checkout.", taskflow.ResumeOptions{FetchRefs: true}),
		choice("complete-direct", "Complete Direct", "Record direct work DONE without changing dirty content.", taskflow.CompleteDirectOptions{Dirty: taskflow.DirtyFail}),
		choice("complete-ff", "Complete FF", "Fast-forward the explicit base only when dirty content needs no mutation.", taskflow.CompleteFFOptions{Dirty: taskflow.DirtyFail}),
		choice("review-handoff", "Review Handoff", "Push and create a review while preserving the current lifecycle state.", taskflow.ReviewHandoffOptions{Dirty: taskflow.DirtyFail}),
		choice("verify-merged", "Verify Merged", "Prove external integration before recording DONE.", taskflow.VerifyMergedOptions{Dirty: taskflow.DirtyFail}),
		choice("retire-keep-branch", "Retire (Keep Branch)", "Remove local task resources while retaining the branch.", taskflow.RetireOptions{}),
	}
	if tracked.EffectiveMode() != task.ModeDirect && tracked.Branch != tracked.Base {
		candidates = append(candidates,
			choice("retire-delete-branch", "Retire + Delete Contained Branch", "Delete the branch only when fresh containment evidence proves it safe.", taskflow.RetireOptions{DeleteBranch: true}),
		)
	}

	mode, state := tracked.EffectiveMode(), tracked.State
	choices := make([]flowtui.ActionChoice, 0, len(candidates))
	for _, candidate := range candidates {
		rule, found := taskflow.LookupTransition(mode, state, candidate.Action())
		if found && rule.Allowed {
			choices = append(choices, candidate)
		}
	}
	return choices
}

func unmanagedFlowChoices(base string) []flowtui.ActionChoice {
	choices := make([]flowtui.ActionChoice, 0, 2)
	if base != "" {
		choices = append(choices, flowtui.NewActionChoice(
			"adopt", "Adopt", "Create metadata for this exact linked checkout without changing Git content.",
			taskflow.AdoptOptions{Mode: task.ModeWorktree, Base: base},
		))
	}
	return append(choices, flowtui.NewActionChoice(
		"remove-checkout", "Remove Checkout", "Remove only a clean exact linked checkout and always preserve its branch.",
		taskflow.RemoveCheckoutOptions{},
	))
}

func remoteFlowChoices() []flowtui.ActionChoice {
	return []flowtui.ActionChoice{
		flowtui.NewActionChoice("remote-fetch", "Remote: Fetch Refs", "Fetch and prune the exact configured remote.", taskflow.RefreshRemoteOptions{FetchRefs: true}),
		flowtui.NewActionChoice("remote-review", "Remote: Query Review", "Query the exact head/base review relationship without fetching.", taskflow.RefreshRemoteOptions{QueryReview: true}),
		flowtui.NewActionChoice("remote-both", "Remote: Fetch + Review", "Fetch refs, then query the exact review relationship.", taskflow.RefreshRemoteOptions{FetchRefs: true, QueryReview: true}),
	}
}

func (l *flowLoader) Plan(
	ctx context.Context,
	repoKey, rowKey, actionID string,
	locator taskflow.Locator,
	options taskflow.ActionOptions,
) (taskflow.Plan, error) {
	expected, request, err := l.validateTarget(repoKey, rowKey, actionID, locator, options)
	if err != nil {
		return taskflow.Plan{}, err
	}
	rt, err := l.runtime.Resolve(ctx)
	if err != nil {
		return taskflow.Plan{}, err
	}
	service, err := l.service(rt)
	if err != nil {
		return taskflow.Plan{}, err
	}
	plan, err := service.Plan(ctx, request)
	if err != nil {
		return taskflow.Plan{}, err
	}
	if err := validateFlowPlanIdentity(plan, expected); err != nil {
		return taskflow.Plan{}, err
	}
	l.mu.Lock()
	l.approved[plan.PlanID] = flowApprovedPlan{target: expected, plan: plan.Clone()}
	l.mu.Unlock()
	return plan, nil
}

func (l *flowLoader) Apply(
	ctx context.Context,
	repoKey, rowKey, actionID string,
	locator taskflow.Locator,
	options taskflow.ActionOptions,
	plan taskflow.Plan,
	approval taskflow.Approval,
) (taskflow.Result, error) {
	expected, _, err := l.validateTarget(repoKey, rowKey, actionID, locator, options)
	if err != nil {
		return taskflow.Result{}, err
	}
	l.mu.Lock()
	approved, ok := l.approved[plan.PlanID]
	l.mu.Unlock()
	if !ok {
		return taskflow.Result{}, errors.New("flow apply rejected a plan not produced by this run")
	}
	if !sameFlowTarget(approved.target, expected) {
		return taskflow.Result{}, errors.New("flow apply target differs from the approved repository, row, action, locator, or options")
	}
	if !reflect.DeepEqual(approved.plan.Clone(), plan.Clone()) {
		return taskflow.Result{}, errors.New("flow apply plan differs from the exact approved plan")
	}
	if err := validateFlowPlanIdentity(plan, expected); err != nil {
		return taskflow.Result{}, err
	}
	rt, err := l.runtime.Resolve(ctx)
	if err != nil {
		return taskflow.Result{}, err
	}
	service, err := l.service(rt)
	if err != nil {
		return taskflow.Result{}, err
	}
	result, applyErr := service.Apply(ctx, plan, approval)
	l.mu.Lock()
	delete(l.approved, plan.PlanID)
	if remote, exists := result.RemoteObservation(); exists {
		l.remote[repoKey] = remote.Clone()
	}
	l.mu.Unlock()
	return result, applyErr
}

func (l *flowLoader) validateTarget(
	repoKey, rowKey, actionID string,
	locator taskflow.Locator,
	options taskflow.ActionOptions,
) (flowTarget, taskflow.Request, error) {
	request, err := taskflow.NewRequest(locator, options)
	if err != nil {
		return flowTarget{}, taskflow.Request{}, err
	}
	key := flowTargetKey(repoKey, rowKey, actionID)
	l.mu.Lock()
	expected, ok := l.targets[repoKey][key]
	l.mu.Unlock()
	if !ok {
		return flowTarget{}, taskflow.Request{}, errors.New("flow action no longer matches the current repository snapshot")
	}
	actual := flowTarget{repoKey: repoKey, rowKey: rowKey, actionID: actionID, request: request}
	if !sameFlowTarget(expected, actual) {
		return flowTarget{}, taskflow.Request{}, errors.New("flow action locator or options differ from the current exact row choice")
	}
	return expected, request, nil
}

func validateFlowPlanIdentity(plan taskflow.Plan, expected flowTarget) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Locator != expected.request.Locator || plan.Request.Locator != expected.request.Locator ||
		plan.Action != expected.request.Action || plan.Request.Action != expected.request.Action ||
		!reflect.DeepEqual(plan.Request.Options, expected.request.Options) {
		return errors.New("taskflow plan does not match the exact flow request identity")
	}
	return nil
}

func sameFlowTarget(left, right flowTarget) bool {
	return left.repoKey == right.repoKey && left.rowKey == right.rowKey && left.actionID == right.actionID &&
		left.request.Locator == right.request.Locator && left.request.Action == right.request.Action &&
		reflect.DeepEqual(left.request.Options, right.request.Options)
}

func flowTargetKey(repoKey, rowKey, actionID string) string {
	return repoKey + "\x00" + rowKey + "\x00" + actionID
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("flow operation needs a context")
	}
	return ctx.Err()
}

func emptyEvidence(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
