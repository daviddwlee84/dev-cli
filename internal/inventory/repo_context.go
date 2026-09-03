package inventory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// ObservationState distinguishes an observation that was not attempted from a
// successful empty observation and a failed observation. In particular, an
// empty successful runtime observation proves that no session covered a row;
// an unobserved or failed runtime does not.
type ObservationState string

const (
	ObservationUnobserved ObservationState = "unobserved"
	ObservationAvailable  ObservationState = "available"
	ObservationFailed     ObservationState = "failed"
)

// TopologyObservation records whether one source of repository topology was
// observed and retains the typed failure when it was not.
type TopologyObservation struct {
	State ObservationState
	Err   error
}

// Available reports that the observation completed, even when its result was
// empty.
func (o TopologyObservation) Available() bool { return o.State == ObservationAvailable }

// Failed reports that the observation was attempted but failed.
func (o TopologyObservation) Failed() bool { return o.State == ObservationFailed }

// WorktreeListError is the typed failure returned when Git's registered
// checkout inventory cannot be read.
type WorktreeListError struct {
	Path string
	Err  error
}

func (e *WorktreeListError) Error() string {
	return fmt.Sprintf("list registered worktrees from %s: %v", e.Path, e.Err)
}

func (e *WorktreeListError) Unwrap() error { return e.Err }

// RuntimeListError is the typed failure retained when runtime sessions could
// not be observed.
type RuntimeListError struct {
	Runtime string
	Err     error
}

func (e *RuntimeListError) Error() string {
	name := e.Runtime
	if name == "" {
		name = "runtime"
	}
	return fmt.Sprintf("list %s sessions: %v", name, e.Err)
}

func (e *RuntimeListError) Unwrap() error { return e.Err }

// RepositoryIdentityError reports that the shared Git common directory could
// not be made canonical. A context may still display the query failure, but it
// cannot claim a stable repository identity.
type RepositoryIdentityError struct {
	Path string
	Err  error
}

func (e *RepositoryIdentityError) Error() string {
	return fmt.Sprintf("canonicalize Git common directory %s: %v", e.Path, e.Err)
}

func (e *RepositoryIdentityError) Unwrap() error { return e.Err }

// TopologyReasonCode is a stable machine-readable explanation of topology
// drift or conflict. Drift describes recoverable disagreement with recorded
// intent; conflict means the evidence is not sufficient to authorize action.
type TopologyReasonCode string

const (
	ReasonWorktreePathMoved            TopologyReasonCode = "worktree-path-moved"
	ReasonWorktreePathMissing          TopologyReasonCode = "worktree-path-missing"
	ReasonCheckoutUnavailable          TopologyReasonCode = "checkout-unavailable"
	ReasonUnexpectedCheckout           TopologyReasonCode = "unexpected-checkout"
	ReasonBranchMismatch               TopologyReasonCode = "branch-mismatch"
	ReasonMainBranchMismatch           TopologyReasonCode = "main-branch-mismatch"
	ReasonTaskModeMismatch             TopologyReasonCode = "task-mode-mismatch"
	ReasonAmbiguousCheckout            TopologyReasonCode = "ambiguous-checkout"
	ReasonMultipleTaskClaims           TopologyReasonCode = "multiple-task-claims"
	ReasonHarnessTaskConflict          TopologyReasonCode = "harness-task-conflict"
	ReasonCanonicalPathUnavailable     TopologyReasonCode = "canonical-path-unavailable"
	ReasonWorktreeInventoryUnavailable TopologyReasonCode = "worktree-inventory-unavailable"
)

// TopologyReason couples a stable reason code with human-readable evidence.
type TopologyReason struct {
	Code   TopologyReasonCode
	Detail string
}

// TaskBindingKind explains why a task was associated with a registered
// checkout. It is evidence, not permission to mutate that checkout.
type TaskBindingKind string

const (
	TaskBindingExactPath  TaskBindingKind = "exact-path"
	TaskBindingMovedPath  TaskBindingKind = "moved-path"
	TaskBindingMainBranch TaskBindingKind = "main-branch"
	TaskBindingUnbound    TaskBindingKind = "unbound"
)

// TaskBinding records one task's single topology association, including any
// drift or conflict discovered while making it.
type TaskBinding struct {
	Task            *task.Task
	Kind            TaskBindingKind
	CheckoutID      string
	DriftReasons    []TopologyReason
	ConflictReasons []TopologyReason
}

// Bound reports whether this binding names a registered checkout.
func (b TaskBinding) Bound() bool { return b.CheckoutID != "" && b.Kind != TaskBindingUnbound }

// OwnershipEvidenceKind identifies a fact that contributes to the display
// ownership classification. Consumers must inspect all evidence and conflicts;
// CheckoutOwnership alone is never authorization to mutate a checkout.
type OwnershipEvidenceKind string

const (
	OwnershipEvidenceGitMain       OwnershipEvidenceKind = "git-main"
	OwnershipEvidenceManagedTask   OwnershipEvidenceKind = "managed-task"
	OwnershipEvidenceClaudeHarness OwnershipEvidenceKind = "claude-harness"
)

// OwnershipEvidence is one independently observed ownership fact.
type OwnershipEvidence struct {
	Kind   OwnershipEvidenceKind
	Path   string
	TaskID string
	Owner  string
}

// ClaudeHarnessEvidence proves that a registered checkout is canonically below
// the selected repository's own .claude/worktrees directory.
type ClaudeHarnessEvidence struct {
	Root         string
	CheckoutPath string
}

// DetectClaudeHarnessWorktree returns canonical path evidence only when the
// checkout is strictly below <repositoryRoot>/.claude/worktrees. A matching
// branch name or a lexical .claude segment elsewhere on disk is not evidence.
func DetectClaudeHarnessWorktree(repositoryRoot, checkoutPath string) (ClaudeHarnessEvidence, bool) {
	if repositoryRoot == "" || checkoutPath == "" {
		return ClaudeHarnessEvidence{}, false
	}
	harnessPath := filepath.Join(repositoryRoot, ".claude", "worktrees")
	canonicalCheckout, err := pathx.CanonicalChild(harnessPath, checkoutPath)
	if err != nil {
		return ClaudeHarnessEvidence{}, false
	}
	harnessRoot, err := pathx.Canonical(harnessPath)
	if err != nil {
		return ClaudeHarnessEvidence{}, false
	}
	return ClaudeHarnessEvidence{Root: harnessRoot, CheckoutPath: canonicalCheckout}, true
}

// IsClaudeHarnessWorktree reports whether strict canonical harness evidence is
// available for checkoutPath.
func IsClaudeHarnessWorktree(repositoryRoot, checkoutPath string) bool {
	_, ok := DetectClaudeHarnessWorktree(repositoryRoot, checkoutPath)
	return ok
}

// CheckoutOwnership is the legacy display classification for a checkout. It is
// intentionally retained for CLI and dashboard compatibility, but it is not an
// authorization decision; use OwnershipEvidence and ConflictReasons for that.
type CheckoutOwnership string

const (
	CheckoutCanonical CheckoutOwnership = "canonical"
	CheckoutDev       CheckoutOwnership = "dev"
	CheckoutExternal  CheckoutOwnership = "external"
	CheckoutEphemeral CheckoutOwnership = "ephemeral"
)

// RepoCheckout is one Git-registered checkout in a repository context. Git's
// Worktree record is retained unchanged; canonical fields are derived beside it
// for identity and matching.
type RepoCheckout struct {
	// ID is the stable host-local row identity: the canonical registered path.
	ID           string
	RepositoryID string
	IdentityErr  error
	Worktree     gitx.Worktree
	Status       gitx.Status
	// StatusErr is retained so a prunable or unreadable checkout can remain
	// visible without pretending its Git state was successfully inspected.
	StatusErr    error
	PathErr      error
	Exists       bool
	LastActivity time.Time
	LastCommit   time.Time
	LastSubject  string
	Sessions     []runtime.Session
	Tasks        []*task.Task
	// TaskBindings is the authoritative association; Tasks is its compatibility
	// projection for existing callers.
	TaskBindings       []TaskBinding
	OwnershipEvidence  []OwnershipEvidence
	HarnessEvidence    *ClaudeHarnessEvidence
	DriftReasons       []TopologyReason
	ConflictReasons    []TopologyReason
	RuntimeObservation TopologyObservation
	Ownership          CheckoutOwnership
}

// Branch returns the named branch from Git's registered worktree record,
// falling back to live status for compatibility with manually constructed
// RepoCheckout values.
func (c RepoCheckout) Branch() string {
	if c.Worktree.Branch != "" {
		return c.Worktree.Branch
	}
	return c.Status.Branch
}

// RepoContextRowKind distinguishes a registered checkout row from a task-only
// row produced when no checkout can safely bind that task.
type RepoContextRowKind string

const (
	RepoContextRowCheckout RepoContextRowKind = "checkout"
	RepoContextRowTask     RepoContextRowKind = "task"
)

// RepoContextRow is the authoritative exact-once row projection. Checkout rows
// appear in Git order; unmatched task rows follow in task-ID order.
type RepoContextRow struct {
	ID                  string
	RepositoryID        string
	Kind                RepoContextRowKind
	CheckoutIndex       int
	Checkout            *RepoCheckout
	Task                *task.Task
	Binding             TaskBinding
	DriftReasons        []TopologyReason
	ConflictReasons     []TopologyReason
	WorktreeObservation TopologyObservation
	RuntimeObservation  TopologyObservation
}

// TaskOnly reports whether the row exists solely to retain an unbound task.
func (r RepoContextRow) TaskOnly() bool { return r.Kind == RepoContextRowTask }

// RepoContext is the complete operational context for one discovered repo.
type RepoContext struct {
	Repo repo.Repo
	// RepositoryID is the canonical Git common directory and is stable across
	// aliases and every linked checkout on this host.
	RepositoryID string
	IdentityErr  error
	// Checkouts contains every authoritative gitx.Worktrees record in Git order.
	// It remains the compatibility projection used by current CLI/TUI callers.
	Checkouts []RepoCheckout
	// Rows is the authoritative exact-once checkout plus task-only projection.
	Rows []RepoContextRow
	// OtherTasks are tracked tasks which were not bound to a checkout. It remains
	// the compatibility projection of OtherTaskBindings.
	OtherTasks        []*task.Task
	OtherTaskBindings []TaskBinding
	Runtime           string
	// RuntimeErr and TaskErr preserve collection failures for readiness and
	// structured-report compatibility.
	RuntimeErr error
	TaskErr    error
	// WorktreeCount remains the linked-worktree count expected by existing
	// callers. On query failure it falls back to the administrative directory
	// count so the error can explain a previously displayed count.
	WorktreeCount       int
	WorktreeErr         error
	WorktreeObservation TopologyObservation
	RuntimeObservation  TopologyObservation
	LastActivity        time.Time
}

// Main returns Git's actual primary record. The first-row fallback preserves
// compatibility with manually constructed contexts which predate Worktree.Main.
func (c RepoContext) Main() (RepoCheckout, bool) {
	for _, checkout := range c.Checkouts {
		if checkout.Worktree.Main {
			return checkout, true
		}
	}
	if len(c.Checkouts) == 0 {
		return RepoCheckout{}, false
	}
	return c.Checkouts[0], true
}

// Linked returns every Git-registered non-main worktree.
func (c RepoContext) Linked() []RepoCheckout {
	hasMarkedMain := false
	for _, checkout := range c.Checkouts {
		if checkout.Worktree.Main {
			hasMarkedMain = true
			break
		}
	}
	if !hasMarkedMain {
		if len(c.Checkouts) < 2 {
			return nil
		}
		return c.Checkouts[1:]
	}
	out := make([]RepoCheckout, 0, len(c.Checkouts)-1)
	for _, checkout := range c.Checkouts {
		if !checkout.Worktree.Main {
			out = append(out, checkout)
		}
	}
	return out
}

// CheckoutIndexForPath returns the most-specific registered checkout
// containing path.
func (c RepoContext) CheckoutIndexForPath(path string) (int, bool) {
	best, bestLen := -1, -1
	for index := range c.Checkouts {
		for _, root := range checkoutRoots(c.Checkouts[index]) {
			if pathContains(root, path) && len(normalizePath(root)) > bestLen {
				best, bestLen = index, len(normalizePath(root))
			}
		}
	}
	return best, best >= 0
}

// Sessions returns the repo's live sessions once each, preserving checkout
// order so a pasted context reads canonical-first.
func (c RepoContext) Sessions() []runtime.Session {
	seen := map[string]bool{}
	var out []runtime.Session
	for _, checkout := range c.Checkouts {
		for _, session := range checkout.Sessions {
			key := session.Handle
			if key == "" {
				key = session.Label + "\x00" + strings.Join(session.Dirs, "\x00")
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, session)
		}
	}
	return out
}

// RepoContextOptions extends repository collection without changing the
// original CollectRepoContext API used by CLI and dashboard callers.
type RepoContextOptions struct {
	Runtime         string
	Sessions        []runtime.Session
	RuntimeObserved bool
	RuntimeErr      error
	IncludeActivity bool
	// Limiter shares one process-heavy enrichment bound across collectors.
	Limiter *Limiter
}

// CollectRepoContext joins Git, task and runtime state for one repository. The
// legacy entry point can prove observation only when sessions are present;
// callers that need a proven empty observation or typed failure use
// CollectRepoContextWithOptions.
func CollectRepoContext(ctx context.Context, r repo.Repo, tasks []*task.Task,
	sessions []runtime.Session, runtimeName string) RepoContext {
	return CollectRepoContextWithOptions(ctx, r, tasks, RepoContextOptions{
		Runtime: runtimeName, Sessions: sessions, RuntimeObserved: len(sessions) > 0,
		IncludeActivity: true,
	})
}

// CollectRepoContextWithOptions builds repository topology from Git's complete
// registered worktree list. It never synthesizes a main checkout after a Git
// query failure.
func CollectRepoContextWithOptions(ctx context.Context, r repo.Repo, tasks []*task.Task,
	opts RepoContextOptions) RepoContext {
	if ctx == nil {
		ctx = context.Background()
	}
	out := RepoContext{
		Repo: r, Runtime: opts.Runtime, RuntimeErr: opts.RuntimeErr,
		WorktreeObservation: TopologyObservation{State: ObservationUnobserved},
		RuntimeObservation:  runtimeObservation(opts),
	}
	queryPath := repoQueryPath(r)
	out.RepositoryID, out.IdentityErr = canonicalRepositoryID(ctx, r, queryPath)
	out.WorktreeCount = linkedAdminCount(r.CommonDir)

	var (
		worktrees []gitx.Worktree
		err       error
	)
	if queryPath == "" {
		err = fmt.Errorf("repository has no Git query path")
	} else {
		worktrees, err = gitx.Worktrees(ctx, queryPath)
	}
	if err != nil {
		listErr := &WorktreeListError{Path: queryPath, Err: err}
		out.WorktreeErr = listErr
		out.WorktreeObservation = TopologyObservation{State: ObservationFailed, Err: listErr}
		assignTasksWithoutTopology(&out, tasks)
		finishRepoContext(&out)
		return out
	}
	out.WorktreeObservation = TopologyObservation{State: ObservationAvailable}
	out.WorktreeCount = 0
	out.Checkouts = make([]RepoCheckout, 0, len(worktrees))
	for _, worktree := range worktrees {
		canonicalPath, canonicalErr := pathx.Canonical(worktree.Path)
		if canonicalPath == "" {
			canonicalPath = filepath.Clean(worktree.Path)
		}
		exists, pathErr := pathIsDir(worktree.Path)
		checkout := RepoCheckout{
			ID: canonicalPath, RepositoryID: out.RepositoryID,
			IdentityErr: canonicalErr,
			Worktree:    worktree, Exists: exists, PathErr: pathErr,
			RuntimeObservation: out.RuntimeObservation,
			Ownership:          CheckoutExternal,
		}
		if canonicalErr != nil {
			checkout.ConflictReasons = appendReason(checkout.ConflictReasons, TopologyReason{
				Code: ReasonCanonicalPathUnavailable, Detail: canonicalErr.Error(),
			})
		}
		if worktree.Main {
			checkout.OwnershipEvidence = append(checkout.OwnershipEvidence, OwnershipEvidence{
				Kind: OwnershipEvidenceGitMain, Path: canonicalPath,
			})
		} else {
			out.WorktreeCount++
		}
		out.Checkouts = append(out.Checkouts, checkout)
	}

	enrichRepoCheckouts(ctx, out.Checkouts, opts.Limiter, opts.IncludeActivity)
	assignHarnessEvidence(&out)
	assignTasks(&out, tasks)
	if out.RuntimeObservation.Available() {
		assignSessions(&out, opts.Sessions)
	}
	finishRepoContext(&out)
	return out
}

func runtimeObservation(opts RepoContextOptions) TopologyObservation {
	if opts.RuntimeErr != nil {
		err := &RuntimeListError{Runtime: opts.Runtime, Err: opts.RuntimeErr}
		return TopologyObservation{State: ObservationFailed, Err: err}
	}
	if opts.RuntimeObserved {
		return TopologyObservation{State: ObservationAvailable}
	}
	return TopologyObservation{State: ObservationUnobserved}
}

func repoQueryPath(r repo.Repo) string {
	for _, candidate := range []string{r.Path, r.RealPath, r.MainRoot, r.CommonDir} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func canonicalRepositoryID(ctx context.Context, r repo.Repo, queryPath string) (string, error) {
	commonDir := r.CommonDir
	if commonDir == "" && r.Bare {
		commonDir = queryPath
	}
	if commonDir == "" && queryPath != "" {
		if discovered, err := gitx.Discover(ctx, queryPath); err == nil {
			commonDir = discovered.GitCommonDir
		}
	}
	if commonDir == "" {
		err := fmt.Errorf("Git common directory is empty")
		return "", &RepositoryIdentityError{Path: commonDir, Err: err}
	}
	canonical, err := pathx.Canonical(commonDir)
	if err != nil {
		return "", &RepositoryIdentityError{Path: commonDir, Err: err}
	}
	return canonical, nil
}

func enrichRepoCheckouts(ctx context.Context, checkouts []RepoCheckout, limiter *Limiter, includeActivity bool) {
	if limiter == nil {
		limiter = NewLimiter(8)
	}
	var wg sync.WaitGroup
	for i := range checkouts {
		if checkouts[i].Worktree.Bare || checkouts[i].Worktree.Prunable || !checkouts[i].Exists || checkouts[i].PathErr != nil {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			release, ok := limiter.Acquire(ctx)
			if !ok {
				checkouts[index].StatusErr = ctx.Err()
				return
			}
			defer release()
			checkout := &checkouts[index]
			checkout.Status, checkout.StatusErr = gitx.StatusOf(ctx, checkout.Worktree.Path)
			if includeActivity {
				checkout.LastActivity, checkout.LastCommit, checkout.LastSubject = checkoutActivity(
					ctx, checkout.Worktree.Path, checkout.Status,
				)
			}
		}(i)
	}
	wg.Wait()
}

func assignHarnessEvidence(out *RepoContext) {
	mainRoot := ""
	for i := range out.Checkouts {
		if out.Checkouts[i].Worktree.Main && !out.Checkouts[i].Worktree.Bare {
			mainRoot = out.Checkouts[i].Worktree.Path
			break
		}
	}
	if mainRoot == "" {
		return
	}
	for i := range out.Checkouts {
		checkout := &out.Checkouts[i]
		if checkout.Worktree.Main {
			continue
		}
		evidence, ok := DetectClaudeHarnessWorktree(mainRoot, checkout.Worktree.Path)
		if !ok {
			continue
		}
		checkout.HarnessEvidence = &evidence
		checkout.OwnershipEvidence = append(checkout.OwnershipEvidence, OwnershipEvidence{
			Kind: OwnershipEvidenceClaudeHarness, Path: evidence.Root,
		})
	}
}

// LooksEphemeralWorktree recognizes a path or branch convention commonly used
// by turn-scoped agent isolation. It is a display and candidate-discovery hint
// only; cleanup must independently verify provider ownership and every safety
// fact.
func LooksEphemeralWorktree(path, branch string) bool {
	slashPath := filepath.ToSlash(path)
	return strings.Contains(slashPath, "/.claude/worktrees/") ||
		strings.HasPrefix(branch, "worktree-")
}

// IsEphemeralWorktree is retained for source compatibility. Callers must treat
// its result with the same hint-only semantics as LooksEphemeralWorktree.
func IsEphemeralWorktree(path, branch string) bool {
	return LooksEphemeralWorktree(path, branch)
}

func linkedAdminCount(commonDir string) int {
	if commonDir == "" {
		return 0
	}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() {
			n++
		}
	}
	return n
}

func pathIsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("checkout path is not a directory")
	}
	return true, nil
}

func checkoutActivity(ctx context.Context, path string, status gitx.Status) (time.Time, time.Time, string) {
	latest := status.LatestChange
	var commit time.Time
	var subject string
	if unix, lastSubject, err := gitx.LastCommit(ctx, path); err == nil && unix > 0 {
		commit = time.Unix(unix, 0)
		if commit.After(latest) {
			latest = commit
		}
		subject = lastSubject
	}
	return latest, commit, subject
}

func assignTasksWithoutTopology(out *RepoContext, tasks []*task.Task) {
	for _, tracked := range sortedUniqueTasks(tasks) {
		binding := TaskBinding{
			Task: tracked, Kind: TaskBindingUnbound,
			ConflictReasons: []TopologyReason{{
				Code:   ReasonWorktreeInventoryUnavailable,
				Detail: "registered worktrees could not be observed",
			}},
		}
		out.OtherTasks = append(out.OtherTasks, tracked)
		out.OtherTaskBindings = append(out.OtherTaskBindings, binding)
	}
}

func assignTasks(out *RepoContext, tasks []*task.Task) {
	paths := map[string][]int{}
	branches := map[string][]int{}
	mainIndex := -1
	for i := range out.Checkouts {
		checkout := &out.Checkouts[i]
		if checkout.ID != "" {
			paths[checkout.ID] = append(paths[checkout.ID], i)
		}
		if branch := checkout.Branch(); branch != "" && !checkout.Worktree.Bare {
			branches[branch] = append(branches[branch], i)
		}
		if checkout.Worktree.Main {
			mainIndex = i
		}
	}

	for _, tracked := range sortedUniqueTasks(tasks) {
		binding := TaskBinding{Task: tracked, Kind: TaskBindingUnbound}
		mode := tracked.EffectiveMode()
		var recordedPathIssue *TopologyReason
		if tracked.WorktreePath != "" {
			canonical, err := pathx.Canonical(tracked.WorktreePath)
			if err != nil {
				reason := TopologyReason{Code: ReasonCanonicalPathUnavailable, Detail: err.Error()}
				recordedPathIssue = &reason
			} else if mode == task.ModeWorktree {
				if matches := paths[canonical]; len(matches) > 0 {
					if len(matches) == 1 {
						binding.Kind = TaskBindingExactPath
						bindTask(out, matches[0], annotateBoundTask(binding, out.Checkouts[matches[0]]))
						continue
					}
					reason := TopologyReason{
						Code:   ReasonAmbiguousCheckout,
						Detail: fmt.Sprintf("task %s path matches %d registered checkout records", tracked.ID, len(matches)),
					}
					binding.ConflictReasons = appendReason(binding.ConflictReasons, reason)
					for _, index := range matches {
						out.Checkouts[index].ConflictReasons = appendReason(out.Checkouts[index].ConflictReasons, reason)
					}
					appendOtherTask(out, binding)
					continue
				}
			}
			if mode != task.ModeWorktree {
				binding.ConflictReasons = appendReason(binding.ConflictReasons, TopologyReason{
					Code: ReasonTaskModeMismatch,
					Detail: fmt.Sprintf("%s task %s records worktree_path %s",
						mode, tracked.ID, tracked.WorktreePath),
				})
			}
		}

		if mode == task.ModeWorktree {
			matches := branches[tracked.Branch]
			switch len(matches) {
			case 1:
				binding.Kind = TaskBindingMovedPath
				binding.DriftReasons = appendReason(binding.DriftReasons, movedPathReason(tracked, out.Checkouts[matches[0]]))
				if recordedPathIssue != nil {
					binding.ConflictReasons = appendReason(binding.ConflictReasons, *recordedPathIssue)
				}
				bindTask(out, matches[0], annotateBoundTask(binding, out.Checkouts[matches[0]]))
				continue
			case 0:
				if recordedPathIssue != nil {
					binding.ConflictReasons = appendReason(binding.ConflictReasons, *recordedPathIssue)
				}
				if tracked.WorktreePath != "" || taskExpectsCheckout(tracked) {
					binding.DriftReasons = appendReason(binding.DriftReasons, TopologyReason{
						Code:   ReasonWorktreePathMissing,
						Detail: fmt.Sprintf("task %s has no registered checkout for branch %s", tracked.ID, tracked.Branch),
					})
				}
			default:
				reason := TopologyReason{
					Code:   ReasonAmbiguousCheckout,
					Detail: fmt.Sprintf("task %s branch %s matches %d registered checkouts", tracked.ID, tracked.Branch, len(matches)),
				}
				binding.ConflictReasons = appendReason(binding.ConflictReasons, reason)
				for _, index := range matches {
					out.Checkouts[index].ConflictReasons = appendReason(out.Checkouts[index].ConflictReasons, reason)
				}
			}
			appendOtherTask(out, binding)
			continue
		}

		if mainIndex >= 0 {
			main := out.Checkouts[mainIndex]
			if checkoutActionable(main) && main.Branch() == tracked.Branch {
				binding.Kind = TaskBindingMainBranch
				if recordedPathIssue != nil {
					binding.ConflictReasons = appendReason(binding.ConflictReasons, *recordedPathIssue)
				}
				bindTask(out, mainIndex, annotateBoundTask(binding, main))
				continue
			}
		}

		if recordedPathIssue != nil {
			binding.ConflictReasons = appendReason(binding.ConflictReasons, *recordedPathIssue)
		}
		for _, index := range branches[tracked.Branch] {
			if index == mainIndex {
				continue
			}
			reason := TopologyReason{
				Code: ReasonTaskModeMismatch,
				Detail: fmt.Sprintf("%s task %s branch is checked out outside Git main at %s",
					mode, tracked.ID, out.Checkouts[index].Worktree.Path),
			}
			binding.ConflictReasons = appendReason(binding.ConflictReasons, reason)
			out.Checkouts[index].ConflictReasons = appendReason(out.Checkouts[index].ConflictReasons, reason)
		}
		if taskExpectsCheckout(tracked) {
			detail := fmt.Sprintf("task %s expects branch %s in Git main", tracked.ID, tracked.Branch)
			switch {
			case mainIndex < 0:
				detail += ", but no Git main record exists"
			case !checkoutActionable(out.Checkouts[mainIndex]):
				detail += ", but Git main is missing, prunable, bare, or unreadable"
			default:
				detail += fmt.Sprintf(", which is on %s", displayBranch(out.Checkouts[mainIndex]))
			}
			binding.DriftReasons = appendReason(binding.DriftReasons, TopologyReason{
				Code: ReasonMainBranchMismatch, Detail: detail,
			})
		}
		appendOtherTask(out, binding)
	}

	finalizeCheckoutClaims(out)
}

func sortedUniqueTasks(tasks []*task.Task) []*task.Task {
	ordered := append([]*task.Task(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i] == nil {
			return false
		}
		if ordered[j] == nil {
			return true
		}
		return ordered[i].ID < ordered[j].ID
	})
	seen := map[string]bool{}
	out := make([]*task.Task, 0, len(ordered))
	for _, tracked := range ordered {
		if tracked == nil {
			continue
		}
		if tracked.ID != "" && seen[tracked.ID] {
			continue
		}
		if tracked.ID != "" {
			seen[tracked.ID] = true
		}
		out = append(out, tracked)
	}
	return out
}

func movedPathReason(tracked *task.Task, checkout RepoCheckout) TopologyReason {
	if tracked.WorktreePath == "" {
		return TopologyReason{
			Code: ReasonWorktreePathMoved,
			Detail: fmt.Sprintf("task %s has no recorded worktree path; branch %s is registered at %s",
				tracked.ID, tracked.Branch, checkout.Worktree.Path),
		}
	}
	return TopologyReason{
		Code: ReasonWorktreePathMoved,
		Detail: fmt.Sprintf("task %s records %s; branch %s is registered at %s",
			tracked.ID, tracked.WorktreePath, tracked.Branch, checkout.Worktree.Path),
	}
}

func annotateBoundTask(binding TaskBinding, checkout RepoCheckout) TaskBinding {
	tracked := binding.Task
	if tracked == nil {
		return binding
	}
	if !checkoutActionable(checkout) {
		binding.DriftReasons = appendReason(binding.DriftReasons, TopologyReason{
			Code: ReasonCheckoutUnavailable,
			Detail: fmt.Sprintf("registered checkout %s is missing, prunable, bare, or unreadable",
				checkout.Worktree.Path),
		})
	}
	if checkout.Branch() != tracked.Branch {
		binding.ConflictReasons = appendReason(binding.ConflictReasons, TopologyReason{
			Code: ReasonBranchMismatch,
			Detail: fmt.Sprintf("task %s expects branch %s; registered checkout is on %s",
				tracked.ID, tracked.Branch, displayBranch(checkout)),
		})
	}
	mode := tracked.EffectiveMode()
	switch {
	case mode == task.ModeWorktree && checkout.Worktree.Main:
		binding.ConflictReasons = appendReason(binding.ConflictReasons, TopologyReason{
			Code:   ReasonTaskModeMismatch,
			Detail: fmt.Sprintf("worktree task %s resolved to Git main", tracked.ID),
		})
	case mode != task.ModeWorktree && !checkout.Worktree.Main:
		binding.ConflictReasons = appendReason(binding.ConflictReasons, TopologyReason{
			Code: ReasonTaskModeMismatch,
			Detail: fmt.Sprintf("%s task %s resolved to linked worktree %s",
				mode, tracked.ID, checkout.Worktree.Path),
		})
	}
	if mode != task.ModeWorktree && tracked.WorktreePath != "" {
		binding.ConflictReasons = appendReason(binding.ConflictReasons, TopologyReason{
			Code: ReasonTaskModeMismatch,
			Detail: fmt.Sprintf("%s task %s records worktree_path %s",
				mode, tracked.ID, tracked.WorktreePath),
		})
	}
	if !taskExpectsCheckout(tracked) {
		binding.DriftReasons = appendReason(binding.DriftReasons, TopologyReason{
			Code: ReasonUnexpectedCheckout,
			Detail: fmt.Sprintf("%s task %s still has registered checkout %s",
				tracked.State, tracked.ID, checkout.Worktree.Path),
		})
	}
	return binding
}

func taskExpectsCheckout(tracked *task.Task) bool {
	if tracked == nil || tracked.State == task.Cold {
		return false
	}
	if tracked.State == task.Done && tracked.EffectiveMode() == task.ModeWorktree {
		return tracked.WorktreePath != ""
	}
	return true
}

func checkoutActionable(checkout RepoCheckout) bool {
	return !checkout.Worktree.Bare && !checkout.Worktree.Prunable && checkout.Exists && checkout.PathErr == nil && checkout.StatusErr == nil
}

func bindTask(out *RepoContext, index int, binding TaskBinding) {
	checkout := &out.Checkouts[index]
	binding.CheckoutID = checkout.ID
	checkout.TaskBindings = append(checkout.TaskBindings, binding)
	checkout.Tasks = append(checkout.Tasks, binding.Task)
	if binding.Task.EffectiveMode() == task.ModeWorktree {
		checkout.OwnershipEvidence = append(checkout.OwnershipEvidence, OwnershipEvidence{
			Kind: OwnershipEvidenceManagedTask, Path: checkout.ID,
			TaskID: binding.Task.ID, Owner: binding.Task.Owner,
		})
	}
}

func appendOtherTask(out *RepoContext, binding TaskBinding) {
	out.OtherTasks = append(out.OtherTasks, binding.Task)
	out.OtherTaskBindings = append(out.OtherTaskBindings, binding)
}

func finalizeCheckoutClaims(out *RepoContext) {
	for i := range out.Checkouts {
		checkout := &out.Checkouts[i]
		sort.SliceStable(checkout.TaskBindings, func(a, b int) bool {
			return checkout.TaskBindings[a].Task.ID < checkout.TaskBindings[b].Task.ID
		})
		sort.SliceStable(checkout.Tasks, func(a, b int) bool {
			return checkout.Tasks[a].ID < checkout.Tasks[b].ID
		})
		if len(checkout.TaskBindings) > 1 {
			ids := make([]string, len(checkout.TaskBindings))
			for j := range checkout.TaskBindings {
				ids[j] = checkout.TaskBindings[j].Task.ID
			}
			reason := TopologyReason{
				Code:   ReasonMultipleTaskClaims,
				Detail: fmt.Sprintf("registered checkout is claimed by tasks %s", strings.Join(ids, ", ")),
			}
			checkout.ConflictReasons = appendReason(checkout.ConflictReasons, reason)
			for j := range checkout.TaskBindings {
				checkout.TaskBindings[j].ConflictReasons = appendReason(
					checkout.TaskBindings[j].ConflictReasons, reason,
				)
			}
		}
		if checkout.HarnessEvidence != nil && len(checkout.TaskBindings) > 0 {
			reason := TopologyReason{
				Code: ReasonHarnessTaskConflict,
				Detail: fmt.Sprintf("checkout has Claude harness evidence and %d managed task claim(s)",
					len(checkout.TaskBindings)),
			}
			checkout.ConflictReasons = appendReason(checkout.ConflictReasons, reason)
			for j := range checkout.TaskBindings {
				checkout.TaskBindings[j].ConflictReasons = appendReason(
					checkout.TaskBindings[j].ConflictReasons, reason,
				)
			}
		}
		for _, binding := range checkout.TaskBindings {
			for _, reason := range binding.DriftReasons {
				checkout.DriftReasons = appendReason(checkout.DriftReasons, reason)
			}
			for _, reason := range binding.ConflictReasons {
				checkout.ConflictReasons = appendReason(checkout.ConflictReasons, reason)
			}
		}
		checkout.Ownership = displayOwnership(*checkout)
	}
}

func displayOwnership(checkout RepoCheckout) CheckoutOwnership {
	if checkout.Worktree.Main {
		return CheckoutCanonical
	}
	if checkout.HarnessEvidence != nil {
		return CheckoutEphemeral
	}
	for _, evidence := range checkout.OwnershipEvidence {
		if evidence.Kind == OwnershipEvidenceManagedTask {
			return CheckoutDev
		}
	}
	return CheckoutExternal
}

func appendReason(reasons []TopologyReason, candidate TopologyReason) []TopologyReason {
	for _, reason := range reasons {
		if reason.Code == candidate.Code && reason.Detail == candidate.Detail {
			return reasons
		}
	}
	return append(reasons, candidate)
}

func finishRepoContext(out *RepoContext) {
	for _, checkout := range out.Checkouts {
		if checkout.LastActivity.After(out.LastActivity) {
			out.LastActivity = checkout.LastActivity
		}
		for _, tracked := range checkout.Tasks {
			if tracked.Updated.After(out.LastActivity) {
				out.LastActivity = tracked.Updated
			}
		}
	}
	for _, tracked := range out.OtherTasks {
		if tracked.Updated.After(out.LastActivity) {
			out.LastActivity = tracked.Updated
		}
	}

	out.Rows = make([]RepoContextRow, 0, len(out.Checkouts)+len(out.OtherTaskBindings))
	for i := range out.Checkouts {
		checkout := &out.Checkouts[i]
		out.Rows = append(out.Rows, RepoContextRow{
			ID: checkout.ID, RepositoryID: out.RepositoryID,
			Kind: RepoContextRowCheckout, CheckoutIndex: i, Checkout: checkout,
			DriftReasons:        append([]TopologyReason(nil), checkout.DriftReasons...),
			ConflictReasons:     append([]TopologyReason(nil), checkout.ConflictReasons...),
			WorktreeObservation: out.WorktreeObservation,
			RuntimeObservation:  out.RuntimeObservation,
		})
	}
	for _, binding := range out.OtherTaskBindings {
		out.Rows = append(out.Rows, RepoContextRow{
			ID: binding.Task.ID, RepositoryID: out.RepositoryID,
			Kind: RepoContextRowTask, CheckoutIndex: -1,
			Task: binding.Task, Binding: binding,
			DriftReasons:        append([]TopologyReason(nil), binding.DriftReasons...),
			ConflictReasons:     append([]TopologyReason(nil), binding.ConflictReasons...),
			WorktreeObservation: out.WorktreeObservation,
			RuntimeObservation:  out.RuntimeObservation,
		})
	}
}

// assignSessions assigns each observed directory to the most-specific
// checkout. A session rooted in repo/.claude/worktrees/x therefore belongs to
// x, not to the canonical checkout which also happens to contain that path.
func assignSessions(out *RepoContext, sessions []runtime.Session) {
	for _, session := range sessions {
		assigned := map[int]bool{}
		for _, dir := range session.Dirs {
			best, bestLen := -1, -1
			for i := range out.Checkouts {
				for _, root := range checkoutRoots(out.Checkouts[i]) {
					if pathContains(root, dir) && len(normalizePath(root)) > bestLen {
						best, bestLen = i, len(normalizePath(root))
					}
				}
			}
			if best >= 0 {
				assigned[best] = true
			}
		}
		for i := range assigned {
			out.Checkouts[i].Sessions = append(out.Checkouts[i].Sessions, session)
		}
	}
}

func checkoutRoots(checkout RepoCheckout) []string {
	return []string{checkout.Worktree.Path}
}

func pathContains(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(normalizePath(root), normalizePath(path))
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// normalizePath resolves symlinks in the longest existing prefix. macOS temp
// paths commonly arrive as both /var/... and /private/var/...; runtime CWDs may
// also name a not-yet-created descendant, so EvalSymlinks on the whole string
// is not sufficient.
func normalizePath(path string) string {
	if canonical, err := pathx.Canonical(path); err == nil {
		return canonical
	}
	return filepath.Clean(path)
}

// FormatRepoContext renders the deterministic Markdown used by both the CLI
// and the TUI's yy binding. checkoutIndex < 0 means the whole repository.
func FormatRepoContext(c RepoContext, checkoutIndex int) string {
	var b strings.Builder
	if checkoutIndex >= 0 && checkoutIndex < len(c.Checkouts) {
		checkout := c.Checkouts[checkoutIndex]
		fmt.Fprintf(&b, "# dev worktree context: %s\n\n", displayBranch(checkout))
		writeCheckoutMarkdown(&b, c, checkout, false)
		return strings.TrimSpace(b.String()) + "\n"
	}

	fmt.Fprintf(&b, "# dev repo context: %s\n\n", c.Repo.Display())
	fmt.Fprintf(&b, "- Repository: `%s`\n", c.Repo.Display())
	mainPath := c.Repo.Path
	if main, ok := c.Main(); ok {
		mainPath = main.Worktree.Path
	}
	fmt.Fprintf(&b, "- Main path: `%s`\n", mainPath)
	fmt.Fprintf(&b, "- Linked worktrees: %d\n", c.WorktreeCount)
	if c.Runtime != "" && c.Runtime != "none" {
		fmt.Fprintf(&b, "- Runtime: `%s`\n", c.Runtime)
	}
	if c.WorktreeErr != nil {
		fmt.Fprintf(&b, "- Worktree inventory error: %s\n", c.WorktreeErr)
	}
	if c.RuntimeErr != nil {
		fmt.Fprintf(&b, "- Runtime inventory error: %s\n", c.RuntimeErr)
	}
	if c.TaskErr != nil {
		fmt.Fprintf(&b, "- Task inventory error: %s\n", c.TaskErr)
	}
	b.WriteString("\n## Checkouts\n")
	for _, checkout := range c.Checkouts {
		b.WriteString("\n")
		writeCheckoutMarkdown(&b, c, checkout, checkout.Worktree.Main)
	}
	if len(c.OtherTasks) > 0 {
		b.WriteString("\n## Other tracked tasks\n\n")
		if len(c.OtherTaskBindings) > 0 {
			for _, binding := range c.OtherTaskBindings {
				writeTaskMarkdown(&b, binding.Task)
				writeTopologyReasons(&b, binding.DriftReasons, binding.ConflictReasons)
			}
		} else {
			for _, tracked := range c.OtherTasks {
				writeTaskMarkdown(&b, tracked)
			}
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func writeCheckoutMarkdown(b *strings.Builder, c RepoContext, checkout RepoCheckout, canonical bool) {
	label := string(checkout.Ownership)
	if canonical {
		label = "canonical"
	}
	fmt.Fprintf(b, "### %s — %s\n\n", displayBranch(checkout), label)
	fmt.Fprintf(b, "- Path: `%s`\n", checkout.Worktree.Path)
	fmt.Fprintf(b, "- Branch: `%s`\n", displayBranch(checkout))
	if checkout.Worktree.Bare {
		b.WriteString("- Git: bare repository\n")
	} else if checkout.PathErr != nil {
		fmt.Fprintf(b, "- Git: unavailable (%s)\n", checkout.PathErr)
	} else if checkout.Worktree.Prunable || !checkout.Exists {
		b.WriteString("- Git: unavailable (checkout path is missing)\n")
	} else if checkout.StatusErr != nil {
		fmt.Fprintf(b, "- Git: unavailable (%s)\n", checkout.StatusErr)
	} else {
		fmt.Fprintf(b, "- Git: %s", checkout.Status.Summary())
		if checkout.Status.Dirty() {
			fmt.Fprintf(b, " — %s", checkout.Status.Breakdown())
		}
		b.WriteString("\n")
		if checkout.Status.Upstream != "" {
			fmt.Fprintf(b, "- Upstream: `%s`\n", checkout.Status.Upstream)
		} else {
			b.WriteString("- Upstream: not published\n")
		}
	}
	if checkout.Worktree.Locked {
		reason := strings.TrimSpace(checkout.Worktree.LockedReason)
		if reason == "" {
			reason = "no reason recorded"
		}
		fmt.Fprintf(b, "- Worktree: locked — %s\n", reason)
	}
	if checkout.Worktree.Prunable || !checkout.Exists {
		reason := strings.TrimSpace(checkout.Worktree.PrunableReason)
		if reason == "" {
			reason = "checkout path is missing"
		}
		fmt.Fprintf(b, "- Worktree: prunable — %s\n", reason)
	}
	if c.RuntimeErr != nil {
		fmt.Fprintf(b, "- Runtime: unavailable (%s)\n", c.RuntimeErr)
	} else if len(checkout.Sessions) == 0 {
		b.WriteString("- Runtime: closed\n")
	} else {
		for _, session := range checkout.Sessions {
			status := session.AgentStatus
			if status == "" {
				status = "unknown"
			}
			fmt.Fprintf(b, "- Runtime: `%s %s` (%s)\n", c.Runtime, session.Handle, status)
			if len(session.AgentSessions) > 0 {
				fmt.Fprintf(b, "  - Agent sessions: %s\n", markdownCodes(session.AgentSessions))
			}
		}
	}
	if c.TaskErr != nil {
		fmt.Fprintf(b, "- Task: unavailable (%s)\n", c.TaskErr)
	} else if len(checkout.Tasks) == 0 {
		b.WriteString("- Task: untracked\n")
	} else {
		for _, tracked := range checkout.Tasks {
			writeTaskMarkdown(b, tracked)
		}
	}
	writeTopologyReasons(b, checkout.DriftReasons, checkout.ConflictReasons)
}

func writeTopologyReasons(b *strings.Builder, drift, conflicts []TopologyReason) {
	for _, reason := range drift {
		fmt.Fprintf(b, "- Drift: `%s` — %s\n", reason.Code, reason.Detail)
	}
	for _, reason := range conflicts {
		fmt.Fprintf(b, "- Conflict: `%s` — %s\n", reason.Code, reason.Detail)
	}
}

func writeTaskMarkdown(b *strings.Builder, tracked *task.Task) {
	fmt.Fprintf(b, "- Task: `%s` — %s", tracked.ID, tracked.State)
	if tracked.Next != "" {
		fmt.Fprintf(b, "; next: %s", tracked.Next)
	}
	b.WriteString("\n")
	if tracked.AgentSession != "" {
		fmt.Fprintf(b, "  - Recorded agent: `%s`\n", tracked.AgentSession)
	}
}

func displayBranch(checkout RepoCheckout) string {
	if branch := checkout.Branch(); branch != "" {
		return branch
	}
	if checkout.Worktree.Detached || checkout.Status.Detached {
		return "(detached HEAD)"
	}
	return "(unknown branch)"
}

func markdownCodes(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	return strings.Join(quoted, ", ")
}

// FormatSessions renders the ys payload. checkoutIndex < 0 selects the whole
// repository; otherwise it selects one checkout.
func FormatSessions(c RepoContext, checkoutIndex int) string {
	indices := make([]int, 0, len(c.Checkouts))
	if checkoutIndex >= 0 && checkoutIndex < len(c.Checkouts) {
		indices = append(indices, checkoutIndex)
	} else {
		for i := range c.Checkouts {
			indices = append(indices, i)
		}
	}
	var b strings.Builder
	for _, i := range indices {
		checkout := c.Checkouts[i]
		for _, session := range checkout.Sessions {
			status := session.AgentStatus
			if status == "" {
				status = "unknown"
			}
			fmt.Fprintf(&b, "- `%s`: `%s %s` (%s)\n",
				checkout.Worktree.Path, c.Runtime, session.Handle, status)
			if len(session.AgentSessions) > 0 {
				fmt.Fprintf(&b, "  - agents: %s\n", markdownCodes(session.AgentSessions))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// LinkedWorktreePaths returns the yw payload: absolute linked-worktree paths,
// one per line. The canonical checkout is intentionally excluded because WT
// counts linked worktrees only.
func LinkedWorktreePaths(c RepoContext) string {
	paths := make([]string, 0, len(c.Linked()))
	for _, checkout := range c.Linked() {
		paths = append(paths, checkout.Worktree.Path)
	}
	return strings.Join(paths, "\n")
}
