package artifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const DefaultLargeLimit int64 = 2 << 20

type PrepareRequest struct {
	Worktree   string
	TaskID     string
	Session    string
	RunID      string
	Base       string
	Plans      []string
	AllowLarge bool
}

type FinalizeRequest struct {
	IntentID      string
	RunID         string
	Settle        time.Duration
	WriterStopped bool
}

// Scanner must redact/scan the staged paths without emitting secret values.
type Scanner func(context.Context, string, []string) error

// Service coordinates Git with the durable intent store.
type Service struct {
	Store      *Store
	ScanStaged Scanner
	LargeLimit int64

	beforeIntentCreate func()
}

// ReadinessState classifies one intent without changing its durable record.
type ReadinessState string

const (
	ReadinessPending              ReadinessState = "pending"
	ReadinessFailed               ReadinessState = "failed"
	ReadinessDiscarded            ReadinessState = "discarded"
	ReadinessFinalizedReachable   ReadinessState = "finalized-reachable"
	ReadinessFinalizedUnreachable ReadinessState = "finalized-unreachable"
	ReadinessObservationError     ReadinessState = "observation-error"
)

// IntentReadiness is the read-only finalization evidence for one intent matched
// by exact checkout path or stable Git common-directory and branch identity.
type IntentReadiness struct {
	Intent           Intent
	State            ReadinessState
	Finalized        bool
	ReceiptReachable bool
	ObservationError error
}

// ReadinessInspection is a complete read-only observation for one selected
// checkout. KnownEmpty is true only when the store was read successfully and no
// intent matched the checkout; it distinguishes absence from failed observation.
type ReadinessInspection struct {
	Checkout         string
	KnownEmpty       bool
	Intents          []IntentReadiness
	ObservationError error
}

// Ready applies only the existing artifact finalization contract: an exact
// checkout is ready when it has no intents, or every intent was explicitly
// discarded or has a finalized receipt that is still reachable. Missing or
// incomplete evidence fails closed.
func (i ReadinessInspection) Ready() bool {
	if i.ObservationError != nil {
		return false
	}
	if len(i.Intents) == 0 {
		return i.KnownEmpty
	}
	if i.KnownEmpty {
		return false
	}
	for _, intent := range i.Intents {
		if intent.ObservationError != nil {
			return false
		}
		switch intent.State {
		case ReadinessDiscarded:
			// Discard is the existing explicit operator escape hatch.
		case ReadinessFinalizedReachable:
			if !intent.Finalized || !intent.ReceiptReachable {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// InspectReadiness gathers artifact finalization evidence for one checkout,
// including intents whose exact checkout moved but stable Git identity matches.
// It never reconciles receipts, finalizes intents, or writes either
// source files or intent records. The returned inspection retains partial
// evidence and the same wrapped error returned separately to ordinary callers.
func InspectReadiness(ctx context.Context, store *Store, checkout string) (ReadinessInspection, error) {
	inspection := ReadinessInspection{}
	if ctx == nil {
		err := errors.New("artifact readiness inspection needs a context")
		inspection.ObservationError = err
		return inspection, err
	}
	if err := ctx.Err(); err != nil {
		inspection.ObservationError = err
		return inspection, err
	}
	if store == nil {
		err := errors.New("artifact readiness inspection needs a store")
		inspection.ObservationError = err
		return inspection, err
	}

	canonical, err := pathx.Canonical(checkout)
	if err != nil {
		err = fmt.Errorf("canonicalize artifact readiness checkout: %w", err)
		inspection.ObservationError = err
		return inspection, err
	}
	inspection.Checkout = canonical

	intents, err := store.List()
	if err != nil {
		err = fmt.Errorf("list artifact intents: %w", err)
		inspection.ObservationError = err
		return inspection, err
	}
	if err := ctx.Err(); err != nil {
		inspection.ObservationError = err
		return inspection, err
	}

	matched := make([]Intent, 0, len(intents))
	unmatched := make([]Intent, 0, len(intents))
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			inspection.ObservationError = joinReadinessError(inspection.ObservationError, err)
			break
		}
		intentCheckout, canonicalErr := pathx.Canonical(intent.WorktreePath)
		if canonicalErr != nil {
			canonicalErr = fmt.Errorf("canonicalize artifact intent %s checkout: %w", intent.ID, canonicalErr)
			inspection.ObservationError = joinReadinessError(inspection.ObservationError, canonicalErr)
			continue
		}
		if intentCheckout == canonical {
			matched = append(matched, intent)
		} else {
			unmatched = append(unmatched, intent)
		}
	}

	if len(unmatched) > 0 && inspection.ObservationError == nil {
		identity, available, identityErr := inspectReadinessCheckoutIdentity(ctx, canonical)
		if identityErr != nil {
			inspection.ObservationError = joinReadinessError(inspection.ObservationError, identityErr)
		} else if available {
			for _, intent := range unmatched {
				intentCommon, commonErr := pathx.Canonical(intent.GitCommonDir)
				if commonErr != nil {
					inspection.ObservationError = joinReadinessError(inspection.ObservationError,
						fmt.Errorf("canonicalize artifact intent %s Git common directory: %w", intent.ID, commonErr))
					continue
				}
				if intentCommon == identity.commonDir && intent.Branch == identity.branch {
					matched = append(matched, intent)
				}
			}
		}
	}

	for _, intent := range matched {
		evidence, evidenceErr := inspectIntentReadiness(ctx, canonical, intent)
		if evidenceErr != nil {
			inspection.ObservationError = joinReadinessError(inspection.ObservationError, evidenceErr)
		}
		inspection.Intents = append(inspection.Intents, evidence)
	}

	inspection.KnownEmpty = len(inspection.Intents) == 0 && inspection.ObservationError == nil
	return inspection, inspection.ObservationError
}

type readinessCheckoutIdentity struct {
	commonDir string
	branch    string
}

func inspectReadinessCheckoutIdentity(ctx context.Context, checkout string) (readinessCheckoutIdentity, bool, error) {
	repository, err := gitx.Discover(ctx, checkout)
	if errors.Is(err, gitx.ErrNotARepo) {
		return readinessCheckoutIdentity{}, false, nil
	}
	if err != nil {
		return readinessCheckoutIdentity{}, false, fmt.Errorf("discover artifact readiness checkout identity: %w", err)
	}
	commonDir, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		return readinessCheckoutIdentity{}, false, fmt.Errorf("canonicalize artifact readiness Git common directory: %w", err)
	}
	status, err := gitx.StatusOf(ctx, checkout)
	if err != nil {
		return readinessCheckoutIdentity{}, false, fmt.Errorf("observe artifact readiness checkout branch: %w", err)
	}
	if status.Detached || status.Branch == "" {
		return readinessCheckoutIdentity{}, false, errors.New("artifact readiness checkout is detached; moved intent identity is ambiguous")
	}
	return readinessCheckoutIdentity{commonDir: commonDir, branch: status.Branch}, true, nil
}

func inspectIntentReadiness(ctx context.Context, checkout string, intent Intent) (IntentReadiness, error) {
	evidence := IntentReadiness{Intent: intent, Finalized: intent.Status == Finalized}
	var err error
	switch intent.Status {
	case Armed, Finalizing:
		evidence.State = ReadinessPending
	case Failed:
		evidence.State = ReadinessFailed
	case Discarded:
		evidence.State = ReadinessDiscarded
	case Finalized:
		evidence.ReceiptReachable, err = receiptRemainsReachable(ctx, checkout, intent)
		switch {
		case err != nil:
			err = fmt.Errorf("inspect artifact intent %s receipt: %w", intent.ID, err)
			evidence.State = ReadinessObservationError
			evidence.ObservationError = err
		case evidence.ReceiptReachable:
			evidence.State = ReadinessFinalizedReachable
		default:
			evidence.State = ReadinessFinalizedUnreachable
		}
	default:
		// Store.List validates statuses, but fail closed if another Store
		// implementation is introduced without preserving that contract.
		err = fmt.Errorf("artifact intent %s has unrecognized status %q", intent.ID, intent.Status)
		evidence.State = ReadinessObservationError
		evidence.ObservationError = err
	}
	return evidence, err
}

func receiptRemainsReachable(ctx context.Context, checkout string, intent Intent) (bool, error) {
	if intent.ArtifactCommit == "" {
		return false, nil
	}
	candidates := []struct {
		dir string
		ref string
	}{
		{checkout, "HEAD"},
		{intent.RepoPath, intent.Branch},
		{intent.RepoPath, intent.Base},
	}
	seen := make(map[string]bool, len(candidates))
	var observationErr error
	for _, candidate := range candidates {
		if candidate.dir == "" || candidate.ref == "" {
			continue
		}
		key := candidate.dir + "\x00" + candidate.ref
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := ctx.Err(); err != nil {
			return false, joinReadinessError(observationErr, err)
		}
		if _, err := gitx.Run(ctx, candidate.dir, "merge-base", "--is-ancestor", intent.ArtifactCommit, candidate.ref); err == nil {
			return true, nil
		} else if !gitNotAncestor(err) {
			observationErr = joinReadinessError(observationErr,
				fmt.Errorf("check %s against %s in %s: %w", intent.ArtifactCommit, candidate.ref, candidate.dir, err))
		}
	}
	if err := ctx.Err(); err != nil {
		observationErr = joinReadinessError(observationErr, err)
	}
	return false, observationErr
}

func gitNotAncestor(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func joinReadinessError(current, next error) error {
	if current == nil {
		return next
	}
	if next == nil {
		return current
	}
	return errors.Join(current, next)
}

func (s *Service) Prepare(ctx context.Context, request PrepareRequest) (*Intent, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("artifact service needs a store")
	}
	provider, sessionID, err := ParseSession(request.Session)
	if err != nil {
		return nil, err
	}
	repository, err := gitx.Discover(ctx, request.Worktree)
	if err != nil {
		return nil, err
	}
	status, err := gitx.StatusOf(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	if status.Detached || status.Branch == "" {
		return nil, fmt.Errorf("artifact preparation requires a named branch")
	}
	if operation, active, err := gitx.InProgress(repository.Root); err != nil {
		return nil, err
	} else if active {
		return nil, fmt.Errorf("artifact preparation refuses Git operation %s in progress", operation)
	}
	head, err := gitx.Run(ctx, repository.Root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	changes, err := statusPaths(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	var unrelated []string
	for _, change := range changes {
		if change.Staged {
			return nil, fmt.Errorf("artifact preparation requires an empty index; staged path: %s", change.Path)
		}
		if !isRecognizedArtifact(change.Path) {
			return nil, fmt.Errorf("commit product changes before prepare; dirty path: %s", change.Path)
		}
		unrelated = append(unrelated, change.Path)
	}
	transcript, err := FindTranscript(repository.Root, provider, sessionID)
	if err != nil {
		return nil, err
	}
	tracked, err := gitTracked(ctx, repository.Root, transcript.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(transcript.Path)
	if err != nil {
		return nil, err
	}
	limit := s.LargeLimit
	if limit <= 0 {
		limit = DefaultLargeLimit
	}
	if !tracked && info.Size() > limit && !request.AllowLarge {
		return nil, fmt.Errorf("untracked transcript is %d bytes; re-run with --allow-large after reviewing repository growth", info.Size())
	}
	plans, err := normalizePlans(repository.Root, request.Plans)
	if err != nil {
		return nil, err
	}
	runID := request.RunID
	if runID == "" {
		runID = os.Getenv("DEV_AGENT_RUN_ID")
	}
	if runID == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	selected := make(map[string]bool, len(plans)+1)
	relTranscript, _ := filepath.Rel(repository.Root, transcript.Path)
	selected[filepath.ToSlash(relTranscript)] = true
	for _, plan := range plans {
		rel, _ := filepath.Rel(repository.Root, plan)
		selected[filepath.ToSlash(rel)] = true
	}
	filtered := unrelated[:0]
	for _, path := range unrelated {
		if !selected[path] {
			filtered = append(filtered, path)
		}
	}
	intent := &Intent{
		RunID: runID, Provider: provider, SessionID: sessionID, TaskID: request.TaskID,
		RepoPath: repository.MainRoot, GitCommonDir: repository.GitCommonDir,
		WorktreePath: repository.Root, Branch: status.Branch, Base: request.Base,
		Head: strings.TrimSpace(head), PlanPaths: plans, UnrelatedArtifacts: filtered, AllowLarge: request.AllowLarge,
	}
	commonDir, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize artifact repository lock identity: %w", err)
	}
	if s.beforeIntentCreate != nil {
		s.beforeIntentCreate()
	}
	if err := lockx.WithDir(ctx, filepath.Join(commonDir, "dev-taskflow"), "taskflow repository", func() error {
		if err := revalidatePreparedIntent(ctx, intent); err != nil {
			return err
		}
		return s.Store.Create(ctx, intent)
	}); err != nil {
		return nil, err
	}
	return intent, nil
}

func revalidatePreparedIntent(ctx context.Context, intent *Intent) error {
	repository, err := gitx.Discover(ctx, intent.WorktreePath)
	if err != nil {
		return fmt.Errorf("revalidate artifact checkout before arming intent: %w", err)
	}
	checkout, err := pathx.Canonical(repository.Root)
	if err != nil {
		return fmt.Errorf("canonicalize revalidated artifact checkout: %w", err)
	}
	expectedCheckout, err := pathx.Canonical(intent.WorktreePath)
	if err != nil {
		return fmt.Errorf("canonicalize prepared artifact checkout: %w", err)
	}
	commonDir, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		return fmt.Errorf("canonicalize revalidated artifact Git common directory: %w", err)
	}
	expectedCommon, err := pathx.Canonical(intent.GitCommonDir)
	if err != nil {
		return fmt.Errorf("canonicalize prepared artifact Git common directory: %w", err)
	}
	if checkout != expectedCheckout || commonDir != expectedCommon {
		return fmt.Errorf("artifact checkout identity changed before intent creation")
	}
	status, err := gitx.StatusOf(ctx, checkout)
	if err != nil {
		return fmt.Errorf("revalidate artifact checkout status: %w", err)
	}
	if status.Detached || status.Branch != intent.Branch {
		return fmt.Errorf("artifact checkout branch changed from %s to %s before intent creation", intent.Branch, status.Branch)
	}
	if operation, active, err := gitx.InProgress(checkout); err != nil {
		return fmt.Errorf("revalidate artifact Git operation: %w", err)
	} else if active {
		return fmt.Errorf("artifact preparation refuses Git operation %s that started before intent creation", operation)
	}
	head, err := gitx.Run(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("revalidate artifact checkout HEAD: %w", err)
	}
	if strings.TrimSpace(head) != strings.TrimSpace(intent.Head) {
		return fmt.Errorf("artifact checkout HEAD changed before intent creation")
	}
	return nil
}

func (s *Service) ObserveSessionEnd(ctx context.Context, runID string, when time.Time) error {
	intent, err := s.Store.FindByRunID(runID)
	if err != nil {
		return nil // SessionEnd without an armed intent is deliberately a no-op.
	}
	if when.IsZero() {
		when = time.Now()
	}
	return s.Store.Update(ctx, intent.ID, func(candidate *Intent) error {
		candidate.SessionEndedAt = when.UTC().Truncate(time.Second)
		return nil
	})
}

func (s *Service) Finalize(ctx context.Context, request FinalizeRequest) (*Intent, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("artifact service needs a store")
	}
	intent, err := s.resolveIntent(request)
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(intent.GitCommonDir, "dev-artifact-finalize")
	var finalized *Intent
	err = catalog.NewStore(lockDir).WithLock(ctx, func() error {
		current, err := s.resolveIntent(request)
		if err != nil {
			return err
		}
		finalized, err = s.finalizeLocked(ctx, request, current)
		return err
	})
	return finalized, err
}

func (s *Service) finalizeLocked(ctx context.Context, request FinalizeRequest, intent *Intent) (*Intent, error) {
	if intent.Status == Finalized {
		if commit, ok := findReceipt(ctx, intent.WorktreePath, intent.ID); ok && commit != intent.ArtifactCommit {
			if err := s.markFinalized(ctx, intent.ID, intent.TranscriptPath, commit); err != nil {
				return nil, err
			}
			return s.Store.Get(intent.ID)
		}
		return intent, nil
	}
	if commit, ok := findReceipt(ctx, intent.WorktreePath, intent.ID); ok {
		if err := s.markFinalized(ctx, intent.ID, intent.TranscriptPath, commit); err != nil {
			return nil, err
		}
		return s.Store.Get(intent.ID)
	}
	if s.ScanStaged == nil {
		return nil, s.fail(ctx, intent.ID, "scanner-missing", fmt.Errorf("artifact finalization requires a staged secret scanner"))
	}
	if err := s.revalidate(ctx, intent); err != nil {
		return nil, s.fail(ctx, intent.ID, "git-drift", err)
	}
	if intent.SessionEndedAt.IsZero() && !request.WriterStopped {
		return nil, s.fail(ctx, intent.ID, "writer-unproven",
			fmt.Errorf("finalization requires SessionEnd observation or explicit post-writer proof"))
	}
	transcript, err := FindTranscript(intent.WorktreePath, intent.Provider, intent.SessionID)
	if err != nil {
		return nil, s.fail(ctx, intent.ID, "transcript-missing", err)
	}
	if err := s.Store.Update(ctx, intent.ID, func(candidate *Intent) error {
		candidate.Status = Finalizing
		candidate.TranscriptPath = transcript.Path
		candidate.FailureCode = ""
		return nil
	}); err != nil {
		return nil, err
	}
	if _, err := StableSnapshot(ctx, transcript.Path, request.Settle); err != nil {
		return nil, s.fail(ctx, intent.ID, "writer-active", err)
	}
	paths := append([]string{transcript.Path}, intent.PlanPaths...)
	if err := ensureOnlyAllowedIndex(ctx, intent.WorktreePath, nil); err != nil {
		return nil, s.fail(ctx, intent.ID, "index-not-empty", err)
	}
	if _, err := gitx.Run(ctx, intent.WorktreePath, append([]string{"add", "--"}, paths...)...); err != nil {
		return nil, s.fail(ctx, intent.ID, "stage-failed", err)
	}
	cleanup := func() {
		_, _ = gitx.Run(context.Background(), intent.WorktreePath, append([]string{"reset", "--"}, paths...)...)
	}
	if err := s.ScanStaged(ctx, intent.WorktreePath, paths); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "scan-failed", err)
	}
	if _, err := StableSnapshot(ctx, transcript.Path, request.Settle); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "writer-active", err)
	}
	if err := ensureOnlyAllowedIndex(ctx, intent.WorktreePath, paths); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "index-drift", err)
	}
	if err := stagedFilesMatch(ctx, intent.WorktreePath, paths); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "index-drift", err)
	}
	message := fmt.Sprintf("chore: finalize %s agent session", intent.Provider)
	trailers := fmt.Sprintf("Agent-Artifact-Session: %s:%s\nDev-Artifact-Intent: %s", intent.Provider, intent.SessionID, intent.ID)
	if _, err := gitx.Run(ctx, intent.WorktreePath, "commit", "-m", message, "-m", trailers); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "commit-failed", err)
	}
	commit, err := gitx.Run(ctx, intent.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return nil, s.fail(ctx, intent.ID, "receipt-failed", err)
	}
	if err := s.markFinalized(ctx, intent.ID, transcript.Path, commit); err != nil {
		return nil, err
	}
	return s.Store.Get(intent.ID)
}

func (s *Service) resolveIntent(request FinalizeRequest) (*Intent, error) {
	if request.IntentID != "" {
		return s.Store.Get(request.IntentID)
	}
	if request.RunID != "" {
		return s.Store.FindByRunID(request.RunID)
	}
	return nil, fmt.Errorf("finalize requires an intent or run id")
}

func (s *Service) revalidate(ctx context.Context, intent *Intent) error {
	repository, err := gitx.Discover(ctx, intent.WorktreePath)
	if err != nil {
		return err
	}
	if repository.GitCommonDir != intent.GitCommonDir {
		return fmt.Errorf("Git common-dir changed")
	}
	status, err := gitx.StatusOf(ctx, intent.WorktreePath)
	if err != nil {
		return err
	}
	if status.Branch != intent.Branch || status.Detached {
		return fmt.Errorf("branch changed from %s to %s", intent.Branch, status.Branch)
	}
	if operation, active, err := gitx.InProgress(intent.WorktreePath); err != nil {
		return err
	} else if active {
		return fmt.Errorf("Git operation %s is in progress", operation)
	}
	head, err := gitx.Run(ctx, intent.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != intent.Head {
		return fmt.Errorf("HEAD changed from %s to %s", intent.Head, head)
	}
	return nil
}

func (s *Service) fail(ctx context.Context, id, code string, cause error) error {
	_ = s.Store.Update(ctx, id, func(intent *Intent) error {
		intent.Status = Failed
		intent.FailureCode = code
		return nil
	})
	return fmt.Errorf("artifact finalization %s: %w", code, cause)
}

func (s *Service) markFinalized(ctx context.Context, id, transcript, commit string) error {
	return s.Store.Update(ctx, id, func(intent *Intent) error {
		intent.Status = Finalized
		intent.TranscriptPath = transcript
		intent.ArtifactCommit = commit
		intent.FailureCode = ""
		return nil
	})
}

type statusPath struct {
	Path   string
	Staged bool
}

func statusPaths(ctx context.Context, repo string) ([]statusPath, error) {
	out, err := gitx.Run(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var paths []statusPath
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		code, path := entry[:2], filepath.ToSlash(entry[3:])
		paths = append(paths, statusPath{Path: path, Staged: code[0] != ' ' && code[0] != '?'})
		if code[0] == 'R' || code[0] == 'C' {
			i++ // porcelain -z emits the source path as the next field.
		}
	}
	return paths, nil
}

func isRecognizedArtifact(path string) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	for _, prefix := range []string{
		".specstory/history/", ".claude/plans/", ".cursor/plans/", ".cursor/rules/",
		".opencode/plans/", ".specify/", ".codex/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return path == ".specstory/statistics.json"
}

func normalizePlans(root string, paths []string) ([]string, error) {
	var out []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		inside, err := pathx.Contains(filepath.Join(root, ".claude", "plans"), path)
		if err != nil || !inside {
			return nil, fmt.Errorf("plan %s is outside .claude/plans", path)
		}
		canonical, err := pathx.Canonical(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("plan %s is not a regular file", path)
		}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out, nil
}

func gitTracked(ctx context.Context, repo, path string) (bool, error) {
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		return false, err
	}
	_, err = gitx.Run(ctx, repo, "ls-files", "--error-unmatch", "--", rel)
	if err == nil {
		return true, nil
	}
	var gitErr *gitx.Error
	if errors.As(err, &gitErr) {
		return false, nil
	}
	return false, err
}

func ensureOnlyAllowedIndex(ctx context.Context, repo string, allowed []string) error {
	out, err := gitx.Run(ctx, repo, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return err
	}
	allow := make(map[string]bool, len(allowed))
	for _, path := range allowed {
		rel, _ := filepath.Rel(repo, path)
		allow[filepath.ToSlash(rel)] = true
	}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" && !allow[filepath.ToSlash(path)] {
			return fmt.Errorf("index contains unrelated path %s", path)
		}
	}
	return nil
}

func stagedFilesMatch(ctx context.Context, repo string, paths []string) error {
	for _, path := range paths {
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		workingBlob, err := gitx.Run(ctx, repo, "hash-object", "--", rel)
		if err != nil {
			return err
		}
		stagedBlob, err := gitx.Run(ctx, repo, "rev-parse", ":"+filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if workingBlob != stagedBlob {
			return fmt.Errorf("staged artifact %s no longer matches scanned working bytes", filepath.ToSlash(rel))
		}
	}
	return nil
}

func findReceipt(ctx context.Context, repo, intentID string) (string, bool) {
	out, err := gitx.Run(ctx, repo, "log", "-n", "100", "--format=%H%x00%B%x00")
	if err != nil {
		return "", false
	}
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if strings.Contains(fields[i+1], "Dev-Artifact-Intent: "+intentID) {
			return fields[i], true
		}
	}
	return "", false
}
