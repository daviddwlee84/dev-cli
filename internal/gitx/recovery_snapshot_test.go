package gitx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestRecoverySnapshotCapturesHiddenTrackedBytesAndRepositoryState(t *testing.T) {
	repository := gittest.New(t)
	repository.Git("update-ref", "refs/recovery/custom", repository.Git("rev-parse", "HEAD"))
	repository.Write(".gitignore", ".env\n")
	repository.Git("add", ".gitignore")
	repository.Git("commit", "-m", "test: add ignore rules")
	repository.Write(".env", "private\n")
	if err := os.MkdirAll(filepath.Join(repository.Root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Root, "nested", ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository.Git("update-index", "--assume-unchanged", "README.md")
	hiddenBody := []byte("hidden working tree edit\n")
	if err := os.WriteFile(filepath.Join(repository.Root, "README.md"), hiddenBody, 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := gitx.RecoverySnapshotOf(t.Context(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Completeness != gitx.RecoveryCompletenessComplete {
		t.Fatalf("snapshot completeness = %s; findings=%+v", snapshot.Completeness, snapshot.Findings)
	}
	if snapshot.ObjectFormat == "" || len(snapshot.Worktrees) != 1 || len(snapshot.AdminRoots) != 1 {
		t.Fatalf("snapshot envelope = format %q, worktrees %d, admin roots %d", snapshot.ObjectFormat, len(snapshot.Worktrees), len(snapshot.AdminRoots))
	}
	if !slices.ContainsFunc(snapshot.Refs, func(ref gitx.RecoveryRef) bool {
		return ref.Name == "refs/recovery/custom" && ref.Kind == gitx.RecoveryRefOther
	}) {
		t.Fatalf("custom ref missing: %+v", snapshot.Refs)
	}

	worktree := snapshot.Worktrees[0]
	if worktree.Presence != gitx.RecoveryPresencePresent || worktree.GitDir == "" {
		t.Fatalf("worktree identity = %+v", worktree)
	}
	if !slices.Contains(worktree.IgnoredPaths, ".env") {
		t.Fatalf("ignored paths = %v", worktree.IgnoredPaths)
	}
	if !slices.ContainsFunc(worktree.IndexEntries, func(entry gitx.RecoveryIndexEntry) bool {
		return entry.Path == "README.md" && entry.AssumeUnchanged
	}) {
		t.Fatalf("assume-unchanged index entry missing: %+v", worktree.IndexEntries)
	}
	tracked, ok := snapshot.RawTrackedBytes(worktree.Path, "README.md")
	if !ok || string(tracked) != string(hiddenBody) {
		t.Fatalf("raw tracked bytes = %q, %v", tracked, ok)
	}
	tracked[0] = 'X'
	again, ok := snapshot.RawTrackedBytes(worktree.Path, "README.md")
	if !ok || string(again) != string(hiddenBody) {
		t.Fatal("RawTrackedBytes did not return a defensive copy")
	}
	if !slices.ContainsFunc(worktree.NestedRepositories, func(nested gitx.RecoveryNestedRepository) bool {
		return nested.Path == "nested" && nested.MarkerType == gitx.RecoveryEntryRegular
	}) || !hasRecoveryFinding(snapshot, "nested-repository") {
		t.Fatalf("nested repository evidence = %+v; findings=%+v", worktree.NestedRepositories, snapshot.Findings)
	}
	config, ok := snapshot.RawAdminBytes(gitx.RecoveryAdminCommonDir, "config")
	if !ok || len(config) == 0 {
		t.Fatal("Git config was not retained as private admin bytes")
	}
}

func TestRecoverySnapshotAggregateCaptureLimitIsExplicit(t *testing.T) {
	repository := gittest.New(t)
	snapshot, err := gitx.RecoverySnapshotWithOptions(t.Context(), repository.Root, gitx.RecoverySnapshotOptions{MaxCaptureBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Completeness != gitx.RecoveryCompletenessPartial || !hasRecoveryFinding(snapshot, "capture-limit-exceeded") {
		t.Fatalf("bounded snapshot = completeness %s, findings %+v", snapshot.Completeness, snapshot.Findings)
	}
	worktree := snapshot.Worktrees[0]
	if _, ok := snapshot.RawTrackedBytes(worktree.Path, "README.md"); ok {
		t.Fatal("tracked bytes above aggregate limit were retained")
	}
}

func TestRecoverySnapshotCancellationIsExplicit(t *testing.T) {
	repository := gittest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := gitx.RecoverySnapshotOf(ctx, repository.Root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if snapshot.Completeness == gitx.RecoveryCompletenessComplete || !hasRecoveryFinding(snapshot, "collection-cancelled") {
		t.Fatalf("cancelled snapshot = completeness %s, findings %+v", snapshot.Completeness, snapshot.Findings)
	}
}

func hasRecoveryFinding(snapshot gitx.RecoverySnapshot, code string) bool {
	return slices.ContainsFunc(snapshot.Findings, func(finding gitx.RecoveryFinding) bool {
		return finding.Code == code && finding.Blocking
	})
}
