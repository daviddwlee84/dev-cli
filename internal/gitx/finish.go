package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// BranchRelation describes committed history between a task branch and base.
type BranchRelation struct {
	BaseOnly   int
	BranchOnly int
}

// Contained reports that every branch commit is already reachable from base.
func (r BranchRelation) Contained() bool { return r.BranchOnly == 0 }

// DirtyPath is one staged, unstaged, conflicted, or untracked checkout path.
// BaseEquivalent is true only when neither its index snapshot nor the final
// git-add-all worktree snapshot contains content absent from base.
type DirtyPath struct {
	Path           string
	OldPath        string
	Staged         bool
	Unstaged       bool
	Untracked      bool
	Conflicted     bool
	BaseEquivalent bool
}

// DisplayPath renders a rename without losing either side.
func (p DirtyPath) DisplayPath() string {
	if p.OldPath != "" && p.OldPath != p.Path {
		return p.OldPath + " -> " + p.Path
	}
	return p.Path
}

// FinishAnalysis is the immutable preflight for a done operation.
type FinishAnalysis struct {
	Status      Status
	Relation    BranchRelation
	Changes     []DirtyPath
	Fingerprint string
}

// UniqueDirty counts paths with content not represented by the base tree.
func (a FinishAnalysis) UniqueDirty() int {
	n := 0
	for _, change := range a.Changes {
		if !change.BaseEquivalent {
			n++
		}
	}
	return n
}

// EquivalentDirty counts dirty paths whose index and worktree snapshots both
// agree with the base.
func (a FinishAnalysis) EquivalentDirty() int {
	return len(a.Changes) - a.UniqueDirty()
}

// AnalyzeFinish compares both committed history and every uncommitted layer
// without changing the real index. The alternate index models the exact tree
// produced by `git add -A`, including untracked files but excluding ignored
// files according to Git's normal rules.
func AnalyzeFinish(ctx context.Context, dir, base, branch string) (FinishAnalysis, error) {
	relation, err := compareBranches(ctx, dir, base, branch)
	if err != nil {
		return FinishAnalysis{}, err
	}
	changes, status, rawStatus, err := finishDirtyPaths(ctx, dir)
	if err != nil {
		return FinishAnalysis{}, err
	}
	analysis := FinishAnalysis{Status: status, Relation: relation, Changes: changes}
	if status.Conflicted > 0 {
		analysis.Fingerprint = finishFingerprint(rawStatus, "conflicted", "conflicted")
		return analysis, nil
	}

	indexTree, worktreeTree, indexDiff, worktreeDiff, err := finishTrees(ctx, dir, base)
	if err != nil {
		return FinishAnalysis{}, err
	}
	for i := range analysis.Changes {
		change := &analysis.Changes[i]
		unique := worktreeDiff[change.Path]
		if change.Staged {
			unique = unique || indexDiff[change.Path]
		}
		if change.OldPath != "" {
			unique = unique || worktreeDiff[change.OldPath]
			if change.Staged {
				unique = unique || indexDiff[change.OldPath]
			}
		}
		change.BaseEquivalent = !unique
	}
	analysis.Fingerprint = finishFingerprint(rawStatus, indexTree, worktreeTree)
	return analysis, nil
}

func compareBranches(ctx context.Context, dir, base, branch string) (BranchRelation, error) {
	out, err := run(ctx, dir, "rev-list", "--left-right", "--count", base+"..."+branch)
	if err != nil {
		return BranchRelation{}, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return BranchRelation{}, fmt.Errorf("git rev-list returned %q, want two counts", out)
	}
	baseOnly, err := strconv.Atoi(fields[0])
	if err != nil {
		return BranchRelation{}, fmt.Errorf("parse base-only count %q: %w", fields[0], err)
	}
	branchOnly, err := strconv.Atoi(fields[1])
	if err != nil {
		return BranchRelation{}, fmt.Errorf("parse branch-only count %q: %w", fields[1], err)
	}
	return BranchRelation{BaseOnly: baseOnly, BranchOnly: branchOnly}, nil
}

func finishDirtyPaths(ctx context.Context, dir string) ([]DirtyPath, Status, string, error) {
	out, err := run(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "-z")
	if err != nil {
		return nil, Status{}, "", err
	}
	records := nulLines(out)
	changes := make([]DirtyPath, 0, len(records))
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if rec == "" {
			continue
		}
		var change DirtyPath
		switch rec[0] {
		case '1', '2':
			fields := strings.Fields(rec)
			if len(fields) < 2 || len(fields[1]) < 2 {
				continue
			}
			change.Path = statusPath(rec)
			change.Staged = fields[1][0] != '.'
			change.Unstaged = fields[1][1] != '.'
			if rec[0] == '2' && i+1 < len(records) {
				i++
				change.OldPath = records[i]
			}
		case 'u':
			change.Path, change.Conflicted = statusPath(rec), true
		case '?':
			change.Path = strings.TrimPrefix(rec, "? ")
			change.Untracked = true
		default:
			continue
		}
		if change.Path != "" {
			changes = append(changes, change)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].DisplayPath() < changes[j].DisplayPath() })
	return changes, statusFromOutput(dir, out), out, nil
}

func finishTrees(ctx context.Context, dir, base string) (string, string, map[string]bool, map[string]bool, error) {
	tempDir, err := os.MkdirTemp("", "dev-finish-index-*")
	if err != nil {
		return "", "", nil, nil, err
	}
	defer os.RemoveAll(tempDir)
	objectDir := filepath.Join(tempDir, "objects")
	if err := os.MkdirAll(objectDir, 0o755); err != nil {
		return "", "", nil, nil, err
	}
	commonDir, err := run(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", nil, nil, err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	repoObjects := filepath.Clean(filepath.Join(commonDir, "objects"))
	alternates := repoObjects
	if inherited := os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); inherited != "" {
		alternates += string(os.PathListSeparator) + inherited
	}
	objectEnv := []string{
		"GIT_OBJECT_DIRECTORY=" + objectDir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + alternates,
	}
	indexSource, err := run(ctx, dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", "", nil, nil, err
	}
	if !filepath.IsAbs(indexSource) {
		indexSource = filepath.Join(dir, indexSource)
	}
	indexBytes, err := os.ReadFile(indexSource)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("read index snapshot: %w", err)
	}
	index := filepath.Join(tempDir, "index")
	if err := os.WriteFile(index, indexBytes, 0o600); err != nil {
		return "", "", nil, nil, fmt.Errorf("copy index snapshot: %w", err)
	}
	env := append(append([]string(nil), objectEnv...), "GIT_INDEX_FILE="+index)
	indexTree, err := runEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("snapshot index: %w", err)
	}
	indexDiff, err := treeDiffPaths(ctx, dir, env, base, indexTree)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("compare index snapshot: %w", err)
	}

	if _, err := runEnv(ctx, dir, env, "add", "--all"); err != nil {
		return "", "", nil, nil, err
	}
	worktreeTree, err := runEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("snapshot worktree: %w", err)
	}
	worktreeDiff, err := treeDiffPaths(ctx, dir, env, base, worktreeTree)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("compare worktree snapshot: %w", err)
	}
	return indexTree, worktreeTree, indexDiff, worktreeDiff, nil
}

func treeDiffPaths(ctx context.Context, dir string, env []string, base, tree string) (map[string]bool, error) {
	out, err := runEnv(ctx, dir, env, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", base, tree)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool)
	for _, path := range nulLines(out) {
		if path != "" {
			paths[path] = true
		}
	}
	return paths, nil
}

func finishFingerprint(status, indexTree, worktreeTree string) string {
	sum := sha256.Sum256([]byte(status + "\x00" + indexTree + "\x00" + worktreeTree))
	return hex.EncodeToString(sum[:])
}

// CommitAllChanges creates a normal user commit, preserving configured hooks.
func CommitAllChanges(ctx context.Context, dir, message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("commit message is required")
	}
	if _, err := run(ctx, dir, "add", "--all"); err != nil {
		return err
	}
	_, err := run(ctx, dir, "commit", "-m", message)
	return err
}

// DiscardAllChanges restores tracked/index state and removes non-ignored
// untracked files. Callers must obtain explicit destructive confirmation.
func DiscardAllChanges(ctx context.Context, dir string) error {
	if _, err := run(ctx, dir, "reset", "--hard", "HEAD"); err != nil {
		return err
	}
	_, err := run(ctx, dir, "clean", "-fd")
	return err
}
