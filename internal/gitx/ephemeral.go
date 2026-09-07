package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	graph, err := SubmodulesOf(ctx, canonical)
	if err != nil {
		return EphemeralCheckout{}, err
	}
	if !sameCanonicalPath(graph.Root, canonical) {
		return EphemeralCheckout{}, fmt.Errorf("not an exact checkout: %s", canonical)
	}
	raw, err := run(ctx, canonical, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "--ignore-submodules=none", "-z")
	if err != nil {
		return EphemeralCheckout{}, err
	}
	ignoredOutput, err := run(ctx, canonical, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return EphemeralCheckout{}, err
	}
	ignored, dirty := nonEmptyNULRecords(ignoredOutput), 0
	for _, n := range graph.Nodes {
		ignored += n.Ignored
		if !n.Initialized || n.State != "available" || n.Status.Dirty() || n.Ignored > 0 || n.HEAD != n.Gitlink {
			dirty++
		}
	}
	return EphemeralCheckout{Status: statusFromOutput(canonical, raw), Ignored: ignored, DirtySubmodules: dirty, Fingerprint: digestEphemeral(raw, ignoredOutput, graph.Fingerprint)}, nil
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
