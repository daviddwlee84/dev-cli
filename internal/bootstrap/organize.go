package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Layout controls how an organisation target names repositories.
type Layout string

const (
	// Flat places every repo directly under the target: <target>/<repo>.
	Flat Layout = "flat"
	// Preserve mirrors the repository's relative path under its scan root.
	Preserve Layout = "preserve"
)

// ParseLayout validates a layout.
func ParseLayout(s string) (Layout, error) {
	switch Layout(s) {
	case Flat, Preserve:
		return Layout(s), nil
	default:
		return "", fmt.Errorf("unknown layout %q: want flat or preserve", s)
	}
}

// ActionState is whether an organisation action can run.
type ActionState string

const (
	// Ready means applying will create or move something.
	Ready ActionState = "ready"
	// Current means the target already has the desired state.
	Current ActionState = "current"
	// Blocked means applying would be unsafe or ambiguous.
	Blocked ActionState = "blocked"
)

// Action is one index-link creation or physical move.
type Action struct {
	Repo   Repository
	Source string
	Target string
	State  ActionState
	Reason string
	// RelativeTarget is the symlink payload when relative links are requested.
	RelativeTarget string
}

// Plan holds a complete organisation proposal.
type OrganizePlan struct {
	Mode    string // "index" or "move"
	Root    string
	Layout  Layout
	Actions []Action
}

// Ready returns actions that will change the filesystem.
func (p OrganizePlan) Ready() []Action {
	var out []Action
	for _, a := range p.Actions {
		if a.State == Ready {
			out = append(out, a)
		}
	}
	return out
}

// Blocked returns unsafe or ambiguous actions.
func (p OrganizePlan) Blocked() []Action {
	var out []Action
	for _, a := range p.Actions {
		if a.State == Blocked {
			out = append(out, a)
		}
	}
	return out
}

// IndexPlan proposes a non-destructive symlink catalog of canonical checkouts.
// Linked worktrees are execution state, not projects, and are deliberately not
// indexed alongside repositories.
func IndexPlan(repos []Repository, root string, layout Layout, relative bool) OrganizePlan {
	p := OrganizePlan{Mode: "index", Root: root, Layout: layout}
	nameTargets := map[string][]int{}

	for _, r := range repos {
		if r.Kind != Canonical {
			continue
		}
		target := targetFor(root, layout, r)
		a := Action{Repo: r, Source: r.RealPath, Target: target, State: Ready}
		if relative {
			rel, err := filepath.Rel(filepath.Dir(target), r.RealPath)
			if err == nil {
				a.RelativeTarget = rel
			}
		}
		classifyIndexTarget(&a)
		nameTargets[target] = append(nameTargets[target], len(p.Actions))
		p.Actions = append(p.Actions, a)
	}

	// A flat index cannot represent duplicate repo names. Block every claimant
	// rather than picking one or inventing a suffix the user did not ask for.
	for target, indices := range nameTargets {
		if len(indices) < 2 {
			continue
		}
		var sources []string
		for _, i := range indices {
			sources = append(sources, p.Actions[i].Source)
		}
		reason := "duplicate target " + target + " from: " + strings.Join(sources, ", ")
		for _, i := range indices {
			p.Actions[i].State, p.Actions[i].Reason = Blocked, reason
		}
	}
	sortActions(p.Actions)
	return p
}

func classifyIndexTarget(a *Action) {
	info, err := os.Lstat(a.Target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return
	case err != nil:
		a.State, a.Reason = Blocked, err.Error()
		return
	case info.Mode()&os.ModeSymlink != 0:
		dest, err := filepath.EvalSymlinks(a.Target)
		if err != nil {
			a.State, a.Reason = Blocked, "existing broken symlink"
			return
		}
		dest, _ = filepath.Abs(dest)
		source, _ := filepath.EvalSymlinks(a.Source)
		if dest == source {
			a.State, a.Reason = Current, "already points at this repository"
			return
		}
		a.State, a.Reason = Blocked, "existing symlink points at "+dest
	default:
		a.State, a.Reason = Blocked, "target already exists and is not a symlink"
	}
}

// ApplyIndex creates the ready symlinks. It never replaces anything.
func ApplyIndex(plan OrganizePlan) (int, error) {
	if plan.Mode != "index" {
		return 0, fmt.Errorf("cannot apply %q as an index plan", plan.Mode)
	}
	created := 0
	for _, a := range plan.Actions {
		if a.State != Ready {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(a.Target), 0o755); err != nil {
			return created, err
		}
		payload := a.Source
		if a.RelativeTarget != "" {
			payload = a.RelativeTarget
		}
		// os.Symlink refuses an occupied target: this is the final race-safe
		// guard after the plan classified it.
		if err := os.Symlink(payload, a.Target); err != nil {
			return created, fmt.Errorf("link %s -> %s: %w", a.Target, payload, err)
		}
		created++
	}
	return created, nil
}

// MovePlan proposes atomic moves of canonical checkouts into root.
//
// Moving is intentionally much stricter than indexing. It is blocked when a
// repo is dirty, has linked worktrees, has symlink aliases, crosses a
// filesystem, or would hit an occupied target. Those are exactly the states
// where a simple os.Rename could lose access or leave Git metadata pointing at
// an old path.
func MovePlan(ctx context.Context, repos []Repository, root string, layout Layout) OrganizePlan {
	p := OrganizePlan{Mode: "move", Root: root, Layout: layout}
	nameTargets := map[string][]int{}

	for _, r := range repos {
		if r.Kind != Canonical {
			continue
		}
		target := targetFor(root, layout, r)
		a := Action{Repo: r, Source: r.Path, Target: target, State: Ready}

		switch {
		case r.Dirty():
			a.State, a.Reason = Blocked, "repository has uncommitted changes"
		case len(r.Aliases) > 0:
			a.State, a.Reason = Blocked, "repository has symlink aliases that would become broken"
		case samePath(r.Path, target):
			a.State, a.Reason = Current, "already at the target"
		default:
			if list, err := gitx.Worktrees(ctx, r.Path); err != nil {
				a.State, a.Reason = Blocked, "cannot inspect worktrees: "+err.Error()
			} else if len(list) > 1 {
				a.State, a.Reason = Blocked,
					fmt.Sprintf("repository has %d linked worktree(s); remove or move those first", len(list)-1)
			} else if info, err := os.Lstat(target); err == nil {
				_ = info
				a.State, a.Reason = Blocked, "target already exists"
			} else if !errors.Is(err, os.ErrNotExist) {
				a.State, a.Reason = Blocked, err.Error()
			} else if !sameFilesystem(r.Path, nearestExisting(filepath.Dir(target))) {
				a.State, a.Reason = Blocked,
					"source and target are on different filesystems; atomic rename is impossible"
			}
		}
		nameTargets[target] = append(nameTargets[target], len(p.Actions))
		p.Actions = append(p.Actions, a)
	}

	for target, indices := range nameTargets {
		if len(indices) < 2 {
			continue
		}
		reason := "duplicate target " + target + "; use preserve layout or separate roots"
		for _, i := range indices {
			p.Actions[i].State, p.Actions[i].Reason = Blocked, reason
		}
	}
	sortActions(p.Actions)
	return p
}

// ApplyMoves atomically renames every ready repository. It stops on the first
// failure and returns how many completed — there is no rollback because a
// rollback can fail too, and every completed rename is itself a valid repo.
func ApplyMoves(plan OrganizePlan) (int, error) {
	if plan.Mode != "move" {
		return 0, fmt.Errorf("cannot apply %q as a move plan", plan.Mode)
	}
	moved := 0
	for _, a := range plan.Actions {
		if a.State != Ready {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(a.Target), 0o755); err != nil {
			return moved, err
		}
		if err := os.Rename(a.Source, a.Target); err != nil {
			return moved, fmt.Errorf("move %s -> %s: %w", a.Source, a.Target, err)
		}
		moved++
	}
	return moved, nil
}

func targetFor(root string, layout Layout, r Repository) string {
	if layout == Preserve {
		return filepath.Join(root, filepath.FromSlash(r.Relative))
	}
	return filepath.Join(root, r.Name)
}

func samePath(a, b string) bool {
	aAbs, _ := filepath.Abs(a)
	bAbs, _ := filepath.Abs(b)
	return filepath.Clean(aAbs) == filepath.Clean(bAbs)
}

func nearestExisting(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

func sortActions(actions []Action) {
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].State != actions[j].State {
			return actionRank(actions[i].State) < actionRank(actions[j].State)
		}
		return actions[i].Target < actions[j].Target
	})
}

func actionRank(s ActionState) int {
	switch s {
	case Ready:
		return 0
	case Blocked:
		return 1
	case Current:
		return 2
	}
	return 3
}
