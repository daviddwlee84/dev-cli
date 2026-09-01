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
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// CheckoutOwnership says who is expected to clean up a checkout. Canonical and
// dev-owned checkouts are part of dev's durable inventory; external and
// ephemeral checkouts are still shown because hiding them would make the WT
// count impossible to explain.
type CheckoutOwnership string

const (
	CheckoutCanonical CheckoutOwnership = "canonical"
	CheckoutDev       CheckoutOwnership = "dev"
	CheckoutExternal  CheckoutOwnership = "external"
	CheckoutEphemeral CheckoutOwnership = "ephemeral"
)

// RepoCheckout is one checkout in a repository context. The first checkout is
// always the canonical checkout; the rest are linked worktrees in Git order.
type RepoCheckout struct {
	Worktree gitx.Worktree
	Status   gitx.Status
	// StatusErr is retained so a prunable or unreadable checkout can remain
	// visible without pretending its Git state was successfully inspected.
	StatusErr error
	// PathErr distinguishes an unreadable checkout path from a checkout which is
	// conclusively missing. Callers must not turn either case into a clean Git
	// status.
	PathErr      error
	Exists       bool
	LastActivity time.Time
	LastCommit   time.Time
	LastSubject  string
	Sessions     []runtime.Session
	Tasks        []*task.Task
	Ownership    CheckoutOwnership
}

// Branch returns the named branch, falling back to the live status for the
// synthetic canonical checkout.
func (c RepoCheckout) Branch() string {
	if c.Worktree.Branch != "" {
		return c.Worktree.Branch
	}
	return c.Status.Branch
}

// RepoContext is the complete operational context for one discovered repo.
type RepoContext struct {
	Repo repo.Repo
	// Checkouts contains the canonical checkout followed by linked worktrees.
	Checkouts []RepoCheckout
	// OtherTasks are tracked branches which are not checked out here (for
	// example cold or branch-only tasks on another branch).
	OtherTasks []*task.Task
	Runtime    string
	// RuntimeErr and TaskErr retain collection failures so a report cannot
	// mislabel unavailable observations as a closed runtime or an untracked task.
	RuntimeErr error
	TaskErr    error
	// WorktreeCount is kept separately so a failed Git query can still explain
	// the number shown from the administrative worktrees directory.
	WorktreeCount int
	WorktreeErr   error
	LastActivity  time.Time
}

// Main returns the canonical checkout.
func (c RepoContext) Main() (RepoCheckout, bool) {
	if len(c.Checkouts) == 0 {
		return RepoCheckout{}, false
	}
	return c.Checkouts[0], true
}

// Linked returns only linked worktrees.
func (c RepoContext) Linked() []RepoCheckout {
	if len(c.Checkouts) < 2 {
		return nil
	}
	return c.Checkouts[1:]
}

// CheckoutIndexForPath returns the most-specific checkout containing path. It
// uses the same symlink-aware matching as runtime assignment, so a current
// directory under a nested linked worktree never resolves to the canonical
// checkout merely because that checkout is its lexical parent.
func (c RepoContext) CheckoutIndexForPath(path string) (int, bool) {
	best, bestLen := -1, -1
	for index := range c.Checkouts {
		for _, root := range checkoutRoots(c.Repo, index, c.Checkouts[index]) {
			if pathContains(root, path) && len(filepath.Clean(root)) > bestLen {
				best, bestLen = index, len(filepath.Clean(root))
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

// RepoContextOptions controls enrichment that does not affect readiness.
type RepoContextOptions struct {
	IncludeActivity bool
}

// CollectRepoContext joins Git, task and runtime state for one repository with
// commit/activity enrichment enabled.
func CollectRepoContext(ctx context.Context, r repo.Repo, tasks []*task.Task,
	sessions []runtime.Session, runtimeName string) RepoContext {
	return CollectRepoContextWithOptions(ctx, r, tasks, sessions, runtimeName, RepoContextOptions{IncludeActivity: true})
}

// CollectRepoContextWithOptions is the configurable form used by the fast
// current-directory status path. Repositories with no linked-worktree admin
// entries deliberately skip `git worktree list`.
func CollectRepoContextWithOptions(ctx context.Context, r repo.Repo, tasks []*task.Task,
	sessions []runtime.Session, runtimeName string, options RepoContextOptions) RepoContext {

	out := RepoContext{Repo: r, Runtime: runtimeName}
	mainExists, mainPathErr := pathIsDir(r.Path)
	main := RepoCheckout{
		Worktree: gitx.Worktree{Path: r.Path, Main: true},
		Exists:   mainExists, PathErr: mainPathErr, Ownership: CheckoutCanonical,
	}
	if r.Bare {
		main.Worktree.Bare = true
	} else {
		main.Status, main.StatusErr = gitx.StatusOf(ctx, r.Path)
		main.Worktree.Branch = main.Status.Branch
		main.Worktree.Detached = main.Status.Detached
		if options.IncludeActivity {
			main.LastActivity, main.LastCommit, main.LastSubject = checkoutActivity(ctx, r.Path, main.Status)
		}
	}
	out.Checkouts = append(out.Checkouts, main)

	out.WorktreeCount, out.WorktreeErr = linkedAdminCount(r.CommonDir)
	if out.WorktreeErr == nil && out.WorktreeCount > 0 {
		worktrees, err := gitx.Worktrees(ctx, r.Path)
		if err != nil {
			out.WorktreeErr = err
		} else {
			out.WorktreeCount = 0
			for _, w := range worktrees {
				if w.Main {
					continue
				}
				exists, pathErr := pathIsDir(w.Path)
				checkout := RepoCheckout{
					Worktree: w, Exists: exists, PathErr: pathErr, Ownership: CheckoutExternal,
				}
				if IsEphemeralWorktree(w.Path, w.Branch) {
					checkout.Ownership = CheckoutEphemeral
				}
				out.Checkouts = append(out.Checkouts, checkout)
				out.WorktreeCount++
			}
		}
	}
	inspectLinkedCheckouts(ctx, &out, options)

	assignTasks(&out, tasks)
	assignSessions(&out, sessions)
	for _, checkout := range out.Checkouts {
		if checkout.LastActivity.After(out.LastActivity) {
			out.LastActivity = checkout.LastActivity
		}
		for _, t := range checkout.Tasks {
			if t.Updated.After(out.LastActivity) {
				out.LastActivity = t.Updated
			}
		}
	}
	for _, t := range out.OtherTasks {
		if t.Updated.After(out.LastActivity) {
			out.LastActivity = t.Updated
		}
	}
	return out
}

func inspectLinkedCheckouts(ctx context.Context, out *RepoContext, options RepoContextOptions) {
	if out == nil || len(out.Checkouts) < 2 {
		return
	}
	semaphore := make(chan struct{}, 8)
	var wait sync.WaitGroup
	for index := 1; index < len(out.Checkouts); index++ {
		checkout := &out.Checkouts[index]
		if !checkout.Exists || checkout.PathErr != nil || checkout.Worktree.Prunable {
			continue
		}
		wait.Add(1)
		go func(checkout *RepoCheckout) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			checkout.Status, checkout.StatusErr = gitx.StatusOf(ctx, checkout.Worktree.Path)
			if options.IncludeActivity {
				checkout.LastActivity, checkout.LastCommit, checkout.LastSubject = checkoutActivity(ctx, checkout.Worktree.Path, checkout.Status)
			}
		}(checkout)
	}
	wait.Wait()
}

// IsEphemeralWorktree recognises turn-scoped checkouts owned by an agent
// harness. They stay visible, but the label prevents an agent from treating
// them like durable dev-owned work.
func IsEphemeralWorktree(path, branch string) bool {
	slashPath := filepath.ToSlash(path)
	return strings.Contains(slashPath, "/.claude/worktrees/") ||
		strings.HasPrefix(branch, "worktree-")
}

func linkedAdminCount(commonDir string) (int, error) {
	if commonDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() {
			n++
		}
	}
	return n, nil
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

func assignTasks(out *RepoContext, tasks []*task.Task) {
	for _, t := range tasks {
		assigned := false
		if t.WorktreePath != "" {
			for i := 1; i < len(out.Checkouts); i++ {
				if samePath(t.WorktreePath, out.Checkouts[i].Worktree.Path) {
					out.Checkouts[i].Tasks = append(out.Checkouts[i].Tasks, t)
					out.Checkouts[i].Ownership = CheckoutDev
					assigned = true
					break
				}
			}
		} else if len(out.Checkouts) > 0 && t.Branch == out.Checkouts[0].Branch() {
			out.Checkouts[0].Tasks = append(out.Checkouts[0].Tasks, t)
			assigned = true
		}
		if !assigned {
			out.OtherTasks = append(out.OtherTasks, t)
		}
	}
	for i := range out.Checkouts {
		sort.SliceStable(out.Checkouts[i].Tasks, func(a, b int) bool {
			return out.Checkouts[i].Tasks[a].ID < out.Checkouts[i].Tasks[b].ID
		})
	}
	sort.SliceStable(out.OtherTasks, func(i, j int) bool {
		return out.OtherTasks[i].ID < out.OtherTasks[j].ID
	})
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
				for _, root := range checkoutRoots(out.Repo, i, out.Checkouts[i]) {
					if pathContains(root, dir) && len(filepath.Clean(root)) > bestLen {
						best, bestLen = i, len(filepath.Clean(root))
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

func checkoutRoots(r repo.Repo, index int, checkout RepoCheckout) []string {
	roots := []string{checkout.Worktree.Path}
	if index == 0 && r.RealPath != "" && !samePath(r.RealPath, r.Path) {
		roots = append(roots, r.RealPath)
	}
	return roots
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

func samePath(a, b string) bool {
	return a != "" && b != "" && normalizePath(a) == normalizePath(b)
}

// normalizePath resolves symlinks in the longest existing prefix. macOS temp
// paths commonly arrive as both /var/... and /private/var/...; runtime CWDs may
// also name a not-yet-created descendant, so EvalSymlinks on the whole string
// is not sufficient.
func normalizePath(path string) string {
	clean := filepath.Clean(path)
	cur := clean
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			rel, relErr := filepath.Rel(cur, clean)
			if relErr == nil && rel != "." {
				return filepath.Clean(filepath.Join(real, rel))
			}
			return filepath.Clean(real)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		cur = parent
	}
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
	fmt.Fprintf(&b, "- Main path: `%s`\n", c.Repo.Path)
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
	for i, checkout := range c.Checkouts {
		b.WriteString("\n")
		writeCheckoutMarkdown(&b, c, checkout, i == 0)
	}
	if len(c.OtherTasks) > 0 {
		b.WriteString("\n## Other tracked tasks\n\n")
		for _, t := range c.OtherTasks {
			writeTaskMarkdown(&b, t)
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
	if checkout.Worktree.Prunable || checkout.PathErr == nil && !checkout.Exists {
		reason := strings.TrimSpace(checkout.Worktree.PrunableReason)
		if reason == "" {
			reason = "checkout path is missing"
		}
		fmt.Fprintf(b, "- Worktree: prunable — %s\n", reason)
	}
	if len(checkout.Sessions) == 0 {
		if c.RuntimeErr != nil {
			fmt.Fprintf(b, "- Runtime: unavailable (%s)\n", c.RuntimeErr)
		} else {
			b.WriteString("- Runtime: closed\n")
		}
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
	if len(checkout.Tasks) == 0 {
		if c.TaskErr != nil {
			fmt.Fprintf(b, "- Task: unavailable (%s)\n", c.TaskErr)
		} else {
			b.WriteString("- Task: untracked\n")
		}
	} else {
		for _, t := range checkout.Tasks {
			writeTaskMarkdown(b, t)
		}
	}
}

func writeTaskMarkdown(b *strings.Builder, t *task.Task) {
	fmt.Fprintf(b, "- Task: `%s` — %s", t.ID, t.State)
	if t.Next != "" {
		fmt.Fprintf(b, "; next: %s", t.Next)
	}
	b.WriteString("\n")
	if t.AgentSession != "" {
		fmt.Fprintf(b, "  - Recorded agent: `%s`\n", t.AgentSession)
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
