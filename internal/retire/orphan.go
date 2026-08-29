package retire

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// artifactOnlyEntries are the directory names an abandoned agent workspace is
// allowed to contain and still count as an empty shell. They are the same
// agent-owned paths the artifact staging allowlist recognises, plus the noise
// macOS leaves behind.
var artifactOnlyEntries = map[string]bool{
	".specstory": true,
	".claude":    true,
	".cursor":    true,
	".opencode":  true,
	".codex":     true,
	".specify":   true,
	".DS_Store":  true,
}

// Orphan is a directory that looks like a worktree, is not one, and holds
// nothing but agent artifacts.
//
// It is the residue of a specific accident: a worktree is removed while its
// transcript writer is still running, the writer recreates the path to flush,
// and what is left is a directory Git has no record of containing the only
// copy of a session's history. It must never be treated as retired — the
// transcripts have to be proven reachable somewhere else first.
type Orphan struct {
	Path string
	// Files are artifact files found inside, relative to Path, sorted.
	Files []string
}

// InspectOrphan reports whether path is an artifact-only orphan. It returns
// false for a path that does not exist, is a real Git checkout, or holds
// anything that is not an agent artifact — losing an unknown file is exactly
// the outcome this exists to prevent.
func InspectOrphan(path string) (Orphan, bool, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return Orphan{}, false, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Orphan{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	if len(entries) == 0 {
		// An empty directory is not an orphan worth reporting as salvage; it
		// carries nothing. Callers remove it through the ordinary paths.
		return Orphan{Path: path}, true, nil
	}
	for _, entry := range entries {
		if !artifactOnlyEntries[entry.Name()] {
			return Orphan{}, false, nil
		}
	}

	orphan := Orphan{Path: path}
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(path, p)
		if relErr != nil {
			return relErr
		}
		orphan.Files = append(orphan.Files, rel)
		return nil
	})
	if err != nil {
		return Orphan{}, false, err
	}
	sort.Strings(orphan.Files)
	return orphan, true, nil
}

// Unsalvaged lists the orphan's files that are not byte-identical to a file of
// the same relative path in repoPath. An orphan with none of these carries no
// content the repository does not already have, so removing it loses nothing.
//
// Byte equality rather than mere existence is deliberate: a transcript writer
// that outlived its worktree usually flushed a *longer* final version than the
// one committed earlier, and that difference is the whole reason to salvage.
func Unsalvaged(orphan Orphan, repoPath string) ([]string, error) {
	var unsalvaged []string
	for _, rel := range orphan.Files {
		if ignorableArtifact(rel) {
			continue
		}
		mine, err := os.ReadFile(filepath.Join(orphan.Path, rel))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		theirs, err := os.ReadFile(filepath.Join(repoPath, rel))
		if err != nil || !bytes.Equal(mine, theirs) {
			unsalvaged = append(unsalvaged, rel)
		}
	}
	return unsalvaged, nil
}

// ignorableArtifact skips derived files that are rewritten constantly and carry
// no history worth preserving.
func ignorableArtifact(rel string) bool {
	base := filepath.Base(rel)
	return base == ".DS_Store" || strings.HasSuffix(filepath.ToSlash(rel), ".specstory/statistics.json")
}
