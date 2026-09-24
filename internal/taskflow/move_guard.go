package taskflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// MoveClaims observes the claims that cannot silently follow a checkout rename.
// Filesystem/catalog identity and the move transaction remain the caller's
// responsibility. Unknown runtime coverage never proves that a move is safe.
type MoveClaims struct {
	Tasks     *task.Store
	Artifacts *artifact.Store
	Runtime   runtime.Runtime
	// Runtimes re-observes the available backend set at each guard boundary.
	Runtimes       func() []runtime.Runtime
	ProtectedPaths []string
}

// WithLock uses the lifecycle lock order before the caller enters its catalog
// transaction. Artifact writers use the same intent-store lease.
func (g MoveClaims) WithLock(ctx context.Context, commonDir string, operation func() error) error {
	if g.Tasks == nil || g.Artifacts == nil {
		return errors.New("move requires task and artifact stores")
	}
	return gitx.WithLifecycleMoveLock(ctx, commonDir, func() error {
		return g.Tasks.WithLock(ctx, func(*task.Tx) error {
			return lockx.WithDir(ctx, g.Artifacts.Dir, "artifact intent", operation)
		})
	})
}

// Inspect returns stable authority for binding a reviewed move. It is read-only
// and must be repeated under WithLock immediately before moving the checkout.
func (g MoveClaims) Inspect(ctx context.Context, source, destination string) (string, error) {
	if g.Tasks == nil || g.Artifacts == nil {
		return "", errors.New("move requires task and artifact observations")
	}
	for _, protected := range append(append([]string{}, g.ProtectedPaths...), g.Tasks.Dir, g.Artifacts.Dir) {
		if protected == "" {
			continue
		}
		inside, err := pathx.Contains(source, protected)
		if err != nil || inside {
			return "", errors.New("checkout contains durable dev state")
		}
	}
	repository, err := gitx.Discover(ctx, source)
	if err != nil {
		return "", err
	}
	if _, owned := inventory.DetectClaudeHarnessWorktree(repository.MainRoot, source); owned {
		return "", errors.New("harness-owned checkout cannot be moved")
	}
	status, err := gitx.StatusOf(ctx, source)
	if err != nil {
		return "", err
	}
	tasks, diagnostics, err := g.Tasks.ListRecords()
	if err != nil {
		return "", err
	}
	if len(diagnostics) != 0 {
		return "", errors.New("task inventory is incomplete")
	}
	var authority []string
	for _, record := range tasks {
		candidate := record.Task
		for _, claimed := range []string{candidate.RepoPath, candidate.WorktreePath} {
			if claimed == "" {
				continue
			}
			for _, target := range []string{source, destination} {
				related, err := movePathsRelated(target, claimed)
				if err != nil {
					return "", fmt.Errorf("inspect task %s path: %w", candidate.ID, err)
				}
				if related {
					return "", fmt.Errorf("checkout is claimed by task %s; retire or recover that task before moving", candidate.ID)
				}
			}
		}
		if candidate.Branch == status.Branch || status.Detached {
			other, err := gitx.Discover(ctx, candidate.RepoPath)
			if err != nil {
				return "", fmt.Errorf("task %s repository identity is unavailable: %w", candidate.ID, err)
			}
			if equalMovePath(other.GitCommonDir, repository.GitCommonDir) {
				return "", fmt.Errorf("checkout branch is claimed by task %s", candidate.ID)
			}
		}
		authority = append(authority, "task:"+candidate.ID+":"+record.Revision)
	}

	intents, err := g.Artifacts.List()
	if err != nil {
		return "", fmt.Errorf("artifact inventory is incomplete: %w", err)
	}
	for _, intent := range intents {
		record, err := g.Artifacts.GetRecord(intent.ID)
		if err != nil {
			return "", err
		}
		authority = append(authority, "artifact:"+intent.ID+":"+record.Revision)
		if intent.Status == artifact.Discarded || intent.Status == artifact.Finalized {
			continue
		}
		for _, target := range []string{source, destination} {
			related, err := movePathsRelated(target, intent.WorktreePath)
			if err != nil {
				return "", err
			}
			if related {
				return "", fmt.Errorf("artifact %s still claims this checkout; finalize or explicitly discard it before moving", intent.ID)
			}
		}
	}
	readiness, err := artifact.InspectReadiness(ctx, g.Artifacts, source)
	if err != nil {
		return "", err
	}
	if !readiness.Ready() {
		return "", errors.New("artifact finalization or history capture is not ready for this checkout")
	}
	if readiness.History != nil {
		authority = append(authority, "history:"+readiness.History.Fingerprint)
	}
	backends := []runtime.Runtime{g.Runtime}
	if g.Runtimes != nil {
		backends = g.Runtimes()
	}
	if len(backends) == 0 {
		return "", errors.New("runtime coverage is unknown; configure an observable runtime before moving the checkout")
	}
	for _, backend := range backends {
		if backend == nil || backend.Name() == "none" {
			return "", errors.New("runtime coverage is unknown; configure an observable runtime before moving the checkout")
		}
		observedRuntime := runtime.Runtime(moveRuntime{backend})
		if lister, ok := backend.(runtime.AgentActivityLister); ok {
			observedRuntime = moveRuntimeWithAgents{moveRuntime{backend}, lister}
		}
		occupancy, err := runtime.InspectOccupancy(ctx, observedRuntime, source, runtime.OccupancyOptions{Profile: runtime.OccupancyStrict})
		if err != nil {
			return "", err
		}
		if len(occupancy.Sessions) != 0 || len(occupancy.Agents) != 0 {
			return "", fmt.Errorf("a live %s runtime or agent occupies this checkout; close it before moving", backend.Name())
		}
		if occupancy.SessionList.Unknown() || occupancy.SessionCoverageErr != nil ||
			(occupancy.AgentActivityList.Supported && occupancy.AgentActivityList.Unknown()) {
			return "", fmt.Errorf("%s runtime coverage is incomplete; checkout move refused", backend.Name())
		}
		authority = append(authority, "runtime:"+occupancy.Backend,
			fmt.Sprintf("%s-agents:%t", occupancy.Backend, occupancy.AgentActivityList.Supported))
	}
	sort.Strings(authority)
	digest := sha256.Sum256([]byte(strings.Join(authority, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

// A session with no checkout observations cannot prove absence from a move
// target. Keep this stricter move policy out of the ordinary navigation probe.
type moveRuntime struct{ runtime.Runtime }

func (r moveRuntime) List(ctx context.Context) ([]runtime.Session, error) {
	sessions, err := r.Runtime.List(ctx)
	if err != nil {
		return sessions, err
	}
	sessions = append([]runtime.Session(nil), sessions...)
	for index, session := range sessions {
		paths := append(append([]string{}, session.Dirs...), session.WorkspaceCheckout)
		for _, pane := range session.Panes {
			if pane.CWD == "" && pane.ShellCWD == "" {
				return sessions, errors.New("runtime pane has no checkout observations")
			}
			paths = append(paths, pane.CWD, pane.ShellCWD)
		}
		known := false
		session.Panes = append([]runtime.Pane(nil), session.Panes...)
		for _, path := range paths {
			if path == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				return sessions, errors.New("runtime checkout observation is not absolute")
			}
			known = true
			// Preserve every explicit workspace/directory claim even when the
			// backend also provides panes for another part of the workspace.
			session.Panes = append(session.Panes, runtime.Pane{CWD: path})
		}
		if !known {
			return sessions, errors.New("runtime session has no checkout observations")
		}
		sessions[index] = session
	}
	return sessions, nil
}

type moveRuntimeWithAgents struct {
	moveRuntime
	lister runtime.AgentActivityLister
}

func (r moveRuntimeWithAgents) AgentActivities(ctx context.Context) ([]runtime.AgentActivity, error) {
	activities, err := r.lister.AgentActivities(ctx)
	if err != nil {
		return activities, err
	}
	for _, activity := range activities {
		if !filepath.IsAbs(activity.CWD) {
			return activities, errors.New("recognized agent checkout is unknown")
		}
	}
	return activities, nil
}

func movePathsRelated(a, b string) (bool, error) {
	within, err := pathx.Contains(a, b)
	if err != nil || within {
		return within, err
	}
	return pathx.Contains(b, a)
}

func equalMovePath(a, b string) bool {
	left, err := pathx.Canonical(a)
	if err != nil {
		return false
	}
	right, err := pathx.Canonical(b)
	return err == nil && left == right
}
