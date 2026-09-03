// Package agenttarget resolves the repository checkouts inspected by agent
// configuration inventories. It deliberately contains no skill, MCP, or UI
// policy so those domains can share one definition of a target.
package agenttarget

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// Target identifies one concrete checkout of one repository. RepoPath is the
// repository's main navigation path (which may be a configured symlink alias),
// while CheckoutRoot is the exact worktree to inspect. Linked worktrees share
// RepoPath and CommonDir but remain distinct targets through CheckoutRoot.
type Target struct {
	RepoName     string `json:"repo_name"`
	RepoDisplay  string `json:"repo_display"`
	RepoPath     string `json:"repo_path"`
	CheckoutRoot string `json:"checkout_root"`
	CommonDir    string `json:"common_dir"`
}

// Key is stable for one checkout of one clone. It is intentionally stricter
// than gitx.Repo.Key, which identifies the clone regardless of checkout.
func (t Target) Key() string {
	return comparablePath(t.CommonDir) + "\x00" + comparablePath(t.CheckoutRoot)
}

// Current resolves the checkout containing cwd. A linked worktree remains the
// target checkout; outside Git, the absolute directory remains a useful project
// scope for agents that support configuration in ordinary folders.
func Current(ctx context.Context, cwd string) (Target, error) {
	target, err := ResolvePath(ctx, cwd)
	if err == nil {
		return target, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Target{}, ctxErr
	}
	if !errors.Is(err, gitx.ErrNotARepo) {
		return Target{}, err
	}
	root, pathErr := absoluteDirectory(cwd)
	if pathErr != nil {
		return Target{}, pathErr
	}
	name := filepath.Base(root)
	return Target{
		RepoName: name, RepoDisplay: name, RepoPath: root,
		CheckoutRoot: root, CommonDir: root,
	}, nil
}

// ResolvePath resolves an explicit path to the checkout containing it.
func ResolvePath(ctx context.Context, value string) (Target, error) {
	absolute, err := absoluteDirectory(value)
	if err != nil {
		return Target{}, err
	}
	found, err := gitx.Discover(ctx, absolute)
	if err != nil {
		return Target{}, fmt.Errorf("resolve target path %q: %w", value, err)
	}
	return fromGit(found)
}

// ResolveRepository resolves an explicit repository reference using repo's
// normal name/path rules, then probes the selected path so an explicit linked
// worktree path is preserved.
func ResolveRepository(ctx context.Context, roots []string, ref string) (Target, error) {
	found, _, err := repo.Resolve(ctx, roots, ref)
	if err != nil {
		return Target{}, err
	}
	target, err := ResolvePath(ctx, found.Path)
	if err != nil {
		return Target{}, err
	}
	// A named canonical repository contributes its configured alias/category.
	// An explicit linked-worktree path keeps the main repository identity from
	// Git, but its checkout root remains the navigation path the caller selected.
	switch {
	case comparablePath(found.Path) == comparablePath(target.RepoPath):
		if found.Name != "" {
			target.RepoName = found.Name
		}
		if display := found.Display(); display != "" {
			target.RepoDisplay = display
		}
		target.RepoPath = navigationPath(found.Path)
		target.CheckoutRoot = target.RepoPath
	case comparablePath(found.Path) == comparablePath(target.CheckoutRoot):
		target.CheckoutRoot = navigationPath(found.Path)
	}
	return target, nil
}

// ResolveRepo is a concise compatibility name for ResolveRepository.
func ResolveRepo(ctx context.Context, roots []string, ref string) (Target, error) {
	return ResolveRepository(ctx, roots, ref)
}

// FromRepository converts one canonical repo discovery result without another
// filesystem walk or Git process. Bare and non-Git inventory entries have no
// checkout to inspect and are skipped.
func FromRepository(found repo.Repo) (Target, bool) {
	if !found.HasGit || found.Bare {
		return Target{}, false
	}
	checkout := firstNonEmpty(found.Path, found.MainRoot, found.RealPath)
	commonDir := found.CommonDir
	if checkout == "" || commonDir == "" {
		return Target{}, false
	}
	// Preserve the first discovery root's navigation alias. CommonDir remains
	// physical identity, so aliases still deduplicate correctly.
	checkout = navigationPath(checkout)
	repository := navigationPath(firstNonEmpty(found.Path, found.MainRoot, found.RealPath, checkout))
	commonDir = canonicalPath(commonDir)
	name := found.Name
	if name == "" {
		name = filepath.Base(repository)
	}
	display := found.Display()
	if display == "" {
		display = name
	}
	return Target{
		RepoName: name, RepoDisplay: display, RepoPath: repository,
		CheckoutRoot: checkout, CommonDir: commonDir,
	}, true
}

// FromRepo is a concise compatibility name for FromRepository.
func FromRepo(found repo.Repo) (Target, bool) { return FromRepository(found) }

// FromRepositories converts an already-discovered repository inventory. This is
// the --all-style path: callers can reuse repo.Discover output and avoid a
// second walk. Bare and non-Git entries are omitted.
func FromRepositories(repositories []repo.Repo) []Target {
	targets := make([]Target, 0, len(repositories))
	for _, found := range repositories {
		if target, ok := FromRepository(found); ok {
			targets = append(targets, target)
		}
	}
	return Dedupe(targets)
}

// FromRepos is a concise compatibility name for FromRepositories.
func FromRepos(repositories []repo.Repo) []Target { return FromRepositories(repositories) }

// WithCurrent appends the startup checkout when it is distinct from the
// canonical repository inventory, then returns the same deterministic order as
// other target builders.
func WithCurrent(targets []Target, current Target) []Target {
	out := append([]Target(nil), targets...)
	if current.CheckoutRoot != "" {
		current = ReconcileCurrent(targets, current)
		out = append(out, current)
	}
	return Dedupe(out)
}

// ReconcileCurrent preserves the exact current checkout while applying the
// canonical repository's configured navigation identity when it is known.
func ReconcileCurrent(targets []Target, current Target) Target {
	for _, target := range targets {
		if comparablePath(target.CommonDir) == comparablePath(current.CommonDir) {
			current.RepoName = target.RepoName
			current.RepoDisplay = target.RepoDisplay
			current.RepoPath = target.RepoPath
			if comparablePath(target.CheckoutRoot) == comparablePath(current.CheckoutRoot) {
				current.CheckoutRoot = target.CheckoutRoot
			}
			break
		}
	}
	return current
}

// All discovers configured repository roots once and converts the result to
// concrete main-checkout targets.
func All(ctx context.Context, roots []string) ([]Target, error) {
	repositories, err := repo.Discover(ctx, roots, repo.DefaultOptions())
	if err != nil {
		return nil, err
	}
	return FromRepositories(repositories), nil
}

// Dedupe returns one deterministic entry per common-directory plus checkout
// root. The first value supplies display metadata when aliases collide.
func Dedupe(targets []Target) []Target {
	seen := make(map[string]struct{}, len(targets))
	out := make([]Target, 0, len(targets))
	for _, target := range targets {
		key := target.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	Sort(out)
	return out
}

// Sort orders targets by display name, repository, checkout, then common dir.
func Sort(targets []Target) {
	sort.SliceStable(targets, func(i, j int) bool {
		left, right := targets[i], targets[j]
		leftDisplay, rightDisplay := strings.ToLower(left.RepoDisplay), strings.ToLower(right.RepoDisplay)
		if leftDisplay != rightDisplay {
			return leftDisplay < rightDisplay
		}
		if left.RepoDisplay != right.RepoDisplay {
			return left.RepoDisplay < right.RepoDisplay
		}
		if left.RepoName != right.RepoName {
			return left.RepoName < right.RepoName
		}
		if left.RepoPath != right.RepoPath {
			return left.RepoPath < right.RepoPath
		}
		if left.CheckoutRoot != right.CheckoutRoot {
			return left.CheckoutRoot < right.CheckoutRoot
		}
		return left.CommonDir < right.CommonDir
	})
}

func fromGit(found gitx.Repo) (Target, error) {
	if found.Bare || found.Root == "" {
		return Target{}, errors.New("repository has no working checkout")
	}
	repository := canonicalPath(found.MainRoot)
	checkout := canonicalPath(found.Root)
	commonDir := canonicalPath(found.GitCommonDir)
	name := found.Name
	if name == "" {
		name = filepath.Base(repository)
	}
	return Target{
		RepoName: name, RepoDisplay: name, RepoPath: repository,
		CheckoutRoot: checkout, CommonDir: commonDir,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func absoluteDirectory(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve target path %q: %w", value, err)
	}
	if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
		absolute = filepath.Dir(absolute)
	}
	return filepath.Clean(absolute), nil
}

func navigationPath(value string) string {
	if value == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err == nil {
		value = absolute
	}
	return filepath.Clean(value)
}

func canonicalPath(value string) string {
	if value == "" {
		return ""
	}
	if resolved, err := pathx.Canonical(value); err == nil {
		return resolved
	}
	return navigationPath(value)
}

func comparablePath(value string) string {
	return canonicalPath(value)
}
