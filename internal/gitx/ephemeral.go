package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// EphemeralCheckout is the read-only Git cleanliness evidence needed by the
// ephemeral cleanup service. It retains counts and a digest, never pathnames.
type EphemeralCheckout struct {
	Status          Status
	Ignored         int
	DirtySubmodules int
	Fingerprint     string
}

// InspectEphemeralCheckout reads full porcelain status, including all
// untracked files and non-ignored submodule state, plus Git's ignored-file
// inventory. Initialized submodules are inspected recursively so ignored files
// cannot hide behind the parent repository boundary. No command changes an
// index or worktree.
func InspectEphemeralCheckout(ctx context.Context, dir string) (EphemeralCheckout, error) {
	return inspectEphemeralCheckout(ctx, dir, make(map[string]bool))
}

func inspectEphemeralCheckout(ctx context.Context, dir string, seen map[string]bool) (EphemeralCheckout, error) {
	canonical, err := pathx.Canonical(dir)
	if err != nil {
		return EphemeralCheckout{}, err
	}
	if seen[canonical] {
		return EphemeralCheckout{}, fmt.Errorf("recursive submodule checkout path %s", canonical)
	}
	seen[canonical] = true
	defer delete(seen, canonical)

	statusOutput, err := run(ctx, canonical,
		"status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=none", "-z")
	if err != nil {
		return EphemeralCheckout{}, err
	}
	ignoredOutput, err := run(ctx, canonical,
		"ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return EphemeralCheckout{}, err
	}
	gitlinksOutput, err := run(ctx, canonical, "ls-files", "--stage", "-z")
	if err != nil {
		return EphemeralCheckout{}, err
	}

	ignored := nonEmptyNULRecords(ignoredOutput)
	dirtyPaths := submoduleDirtyPaths(statusOutput)
	dirtySubmodules := 0
	var childFingerprints []string
	for _, submodulePath := range gitlinkPaths(gitlinksOutput) {
		joined := filepath.Join(canonical, filepath.FromSlash(submodulePath))
		child, err := pathx.CanonicalChild(canonical, joined)
		if err != nil {
			return EphemeralCheckout{}, fmt.Errorf("validate submodule path: %w", err)
		}
		info, err := os.Lstat(child)
		if errors.Is(err, fs.ErrNotExist) {
			dirtySubmodules++
			childFingerprints = append(childFingerprints, digestEphemeral(submodulePath, "missing"))
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return EphemeralCheckout{}, fmt.Errorf("inspect submodule checkout")
		}
		childInspection, err := inspectEphemeralCheckout(ctx, child, seen)
		if err != nil {
			return EphemeralCheckout{}, err
		}
		ignored += childInspection.Ignored
		childDirty := dirtyPaths[submodulePath] || childInspection.Status.Dirty() ||
			childInspection.Ignored > 0 || childInspection.DirtySubmodules > 0
		if childDirty {
			dirtySubmodules++
		}
		dirtySubmodules += childInspection.DirtySubmodules
		childFingerprints = append(childFingerprints, digestEphemeral(submodulePath, childInspection.Fingerprint))
	}
	sort.Strings(childFingerprints)
	fingerprintParts := []string{statusOutput, ignoredOutput, gitlinksOutput}
	fingerprintParts = append(fingerprintParts, childFingerprints...)
	return EphemeralCheckout{
		Status:          statusFromOutput(canonical, statusOutput),
		Ignored:         ignored,
		DirtySubmodules: dirtySubmodules,
		Fingerprint:     digestEphemeral(fingerprintParts...),
	}, nil
}

func nonEmptyNULRecords(output string) int {
	count := 0
	for _, record := range nulLines(output) {
		if record != "" {
			count++
		}
	}
	return count
}

func submoduleDirtyCount(output string) int { return len(submoduleDirtyPaths(output)) }

func submoduleDirtyPaths(output string) map[string]bool {
	paths := make(map[string]bool)
	for _, record := range nulLines(output) {
		if record == "" || (record[0] != '1' && record[0] != '2') {
			continue
		}
		fields := strings.Fields(record)
		if len(fields) > 2 && strings.HasPrefix(fields[2], "S") {
			if path := statusPath(record); path != "" {
				paths[filepath.ToSlash(path)] = true
			}
		}
	}
	return paths
}

func gitlinkPaths(output string) []string {
	var paths []string
	for _, record := range nulLines(output) {
		metadata, path, ok := strings.Cut(record, "\t")
		if !ok || path == "" {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) == 3 && fields[0] == "160000" && fields[2] == "0" {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	sort.Strings(paths)
	return paths
}

func digestEphemeral(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
