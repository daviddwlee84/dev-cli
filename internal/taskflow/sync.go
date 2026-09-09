package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// SyncConfig supplies host-local authority; synchronization never writes task
// intent. Runtimes must include every backend observed by the caller.
type SyncConfig struct {
	Tasks    *task.Store
	Host     string
	Runtimes func() []runtime.Runtime
	Run      GitRunFunc
}

type syncObservation struct {
	spec     PlanSpec
	endpoint string
	branch   gitx.BranchState
}

func NewSyncService(cfg SyncConfig) (*Service, error) {
	if cfg.Tasks == nil {
		return nil, errors.New("sync requires task inventory")
	}
	if cfg.Run == nil {
		cfg.Run = gitx.RunUnattended
	}
	observe := func(ctx context.Context, req Request, load taskInventoryLoader) (syncObservation, error) {
		var o syncObservation
		l := req.Locator
		if l.RepoPath == "" || l.GitCommonDir == "" || l.Remote == "" || strings.HasPrefix(l.Remote, "-") || strings.ContainsAny(l.Remote, "\x00\n\r:") {
			return o, errors.New("sync needs an exact repository and named remote")
		}
		r, err := gitx.Discover(ctx, l.RepoPath)
		if err != nil {
			return o, err
		}
		common, err := pathx.Canonical(r.GitCommonDir)
		if err != nil {
			return o, err
		}
		if common != l.GitCommonDir {
			return o, &StalePlanError{Reason: "repository identity changed"}
		}
		top, err := gitx.RecoveryTopologyOf(ctx, l.RepoPath)
		if err != nil {
			return o, err
		}
		branches, err := gitx.BranchStates(ctx, l.RepoPath)
		if err != nil {
			return o, err
		}
		conditions := []Condition{}
		check := func(code ConditionCode, ok bool, detail string) {
			v := VerdictMet
			if !ok {
				v = VerdictBlocked
			}
			conditions = append(conditions, condition(code, v, RequirementRequired, detail, "refresh or handle this item individually"))
		}
		var remote *gitx.RemoteInfo
		for i := range top.Remotes {
			if top.Remotes[i].Name == l.Remote {
				remote = &top.Remotes[i]
			}
		}
		if remote == nil {
			return o, &StalePlanError{Reason: "selected remote disappeared"}
		}
		urls := remote.FetchURLs
		if req.Action == PushBranch {
			urls = remote.PushURLs
		}
		check(ConditionRemoteURL, len(urls) == 1, "one exact remote endpoint is required")
		if len(urls) == 1 {
			o.endpoint = urls[0]
		}
		if req.Action != FetchRepository {
			for _, b := range branches {
				if b.Ref == "refs/heads/"+l.Branch {
					o.branch = b
				}
			}
			if o.branch.OID == "" || o.branch.OID != l.HeadOID {
				return o, &StalePlanError{Reason: "selected branch commit changed"}
			}
			b := o.branch
			check(ConditionBranchPublished, b.ComparisonKnown && b.Remote == l.Remote && b.Remote != "." && strings.HasPrefix(b.RemoteRef, "refs/heads/"), "an available upstream on the selected remote is required")
			if req.Action == PushBranch {
				check(ConditionBranchRelation, b.Ahead > 0 && b.Behind == 0, "push only a branch strictly ahead of its cached upstream")
				check(ConditionTargetBranch, b.PushRemote == b.Remote && b.PushRef == b.RemoteRef && len(remote.FetchURLs) == 1 && remote.FetchURLs[0] == o.endpoint, "triangular or multiple push destinations require individual review")
			} else {
				check(ConditionBranchRelation, b.Behind > 0 && b.Ahead == 0, "fast-forward only a strictly behind branch")
			}
		}
		authority := map[string]string{"common": common, "remote": syncDigest(remote), "branch": syncDigest(o.branch)}
		if req.Action != FetchRepository {
			records, diagnostics, e := load()
			if e != nil {
				return o, e
			}
			check(ConditionTaskInventory, len(diagnostics) == 0, "task inventory must be complete")
			claims := []task.Record{}
			owned := true
			for _, rec := range records {
				tr, e := gitx.Discover(ctx, rec.Task.RepoPath)
				if e != nil {
					if rec.Task.RepoPath == l.RepoPath {
						owned = false
					}
					continue
				}
				tc, e := pathx.Canonical(tr.GitCommonDir)
				if e != nil {
					if rec.Task.RepoPath == l.RepoPath {
						owned = false
					}
					continue
				}
				if tc == common && rec.Task.Branch == l.Branch {
					claims = append(claims, rec)
					if rec.Task.Owner != "" && rec.Task.Owner != cfg.Host {
						owned = false
					}
				}
			}
			authority["tasks"] = syncDigest(claims)
			check(ConditionOwner, owned, "branch task ownership must be local and observable")
			worktrees, e := gitx.Worktrees(ctx, l.RepoPath)
			if e != nil {
				return o, e
			}
			relevantWorktrees := []gitx.Worktree{}
			for _, w := range worktrees {
				if w.Branch == l.Branch {
					relevantWorktrees = append(relevantWorktrees, w)
				}
			}
			authority["worktrees"] = syncDigest(relevantWorktrees)
			matched := false
			clear := true
			occupied := false
			runtimeKnown := true
			for _, w := range worktrees {
				if w.Branch != l.Branch {
					continue
				}
				matched = true
				op, busy, e := gitx.InProgress(w.Path)
				if e != nil || busy || op != "" {
					clear = false
				}
				if req.Action == FastForwardBranch {
					check(ConditionCheckoutExact, w.Path == l.CheckoutPath && !w.Locked && !w.Prunable && !w.Detached, "fast-forward requires the exact available checkout")
					contents, e := gitx.InspectTriageContents(ctx, w.Path, nil)
					if e != nil {
						return o, e
					}
					authority["contents"] = contents.Fingerprint
					check(ConditionCheckoutClean, !contents.Status.Dirty() && len(contents.Nested) == 0, "fast-forward requires a clean checkout without nested repositories")
				}
				backends := []runtime.Runtime{}
				if cfg.Runtimes != nil {
					backends = cfg.Runtimes()
				}
				if len(backends) == 0 {
					runtimeKnown = false
				}
				for _, rt := range backends {
					occ, e := runtime.InspectOccupancy(ctx, rt, w.Path, runtime.OccupancyOptions{InspectProcesses: true, CallerPaneID: os.Getenv("HERDR_PANE_ID"), CallerWorkspaceID: os.Getenv("HERDR_WORKSPACE_ID")})
					if occupancyObservationError(occ, e) != nil {
						runtimeKnown = false
					}
					// Fetch is independent; writers never publish another live agent's work.
					if req.Action == FastForwardBranch {
						for _, session := range occ.Sessions {
							for _, pane := range session.Panes {
								if pane.Process == nil || (pane.Process.State != runtime.ProcessShell && pane.Process.State != runtime.ProcessCaller) {
									runtimeKnown = false
								}
							}
						}
					}
					if len(occ.Agents) > 0 {
						occupied = true
					}
					authority["runtime:"+rt.Name()+":"+w.Path] = authorityHash("sync-runtime", occupancyAuthority(occ, e), syncDigest(occ.Sessions))
				}
			}
			check(ConditionGitOperation, clear, "no Git operation may be in progress")
			check(ConditionAgentOccupancy, !occupied, "no recognized agent may occupy this branch checkout")
			if req.Action == FastForwardBranch {
				check(ConditionRuntimeAvailable, matched && runtimeKnown, "checkout and runtime coverage must be observed")
			}
		}
		effect := EffectFetchRefs
		detail := "fetch remote-tracking branches (including pruning deleted remote branches), no tags or submodules"
		if req.Action == PushBranch {
			effect = EffectPushBranch
			detail = "push exact commit " + o.branch.OID + " to " + l.Remote + "/" + o.branch.RemoteRef
		}
		if req.Action == FastForwardBranch {
			effect = EffectMergeFF
			detail = "fast-forward exact checkout to " + o.branch.UpstreamOID + "; abort if ignored files would be overwritten"
		}
		if endpoint := catalog.NormalizeRemoteIdentity(o.endpoint); endpoint != "" {
			detail += " · " + endpoint
		}
		o.spec = PlanSpec{Authority: authority, Conditions: conditions, Effects: []Effect{NewEffect(effect, detail, l.RepoPath, false, req.Action != FastForwardBranch, nil)}, Confirmation: Confirmation{Kind: ConfirmationApproval, Prompt: detail}, Summary: string(req.Action) + " " + l.Remote + " " + l.Branch, DisplayedAt: time.Now()}
		return o, nil
	}
	plan := func(ctx context.Context, req Request) (PlanSpec, error) {
		o, e := observe(ctx, req, cfg.Tasks.ListRecords)
		return o.spec, e
	}
	apply := func(ctx context.Context, p Plan) (Result, error) {
		var result Result
		err := lockx.WithDir(ctx, filepath.Join(p.Locator.GitCommonDir, "dev-taskflow"), "taskflow repository", func() error {
			return cfg.Tasks.WithLock(ctx, func(tx *task.Tx) error {
				o, e := observe(ctx, p.Request, tx.ListRecords)
				if e != nil {
					return e
				}
				fresh, e := BuildPlan(p.Request, o.spec)
				if e != nil {
					return e
				}
				if fresh.PlanID != p.PlanID {
					return &StalePlanError{Reason: "sync authority changed; preview again"}
				}
				if fresh.Availability != AvailabilityReady {
					return errors.New("sync is blocked")
				}
				args := []string{}
				switch p.Action {
				case FetchRepository:
					args = []string{"fetch", "--prune", "--no-tags", "--no-recurse-submodules", "--", o.endpoint, "+refs/heads/*:refs/remotes/" + p.Locator.Remote + "/*"}
				case PushBranch:
					args = []string{"push", "--porcelain", "--no-mirror", "--no-follow-tags", "--recurse-submodules=no", "--", o.endpoint, o.branch.OID + ":" + o.branch.RemoteRef}
				case FastForwardBranch:
					args = []string{"-c", "submodule.recurse=false", "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", "--", o.branch.UpstreamOID}
				default:
					return errors.New("invalid synchronization action")
				}
				path := p.Locator.RepoPath
				if p.Action == FastForwardBranch {
					path = p.Locator.CheckoutPath
				}
				step := StepResult{Effect: p.Effects()[0], Status: StepAttempted, StartedAt: time.Now()}
				_, e = cfg.Run(ctx, path, args...)
				step.FinishedAt = time.Now()
				step.Status = StepCompleted
				if e != nil {
					step.Status = StepFailed
					step.Failure = "Git operation failed; refresh and inspect authentication, rejection, or checkout blockers"
					e = errors.New(step.Failure)
				}
				result = NewResult(ResultSpec{Steps: []StepResult{step}})
				return e
			})
		})
		return result, err
	}
	h := Handler{Plan: plan, Apply: apply}
	return NewService(Handlers{FetchRepository: h, PushBranch: h, FastForwardBranch: h}), nil
}

func syncDigest(v any) string { b, _ := json.Marshal(v); return authorityHash("sync-v1", string(b)) }

// NewProtectedLifecycleService installs additional caller-owned checkout
// evidence checks inside the same repository lock and immediately at removal.
// The caller must seal this evidence into its enclosing reviewed batch plan.
func NewProtectedLifecycleService(cfg LifecycleConfig, verify func(context.Context) error, verifyRemoval func(context.Context, string) error) (*Service, error) {
	if verify == nil || verifyRemoval == nil {
		return nil, errors.New("cleanup protection is required")
	}
	cfg.Hooks.RepoLock = func(ctx context.Context, common string, op func() error) error {
		return lockx.WithDir(ctx, filepath.Join(common, "dev-taskflow"), "taskflow repository", func() error {
			if err := verify(ctx); err != nil {
				return err
			}
			return op()
		})
	}
	cfg.Hooks.RemoveWorktree = func(ctx context.Context, repo, path string, force bool) error {
		if force {
			return errors.New("protected cleanup never forces removal")
		}
		if err := verifyRemoval(ctx, path); err != nil {
			return err
		}
		return gitx.RemoveWorktree(ctx, repo, path, false)
	}
	return NewLifecycleService(cfg)
}
