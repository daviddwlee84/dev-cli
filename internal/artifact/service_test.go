package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
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
		if finalizeErr != nil {
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
