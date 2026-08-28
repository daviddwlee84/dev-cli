package gitx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
)

type UncommitReceipt struct {
	OldOID  string    `json:"old_oid"`
	Parent  string    `json:"parent"`
	Branch  string    `json:"branch"`
	Created time.Time `json:"created"`
}

type PullRebaseResult struct {
	StashOID     string
	Restored     bool
	Dropped      bool
	HadLocalWork bool
}

type AmendOptions struct {
	RewritePublished bool
	ExcludeArtifacts bool
}

func InProgress(dir string) (string, bool, error) {
	repository, err := Discover(context.Background(), dir)
	if err != nil {
		return "", false, err
	}
	for _, candidate := range []string{
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD",
		"rebase-merge", "rebase-apply", "sequencer",
	} {
		for _, root := range []string{repository.GitDir, repository.GitCommonDir} {
			if _, err := os.Stat(filepath.Join(root, candidate)); err == nil {
				return candidate, true, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", false, err
			}
		}
	}
	return "", false, nil
}

func Uncommit(ctx context.Context, dir string, rewritePublished bool) (*UncommitReceipt, error) {
	repository, status, err := transactionPreflight(ctx, dir, rewritePublished)
	if err != nil {
		return nil, err
	}
	if status.Staged > 0 {
		return nil, fmt.Errorf("uncommit requires an empty index so existing staged work is not mixed")
	}
	line, err := Run(ctx, repository.Root, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		if len(fields) == 1 {
			return nil, fmt.Errorf("cannot uncommit the root commit")
		}
		return nil, fmt.Errorf("cannot uncommit a merge commit; parent topology would be lost")
	}
	receipt := &UncommitReceipt{OldOID: fields[0], Parent: fields[1], Branch: status.Branch, Created: time.Now().UTC()}
	path := uncommitReceiptPath(repository.GitDir)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("an uncommit receipt already exists; run dev git recommit first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := writeJSONAtomic(path, receipt); err != nil {
		return nil, err
	}
	if _, err := Run(ctx, repository.Root, "reset", "--soft", receipt.Parent); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return receipt, nil
}

func Recommit(ctx context.Context, dir string) (string, error) {
	repository, err := Discover(ctx, dir)
	if err != nil {
		return "", err
	}
	if operation, active, err := InProgress(repository.Root); err != nil {
		return "", err
	} else if active {
		return "", fmt.Errorf("Git operation %s is in progress", operation)
	}
	path := uncommitReceiptPath(repository.GitDir)
	receipt, err := readReceipt(path)
	if err != nil {
		return "", err
	}
	status, err := StatusOf(ctx, repository.Root)
	if err != nil {
		return "", err
	}
	if status.Detached || status.Branch != receipt.Branch {
		return "", fmt.Errorf("branch changed from %s to %s", receipt.Branch, status.Branch)
	}
	if status.Staged == 0 {
		return "", fmt.Errorf("nothing is staged to recommit")
	}
	head, err := Run(ctx, repository.Root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if head != receipt.Parent {
		return "", fmt.Errorf("HEAD changed since uncommit: expected %s, got %s", receipt.Parent, head)
	}
	if !RefExists(ctx, repository.Root, receipt.OldOID) {
		return "", fmt.Errorf("original commit %s is no longer available", receipt.OldOID)
	}
	if _, err := Run(ctx, repository.Root, "commit", "-C", receipt.OldOID); err != nil {
		return "", err
	}
	commit, err := Run(ctx, repository.Root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("commit created, but remove uncommit receipt: %w", err)
	}
	return commit, nil
}

func PullRebase(ctx context.Context, dir string) (PullRebaseResult, error) {
	repository, status, err := transactionPreflight(ctx, dir, true)
	if err != nil {
		return PullRebaseResult{}, err
	}
	if status.Upstream == "" {
		return PullRebaseResult{}, fmt.Errorf("branch %s has no upstream", status.Branch)
	}
	var result PullRebaseResult
	lockDir := filepath.Join(repository.GitCommonDir, "dev-git-transactions")
	err = lockx.WithDir(ctx, lockDir, "git transaction", func() error {
		current, err := StatusOf(ctx, repository.Root)
		if err != nil {
			return err
		}
		if current.Dirty() {
			result.HadLocalWork = true
			tag := fmt.Sprintf("dev-pull-rebase-%d", time.Now().UnixNano())
			if _, err := Run(ctx, repository.Root, "stash", "push", "--include-untracked", "-m", tag); err != nil {
				return err
			}
			result.StashOID, err = stashOIDForTag(ctx, repository.Root, tag)
			if err != nil {
				return fmt.Errorf("capture created stash oid: %w", err)
			}
			if result.StashOID == "" {
				return fmt.Errorf("Git reported local work but created no stash; refusing to reuse an existing stash")
			}
		}
		if _, err := Run(ctx, repository.Root, "-c", "rebase.autoStash=false", "pull", "--rebase"); err != nil {
			return fmt.Errorf("pull --rebase failed; local work remains in stash %s: %w", result.StashOID, err)
		}
		if result.StashOID == "" {
			return nil
		}
		if _, err := Run(ctx, repository.Root, "stash", "apply", "--index", result.StashOID); err != nil {
			return fmt.Errorf("restore local work from stash %s failed; stash retained: %w", result.StashOID, err)
		}
		result.Restored = true
		selector, ok, err := stashSelector(ctx, repository.Root, result.StashOID)
		if err != nil {
			return err
		}
		if !ok {
			return nil // Applied bytes are safe; retaining an extra stash beats dropping the wrong one.
		}
		if _, err := Run(ctx, repository.Root, "stash", "drop", selector); err != nil {
			return fmt.Errorf("local work restored, but exact stash %s was retained: %w", result.StashOID, err)
		}
		result.Dropped = true
		return nil
	})
	return result, err
}

func AmendAll(ctx context.Context, dir string, options AmendOptions) (string, []string, error) {
	repository, _, err := transactionPreflight(ctx, dir, options.RewritePublished)
	if err != nil {
		return "", nil, err
	}
	var excluded []string
	args := []string{"add", "-A", "--", "."}
	if options.ExcludeArtifacts {
		for _, pattern := range AgentArtifactPathspecs() {
			args = append(args, ":(exclude)"+pattern)
		}
		paths, err := ChangedPaths(ctx, repository.Root)
		if err != nil {
			return "", nil, err
		}
		for _, path := range paths {
			if IsAgentArtifact(path) {
				excluded = append(excluded, path)
			}
		}
		sortStrings(excluded)
	}
	if _, err := Run(ctx, repository.Root, args...); err != nil {
		return "", excluded, err
	}
	if options.ExcludeArtifacts {
		resetArgs := append([]string{"reset", "--"}, AgentArtifactPathspecs()...)
		if _, err := Run(ctx, repository.Root, resetArgs...); err != nil {
			return "", excluded, fmt.Errorf("unstage excluded agent artifacts: %w", err)
		}
	}
	staged, err := Run(ctx, repository.Root, "diff", "--cached", "--name-only")
	if err != nil {
		return "", excluded, err
	}
	if strings.TrimSpace(staged) == "" {
		return "", excluded, fmt.Errorf("nothing is staged to amend")
	}
	if _, err := Run(ctx, repository.Root, "commit", "--amend", "--no-edit"); err != nil {
		return "", excluded, err
	}
	commit, err := Run(ctx, repository.Root, "rev-parse", "HEAD")
	return commit, excluded, err
}

func ChangedPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := Run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		code := entry[:2]
		paths = append(paths, filepath.ToSlash(entry[3:]))
		if code[0] == 'R' || code[0] == 'C' {
			i++
		}
	}
	return paths, nil
}

func IsAgentArtifact(path string) bool {
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

func AgentArtifactPathspecs() []string {
	return []string{
		".specstory/history/**", ".specstory/statistics.json", ".claude/plans/**",
		".cursor/plans/**", ".cursor/rules/**", ".opencode/plans/**", ".specify/**", ".codex/**",
	}
}

func transactionPreflight(ctx context.Context, dir string, rewritePublished bool) (Repo, Status, error) {
	repository, err := Discover(ctx, dir)
	if err != nil {
		return Repo{}, Status{}, err
	}
	if repository.Bare {
		return Repo{}, Status{}, fmt.Errorf("transaction requires a working tree")
	}
	status, err := StatusOf(ctx, repository.Root)
	if err != nil {
		return Repo{}, Status{}, err
	}
	if status.Detached || status.Branch == "" {
		return Repo{}, Status{}, fmt.Errorf("transaction refuses detached HEAD")
	}
	if status.Conflicted > 0 {
		return Repo{}, Status{}, fmt.Errorf("transaction refuses %d conflicted path(s)", status.Conflicted)
	}
	if operation, active, err := InProgress(repository.Root); err != nil {
		return Repo{}, Status{}, err
	} else if active {
		return Repo{}, Status{}, fmt.Errorf("Git operation %s is in progress", operation)
	}
	if status.Published() && status.Ahead == 0 && !rewritePublished {
		return Repo{}, Status{}, fmt.Errorf("HEAD is already contained in %s; pass --rewrite-published to acknowledge rewriting it", status.Upstream)
	}
	return repository, status, nil
}

func uncommitReceiptPath(gitDir string) string { return filepath.Join(gitDir, "dev-uncommit.json") }

func readReceipt(path string) (*UncommitReceipt, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var receipt UncommitReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	if receipt.OldOID == "" || receipt.Parent == "" || receipt.Branch == "" {
		return nil, fmt.Errorf("invalid uncommit receipt")
	}
	return &receipt, nil
}

func writeJSONAtomic(path string, value any) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dev-receipt-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func stashSelector(ctx context.Context, dir, oid string) (string, bool, error) {
	out, err := Run(ctx, dir, "stash", "list", "--format=%H%x09%gd")
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		got, selector, ok := strings.Cut(line, "\t")
		if ok && got == oid {
			return selector, true, nil
		}
	}
	return "", false, nil
}

func stashOIDForTag(ctx context.Context, dir, tag string) (string, error) {
	out, err := Run(ctx, dir, "stash", "list", "--format=%H%x09%gs")
	if err != nil {
		return "", err
	}
	var match string
	for _, line := range strings.Split(out, "\n") {
		oid, subject, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasSuffix(subject, ": "+tag) {
			continue
		}
		if match != "" && match != oid {
			return "", fmt.Errorf("multiple stashes matched transaction tag %s", tag)
		}
		match = oid
	}
	return match, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
