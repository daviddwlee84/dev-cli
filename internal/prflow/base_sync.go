package prflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type BaseSyncConfig struct {
	Tasks    *task.Store
	Host     string
	Runtimes func() []runtime.Runtime
	Resolver DetailResolver
	Run      taskflow.GitRunFunc
}
type BaseSyncService struct {
	cfg     BaseSyncConfig
	service *taskflow.Service
}

func NewBaseSyncService(cfg BaseSyncConfig) (*BaseSyncService, error) {
	service, err := taskflow.NewSyncService(taskflow.SyncConfig{Tasks: cfg.Tasks, Host: cfg.Host, Runtimes: cfg.Runtimes, Run: cfg.Run})
	if err != nil {
		return nil, err
	}
	return &BaseSyncService{cfg: cfg, service: service}, nil
}

type BaseSyncRequest struct {
	Reference Reference `json:"reference"`
	RepoPath  string    `json:"repo_path"`
	Mode      string    `json:"mode"`
}
type BaseSyncPlan struct {
	Request   BaseSyncRequest `json:"request"`
	Detail    Detail          `json:"detail"`
	Plan      taskflow.Plan   `json:"plan"`
	RepoPath  string          `json:"repo_path"`
	Path      string          `json:"path,omitempty"`
	Branch    string          `json:"branch"`
	Remote    string          `json:"remote"`
	OldOID    string          `json:"old_oid"`
	NewOID    string          `json:"new_oid"`
	Operation string          `json:"operation"`
	NoOp      bool            `json:"no_op"`
	Reason    string          `json:"reason,omitempty"`
	seal      string
}
type BaseSyncResult struct {
	Operation      string                `json:"operation"`
	Path           string                `json:"path"`
	OldOID         string                `json:"old_oid"`
	NewOID         string                `json:"new_oid,omitempty"`
	TargetOID      string                `json:"target_oid,omitempty"`
	NoOp           bool                  `json:"no_op"`
	PartialSuccess bool                  `json:"partial_success"`
	Steps          []taskflow.StepResult `json:"steps"`
	Recovery       []string              `json:"recovery,omitempty"`
}

func (p BaseSyncPlan) digest() string {
	return checkoutDigest([]any{p.Request, checkoutDetailIdentity(p.Detail), p.Detail.State, p.Detail.MergeOID, p.Plan.PlanID, p.RepoPath, p.Path, p.Branch, p.Remote, p.OldOID, p.NewOID, p.Operation, p.NoOp, p.Reason})
}
func (s *BaseSyncService) PlanFetch(ctx context.Context, r BaseSyncRequest) (BaseSyncPlan, error) {
	return s.plan(ctx, r, true)
}
func (s *BaseSyncService) Plan(ctx context.Context, r BaseSyncRequest) (BaseSyncPlan, error) {
	return s.plan(ctx, r, false)
}

func (s *BaseSyncService) plan(ctx context.Context, r BaseSyncRequest, fetch bool) (BaseSyncPlan, error) {
	var p BaseSyncPlan
	if s.cfg.Resolver == nil {
		return p, errors.New("base sync requires a detail resolver")
	}
	if r.Mode == "" {
		r.Mode = "ff-only"
	}
	if r.Mode != "ff-only" && r.Mode != "rebase" {
		return p, errors.New("base sync mode must be ff-only or rebase")
	}
	d, err := s.cfg.Resolver.Detail(ctx, r.Reference)
	if err != nil {
		return p, err
	}
	if err = validateCheckoutDetail(d, r.Reference); err != nil {
		return p, err
	}
	if d.State != forge.PRStateMerged || d.MergeOID == "" {
		return p, errors.New("base synchronization requires a confirmed merged PR and merge commit")
	}
	g, err := gitx.Discover(ctx, r.RepoPath)
	if err != nil {
		return p, err
	}
	common, err := pathx.Canonical(g.GitCommonDir)
	if err != nil {
		return p, err
	}
	branches, err := gitx.BranchStates(ctx, g.MainRoot)
	if err != nil {
		return p, err
	}
	var branch gitx.BranchState
	for _, b := range branches {
		if b.Ref == "refs/heads/"+d.BaseBranch {
			branch = b
		}
	}
	if branch.OID == "" {
		return p, fmt.Errorf("local base branch %s does not exist", d.BaseBranch)
	}
	if branch.Remote == "" || branch.Remote == "." || branch.RemoteRef != "refs/heads/"+d.BaseBranch {
		return p, errors.New("local base must track the PR's exact base branch on a named remote")
	}
	top, err := gitx.RecoveryTopologyOf(ctx, g.MainRoot)
	if err != nil {
		return p, err
	}
	endpoint := ""
	for _, remote := range top.Remotes {
		if remote.Name == branch.Remote {
			if len(remote.FetchURLs) != 1 {
				return p, errors.New("base remote must have one exact fetch endpoint")
			}
			endpoint = remote.FetchURLs[0]
		}
	}
	if !samePRRepository(forge.ParseRemoteIdentity(endpoint), r.Reference.Forge, r.Reference.Host, r.Reference.Repo) {
		return p, errors.New("base tracking endpoint does not identify the PR repository")
	}
	p = BaseSyncPlan{Request: r, Detail: d, RepoPath: g.MainRoot, Branch: d.BaseBranch, Remote: branch.Remote, OldOID: branch.OID, NewOID: branch.UpstreamOID, Operation: "fast-forward"}
	l := taskflow.Locator{RepoPath: g.MainRoot, GitCommonDir: common, RepositoryID: common, RowKey: common, RowKind: "repository", Branch: d.BaseBranch, HeadOID: branch.OID, Remote: branch.Remote, Upstream: branch.Upstream, UpstreamOID: branch.UpstreamOID}
	action := taskflow.FastForwardBranch
	if fetch {
		action = taskflow.FetchRepository
		p.Operation = "fetch"
		l.UpstreamOID = ""
	} else {
		if !branch.ComparisonKnown {
			return p, errors.New("base comparison unavailable; fetch the exact base remote first")
		}
		if _, err := gitx.Run(ctx, g.MainRoot, "merge-base", "--is-ancestor", d.MergeOID, branch.UpstreamOID); err != nil {
			return p, errors.New("fetched base does not contain the confirmed PR merge; fetch and verify before synchronization")
		}
		worktrees, err := gitx.Worktrees(ctx, g.MainRoot)
		if err != nil {
			return p, err
		}
		for _, w := range worktrees {
			if w.Branch == d.BaseBranch {
				if p.Path != "" {
					return p, errors.New("base branch has multiple registered checkouts")
				}
				p.Path = w.Path
			}
		}
		if p.Path == "" {
			return p, errors.New("base branch is not checked out; no checkout is switched implicitly")
		}
		l.CheckoutPath, l.RowKey, l.RowKind = p.Path, p.Path, "checkout"
		if branch.Behind == 0 {
			p.NoOp = true
			p.Reason = "local base already contains the fetched upstream; local commits retained"
			p.seal = p.digest()
			return p, nil
		}
		if branch.Ahead > 0 {
			if r.Mode != "rebase" {
				return p, errors.New("base has local commits; explicitly choose rebase after reviewing them")
			}
			action = taskflow.RebaseBranch
			p.Operation = "rebase"
		}
	}
	req, err := taskflow.NewRequest(l, taskflow.SyncOptions{Operation: action, Endpoint: endpoint, RemoteRef: branch.RemoteRef})
	if err != nil {
		return p, err
	}
	p.Plan, err = s.service.Plan(ctx, req)
	if err != nil {
		return p, err
	}
	p.seal = p.digest()
	return p, nil
}

// ApplyFetch and Apply each own one taskflow transaction; callers must not
// wrap them in a repository lock. Fetch always precedes a fresh local plan.
func (s *BaseSyncService) ApplyFetch(ctx context.Context, p BaseSyncPlan) (BaseSyncResult, error) {
	if p.Operation != "fetch" {
		return BaseSyncResult{}, errors.New("not a fetch plan")
	}
	return s.Apply(ctx, p)
}
func (s *BaseSyncService) Apply(ctx context.Context, p BaseSyncPlan) (BaseSyncResult, error) {
	result := BaseSyncResult{Operation: p.Operation, Path: p.Path, OldOID: p.OldOID, TargetOID: p.NewOID, NoOp: p.NoOp, Steps: []taskflow.StepResult{}}
	if p.seal == "" || p.seal != p.digest() {
		return result, errors.New("base sync plan changed; preview again")
	}
	if p.NoOp {
		result.NewOID = p.OldOID
		fresh, err := s.plan(ctx, p.Request, false)
		if err != nil {
			return result, err
		}
		if fresh.digest() != p.digest() {
			return result, errors.New("base changed after no-op preview")
		}
		return result, nil
	}
	// PR metadata may change after merge/fetch; provider/base repository and
	// base branch must remain the reviewed authority. Head/check state is not
	// permission to rewrite the selected local checkout.
	d, err := s.cfg.Resolver.Detail(ctx, p.Request.Reference)
	if err != nil {
		return result, err
	}
	if d.State != forge.PRStateMerged || d.MergeOID == "" || d.MergeOID != p.Detail.MergeOID {
		return result, errors.New("confirmed merge identity changed; refresh before synchronization")
	}
	if d.Reference != p.Detail.Reference || d.AccountID != p.Detail.AccountID || d.RepositoryID != p.Detail.RepositoryID || d.BaseURL != p.Detail.BaseURL || d.BaseBranch != p.Detail.BaseBranch {
		return result, errors.New("PR base identity changed; preview synchronization again")
	}
	applied, err := s.service.Apply(ctx, p.Plan, taskflow.Approve(p.Plan.PlanID))
	result.Steps = applied.AttemptedSteps()
	result.Recovery = applied.Recovery()
	result.PartialSuccess = applied.PartialSuccess
	if p.Operation == "fetch" && err == nil {
		observed, e := gitx.Run(ctx, p.RepoPath, "rev-parse", "--verify", "refs/remotes/"+p.Remote+"/"+p.Branch+"^{commit}")
		if e != nil {
			return result, e
		}
		result.NewOID = observed
	}
	if p.Operation != "fetch" && err == nil {
		head, e := gitx.Run(ctx, p.Path, "rev-parse", "HEAD")
		if e != nil {
			return result, e
		}
		result.NewOID = strings.TrimSpace(head)
	}
	return result, err
}
