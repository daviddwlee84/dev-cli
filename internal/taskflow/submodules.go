package taskflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

const effectVerifySubmodules EffectCode = "verify-submodule-recovery"

func recursiveRequest(r Request) (bool, bool) {
	switch o := r.Options.(type) {
	case ParkColdOptions:
		return o.Recursive, r.Locator.Mode == task.ModeWorktree
	case RetireOptions:
		return o.Recursive, r.Locator.Mode == task.ModeWorktree
	case RemoveCheckoutOptions:
		return o.Recursive, true
	}
	return false, false
}

func submoduleIntegrationRequired(r Request) bool {
	if o, ok := r.Options.(RemoveCheckoutOptions); ok {
		return o.RequireContained
	}
	return r.Action == Retire || isCompletionAction(r.Action)
}

func (s *lifecycleService) decorateSubmodules(ctx context.Context, r Request, spec PlanSpec, root string) PlanSpec {
	// Missing checkout authority is already blocked by the ordinary conditions.
	if root == "" {
		return spec
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		return spec
	}
	g, err := gitx.SubmodulesOf(ctx, root)
	if err != nil {
		spec.Conditions = append(spec.Conditions, condition("submodules-observed", VerdictError, RequirementRequired, err.Error(), "repair submodule observation"))
		return spec
	}
	if len(g.Nodes) == 0 {
		if repository, err := gitx.Discover(ctx, root); err == nil && repository.IsLinkedWorktree {
			if _, err := os.Lstat(filepath.Join(repository.GitDir, "modules")); err == nil {
				spec.Conditions = append(spec.Conditions, condition("submodule-orphan-storage", VerdictBlocked, RequirementRequired, "private module storage remains without current gitlinks", "inspect and preserve orphan submodule repositories"))
			}
		}
		return spec
	}
	if spec.Authority == nil {
		spec.Authority = map[string]string{}
	}
	spec.Authority["submodules"] = g.Fingerprint
	repo, err := gitx.Discover(ctx, root)
	if err != nil {
		return spec
	}
	_, revision, err := submodule.Load(s.cfg, repo.GitCommonDir, r.Locator.Branch)
	if err != nil {
		spec.Conditions = append(spec.Conditions, condition("submodule-intent", VerdictError, RequirementRequired, err.Error(), "repair workspace intent"))
		return spec
	}
	spec.Authority["submodule-intent"] = revision
	recursive, cleanup := recursiveRequest(r)
	spec.Authority["submodules.recursive"] = boolString(recursive)
	if cleanup {
		_, err := submodule.InspectRemoval(ctx, s.cfg, root, recursive, submoduleIntegrationRequired(r))
		verdict, evidence := VerdictMet, "all child repositories pass local disposal checks; fresh remote proof runs before mutation"
		if err != nil {
			verdict, evidence = VerdictBlocked, err.Error()
		}
		spec.Conditions = append(spec.Conditions, condition("submodules-disposable", verdict, RequirementRequired, evidence, "preserve/finish every child, then explicitly request --recursive"))
		if !recursive {
			spec.FallbackCommand += " --recursive"
		}
		if recursive {
			var order []string
			for i := len(g.Nodes) - 1; i >= 0; i-- {
				order = append(order, g.Nodes[i].Path)
			}
			for i, effect := range spec.Effects {
				if effect.Code == EffectRemoveWorktree {
					details := effect.Details.Map()
					details["submodule-order"] = strings.Join(order, " -> ")
					details["private-child-clones"] = "dispose after fresh remote proof; preserve outer branch"
					spec.Effects[i] = NewEffect(effect.Code, "stage verified children inside-out, remove outer checkout, then dispose private child clones", effect.Target, true, effect.Network, details)
				}
			}
			spec.Confirmation.Prompt += " This includes private submodule refs and objects after fresh recovery proof."
		}
	}
	publication := isCompletionAction(r.Action)
	if o, ok := r.Options.(ParkWarmOptions); ok && o.Push {
		publication = true
	}
	if cleanup || publication {
		claimHash, claimErr := s.submoduleClaims(ctx, g, r.Locator.TaskID)
		spec.Authority["submodule-claims"] = claimHash
		verdict, evidence := VerdictMet, "child task and artifact observations are complete"
		if claimErr != nil {
			verdict, evidence = VerdictBlocked, claimErr.Error()
		}
		spec.Conditions = append(spec.Conditions, condition("submodule-claims", verdict, RequirementRequired, evidence, "finish child task/artifact ownership before parent cleanup"))
		spec.Effects = append([]Effect{NewEffect(effectVerifySubmodules, "verify every submodule against fresh recovery-origin refs", root, false, true, nil)}, spec.Effects...)
	}
	return spec
}

func (s *lifecycleService) submoduleClaims(ctx context.Context, g gitx.SubmoduleGraph, parentTask string) (string, error) {
	records, diagnostics, err := s.tasks.ListRecords()
	if err != nil || len(diagnostics) > 0 {
		return "unknown", fmt.Errorf("child task inventory is incomplete")
	}
	var evidence []string
	var blockers []string
	for _, n := range g.Nodes {
		child := filepath.Join(g.Root, filepath.FromSlash(n.Path))
		for _, record := range records {
			if record.Task.ID == parentTask {
				continue
			}
			for _, path := range []string{record.Task.RepoPath, record.Task.WorktreePath} {
				if path == "" {
					continue
				}
				inside, err := pathx.Contains(child, path)
				if err != nil {
					return "unknown", err
				}
				if inside {
					evidence = append(evidence, record.Task.ID+":"+record.Revision)
					blockers = append(blockers, fmt.Sprintf("%s is claimed by task %s", n.Path, record.Task.ID))
					break
				}
			}
		}
		inspection, err := s.inspectArtifacts(ctx, s.artifacts, child)
		evidence = append(evidence, n.Path+":"+artifactAuthority(inspection, err))
		if err != nil || !inspection.Ready() {
			blockers = append(blockers, n.Path+" has unknown or unfinished artifacts")
		}
	}
	hash := authorityHash("submodule-claims", evidence...)
	if len(blockers) > 0 {
		return hash, fmt.Errorf("%s", strings.Join(blockers, "; "))
	}
	return hash, nil
}

// The effect remains in the sealed Plan; execution skips only the preflight
// effect already recorded by prepareSubmodules before any ordinary effect.
func (e *executionState) executionEffects() []Effect {
	var out []Effect
	for _, effect := range e.plan.Effects() {
		if effect.Code != effectVerifySubmodules {
			out = append(out, effect)
		}
	}
	return out
}

func (e *executionState) prepareSubmodules(ctx context.Context) error {
	for _, effect := range e.plan.Effects() {
		if effect.Code == effectVerifySubmodules {
			return e.run(effect, func() (string, error) {
				root := e.plan.Locator.CheckoutPath
				recursive, cleanup := recursiveRequest(e.plan.Request)
				var p *submodule.Removal
				var err error
				if cleanup {
					p, err = submodule.InspectRemoval(ctx, e.service.cfg, root, recursive, submoduleIntegrationRequired(e.plan.Request))
				} else {
					p, err = submodule.InspectPublication(ctx, e.service.cfg, root, isCompletionAction(e.plan.Action))
				}
				if err != nil {
					return "", err
				}
				if p == nil {
					return "", fmt.Errorf("submodule graph disappeared")
				}
				if p.Graph.Fingerprint != e.plan.AuthorityFields()["submodules"] || p.Revision != e.plan.AuthorityFields()["submodule-intent"] {
					return "", &StalePlanError{Reason: "submodule graph or intent changed"}
				}
				if err := e.checkSubmoduleClaims(ctx, p, false); err != nil {
					return "", err
				}
				claimHash, err := e.service.submoduleClaims(ctx, p.Graph, e.plan.Locator.TaskID)
				if err != nil {
					return "", err
				}
				if claimHash != e.plan.AuthorityFields()["submodule-claims"] {
					return "", &StalePlanError{Reason: "child task/artifact authority changed"}
				}
				if err := p.VerifyRemote(ctx); err != nil {
					return "", err
				}
				e.submoduleRemoval = p
				return "verified fresh remote reachability for every child repository", nil
			})
		}
	}
	return nil
}

func (e *executionState) checkSubmoduleClaims(ctx context.Context, p *submodule.Removal, afterClose bool) error {
	hash, err := e.service.submoduleClaims(ctx, p.Graph, e.plan.Locator.TaskID)
	if err != nil {
		return err
	}
	if hash != e.plan.AuthorityFields()["submodule-claims"] {
		return &StalePlanError{Reason: "child task/artifact authority changed"}
	}
	if !afterClose {
		return nil
	}
	rt := e.service.defaultRuntime()
	if e.plan.Locator.TaskID != "" {
		record, err := e.service.tasks.GetRecord(e.plan.Locator.TaskID)
		if err != nil {
			return err
		}
		rt, err = e.service.runtimeFor(record.Task)
		if err != nil {
			return err
		}
	}
	options := retire.Options{CWD: e.service.cwd, CallerWorkspaceID: e.service.callerWorkspace, CallerPaneID: e.service.callerPane}
	switch o := e.plan.Request.Options.(type) {
	case RetireOptions:
		options.CloseUnknown = o.CloseUnknown
		options.AssumeNoRuntime = o.AssumeNoRuntime
	case ParkColdOptions:
		options.CloseUnknown = o.CloseUnknown
		options.AssumeNoRuntime = o.AssumeNoRuntime
	case RemoveCheckoutOptions:
		options.CloseUnknown = o.CloseUnknown
		options.AssumeNoRuntime = o.AssumeNoRuntime
	}
	paths := []string{p.Graph.Root}
	for _, n := range p.Graph.Nodes {
		paths = append(paths, filepath.Join(p.Graph.Root, filepath.FromSlash(n.Path)))
	}
	for _, path := range paths {
		live, err := e.service.inspectCleanup(ctx, rt, path, options)
		if err != nil {
			return err
		}
		if !live.Ready() || len(live.Sessions) > 0 {
			return fmt.Errorf("submodule workspace %s has live or unknown runtime coverage", path)
		}
	}
	return nil
}

func (e *executionState) removeWithSubmodules(ctx context.Context, repo, checkout string) error {
	return e.submoduleRemoval.Apply(ctx, func(ctx context.Context, _ string) error {
		return e.checkSubmoduleClaims(ctx, e.submoduleRemoval, true)
	}, func() error { return e.service.removeWorktree(ctx, repo, checkout, false) })
}

// Human-facing summaries use the same graph without making remote requests.
func submoduleSummary(nodes []gitx.SubmoduleNode) string {
	var paths []string
	for _, n := range nodes {
		paths = append(paths, n.Path)
	}
	return strings.Join(paths, ", ")
}
