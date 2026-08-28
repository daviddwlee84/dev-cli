package experiment

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Deprecate changes only experiment metadata. The host location remains
// present, archived, or evicted exactly as it was.
func (s *Service) Deprecate(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	item, diagnostics, err := s.ResolveWithOptions(ctx, request.Ref, ResolveOptions{
		IncludeDeprecated: true,
		IncludeArchived:   true,
		IncludeEvicted:    true,
	})
	result := TransitionResult{Item: item, Diagnostics: diagnostics}
	if err != nil {
		return result, err
	}
	updated, err := s.updatePhase(ctx, item.ID, catalog.PhaseDeprecated)
	if err != nil {
		return result, err
	}
	result.Item = s.itemForEntry(ctx, updated)
	return result, nil
}

// Reactivate changes only experiment metadata. It never restores or recreates
// host-local bytes.
func (s *Service) Reactivate(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	item, diagnostics, err := s.ResolveWithOptions(ctx, request.Ref, ResolveOptions{
		IncludeDeprecated: true,
		IncludeArchived:   true,
		IncludeEvicted:    true,
	})
	result := TransitionResult{Item: item, Diagnostics: diagnostics}
	if err != nil {
		return result, err
	}
	updated, err := s.updatePhase(ctx, item.ID, catalog.PhaseActive)
	if err != nil {
		return result, err
	}
	result.Item = s.itemForEntry(ctx, updated)
	return result, nil
}

func (s *Service) updatePhase(ctx context.Context, id string, phase catalog.ExperimentPhase) (*catalog.Entry, error) {
	var updated *catalog.Entry
	err := s.store.WithLock(ctx, func() error {
		current, err := s.store.Get(id)
		if err != nil {
			return err
		}
		if !eligibleTryRecord(current) {
			return &NotFoundError{Ref: id, Root: s.triesRoot}
		}
		if current.MoveIntent != nil {
			return fmt.Errorf("try %s has a pending %s move", id, current.MoveIntent.Operation)
		}
		if current.Experiment.Phase == phase {
			updated = current
			return nil
		}
		now := s.now()
		updated, err = s.catalogUpdate(id, func(entry *catalog.Entry) error {
			if !eligibleTryRecord(entry) {
				return &NotFoundError{Ref: id, Root: s.triesRoot}
			}
			if entry.MoveIntent != nil {
				return fmt.Errorf("try %s has a pending %s move", id, entry.MoveIntent.Operation)
			}
			switch phase {
			case catalog.PhaseDeprecated:
				if entry.Experiment.Phase != catalog.PhaseActive {
					return fmt.Errorf("try %s cannot transition from %s to deprecated", id, entry.Experiment.Phase)
				}
				entry.Experiment.Phase = catalog.PhaseDeprecated
				entry.Experiment.DeprecatedAt = now
				if location, ok := entry.LocationFor(s.host); ok {
					entry.Experiment.DeprecatedPath = location.CurrentPath
				}
				if entry.Experiment.DeprecatedPath == "" {
					entry.Experiment.DeprecatedPath = entry.Experiment.OriginalPath
				}
			case catalog.PhaseActive:
				if entry.Experiment.Phase != catalog.PhaseDeprecated {
					return fmt.Errorf("try %s cannot transition from %s to active", id, entry.Experiment.Phase)
				}
				entry.Experiment.Phase = catalog.PhaseActive
			default:
				return fmt.Errorf("unsupported experiment phase %q", phase)
			}
			return nil
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("transition try %s to %s: %w", id, phase, err)
	}
	return updated, nil
}

// Patch changes tags and note while preserving task tags as a separate model.
func (s *Service) Patch(ctx context.Context, request PatchRequest) (Item, []Diagnostic, error) {
	item, diagnostics, err := s.ResolveWithOptions(ctx, request.Ref, ResolveOptions{
		IncludeDeprecated: true,
		IncludeArchived:   true,
		IncludeEvicted:    true,
		IncludeGraduated:  true,
	})
	if err != nil {
		return Item{}, diagnostics, err
	}
	remove := catalog.NormalizeTags(request.RemoveTags)
	add := catalog.NormalizeTags(request.AddTags)
	var updated *catalog.Entry
	err = s.store.WithLock(ctx, func() error {
		var updateErr error
		updated, updateErr = s.catalogUpdate(item.ID, func(entry *catalog.Entry) error {
			if entry.Experiment == nil {
				return &NotFoundError{Ref: request.Ref, Root: s.triesRoot}
			}
			if entry.MoveIntent != nil {
				return fmt.Errorf("try %s has a pending %s move", entry.ID, entry.MoveIntent.Operation)
			}
			kept := make([]string, 0, len(entry.Tags)+len(add))
			for _, tag := range entry.Tags {
				if !slices.Contains(remove, strings.ToLower(strings.TrimSpace(tag))) {
					kept = append(kept, tag)
				}
			}
			entry.Tags = append(kept, add...)
			if request.Note != nil {
				entry.Note = *request.Note
			}
			return nil
		})
		return updateErr
	})
	if err != nil {
		return Item{}, diagnostics, fmt.Errorf("mark try %s: %w", request.Ref, err)
	}
	return s.itemForEntry(ctx, updated), diagnostics, nil
}

// Attach explicitly associates an existing catalog ID with one visible Try
// directory. Corrupt catalog state blocks the attachment because an unreadable
// record may already own the path or Git common directory.
func (s *Service) Attach(ctx context.Context, id, path string) (Item, error) {
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return Item{}, fmt.Errorf("attach try path: %w", err)
	}
	if err := s.validateVisibleTryPath(canonical); err != nil {
		return Item{}, fmt.Errorf("attach try path: %w", err)
	}
	probe := s.probeDirectory(ctx, canonical)
	if !probe.valid {
		if probe.diagnostic != nil {
			return Item{}, probe.diagnostic
		}
		return Item{}, fmt.Errorf("attach try path %s: not a directory", canonical)
	}

	var updated *catalog.Entry
	err = s.store.WithLock(ctx, func() error {
		entry, getErr := s.store.Get(id)
		if getErr != nil {
			return getErr
		}
		if !eligibleTryRecord(entry) {
			return &NotFoundError{Ref: id, Root: s.triesRoot}
		}
		if entry.MoveIntent != nil {
			return fmt.Errorf("try %s has a pending %s move", id, entry.MoveIntent.Operation)
		}
		if location, located := entry.LocationFor(s.host); located && !samePath(location.CurrentPath, canonical) {
			switch location.State {
			case catalog.LocationArchived:
				return fmt.Errorf("try %s is archived on this host; use restore instead of attach", id)
			case catalog.LocationEvicted:
				return fmt.Errorf("try %s is evicted on this host and cannot be attached without recovery", id)
			case catalog.LocationPresent:
				if exists, statErr := pathExists(location.CurrentPath); statErr != nil {
					return statErr
				} else if exists {
					return fmt.Errorf("try %s is still present at %s", id, location.CurrentPath)
				}
			}
		}
		entries, diagnostics, listErr := s.store.ListWithDiagnostics()
		if listErr != nil {
			return listErr
		}
		if len(diagnostics) > 0 {
			return incompleteCatalogError(diagnostics)
		}
		for _, matched := range matchingEntries(entries, s.host, probe) {
			if matched.ID != id {
				return fmt.Errorf("attach %s: path or Git common directory already belongs to catalog asset %s", id, matched.ID)
			}
		}
		updated, getErr = s.catalogUpdate(id, func(candidate *catalog.Entry) error {
			if !eligibleTryRecord(candidate) {
				return &NotFoundError{Ref: id, Root: s.triesRoot}
			}
			if candidate.MoveIntent != nil {
				return fmt.Errorf("try %s has a pending %s move", id, candidate.MoveIntent.Operation)
			}
			previous, _ := candidate.LocationFor(s.host)
			if probe.originURL != "" {
				candidate.Experiment.OriginURL = probe.originURL
				candidate.RemoteIdentity = remoteIdentityFromOrigin(probe.originURL)
			}
			return candidate.SetLocation(s.host, locationFromProbe(probe, previous))
		})
		return getErr
	})
	if err != nil {
		return Item{}, fmt.Errorf("attach try %s: %w", id, err)
	}
	return s.itemForEntry(ctx, updated), nil
}

// PlanGraduate resolves either a present or archived Try and validates its
// project destination without changing repository contents.
func (s *Service) PlanGraduate(ctx context.Context, request GraduateRequest) (GraduatePlan, error) {
	item, diagnostics, err := s.resolveGraduateItem(ctx, request)
	plan := GraduatePlan{Item: item, DryRun: request.DryRun, Diagnostics: diagnostics}
	if err != nil {
		return plan, err
	}
	if item.Entry == nil || item.ID == "" || item.CatalogError != nil {
		if item.CatalogError != nil {
			return plan, fmt.Errorf("try %s is not safely tracked: %w", item.DisplayName(), item.CatalogError)
		}
		return plan, fmt.Errorf("try %s is not safely tracked in the catalog", item.DisplayName())
	}
	location, ok := item.Entry.LocationFor(s.host)
	if !ok || (location.State != catalog.LocationPresent && location.State != catalog.LocationArchived) {
		return plan, &NotFoundError{Ref: request.Ref, Root: s.triesRoot}
	}
	source, err := pathx.Canonical(location.CurrentPath)
	if err != nil {
		return plan, fmt.Errorf("graduate source %s: %w", location.CurrentPath, err)
	}

	name := request.Name
	if name == "" {
		name, _, _ = splitDatedBasename(filepath.Base(source))
	}
	if name == "" {
		return plan, fmt.Errorf("could not derive a project name from %s — pass --name", source)
	}
	if err := pathx.ValidateComponent(name); err != nil {
		return plan, fmt.Errorf("project name %q: %w", name, err)
	}
	if name != strings.TrimSpace(name) {
		return plan, fmt.Errorf("project name %q is not normalized: %w", name, pathx.ErrInvalidComponent)
	}
	if request.Category != "" {
		if err := pathx.ValidateComponent(request.Category); err != nil {
			return plan, fmt.Errorf("project category %q: %w", request.Category, err)
		}
		if request.Category != strings.TrimSpace(request.Category) {
			return plan, fmt.Errorf("project category %q is not normalized: %w", request.Category, pathx.ErrInvalidComponent)
		}
	}
	components := []string{name}
	if request.Category != "" {
		components = []string{request.Category, name}
	}
	destination, err := s.availableGraduationDestination(components)
	if err != nil {
		return plan, fmt.Errorf("choose graduation destination: %w", err)
	}
	transition := TransitionPlan{
		Operation: TransitionGraduate, Item: item,
		Source: source, Destination: destination, Diagnostics: diagnostics,
	}
	if err := s.validatePlannedMove(item.Entry, transition); err != nil {
		return plan, err
	}
	probe, err := s.inspectMovableSource(ctx, source)
	if err != nil {
		return plan, err
	}
	transition.LinkedWorktree = probe.live.Repo != nil && probe.live.Repo.IsLinkedWorktree

	plan.Source = source
	plan.Destination = destination
	plan.Category = request.Category
	plan.Name = name
	plan.NeedsGitInit = probe.live.Repo == nil
	plan.NeedsInitialCommit = plan.NeedsGitInit || !s.hasCommit(ctx, source)
	plan.LinkedWorktree = transition.LinkedWorktree
	return plan, nil
}

// Graduate prepares Git at the current location, then uses the same journaled
// move protocol as archive and restore.
func (s *Service) Graduate(ctx context.Context, request GraduateRequest) (GraduateResult, error) {
	plan, err := s.PlanGraduate(ctx, request)
	result := GraduateResult{Plan: plan, Item: plan.Item, Diagnostics: plan.Diagnostics}
	if err != nil {
		return result, err
	}
	if request.DryRun {
		return result, nil
	}
	if plan.NeedsGitInit {
		if _, err := s.gitRun(ctx, plan.Source, "init", "-b", "main"); err != nil {
			return result, fmt.Errorf("initialize Git before graduating %s: %w", plan.Source, err)
		}
		result.GitInitialized = true
	}
	made, err := s.ensureInitialCommit(ctx, plan.Source, plan.Name)
	if err != nil {
		return result, fmt.Errorf("ensure initial commit before graduating %s: %w", plan.Source, err)
	}
	result.InitialCommitMade = made

	transitionResult, err := s.applyTransition(ctx, TransitionResult{
		Plan: TransitionPlan{
			Operation: TransitionGraduate, Item: plan.Item,
			Source: plan.Source, Destination: plan.Destination,
			Diagnostics: plan.Diagnostics, LinkedWorktree: plan.LinkedWorktree,
		},
		Item: plan.Item, Diagnostics: plan.Diagnostics,
	})
	result.Item = transitionResult.Item
	result.Moved = transitionResult.Moved
	result.RolledBack = transitionResult.RolledBack
	result.RollbackError = transitionResult.RollbackError
	return result, err
}

// PlanArchive validates a present Try's collision-safe hidden destination.
func (s *Service) PlanArchive(ctx context.Context, request TransitionRequest) (TransitionPlan, error) {
	item, diagnostics, err := s.ResolveWithOptions(ctx, request.Ref, ResolveOptions{IncludeDeprecated: true})
	plan := TransitionPlan{Operation: TransitionArchive, Item: item, Diagnostics: diagnostics}
	if err != nil {
		return plan, err
	}
	entry := item.Entry
	if entry == nil || item.ID == "" || item.CatalogError != nil {
		return plan, fmt.Errorf("try %s is not safely tracked", item.DisplayName())
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || location.State != catalog.LocationPresent {
		return plan, fmt.Errorf("try %s is not present on this host", item.DisplayName())
	}
	source, err := pathx.Canonical(location.CurrentPath)
	if err != nil {
		return plan, fmt.Errorf("archive source: %w", err)
	}
	destination, err := s.archiveDestination(entry.ID, filepath.Base(source))
	if err != nil {
		return plan, err
	}
	plan.Source, plan.Destination = source, destination
	if err := s.validatePlannedMove(entry, plan); err != nil {
		return plan, err
	}
	probe, err := s.inspectMovableSource(ctx, source)
	if err != nil {
		return plan, err
	}
	plan.LinkedWorktree = probe.live.Repo != nil && probe.live.Repo.IsLinkedWorktree
	return plan, nil
}

// Archive moves a present Try into <tries_root>/.dev/archive/<id>/<basename>.
func (s *Service) Archive(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	plan, err := s.PlanArchive(ctx, request)
	result := TransitionResult{Plan: plan, Item: plan.Item, Diagnostics: plan.Diagnostics}
	if err != nil {
		return result, err
	}
	return s.applyTransition(ctx, result)
}

// PlanRestore validates an archived Try's stored or explicit visible target.
func (s *Service) PlanRestore(ctx context.Context, request TransitionRequest) (TransitionPlan, error) {
	item, diagnostics, err := s.ResolveWithOptions(ctx, request.Ref, ResolveOptions{
		IncludeDeprecated: true,
		IncludeArchived:   true,
	})
	plan := TransitionPlan{Operation: TransitionRestore, Item: item, Diagnostics: diagnostics}
	if err != nil {
		return plan, err
	}
	entry := item.Entry
	if entry == nil || item.ID == "" || item.CatalogError != nil {
		return plan, fmt.Errorf("try %s is not safely tracked", item.DisplayName())
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || location.State != catalog.LocationArchived {
		return plan, fmt.Errorf("try %s is not archived on this host", item.DisplayName())
	}
	source, err := pathx.Canonical(location.CurrentPath)
	if err != nil {
		return plan, fmt.Errorf("restore source: %w", err)
	}
	destination := location.RestorePath
	if request.To != "" {
		destination, err = s.explicitRestoreDestination(request.To)
		if err != nil {
			return plan, err
		}
	}
	if destination == "" {
		return plan, fmt.Errorf("try %s has no stored restore path; pass --to", item.DisplayName())
	}
	destination, err = pathx.Canonical(destination)
	if err != nil {
		return plan, fmt.Errorf("restore destination: %w", err)
	}
	plan.Source, plan.Destination = source, destination
	if err := s.validatePlannedMove(entry, plan); err != nil {
		return plan, err
	}
	probe, err := s.inspectMovableSource(ctx, source)
	if err != nil {
		return plan, err
	}
	plan.LinkedWorktree = probe.live.Repo != nil && probe.live.Repo.IsLinkedWorktree
	return plan, nil
}

// Restore moves an archived Try back to its stored or explicit visible path.
func (s *Service) Restore(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	plan, err := s.PlanRestore(ctx, request)
	result := TransitionResult{Plan: plan, Item: plan.Item, Diagnostics: plan.Diagnostics}
	if err != nil {
		return result, err
	}
	return s.applyTransition(ctx, result)
}

func (s *Service) validatePlannedMove(entry *catalog.Entry, plan TransitionPlan) error {
	if err := s.validateTransitionEntry(entry, plan.Operation, plan.Source); err != nil {
		return err
	}
	if err := s.validateTransitionPaths(entry, plan.Operation, plan.Source, plan.Destination); err != nil {
		return err
	}
	sourceExists, err := pathExists(plan.Source)
	if err != nil {
		return fmt.Errorf("inspect move source %s: %w", plan.Source, err)
	}
	if !sourceExists {
		return fmt.Errorf("move source %s does not exist", plan.Source)
	}
	if err := rejectExisting(plan.Destination); err != nil {
		return err
	}
	same, err := s.sameFilesystem(plan.Source, plan.Destination)
	if err != nil {
		return fmt.Errorf("compare move filesystems: %w", err)
	}
	if !same {
		return fmt.Errorf("move %s to %s: %w", plan.Source, plan.Destination, ErrCrossFilesystem)
	}
	return nil
}

func (s *Service) applyTransition(ctx context.Context, result TransitionResult) (TransitionResult, error) {
	plan := result.Plan
	if err := s.beginMoveIntent(ctx, plan); err != nil {
		return result, err
	}

	failMove := func(cause error) (TransitionResult, error) {
		repairErr := s.reconcileMoveIntentByID(ctx, plan.Item.ID)
		if repairErr != nil {
			return result, errors.Join(cause, fmt.Errorf("repair pending %s intent: %w", plan.Operation, repairErr))
		}
		return result, cause
	}

	if err := os.MkdirAll(filepath.Dir(plan.Destination), 0o755); err != nil {
		return failMove(fmt.Errorf("create %s destination parent: %w", plan.Operation, err))
	}
	same, err := s.sameFilesystem(plan.Source, plan.Destination)
	if err != nil {
		return failMove(fmt.Errorf("compare move filesystems: %w", err))
	}
	if !same {
		return failMove(fmt.Errorf("move %s to %s: %w", plan.Source, plan.Destination, ErrCrossFilesystem))
	}
	fresh, err := s.store.Get(plan.Item.ID)
	if err != nil {
		return failMove(fmt.Errorf("reload %s intent: %w", plan.Operation, err))
	}
	if err := s.validateTransitionEntry(fresh, plan.Operation, plan.Source); err != nil {
		return failMove(fmt.Errorf("revalidate %s catalog source: %w", plan.Operation, err))
	}
	if err := s.validateTransitionPaths(fresh, plan.Operation, plan.Source, plan.Destination); err != nil {
		return failMove(fmt.Errorf("revalidate %s paths: %w", plan.Operation, err))
	}
	if err := rejectExisting(plan.Destination); err != nil {
		return failMove(err)
	}
	probe, err := s.inspectMovableSource(ctx, plan.Source)
	if err != nil {
		return failMove(err)
	}
	linked := probe.live.Repo != nil && probe.live.Repo.IsLinkedWorktree
	if linked != plan.LinkedWorktree {
		return failMove(fmt.Errorf("%s source Git worktree identity changed after planning", plan.Operation))
	}
	if err := s.movePath(ctx, linked, plan.Source, plan.Destination); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return failMove(fmt.Errorf("move %s to %s: %w", plan.Source, plan.Destination, ErrCrossFilesystem))
		}
		return failMove(fmt.Errorf("move %s to %s: %w", plan.Source, plan.Destination, err))
	}
	result.Moved = true

	destinationProbe := s.probeDirectory(ctx, plan.Destination)
	if !destinationProbe.valid {
		cause := errors.New("moved destination is not a directory")
		if destinationProbe.diagnostic != nil {
			cause = destinationProbe.diagnostic.Err
		}
		return s.rollbackTransition(ctx, result, cause)
	}
	updated, err := s.finalizeMoveIntent(ctx, plan.Item.ID, destinationProbe)
	if err != nil {
		return s.rollbackTransition(ctx, result, err)
	}
	result.Item = itemFromEntry(updated, destinationProbe.live)
	enriched := s.enrich(ctx, []Item{result.Item})
	if len(enriched) == 1 {
		result.Item = enriched[0]
	}
	return result, nil
}

func (s *Service) beginMoveIntent(ctx context.Context, plan TransitionPlan) error {
	return s.store.WithLock(ctx, func() error {
		entry, err := s.store.Get(plan.Item.ID)
		if err != nil {
			return err
		}
		if entry.MoveIntent != nil {
			return fmt.Errorf("catalog asset %s already has a pending %s move", entry.ID, entry.MoveIntent.Operation)
		}
		if err := s.validateTransitionEntry(entry, plan.Operation, plan.Source); err != nil {
			return err
		}
		if err := s.validateTransitionPaths(entry, plan.Operation, plan.Source, plan.Destination); err != nil {
			return err
		}
		sourceExists, err := pathExists(plan.Source)
		if err != nil {
			return err
		}
		destinationExists, err := pathExists(plan.Destination)
		if err != nil {
			return err
		}
		if !sourceExists || destinationExists {
			return fmt.Errorf("stale %s plan: source exists=%t destination exists=%t", plan.Operation, sourceExists, destinationExists)
		}
		_, err = s.catalogUpdate(entry.ID, func(current *catalog.Entry) error {
			if current.MoveIntent != nil {
				return fmt.Errorf("catalog asset %s already has a pending %s move", current.ID, current.MoveIntent.Operation)
			}
			if err := s.validateTransitionEntry(current, plan.Operation, plan.Source); err != nil {
				return err
			}
			current.MoveIntent = &catalog.MoveIntent{
				Host: s.host, Operation: string(plan.Operation),
				SourcePath: plan.Source, DestinationPath: plan.Destination,
				Started: s.now(),
			}
			return nil
		})
		return err
	})
}

func (s *Service) rollbackTransition(ctx context.Context, result TransitionResult, cause error) (TransitionResult, error) {
	plan := result.Plan
	rollbackErr := rejectExisting(plan.Source)
	if rollbackErr == nil {
		rollbackErr = s.movePath(ctx, plan.LinkedWorktree, plan.Destination, plan.Source)
	}
	if rollbackErr == nil {
		if repairErr := s.reconcileMoveIntentByID(ctx, plan.Item.ID); repairErr != nil {
			rollbackErr = fmt.Errorf("filesystem move rolled back but catalog intent repair failed: %w", repairErr)
		}
	}
	result.RollbackError = rollbackErr
	result.RolledBack = rollbackErr == nil
	if rollbackErr == nil {
		result.Moved = false
	}
	operation := string(plan.Operation)
	if plan.Operation == TransitionGraduate {
		operation = "graduation"
	}
	return result, &FinalizeError{
		Operation: operation, Source: plan.Source, Destination: plan.Destination,
		Cause: cause, RollbackErr: rollbackErr,
	}
}

func (s *Service) movePath(ctx context.Context, linked bool, source, destination string) error {
	if !linked {
		return s.rename(source, destination)
	}
	repository, err := s.gitDiscover(ctx, source)
	if err != nil {
		return fmt.Errorf("discover linked worktree before move: %w", err)
	}
	if !repository.IsLinkedWorktree || repository.MainRoot == "" || samePath(repository.MainRoot, source) {
		return fmt.Errorf("%s is no longer a linked worktree with a separate main checkout", source)
	}
	// Run from the stable main checkout rather than from the directory Git is
	// moving. This avoids platform-specific failures when a process has its own
	// current working directory open inside the linked worktree.
	return s.moveWorktree(ctx, repository.MainRoot, source, destination)
}

func (s *Service) inspectMovableSource(ctx context.Context, source string) (directoryProbe, error) {
	probe := s.probeDirectory(ctx, source)
	if !probe.valid {
		if probe.diagnostic != nil {
			return probe, probe.diagnostic
		}
		return probe, fmt.Errorf("move source %s is not a directory", source)
	}
	if probe.live.DiscoverError != nil {
		return probe, fmt.Errorf("inspect Git metadata before move: %w", probe.live.DiscoverError)
	}
	if probe.live.Repo == nil {
		return probe, nil
	}
	if probe.live.Repo.Bare {
		return probe, errors.New("moving a bare repository through Try lifecycle is not supported")
	}
	if probe.live.Repo.IsLinkedWorktree {
		return probe, nil
	}
	worktrees, err := s.worktrees(ctx, source)
	if err != nil {
		return probe, fmt.Errorf("inspect linked worktrees before move: %w", err)
	}
	if len(worktrees) > 1 {
		return probe, errors.New("moving a main checkout with linked worktrees is not supported; move or remove its linked worktrees first")
	}
	return probe, nil
}

func (s *Service) archiveDestination(id, basename string) (string, error) {
	if err := catalog.ValidateID(id); err != nil {
		return "", err
	}
	if err := pathx.ValidateComponent(basename); err != nil {
		return "", fmt.Errorf("archive basename: %w", err)
	}
	root, err := s.canonicalArchiveRoot()
	if err != nil {
		return "", err
	}
	destination, err := pathx.JoinChild(root, id, basename)
	if err != nil {
		return "", fmt.Errorf("archive destination: %w", err)
	}
	return destination, nil
}

func (s *Service) canonicalArchiveRoot() (string, error) {
	triesRoot, err := pathx.Canonical(s.triesRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize tries root: %w", err)
	}
	root := filepath.Join(triesRoot, ".dev", "archive")
	canonical, err := pathx.CanonicalChild(triesRoot, root)
	if err != nil {
		return "", fmt.Errorf("validate archive root: %w", err)
	}
	return canonical, nil
}

func (s *Service) explicitRestoreDestination(to string) (string, error) {
	if filepath.IsAbs(to) {
		return pathx.Canonical(to)
	}
	if err := pathx.ValidateComponent(to); err != nil {
		return "", fmt.Errorf("restore destination %q: %w", to, err)
	}
	return pathx.JoinChild(s.triesRoot, to)
}

func (s *Service) validateTransitionEntry(entry *catalog.Entry, operation TransitionOperation, source string) error {
	if !eligibleTryRecord(entry) {
		return fmt.Errorf("catalog asset %s is not a mutable Try", entry.ID)
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || !storedLocationMatches(location, source) {
		return fmt.Errorf("catalog asset %s moved or was reassigned after planning", entry.ID)
	}
	switch operation {
	case TransitionArchive:
		if location.State != catalog.LocationPresent {
			return fmt.Errorf("catalog asset %s is not present", entry.ID)
		}
	case TransitionRestore:
		if location.State != catalog.LocationArchived {
			return fmt.Errorf("catalog asset %s is not archived", entry.ID)
		}
	case TransitionGraduate:
		if location.State != catalog.LocationPresent && location.State != catalog.LocationArchived {
			return fmt.Errorf("catalog asset %s is neither present nor archived", entry.ID)
		}
	default:
		return fmt.Errorf("unknown move operation %q", operation)
	}
	return nil
}

func (s *Service) validateTransitionPaths(entry *catalog.Entry, operation TransitionOperation, source, destination string) error {
	if err := s.validateTransitionContainment(entry.ID, operation, source, destination); err != nil {
		return err
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || !storedLocationMatches(location, source) {
		return fmt.Errorf("catalog asset %s no longer owns %s", entry.ID, source)
	}
	return nil
}

func (s *Service) validateTransitionContainment(id string, operation TransitionOperation, source, destination string) error {
	canonicalSource, err := pathx.Canonical(source)
	if err != nil {
		return fmt.Errorf("canonicalize %s source: %w", operation, err)
	}
	canonicalDestination, err := pathx.Canonical(destination)
	if err != nil {
		return fmt.Errorf("canonicalize %s destination: %w", operation, err)
	}
	if canonicalSource != filepath.Clean(source) || canonicalDestination != filepath.Clean(destination) {
		return fmt.Errorf("%s paths changed after canonicalization", operation)
	}
	if canonicalSource == canonicalDestination {
		return fmt.Errorf("%s source and destination are identical", operation)
	}

	switch operation {
	case TransitionArchive:
		if err := s.validateVisibleTryPath(canonicalSource); err != nil {
			return fmt.Errorf("archive source: %w", err)
		}
		expected, err := s.archiveDestination(id, filepath.Base(canonicalSource))
		if err != nil {
			return err
		}
		if expected != canonicalDestination {
			return fmt.Errorf("archive destination %s is not the reserved path %s", canonicalDestination, expected)
		}
	case TransitionRestore:
		if err := s.validateArchivedPath(id, canonicalSource); err != nil {
			return fmt.Errorf("restore source: %w", err)
		}
		if err := s.validateVisibleTryPath(canonicalDestination); err != nil {
			return fmt.Errorf("restore destination: %w", err)
		}
	case TransitionGraduate:
		visibleErr := s.validateVisibleTryPath(canonicalSource)
		archivedErr := s.validateArchivedPath(id, canonicalSource)
		if visibleErr != nil && archivedErr != nil {
			return fmt.Errorf("graduate source is neither a visible nor reserved archived Try: %v; %v", visibleErr, archivedErr)
		}
		if err := s.validateProjectDestination(canonicalDestination); err != nil {
			return fmt.Errorf("graduate destination: %w", err)
		}
	default:
		return fmt.Errorf("unknown move operation %q", operation)
	}
	if nested, nestedErr := pathx.IsChild(canonicalSource, canonicalDestination); nestedErr != nil {
		return fmt.Errorf("validate %s destination: %w", operation, nestedErr)
	} else if nested {
		return fmt.Errorf("%s destination %s is inside its source %s", operation, canonicalDestination, canonicalSource)
	}
	return nil
}

func (s *Service) validateArchivedPath(id, path string) error {
	root, err := s.canonicalArchiveRoot()
	if err != nil {
		return err
	}
	canonical, err := pathx.CanonicalChild(root, path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil {
		return err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != id || strings.HasPrefix(parts[1], ".") {
		return fmt.Errorf("%s is not the reserved archive path for %s", canonical, id)
	}
	return nil
}

func (s *Service) validateProjectDestination(path string) error {
	root, err := pathx.Canonical(s.projectRoot)
	if err != nil {
		return err
	}
	canonical, err := pathx.CanonicalChild(root, path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil {
		return err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) < 1 || len(parts) > 2 {
		return fmt.Errorf("%s must be a project or category/project below %s", canonical, root)
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("hidden project component %q is not allowed", part)
		}
		if err := pathx.ValidateComponent(part); err != nil {
			return err
		}
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func moveIntentEqual(left, right *catalog.MoveIntent) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// ReconcileMoveIntents repairs moves interrupted between intent creation and
// catalog finalization. Ambiguous both-present and neither-present states are
// retained for explicit recovery and returned as diagnostics.
func (s *Service) ReconcileMoveIntents(ctx context.Context) ([]Diagnostic, error) {
	entries, catalogProblems, err := s.store.ListWithDiagnostics()
	if err != nil {
		return nil, err
	}
	diagnostics := catalogDiagnostics(catalogProblems)
	for _, entry := range entries {
		if entry.MoveIntent == nil || entry.MoveIntent.Host != s.host {
			continue
		}
		if err := s.reconcileMoveIntent(ctx, entry); err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Path: entry.ID, Operation: "reconcile pending move", Err: err,
			})
		}
	}
	diagnostics = deduplicateDiagnostics(diagnostics)
	sortDiagnostics(diagnostics)
	return diagnostics, nil
}

func (s *Service) reconcileMoveIntentByID(ctx context.Context, id string) error {
	entry, err := s.store.Get(id)
	if err != nil {
		return err
	}
	if entry.MoveIntent == nil || entry.MoveIntent.Host != s.host {
		return nil
	}
	return s.reconcileMoveIntent(ctx, entry)
}

func (s *Service) reconcileMoveIntent(ctx context.Context, snapshot *catalog.Entry) error {
	intent := snapshot.MoveIntent
	if intent == nil || intent.Host != s.host {
		return nil
	}
	operation := TransitionOperation(intent.Operation)
	if err := s.validateTransitionContainment(snapshot.ID, operation, intent.SourcePath, intent.DestinationPath); err != nil {
		return err
	}
	sourceExists, err := pathExists(intent.SourcePath)
	if err != nil {
		return err
	}
	destinationExists, err := pathExists(intent.DestinationPath)
	if err != nil {
		return err
	}
	switch {
	case sourceExists && !destinationExists:
		return s.clearMoveIntent(ctx, snapshot.ID, intent)
	case !sourceExists && destinationExists:
		probe := s.probeDirectory(ctx, intent.DestinationPath)
		if !probe.valid {
			if probe.diagnostic != nil {
				return probe.diagnostic
			}
			return fmt.Errorf("pending %s destination %s is not a directory", operation, intent.DestinationPath)
		}
		_, err := s.finalizeMoveIntent(ctx, snapshot.ID, probe)
		return err
	case sourceExists && destinationExists:
		return fmt.Errorf("pending %s move for %s is ambiguous: both source and destination exist; intent retained", operation, snapshot.ID)
	default:
		return fmt.Errorf("pending %s move for %s is ambiguous: neither source nor destination exists; intent retained", operation, snapshot.ID)
	}
}

func (s *Service) clearMoveIntent(ctx context.Context, id string, expected *catalog.MoveIntent) error {
	return s.store.WithLock(ctx, func() error {
		current, err := s.store.Get(id)
		if err != nil {
			return err
		}
		if !moveIntentEqual(current.MoveIntent, expected) {
			return fmt.Errorf("catalog asset %s move intent changed during reconciliation", id)
		}
		if err := s.validateTransitionContainment(id, TransitionOperation(expected.Operation), expected.SourcePath, expected.DestinationPath); err != nil {
			return err
		}
		sourceExists, err := pathExists(expected.SourcePath)
		if err != nil {
			return err
		}
		destinationExists, err := pathExists(expected.DestinationPath)
		if err != nil {
			return err
		}
		if !sourceExists || destinationExists {
			return fmt.Errorf("catalog asset %s move state changed during rollback reconciliation", id)
		}
		_, err = s.catalogUpdate(id, func(entry *catalog.Entry) error {
			if !moveIntentEqual(entry.MoveIntent, expected) {
				return fmt.Errorf("catalog asset %s move intent changed during reconciliation", id)
			}
			entry.MoveIntent = nil
			return nil
		})
		return err
	})
}

func (s *Service) finalizeMoveIntent(ctx context.Context, id string, probe directoryProbe) (*catalog.Entry, error) {
	var updated *catalog.Entry
	err := s.store.WithLock(ctx, func() error {
		current, err := s.store.Get(id)
		if err != nil {
			return err
		}
		intent := current.MoveIntent
		if intent == nil || intent.Host != s.host {
			return fmt.Errorf("catalog asset %s has no pending move for this host", id)
		}
		operation := TransitionOperation(intent.Operation)
		if err := s.validateTransitionEntry(current, operation, intent.SourcePath); err != nil {
			return err
		}
		if err := s.validateTransitionPaths(current, operation, intent.SourcePath, intent.DestinationPath); err != nil {
			return err
		}
		sourceExists, err := pathExists(intent.SourcePath)
		if err != nil {
			return err
		}
		destinationExists, err := pathExists(intent.DestinationPath)
		if err != nil {
			return err
		}
		if sourceExists || !destinationExists {
			return fmt.Errorf("catalog asset %s move is not ready to finalize", id)
		}
		if !samePath(probe.live.CurrentPath, intent.DestinationPath) {
			return fmt.Errorf("moved destination probe no longer matches %s", intent.DestinationPath)
		}
		if probe.live.DiscoverError != nil {
			return fmt.Errorf("re-probe moved destination Git metadata: %w", probe.live.DiscoverError)
		}
		if operation == TransitionGraduate && probe.live.Repo == nil {
			return errors.New("moved project is not a discoverable Git repository")
		}

		expected := *intent
		updated, err = s.catalogUpdate(id, func(entry *catalog.Entry) error {
			if entry.MoveIntent == nil || *entry.MoveIntent != expected {
				return fmt.Errorf("catalog asset %s move intent changed before finalization", id)
			}
			previous, ok := entry.LocationFor(s.host)
			if !ok {
				return fmt.Errorf("catalog asset %s lost its host location", id)
			}
			switch operation {
			case TransitionArchive:
				location := locationFromProbe(probe, previous)
				location.State = catalog.LocationArchived
				location.RestorePath = intent.SourcePath
				if err := entry.SetLocation(s.host, location); err != nil {
					return err
				}
			case TransitionRestore:
				location := locationFromProbe(probe, previous)
				location.RestorePath = ""
				if err := entry.SetLocation(s.host, location); err != nil {
					return err
				}
			case TransitionGraduate:
				entry.Kind = catalog.KindRepository
				entry.Name = filepath.Base(intent.DestinationPath)
				entry.Experiment.Phase = catalog.PhaseGraduated
				entry.Experiment.GraduatedAt = intent.Started
				entry.Experiment.GraduatedPath = probe.live.CurrentPath
				if probe.originURL != "" {
					entry.Experiment.OriginURL = probe.originURL
					entry.RemoteIdentity = remoteIdentityFromOrigin(probe.originURL)
				}
				location := locationFromProbe(probe, previous)
				location.RestorePath = ""
				if err := entry.SetLocation(s.host, location); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown move operation %q", operation)
			}
			entry.MoveIntent = nil
			return nil
		})
		return err
	})
	return updated, err
}

func (s *Service) itemForEntry(ctx context.Context, entry *catalog.Entry) Item {
	if entry == nil {
		return Item{}
	}
	location, located := entry.LocationFor(s.host)
	live := LiveFacts{}
	if located {
		live.CurrentPath = location.CurrentPath
		live.RealPath = location.RealPath
		live.Present = location.State == catalog.LocationPresent
	}
	if live.Present {
		probe := s.probeDirectory(ctx, live.CurrentPath)
		if probe.valid {
			live = probe.live
		}
	}
	item := itemFromEntry(entry, live)
	if live.Present {
		enriched := s.enrich(ctx, []Item{item})
		if len(enriched) == 1 {
			item = enriched[0]
		}
	}
	return item
}
