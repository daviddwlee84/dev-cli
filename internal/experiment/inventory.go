package experiment

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

type directoryProbe struct {
	live       LiveFacts
	originURL  string
	valid      bool
	diagnostic *Diagnostic
	gitMarker  bool
}

// Reconcile backfills every immediate, non-hidden directory under TriesRoot.
// It never moves, renames, or writes into a discovered directory. Filesystem
// and Git probes happen before the short cross-process catalog transaction.
func (s *Service) Reconcile(ctx context.Context) ([]Item, []Diagnostic, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	intentDiagnostics, err := s.ReconcileMoveIntents(ctx)
	if err != nil {
		return nil, intentDiagnostics, err
	}
	if err := s.ensureTriesRoot(); err != nil {
		return nil, intentDiagnostics, err
	}
	directoryEntries, err := os.ReadDir(s.triesRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("read tries root %s: %w", s.triesRoot, err)
	}
	paths := make([]string, 0, len(directoryEntries))
	for _, entry := range directoryEntries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		paths = append(paths, filepath.Join(s.triesRoot, entry.Name()))
	}
	probes := boundedMap(ctx, s.workers, paths, func(ctx context.Context, path string) directoryProbe {
		return s.probeDirectory(ctx, path)
	})

	validProbes := make([]directoryProbe, 0, len(probes))
	diagnostics := append([]Diagnostic(nil), intentDiagnostics...)
	for _, probe := range probes {
		if probe.diagnostic != nil {
			diagnostics = append(diagnostics, *probe.diagnostic)
		}
		if !probe.valid {
			continue
		}
		if visibilityErr := s.validateVisibleTryPath(probe.live.CurrentPath); visibilityErr != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Path: probe.live.CurrentPath, Operation: "validate try location", Err: visibilityErr,
			})
			continue
		}
		validProbes = append(validProbes, probe)
	}

	items := make([]Item, 0, len(validProbes))
	lockErr := s.store.WithLock(ctx, func() error {
		entries, catalogProblems, listErr := s.store.ListWithDiagnostics()
		if listErr != nil {
			return listErr
		}
		diagnostics = append(diagnostics, catalogDiagnostics(catalogProblems)...)

		for _, probe := range validProbes {
			matches := matchingEntries(entries, s.host, probe)
			if len(matches) == 0 && len(catalogProblems) == 0 && remoteIdentityFromOrigin(probe.originURL) != "" {
				matched, matchErr := s.registry.Match(observationFromProbe(s.host, probe))
				switch {
				case matchErr == nil && eligibleTryRecord(matched):
					matches = []*catalog.Entry{matched}
				case matchErr == nil:
					// A graduated experiment at the same identity is history, not an
					// active Try to revive implicitly.
				case errors.Is(matchErr, catalog.ErrNotFound), errors.Is(matchErr, catalog.ErrAmbiguous):
					// A live clone must get its own identity when the remote hint is
					// absent or ambiguous.
				default:
					item := s.transientItem(probe, matchErr)
					items = append(items, item)
					diagnostics = append(diagnostics, Diagnostic{
						Path: probe.live.CurrentPath, Operation: "match catalog try", Err: matchErr,
					})
					continue
				}
			}
			switch len(matches) {
			case 0:
				if len(catalogProblems) > 0 {
					// The catalog diagnostic is reported once, while each live item carries
					// the same typed tracking error for callers that try to mutate it.
					items = append(items, s.transientItem(probe, incompleteCatalogError(catalogProblems)))
					continue
				}
				entry := s.newEntry(probe)
				if createErr := s.catalogCreate(entry); createErr != nil {
					item := s.transientItem(probe, createErr)
					items = append(items, item)
					diagnostics = append(diagnostics, Diagnostic{
						Path: probe.live.CurrentPath, Operation: "catalog try", Err: createErr,
					})
					continue
				}
				entries = append(entries, entry)
				items = append(items, itemFromEntry(entry, probe.live))
			case 1:
				entry := matches[0]
				updated, updateErr := s.refreshEntry(entry, probe)
				if updateErr != nil {
					item := itemFromEntry(entry, probe.live)
					item.CatalogError = updateErr
					items = append(items, item)
					diagnostics = append(diagnostics, Diagnostic{
						Path: probe.live.CurrentPath, Operation: "refresh catalog try", Err: updateErr,
					})
					continue
				}
				replaceEntry(entries, updated)
				items = append(items, itemFromEntry(updated, probe.live))
			default:
				matchedItems := make([]Item, len(matches))
				for index, entry := range matches {
					matchedItems[index] = itemFromEntry(entry, probe.live)
				}
				ambiguous := &AmbiguousError{Ref: probe.live.CurrentPath, Candidates: candidatesOf(matchedItems)}
				items = append(items, s.transientItem(probe, ambiguous))
				diagnostics = append(diagnostics, Diagnostic{
					Path: probe.live.CurrentPath, Operation: "match catalog try", Err: ambiguous,
				})
			}
		}
		return nil
	})

	items = deduplicateItems(items)
	sortItemsByName(items)
	sortDiagnostics(diagnostics)
	return items, diagnostics, lockErr
}

// List reconciles visible directories, joins requested catalog history, and
// enriches Git repositories with bounded status/last-commit probes.
func (s *Service) List(ctx context.Context, options ListOptions) ([]Item, []Diagnostic, error) {
	visible, diagnostics, err := s.Reconcile(ctx)
	if err != nil {
		return visible, diagnostics, err
	}

	entries, catalogProblems, err := s.store.ListWithDiagnostics()
	if err != nil {
		return visible, diagnostics, err
	}
	diagnostics = append(diagnostics, catalogDiagnostics(catalogProblems)...)

	items := make([]Item, 0, len(entries)+len(visible))
	seen := make(map[string]struct{}, len(visible))
	seenPaths := make(map[string]struct{}, len(visible))
	for _, item := range visible {
		if item.ID != "" {
			seen[item.ID] = struct{}{}
		}
		if key := pathKey(item.Live.CurrentPath); key != "" {
			seenPaths[key] = struct{}{}
		}
		includeLocation := true
		if item.Entry != nil {
			if location, ok := item.Entry.LocationFor(s.host); ok {
				includeLocation = includeLocationState(location.State, options)
			}
		}
		if includeExperimentPhase(item.Phase, options) && includeLocation {
			items = append(items, item)
		}
	}

	for _, entry := range entries {
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		if location, ok := entry.LocationFor(s.host); ok {
			if _, pathSeen := seenPaths[pathKey(location.CurrentPath)]; pathSeen {
				continue
			}
		}
		graduated := entry.Kind == catalog.KindRepository && entry.Experiment != nil &&
			entry.Experiment.Phase == catalog.PhaseGraduated
		if graduated {
			if !options.All && !options.IncludeGraduated {
				continue
			}
		} else if entry.Kind != catalog.KindTry || entry.Experiment == nil ||
			!includeExperimentPhase(entry.Experiment.Phase, options) {
			continue
		}

		location, located := entry.LocationFor(s.host)
		if !located {
			if !options.All && !options.IncludeNonPresent {
				continue
			}
		} else if !includeLocationState(location.State, options) {
			continue
		}
		present := located && location.State == catalog.LocationPresent && !s.hiddenStorage(location.CurrentPath)
		if present && !graduated && s.validateVisibleTryPath(location.CurrentPath) != nil {
			present = false
		}
		if present {
			isDirectory, statErr := directoryExists(location.CurrentPath)
			if statErr != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Path: location.CurrentPath, Operation: "stat catalog try", Err: statErr,
				})
				present = false
			} else {
				present = isDirectory
			}
		}
		if location.State == catalog.LocationPresent && !present &&
			!options.All && !options.IncludeNonPresent {
			continue
		}

		live := LiveFacts{Present: present}
		if located {
			live.CurrentPath = location.CurrentPath
			live.RealPath = location.RealPath
		}
		if present {
			probe := s.probeDirectory(ctx, location.CurrentPath)
			if probe.diagnostic != nil {
				diagnostics = append(diagnostics, *probe.diagnostic)
			}
			if probe.valid {
				live = probe.live
			}
		}
		items = append(items, itemFromEntry(entry, live))
	}

	if query := strings.ToLower(strings.TrimSpace(options.Query)); query != "" {
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.SearchText()), query) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if !options.SkipEnrichment {
		items = s.enrich(ctx, items)
	}
	sortItemsByActivity(items)
	diagnostics = deduplicateDiagnostics(diagnostics)
	sortDiagnostics(diagnostics)
	return items, diagnostics, nil
}

func includeExperimentPhase(phase catalog.ExperimentPhase, options ListOptions) bool {
	switch phase {
	case catalog.PhaseActive:
		return true
	case catalog.PhaseDeprecated:
		return options.All || options.IncludeDeprecated
	case catalog.PhaseGraduated:
		return options.All || options.IncludeGraduated
	default:
		return false
	}
}

func includeLocationState(state catalog.LocationState, options ListOptions) bool {
	switch state {
	case catalog.LocationPresent:
		return true
	case catalog.LocationArchived:
		return options.All || options.IncludeNonPresent || options.IncludeArchived
	case catalog.LocationEvicted:
		return options.All || options.IncludeNonPresent || options.IncludeEvicted
	default:
		return false
	}
}

// Resolve applies stable, deterministic matching: exact ID, exact basename,
// exact date-stripped name/slug, then one unique case-insensitive substring.
func (s *Service) Resolve(ctx context.Context, ref string) (Item, []Diagnostic, error) {
	return s.ResolveWithOptions(ctx, ref, ResolveOptions{})
}

// ResolveWithOptions uses the same deterministic matching while requiring each
// history class to be opted into explicitly.
func (s *Service) ResolveWithOptions(ctx context.Context, ref string, options ResolveOptions) (Item, []Diagnostic, error) {
	items, diagnostics, err := s.List(ctx, ListOptions{
		IncludeDeprecated: options.IncludeDeprecated,
		IncludeArchived:   options.IncludeArchived,
		IncludeEvicted:    options.IncludeEvicted,
		IncludeGraduated:  options.IncludeGraduated,
		SkipEnrichment:    true,
	})
	if err != nil {
		return Item{}, diagnostics, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Item{}, diagnostics, &NotFoundError{Ref: ref, Root: s.triesRoot}
	}

	if looksLikePath(ref) {
		pathRef := config.Expand(ref)
		if canonical, canonicalErr := pathx.Canonical(pathRef); canonicalErr == nil {
			var matches []Item
			for _, item := range items {
				if samePath(canonical, item.Live.CurrentPath) {
					matches = append(matches, item)
				}
			}
			if len(matches) > 0 {
				return s.selectResolved(ctx, ref, matches, diagnostics)
			}
		}
	}

	for _, match := range []func(Item) bool{
		func(item Item) bool { return item.ID != "" && item.ID == ref },
		func(item Item) bool { return item.Basename == ref },
		func(item Item) bool {
			stripped, _, _ := splitDatedBasename(item.Basename)
			return stripped == ref || item.Name == ref || item.Slug == ref
		},
	} {
		var matches []Item
		for _, item := range items {
			if match(item) {
				matches = append(matches, item)
			}
		}
		if len(matches) > 0 {
			return s.selectResolved(ctx, ref, matches, diagnostics)
		}
	}

	needle := strings.ToLower(ref)
	var matches []Item
	for _, item := range items {
		stripped, _, _ := splitDatedBasename(item.Basename)
		for _, value := range []string{item.Basename, stripped, item.Name, item.Slug} {
			if value != "" && strings.Contains(strings.ToLower(value), needle) {
				matches = append(matches, item)
				break
			}
		}
	}
	if len(matches) == 0 {
		return Item{}, diagnostics, &NotFoundError{Ref: ref, Root: s.triesRoot}
	}
	return s.selectResolved(ctx, ref, matches, diagnostics)
}

func (s *Service) selectResolved(ctx context.Context, ref string, matches []Item, diagnostics []Diagnostic) (Item, []Diagnostic, error) {
	if len(matches) == 1 {
		if matches[0].CatalogError != nil {
			return Item{}, diagnostics, matches[0].CatalogError
		}
		enriched := s.enrich(ctx, matches)
		return enriched[0], diagnostics, nil
	}
	return Item{}, diagnostics, &AmbiguousError{Ref: ref, Candidates: candidatesOf(matches)}
}

// ResolveOrCreate preserves the low-friction dev try behavior: an existing
// non-clone match wins; clone requests always get a collision-safe new path.
func (s *Service) ResolveOrCreate(ctx context.Context, request CreateRequest) (CreateResult, error) {
	if request.Clone == "" {
		name := config.Slug(strings.ReplaceAll(request.Name, " ", "-"))
		item, diagnostics, err := s.Resolve(ctx, name)
		if err == nil {
			result := CreateResult{
				Item: item, Path: item.Live.CurrentPath, Existing: true,
				Tracked: item.ID != "" && item.CatalogError == nil, Diagnostics: diagnostics,
			}
			if !result.Tracked {
				trackingErr := item.CatalogError
				if trackingErr == nil {
					trackingErr = errors.New("matching directory has no catalog ID")
				}
				return result, fmt.Errorf("existing try at %s is not tracked in the catalog: %w", result.Path, trackingErr)
			}
			return result, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return CreateResult{Diagnostics: diagnostics}, err
		}
	}
	return s.Create(ctx, request)
}

// Create makes a new collision-safe dated directory or clone and catalogs it
// only after the filesystem operation succeeds.
func (s *Service) Create(ctx context.Context, request CreateRequest) (CreateResult, error) {
	var result CreateResult
	if err := s.ensureTriesRoot(); err != nil {
		return result, err
	}
	name := request.Name
	if request.Clone != "" && strings.TrimSpace(name) == "" {
		name = cloneDefaultName(request.Clone)
	}
	slug := config.Slug(strings.ReplaceAll(name, " ", "-"))
	if err := pathx.ValidateComponent(slug); err != nil {
		return result, fmt.Errorf("try name %q: %w", name, err)
	}

	path, err := s.availableDatedPath(slug)
	if err != nil {
		return result, err
	}
	result.Path = path
	if request.Clone != "" {
		cloneRef, normalizeErr := s.normalizeCloneRef(request.Clone)
		if normalizeErr != nil {
			return result, normalizeErr
		}
		if _, err := s.gitRun(ctx, s.triesRoot, "clone", cloneRef, path); err != nil {
			return result, fmt.Errorf("clone try into %s: %w", path, err)
		}
		result.Created = true
		result.Cloned = true
	} else {
		if err := os.Mkdir(path, 0o755); err != nil {
			return result, fmt.Errorf("create try %s: %w", path, err)
		}
		result.Created = true
		if !request.NoGit {
			if _, err := s.gitRun(ctx, path, "init", "-b", "main"); err != nil {
				result.InitWarning = err
			}
		}
	}

	canonical, canonicalErr := pathx.Canonical(path)
	if canonicalErr != nil {
		return result, fmt.Errorf("try exists at %s but its canonical path could not be determined: %w", path, canonicalErr)
	}
	result.Path = canonical
	items, diagnostics, reconcileErr := s.Reconcile(ctx)
	result.Diagnostics = diagnostics
	if reconcileErr != nil {
		return result, fmt.Errorf("try exists at %s but catalog reconciliation failed: %w", canonical, reconcileErr)
	}
	for _, item := range items {
		if samePath(item.Live.CurrentPath, canonical) {
			result.Item = item
			result.Tracked = item.ID != "" && item.CatalogError == nil
			break
		}
	}
	if !result.Tracked {
		trackingErr := result.Item.CatalogError
		if trackingErr == nil {
			trackingErr = errors.New("reconciliation did not produce a catalog record")
		}
		return result, fmt.Errorf("try exists at %s but could not be added to the catalog: %w", canonical, trackingErr)
	}
	enriched := s.enrich(ctx, []Item{result.Item})
	if len(enriched) == 1 {
		result.Item = enriched[0]
	}
	return result, nil
}

// RefreshOrigin re-probes origin after an explicit external remote-creation
// step and persists its raw URL and normalized identity on the same catalog ID.
func (s *Service) RefreshOrigin(ctx context.Context, id string) (Item, error) {
	entry, err := s.registry.Get(id)
	if err != nil {
		return Item{}, fmt.Errorf("load experiment %s for origin refresh: %w", id, err)
	}
	if entry.Experiment == nil ||
		(entry.Kind != catalog.KindTry && !(entry.Kind == catalog.KindRepository && entry.Experiment.Phase == catalog.PhaseGraduated)) {
		return Item{}, &NotFoundError{Ref: id, Root: s.triesRoot}
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || location.State != catalog.LocationPresent || location.CurrentPath == "" {
		return Item{}, &NotFoundError{Ref: id, Root: s.triesRoot}
	}
	probe := s.probeDirectory(ctx, location.CurrentPath)
	if !probe.valid || probe.live.Repo == nil || probe.live.DiscoverError != nil {
		if probe.diagnostic != nil {
			return Item{}, fmt.Errorf("refresh experiment origin: %w", probe.diagnostic.Err)
		}
		return Item{}, errors.New("refresh experiment origin: path is not a discoverable Git repository")
	}
	if probe.originURL == "" {
		return Item{}, errors.New("refresh experiment origin: Git origin is not configured")
	}
	var updated *catalog.Entry
	err = s.store.WithLock(ctx, func() error {
		var updateErr error
		updated, updateErr = s.catalogUpdate(id, func(current *catalog.Entry) error {
			currentLocation, found := current.LocationFor(s.host)
			if !found || currentLocation.State != catalog.LocationPresent ||
				!samePath(currentLocation.CurrentPath, probe.live.CurrentPath) {
				return fmt.Errorf("catalog asset %s moved or was reassigned before origin refresh", id)
			}
			if current.MoveIntent != nil && current.MoveIntent.Host == s.host {
				return fmt.Errorf("catalog asset %s has a pending %s move", id, current.MoveIntent.Operation)
			}
			if current.Experiment == nil {
				return fmt.Errorf("catalog asset %s no longer has experiment provenance", id)
			}
			current.Experiment.OriginURL = probe.originURL
			current.RemoteIdentity = remoteIdentityFromOrigin(probe.originURL)
			return current.SetLocation(s.host, locationFromProbe(probe, currentLocation))
		})
		return updateErr
	})
	if err != nil {
		return Item{}, fmt.Errorf("refresh experiment origin: %w", err)
	}
	item := itemFromEntry(updated, probe.live)
	enriched := s.enrich(ctx, []Item{item})
	if len(enriched) == 1 {
		item = enriched[0]
	}
	return item, nil
}

// Touch updates LastOpened only after a caller has successfully handed off the
// selected OpenTarget.
func (s *Service) Touch(ctx context.Context, id string) (TouchResult, error) {
	if id == "" {
		return TouchResult{}, &NotFoundError{Ref: id, Root: s.triesRoot}
	}
	now := s.now()
	var updated *catalog.Entry
	err := s.store.WithLock(ctx, func() error {
		var updateErr error
		updated, updateErr = s.catalogUpdate(id, func(entry *catalog.Entry) error {
			if entry.Experiment == nil ||
				(entry.Kind != catalog.KindTry && !(entry.Kind == catalog.KindRepository && entry.Experiment.Phase == catalog.PhaseGraduated)) {
				return &NotFoundError{Ref: id, Root: s.triesRoot}
			}
			if entry.MoveIntent != nil && entry.MoveIntent.Host == s.host {
				return fmt.Errorf("try %s has a pending %s move", id, entry.MoveIntent.Operation)
			}
			location, ok := entry.LocationFor(s.host)
			if !ok || location.State != catalog.LocationPresent {
				return &NotFoundError{Ref: id, Root: s.triesRoot}
			}
			entry.LastOpened = now
			return nil
		})
		return updateErr
	})
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			return TouchResult{}, &NotFoundError{Ref: id, Root: s.triesRoot}
		}
		return TouchResult{}, fmt.Errorf("touch try %s: %w", id, err)
	}
	location, _ := updated.LocationFor(s.host)
	item := itemFromEntry(updated, LiveFacts{
		Present:     location.State == catalog.LocationPresent,
		CurrentPath: location.CurrentPath, RealPath: location.RealPath,
	})
	return TouchResult{Item: item, TouchedAt: now}, nil
}

func (s *Service) probeDirectory(ctx context.Context, path string) directoryProbe {
	probe := directoryProbe{}
	if err := ctx.Err(); err != nil {
		probe.diagnostic = &Diagnostic{Path: path, Operation: "inspect try", Err: err}
		return probe
	}
	info, err := os.Stat(path)
	if err != nil {
		probe.diagnostic = &Diagnostic{Path: path, Operation: "stat try", Err: err}
		return probe
	}
	if !info.IsDir() {
		return probe
	}
	canonical, err := pathx.Canonical(path)
	if err != nil {
		probe.diagnostic = &Diagnostic{Path: path, Operation: "canonicalize try", Err: err}
		return probe
	}
	probe.valid = true
	probe.live = LiveFacts{Present: true, CurrentPath: canonical, RealPath: canonical}
	_, markerErr := os.Lstat(filepath.Join(canonical, ".git"))
	probe.gitMarker = markerErr == nil || !errors.Is(markerErr, fs.ErrNotExist)

	repository, discoverErr := s.gitDiscover(ctx, canonical)
	if discoverErr != nil {
		if probe.gitMarker || !errors.Is(discoverErr, gitx.ErrNotARepo) {
			probe.live.DiscoverError = discoverErr
			probe.diagnostic = &Diagnostic{Path: canonical, Operation: "discover Git try", Err: discoverErr}
		}
		return probe
	}
	repositoryRoot := repository.Root
	if repository.Bare {
		repositoryRoot = repository.MainRoot
	}
	canonicalRoot, rootErr := pathx.Canonical(repositoryRoot)
	if rootErr != nil || !samePath(canonicalRoot, canonical) {
		// A TriesRoot may itself be inside a repository. Its child directories are
		// not independent Git Tries merely because rev-parse walks to that parent.
		return probe
	}
	repository.Root = canonicalRoot
	if repository.MainRoot != "" {
		if canonicalMain, mainErr := pathx.Canonical(repository.MainRoot); mainErr == nil {
			repository.MainRoot = canonicalMain
		}
	}
	if repository.GitCommonDir != "" {
		commonDir, commonErr := pathx.Canonical(repository.GitCommonDir)
		if commonErr != nil {
			probe.live.DiscoverError = commonErr
			probe.diagnostic = &Diagnostic{Path: canonical, Operation: "canonicalize Git common directory", Err: commonErr}
			repository.GitCommonDir = ""
		} else {
			repository.GitCommonDir = commonDir
		}
	}
	probe.live.Repo = &repository
	probe.originURL = gitx.RemoteFromConfig(repository.GitCommonDir, "origin")
	return probe
}

func (s *Service) enrich(ctx context.Context, items []Item) []Item {
	return boundedMap(ctx, s.workers, items, func(ctx context.Context, item Item) Item {
		if item.Live.Repo == nil || item.Live.CurrentPath == "" {
			return item
		}
		status, statusErr := s.gitStatus(ctx, item.Live.CurrentPath)
		if statusErr != nil {
			item.Live.StatusError = statusErr
		} else {
			item.Live.Status = &status
		}
		unix, subject, commitErr := s.gitLastCommit(ctx, item.Live.CurrentPath)
		if commitErr != nil {
			item.Live.LastCommitError = commitErr
		} else if unix > 0 {
			item.Live.LastCommit = time.Unix(unix, 0).UTC()
			item.Live.LastCommitSubject = subject
		}
		return item
	})
}

func (s *Service) experimentFromProbe(probe directoryProbe) *catalog.Experiment {
	name, started, dated := splitDatedBasename(filepath.Base(probe.live.CurrentPath))
	if strings.TrimSpace(name) == "" {
		name = "unnamed"
	} else {
		name = strings.TrimSpace(name)
	}
	if !dated {
		started = s.now()
	}
	return &catalog.Experiment{
		Phase: catalog.PhaseActive, Slug: config.Slug(name),
		OriginURL: probe.originURL, Started: started,
		OriginalPath: probe.live.CurrentPath,
	}
}

func (s *Service) newEntry(probe directoryProbe) *catalog.Entry {
	name, started, dated := splitDatedBasename(filepath.Base(probe.live.CurrentPath))
	if strings.TrimSpace(name) == "" {
		name = "unnamed"
	} else {
		name = strings.TrimSpace(name)
	}
	if !dated {
		started = s.now()
	}
	entry := &catalog.Entry{
		Kind:           catalog.KindTry,
		Name:           name,
		RemoteIdentity: remoteIdentityFromOrigin(probe.originURL),
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive, Slug: config.Slug(name),
			OriginURL: probe.originURL, Started: started,
			OriginalPath: probe.live.CurrentPath,
		},
		Locations: map[string]catalog.Location{
			s.host: locationFromProbe(probe, catalog.Location{}),
		},
	}
	entry.Created = started
	return entry
}

func (s *Service) transientItem(probe directoryProbe, catalogErr error) Item {
	name, started, dated := splitDatedBasename(filepath.Base(probe.live.CurrentPath))
	if strings.TrimSpace(name) == "" {
		name = "unnamed"
	} else {
		name = strings.TrimSpace(name)
	}
	if !dated {
		started = s.now()
	}
	return Item{
		Kind: catalog.KindTry, Name: name,
		Basename: filepath.Base(probe.live.CurrentPath), Slug: config.Slug(name),
		Phase: catalog.PhaseActive, Created: started, Started: started,
		OriginURL: probe.originURL, RemoteIdentity: remoteIdentityFromOrigin(probe.originURL),
		Live: probe.live, CatalogError: catalogErr,
	}
}

func (s *Service) refreshEntry(entry *catalog.Entry, probe directoryProbe) (*catalog.Entry, error) {
	if entry.Kind == catalog.KindRepository && entry.Experiment != nil &&
		entry.Experiment.Phase == catalog.PhaseGraduated {
		// Exact host path/common-dir ownership keeps graduated history attached,
		// but scanning a tries root must never turn it back into an active Try.
		return entry, nil
	}
	if entry.MoveIntent != nil && entry.MoveIntent.Host == s.host {
		return nil, fmt.Errorf("catalog asset %s has a pending %s move", entry.ID, entry.MoveIntent.Operation)
	}
	location, located := entry.LocationFor(s.host)
	if located && location.State != catalog.LocationPresent {
		return nil, fmt.Errorf("catalog asset %s is %s on this host; use an explicit lifecycle transition", entry.ID, location.State)
	}
	wantedLocation := locationFromProbe(probe, location)
	if entry.Kind == catalog.KindRepository && entry.Experiment == nil {
		metadata := s.experimentFromProbe(probe)
		return s.catalogUpdate(entry.ID, func(updated *catalog.Entry) error {
			if updated.Kind != catalog.KindRepository || updated.Experiment != nil {
				return fmt.Errorf("catalog asset %s changed before it could become a Try", updated.ID)
			}
			updated.Kind = catalog.KindTry
			updated.Experiment = metadata
			if probe.originURL != "" {
				updated.RemoteIdentity = remoteIdentityFromOrigin(probe.originURL)
			}
			return updated.SetLocation(s.host, wantedLocation)
		})
	}

	wantedOrigin := entry.Experiment.OriginURL
	wantedIdentity := entry.RemoteIdentity
	if probe.originURL != "" {
		wantedOrigin = probe.originURL
		wantedIdentity = remoteIdentityFromOrigin(probe.originURL)
	}
	if locationEqual(location, wantedLocation) && entry.Experiment.OriginURL == wantedOrigin &&
		entry.RemoteIdentity == wantedIdentity {
		return entry, nil
	}
	return s.catalogUpdate(entry.ID, func(updated *catalog.Entry) error {
		if updated.Kind != catalog.KindTry || updated.Experiment == nil || updated.Experiment.Phase == catalog.PhaseGraduated {
			return fmt.Errorf("catalog asset %s is not an active Try", updated.ID)
		}
		if probe.originURL != "" {
			updated.Experiment.OriginURL = probe.originURL
			updated.RemoteIdentity = remoteIdentityFromOrigin(probe.originURL)
		}
		return updated.SetLocation(s.host, wantedLocation)
	})
}

func locationFromProbe(probe directoryProbe, previous catalog.Location) catalog.Location {
	commonDir := ""
	if probe.live.Repo != nil && probe.live.DiscoverError == nil {
		commonDir = probe.live.Repo.GitCommonDir
	} else if probe.gitMarker && probe.live.DiscoverError != nil {
		// A transient Git failure must not erase identity gathered earlier.
		commonDir = previous.GitCommonDir
	}
	return catalog.Location{
		State:        catalog.LocationPresent,
		CurrentPath:  probe.live.CurrentPath,
		RealPath:     probe.live.RealPath,
		GitCommonDir: commonDir,
	}
}

func locationEqual(left, right catalog.Location) bool {
	left.Updated = time.Time{}
	right.Updated = time.Time{}
	return left == right
}

func eligibleTryRecord(entry *catalog.Entry) bool {
	return entry != nil && entry.Kind == catalog.KindTry && entry.Experiment != nil &&
		entry.Experiment.Phase != catalog.PhaseGraduated
}

func eligibleLocalRecord(entry *catalog.Entry) bool {
	return eligibleTryRecord(entry) ||
		(entry != nil && entry.Kind == catalog.KindRepository &&
			(entry.Experiment == nil || entry.Experiment.Phase == catalog.PhaseGraduated))
}

func remoteIdentityFromOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if strings.Contains(origin, "://") {
		return catalog.NormalizeRemoteIdentity(origin)
	}
	colon := strings.IndexByte(origin, ':')
	if colon > 0 && !strings.ContainsAny(origin[:colon], `/\\`) {
		return catalog.NormalizeRemoteIdentity(origin)
	}
	return ""
}

func observationFromProbe(host string, probe directoryProbe) catalog.Observation {
	observation := catalog.Observation{
		Host: host, Path: probe.live.CurrentPath, RealPath: probe.live.RealPath,
		Name: filepath.Base(probe.live.CurrentPath), RemoteIdentity: remoteIdentityFromOrigin(probe.originURL),
	}
	if probe.live.Repo != nil {
		observation.CommonDir = probe.live.Repo.GitCommonDir
	}
	return observation
}

func matchingEntries(entries []*catalog.Entry, host string, probe directoryProbe) []*catalog.Entry {
	matched := make(map[string]*catalog.Entry)
	for _, entry := range entries {
		if !eligibleLocalRecord(entry) {
			continue
		}
		location, located := entry.LocationFor(host)
		if located {
			pathMatch := samePath(probe.live.CurrentPath, location.CurrentPath) ||
				samePath(probe.live.CurrentPath, location.RealPath) ||
				samePath(probe.live.CurrentPath, location.RestorePath)
			commonMatch := probe.live.Repo != nil && probe.live.Repo.GitCommonDir != "" &&
				samePath(probe.live.Repo.GitCommonDir, location.GitCommonDir)
			if pathMatch || commonMatch {
				matched[entry.ID] = entry
			}
		} else if entry.Experiment != nil && samePath(probe.live.CurrentPath, entry.Experiment.OriginalPath) {
			matched[entry.ID] = entry
		}
	}
	out := make([]*catalog.Entry, 0, len(matched))
	for _, entry := range matched {
		out = append(out, entry)
	}
	catalog.Sort(out)
	return out
}

func incompleteCatalogError(diagnostics []catalog.Diagnostic) error {
	joined := make([]error, 1, len(diagnostics)+1)
	joined[0] = catalog.ErrIncompleteCatalog
	for _, diagnostic := range diagnostics {
		joined = append(joined, diagnostic)
	}
	return errors.Join(joined...)
}

func replaceEntry(entries []*catalog.Entry, replacement *catalog.Entry) {
	for index, entry := range entries {
		if entry.ID == replacement.ID {
			entries[index] = replacement
			return
		}
	}
}

func splitDatedBasename(basename string) (string, time.Time, bool) {
	if len(basename) <= len("2006-01-02-") || basename[10] != '-' {
		return basename, time.Time{}, false
	}
	parsed, err := time.Parse("2006-01-02", basename[:10])
	if err != nil || parsed.Format("2006-01-02") != basename[:10] {
		return basename, time.Time{}, false
	}
	name := basename[11:]
	if name == "" {
		return basename, time.Time{}, false
	}
	return name, parsed, true
}

func (s *Service) availableDatedPath(slug string) (string, error) {
	prefix := s.clock().Format("2006-01-02") + "-" + slug
	for version := 1; version < 10_000; version++ {
		basename := prefix
		if version > 1 {
			basename = fmt.Sprintf("%s-%d", prefix, version)
		}
		intended := filepath.Join(s.triesRoot, basename)
		if _, err := os.Lstat(intended); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect try destination %s: %w", intended, err)
		}
		candidate, err := pathx.JoinChild(s.triesRoot, basename)
		if err != nil {
			return "", fmt.Errorf("choose try destination: %w", err)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect try destination %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("choose a unique destination for %q", slug)
}

func cloneDefaultName(ref string) string {
	if localClonePath(ref) {
		trimmed := strings.TrimSuffix(strings.TrimSuffix(ref, string(filepath.Separator)), ".git")
		return config.Slug(filepath.Base(trimmed))
	}
	if _, identity := forge.IdentityFromURL(ref); identity != "" {
		return config.Slug(strings.ReplaceAll(identity, "/", "-"))
	}
	identity := clonePathIdentity(ref)
	if identity != "" {
		return config.Slug(strings.ReplaceAll(identity, "/", "-"))
	}
	trimmed := strings.TrimSuffix(strings.TrimSuffix(ref, "/"), ".git")
	if index := strings.LastIndexAny(trimmed, "/:"); index >= 0 {
		trimmed = trimmed[index+1:]
	}
	return config.Slug(trimmed)
}

func (s *Service) normalizeCloneRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("clone reference is required")
	}
	if local, ok, err := s.resolveLocalCloneRef(ref); ok || err != nil {
		return local, err
	}
	// SCP-style remotes are already complete clone references. In particular,
	// nonstandard SSH usernames must not be rewritten as preferred-forge shorthand.
	if !strings.Contains(ref, "://") && strings.ContainsAny(ref, "@:") {
		return ref, nil
	}
	kind := forge.FromURL(ref)
	if kind != forge.Unknown {
		adapter, err := forge.For(kind)
		if err == nil {
			return adapter.CloneURL(ref), nil
		}
		return ref, nil
	}
	if !strings.Contains(ref, "://") && strings.Contains(ref, "/") {
		if adapter, err := forge.Preferred(); err == nil {
			return adapter.CloneURL(ref), nil
		}
	}
	return ref, nil
}

func (s *Service) resolveLocalCloneRef(ref string) (string, bool, error) {
	if strings.HasPrefix(ref, "file://") {
		return ref, true, nil
	}
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref), true, nil
	}
	if ref == "~" || strings.HasPrefix(ref, "~/") {
		return config.Expand(ref), true, nil
	}

	explicit := strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../")
	cwd, err := s.getwd()
	if err != nil {
		if explicit {
			return "", true, fmt.Errorf("resolve local clone reference %q: %w", ref, err)
		}
		return "", false, nil
	}
	candidate := filepath.Clean(filepath.Join(cwd, ref))
	if explicit {
		return candidate, true, nil
	}
	if strings.ContainsAny(ref, "@:") {
		return "", false, nil
	}
	if _, err := os.Stat(candidate); err == nil {
		return candidate, true, nil
	}
	return "", false, nil
}

func localClonePath(ref string) bool {
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "file://") || strings.HasPrefix(ref, "./") ||
		strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "~/") {
		return true
	}
	_, err := os.Stat(ref)
	return err == nil
}

func clonePathIdentity(ref string) string {
	if parsed, err := url.Parse(ref); err == nil && parsed.Host != "" {
		return strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
	}
	if at := strings.LastIndexByte(ref, '@'); at >= 0 {
		ref = ref[at+1:]
	}
	if colon := strings.IndexByte(ref, ':'); colon >= 0 {
		ref = ref[colon+1:]
	}
	ref = strings.Trim(strings.TrimSuffix(ref, ".git"), "/")
	if strings.Contains(ref, "/") {
		return ref
	}
	return ""
}

func (s *Service) validateVisibleTryPath(path string) error {
	canonicalRoot, err := pathx.Canonical(s.triesRoot)
	if err != nil {
		return fmt.Errorf("canonicalize tries root: %w", err)
	}
	canonicalPath, err := pathx.CanonicalChild(canonicalRoot, path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil {
		return fmt.Errorf("locate try under root: %w", err)
	}
	if strings.Contains(relative, string(filepath.Separator)) || strings.HasPrefix(relative, ".") {
		return fmt.Errorf("%s is not an immediate visible child of %s", canonicalPath, canonicalRoot)
	}
	return nil
}

func (s *Service) hiddenStorage(path string) bool {
	if path == "" {
		return false
	}
	if hiddenImmediateChild(s.triesRoot, path) {
		return true
	}
	canonicalRoot, rootErr := pathx.Canonical(s.triesRoot)
	canonicalPath, pathErr := pathx.Canonical(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	return hiddenImmediateChild(canonicalRoot, canonicalPath)
}

func hiddenImmediateChild(root, path string) bool {
	absoluteRoot, rootErr := filepath.Abs(root)
	absolutePath, pathErr := filepath.Abs(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(absoluteRoot), filepath.Clean(absolutePath))
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	first, _, _ := strings.Cut(relative, string(filepath.Separator))
	return strings.HasPrefix(first, ".")
}

func looksLikePath(ref string) bool {
	return filepath.IsAbs(ref) || strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "~") ||
		strings.ContainsAny(ref, `/\\`)
}

func pathKey(path string) string {
	if path == "" {
		return ""
	}
	if canonical, err := pathx.Canonical(path); err == nil {
		return canonical
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftCanonical, leftErr := pathx.Canonical(left)
	rightCanonical, rightErr := pathx.Canonical(right)
	if leftErr == nil && rightErr == nil {
		return leftCanonical == rightCanonical
	}
	leftAbsolute, leftAbsErr := filepath.Abs(left)
	rightAbsolute, rightAbsErr := filepath.Abs(right)
	return leftAbsErr == nil && rightAbsErr == nil && filepath.Clean(leftAbsolute) == filepath.Clean(rightAbsolute)
}

func sortItemsByName(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].DisplayName()), strings.ToLower(items[j].DisplayName())
		if left != right {
			return left < right
		}
		if items[i].DisplayName() != items[j].DisplayName() {
			return items[i].DisplayName() < items[j].DisplayName()
		}
		return items[i].ID < items[j].ID
	})
}

func sortItemsByActivity(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].Activity(), items[j].Activity()
		if !left.Equal(right) {
			if left.IsZero() {
				return false
			}
			if right.IsZero() {
				return true
			}
			return left.After(right)
		}
		leftName, rightName := strings.ToLower(items[i].DisplayName()), strings.ToLower(items[j].DisplayName())
		if leftName != rightName {
			return leftName < rightName
		}
		return items[i].ID < items[j].ID
	})
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		return diagnostics[i].Operation < diagnostics[j].Operation
	})
}

func deduplicateItems(items []Item) []Item {
	seenIDs := make(map[string]struct{}, len(items))
	seenPaths := make(map[string]struct{}, len(items))
	out := items[:0]
	for _, item := range items {
		if item.ID != "" {
			if _, ok := seenIDs[item.ID]; ok {
				continue
			}
			seenIDs[item.ID] = struct{}{}
		} else if item.Live.CurrentPath != "" {
			if _, ok := seenPaths[item.Live.CurrentPath]; ok {
				continue
			}
			seenPaths[item.Live.CurrentPath] = struct{}{}
		}
		out = append(out, item)
	}
	return out
}

func deduplicateDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	seen := make(map[string]struct{}, len(diagnostics))
	out := diagnostics[:0]
	for _, diagnostic := range diagnostics {
		key := diagnostic.Path + "\x00" + diagnostic.Operation + "\x00" + fmt.Sprint(diagnostic.Err)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, diagnostic)
	}
	return out
}

func boundedMap[Input, Output any](ctx context.Context, limit int, input []Input, work func(context.Context, Input) Output) []Output {
	if len(input) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(input) {
		limit = len(input)
	}
	type job struct {
		index int
		value Input
	}
	type result struct {
		index int
		value Output
	}
	jobs := make(chan job, len(input))
	results := make(chan result, len(input))
	for index, value := range input {
		jobs <- job{index: index, value: value}
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(limit)
	for range limit {
		go func() {
			defer workers.Done()
			for next := range jobs {
				results <- result{index: next.index, value: work(ctx, next.value)}
			}
		}()
	}
	workers.Wait()
	close(results)

	out := make([]Output, len(input))
	for result := range results {
		out[result.index] = result.value
	}
	return out
}
