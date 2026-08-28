package artifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const DefaultLargeLimit int64 = 2 << 20

type PrepareRequest struct {
	Worktree   string
	TaskID     string
	Session    string
	RunID      string
	Base       string
	Plans      []string
	AllowLarge bool
}

type FinalizeRequest struct {
	IntentID      string
	RunID         string
	Settle        time.Duration
	WriterStopped bool
}

// Scanner must redact/scan the staged paths without emitting secret values.
type Scanner func(context.Context, string, []string) error

// Service coordinates Git with the durable intent store.
type Service struct {
	Store      *Store
	ScanStaged Scanner
	LargeLimit int64
}

func (s *Service) Prepare(ctx context.Context, request PrepareRequest) (*Intent, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("artifact service needs a store")
	}
	provider, sessionID, err := ParseSession(request.Session)
	if err != nil {
		return nil, err
	}
	repository, err := gitx.Discover(ctx, request.Worktree)
	if err != nil {
		return nil, err
	}
	status, err := gitx.StatusOf(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	if status.Detached || status.Branch == "" {
		return nil, fmt.Errorf("artifact preparation requires a named branch")
	}
	if operation, active, err := gitx.InProgress(repository.Root); err != nil {
		return nil, err
	} else if active {
		return nil, fmt.Errorf("artifact preparation refuses Git operation %s in progress", operation)
	}
	head, err := gitx.Run(ctx, repository.Root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	changes, err := statusPaths(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	var unrelated []string
	for _, change := range changes {
		if change.Staged {
			return nil, fmt.Errorf("artifact preparation requires an empty index; staged path: %s", change.Path)
		}
		if !isRecognizedArtifact(change.Path) {
			return nil, fmt.Errorf("commit product changes before prepare; dirty path: %s", change.Path)
		}
		unrelated = append(unrelated, change.Path)
	}
	transcript, err := FindTranscript(repository.Root, provider, sessionID)
	if err != nil {
		return nil, err
	}
	tracked, err := gitTracked(ctx, repository.Root, transcript.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(transcript.Path)
	if err != nil {
		return nil, err
	}
	limit := s.LargeLimit
	if limit <= 0 {
		limit = DefaultLargeLimit
	}
	if !tracked && info.Size() > limit && !request.AllowLarge {
		return nil, fmt.Errorf("untracked transcript is %d bytes; re-run with --allow-large after reviewing repository growth", info.Size())
	}
	plans, err := normalizePlans(repository.Root, request.Plans)
	if err != nil {
		return nil, err
	}
	runID := request.RunID
	if runID == "" {
		runID = os.Getenv("DEV_AGENT_RUN_ID")
	}
	if runID == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	selected := make(map[string]bool, len(plans)+1)
	relTranscript, _ := filepath.Rel(repository.Root, transcript.Path)
	selected[filepath.ToSlash(relTranscript)] = true
	for _, plan := range plans {
		rel, _ := filepath.Rel(repository.Root, plan)
		selected[filepath.ToSlash(rel)] = true
	}
	filtered := unrelated[:0]
	for _, path := range unrelated {
		if !selected[path] {
			filtered = append(filtered, path)
		}
	}
	intent := &Intent{
		RunID: runID, Provider: provider, SessionID: sessionID, TaskID: request.TaskID,
		RepoPath: repository.MainRoot, GitCommonDir: repository.GitCommonDir,
		WorktreePath: repository.Root, Branch: status.Branch, Base: request.Base,
		Head: head, PlanPaths: plans, UnrelatedArtifacts: filtered, AllowLarge: request.AllowLarge,
	}
	if err := s.Store.Create(ctx, intent); err != nil {
		return nil, err
	}
	return intent, nil
}

func (s *Service) ObserveSessionEnd(ctx context.Context, runID string, when time.Time) error {
	intent, err := s.Store.FindByRunID(runID)
	if err != nil {
		return nil // SessionEnd without an armed intent is deliberately a no-op.
	}
	if when.IsZero() {
		when = time.Now()
	}
	return s.Store.Update(ctx, intent.ID, func(candidate *Intent) error {
		candidate.SessionEndedAt = when.UTC().Truncate(time.Second)
		return nil
	})
}

func (s *Service) Finalize(ctx context.Context, request FinalizeRequest) (*Intent, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("artifact service needs a store")
	}
	intent, err := s.resolveIntent(request)
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(intent.GitCommonDir, "dev-artifact-finalize")
	var finalized *Intent
	err = catalog.NewStore(lockDir).WithLock(ctx, func() error {
		current, err := s.resolveIntent(request)
		if err != nil {
			return err
		}
		finalized, err = s.finalizeLocked(ctx, request, current)
		return err
	})
	return finalized, err
}

func (s *Service) finalizeLocked(ctx context.Context, request FinalizeRequest, intent *Intent) (*Intent, error) {
	if intent.Status == Finalized {
		if commit, ok := findReceipt(ctx, intent.WorktreePath, intent.ID); ok && commit != intent.ArtifactCommit {
			if err := s.markFinalized(ctx, intent.ID, intent.TranscriptPath, commit); err != nil {
				return nil, err
			}
			return s.Store.Get(intent.ID)
		}
		return intent, nil
	}
	if commit, ok := findReceipt(ctx, intent.WorktreePath, intent.ID); ok {
		if err := s.markFinalized(ctx, intent.ID, intent.TranscriptPath, commit); err != nil {
			return nil, err
		}
		return s.Store.Get(intent.ID)
	}
	if s.ScanStaged == nil {
		return nil, s.fail(ctx, intent.ID, "scanner-missing", fmt.Errorf("artifact finalization requires a staged secret scanner"))
	}
	if err := s.revalidate(ctx, intent); err != nil {
		return nil, s.fail(ctx, intent.ID, "git-drift", err)
	}
	if intent.SessionEndedAt.IsZero() && !request.WriterStopped {
		return nil, s.fail(ctx, intent.ID, "writer-unproven",
			fmt.Errorf("finalization requires SessionEnd observation or explicit post-writer proof"))
	}
	transcript, err := FindTranscript(intent.WorktreePath, intent.Provider, intent.SessionID)
	if err != nil {
		return nil, s.fail(ctx, intent.ID, "transcript-missing", err)
	}
	if err := s.Store.Update(ctx, intent.ID, func(candidate *Intent) error {
		candidate.Status = Finalizing
		candidate.TranscriptPath = transcript.Path
		candidate.FailureCode = ""
		return nil
	}); err != nil {
		return nil, err
	}
	if _, err := StableSnapshot(ctx, transcript.Path, request.Settle); err != nil {
		return nil, s.fail(ctx, intent.ID, "writer-active", err)
	}
	paths := append([]string{transcript.Path}, intent.PlanPaths...)
	if err := ensureOnlyAllowedIndex(ctx, intent.WorktreePath, nil); err != nil {
		return nil, s.fail(ctx, intent.ID, "index-not-empty", err)
	}
	if _, err := gitx.Run(ctx, intent.WorktreePath, append([]string{"add", "--"}, paths...)...); err != nil {
		return nil, s.fail(ctx, intent.ID, "stage-failed", err)
	}
	cleanup := func() {
		_, _ = gitx.Run(context.Background(), intent.WorktreePath, append([]string{"reset", "--"}, paths...)...)
	}
	if err := s.ScanStaged(ctx, intent.WorktreePath, paths); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "scan-failed", err)
	}
	if _, err := StableSnapshot(ctx, transcript.Path, request.Settle); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "writer-active", err)
	}
	if err := ensureOnlyAllowedIndex(ctx, intent.WorktreePath, paths); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "index-drift", err)
	}
	if err := stagedFilesMatch(ctx, intent.WorktreePath, paths); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "index-drift", err)
	}
	message := fmt.Sprintf("chore: finalize %s agent session", intent.Provider)
	trailers := fmt.Sprintf("Agent-Artifact-Session: %s:%s\nDev-Artifact-Intent: %s", intent.Provider, intent.SessionID, intent.ID)
	if _, err := gitx.Run(ctx, intent.WorktreePath, "commit", "-m", message, "-m", trailers); err != nil {
		cleanup()
		return nil, s.fail(ctx, intent.ID, "commit-failed", err)
	}
	commit, err := gitx.Run(ctx, intent.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return nil, s.fail(ctx, intent.ID, "receipt-failed", err)
	}
	if err := s.markFinalized(ctx, intent.ID, transcript.Path, commit); err != nil {
		return nil, err
	}
	return s.Store.Get(intent.ID)
}

func (s *Service) resolveIntent(request FinalizeRequest) (*Intent, error) {
	if request.IntentID != "" {
		return s.Store.Get(request.IntentID)
	}
	if request.RunID != "" {
		return s.Store.FindByRunID(request.RunID)
	}
	return nil, fmt.Errorf("finalize requires an intent or run id")
}

func (s *Service) revalidate(ctx context.Context, intent *Intent) error {
	repository, err := gitx.Discover(ctx, intent.WorktreePath)
	if err != nil {
		return err
	}
	if repository.GitCommonDir != intent.GitCommonDir {
		return fmt.Errorf("Git common-dir changed")
	}
	status, err := gitx.StatusOf(ctx, intent.WorktreePath)
	if err != nil {
		return err
	}
	if status.Branch != intent.Branch || status.Detached {
		return fmt.Errorf("branch changed from %s to %s", intent.Branch, status.Branch)
	}
	if operation, active, err := gitx.InProgress(intent.WorktreePath); err != nil {
		return err
	} else if active {
		return fmt.Errorf("Git operation %s is in progress", operation)
	}
	head, err := gitx.Run(ctx, intent.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != intent.Head {
		return fmt.Errorf("HEAD changed from %s to %s", intent.Head, head)
	}
	return nil
}

func (s *Service) fail(ctx context.Context, id, code string, cause error) error {
	_ = s.Store.Update(ctx, id, func(intent *Intent) error {
		intent.Status = Failed
		intent.FailureCode = code
		return nil
	})
	return fmt.Errorf("artifact finalization %s: %w", code, cause)
}

func (s *Service) markFinalized(ctx context.Context, id, transcript, commit string) error {
	return s.Store.Update(ctx, id, func(intent *Intent) error {
		intent.Status = Finalized
		intent.TranscriptPath = transcript
		intent.ArtifactCommit = commit
		intent.FailureCode = ""
		return nil
	})
}

type statusPath struct {
	Path   string
	Staged bool
}

func statusPaths(ctx context.Context, repo string) ([]statusPath, error) {
	out, err := gitx.Run(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var paths []statusPath
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		code, path := entry[:2], filepath.ToSlash(entry[3:])
		paths = append(paths, statusPath{Path: path, Staged: code[0] != ' ' && code[0] != '?'})
		if code[0] == 'R' || code[0] == 'C' {
			i++ // porcelain -z emits the source path as the next field.
		}
	}
	return paths, nil
}

func isRecognizedArtifact(path string) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	for _, prefix := range []string{
		".specstory/history/", ".claude/plans/", ".cursor/plans/", ".cursor/rules/",
		".opencode/plans/", ".specify/", ".codex/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return path == ".specstory/statistics.json"
}

func normalizePlans(root string, paths []string) ([]string, error) {
	var out []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		inside, err := pathx.Contains(filepath.Join(root, ".claude", "plans"), path)
		if err != nil || !inside {
			return nil, fmt.Errorf("plan %s is outside .claude/plans", path)
		}
		canonical, err := pathx.Canonical(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("plan %s is not a regular file", path)
		}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out, nil
}

func gitTracked(ctx context.Context, repo, path string) (bool, error) {
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		return false, err
	}
	_, err = gitx.Run(ctx, repo, "ls-files", "--error-unmatch", "--", rel)
	if err == nil {
		return true, nil
	}
	var gitErr *gitx.Error
	if errors.As(err, &gitErr) {
		return false, nil
	}
	return false, err
}

func ensureOnlyAllowedIndex(ctx context.Context, repo string, allowed []string) error {
	out, err := gitx.Run(ctx, repo, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return err
	}
	allow := make(map[string]bool, len(allowed))
	for _, path := range allowed {
		rel, _ := filepath.Rel(repo, path)
		allow[filepath.ToSlash(rel)] = true
	}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" && !allow[filepath.ToSlash(path)] {
			return fmt.Errorf("index contains unrelated path %s", path)
		}
	}
	return nil
}

func stagedFilesMatch(ctx context.Context, repo string, paths []string) error {
	for _, path := range paths {
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		workingBlob, err := gitx.Run(ctx, repo, "hash-object", "--", rel)
		if err != nil {
			return err
		}
		stagedBlob, err := gitx.Run(ctx, repo, "rev-parse", ":"+filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if workingBlob != stagedBlob {
			return fmt.Errorf("staged artifact %s no longer matches scanned working bytes", filepath.ToSlash(rel))
		}
	}
	return nil
}

func findReceipt(ctx context.Context, repo, intentID string) (string, bool) {
	out, err := gitx.Run(ctx, repo, "log", "-n", "100", "--format=%H%x00%B%x00")
	if err != nil {
		return "", false
	}
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if strings.Contains(fields[i+1], "Dev-Artifact-Intent: "+intentID) {
			return fields[i], true
		}
	}
	return "", false
}
