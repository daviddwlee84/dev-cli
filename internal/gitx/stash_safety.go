package gitx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// StashSafety describes checkout content that ordinary git stash cannot prove
// it will preserve completely.
type StashSafety struct {
	DirtySubmodules    int
	NestedRepositories []string
	Fingerprint        string
}

// Safe reports whether staged, unstaged, and untracked work can be handled by
// the exact stash transaction without crossing another repository boundary.
func (s StashSafety) Safe() bool {
	return s.DirtySubmodules == 0 && len(s.NestedRepositories) == 0
}

// InspectStashSafety recursively checks submodule state and probes changed
// paths for embedded repositories. It is read-only and returns no file bodies.
func InspectStashSafety(ctx context.Context, dir string) (StashSafety, error) {
	inspection, err := InspectEphemeralCheckout(ctx, dir)
	if err != nil {
		return StashSafety{}, err
	}
	root, err := pathx.Canonical(dir)
	if err != nil {
		return StashSafety{}, err
	}
	paths, err := ChangedPaths(ctx, root)
	if err != nil {
		return StashSafety{}, err
	}
	gitlinksOutput, err := run(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return StashSafety{}, err
	}
	gitlinks := gitlinkPaths(gitlinksOutput)
	nested := make(map[string]struct{})
	for _, changed := range paths {
		changed = filepath.ToSlash(filepath.Clean(changed))
		if changed == "." || changed == "" || withinGitlink(changed, gitlinks) {
			continue
		}
		candidate := changed
		if info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(candidate))); statErr == nil && !info.IsDir() {
			candidate = filepath.ToSlash(filepath.Dir(candidate))
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return StashSafety{}, fmt.Errorf("inspect changed path %s: %w", changed, statErr)
		}
		for candidate != "." && candidate != "" {
			marker := filepath.Join(root, filepath.FromSlash(candidate), ".git")
			if _, markerErr := os.Lstat(marker); markerErr == nil {
				nested[candidate] = struct{}{}
				break
			} else if !os.IsNotExist(markerErr) {
				return StashSafety{}, fmt.Errorf("inspect nested repository marker %s: %w", candidate, markerErr)
			}
			candidate = filepath.ToSlash(filepath.Dir(candidate))
		}
	}
	result := StashSafety{DirtySubmodules: inspection.DirtySubmodules, Fingerprint: inspection.Fingerprint}
	for path := range nested {
		result.NestedRepositories = append(result.NestedRepositories, path)
	}
	sort.Strings(result.NestedRepositories)
	return result, nil
}

func withinGitlink(path string, gitlinks []string) bool {
	for _, gitlink := range gitlinks {
		if path == gitlink || strings.HasPrefix(path, gitlink+"/") {
			return true
		}
	}
	return false
}
