package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
