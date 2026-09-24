package experiment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// SuggestedGraduateName reads successful graduation history without migrating
// it. Legacy paths are parsed as metadata, never as current location authority.
func SuggestedGraduateName(item Item) string {
	if item.Entry != nil && item.Entry.Experiment != nil {
		history := item.Entry.Experiment
		if history.GraduatedName != "" {
			return history.GraduatedName
		}
		if !history.GraduatedAt.IsZero() && history.GraduatedPath != "" {
			name := path.Base(strings.ReplaceAll(history.GraduatedPath, `\`, "/"))
			if pathx.ValidateComponent(name) == nil && name == strings.TrimSpace(name) {
				return name
			}
		}
	}
	basename := item.Basename
	if item.Live.CurrentPath != "" {
		basename = filepath.Base(item.Live.CurrentPath)
	}
	name, _, _ := splitDatedBasename(basename)
	return name
}

// ResolveGraduate selects a source without validating a destination or writing
// catalog metadata. Legacy uncataloged folders may return an Item without an ID;
// callers can pass that exact path to PlanGraduate; ApplyGraduate enrolls it.
func (s *Service) ResolveGraduate(ctx context.Context, request GraduateRequest) (Item, []Diagnostic, error) {
	ctx = gitx.WithReadOnlyObservations(ctx)
	item, diagnostics, err := s.resolveGraduateItemWithOptions(ctx, request, true)
	if err == nil && request.Expected != nil && !sameRemovalEntry(item.Entry, request.Expected) {
		err = errors.New("selected Try changed; refresh graduation review")
	}
	return item, diagnostics, err
}

// The asset lease is outside every checkout, so a non-Git Try can retain it
// across initialization and all platforms can retain it across directory moves.
// Move apply/recovery take this lease before any repository and catalog locks.
func (s *Service) withMoveLease(ctx context.Context, id string, operation func() error) error {
	if err := catalog.ValidateID(id); err != nil {
		return err
	}
	leaseRoot, err := s.moveLeaseRoot()
	if err != nil {
		return err
	}
	return lockx.WithDir(ctx, filepath.Join(leaseRoot, "asset-"+id), "experiment move", operation)
}

func (s *Service) moveLeaseRoot() (string, error) {
	storePath, err := pathx.Canonical(s.store.Dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(storePath), ".experiment-move-leases", removalDigest(storePath)), nil
}

func (s *Service) withSourceMoveLease(ctx context.Context, identity string, operation func() error) error {
	leaseRoot, err := s.moveLeaseRoot()
	if err != nil {
		return err
	}
	return lockx.WithDir(ctx, filepath.Join(leaseRoot, "source-"+removalDigest(identity)), "legacy experiment move", operation)
}

func (p GraduatePlan) fingerprint() string {
	return removalDigest([]any{p.Item, p.Source, p.Destination, p.Name, p.Category, p.GitCommonDir,
		p.NeedsGitInit, p.NeedsInitialCommit, p.LinkedWorktree, p.DryRun, p.Register, p.entry,
		p.host, p.triesRoot, p.projectRoot, p.identity, p.gitIdentity, p.gitDirIdentity,
		p.gitState, p.gitConfig, p.gitOtherRefs, p.gitHeadRef, p.gitCheckoutHead, p.gitCheckoutBranch, p.gitDetached,
		p.tree, p.products, p.parent, p.parentIdentity, p.seedREADME})
}

func (s *Service) PlanGraduate(ctx context.Context, request GraduateRequest) (GraduatePlan, error) {
	ctx = gitx.WithReadOnlyObservations(ctx)
	item, diagnostics, err := s.resolveGraduateItemWithOptions(ctx, request, true)
	plan := GraduatePlan{Item: item, Diagnostics: diagnostics, DryRun: request.DryRun}
	if err != nil {
		return plan, err
	}
	if item.ID == "" && item.Live.CurrentPath != "" {
		identity, _, err := removalIdentity(item.Live.CurrentPath)
		if err != nil {
			return plan, err
		}
		err = s.withSourceMoveLease(ctx, identity, func() error {
			if err := s.unclaimedTryPath(item.Live.CurrentPath); err != nil {
				return err
			}
			current, _, err := removalIdentity(item.Live.CurrentPath)
			if err != nil || current != identity {
				return errors.New("legacy Try changed while acquiring planning lease")
			}
			probe, err := s.inspectMovableSource(ctx, item.Live.CurrentPath)
			if err != nil {
				return err
			}
			candidate := itemFromEntry(s.newEntry(probe), probe.live)
			plan, err = s.planGraduateSource(ctx, request, candidate, diagnostics)
			return err
		})
		return plan, err
	}
	if item.Entry == nil || item.ID == "" {
		return plan, errors.New("graduation requires a safely cataloged Try")
	}
	err = s.withMoveLease(ctx, item.ID, func() error {
		fresh, err := s.store.Get(item.ID)
		if err != nil {
			return err
		}
		if !sameRemovalEntry(fresh, item.Entry) {
			return errors.New("Try changed while acquiring graduation lease")
		}
		plan, err = s.planGraduateSource(ctx, request, itemFromEntry(fresh, item.Live), diagnostics)
		return err
	})
	return plan, err
}

func (s *Service) planGraduateSource(ctx context.Context, request GraduateRequest, item Item, diagnostics []Diagnostic) (GraduatePlan, error) {
	var plan GraduatePlan
	location, ok := item.Entry.LocationFor(s.host)
	if !ok {
		return plan, errors.New("Try has no location on this host")
	}
	probe, err := s.inspectMovableSource(ctx, location.CurrentPath)
	if err != nil {
		return plan, err
	}
	common := ""
	if probe.live.Repo != nil {
		common = probe.live.Repo.GitCommonDir
	}
	prepare := func() error {
		var err error
		plan, err = s.prepareGraduate(ctx, request, itemFromEntry(item.Entry, probe.live), diagnostics)
		if err != nil {
			return err
		}
		if item.ID == "" {
			if err := s.unclaimedTryPath(location.CurrentPath); err != nil {
				return err
			}
		} else {
			current, err := s.store.Get(item.ID)
			if err != nil {
				return err
			}
			if !sameRemovalEntry(current, item.Entry) {
				return errors.New("Try catalog changed during graduation planning")
			}
		}
		if plan.GitCommonDir != common {
			return errors.New("Git identity changed while acquiring graduation lease")
		}
		return nil
	}
	if common != "" {
		err = gitx.WithLifecycleMoveLock(ctx, common, prepare)
	} else {
		err = prepare()
	}
	return plan, err
}

func (s *Service) freezeGraduate(ctx context.Context, plan GraduatePlan, probe directoryProbe) (GraduatePlan, error) {
	if !eligibleTryRecord(plan.Item.Entry) || plan.Item.Entry.MoveIntent != nil {
		return plan, errors.New("graduation requires a Try without a pending move")
	}
	if inside, err := pathx.Contains(plan.Source, s.store.Dir); err != nil || inside {
		return plan, errors.New("graduation source contains durable catalog state")
	}
	plan.entry = plan.Item.Entry.Clone()
	plan.Item = itemFromEntry(plan.entry, probe.live)
	plan.host, plan.triesRoot, plan.projectRoot = s.host, s.triesRoot, s.projectRoot
	var err error
	plan.identity, _, err = removalIdentity(plan.Source)
	if err != nil {
		return plan, err
	}
	plan.GitCommonDir, plan.gitIdentity, plan.gitDirIdentity, plan.gitState = "", "", "", ""
	plan.gitConfig, plan.gitOtherRefs, plan.gitHeadRef = "", "", ""
	plan.gitCheckoutHead, plan.gitCheckoutBranch, plan.gitDetached = "", "", false
	if repository := probe.live.Repo; repository != nil {
		plan.GitCommonDir = repository.GitCommonDir
		plan.gitIdentity, _, err = removalIdentity(repository.GitCommonDir)
		if err != nil {
			return plan, err
		}
		plan.gitDirIdentity, _, err = removalIdentity(repository.GitDir)
		if err != nil {
			return plan, err
		}
		index, err := s.gitRun(ctx, plan.Source, "ls-files", "--stage", "-z")
		if err != nil {
			return plan, err
		}
		configuration, err := s.gitRun(ctx, plan.Source, "config", "--null", "--list")
		if err != nil {
			return plan, err
		}
		refs, err := s.gitRun(ctx, plan.Source, "for-each-ref", "--format=%(refname)%00%(objectname)")
		if err != nil {
			return plan, err
		}
		if plan.NeedsInitialCommit {
			plan.gitHeadRef, err = s.gitRun(ctx, plan.Source, "symbolic-ref", "--quiet", "HEAD")
			if err != nil {
				return plan, fmt.Errorf("inspect initial-commit branch: %w", err)
			}
		}
		otherRefs := []string{}
		for _, ref := range strings.Split(refs, "\n") {
			if ref == "" {
				continue
			}
			name, _, _ := strings.Cut(ref, "\x00")
			if name != plan.gitHeadRef {
				otherRefs = append(otherRefs, ref)
			}
		}
		plan.gitConfig, plan.gitOtherRefs = removalDigest(configuration), removalDigest(otherRefs)
		worktrees, err := s.worktrees(ctx, plan.Source)
		if err != nil {
			return plan, err
		}
		found := 0
		for _, worktree := range worktrees {
			if samePath(worktree.Path, plan.Source) {
				found++
				plan.gitCheckoutHead, plan.gitCheckoutBranch, plan.gitDetached = worktree.Head, worktree.Branch, worktree.Detached
			}
		}
		if found != 1 {
			return plan, errors.New("graduation requires one exact current Git worktree registration")
		}
		plan.gitState = removalDigest([]any{repository, index, configuration, refs, worktrees})
	}
	plan.parent, err = nearestExisting(filepath.Dir(plan.Destination))
	if err != nil {
		return plan, err
	}
	plan.parentIdentity, _, err = removalIdentity(plan.parent)
	if err != nil {
		return plan, err
	}
	plan.tree, err = graduateTree(ctx, plan.Source, false, false)
	if err != nil {
		return plan, err
	}
	plan.products, err = graduateTree(ctx, plan.Source, true, false)
	if err != nil {
		return plan, err
	}
	plan.seedREADME, err = isEmptyWorkTree(plan.Source)
	if err != nil {
		return plan, err
	}
	plan.seal = plan.fingerprint()
	return plan, nil
}

// ApplyGraduate applies the sealed local move. It does not require idle runtime
// coverage: active Try/current-directory graduation remains supported. External
// writers may make the review stale, and Git preparation effects are reported
// separately if a later check fails. Remote publication belongs to the caller.
func (s *Service) ApplyGraduate(ctx context.Context, plan GraduatePlan) (GraduateResult, error) {
	ctx = gitx.WithReadOnlyObservations(ctx)
	result := GraduateResult{Plan: plan, Item: plan.Item, Diagnostics: plan.Diagnostics}
	if plan.seal == "" || plan.fingerprint() != plan.seal {
		return result, errors.New("invalid or modified graduation plan")
	}
	if plan.host != s.host || plan.triesRoot != s.triesRoot || plan.projectRoot != s.projectRoot {
		return result, errors.New("graduation plan belongs to a different host or roots")
	}
	if plan.DryRun {
		return result, nil
	}
	if plan.Register {
		err := s.withSourceMoveLease(ctx, plan.identity, func() error {
			if err := s.checkTransientGraduateReview(ctx, plan); err != nil {
				return err
			}
			entry := plan.entry.Clone()
			if err := s.store.WithLock(ctx, func() error {
				if err := s.unclaimedTryPath(plan.Source); err != nil {
					return err
				}
				current, _, err := removalIdentity(plan.Source)
				if err != nil || current != plan.identity {
					return errors.New("legacy Try changed before registration")
				}
				return s.catalogCreate(entry)
			}); err != nil {
				return err
			}
			result.Registered = true
			bound := plan
			bound.Register, bound.entry = false, entry.Clone()
			bound.Item = itemFromEntry(entry, plan.Item.Live)
			bound.seal = bound.fingerprint()
			applied, err := s.ApplyGraduate(ctx, bound)
			result.Item, result.Moved, result.GitInitialized, result.InitialCommitMade = applied.Item, applied.Moved, applied.GitInitialized, applied.InitialCommitMade
			result.RolledBack, result.RollbackError = applied.RolledBack, applied.RollbackError
			result.Publication, result.PublicationError = applied.Publication, applied.PublicationError
			return err
		})
		return result, err
	}
	err := s.withMoveLease(ctx, plan.entry.ID, func() error {
		apply := func() error {
			if err := s.checkGraduateReview(ctx, plan); err != nil {
				return err
			}
			initializedGitIdentity := ""
			if plan.NeedsGitInit {
				if _, err := s.gitRun(ctx, plan.Source, "init", "-b", "main"); err != nil {
					return fmt.Errorf("initialize Git before graduating %s: %w", plan.Source, err)
				}
				result.GitInitialized = true
				var err error
				initializedGitIdentity, _, err = removalIdentity(filepath.Join(plan.Source, ".git"))
				if err != nil {
					return fmt.Errorf("observe initialized Git directory: %w", err)
				}
			}
			finish := func() error { return s.finishReviewedGraduation(ctx, plan, initializedGitIdentity, &result) }
			if plan.NeedsGitInit {
				probe, err := s.inspectMovableSource(ctx, plan.Source)
				if err != nil {
					return err
				}
				if probe.live.Repo == nil {
					return errors.New("Git initialization did not produce the reviewed checkout")
				}
				return gitx.WithLifecycleMoveLock(ctx, probe.live.Repo.GitCommonDir, finish)
			}
			return finish()
		}
		if plan.GitCommonDir != "" {
			return gitx.WithLifecycleMoveLock(ctx, plan.GitCommonDir, apply)
		}
		return apply()
	})
	return result, err
}

func (s *Service) checkTransientGraduateReview(ctx context.Context, plan GraduatePlan) error {
	if err := s.unclaimedTryPath(plan.Source); err != nil {
		return err
	}
	observed, err := s.prepareGraduate(ctx, GraduateRequest{Name: plan.Name, Category: plan.Category}, plan.Item, plan.Diagnostics)
	if err != nil {
		return err
	}
	if observed.fingerprint() != plan.seal {
		return errors.New("stale legacy graduation plan: source, contents, Git or destination changed")
	}
	return s.unclaimedTryPath(plan.Source)
}

func (s *Service) checkGraduateReview(ctx context.Context, plan GraduatePlan) error {
	fresh, err := s.store.Get(plan.entry.ID)
	if err != nil {
		return err
	}
	if !sameRemovalEntry(fresh, plan.entry) {
		return errors.New("stale graduation plan: catalog record changed")
	}
	observed, err := s.prepareGraduate(ctx, GraduateRequest{Ref: fresh.ID, Name: plan.Name, Category: plan.Category},
		itemFromEntry(fresh, LiveFacts{Present: true, CurrentPath: plan.Source}), plan.Diagnostics)
	if err != nil {
		return err
	}
	if observed.fingerprint() != plan.seal {
		return errors.New("stale graduation plan: source, contents, Git or destination authority changed")
	}
	latest, err := s.store.Get(plan.entry.ID)
	if err != nil {
		return err
	}
	if !sameRemovalEntry(latest, plan.entry) {
		return errors.New("stale graduation plan: catalog source moved or was reassigned")
	}
	return nil
}

func (s *Service) checkGraduateProducts(ctx context.Context, plan GraduatePlan, allowREADME bool) error {
	identity, _, err := removalIdentity(plan.Source)
	if err != nil || identity != plan.identity {
		return errors.New("graduation source directory changed during Git preparation")
	}
	if allowREADME && plan.seedREADME {
		body, err := os.ReadFile(filepath.Join(plan.Source, "README.md"))
		if err != nil || string(body) != "# "+plan.Name+"\n" {
			return errors.New("initial README changed during Git preparation")
		}
	}
	products, err := graduateTree(ctx, plan.Source, true, allowREADME && plan.seedREADME)
	if err != nil {
		return err
	}
	if products != plan.products {
		return errors.New("Try contents changed during Git preparation; prepared Git state retained at the source")
	}
	fresh, err := s.store.Get(plan.entry.ID)
	if err != nil {
		return err
	}
	if !sameRemovalEntry(fresh, plan.entry) {
		return errors.New("catalog changed during Git preparation; prepared Git state retained at the source")
	}
	return s.checkGraduateDestination(plan)
}

func (s *Service) checkGraduateDestination(plan GraduatePlan) error {
	identity, _, err := removalIdentity(plan.parent)
	if err != nil || identity != plan.parentIdentity {
		return errors.New("reviewed graduation destination parent changed")
	}
	if err := s.validateTransitionContainment(plan.entry.ID, TransitionGraduate, plan.Source, plan.Destination); err != nil {
		return err
	}
	return rejectExisting(plan.Destination)
}

type graduateMoveAuthority struct{ plan GraduatePlan }

func (s *Service) finishReviewedGraduation(ctx context.Context, plan GraduatePlan, initializedGitIdentity string, result *GraduateResult) error {
	if err := s.checkGraduateProducts(ctx, plan, false); err != nil {
		return err
	}
	prepared := plan
	before := plan
	if plan.NeedsGitInit {
		probe, err := s.inspectMovableSource(ctx, plan.Source)
		if err != nil {
			return err
		}
		before, err = s.freezeGraduate(ctx, plan, probe)
		if err != nil {
			return err
		}
		if initializedGitIdentity == "" || before.gitIdentity != initializedGitIdentity || before.gitDirIdentity != initializedGitIdentity {
			return errors.New("initialized Git directory changed while acquiring graduation lease; preparation retained at the source")
		}
	}
	if plan.NeedsInitialCommit {
		// Existing repositories retain their reviewed storage and Git authority
		// until the exact point where our own initial commit is allowed to act.
		probe, err := s.inspectMovableSource(ctx, plan.Source)
		if err != nil {
			return err
		}
		observed, err := s.freezeGraduate(ctx, before, probe)
		if err != nil {
			return err
		}
		if observed.fingerprint() != before.seal {
			return errors.New("Git or source authority changed before initial commit")
		}
		made, err := s.ensureInitialCommit(ctx, plan.Source, plan.Name)
		result.InitialCommitMade = made
		if err != nil {
			return fmt.Errorf("ensure initial commit before graduating %s: %w", plan.Source, err)
		}
		if !made {
			return errors.New("initial commit appeared outside the reviewed preparation; Git state retained at the source")
		}
		probe, err = s.inspectMovableSource(ctx, plan.Source)
		if err != nil {
			return err
		}
		if probe.live.Repo == nil || !s.hasCommit(ctx, plan.Source) {
			return errors.New("prepared graduation source is not a committed Git repository")
		}
		prepared, err = s.freezeGraduate(ctx, plan, probe)
		if err != nil {
			return err
		}
		if prepared.GitCommonDir != before.GitCommonDir || prepared.gitIdentity != before.gitIdentity || prepared.gitDirIdentity != before.gitDirIdentity ||
			prepared.gitConfig != before.gitConfig || prepared.gitOtherRefs != before.gitOtherRefs || prepared.gitHeadRef != before.gitHeadRef {
			return errors.New("Git storage, configuration or unrelated refs changed during initial commit; preparation retained at the source")
		}
		parents, err := s.gitRun(ctx, plan.Source, "rev-list", "--parents", "-n", "1", "HEAD")
		if err != nil || len(strings.Fields(parents)) != 1 {
			return errors.New("initial commit authority changed; preparation retained at the source")
		}
		if _, err := s.gitRun(ctx, plan.Source, "diff", "--cached", "--quiet", "HEAD", "--"); err != nil {
			return errors.New("index changed after initial commit; preparation retained at the source")
		}
		if err := s.checkGraduateProducts(ctx, plan, true); err != nil {
			return err
		}
	}
	// No preparation means no authority refresh: a late ref/index/admin change
	// must remain a stale plan, even if the working files themselves are equal.
	moved, err := s.applyTransitionLocked(ctx, TransitionResult{Plan: TransitionPlan{
		Operation: TransitionGraduate, Item: prepared.Item, Source: plan.Source, Destination: plan.Destination,
		LinkedWorktree: plan.LinkedWorktree, Diagnostics: plan.Diagnostics,
		graduate: &graduateMoveAuthority{plan: prepared},
	}, Item: prepared.Item, Diagnostics: plan.Diagnostics})
	result.Item, result.Moved, result.RolledBack, result.RollbackError = moved.Item, moved.Moved, moved.RolledBack, moved.RollbackError
	if err == nil && moved.Moved {
		result.Publication, result.PublicationError = s.captureGraduatePublication(ctx, plan.Destination, prepared)
	}
	return err
}

func (s *Service) captureGraduatePublication(ctx context.Context, checkout string, prepared GraduatePlan) (*GraduatePublication, error) {
	repository, err := s.gitDiscover(ctx, checkout)
	if err != nil {
		return nil, err
	}
	for _, field := range []struct{ path, identity string }{{checkout, prepared.identity}, {repository.GitCommonDir, prepared.gitIdentity}, {repository.GitDir, prepared.gitDirIdentity}} {
		identity, _, err := removalIdentity(field.path)
		if err != nil || identity != field.identity {
			return nil, errors.New("local graduation completed, but checkout/Git identity changed before publication could be bound")
		}
	}
	publication := &GraduatePublication{Checkout: checkout, GitCommonDir: repository.GitCommonDir, GitDir: repository.GitDir}
	for _, field := range []struct {
		path  string
		value *string
	}{{checkout, &publication.CheckoutIdentity}, {repository.GitCommonDir, &publication.GitCommonIdentity}, {repository.GitDir, &publication.GitDirIdentity}} {
		*field.value, err = gitx.DirectoryIdentity(field.path)
		if err != nil {
			return nil, err
		}
	}
	publication.Branch, err = s.gitRun(ctx, checkout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exited *exec.ExitError
		if !prepared.gitDetached || !errors.As(err, &exited) || exited.ExitCode() != 1 {
			return nil, errors.New("local graduation completed, but branch identity is unavailable for publication")
		}
		publication.Branch, publication.Detached = "", true
	} else if publication.Branch == "" {
		return nil, errors.New("local graduation completed, but branch identity is unavailable for publication")
	}
	publication.Head, err = s.gitRun(ctx, checkout, "rev-parse", "--verify", "HEAD")
	if err != nil || publication.Head == "" {
		return nil, errors.New("local graduation completed, but HEAD is unavailable for publication")
	}
	if publication.Detached != prepared.gitDetached || publication.Branch != prepared.gitCheckoutBranch || publication.Head != prepared.gitCheckoutHead {
		return nil, errors.New("local graduation completed, but reviewed branch or HEAD changed before publication could be bound")
	}
	return publication, nil
}

func (s *Service) verifyGraduateBoundary(ctx context.Context, authority *graduateMoveAuthority) error {
	if authority == nil {
		return errors.New("graduation move lacks reviewed authority")
	}
	plan := authority.plan
	entry, err := s.store.Get(plan.entry.ID)
	if err != nil {
		return err
	}
	if entry.MoveIntent == nil || entry.MoveIntent.Operation != string(TransitionGraduate) {
		return errors.New("graduation move intent disappeared")
	}
	copy := entry.Clone()
	copy.MoveIntent, copy.Updated = nil, plan.entry.Updated
	if !sameRemovalEntry(copy, plan.entry) {
		return errors.New("catalog changed before graduation move")
	}
	if err := s.checkGraduateDestination(plan); err != nil {
		return err
	}
	probe, err := s.inspectMovableSource(ctx, plan.Source)
	if err != nil {
		return err
	}
	observed, err := s.freezeGraduate(ctx, plan, probe)
	if err != nil {
		return err
	}
	// Creating the reviewed destination parents may advance the nearest
	// existing ancestor. The originally reviewed parent's identity was checked.
	observed.parent, observed.parentIdentity = plan.parent, plan.parentIdentity
	if observed.fingerprint() != plan.seal {
		return errors.New("graduation source or Git authority changed immediately before moving")
	}
	return nil
}
