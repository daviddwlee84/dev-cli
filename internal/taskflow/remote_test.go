package taskflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

const (
	remoteTestHead       = "1111111111111111111111111111111111111111"
	remoteTestBase       = "2222222222222222222222222222222222222222"
	remoteTestRemoteHead = "3333333333333333333333333333333333333333"
	remoteTestRemoteBase = "4444444444444444444444444444444444444444"
)

type remoteCapableForge struct {
	kind      forge.Kind
	available bool
}

func (f *remoteCapableForge) Kind() forge.Kind             { return f.kind }
func (f *remoteCapableForge) Bin() string                  { return "test-" + string(f.kind) }
func (f *remoteCapableForge) Available() bool              { return f.available }
func (f *remoteCapableForge) CloneURL(value string) string { return value }
func (f *remoteCapableForge) CreatePR(context.Context, string, forge.PRRequest) (string, error) {
	return "", errors.New("unexpected CreatePR")
}
func (f *remoteCapableForge) CreateRepo(context.Context, string, forge.RepoRequest) (string, error) {
	return "", errors.New("unexpected CreateRepo")
}
func (f *remoteCapableForge) ListRepos(context.Context) ([]forge.RemoteRepo, error) {
	return nil, errors.New("unexpected ListRepos")
}
func (f *remoteCapableForge) QueryReview(context.Context, string, forge.ReviewQuery) (*forge.Review, error) {
	return nil, errors.New("unexpected direct QueryReview")
}

type remoteNoReviewForge struct{ available bool }

func (f remoteNoReviewForge) Kind() forge.Kind             { return forge.GitHub }
func (f remoteNoReviewForge) Bin() string                  { return "test-github" }
func (f remoteNoReviewForge) Available() bool              { return f.available }
func (f remoteNoReviewForge) CloneURL(value string) string { return value }
func (remoteNoReviewForge) CreatePR(context.Context, string, forge.PRRequest) (string, error) {
	return "", errors.New("unexpected CreatePR")
}
func (remoteNoReviewForge) CreateRepo(context.Context, string, forge.RepoRequest) (string, error) {
	return "", errors.New("unexpected CreateRepo")
}
func (remoteNoReviewForge) ListRepos(context.Context) ([]forge.RemoteRepo, error) {
	return nil, errors.New("unexpected ListRepos")
}

type remoteTestHarness struct {
	repo       string
	common     string
	checkout   string
	discoverAt string

	cfg       config.Config
	tasks     *task.Store
	artifacts *artifact.Store
	record    task.Record
	service   *Service

	remoteURL     string
	refs          map[string]string
	provider      forge.Forge
	resolveErr    error
	fetchErr      error
	fetchMutation func()
	queryReview   *forge.Review
	queryErr      error
	clock         time.Time

	calls          []string
	remoteNames    []string
	queries        []forge.ReviewQuery
	taskWriteCalls int
}

func newRemoteTestHarness(t *testing.T) *remoteTestHarness {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	common := filepath.Join(repo, ".git")
	checkout := filepath.Join(root, "topic-checkout")
	for _, dir := range []string{repo, common, checkout} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Paths.StateDir = filepath.Join(root, "state")
	tasks := task.NewStore(cfg.TasksDir())
	artifacts := artifact.NewStore(filepath.Join(cfg.StateDir(), "artifact-intents", "v1"))
	candidate := task.Task{
		Name: "topic", Repo: "repo", RepoPath: repo,
		Branch: "topic", Base: "main", WorktreePath: checkout,
		Mode: task.ModeWorktree, State: task.Hot, Owner: "test-host",
	}
	created, err := tasks.Create(context.Background(), &candidate)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	h := &remoteTestHarness{
		repo: repo, common: common, checkout: checkout, discoverAt: repo,
		cfg: cfg, tasks: tasks, artifacts: artifacts, record: *created,
		remoteURL: "https://github.com/acme/widget.git",
		refs: map[string]string{
			"refs/heads/topic":          remoteTestHead,
			"refs/heads/main":           remoteTestBase,
			"refs/remotes/origin/topic": remoteTestRemoteHead,
			"refs/remotes/origin/main":  remoteTestRemoteBase,
		},
		provider: &remoteCapableForge{kind: forge.GitHub, available: true},
		clock:    time.Unix(1_700_000_000, 0).UTC(),
	}
	h.service = h.newService(t)
	return h
}

func (h *remoteTestHarness) newService(t *testing.T) *Service {
	t.Helper()
	service, err := NewLifecycleService(LifecycleConfig{
		Config: h.cfg, Tasks: h.tasks, Artifacts: h.artifacts,
		Host: "test-host", CWD: h.repo,
		Clock: func() time.Time { return h.clock },
		Hooks: LifecycleHooks{
			RepoLock: func(_ context.Context, commonDir string, operation func() error) error {
				h.calls = append(h.calls, "repo-lock:"+commonDir)
				return operation()
			},
			GitDiscover: func(_ context.Context, dir string) (gitx.Repo, error) {
				root := h.discoverAt
				gitDir := h.common
				linked := false
				if dir == h.checkout {
					root = h.checkout
					gitDir = filepath.Join(h.common, "worktrees", "topic")
					linked = true
				}
				return gitx.Repo{
					Root: root, GitDir: gitDir, GitCommonDir: h.common,
					MainRoot: h.discoverAt, Name: "repo", IsLinkedWorktree: linked,
				}, nil
			},
			GitRun: func(_ context.Context, _ string, args ...string) (string, error) {
				if len(args) == 0 {
					return "", errors.New("empty Git invocation")
				}
				switch args[0] {
				case "check-ref-format":
					if len(args) != 2 || strings.Contains(args[1], "..") || strings.ContainsAny(args[1], " ~^:?*[\\") {
						return "", errors.New("invalid ref")
					}
					return "", nil
				case "rev-parse":
					if len(args) != 3 || args[1] != "--verify" {
						return "", fmt.Errorf("unexpected rev-parse args %q", args)
					}
					ref := strings.TrimSuffix(args[2], "^{commit}")
					if ref == "HEAD" {
						ref = "refs/heads/topic"
					}
					oid, ok := h.refs[ref]
					if !ok {
						return "", fmt.Errorf("unknown ref %s", ref)
					}
					return oid, nil
				case "fetch":
					h.calls = append(h.calls, "fetch:"+strings.Join(args[1:], " "))
					if h.fetchErr != nil {
						return "", h.fetchErr
					}
					if h.fetchMutation != nil {
						h.fetchMutation()
					}
					return "", nil
				default:
					return "", fmt.Errorf("unexpected Git invocation %q", args)
				}
			},
			GitRemote: func(_ context.Context, _ string, name string) string {
				h.remoteNames = append(h.remoteNames, name)
				return h.remoteURL
			},
			GitRefState: func(_ context.Context, _ string, ref string) (bool, error) {
				_, ok := h.refs[ref]
				return ok, nil
			},
			ResolveWorktree: func(_ context.Context, _ string, exact string) (gitx.RegisteredWorktree, error) {
				want, canonicalErr := pathx.Canonical(h.checkout)
				if canonicalErr != nil || exact != want {
					return gitx.RegisteredWorktree{}, fmt.Errorf("%w: %s", gitx.ErrWorktreeNotFound, exact)
				}
				repoPath, _ := pathx.Canonical(h.repo)
				commonDir, _ := pathx.Canonical(h.common)
				return gitx.RegisteredWorktree{
					RepositoryPath: repoPath, GitCommonDir: commonDir, Path: want,
					Worktree: gitx.Worktree{Path: want, Head: h.refs["refs/heads/topic"], Branch: "topic"},
				}, nil
			},
			ResolveForge: func(kind forge.Kind) (forge.Forge, error) {
				if h.resolveErr != nil {
					return nil, h.resolveErr
				}
				if h.provider == nil {
					return nil, nil
				}
				if h.provider.Kind() != kind {
					return h.provider, nil
				}
				return h.provider, nil
			},
			QueryReview: func(_ context.Context, _ forge.Forge, _ string, query forge.ReviewQuery) (*forge.Review, error) {
				h.calls = append(h.calls, "query:"+query.Repository+":"+query.Head+":"+query.Base)
				h.queries = append(h.queries, query)
				if h.queryReview == nil {
					return nil, h.queryErr
				}
				copy := *h.queryReview
				return &copy, h.queryErr
			},
			TaskCreate: func(*task.Tx, *task.Task) (*task.Record, error) {
				h.taskWriteCalls++
				return nil, errors.New("unexpected task create")
			},
			TaskUpdate: func(*task.Tx, *task.Task, string) (*task.Record, error) {
				h.taskWriteCalls++
				return nil, errors.New("unexpected task update")
			},
			TaskDelete: func(*task.Tx, string, string) error {
				h.taskWriteCalls++
				return errors.New("unexpected task delete")
			},
		},
	})
	if err != nil {
		t.Fatalf("NewLifecycleService: %v", err)
	}
	return service
}

func (h *remoteTestHarness) locator(t *testing.T, managed bool) Locator {
	t.Helper()
	locator := Locator{
		RepoKey: h.common, RepositoryID: h.common, GitCommonDir: h.common,
		RepoPath: h.repo, CheckoutPath: h.checkout,
		Branch: "topic", Base: "main", HeadOID: h.refs["refs/heads/topic"],
		BaseOID: h.refs["refs/heads/main"],
	}
	if managed {
		record, err := h.tasks.GetRecord(h.record.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		locator.TaskID = record.Task.ID
		locator.TaskRevision = record.Revision
		locator.Mode = record.Task.EffectiveMode()
		locator.State = record.Task.State
	}
	return locator
}

func (h *remoteTestHarness) request(t *testing.T, managed bool, options RefreshRemoteOptions) Request {
	t.Helper()
	return mustRequest(t, h.locator(t, managed), options)
}

func (h *remoteTestHarness) resetCalls() {
	h.calls = nil
	h.remoteNames = nil
	h.queries = nil
}

func (h *remoteTestHarness) networkCalls() []string {
	var calls []string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "fetch:") || strings.HasPrefix(call, "query:") {
			calls = append(calls, call)
		}
	}
	return calls
}

func (h *remoteTestHarness) review(state forge.ReviewState, draft bool) *forge.Review {
	return &forge.Review{
		Provider: forge.GitHub, Repository: "acme/widget", ID: "review-id", Number: 17,
		State: state, Draft: draft, URL: "https://github.com/acme/widget/pull/17",
		Head: "topic", Base: "main", ObservedAt: time.Unix(1_700_000_100, 0).UTC(),
	}
}

func TestRefreshRemotePlanIsLocalAndNotPersistedTransition(t *testing.T) {
	for _, managed := range []bool{true, false} {
		name := "unmanaged"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			h := newRemoteTestHarness(t)
			plan, err := h.service.Plan(t.Context(), h.request(t, managed, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if calls := h.networkCalls(); len(calls) != 0 {
				t.Fatalf("Plan made network calls: %v", calls)
			}
			if plan.HasPersistedTransition || plan.StatePreserving || plan.Target != "" || plan.RemovesTask {
				t.Fatalf("remote refresh represented as task transition: %+v", plan)
			}
			if _, found := LookupTransition(plan.Locator.Mode, plan.Locator.State, RefreshRemote); found {
				t.Fatal("RefreshRemote still appears in exhaustive transition table")
			}
			if got := effectCodes(plan); !reflect.DeepEqual(got, []EffectCode{EffectFetchRefs, EffectQueryReview}) {
				t.Fatalf("effects=%v", got)
			}
			for _, effect := range plan.Effects() {
				if !effect.Network || effect.Code == EffectUpdateTask {
					t.Errorf("unexpected remote effect: %+v", effect)
				}
			}
			if plan.Availability != AvailabilityReady {
				t.Fatalf("availability=%s conditions=%+v", plan.Availability, plan.Conditions())
			}
			authority := plan.AuthorityFields()
			for key, want := range map[string]string{
				"remote.name": "origin", "remote.url": h.remoteURL,
				"remote.provider": string(forge.GitHub), "remote.repository": "acme/widget",
				"remote.head": "topic", "remote.base": "main",
			} {
				if authority[key] != want {
					t.Errorf("authority[%s]=%q want %q", key, authority[key], want)
				}
			}
			if managed && authority["remote.task-revision"] == "" {
				t.Fatal("managed plan omitted task revision authority")
			}
			if !managed && authority["remote.task-revision"] != "" {
				t.Fatal("unmanaged plan acquired task authority")
			}
		})
	}
}

func TestRefreshRemoteExplicitRemoteAndNormalizedLocator(t *testing.T) {
	h := newRemoteTestHarness(t)
	h.refs["refs/remotes/upstream/topic"] = remoteTestRemoteHead
	h.refs["refs/remotes/upstream/main"] = remoteTestRemoteBase
	locator := h.locator(t, false)
	locator.Remote = "upstream"
	request := mustRequest(t, locator, RefreshRemoteOptions{FetchRefs: true})
	plan, err := h.service.Plan(t.Context(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.AuthorityFields()["remote.name"] != "upstream" || plan.Effects()[0].Details.Map()["remote"] != "upstream" {
		t.Fatalf("explicit remote was not preserved: authority=%v effect=%v", plan.AuthorityFields(), plan.Effects()[0].Details.Map())
	}
	if got := h.remoteNames[len(h.remoteNames)-1]; got != "upstream" {
		t.Fatalf("configured URL read for %q", got)
	}

	invalid := []struct {
		name   string
		mutate func(*Locator)
	}{
		{"missing unmanaged head", func(locator *Locator) { locator.HeadOID = "" }},
		{"short head OID", func(locator *Locator) { locator.HeadOID = "abc123" }},
		{"full head prefix", func(locator *Locator) { locator.Branch = "refs/heads/topic" }},
		{"whitespace base", func(locator *Locator) { locator.Base = " main" }},
		{"invalid Git branch", func(locator *Locator) { locator.Branch = "bad..branch" }},
		{"option-like remote", func(locator *Locator) { locator.Remote = "--upload-pack=bad" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			locator := h.locator(t, false)
			test.mutate(&locator)
			request := mustRequest(t, locator, RefreshRemoteOptions{FetchRefs: true})
			if _, err := h.service.Plan(t.Context(), request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Plan error=%v, want ErrInvalidRequest", err)
			}
		})
	}

	for _, mutate := range []func(*Locator){
		func(locator *Locator) { locator.Mode = task.ModeBranch },
		func(locator *Locator) { locator.State = task.Warm },
	} {
		locator := h.locator(t, true)
		mutate(&locator)
		request := mustRequest(t, locator, RefreshRemoteOptions{FetchRefs: true})
		if _, err := h.service.Plan(t.Context(), request); !errors.Is(err, ErrStalePlan) {
			t.Fatalf("managed exact mode/state error=%v, want ErrStalePlan", err)
		}
	}
}

func TestRefreshRemoteProviderAndInputConditions(t *testing.T) {
	tests := []struct {
		name         string
		configure    func(*remoteTestHarness, *Locator)
		options      RefreshRemoteOptions
		code         ConditionCode
		verdict      Verdict
		requirement  Requirement
		availability Availability
	}{
		{
			name: "missing remote URL", options: RefreshRemoteOptions{QueryReview: true},
			configure: func(h *remoteTestHarness, _ *Locator) { h.remoteURL = "" },
			code:      ConditionRemoteURL, verdict: VerdictBlocked, requirement: RequirementRequired, availability: AvailabilityBlocked,
		},
		{
			name: "unknown provider", options: RefreshRemoteOptions{QueryReview: true},
			configure: func(h *remoteTestHarness, _ *Locator) { h.remoteURL = "ssh://git@gitea.example/acme/widget.git" },
			code:      ConditionReviewProvider, verdict: VerdictUnsupported, requirement: RequirementRequired, availability: AvailabilityUnsupported,
		},
		{
			name: "unsupported review capability", options: RefreshRemoteOptions{QueryReview: true},
			configure: func(h *remoteTestHarness, _ *Locator) { h.provider = remoteNoReviewForge{available: true} },
			code:      ConditionReviewCapability, verdict: VerdictUnsupported, requirement: RequirementRequired, availability: AvailabilityUnsupported,
		},
		{
			name: "missing provider CLI", options: RefreshRemoteOptions{QueryReview: true},
			configure: func(h *remoteTestHarness, _ *Locator) {
				h.provider = &remoteCapableForge{kind: forge.GitHub, available: false}
			},
			code: ConditionReviewCLI, verdict: VerdictUnsupported, requirement: RequirementRequired, availability: AvailabilityUnsupported,
		},
		{
			name: "missing review base", options: RefreshRemoteOptions{QueryReview: true},
			configure: func(_ *remoteTestHarness, locator *Locator) { locator.Base, locator.BaseOID = "", "" },
			code:      ConditionReviewBase, verdict: VerdictNeedsInput, requirement: RequirementRequired, availability: AvailabilityNeedsInput,
		},
		{
			name: "fetch only ignores unknown forge", options: RefreshRemoteOptions{FetchRefs: true},
			configure: func(h *remoteTestHarness, _ *Locator) {
				h.remoteURL = "ssh://git@gitea.example/acme/widget.git"
				h.provider = nil
			},
			code: ConditionReviewProvider, verdict: VerdictUnsupported, requirement: RequirementAdvisory, availability: AvailabilityReady,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newRemoteTestHarness(t)
			locator := h.locator(t, false)
			test.configure(h, &locator)
			request := mustRequest(t, locator, test.options)
			plan, err := h.service.Plan(t.Context(), request)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			got, found := conditionByCode(plan, test.code)
			if !found || got.Verdict != test.verdict || got.Requirement != test.requirement {
				t.Fatalf("condition %s=%+v found=%t", test.code, got, found)
			}
			if plan.Availability != test.availability {
				t.Fatalf("availability=%s want=%s conditions=%+v", plan.Availability, test.availability, plan.Conditions())
			}
			if calls := h.networkCalls(); len(calls) != 0 {
				t.Fatalf("condition planning made network calls: %v", calls)
			}
		})
	}
}

func TestRefreshRemoteApplyOrderingManagedAndUnmanagedWithoutTaskWrites(t *testing.T) {
	tests := []struct {
		name    string
		managed bool
		options RefreshRemoteOptions
		steps   []EffectCode
		calls   []string
	}{
		{"managed fetch only", true, RefreshRemoteOptions{FetchRefs: true}, []EffectCode{EffectFetchRefs}, []string{"fetch:--prune -- origin"}},
		{"managed query only", true, RefreshRemoteOptions{QueryReview: true}, []EffectCode{EffectQueryReview}, []string{"query:acme/widget:topic:main"}},
		{"managed both", true, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}, []EffectCode{EffectFetchRefs, EffectQueryReview}, []string{"fetch:--prune -- origin", "query:acme/widget:topic:main"}},
		{"unmanaged both", false, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}, []EffectCode{EffectFetchRefs, EffectQueryReview}, []string{"fetch:--prune -- origin", "query:acme/widget:topic:main"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newRemoteTestHarness(t)
			h.queryReview = h.review(forge.ReviewOpen, false)
			before, err := h.tasks.GetRecord(h.record.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := h.service.Plan(t.Context(), h.request(t, test.managed, test.options))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if len(h.networkCalls()) != 0 {
				t.Fatal("Plan invoked a network effect")
			}
			h.resetCalls()
			result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
			if err != nil {
				t.Fatalf("Apply: %v steps=%+v", err, result.AttemptedSteps())
			}
			if got := stepCodes(result); !reflect.DeepEqual(got, test.steps) {
				t.Fatalf("steps=%v want=%v", got, test.steps)
			}
			if got := h.networkCalls(); !reflect.DeepEqual(got, test.calls) {
				t.Fatalf("network order=%v want=%v", got, test.calls)
			}
			wantCommon, _ := pathx.Canonical(h.common)
			if len(h.calls) == 0 || h.calls[0] != "repo-lock:"+wantCommon {
				t.Fatalf("repository lock order=%v", h.calls)
			}
			if h.taskWriteCalls != 0 {
				t.Fatalf("task write calls=%d", h.taskWriteCalls)
			}
			after, err := h.tasks.GetRecord(h.record.Task.ID)
			if err != nil || after.Revision != before.Revision {
				t.Fatalf("task record changed: before=%s after=%v err=%v", before.Revision, after, err)
			}
			data, ok := result.RemoteObservation()
			if !ok || data.RemoteName != "origin" || data.Head != "topic" || data.Base != "main" {
				t.Fatalf("remote data=%+v present=%t", data, ok)
			}
			if test.options.FetchRefs && (!data.HasAfterRefs || data.AfterRefs.ObservedAt.IsZero()) {
				t.Fatalf("fetch result omitted post-fetch refs: %+v", data)
			}
			if !test.options.FetchRefs && data.HasAfterRefs {
				t.Fatalf("query-only result has post-fetch refs: %+v", data)
			}
			if result.PartialSuccess {
				t.Fatal("successful remote refresh marked partial")
			}
		})
	}
}

func TestRefreshRemoteUnmanagedApplyDoesNotTakeTaskStoreLock(t *testing.T) {
	h := newRemoteTestHarness(t)
	h.queryReview = h.review(forge.ReviewOpen, false)
	plan, err := h.service.Plan(t.Context(), h.request(t, false, RefreshRemoteOptions{QueryReview: true}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var result Result
	err = h.tasks.WithLock(ctx, func(*task.Tx) error {
		var applyErr error
		result, applyErr = h.service.Apply(ctx, plan, Approve(plan.PlanID))
		return applyErr
	})
	if err != nil {
		t.Fatalf("unmanaged Apply tried to reacquire task lock or failed: %v", err)
	}
	if got := stepCodes(result); !reflect.DeepEqual(got, []EffectCode{EffectQueryReview}) {
		t.Fatalf("steps=%v", got)
	}
}

func TestRefreshRemoteStaleAuthorityStopsBeforeNetwork(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *remoteTestHarness)
	}{
		{
			name: "task revision",
			mutate: func(t *testing.T, h *remoteTestHarness) {
				record, _ := h.tasks.GetRecord(h.record.Task.ID)
				candidate := record.Task
				candidate.Next = "changed"
				if _, err := h.tasks.Update(t.Context(), &candidate, record.Revision); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "repository identity",
			mutate: func(t *testing.T, h *remoteTestHarness) {
				h.discoverAt = filepath.Join(filepath.Dir(h.repo), "different-main")
				if err := os.MkdirAll(h.discoverAt, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "remote URL",
			mutate: func(_ *testing.T, h *remoteTestHarness) {
				h.remoteURL = "https://github.com/acme/other.git"
			},
		},
		{
			name: "remote tracking ref",
			mutate: func(_ *testing.T, h *remoteTestHarness) {
				h.refs["refs/remotes/origin/topic"] = "5555555555555555555555555555555555555555"
			},
		},
		{
			name: "provider availability",
			mutate: func(_ *testing.T, h *remoteTestHarness) {
				h.provider.(*remoteCapableForge).available = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newRemoteTestHarness(t)
			h.queryReview = h.review(forge.ReviewOpen, false)
			plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			h.resetCalls()
			test.mutate(t, h)
			result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
			if !errors.Is(err, ErrStalePlan) {
				t.Fatalf("Apply error=%v, want ErrStalePlan", err)
			}
			if calls := h.networkCalls(); len(calls) != 0 {
				t.Fatalf("stale apply made network calls: %v", calls)
			}
			if len(result.AttemptedSteps()) != 0 {
				t.Fatalf("stale apply has steps: %+v", result.AttemptedSteps())
			}
		})
	}
}

func TestRefreshRemoteFetchRevalidatesLocalRefsBeforeReviewQuery(t *testing.T) {
	h := newRemoteTestHarness(t)
	h.queryReview = h.review(forge.ReviewOpen, false)
	h.fetchMutation = func() {
		h.refs["refs/heads/main"] = "9999999999999999999999999999999999999999"
	}
	plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}))
	if err != nil {
		t.Fatal(err)
	}
	h.resetCalls()
	result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Apply error=%v steps=%+v, want ErrStalePlan", err, result.AttemptedSteps())
	}
	if calls := h.networkCalls(); !reflect.DeepEqual(calls, []string{"fetch:--prune -- origin"}) {
		t.Fatalf("network calls=%v, want fetch without query", calls)
	}
	if steps := result.AttemptedSteps(); len(steps) != 2 || steps[0].Effect.Code != EffectFetchRefs ||
		steps[0].Status != StepCompleted || steps[1].Effect.Code != EffectQueryReview || steps[1].Status != StepFailed {
		t.Fatalf("steps=%+v", steps)
	}
}

func TestRefreshRemoteReviewDataStatesAndKnownAbsence(t *testing.T) {
	states := []struct {
		name  string
		state forge.ReviewState
		draft bool
	}{
		{"open", forge.ReviewOpen, false},
		{"draft", forge.ReviewDraft, true},
		{"merged", forge.ReviewMerged, false},
		{"closed", forge.ReviewClosed, false},
	}
	for _, test := range states {
		t.Run(test.name, func(t *testing.T) {
			h := newRemoteTestHarness(t)
			h.queryReview = h.review(test.state, test.draft)
			plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{QueryReview: true}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
			if err != nil {
				t.Fatal(err)
			}
			data, ok := result.RemoteObservation()
			if !ok || !data.HasReview || data.Review.State != ObservationKnown || !data.Review.Exists {
				t.Fatalf("review data=%+v present=%t", data, ok)
			}
			if data.Review.Provider != forge.GitHub || data.Review.ReviewState != test.state ||
				data.Review.Draft != test.draft || data.Review.URL != "https://github.com/acme/widget/pull/17" ||
				data.Review.ObservedAt.IsZero() {
				t.Fatalf("review fields=%+v", data.Review)
			}
			if _, handoff := result.Handoff(); handoff {
				t.Fatal("review URL became an automatic handoff")
			}
		})
	}

	t.Run("known absence", func(t *testing.T) {
		h := newRemoteTestHarness(t)
		plan, err := h.service.Plan(t.Context(), h.request(t, false, RefreshRemoteOptions{QueryReview: true}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
		if err != nil {
			t.Fatal(err)
		}
		data, ok := result.RemoteObservation()
		if !ok || !data.HasReview || data.Review.State != ObservationKnown || data.Review.Exists || data.Review.ObservedAt.IsZero() {
			t.Fatalf("known absence=%+v present=%t", data, ok)
		}
	})
}

func TestRefreshRemoteReviewErrorsAreRetained(t *testing.T) {
	authErr := errors.New("provider authentication or version error")
	tests := []struct {
		name string
		err  error
		kind RemoteReviewFailureKind
	}{
		{
			name: "ambiguity",
			err: &forge.AmbiguousReviewError{
				Provider: forge.GitHub,
				Query:    forge.ReviewQuery{Repository: "acme/widget", Head: "topic", Base: "main"},
				Matches:  []forge.Review{{ID: "one"}, {ID: "two"}},
			},
			kind: RemoteReviewFailureAmbiguous,
		},
		{name: "authentication or version", err: authErr, kind: RemoteReviewFailureProvider},
		{name: "malformed", err: &forge.MalformedReviewResponseError{Provider: forge.GitHub, Index: 0, Field: "state"}, kind: RemoteReviewFailureMalformed},
		{name: "unsupported", err: &forge.ErrUnsupported{Kind: forge.GitHub, Operation: "review query version"}, kind: RemoteReviewFailureUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newRemoteTestHarness(t)
			h.queryErr = test.err
			plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{QueryReview: true}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
			if err == nil {
				t.Fatal("Apply unexpectedly succeeded")
			}
			steps := result.AttemptedSteps()
			if len(steps) != 1 || steps[0].Effect.Code != EffectQueryReview || steps[0].Status != StepFailed {
				t.Fatalf("steps=%+v", steps)
			}
			data, ok := result.RemoteObservation()
			if !ok || !data.HasReview || data.Review.State != ObservationError || data.Review.Exists ||
				data.Review.FailureKind != test.kind || data.Review.Failure == "" {
				t.Fatalf("error data=%+v present=%t", data, ok)
			}
		})
	}

	t.Run("defensive malformed provider result", func(t *testing.T) {
		h := newRemoteTestHarness(t)
		h.queryReview = h.review(forge.ReviewOpen, false)
		h.queryReview.Head = "wrong-head"
		plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{QueryReview: true}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
		if !errors.Is(err, forge.ErrMalformedReviewResponse) {
			t.Fatalf("Apply error=%v", err)
		}
		data, _ := result.RemoteObservation()
		if data.Review.FailureKind != RemoteReviewFailureMalformed || data.Review.State != ObservationError {
			t.Fatalf("malformed data=%+v", data.Review)
		}
	})
}

func TestRefreshRemotePartialAndFetchFailureOrdering(t *testing.T) {
	t.Run("fetch success then query failure", func(t *testing.T) {
		h := newRemoteTestHarness(t)
		h.queryErr = errors.New("authentication failed")
		plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}))
		if err != nil {
			t.Fatal(err)
		}
		h.resetCalls()
		result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
		if err == nil {
			t.Fatal("Apply unexpectedly succeeded")
		}
		steps := result.AttemptedSteps()
		if len(steps) != 2 || steps[0].Status != StepCompleted || steps[1].Status != StepFailed ||
			!result.PartialSuccess || len(result.Recovery()) == 0 {
			t.Fatalf("partial result=%+v partial=%t recovery=%v", steps, result.PartialSuccess, result.Recovery())
		}
		data, _ := result.RemoteObservation()
		if !data.HasAfterRefs || !data.HasReview || data.Review.State != ObservationError {
			t.Fatalf("partial remote data=%+v", data)
		}
		if got := h.networkCalls(); !reflect.DeepEqual(got, []string{"fetch:--prune -- origin", "query:acme/widget:topic:main"}) {
			t.Fatalf("network order=%v", got)
		}
	})

	t.Run("fetch failure short circuits query", func(t *testing.T) {
		h := newRemoteTestHarness(t)
		h.fetchErr = errors.New("offline")
		h.queryReview = h.review(forge.ReviewOpen, false)
		plan, err := h.service.Plan(t.Context(), h.request(t, true, RefreshRemoteOptions{FetchRefs: true, QueryReview: true}))
		if err != nil {
			t.Fatal(err)
		}
		h.resetCalls()
		result, err := h.service.Apply(t.Context(), plan, Approve(plan.PlanID))
		if err == nil {
			t.Fatal("Apply unexpectedly succeeded")
		}
		steps := result.AttemptedSteps()
		if len(steps) != 1 || steps[0].Effect.Code != EffectFetchRefs || steps[0].Status != StepFailed {
			t.Fatalf("steps=%+v", steps)
		}
		if got := h.networkCalls(); !reflect.DeepEqual(got, []string{"fetch:--prune -- origin"}) {
			t.Fatalf("query ran after failed fetch: %v", got)
		}
		data, _ := result.RemoteObservation()
		if data.HasAfterRefs || data.HasReview {
			t.Fatalf("failed fetch fabricated observations: %+v", data)
		}
		if len(result.Recovery()) < 2 {
			t.Fatalf("recovery=%v", result.Recovery())
		}
	})
}

func TestRefreshRemotePlanIdentityExcludesTimeAndIncludesAuthority(t *testing.T) {
	h := newRemoteTestHarness(t)
	request := h.request(t, true, RefreshRemoteOptions{FetchRefs: true, QueryReview: true})
	first, err := h.service.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(24 * time.Hour)
	second, err := h.service.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID != second.PlanID || first.AuthorityFingerprint != second.AuthorityFingerprint {
		t.Fatalf("observation timestamps changed identity: %s != %s", first.PlanID, second.PlanID)
	}

	h.remoteURL = "https://github.com/acme/other.git"
	changedURL, err := h.service.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if changedURL.PlanID == first.PlanID {
		t.Fatal("remote URL/repository identity did not change PlanID")
	}

	h.remoteURL = "https://github.com/acme/widget.git"
	h.refs["refs/remotes/origin/topic"] = "5555555555555555555555555555555555555555"
	changedRef, err := h.service.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if changedRef.PlanID == first.PlanID {
		t.Fatal("pre-action ref authority did not change PlanID")
	}
}

func TestRemoteReviewObservationContainsOnlyPortableEvidence(t *testing.T) {
	typeOf := reflect.TypeOf(RemoteReviewObservation{})
	got := make([]string, 0, typeOf.NumField())
	for index := 0; index < typeOf.NumField(); index++ {
		got = append(got, typeOf.Field(index).Name)
	}
	want := []string{"State", "Exists", "Provider", "ReviewState", "Draft", "URL", "ObservedAt", "FailureKind", "Failure"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("review evidence fields=%v want=%v", got, want)
	}
}

func TestResultDefensivelyCopiesRemoteObservation(t *testing.T) {
	remote := &RemoteObservation{
		RemoteName: "origin", RemoteURL: "https://github.com/acme/widget.git",
		Provider: forge.GitHub, Repository: "acme/widget", Head: "topic", Base: "main",
		BeforeRefs: RemoteRefsObservation{
			LocalHead:  NamedRefObservation{Ref: "refs/heads/topic", Exists: true, OID: remoteTestHead},
			ObservedAt: time.Unix(1, 0),
		},
		Review: RemoteReviewObservation{
			State: ObservationKnown, Exists: true, Provider: forge.GitHub,
			ReviewState: forge.ReviewOpen, URL: "https://github.com/acme/widget/pull/17",
			ObservedAt: time.Unix(2, 0),
		},
		HasReview: true,
	}
	result := NewResult(ResultSpec{Remote: remote})
	remote.RemoteURL = "changed"
	remote.BeforeRefs.LocalHead.OID = "changed"
	remote.Review.URL = "changed"

	got, ok := result.RemoteObservation()
	if !ok || got.RemoteURL != "https://github.com/acme/widget.git" || got.BeforeRefs.LocalHead.OID != remoteTestHead ||
		got.Review.URL != "https://github.com/acme/widget/pull/17" {
		t.Fatalf("result changed through input pointer: %+v present=%t", got, ok)
	}
	got.RemoteURL = "accessor-change"
	got.BeforeRefs.LocalHead.OID = "accessor-change"
	got.Review.URL = "accessor-change"
	again, _ := result.RemoteObservation()
	if again.RemoteURL != "https://github.com/acme/widget.git" || again.BeforeRefs.LocalHead.OID != remoteTestHead ||
		again.Review.URL != "https://github.com/acme/widget/pull/17" {
		t.Fatalf("result changed through accessor value: %+v", again)
	}
	clone := result.Clone()
	cloneData, _ := clone.RemoteObservation()
	cloneData.Repository = "clone-change"
	original, _ := result.RemoteObservation()
	if original.Repository != "acme/widget" {
		t.Fatalf("result changed through clone: %+v", original)
	}
}
