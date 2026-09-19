package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

func TestPrepareAndFinalizeExactArtifactsOnce(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	current := filepath.Join(r.Root, ".specstory", "history", "current.md")
	other := filepath.Join(r.Root, ".specstory", "history", "other.md")
	plan := filepath.Join(r.Root, ".claude", "plans", "retire.md")
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(plan), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, current, "Claude Code", uuid, "final")
	writeTranscript(t, other, "Codex CLI", "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2", "other")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Root, ".specstory", "statistics.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(filepath.Join(t.TempDir(), "intents"))
	store.newID = func() string { return "intent-finalize" }
	var scanned []string
	service := &Service{Store: store, ScanStaged: func(_ context.Context, _ string, paths []string) error {
		scanned = append([]string(nil), paths...)
		return nil
	}}
	intent, err := service.Prepare(context.Background(), PrepareRequest{
		Worktree: r.Root, Session: "claude:" + uuid, RunID: "run-finalize", Plans: []string{plan},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(intent.UnrelatedArtifacts) != 2 {
		t.Fatalf("unrelated artifacts = %q plans=%q", intent.UnrelatedArtifacts, intent.PlanPaths)
	}
	finalized, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond, WriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	resolvedCurrent, err := filepath.EvalSymlinks(current)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Status != Finalized || finalized.ArtifactCommit == "" || finalized.TranscriptPath != resolvedCurrent {
		t.Fatalf("finalized intent = %+v", finalized)
	}
	if len(scanned) != 2 {
		t.Fatalf("scanner paths = %v", scanned)
	}
	changed := r.Git("show", "--pretty=", "--name-only", "HEAD")
	if !strings.Contains(changed, ".specstory/history/current.md") || !strings.Contains(changed, ".claude/plans/retire.md") {
		t.Fatalf("artifact commit paths:\n%s", changed)
	}
	if strings.Contains(changed, "statistics.json") || strings.Contains(changed, "other.md") {
		t.Fatalf("artifact commit included unrelated files:\n%s", changed)
	}
	message := r.Git("log", "-1", "--format=%B")
	if !strings.Contains(message, "Agent-Artifact-Session: claude:"+uuid) || !strings.Contains(message, "Dev-Artifact-Intent: "+intent.ID) {
		t.Fatalf("artifact commit trailers:\n%s", message)
	}
	before := r.Git("rev-parse", "HEAD")
	if _, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond, WriterStopped: true}); err != nil {
		t.Fatal(err)
	}
	if after := r.Git("rev-parse", "HEAD"); after != before {
		t.Fatalf("retry created duplicate commit: before=%s after=%s", before, after)
	}
	r.Write("post-rewrite.txt", "rebased tree\n")
	r.Git("add", "post-rewrite.txt")
	r.Git("commit", "--amend", "--no-edit")
	rewritten := r.Git("rev-parse", "HEAD")
	if rewritten == before {
		t.Fatal("test setup did not rewrite the artifact commit")
	}
	reconciled, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.ArtifactCommit != rewritten {
		t.Fatalf("rewritten receipt was not reconciled: got=%s want=%s", reconciled.ArtifactCommit, rewritten)
	}
}

func TestPrepareSharesTaskflowRepositoryLockAndRevalidatesBeforeCreate(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	branch := "feat/prepare-race"
	checkout := filepath.Join(t.TempDir(), "prepare-race")
	r.Git("branch", branch)
	r.Git("worktree", "add", checkout, branch)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	transcript := filepath.Join(checkout, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, transcript, "Claude Code", uuid, "final")
	repository, err := gitx.Discover(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	commonDir, err := filepath.EvalSymlinks(repository.GitCommonDir)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	ready := make(chan struct{})
	service := &Service{Store: store, beforeIntentCreate: func() { close(ready) }}
	request := PrepareRequest{Worktree: checkout, Session: "claude:" + uuid, RunID: "run-prepare-race"}
	prepared := make(chan error, 1)

	err = lockx.WithDir(context.Background(), filepath.Join(commonDir, "dev-taskflow"), "taskflow repository", func() error {
		go func() {
			_, prepareErr := service.Prepare(context.Background(), request)
			prepared <- prepareErr
		}()
		select {
		case <-ready:
		case <-time.After(20 * time.Second):
			t.Fatal("Prepare did not reach the repository lock")
		}
		r.Git("worktree", "remove", "--force", checkout)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepareErr := <-prepared; prepareErr == nil || !strings.Contains(prepareErr.Error(), "revalidate artifact checkout") {
		t.Fatalf("Prepare after checkout removal=%v", prepareErr)
	}
	if intents, err := store.List(); err != nil || len(intents) != 0 {
		t.Fatalf("artifact intents=%+v err=%v", intents, err)
	}
}

func TestPrepareRejectsProductChangesAndLargeUntrackedTranscript(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "large.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, strings.Repeat("x", 128))
	if err := os.WriteFile(filepath.Join(r.Root, "product.go"), []byte("package product\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: NewStore(t.TempDir()), LargeLimit: 64}
	if _, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid}); err == nil || !strings.Contains(err.Error(), "product changes") {
		t.Fatalf("product dirt should block prepare: %v", err)
	}
	if err := os.Remove(filepath.Join(r.Root, "product.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid}); err == nil || !strings.Contains(err.Error(), "allow-large") {
		t.Fatalf("large untracked transcript should require acknowledgement: %v", err)
	}
}

func TestPrepareRejectsActiveGitOperation(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, "final")
	head := r.Git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(r.Root, ".git", "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: NewStore(t.TempDir())}
	if _, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid}); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("active Git operation should block prepare: %v", err)
	}
}

func isolateGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestFinalizeScanFailureUnstagesOnlyOwnedPaths(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, "secret-shaped bytes")
	store := NewStore(t.TempDir())
	service := &Service{Store: store, ScanStaged: func(context.Context, string, []string) error {
		return errors.New("scanner rejected content")
	}}
	intent, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond, WriterStopped: true}); err == nil || !strings.Contains(err.Error(), "scan-failed") {
		t.Fatalf("scan failure = %v", err)
	}
	if staged := r.Git("diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("failed finalizer left staged paths: %s", staged)
	}
	got, err := store.Get(intent.ID)
	if err != nil || got.Status != Failed || got.FailureCode != "scan-failed" {
		t.Fatalf("failed intent = %+v, %v", got, err)
	}
}

func TestFinalizeRequiresPostWriterProof(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, "final")
	store := NewStore(t.TempDir())
	service := &Service{Store: store, ScanStaged: func(context.Context, string, []string) error { return nil }}
	intent, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid, RunID: "writer-proof"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "writer-unproven") {
		t.Fatalf("missing writer proof should fail: %v", err)
	}
	if err := service.ObserveSessionEnd(context.Background(), intent.RunID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond}); err != nil {
		t.Fatalf("observed SessionEnd should permit finalization: %v", err)
	}
}

func TestFinalizeNeverRestagesBytesChangedAfterScan(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, "scanned")
	store := NewStore(t.TempDir())
	service := &Service{Store: store, ScanStaged: func(_ context.Context, _ string, _ []string) error {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.WriteString("changed after scan\n")
		return err
	}}
	intent, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid, RunID: "scan-race"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond, WriterStopped: true}); err == nil || !strings.Contains(err.Error(), "index-drift") {
		t.Fatalf("post-scan mutation should fail without re-staging: %v", err)
	}
	if staged := r.Git("diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("post-scan mutation left staged paths: %q", staged)
	}
}

func TestFinalizeCommitFailureUnstagesOwnedPaths(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, "final")
	hook := filepath.Join(r.Root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	service := &Service{Store: store, ScanStaged: func(context.Context, string, []string) error { return nil }}
	intent, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid, RunID: "hook-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), FinalizeRequest{IntentID: intent.ID, Settle: time.Millisecond, WriterStopped: true}); err == nil || !strings.Contains(err.Error(), "commit-failed") {
		t.Fatalf("commit hook failure = %v", err)
	}
	if staged := r.Git("diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("commit failure left staged paths: %q", staged)
	}
}

func TestConcurrentFinalizersShareCommonDirLock(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	uuid := "72b5c55e-d964-45cd-b040-cb29d0d7af05"
	path := filepath.Join(r.Root, ".specstory", "history", "current.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Claude Code", uuid, "final")
	store := NewStore(t.TempDir())
	var scans atomic.Int32
	service := &Service{Store: store, ScanStaged: func(context.Context, string, []string) error {
		scans.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	}}
	intent, err := service.Prepare(context.Background(), PrepareRequest{Worktree: r.Root, Session: "claude:" + uuid, RunID: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, finalizeErr := service.Finalize(context.Background(), FinalizeRequest{
				IntentID: intent.ID, Settle: time.Millisecond, WriterStopped: true,
			})
			errs <- finalizeErr
		}()
	}
	wg.Wait()
	close(errs)
	for finalizeErr := range errs {
		if finalizeErr != nil && !errors.Is(finalizeErr, ErrStaleRevision) {
			t.Fatalf("concurrent finalize: %v", finalizeErr)
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("scanner ran %d times, want one serialized finalization", scans.Load())
	}
	if count := r.Git("rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("concurrent finalizers created duplicate commits: %s", count)
	}
}

func TestInspectReadinessClassifiesExactCheckoutIntents(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	reachable := r.Git("rev-parse", "HEAD")
	r.Commit("orphan.txt", "orphaned receipt\n", "chore: orphan receipt")
	unreachable := r.Git("rev-parse", "HEAD")
	r.Git("reset", "--hard", "HEAD^")

	store := NewStore(t.TempDir())
	createReadinessIntent(t, store, "intent-armed", r.Root, Armed, "")
	createReadinessIntent(t, store, "intent-finalizing", r.Root, Finalizing, "")
	createReadinessIntent(t, store, "intent-failed", r.Root, Failed, "")
	createReadinessIntent(t, store, "intent-discarded", r.Root, Discarded, "")
	createReadinessIntent(t, store, "intent-reachable", r.Root, Finalized, reachable)
	createReadinessIntent(t, store, "intent-unreachable", r.Root, Finalized, unreachable)
	createReadinessIntent(t, store, "intent-other", t.TempDir(), Armed, "")

	inspection, err := InspectReadiness(context.Background(), store, r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Checkout != r.Root || inspection.KnownEmpty {
		t.Fatalf("inspection identity = %+v", inspection)
	}
	if len(inspection.Intents) != 6 {
		t.Fatalf("exact-checkout intents = %d, want 6: %+v", len(inspection.Intents), inspection.Intents)
	}
	type wantEvidence struct {
		state     ReadinessState
		finalized bool
		reachable bool
	}
	want := map[string]wantEvidence{
		"intent-armed":       {state: ReadinessPending},
		"intent-finalizing":  {state: ReadinessPending},
		"intent-failed":      {state: ReadinessFailed},
		"intent-discarded":   {state: ReadinessDiscarded},
		"intent-reachable":   {state: ReadinessFinalizedReachable, finalized: true, reachable: true},
		"intent-unreachable": {state: ReadinessFinalizedUnreachable, finalized: true},
	}
	for _, evidence := range inspection.Intents {
		expected, ok := want[evidence.Intent.ID]
		if !ok {
			t.Errorf("unexpected intent evidence: %+v", evidence)
			continue
		}
		delete(want, evidence.Intent.ID)
		if evidence.State != expected.state || evidence.Finalized != expected.finalized ||
			evidence.ReceiptReachable != expected.reachable || evidence.ObservationError != nil {
			t.Errorf("%s evidence = %+v, want %+v", evidence.Intent.ID, evidence, expected)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing classifications: %v", want)
	}
	if inspection.Ready() {
		t.Fatal("pending, failed, and unreachable intents must block readiness")
	}

	empty, err := InspectReadiness(context.Background(), store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !empty.KnownEmpty || len(empty.Intents) != 0 || !empty.Ready() {
		t.Fatalf("successful empty observation = %+v", empty)
	}
}

func TestInspectReadinessMatchesMovedCheckoutByRepositoryAndBranch(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	branch := "feat/moved-artifact"
	oldPath := filepath.Join(t.TempDir(), "old-checkout")
	newPath := filepath.Join(t.TempDir(), "new-checkout")
	r.Git("branch", branch)
	r.Git("worktree", "add", oldPath, branch)
	repository, err := gitx.Discover(context.Background(), oldPath)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	store.newID = func() string { return "intent-moved" }
	intent := &Intent{
		RunID: "run-moved", Provider: "claude", SessionID: "72b5c55e-d964-45cd-b040-cb29d0d7af05",
		RepoPath: r.Root, GitCommonDir: repository.GitCommonDir, WorktreePath: oldPath,
		Branch: branch, Base: "main", Head: r.Git("rev-parse", branch),
	}
	if err := store.Create(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	r.Git("worktree", "move", oldPath, newPath)

	inspection, err := InspectReadiness(context.Background(), store, newPath)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.KnownEmpty || inspection.Ready() || len(inspection.Intents) != 1 ||
		inspection.Intents[0].Intent.ID != "intent-moved" || inspection.Intents[0].State != ReadinessPending {
		t.Fatalf("moved-checkout readiness=%+v", inspection)
	}
}

func TestCleanupRegressionArtifactUnrelatedIntentsDoNotBlockDetachedCheckout(t *testing.T) {
	isolateGitConfig(t)
	detached := gittest.New(t)
	detached.Git("checkout", "--detach", "HEAD")
	other := gittest.New(t)
	other.Commit("other.txt", "unrelated receipt\n", "chore: unrelated receipt")

	for _, status := range []Status{Armed, Finalizing, Finalized} {
		t.Run(string(status), func(t *testing.T) {
			store := NewStore(t.TempDir())
			before, err := InspectReadiness(t.Context(), store, detached.Root)
			if err != nil || !before.KnownEmpty || !before.Ready() {
				t.Fatalf("empty-store control = %+v, %v", before, err)
			}
			createReadinessIntent(t, store, "intent-unrelated", other.Root, status, other.Git("rev-parse", "HEAD"))

			after, err := InspectReadiness(t.Context(), store, detached.Root)
			if err != nil || after.ObservationError != nil || !after.KnownEmpty || !after.Ready() || len(after.Intents) != 0 {
				t.Fatalf("unrelated %s intent changed detached readiness = %+v, %v", status, after, err)
			}
		})
	}
}

func TestCleanupRegressionArtifactExactPendingStillBlocksDetachedCheckout(t *testing.T) {
	isolateGitConfig(t)
	detached := gittest.New(t)
	detached.Git("checkout", "--detach", "HEAD")
	other := gittest.New(t)

	for _, status := range []Status{Armed, Finalizing} {
		t.Run(string(status), func(t *testing.T) {
			store := NewStore(t.TempDir())
			createReadinessIntent(t, store, "intent-unrelated", other.Root, Finalized, other.Git("rev-parse", "HEAD"))
			createReadinessIntent(t, store, "intent-exact", detached.Root, status, "")

			inspection, err := InspectReadiness(t.Context(), store, detached.Root)
			if err != nil || inspection.ObservationError != nil {
				t.Fatalf("exact checkout should not require branch identity: %+v, %v", inspection, err)
			}
			if inspection.KnownEmpty || inspection.Ready() || len(inspection.Intents) != 1 ||
				inspection.Intents[0].Intent.ID != "intent-exact" || inspection.Intents[0].State != ReadinessPending {
				t.Fatalf("exact %s intent did not block detached readiness: %+v", status, inspection)
			}
		})
	}
}

func TestCleanupRegressionArtifactMovedDetachedCheckoutRemainsAmbiguous(t *testing.T) {
	isolateGitConfig(t)
	for _, status := range []Status{Armed, Finalized} {
		t.Run(string(status), func(t *testing.T) {
			r := gittest.New(t)
			branch := "feat/moved-artifact"
			oldPath := filepath.Join(t.TempDir(), "old-checkout")
			newPath := filepath.Join(t.TempDir(), "new-checkout")
			r.Git("worktree", "add", "-b", branch, oldPath, "main")
			repository, err := gitx.Discover(t.Context(), oldPath)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore(t.TempDir())
			intent := createReadinessIntent(t, store, "intent-moved", oldPath, status, r.Git("rev-parse", branch))
			if err := store.Update(t.Context(), intent.ID, func(candidate *Intent) error {
				candidate.RepoPath = r.Root
				candidate.GitCommonDir = repository.GitCommonDir
				candidate.Branch = branch
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			r.Git("worktree", "move", oldPath, newPath)
			r.GitIn(newPath, "checkout", "--detach", "HEAD")

			inspection, err := InspectReadiness(t.Context(), store, newPath)
			if err == nil || !strings.Contains(err.Error(), "moved intent identity is ambiguous") ||
				inspection.ObservationError != err || inspection.KnownEmpty || inspection.Ready() || len(inspection.Intents) != 0 {
				t.Fatalf("same-repository moved %s intent lost detached ambiguity: %+v, %v", status, inspection, err)
			}
		})
	}
}

func TestCleanupRegressionArtifactDetachedCheckoutRetainsStoreErrors(t *testing.T) {
	isolateGitConfig(t)
	detached := gittest.New(t)
	detached.Git("checkout", "--detach", "HEAD")

	t.Run("unreadable store", func(t *testing.T) {
		// A regular file makes ReadDir fail on every platform without relying
		// on POSIX permission bits or the test runner's privileges.
		path := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(path, []byte("not an intent store\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		inspection, err := InspectReadiness(t.Context(), NewStore(path), detached.Root)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || !strings.Contains(err.Error(), "list artifact intents") ||
			inspection.ObservationError != err || inspection.KnownEmpty || inspection.Ready() {
			t.Fatalf("store read error was not retained: %+v, %v", inspection, err)
		}
	})

	t.Run("corrupt unrelated intent", func(t *testing.T) {
		store := NewStore(t.TempDir())
		other := gittest.New(t)
		intent := createReadinessIntent(t, store, "intent-unrelated", other.Root, Finalized, other.Git("rev-parse", "HEAD"))
		body := []byte(`{"schema_version": nope}`)
		if err := os.WriteFile(store.path(intent.ID), body, 0o600); err != nil {
			t.Fatal(err)
		}
		inspection, err := InspectReadiness(t.Context(), store, detached.Root)
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) || !strings.Contains(err.Error(), "list artifact intents") ||
			inspection.ObservationError != err || inspection.KnownEmpty || inspection.Ready() {
			t.Fatalf("store decode error was not retained: %+v, %v", inspection, err)
		}
		if after, readErr := os.ReadFile(store.path(intent.ID)); readErr != nil || !bytes.Equal(after, body) {
			t.Fatalf("failed inspection changed record: bytes=%q err=%v", after, readErr)
		}
	})
}

func TestReadinessInspectionReadyUsesFinalizationContract(t *testing.T) {
	observationErr := errors.New("observation failed")
	discarded := IntentReadiness{State: ReadinessDiscarded}
	reachable := IntentReadiness{
		State: ReadinessFinalizedReachable, Finalized: true, ReceiptReachable: true,
	}
	tests := []struct {
		name       string
		inspection ReadinessInspection
		want       bool
	}{
		{name: "known empty", inspection: ReadinessInspection{KnownEmpty: true}, want: true},
		{name: "unknown empty", inspection: ReadinessInspection{}},
		{name: "discarded", inspection: ReadinessInspection{Intents: []IntentReadiness{discarded}}, want: true},
		{name: "finalized reachable", inspection: ReadinessInspection{Intents: []IntentReadiness{reachable}}, want: true},
		{name: "ready mixture", inspection: ReadinessInspection{Intents: []IntentReadiness{discarded, reachable}}, want: true},
		{name: "pending", inspection: ReadinessInspection{Intents: []IntentReadiness{{State: ReadinessPending}}}},
		{name: "failed", inspection: ReadinessInspection{Intents: []IntentReadiness{{State: ReadinessFailed}}}},
		{name: "finalized unreachable", inspection: ReadinessInspection{Intents: []IntentReadiness{{State: ReadinessFinalizedUnreachable, Finalized: true}}}},
		{name: "incomplete finalized evidence", inspection: ReadinessInspection{Intents: []IntentReadiness{{State: ReadinessFinalizedReachable}}}},
		{name: "intent observation error", inspection: ReadinessInspection{Intents: []IntentReadiness{{State: ReadinessObservationError, ObservationError: observationErr}}}},
		{name: "global observation error", inspection: ReadinessInspection{ObservationError: observationErr}},
		{name: "contradictory empty", inspection: ReadinessInspection{KnownEmpty: true, Intents: []IntentReadiness{discarded}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.inspection.Ready(); got != test.want {
				t.Errorf("Ready() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestInspectReadinessIsByteForByteReadOnly(t *testing.T) {
	isolateGitConfig(t)
	r := gittest.New(t)
	const intentID = "intent-read-only"
	transcript := r.Write(".specstory/history/session.md", "final transcript bytes\n")
	plan := r.Write(".claude/plans/finish.md", "# exact plan bytes\n")
	r.Git("add", ".specstory/history/session.md", ".claude/plans/finish.md")
	r.Git("commit", "-m", "chore: artifact receipt", "-m", "Dev-Artifact-Intent: "+intentID)
	recordedReceipt := r.Git("rev-parse", "HEAD")

	store := NewStore(t.TempDir())
	createReadinessIntent(t, store, intentID, r.Root, Finalized, recordedReceipt)
	if err := store.Update(context.Background(), intentID, func(intent *Intent) error {
		intent.TranscriptPath = transcript
		intent.PlanPaths = []string{plan}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	marker := r.Write("rewrite-marker.txt", "rewritten receipt\n")
	r.Git("add", "rewrite-marker.txt")
	r.Git("commit", "--amend", "--no-edit")
	currentReceipt := r.Git("rev-parse", "HEAD")
	if currentReceipt == recordedReceipt {
		t.Fatal("test setup did not rewrite the receipt")
	}
	if message := r.Git("log", "-1", "--format=%B"); !strings.Contains(message, "Dev-Artifact-Intent: "+intentID) {
		t.Fatalf("rewritten receipt lost intent trailer:\n%s", message)
	}

	readBytes := func(path string) []byte {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	sources := []string{transcript, plan, marker}
	beforeSources := make(map[string][]byte, len(sources))
	for _, path := range sources {
		beforeSources[path] = readBytes(path)
	}
	intentPath := store.path(intentID)
	beforeIntent := readBytes(intentPath)
	beforeStatus := r.Git("status", "--porcelain=v1", "--untracked-files=all")

	inspection, err := InspectReadiness(context.Background(), store, r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Intents) != 1 || inspection.Intents[0].State != ReadinessFinalizedUnreachable || inspection.Ready() {
		t.Fatalf("stale receipt inspection = %+v", inspection)
	}
	if inspection.Intents[0].Intent.ArtifactCommit != recordedReceipt {
		t.Fatalf("inspection reconciled receipt in memory: %s", inspection.Intents[0].Intent.ArtifactCommit)
	}
	for path, before := range beforeSources {
		if after := readBytes(path); !bytes.Equal(after, before) {
			t.Errorf("source file %s changed during inspection", path)
		}
	}
	if after := readBytes(intentPath); !bytes.Equal(after, beforeIntent) {
		t.Fatal("intent record changed during inspection")
	}
	if after := r.Git("rev-parse", "HEAD"); after != currentReceipt {
		t.Fatalf("inspection changed HEAD: got %s want %s", after, currentReceipt)
	}
	if after := r.Git("status", "--porcelain=v1", "--untracked-files=all"); after != beforeStatus {
		t.Fatalf("inspection changed index or working tree: before=%q after=%q", beforeStatus, after)
	}
	stored, err := store.Get(intentID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ArtifactCommit != recordedReceipt {
		t.Fatalf("inspection reconciled durable receipt: got %s want %s", stored.ArtifactCommit, recordedReceipt)
	}
}

func TestInspectReadinessRetainsObservationErrors(t *testing.T) {
	t.Run("list decode", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "broken.json")
		body := []byte(`{"schema_version": nope}`)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		inspection, err := InspectReadiness(context.Background(), NewStore(dir), t.TempDir())
		var syntaxErr *json.SyntaxError
		if err == nil || !errors.As(err, &syntaxErr) {
			t.Fatalf("list error was not retained: %v", err)
		}
		syntaxErr = nil
		if !errors.As(inspection.ObservationError, &syntaxErr) || inspection.KnownEmpty || inspection.Ready() {
			t.Fatalf("list observation = %+v", inspection)
		}
		if after, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(after, body) {
			t.Fatalf("failed list changed record: bytes=%q err=%v", after, readErr)
		}
	})

	t.Run("receipt", func(t *testing.T) {
		isolateGitConfig(t)
		r := gittest.New(t)
		store := NewStore(t.TempDir())
		createReadinessIntent(t, store, "intent-bad-receipt", r.Root, Finalized, "not-a-commit")
		inspection, err := InspectReadiness(context.Background(), store, r.Root)
		var gitErr *gitx.Error
		if err == nil || !errors.As(err, &gitErr) {
			t.Fatalf("receipt error was not retained: %v", err)
		}
		if len(inspection.Intents) != 1 {
			t.Fatalf("receipt evidence = %+v", inspection)
		}
		evidence := inspection.Intents[0]
		gitErr = nil
		if evidence.State != ReadinessObservationError || !evidence.Finalized || evidence.ReceiptReachable ||
			!errors.As(evidence.ObservationError, &gitErr) || inspection.Ready() {
			t.Fatalf("receipt observation = %+v", inspection)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		inspection, err := InspectReadiness(ctx, NewStore(t.TempDir()), t.TempDir())
		if !errors.Is(err, context.Canceled) || !errors.Is(inspection.ObservationError, context.Canceled) ||
			inspection.KnownEmpty || inspection.Ready() {
			t.Fatalf("canceled observation = %+v, %v", inspection, err)
		}
	})
}

func createReadinessIntent(t *testing.T, store *Store, id, checkout string, status Status, artifactCommit string) Intent {
	t.Helper()
	store.newID = func() string { return id }
	intent := &Intent{
		RunID: "run-" + id, Provider: "claude", SessionID: "72b5c55e-d964-45cd-b040-cb29d0d7af05",
		RepoPath: checkout, GitCommonDir: filepath.Join(checkout, ".git"), WorktreePath: checkout,
		Branch: "main", Base: "main", Head: "0123456789abcdef0123456789abcdef01234567",
	}
	if err := store.Create(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if status != Armed || artifactCommit != "" {
		if err := store.Update(context.Background(), intent.ID, func(candidate *Intent) error {
			candidate.Status = status
			candidate.ArtifactCommit = artifactCommit
			if status == Failed {
				candidate.FailureCode = "test-failure"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := store.Get(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *stored
}

func exactSelectionFixture(t *testing.T) (*Service, *gittest.Repo, string) {
	t.Helper()
	isolateGitConfig(t)
	r := gittest.New(t)
	path := filepath.Join(r.Root, ".specstory", "history", "selected.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, path, "Codex CLI", "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2", "body")
	return &Service{Store: NewStore(t.TempDir()), ScanStaged: func(context.Context, string, []string) error { return nil }}, r, path
}

func TestPrepareExactSelectionProof(t *testing.T) {
	const sid = "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"
	for _, name := range []string{"valid duplicate", "omitted duplicate", "wrong provider", "wrong session", "outside history", "escape", "symlink", "parent symlink", "body UUID"} {
		t.Run(name, func(t *testing.T) {
			s, r, path := exactSelectionFixture(t)
			other := filepath.Join(filepath.Dir(path), "alias.md")
			writeTranscript(t, other, "Codex CLI", sid, "alias")
			request := PrepareRequest{Worktree: r.Root, Session: "codex:" + sid, SpecStoryPath: path}
			switch name {
			case "omitted duplicate":
				request.SpecStoryPath = ""
			case "wrong provider":
				request.Session = "claude:" + sid
			case "wrong session":
				request.Session = "codex:72b5c55e-d964-45cd-b040-cb29d0d7af05"
			case "outside history":
				request.SpecStoryPath = filepath.Join(r.Root, ".claude", "plans", "wrong.md")
				if err := os.MkdirAll(filepath.Dir(request.SpecStoryPath), 0o700); err != nil {
					t.Fatal(err)
				}
				writeTranscript(t, request.SpecStoryPath, "Codex CLI", sid, "outside")
			case "escape":
				request.SpecStoryPath = filepath.Join(t.TempDir(), "outside.md")
				writeTranscript(t, request.SpecStoryPath, "Codex CLI", sid, "outside")
			case "symlink":
				request.SpecStoryPath = filepath.Join(filepath.Dir(path), "link.md")
				if err := os.Symlink(path, request.SpecStoryPath); err != nil {
					t.Skip(err)
				}
			case "parent symlink":
				link := filepath.Join(filepath.Dir(path), "linked")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Skip(err)
				}
				request.SpecStoryPath = filepath.Join(link, "selected.md")
			case "body UUID":
				if err := os.WriteFile(path, []byte("<!-- Generated by SpecStory, Markdown v2.1.0 -->\n# Title\nbody\n<!-- Codex CLI Session "+sid+" (now) -->\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			intent, err := s.Prepare(t.Context(), request)
			if name != "valid duplicate" {
				if err == nil {
					t.Fatal("unproven selector was accepted")
				}
				if records, e := s.Store.List(); e != nil || len(records) != 0 {
					t.Fatalf("refused preparation wrote state: %+v %v", records, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected, _ := filepath.EvalSymlinks(path)
			if intent.SpecStoryPath != expected {
				t.Fatalf("selection=%q want %q", intent.SpecStoryPath, expected)
			}
			if _, err := s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond}); err != nil {
				t.Fatal(err)
			}
			if got := r.Git("show", "--pretty=", "--name-only", "HEAD"); got != ".specstory/history/selected.md" {
				t.Fatalf("wrong committed selection: %q", got)
			}
		})
	}
}

func TestFinalizeSelectionBindingAndLegacyCompatibility(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new binding", true: "legacy lookup"}[legacy], func(t *testing.T) {
			s, r, path := exactSelectionFixture(t)
			intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
			if err != nil {
				t.Fatal(err)
			}
			if intent.SpecStoryPath == "" {
				t.Fatal("implicit unique selection was not bound")
			}
			if legacy {
				if err := s.Store.Update(t.Context(), intent.ID, func(i *Intent) error { i.SpecStoryPath = ""; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			alias := filepath.Join(filepath.Dir(path), "alias.md")
			if err := os.Rename(path, alias); err != nil {
				t.Fatal(err)
			}
			head := r.Git("rev-parse", "HEAD")
			_, err = s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond})
			if legacy {
				if err != nil {
					t.Fatalf("legacy lookup lost compatibility: %v", err)
				}
			} else if err == nil || r.Git("rev-parse", "HEAD") != head {
				t.Fatalf("new intent substituted same-UUID alias: %v", err)
			}
		})
	}
}

func TestFinalizeGuardAtEveryMutationBoundary(t *testing.T) {
	for _, refuseAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(refuseAt), func(t *testing.T) {
			s, r, path := exactSelectionFixture(t)
			intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
			if err != nil {
				t.Fatal(err)
			}
			head := r.Git("rev-parse", "HEAD")
			before, _ := os.ReadFile(path)
			scans, guards := 0, 0
			s.ScanStaged = func(context.Context, string, []string) error { scans++; return nil }
			_, err = s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond, Guard: func(_ context.Context, root string, paths []string) error {
				guards++
				if root != intent.WorktreePath || len(paths) != 1 || paths[0] != intent.SpecStoryPath {
					t.Fatalf("wrong guard authority: %s %v", root, paths)
				}
				if guards == refuseAt {
					return errors.New("observed active writer")
				}
				return nil
			}})
			if err == nil || !strings.Contains(err.Error(), "observed active writer") {
				t.Fatalf("writer-stopped overrode observation: %v", err)
			}
			if (refuseAt < 3 && scans != 0) || (refuseAt == 3 && scans != 1) {
				t.Fatalf("scan count=%d", scans)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || r.Git("rev-parse", "HEAD") != head || r.Git("diff", "--cached", "--name-only") != "" {
				t.Fatal("guard refusal left source/index/commit effects")
			}
		})
	}
}

func TestFinalizeRevalidatesSessionAfterScanner(t *testing.T) {
	s, r, path := exactSelectionFixture(t)
	intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
	if err != nil {
		t.Fatal(err)
	}
	s.ScanStaged = func(ctx context.Context, root string, paths []string) error {
		writeTranscript(t, path, "Claude Code", "72b5c55e-d964-45cd-b040-cb29d0d7af05", "replacement")
		_, err := gitx.Run(ctx, root, "add", "--", paths[0])
		return err
	}
	head := r.Git("rev-parse", "HEAD")
	if _, err := s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "source-drift") {
		t.Fatalf("source drift=%v", err)
	}
	if r.Git("rev-parse", "HEAD") != head || r.Git("diff", "--cached", "--name-only") != "" {
		t.Fatal("source drift committed")
	}
}

func TestFinalizeCASPreservesConcurrentObservation(t *testing.T) {
	for _, scannerFails := range []bool{false, true} {
		t.Run(fmt.Sprint(scannerFails), func(t *testing.T) {
			s, r, _ := exactSelectionFixture(t)
			intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
			if err != nil {
				t.Fatal(err)
			}
			s.ScanStaged = func(ctx context.Context, _ string, _ []string) error {
				if err := s.ObserveSessionEnd(ctx, intent.RunID, time.Now()); err != nil {
					return err
				}
				if scannerFails {
					return errors.New("scanner failed")
				}
				return nil
			}
			head := r.Git("rev-parse", "HEAD")
			if _, err := s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond}); !errors.Is(err, ErrStaleRevision) {
				t.Fatalf("concurrent observation=%v", err)
			}
			current, err := s.Store.Get(intent.ID)
			if err != nil || current.Status != Finalizing || current.SessionEndedAt.IsZero() {
				t.Fatalf("observation overwritten: %+v %v", current, err)
			}
			if r.Git("rev-parse", "HEAD") != head || r.Git("diff", "--cached", "--name-only") != "" {
				t.Fatal("stale finalizer committed")
			}
		})
	}
}

func TestFinalizeRejectsReviewedStaleRevisionAndDiscard(t *testing.T) {
	s, r, _ := exactSelectionFixture(t)
	intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Store.GetRecord(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveSessionEnd(t.Context(), intent.RunID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, Revision: record.Revision, WriterStopped: true}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale reviewed finalize=%v", err)
	}
	if err := s.Store.Update(t.Context(), intent.ID, func(i *Intent) error { i.Status = Discarded; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true}); err == nil {
		t.Fatal("discarded intent resurrected")
	}
}

func TestPrepareRechecksSourceAndIndexAfterWaiting(t *testing.T) {
	for _, sourceDrift := range []bool{false, true} {
		t.Run(fmt.Sprint(sourceDrift), func(t *testing.T) {
			s, r, path := exactSelectionFixture(t)
			s.beforeIntentCreate = func() {
				if sourceDrift {
					writeTranscript(t, path, "Claude Code", "72b5c55e-d964-45cd-b040-cb29d0d7af05", "replacement")
				} else {
					r.Write("product.txt", "staged product")
					r.Git("add", "product.txt")
				}
			}
			if _, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"}); err == nil {
				t.Fatal("drift before arming accepted")
			}
			if intents, err := s.Store.List(); err != nil || len(intents) != 0 {
				t.Fatalf("stale preparation created intent: %+v %v", intents, err)
			}
		})
	}
}

func TestFinalizeNeverPassesStagedProductsToLegacyScanner(t *testing.T) {
	for _, stageAt := range []string{"before finalize", "before scanner"} {
		t.Run(stageAt, func(t *testing.T) {
			s, r, _ := exactSelectionFixture(t)
			intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
			if err != nil {
				t.Fatal(err)
			}
			stage := func() { r.Write("product.txt", "staged product"); r.Git("add", "product.txt") }
			if stageAt == "before finalize" {
				stage()
			}
			scanned := false
			s.ScanStaged = func(context.Context, string, []string) error { scanned = true; return nil }
			calls := 0
			_, err = s.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond, Guard: func(context.Context, string, []string) error {
				calls++
				if stageAt == "before scanner" && calls == 2 {
					stage()
				}
				return nil
			}})
			if err == nil || scanned {
				t.Fatalf("staged product reached scanner: %v scanned=%v", err, scanned)
			}
			if staged := r.Git("diff", "--cached", "--name-only"); staged != "product.txt" {
				t.Fatalf("foreign index content changed: %q", staged)
			}
		})
	}
}

func TestObserveSessionEndDuringFinalizeDoesNotWaitForScanner(t *testing.T) {
	s, r, _ := exactSelectionFixture(t)
	intent, err := s.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s.ScanStaged = func(ctx context.Context, _ string, _ []string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := s.Finalize(ctx, FinalizeRequest{IntentID: intent.ID, WriterStopped: true, Settle: time.Millisecond})
		finished <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("scanner was not reached")
	}
	err = s.ObserveSessionEnd(ctx, intent.RunID, time.Now())
	close(release)
	if err != nil {
		t.Fatalf("observation blocked on long scanner: %v", err)
	}
	if err = <-finished; !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale finalizer=%v", err)
	}
	current, err := s.Store.Get(intent.ID)
	if err != nil || current.SessionEndedAt.IsZero() || current.Status != Finalizing {
		t.Fatalf("concurrent observation lost: %+v %v", current, err)
	}
}

func TestObserveSessionEndDoesNotHideStoreErrors(t *testing.T) {
	s := &Service{Store: NewStore(t.TempDir())}
	if err := s.ObserveSessionEnd(t.Context(), "absent", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Store.Dir, "broken.json"), []byte("invalid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveSessionEnd(t.Context(), "absent", time.Now()); err == nil {
		t.Fatal("failed observation became an absent intent")
	}
}
