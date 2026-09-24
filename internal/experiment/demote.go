package experiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// DemoteRequest selects a previously graduated experiment. To is an immediate
// visible child of tries_root, either absolute or a basename. Without To, the
// original Try path must still be safe and unoccupied. Expected pins a UI row.
type DemoteRequest struct {
	Ref      string
	To       string
	DryRun   bool
	Expected *catalog.Entry
}

// DemoteGuardRequest identifies the exact checkout whose external claims must
// be observed. The guard must reject unknown or occupied authority and return
// a stable fingerprint of the complete task/runtime/artifact/harness evidence.
type DemoteGuardRequest struct {
	Source         string
	Destination    string
	GitCommonDir   string
	LinkedWorktree bool
}

// DemotePlan is a reviewed, immutable move. Public fields are for rendering;
// modifying them invalidates its private seal. No Git history is undone.
type DemotePlan struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	Destination    string `json:"destination"`
	GitCommonDir   string `json:"git_common_dir"`
	LinkedWorktree bool   `json:"linked_worktree"`
	DryRun         bool   `json:"dry_run"`
	host           string
	triesRoot      string
	entry          *catalog.Entry
	identity       string
	gitIdentity    string
	gitDirIdentity string
	gitState       string
	parentIdentity string
	tree           string
	guard          string
	seal           string
}

func (p DemotePlan) fingerprint() string {
	return removalDigest([]any{p.ID, p.Source, p.Destination, p.GitCommonDir,
		p.LinkedWorktree, p.DryRun, p.host, p.triesRoot, p.entry, p.identity, p.gitIdentity, p.gitDirIdentity, p.gitState,
		p.parentIdentity, p.tree, p.guard})
}

func (p DemotePlan) guardRequest() DemoteGuardRequest {
	return DemoteGuardRequest{Source: p.Source, Destination: p.Destination,
		GitCommonDir: p.GitCommonDir, LinkedWorktree: p.LinkedWorktree}
}

func graduatedEntry(entry *catalog.Entry) bool {
	return entry != nil && entry.Kind == catalog.KindRepository && entry.Experiment != nil &&
		entry.Experiment.Phase == catalog.PhaseGraduated
}

// PlanDemote is read-only apart from acquiring the caller's lifecycle locks.
// It never reconciles inventory or creates catalog records. The injected lock
// acquires repository then task-store authority; catalog locks come inside it.
func (s *Service) PlanDemote(ctx context.Context, request DemoteRequest) (DemotePlan, error) {
	ctx = gitx.WithReadOnlyObservations(ctx)
	var plan DemotePlan
	if s.demoteLock == nil || s.demoteGuard == nil {
		return plan, errors.New("demotion requires lifecycle locking and a task/runtime/artifact observation guard")
	}
	entry, err := s.resolveDemoteEntry(request.Ref)
	if err != nil {
		return plan, err
	}
	if request.Expected != nil && !sameRemovalEntry(entry, request.Expected) {
		return plan, errors.New("selected repository changed; reload before demotion")
	}
	location, _ := entry.LocationFor(s.host)
	probe, err := s.inspectMovableSource(ctx, location.CurrentPath)
	if err != nil {
		return plan, err
	}
	if probe.live.Repo == nil {
		return plan, errors.New("graduated repository is no longer a Git checkout")
	}
	common := probe.live.Repo.GitCommonDir
	err = s.demoteLock(ctx, common, func() error {
		fresh, getErr := s.store.Get(entry.ID)
		if getErr != nil {
			return getErr
		}
		if !sameRemovalEntry(fresh, entry) {
			return errors.New("repository changed while acquiring demotion locks")
		}
		plan, getErr = s.prepareDemote(ctx, fresh, request.To, request.DryRun)
		if getErr == nil && plan.GitCommonDir != common {
			return errors.New("Git common directory changed while acquiring demotion locks")
		}
		return getErr
	})
	return plan, err
}

// ApplyDemote applies only the exact reviewed plan. Current contents, Git and
// filesystem identities, catalog state and external authority are checked again
// under the same lock ordering, including immediately before the move.
func (s *Service) ApplyDemote(ctx context.Context, plan DemotePlan) (TransitionResult, error) {
	ctx = gitx.WithReadOnlyObservations(ctx)
	result := TransitionResult{Plan: TransitionPlan{Operation: TransitionDemote,
		Source: plan.Source, Destination: plan.Destination, LinkedWorktree: plan.LinkedWorktree}}
	if plan.seal == "" || plan.fingerprint() != plan.seal {
		return result, errors.New("invalid or modified demotion plan")
	}
	if plan.host != s.host || plan.triesRoot != s.triesRoot {
		return result, errors.New("demotion plan belongs to a different host or tries_root")
	}
	if plan.DryRun {
		return result, nil
	}
	if s.demoteLock == nil || s.demoteGuard == nil {
		return result, errors.New("demotion requires lifecycle locking and an observation guard")
	}
	err := s.demoteLock(ctx, plan.GitCommonDir, func() error {
		fresh, err := s.store.Get(plan.ID)
		if err != nil {
			return err
		}
		if !sameRemovalEntry(fresh, plan.entry) {
			return errors.New("stale demotion plan: catalog record changed")
		}
		observed, err := s.prepareDemote(ctx, fresh, plan.Destination, false)
		if err != nil {
			return err
		}
		if observed.fingerprint() != plan.seal {
			return fmt.Errorf("stale demotion plan: %s changed", demoteDifference(plan, observed))
		}
		result.Item = itemFromEntry(plan.entry.Clone(), LiveFacts{Present: true, CurrentPath: plan.Source, RealPath: plan.Source})
		result.Plan.Item = result.Item
		result.Plan.demote = &plan
		result, err = s.applyTransition(ctx, result)
		return err
	})
	return result, err
}

func (s *Service) prepareDemote(ctx context.Context, entry *catalog.Entry, to string, dryRun bool) (DemotePlan, error) {
	ctx = gitx.WithReadOnlyObservations(ctx)
	plan := DemotePlan{DryRun: dryRun, host: s.host, triesRoot: s.triesRoot}
	if !graduatedEntry(entry) || entry.MoveIntent != nil || entry.RecoveryReceipt != nil {
		return plan, errors.New("demotion requires a previously graduated Try without a pending operation")
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || location.State != catalog.LocationPresent || location.CurrentPath == "" {
		return plan, errors.New("graduated Try is not present on this host")
	}
	source, err := pathx.Canonical(location.CurrentPath)
	if err != nil {
		return plan, err
	}
	if source != filepath.Clean(location.CurrentPath) {
		return plan, errors.New("demotion source must be its current canonical directory, not a symlink")
	}
	plan.ID, plan.Source, plan.entry = entry.ID, source, entry.Clone()
	if to == "" {
		to = entry.Experiment.OriginalPath
		if err := s.validateVisibleTryPath(to); err != nil {
			return plan, fmt.Errorf("original Try path is not available under the current tries_root; pass --to: %w", err)
		}
	}
	if strings.HasPrefix(to, "~") || filepath.IsAbs(to) {
		to = config.Expand(to)
	}
	intended := to
	if !filepath.IsAbs(intended) {
		intended = filepath.Join(s.triesRoot, intended)
	}
	// A dangling symlink also occupies the selected spelling. Canonicalizing
	// first could otherwise publish into its missing target and retain the link.
	if err := rejectExisting(intended); err != nil {
		return plan, fmt.Errorf("demotion destination is occupied; pass --to for another Try path: %w", err)
	}
	destination, err := s.explicitRestoreDestination(to)
	if err != nil {
		return plan, err
	}
	plan.Destination = destination
	if err := s.validateTransitionContainment(entry.ID, TransitionDemote, source, destination); err != nil {
		return plan, err
	}
	if err := rejectExisting(destination); err != nil {
		return plan, fmt.Errorf("demotion destination is occupied; pass --to for another Try path: %w", err)
	}
	wd, err := s.getwd()
	if err != nil {
		return plan, err
	}
	if inside, err := pathx.Contains(source, wd); err != nil || inside {
		return plan, errors.New("leave the repository directory before demoting it")
	}
	if err := s.demoteCatalogClaims(entry, source, destination); err != nil {
		return plan, err
	}
	probe, err := s.inspectDemoteSource(ctx, source)
	if err != nil {
		return plan, err
	}
	plan.GitCommonDir = probe.live.Repo.GitCommonDir
	plan.LinkedWorktree = probe.live.Repo.IsLinkedWorktree
	plan.gitState = probe.demoteAuthority
	if location.GitCommonDir == "" || !samePath(location.GitCommonDir, plan.GitCommonDir) {
		return plan, errors.New("graduated repository Git identity differs from its catalog location")
	}
	plan.identity, _, err = removalIdentity(source)
	if err != nil {
		return plan, err
	}
	plan.gitIdentity, _, err = removalIdentity(plan.GitCommonDir)
	if err != nil {
		return plan, err
	}
	plan.gitDirIdentity, _, err = removalIdentity(probe.live.Repo.GitDir)
	if err != nil {
		return plan, err
	}
	plan.parentIdentity, _, err = removalIdentity(filepath.Dir(destination))
	if err != nil {
		return plan, fmt.Errorf("inspect existing tries_root: %w", err)
	}
	same, err := s.sameFilesystem(source, destination)
	if err != nil {
		return plan, err
	}
	if !same {
		return plan, ErrCrossFilesystem
	}
	plan.guard, err = s.demoteGuard(ctx, plan.guardRequest())
	if err != nil {
		return plan, err
	}
	if plan.guard == "" {
		return plan, errors.New("demotion observation guard returned no authority")
	}
	plan.tree, err = demoteTree(ctx, source)
	if err != nil {
		return plan, err
	}
	plan.seal = plan.fingerprint()
	return plan, nil
}

// resolveDemoteEntry deliberately uses the catalog read boundary, not List's
// compatibility reconciliation path. Only graduated records participate.
func (s *Service) resolveDemoteEntry(ref string) (*catalog.Entry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("a graduated Try name, catalog ID or path is required")
	}
	entries, diagnostics, err := s.store.ListWithDiagnostics()
	if err != nil {
		return nil, err
	}
	if len(diagnostics) != 0 {
		return nil, incompleteCatalogError(diagnostics)
	}
	canonicalRef := ""
	if looksLikePath(ref) {
		canonicalRef, err = pathx.Canonical(config.Expand(ref))
		if err != nil {
			return nil, err
		}
	}
	for _, match := range []func(*catalog.Entry, catalog.Location) bool{
		func(e *catalog.Entry, l catalog.Location) bool { return e.ID == ref },
		func(e *catalog.Entry, l catalog.Location) bool {
			return canonicalRef != "" && samePath(l.CurrentPath, canonicalRef)
		},
		func(e *catalog.Entry, l catalog.Location) bool {
			return e.Name == ref || filepath.Base(l.CurrentPath) == ref || e.Experiment.Slug == ref
		},
		func(e *catalog.Entry, l catalog.Location) bool {
			return canonicalRef == "" && strings.Contains(strings.ToLower(e.Name+" "+filepath.Base(l.CurrentPath)+" "+e.Experiment.Slug), strings.ToLower(ref))
		},
	} {
		var matches []*catalog.Entry
		var candidates []Candidate
		for _, entry := range entries {
			if !graduatedEntry(entry) {
				continue
			}
			location, ok := entry.LocationFor(s.host)
			if !ok || location.State != catalog.LocationPresent || !match(entry, location) {
				continue
			}
			matches = append(matches, entry)
			candidates = append(candidates, Candidate{ID: entry.ID, Name: entry.Name, Path: location.CurrentPath, Basename: filepath.Base(location.CurrentPath)})
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return nil, &AmbiguousError{Ref: ref, Candidates: candidates}
		}
	}
	return nil, fmt.Errorf("%w: %q is not a present previously graduated Try", ErrNotFound, ref)
}

func (s *Service) demoteCatalogClaims(selected *catalog.Entry, source, destination string) error {
	entries, problems, err := s.store.ListWithDiagnostics()
	if err != nil {
		return err
	}
	if len(problems) != 0 {
		return incompleteCatalogError(problems)
	}
	for _, other := range entries {
		if other.ID == selected.ID {
			continue
		}
		if location, ok := other.LocationFor(s.host); ok {
			for _, path := range []string{location.CurrentPath, location.RealPath, location.GitCommonDir, location.RestorePath} {
				if pathsRelated(source, path) || pathsRelated(destination, path) {
					return fmt.Errorf("catalog asset %s claims a demotion path", other.ID)
				}
			}
		}
		if intent := other.MoveIntent; intent != nil && intent.Host == s.host {
			for _, path := range []string{intent.SourcePath, intent.DestinationPath} {
				if pathsRelated(source, path) || pathsRelated(destination, path) {
					return fmt.Errorf("catalog move %s claims a demotion path", other.ID)
				}
			}
		}
	}
	if inside, err := pathx.Contains(source, s.store.Dir); err != nil || inside {
		return errors.New("repository contains durable dev catalog state")
	}
	return nil
}

func (s *Service) inspectDemoteSource(ctx context.Context, source string) (directoryProbe, error) {
	probe, err := s.inspectMovableSource(ctx, source)
	if err != nil {
		return probe, err
	}
	repository := probe.live.Repo
	if repository == nil || repository.Bare {
		return probe, errors.New("demotion requires a non-bare Git checkout")
	}
	if !repository.IsLinkedWorktree && !samePath(repository.GitCommonDir, filepath.Join(source, ".git")) {
		return probe, errors.New("external Git storage or a gitlink checkout cannot be demoted")
	}
	if !repository.IsLinkedWorktree {
		alternates := filepath.Join(repository.GitCommonDir, "objects", "info", "alternates")
		if body, err := os.ReadFile(alternates); err == nil && strings.TrimSpace(string(body)) != "" {
			return probe, errors.New("shared Git object alternates block demotion")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return probe, err
		}
	}
	configuration, err := s.gitRun(ctx, source, "config", "--null", "--list")
	if err != nil {
		return probe, fmt.Errorf("inspect demotion Git configuration: %w", err)
	}
	for _, setting := range strings.Split(configuration, "\x00") {
		if value, ok := strings.CutPrefix(setting, "core.worktree\n"); ok && strings.TrimSpace(value) != "" {
			return probe, errors.New("configured core.worktree requires explicit relocation and blocks demotion")
		}
	}
	superproject, err := s.gitRun(ctx, source, "rev-parse", "--show-superproject-working-tree")
	if err != nil || strings.TrimSpace(superproject) != "" {
		return probe, errors.New("gitlink checkout or unavailable superproject identity blocks demotion")
	}
	index, err := s.gitRun(ctx, source, "ls-files", "--stage", "-z")
	if err != nil {
		return probe, fmt.Errorf("inspect demotion gitlinks: %w", err)
	}
	for _, record := range strings.Split(index, "\x00") {
		if strings.HasPrefix(record, "160000 ") {
			return probe, errors.New("repositories containing gitlinks cannot be demoted")
		}
	}
	worktrees, err := s.worktrees(ctx, source)
	if err != nil {
		return probe, err
	}
	matches := 0
	var selected gitx.Worktree
	for _, worktree := range worktrees {
		if !samePath(worktree.Path, source) {
			continue
		}
		matches++
		selected = worktree
		if worktree.Locked || worktree.Prunable || worktree.Bare || worktree.Main == repository.IsLinkedWorktree {
			return probe, errors.New("worktree registration does not permit demotion")
		}
	}
	if matches != 1 {
		return probe, errors.New("demotion requires exactly one current worktree registration")
	}
	gitDirIdentity, _, err := removalIdentity(repository.GitDir)
	if err != nil {
		return probe, err
	}
	probe.demoteAuthority = removalDigest([]any{repository, gitDirIdentity, selected, index, configuration})
	return probe, nil
}

// demoteTree observes the entire moved tree, including dirty and ignored files,
// without following links or entering independent nested repositories. Git's
// root .git file is allowed for an independently verified linked checkout.
func demoteTree(ctx context.Context, source string) (string, error) {
	root, _, err := safefile.OpenRoot(source)
	if err != nil {
		return "", err
	}
	defer root.Close()
	_, volume, err := removalIdentity(source)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 1000000 {
			return errors.New("repository has too many entries to verify demotion safely")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if name != ".git" && strings.EqualFold(filepath.Base(name), ".git") {
			return fmt.Errorf("nested Git repository at %s blocks demotion", name)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if name == ".git" || strings.HasPrefix(name, ".git/") {
				return errors.New("symlinked Git storage blocks demotion")
			}
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%q link %q\n", name, target)
			return nil
		}
		if info.IsDir() && name != "." && name != ".git" && !strings.HasPrefix(name, ".git/") {
			head, headErr := root.Stat(name + "/HEAD")
			objects, objectsErr := root.Stat(name + "/objects")
			configuration, configErr := root.Stat(name + "/config")
			if headErr == nil && head.Mode().IsRegular() && objectsErr == nil && objects.IsDir() && configErr == nil && configuration.Mode().IsRegular() {
				return fmt.Errorf("nested bare repository at %s blocks demotion", name)
			}
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("special file %s blocks demotion", name)
		}
		identity, device, err := removalIdentity(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if device != volume {
			return fmt.Errorf("mount boundary at %s blocks demotion", name)
		}
		fmt.Fprintf(hash, "%q %s %d %d %d\n", name, identity, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Service) verifyDemoteBoundary(ctx context.Context, plan DemotePlan) error {
	entry, err := s.store.Get(plan.ID)
	if err != nil {
		return err
	}
	if entry.MoveIntent == nil || entry.MoveIntent.Operation != string(TransitionDemote) {
		return errors.New("demotion move intent disappeared")
	}
	// The journal publication alone changes Updated and MoveIntent. Every
	// other reviewed field remains authority until the filesystem transition.
	copy := entry.Clone()
	copy.MoveIntent = nil
	copy.Updated = plan.entry.Updated
	if !sameRemovalEntry(copy, plan.entry) {
		return errors.New("catalog record changed before demotion")
	}
	observed, err := s.prepareDemote(ctx, copy, plan.Destination, false)
	if err != nil {
		return err
	}
	if observed.fingerprint() != plan.seal {
		return fmt.Errorf("demotion %s changed before the move", demoteDifference(plan, observed))
	}
	return nil
}

func demoteDifference(reviewed, observed DemotePlan) string {
	switch {
	case reviewed.identity != observed.identity:
		return "source directory identity"
	case reviewed.gitIdentity != observed.gitIdentity || reviewed.gitDirIdentity != observed.gitDirIdentity || reviewed.GitCommonDir != observed.GitCommonDir || reviewed.gitState != observed.gitState:
		return "Git identity"
	case reviewed.parentIdentity != observed.parentIdentity:
		return "destination parent identity"
	case reviewed.guard != observed.guard:
		return "task, runtime or artifact authority"
	case reviewed.tree != observed.tree:
		return "repository contents"
	default:
		return "reviewed catalog or move authority"
	}
}

func verifyDemoteDestination(intent *catalog.MoveIntent, probe directoryProbe) error {
	if intent == nil || probe.live.Repo == nil || probe.live.Repo.IsLinkedWorktree != intent.LinkedWorktree {
		return errors.New("demotion destination Git identity changed")
	}
	identity, _, err := removalIdentity(probe.live.CurrentPath)
	if err != nil || identity != intent.SourceIdentity {
		return errors.New("demotion destination is not the reviewed source directory")
	}
	gitIdentity, _, err := removalIdentity(probe.live.Repo.GitCommonDir)
	if err != nil || gitIdentity != intent.GitCommonIdentity {
		return errors.New("demotion destination Git storage differs from the reviewed source")
	}
	gitDirIdentity, _, err := removalIdentity(probe.live.Repo.GitDir)
	if err != nil || gitDirIdentity != intent.GitDirIdentity {
		return errors.New("demotion destination checkout Git directory differs from the reviewed source")
	}
	return nil
}

// Ordinary listing may reconcile a completed/interrupted demotion, but must
// never clear the source-only journal of a live apply. A physical identity-
// checked source or destination identifies the same movable repository lease.
// No task/artifact lock is needed here: recovery changes catalog metadata only.
func (s *Service) reconcileDemoteIntent(ctx context.Context, snapshot *catalog.Entry) error {
	intent := snapshot.MoveIntent
	sourceExists, err := pathExists(intent.SourcePath)
	if err != nil {
		return err
	}
	destinationExists, err := pathExists(intent.DestinationPath)
	if err != nil {
		return err
	}
	if sourceExists == destinationExists {
		// Both ambiguous cases retain the journal without any catalog write.
		return fmt.Errorf("pending demotion has ambiguous paths (source present=%t, destination present=%t); journal retained", sourceExists, destinationExists)
	}
	path := intent.SourcePath
	if destinationExists {
		path = intent.DestinationPath
	}
	probe := s.probeDirectory(ctx, path)
	if !probe.valid {
		return errors.New("cannot observe pending demotion repository identity")
	}
	if err := verifyDemoteDestination(intent, probe); err != nil {
		return err
	}
	return gitx.WithLifecycleMoveLock(ctx, probe.live.Repo.GitCommonDir, func() error {
		fresh, err := s.store.Get(snapshot.ID)
		if err != nil {
			return err
		}
		if fresh.MoveIntent == nil {
			return nil
		}
		if !moveIntentEqual(fresh.MoveIntent, intent) {
			return errors.New("demotion journal changed while acquiring recovery lease")
		}
		return s.reconcileMoveIntentLocked(ctx, fresh)
	})
}
