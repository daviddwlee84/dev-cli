package triage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (s *Service) tryService() (*experiment.Service, error) {
	hooks := s.cfg.TryHooks
	hooks.RemovalGuard = s.tryGuard
	hooks.ForgetGuard = func(ctx context.Context, entry *catalog.Entry) (string, error) {
		if err := note.NewStore(s.cfg.Config.NotesDir()).CheckUnreferenced(entry.ID); err != nil {
			return "", err
		}
		location, _ := entry.LocationFor(s.cfg.Host)
		return s.tryGuard(ctx, location.CurrentPath)
	}
	return experiment.NewService(experiment.ServiceConfig{Store: s.cfg.Catalog, TriesRoot: config.Expand(s.cfg.Config.Paths.TriesRoot), ProjectRoot: config.Expand(s.cfg.Config.Paths.ProjectRoot), Host: s.cfg.Host, Hooks: hooks})
}

func (s *Service) tryGuard(ctx context.Context, path string) (string, error) {
	if s.cfg.Tasks == nil {
		return "", errors.New("task inventory unavailable")
	}
	related := func(candidate string) (bool, error) {
		if candidate == "" {
			return false, nil
		}
		within, err := pathx.Contains(path, candidate)
		if err != nil || within {
			return within, err
		}
		return pathx.Contains(candidate, path)
	}
	tasks, diagnostics, err := s.cfg.Tasks.ListWithDiagnostics()
	if err != nil || len(diagnostics) > 0 {
		return "", errors.New("task inventory incomplete")
	}
	for _, t := range tasks {
		for _, p := range []string{t.RepoPath, t.WorktreePath} {
			if yes, err := related(p); err != nil {
				return "", err
			} else if yes {
				return "", fmt.Errorf("task %s references this Try", t.ID)
			}
		}
	}
	artifacts := s.cfg.Lifecycle.Artifacts
	if artifacts == nil {
		artifacts = artifact.NewStore(filepath.Join(s.cfg.Config.StateDir(), "artifact-intents", "v1"))
	}
	intents, err := artifacts.List()
	if err != nil {
		return "", err
	}
	for _, intent := range intents {
		for _, p := range []string{intent.RepoPath, intent.WorktreePath, intent.GitCommonDir} {
			if yes, err := related(p); err != nil {
				return "", err
			} else if yes {
				return "", errors.New("agent artifact intent references this Try")
			}
		}
	}
	if len(s.cfg.Runtimes) == 0 {
		return "", errors.New("runtime coverage unavailable; inspect this Try individually")
	}
	names := []string{}
	covered := func(candidate string) (bool, error) {
		if candidate == "" {
			return false, nil
		}
		return pathx.Contains(path, candidate)
	}
	for _, rt := range s.cfg.Runtimes {
		if rt == nil || rt.Name() == "none" {
			return "", errors.New("runtime coverage unavailable")
		}
		sessions, err := rt.List(ctx)
		if err != nil {
			return "", fmt.Errorf("%s coverage unavailable: %w", rt.Name(), err)
		}
		for _, session := range sessions {
			paths := append([]string{}, session.Dirs...)
			paths = append(paths, session.WorkspaceCheckout)
			for _, pane := range session.Panes {
				paths = append(paths, pane.CWD, pane.ShellCWD)
			}
			known := false
			for _, p := range paths {
				known = known || p != ""
				if yes, err := covered(p); err != nil {
					return "", err
				} else if yes {
					return "", fmt.Errorf("live %s session %s references this Try", rt.Name(), session.Handle)
				}
			}
			if !known {
				return "", errors.New("runtime session has unknown directory coverage")
			}
		}
		if lister, ok := rt.(runtime.AgentActivityLister); ok {
			agents, err := lister.AgentActivities(ctx)
			if err != nil {
				return "", err
			}
			for _, agent := range agents {
				if agent.CWD == "" {
					return "", errors.New("agent directory coverage unavailable")
				}
				if yes, err := covered(agent.CWD); err != nil {
					return "", err
				} else if yes {
					return "", errors.New("recognized agent references this Try")
				}
			}
		}
		names = append(names, rt.Name())
	}
	return digest(names), nil
}

// Candidate decoration is cheap: exact recursive content/occupancy checks are
// deferred to Prepare, then repeated at Apply by experiment's guarded service.
func (s *Service) tryCandidates(ctx context.Context, i *Item) {
	if i.Kind != "try" || (i.Scope != "directory" && i.Scope != "checkout") {
		return
	}
	service, err := s.tryService()
	if err != nil {
		i.add("try-unavailable", err.Error(), 0, false)
		return
	}
	i.Presence = service.Presence(i.Path)
	switch i.Presence {
	case "missing":
		filtered := i.Findings[:0]
		for _, f := range i.Findings {
			if f.Code != "unavailable" {
				filtered = append(filtered, f)
			}
		}
		i.Findings = filtered
		i.add("missing", "Directory is confirmed missing; preview forgetting its unreferenced catalog entry", 0, false)
		if i.CatalogID != "" {
			i.Actions = append(i.Actions, Action{Name: "forget-try", Availability: "candidate"})
		}
	case "present":
		a := Action{Name: "trash-try", Availability: "candidate"}
		if i.Worktree != nil && !i.Worktree.Main {
			a.Availability, a.Reason = "blocked", "linked/shared checkout: use its worktree lifecycle individually"
		}
		i.Actions = append(i.Actions, a)
	case "unavailable":
		i.add("try-unavailable", "Presence cannot be verified; restore access before choosing a removal action", 0, false)
	}
}

func (s *Service) PlanForget(ctx context.Context, ref string, expected *catalog.Entry) (experiment.ForgetPlan, error) {
	service, err := s.tryService()
	if err != nil {
		return experiment.ForgetPlan{}, err
	}
	return service.PlanForget(ctx, ref, expected)
}

func (s *Service) ApplyForget(ctx context.Context, plan experiment.ForgetPlan) (experiment.RemovalResult, error) {
	service, err := s.tryService()
	if err != nil {
		return experiment.RemovalResult{}, err
	}
	var result experiment.RemovalResult
	err = s.cfg.Tasks.WithLock(ctx, func(*task.Tx) error {
		return note.NewStore(s.cfg.Config.NotesDir()).WithUnreferenced(ctx, plan.ID, func() error {
			var err error
			result, err = service.ApplyForget(ctx, plan)
			return err
		})
	})
	return result, err
}

func (s *Service) prepareTry(ctx context.Context, item Item, action string) batchEntry {
	e := batchEntry{item: item, preview: Preview{ItemID: item.ID, Path: item.Path, Action: action, Reasons: []string{}, Effects: []string{}}}
	service, err := s.tryService()
	if err == nil && action == "forget-try" {
		var plan experiment.ForgetPlan
		plan, err = service.PlanForget(ctx, item.CatalogID, nil)
		if err == nil && plan.Source != item.Path {
			err = errors.New("selected Try path changed")
		}
		if err == nil {
			e.preview.PlanID, e.preview.Ready = plan.ID, true
			e.preview.Effects = append(e.preview.Effects, "Forget catalog entry "+plan.ID+"; no project or note files are removed")
			e.tryApply = func(ctx context.Context) (experiment.RemovalResult, error) { return s.ApplyForget(ctx, plan) }
		}
	} else if err == nil {
		req := experiment.RemovalRequest{Ref: item.CatalogID}
		if item.CatalogID == "" {
			req.Path = item.Path
		}
		var plan experiment.RemovalPlan
		plan, err = service.PlanRemoval(ctx, req)
		if err == nil && plan.Source != item.Path {
			err = errors.New("selected Try path changed")
		}
		if err == nil {
			e.preview.PlanID, e.preview.Ready = plan.ID, true
			if plan.Register {
				e.preview.Effects = append(e.preview.Effects, "Register this selected Try before moving it to Trash")
			}
			e.preview.Effects = append(e.preview.Effects, fmt.Sprintf("Move whole directory to system Trash: %d files, %d logical bytes; includes ignored and untracked contents", plan.Files, plan.Bytes))
			e.preview.Effects = append(e.preview.Effects, plan.Warnings...)
			for _, f := range item.Findings {
				if f.Work {
					e.preview.Effects = append(e.preview.Effects, f.Code+": "+f.Detail)
				}
			}
			e.tryApply = func(ctx context.Context) (experiment.RemovalResult, error) {
				var result experiment.RemovalResult
				err := s.cfg.Tasks.WithLock(ctx, func(*task.Tx) error { var err error; result, err = service.ApplyRemoval(ctx, plan); return err })
				return result, err
			}
		}
	}
	if err != nil {
		e.preview.Reasons = append(e.preview.Reasons, SafeText(err.Error()))
	}
	return e
}
