package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
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
	recursive, cleanup := recursiveRequest(r)
	var removal *submodule.Removal
	var removalErr error
	if cleanup {
		removal, removalErr = submodule.InspectRemoval(ctx, s.cfg, root, recursive, submoduleIntegrationRequired(r))
	}
	if len(g.Nodes) == 0 && removal == nil && removalErr == nil {
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
	spec.Authority["submodules.recursive"] = boolString(recursive)
	allEmpty := cleanup && removal != nil && removal.LayoutFingerprint != ""
	for _, n := range g.Nodes {
		if n.Initialized {
			allEmpty = false
		}
	}
	if cleanup {
		if removal != nil {
			spec.Authority["submodule-removal"] = removal.LayoutFingerprint
		}
		verdict, evidence := VerdictMet, "all child repositories pass local disposal checks; fresh remote proof runs before mutation"
		if allEmpty {
			evidence = "only absent or empty gitlinks and directory-only administration remain; local recursive cleanup needs no child remote proof"
		}
		if removalErr != nil {
			verdict, evidence = VerdictBlocked, removalErr.Error()
		}
		spec.Conditions = append(spec.Conditions, condition("submodules-disposable", verdict, RequirementRequired, evidence, "preserve/finish every child, then explicitly request --recursive"))
		if !recursive {
			spec.FallbackCommand += " --recursive"
		}
		if recursive {
			var order []string
			for i := len(g.Nodes) - 1; i >= 0; i-- {
				if g.Nodes[i].Initialized {
					order = append(order, g.Nodes[i].Path)
				}
			}
			for i, effect := range spec.Effects {
				if effect.Code == EffectRemoveWorktree {
					details := effect.Details.Map()
					details["submodule-order"] = strings.Join(order, " -> ")
					details["private-child-clones"] = "dispose after fresh remote proof; preserve outer branch"
					description := "stage verified children inside-out, remove outer checkout, then dispose private child clones"
					if allEmpty {
						details["private-child-clones"] = "none; only reviewed empty administration directories may be pruned"
						description = "recheck empty submodule layout, prune empty administration directories, then remove outer checkout"
					}
					spec.Effects[i] = NewEffect(effect.Code, description, effect.Target, true, effect.Network, details)
				}
			}
			if allEmpty {
				spec.Confirmation.Prompt += " This includes only reviewed empty submodule directories; no child repository is disposed."
			} else {
				spec.Confirmation.Prompt += " This includes private submodule refs and objects after fresh recovery proof."
			}
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
		description := "verify every initialized submodule against fresh recovery-origin refs"
		if allEmpty {
			description = "revalidate empty submodule layout and child ownership locally"
		}
		spec.Effects = append([]Effect{NewEffect(effectVerifySubmodules, description, root, false, !allEmpty, nil)}, spec.Effects...)
	}
	return spec
}

func (s *lifecycleService) submoduleClaims(ctx context.Context, g gitx.SubmoduleGraph, parentTask string) (string, error) {
	records, diagnostics, err := s.tasks.ListRecords()
	if err != nil || len(diagnostics) > 0 {
		causes := []error{errors.New("child task inventory is incomplete"), err}
		for _, diagnostic := range diagnostics {
			causes = append(causes, diagnostic)
		}
		return "unknown", errors.Join(causes...)
	}
	var intents []artifact.Intent
	for _, n := range g.Nodes {
		if !n.Initialized {
			if s.artifacts == nil {
				return "unknown", fmt.Errorf("submodule %s artifact inventory is unavailable", n.Path)
			}
			intents, err = s.artifacts.List()
			if err != nil {
				return "unknown", fmt.Errorf("submodule %s artifact observation: %w", n.Path, err)
			}
			break // One strict listing serves all empty/absent child paths.
		}
	}
	var evidence []string
	var blockers []error
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
					return "unknown", fmt.Errorf("submodule %s task %s ownership: %w", n.Path, record.Task.ID, err)
				}
				if inside {
					evidence = append(evidence, record.Task.ID+":"+record.Revision)
					blockers = append(blockers, fmt.Errorf("%s is claimed by task %s", n.Path, record.Task.ID))
					break
				}
			}
		}
		if !n.Initialized {
			for _, intent := range intents {
				matched := false
				for _, path := range []string{intent.WorktreePath, intent.RepoPath} {
					if !filepath.IsAbs(path) {
						return "unknown", fmt.Errorf("submodule %s artifact intent %s has non-absolute ownership", n.Path, intent.ID)
					}
					inside, err := pathx.Contains(child, path)
					if err != nil {
						return "unknown", fmt.Errorf("submodule %s artifact intent %s identity: %w", n.Path, intent.ID, err)
					}
					matched = matched || inside
				}
				if matched {
					// Preserve identity/status/receipt authority even for discarded
					// intents. Do not ask Git at an empty path: it finds the parent.
					data, err := json.Marshal(intent)
					if err != nil {
						return "unknown", fmt.Errorf("submodule %s artifact intent %s: %w", n.Path, intent.ID, err)
					}
					evidence = append(evidence, n.Path+":"+string(data))
					if intent.Status != artifact.Discarded {
						blockers = append(blockers, fmt.Errorf("empty submodule %s is claimed by artifact intent %s", n.Path, intent.ID))
					}
				}
			}
			continue
		}
		inspection, err := s.inspectArtifacts(ctx, s.artifacts, child)
		evidence = append(evidence, n.Path+":"+artifactAuthority(inspection, err))
		if err != nil {
			blockers = append(blockers, fmt.Errorf("submodule %s artifact observation: %w", n.Path, err))
		} else if !inspection.Ready() {
			blockers = append(blockers, fmt.Errorf("submodule %s has unfinished artifacts", n.Path))
		}
	}
	sort.Strings(evidence)
	return authorityHash("submodule-claims", evidence...), errors.Join(blockers...)
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
				if p.Graph.Fingerprint != e.plan.AuthorityFields()["submodules"] || p.Revision != e.plan.AuthorityFields()["submodule-intent"] ||
					(cleanup && p.LayoutFingerprint != e.plan.AuthorityFields()["submodule-removal"]) {
					return "", &StalePlanError{Reason: "submodule graph, removal layout or intent changed"}
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
				if cleanup && p.InitializedCount() == 0 {
					return "verified empty submodule layout and child ownership locally", nil
				}
				return "verified fresh remote reachability for every initialized child repository", nil
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
	err := e.submoduleRemoval.Apply(ctx, func(ctx context.Context, _ string) error {
		return e.checkSubmoduleClaims(ctx, e.submoduleRemoval, true)
	}, func() error { return e.service.removeWorktree(ctx, repo, checkout, false) })
	var pruned *submodule.PrunedDirectoriesError
	if errors.As(err, &pruned) {
		e.partial = true
		e.recovery = append(e.recovery, "Empty submodule administration directories were pruned: "+strings.Join(pruned.Paths, ", ")+". Outer cleanup is incomplete; refresh the plan before retrying.")
	}
	return err
}

// Human-facing summaries use the same graph without making remote requests.
func submoduleSummary(nodes []gitx.SubmoduleNode) string {
	var paths []string
	for _, n := range nodes {
		paths = append(paths, n.Path)
	}
	return strings.Join(paths, ", ")
}
