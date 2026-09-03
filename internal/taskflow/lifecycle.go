package taskflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

// RuntimeResolver returns the runtime selected for this invocation.
type RuntimeResolver func() runtime.Runtime

// NamedRuntimeResolver returns the backend that owns a persisted runtime
// handle. It must not reinterpret an opaque handle using a different backend.
type NamedRuntimeResolver func(string) runtime.Runtime

// LogFunc receives non-sensitive lifecycle progress. Production callers may
// route it to stderr; a nil logger discards progress.
type LogFunc func(string, ...any)

// Narrow operation types make safety ordering observable without exposing a
// cli.App or requiring tests to replace whole handlers.
type (
	RepoLockFunc         func(context.Context, string, func() error) error
	GitDiscoverFunc      func(context.Context, string) (gitx.Repo, error)
	GitStatusFunc        func(context.Context, string) (gitx.Status, error)
	GitInProgressFunc    func(context.Context, string) (string, bool, error)
	GitRunFunc           func(context.Context, string, ...string) (string, error)
	GitRemoteFunc        func(context.Context, string, string) string
	GitRefStateFunc      func(context.Context, string, string) (bool, error)
	GitDefaultBranchFunc func(context.Context, string) string
	GitWorktreesFunc     func(context.Context, string) ([]gitx.Worktree, error)
	ResolveWorktreeFunc  func(context.Context, string, string) (gitx.RegisteredWorktree, error)
	WIPCommitFunc        func(context.Context, string, string) (bool, error)
	AnalyzeFinishFunc    func(context.Context, string, string, string) (gitx.FinishAnalysis, error)
	CommitAllFunc        func(context.Context, string, string) error
	DiscardAllFunc       func(context.Context, string) error
	IsAncestorFunc       func(context.Context, string, string, string) (bool, error)
	RemoveWorktreeFunc   func(context.Context, string, string, bool) error
	ArtifactInspectFunc  func(context.Context, *artifact.Store, string) (artifact.ReadinessInspection, error)
	OccupancyInspectFunc func(context.Context, runtime.Runtime, string, runtime.OccupancyOptions) (runtime.Occupancy, error)
	CleanupInspectFunc   func(context.Context, runtime.Runtime, string, retire.Options) (retire.Inspection, error)
	CleanupCloseFunc     func(context.Context, runtime.Runtime, string, retire.Options) (retire.Inspection, error)
	RuntimeOpenFunc      func(context.Context, runtime.Runtime, string, string) (runtime.OpenResult, error)
	WorktreeCreateFunc   func(context.Context, wt.CreateRequest) (*wt.CreateResult, error)
	ForgeDetectFunc      func(context.Context, string) forge.Kind
	ForgeResolveFunc     func(forge.Kind) (forge.Forge, error)
	CreatePRFunc         func(context.Context, forge.Forge, string, forge.PRRequest) (string, error)
	QueryReviewFunc      func(context.Context, forge.Forge, string, forge.ReviewQuery) (*forge.Review, error)
	TaskCreateFunc       func(*task.Tx, *task.Task) (*task.Record, error)
	TaskUpdateFunc       func(*task.Tx, *task.Task, string) (*task.Record, error)
	TaskDeleteFunc       func(*task.Tx, string, string) error
	CanonicalPathFunc    func(string) (string, error)
)

// LifecycleHooks replaces individual mechanisms. Zero fields use the existing
// domain implementations; policy remains in taskflow.
type LifecycleHooks struct {
	RepoLock         RepoLockFunc
	GitDiscover      GitDiscoverFunc
	GitStatus        GitStatusFunc
	GitInProgress    GitInProgressFunc
	GitRun           GitRunFunc
	GitRemote        GitRemoteFunc
	GitRefState      GitRefStateFunc
	GitDefaultBranch GitDefaultBranchFunc
	GitWorktrees     GitWorktreesFunc
	ResolveWorktree  ResolveWorktreeFunc
	WIPCommit        WIPCommitFunc
	AnalyzeFinish    AnalyzeFinishFunc
	CommitAll        CommitAllFunc
	DiscardAll       DiscardAllFunc
	IsAncestor       IsAncestorFunc
	RemoveWorktree   RemoveWorktreeFunc
	InspectArtifacts ArtifactInspectFunc
	InspectOccupancy OccupancyInspectFunc
	InspectCleanup   CleanupInspectFunc
	CloseAndWait     CleanupCloseFunc
	OpenRuntime      RuntimeOpenFunc
	CreateWorktree   WorktreeCreateFunc
	DetectForge      ForgeDetectFunc
	ResolveForge     ForgeResolveFunc
	CreatePR         CreatePRFunc
	QueryReview      QueryReviewFunc
	TaskCreate       TaskCreateFunc
	TaskUpdate       TaskUpdateFunc
	TaskDelete       TaskDeleteFunc
	CanonicalPath    CanonicalPathFunc
}

// LifecycleConfig contains production dependencies and host-local caller
// identity. Tasks and Artifacts are required so destructive plans can never
// silently omit their durable guards.
type LifecycleConfig struct {
	Config    config.Config
	Tasks     *task.Store
	Artifacts *artifact.Store

	DefaultRuntime RuntimeResolver
	NamedRuntime   NamedRuntimeResolver

	Host                string
	CWD                 string
	CallerWorkspaceID   string
	CallerPaneID        string
	AllowSharedCheckout bool
	Clock               func() time.Time
	Logf                LogFunc
	Hooks               LifecycleHooks
}

type lifecycleService struct {
	cfg       config.Config
	tasks     *task.Store
	artifacts *artifact.Store

	defaultRuntime      RuntimeResolver
	namedRuntime        NamedRuntimeResolver
	host                string
	cwd                 string
	callerWorkspace     string
	callerPane          string
	allowSharedCheckout bool
	clock               func() time.Time
	logf                LogFunc

	repoLock         RepoLockFunc
	gitDiscover      GitDiscoverFunc
	gitStatus        GitStatusFunc
	gitInProgress    GitInProgressFunc
	gitRun           GitRunFunc
	gitRemote        GitRemoteFunc
	gitRefState      GitRefStateFunc
	gitDefaultBranch GitDefaultBranchFunc
	gitWorktrees     GitWorktreesFunc
	resolveWorktree  ResolveWorktreeFunc
	wipCommit        WIPCommitFunc
	analyzeFinish    AnalyzeFinishFunc
	commitAll        CommitAllFunc
	discardAll       DiscardAllFunc
	isAncestor       IsAncestorFunc
	removeWorktree   RemoveWorktreeFunc
	inspectArtifacts ArtifactInspectFunc
	inspectOccupancy OccupancyInspectFunc
	inspectCleanup   CleanupInspectFunc
	closeAndWait     CleanupCloseFunc
	openRuntime      RuntimeOpenFunc
	createWorktree   WorktreeCreateFunc
	detectForge      ForgeDetectFunc
	resolveForge     ForgeResolveFunc
	createPR         CreatePRFunc
	queryReview      QueryReviewFunc
	taskCreate       TaskCreateFunc
	taskUpdate       TaskUpdateFunc
	taskDelete       TaskDeleteFunc
	canonicalPath    CanonicalPathFunc
}

// NewLifecycleService installs the concrete guarded parking, resume, and
// completion handlers. Actions without complete policy implementations remain
// unavailable rather than falling back to an unsafe generic executor.
func NewLifecycleService(input LifecycleConfig) (*Service, error) {
	implementation, err := newLifecycleImplementation(input)
	if err != nil {
		return nil, err
	}
	service := NewService(Handlers{
		ParkWarm:       implementation.handler(ParkWarm),
		ParkCold:       implementation.handler(ParkCold),
		Resume:         implementation.handler(Resume),
		CompleteDirect: implementation.handler(CompleteDirect),
		CompleteFF:     implementation.handler(CompleteFF),
		ReviewHandoff:  implementation.handler(ReviewHandoff),
		VerifyMerged:   implementation.handler(VerifyMerged),
		Retire:         implementation.retireHandler(),
		Adopt:          implementation.adoptHandler(),
		RemoveCheckout: implementation.removeCheckoutHandler(),
		RefreshRemote:  implementation.refreshRemoteHandler(),
	})
	service.locateTask = implementation.locateExactTask
	return service, nil
}

func newLifecycleImplementation(input LifecycleConfig) (*lifecycleService, error) {
	if input.Tasks == nil {
		return nil, errors.New("taskflow lifecycle service requires a task store")
	}
	if input.Artifacts == nil {
		return nil, errors.New("taskflow lifecycle service requires an artifact store")
	}
	if err := input.Config.Validate(); err != nil {
		return nil, fmt.Errorf("taskflow lifecycle config: %w", err)
	}

	host := strings.TrimSpace(input.Host)
	if host == "" {
		host = config.Hostname()
	}
	if host == "" || host != strings.TrimSpace(host) || strings.ContainsRune(host, '\x00') {
		return nil, errors.New("taskflow lifecycle host must be normalized")
	}
	cwd := input.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve taskflow caller directory: %w", err)
		}
	}
	callerWorkspace := input.CallerWorkspaceID
	if callerWorkspace == "" {
		callerWorkspace = os.Getenv("HERDR_WORKSPACE_ID")
	}
	callerPane := input.CallerPaneID
	if callerPane == "" {
		callerPane = os.Getenv("HERDR_PANE_ID")
		if callerPane == "" {
			callerPane = os.Getenv("TMUX_PANE")
		}
	}
	clock := input.Clock
	if clock == nil {
		clock = time.Now
	}
	logf := input.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	defaultRuntime := input.DefaultRuntime
	if defaultRuntime == nil {
		defaultRuntime = func() runtime.Runtime {
			return configuredRuntime(input.Config, input.Config.Runtime.Backend)
		}
	}
	namedRuntime := input.NamedRuntime
	if namedRuntime == nil {
		namedRuntime = func(name string) runtime.Runtime {
			return configuredRuntime(input.Config, name)
		}
	}

	s := &lifecycleService{
		cfg: input.Config, tasks: input.Tasks, artifacts: input.Artifacts,
		defaultRuntime: defaultRuntime, namedRuntime: namedRuntime,
		host: host, cwd: cwd,
		callerWorkspace: callerWorkspace, callerPane: callerPane,
		allowSharedCheckout: input.AllowSharedCheckout,
		clock:               clock, logf: logf,
		repoLock: func(ctx context.Context, commonDir string, operation func() error) error {
			return lockx.WithDir(ctx, filepath.Join(commonDir, "dev-taskflow"), "taskflow repository", operation)
		},
		gitDiscover: gitx.Discover,
		gitStatus:   gitx.StatusOf,
		gitInProgress: func(ctx context.Context, dir string) (string, bool, error) {
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
			return gitx.InProgress(dir)
		},
		gitRun:           gitx.Run,
		gitRemote:        gitx.Remote,
		gitRefState:      gitx.RefState,
		gitDefaultBranch: gitx.DefaultBranch,
		gitWorktrees:     gitx.Worktrees,
		resolveWorktree:  gitx.ResolveRegisteredWorktree,
		wipCommit:        gitx.WipCommit,
		analyzeFinish:    gitx.AnalyzeFinish,
		commitAll:        gitx.CommitAllChanges,
		discardAll:       gitx.DiscardAllChanges,
		isAncestor:       gitIsAncestor,
		removeWorktree:   gitx.RemoveWorktree,
		inspectArtifacts: artifact.InspectReadiness,
		inspectOccupancy: runtime.InspectOccupancy,
		inspectCleanup:   retire.Inspect,
		closeAndWait:     retire.CloseAndWait,
		openRuntime:      openRuntimeSurface,
		detectForge:      forge.Detect,
		resolveForge:     forge.For,
		createPR: func(ctx context.Context, provider forge.Forge, dir string, request forge.PRRequest) (string, error) {
			if provider == nil {
				return "", errors.New("create review: no forge provider")
			}
			return provider.CreatePR(ctx, dir, request)
		},
		queryReview: func(ctx context.Context, provider forge.Forge, dir string, query forge.ReviewQuery) (*forge.Review, error) {
			if provider == nil {
				return nil, errors.New("query review: no forge provider")
			}
			return forge.QueryReview(ctx, provider, dir, query)
		},
		taskCreate: func(tx *task.Tx, candidate *task.Task) (*task.Record, error) {
			return tx.Create(candidate)
		},
		taskUpdate: func(tx *task.Tx, candidate *task.Task, revision string) (*task.Record, error) {
			return tx.Update(candidate, revision)
		},
		taskDelete: func(tx *task.Tx, id, revision string) error {
			return tx.DeleteIfRevision(id, revision)
		},
		canonicalPath: pathx.Canonical,
	}
	s.createWorktree = func(ctx context.Context, request wt.CreateRequest) (*wt.CreateResult, error) {
		manager := &wt.Manager{Cfg: s.cfg, Log: lifecycleLogWriter{s.logf}}
		return manager.Create(ctx, request)
	}
	applyLifecycleHooks(s, input.Hooks)
	return s, nil
}

func configuredRuntime(cfg config.Config, name string) runtime.Runtime {
	rt := runtime.Select(name)
	if herdr, ok := rt.(*runtime.Herdr); ok {
		return herdr.WithMetadataSource(cfg.Runtime.MetadataSource)
	}
	return rt
}

func applyLifecycleHooks(s *lifecycleService, hooks LifecycleHooks) {
	if hooks.RepoLock != nil {
		s.repoLock = hooks.RepoLock
	}
	if hooks.GitDiscover != nil {
		s.gitDiscover = hooks.GitDiscover
	}
	if hooks.GitStatus != nil {
		s.gitStatus = hooks.GitStatus
	}
	if hooks.GitInProgress != nil {
		s.gitInProgress = hooks.GitInProgress
	}
	if hooks.GitRun != nil {
		s.gitRun = hooks.GitRun
	}
	if hooks.GitRemote != nil {
		s.gitRemote = hooks.GitRemote
	}
	if hooks.GitRefState != nil {
		s.gitRefState = hooks.GitRefState
	}
	if hooks.GitDefaultBranch != nil {
		s.gitDefaultBranch = hooks.GitDefaultBranch
	}
	if hooks.GitWorktrees != nil {
		s.gitWorktrees = hooks.GitWorktrees
	}
	if hooks.ResolveWorktree != nil {
		s.resolveWorktree = hooks.ResolveWorktree
	}
	if hooks.WIPCommit != nil {
		s.wipCommit = hooks.WIPCommit
	}
	if hooks.AnalyzeFinish != nil {
		s.analyzeFinish = hooks.AnalyzeFinish
	}
	if hooks.CommitAll != nil {
		s.commitAll = hooks.CommitAll
	}
	if hooks.DiscardAll != nil {
		s.discardAll = hooks.DiscardAll
	}
	if hooks.IsAncestor != nil {
		s.isAncestor = hooks.IsAncestor
	}
	if hooks.RemoveWorktree != nil {
		s.removeWorktree = hooks.RemoveWorktree
	}
	if hooks.InspectArtifacts != nil {
		s.inspectArtifacts = hooks.InspectArtifacts
	}
	if hooks.InspectOccupancy != nil {
		s.inspectOccupancy = hooks.InspectOccupancy
	}
	if hooks.InspectCleanup != nil {
		s.inspectCleanup = hooks.InspectCleanup
	}
	if hooks.CloseAndWait != nil {
		s.closeAndWait = hooks.CloseAndWait
	}
	if hooks.OpenRuntime != nil {
		s.openRuntime = hooks.OpenRuntime
	}
	if hooks.CreateWorktree != nil {
		s.createWorktree = hooks.CreateWorktree
	}
	if hooks.DetectForge != nil {
		s.detectForge = hooks.DetectForge
	}
	if hooks.ResolveForge != nil {
		s.resolveForge = hooks.ResolveForge
	}
	if hooks.CreatePR != nil {
		s.createPR = hooks.CreatePR
	}
	if hooks.QueryReview != nil {
		s.queryReview = hooks.QueryReview
	}
	if hooks.TaskCreate != nil {
		s.taskCreate = hooks.TaskCreate
	}
	if hooks.TaskUpdate != nil {
		s.taskUpdate = hooks.TaskUpdate
	}
	if hooks.TaskDelete != nil {
		s.taskDelete = hooks.TaskDelete
	}
	if hooks.CanonicalPath != nil {
		s.canonicalPath = hooks.CanonicalPath
	}
}

func (s *lifecycleService) handler(action Action) Handler {
	return Handler{
		Plan: func(ctx context.Context, request Request) (PlanSpec, error) {
			return s.planSpec(ctx, request)
		},
		Apply: func(ctx context.Context, plan Plan) (Result, error) {
			return s.applyGuarded(ctx, action, plan)
		},
	}
}

func (s *lifecycleService) now() time.Time { return s.clock().UTC() }

func (s *lifecycleService) runtimeFor(candidate task.Task) (runtime.Runtime, error) {
	var rt runtime.Runtime
	if candidate.RuntimeHandle != "" && candidate.RuntimeName != "" {
		rt = s.namedRuntime(candidate.RuntimeName)
		if rt == nil {
			return nil, fmt.Errorf("recorded runtime backend %q is unavailable", candidate.RuntimeName)
		}
		if rt.Name() != candidate.RuntimeName {
			return nil, fmt.Errorf("recorded runtime backend %q resolved to %q", candidate.RuntimeName, rt.Name())
		}
	} else {
		rt = s.defaultRuntime()
	}
	if rt == nil || strings.TrimSpace(rt.Name()) == "" {
		return nil, errors.New("taskflow runtime resolver returned no backend")
	}
	return rt, nil
}

func openRuntimeSurface(ctx context.Context, rt runtime.Runtime, path, label string) (runtime.OpenResult, error) {
	if rt == nil {
		return runtime.OpenResult{}, errors.New("open runtime: no backend")
	}
	if worktreeRuntime, ok := rt.(runtime.WorktreeOpener); ok {
		return worktreeRuntime.OpenWorktree(ctx, path, label)
	}
	return rt.Open(ctx, path, label)
}

type lifecycleLogWriter struct{ logf LogFunc }

func (w lifecycleLogWriter) Write(data []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line != "" {
			w.logf("%s", line)
		}
	}
	return len(data), nil
}

var _ io.Writer = lifecycleLogWriter{}
