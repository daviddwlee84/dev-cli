package gitx_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestUncommitAndRecommitReuseFullMessage(t *testing.T) {
	isolateTransactions(t)
	r := gittest.New(t)
	r.Write("feature.txt", "feature\n")
	r.Git("add", "feature.txt")
	r.Git("commit", "-m", "feat: subject", "-m", "body line one\nbody line two")
	old := r.Git("rev-parse", "HEAD")
	parent := r.Git("rev-parse", "HEAD^")

	receipt, err := gitx.Uncommit(context.Background(), r.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.OldOID != old || receipt.Parent != parent || r.Git("rev-parse", "HEAD") != parent {
		t.Fatalf("receipt/head = %+v head=%s", receipt, r.Git("rev-parse", "HEAD"))
	}
	status, err := gitx.StatusOf(context.Background(), r.Root)
	if err != nil || status.Staged == 0 {
		t.Fatalf("uncommitted tree should be staged: %+v, %v", status, err)
	}
	mergeHead := filepath.Join(r.Root, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte(old+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Recommit(context.Background(), r.Root); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("recommit should refuse another Git operation: %v", err)
	}
	if err := os.Remove(mergeHead); err != nil {
		t.Fatal(err)
	}
	commit, err := gitx.Recommit(context.Background(), r.Root)
	if err != nil || commit == "" {
		t.Fatalf("recommit = %q, %v", commit, err)
	}
	message := r.Git("log", "-1", "--format=%B")
	if !strings.Contains(message, "feat: subject") || !strings.Contains(message, "body line one\nbody line two") {
		t.Fatalf("message was not reused:\n%s", message)
	}
	if _, err := gitx.Recommit(context.Background(), r.Root); err == nil {
		t.Fatal("receipt should be removed after recommit")
	}
}

func TestUncommitRefusesRootMergeAndStagedMix(t *testing.T) {
	isolateTransactions(t)
	r := gittest.New(t)
	if _, err := gitx.Uncommit(context.Background(), r.Root, false); err == nil || !strings.Contains(err.Error(), "root commit") {
		t.Fatalf("root uncommit = %v", err)
	}
	r.Commit("second.txt", "two\n", "second")
	r.Write("staged.txt", "staged\n")
	r.Git("add", "staged.txt")
	if _, err := gitx.Uncommit(context.Background(), r.Root, false); err == nil || !strings.Contains(err.Error(), "empty index") {
		t.Fatalf("staged mix should fail: %v", err)
	}
}

func TestPullRebaseRestoresExactStashAndIndex(t *testing.T) {
	isolateTransactions(t)
	r := gittest.New(t)
	r.WithRemote()

	r.Write("old-stash.txt", "older\n")
	r.Git("stash", "push", "--include-untracked", "-m", "unrelated")
	unrelated := r.Git("rev-parse", "refs/stash")

	r.Commit("remote.txt", "remote\n", "remote update")
	r.Git("push", "origin", "main")
	r.Git("reset", "--hard", "HEAD^")
	r.Write("staged.txt", "staged\n")
	r.Git("add", "staged.txt")
	r.Write("untracked.txt", "untracked\n")
	if err := os.WriteFile(filepath.Join(r.Root, "README.md"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := gitx.PullRebase(context.Background(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HadLocalWork || !result.Restored || !result.Dropped || result.StashOID == "" || result.StashOID == unrelated {
		t.Fatalf("pull result = %+v", result)
	}
	if got := r.Git("rev-parse", "HEAD"); got != r.Git("rev-parse", "origin/main") {
		t.Fatalf("HEAD %s != origin/main", got)
	}
	if staged := r.Git("diff", "--cached", "--name-only"); staged != "staged.txt" {
		t.Fatalf("staged state = %q", staged)
	}
	for _, path := range []string{"staged.txt", "untracked.txt"} {
		if _, err := os.Stat(filepath.Join(r.Root, path)); err != nil {
			t.Fatalf("restored %s: %v", path, err)
		}
	}
	if got := r.Git("rev-parse", "refs/stash"); got != unrelated {
		t.Fatalf("unrelated stash moved: got=%s want=%s", got, unrelated)
	}
}

func TestAmendAllCanExcludeAgentArtifacts(t *testing.T) {
	isolateTransactions(t)
	r := gittest.New(t)
	r.Write("product.txt", "product\n")
	r.Write(".specstory/history/session.md", "transcript\n")
	r.Git("add", ".specstory/history/session.md")
	commit, excluded, err := gitx.AmendAll(context.Background(), r.Root, gitx.AmendOptions{ExcludeArtifacts: true})
	if err != nil || commit == "" {
		t.Fatalf("amend = %q, %v", commit, err)
	}
	if len(excluded) != 1 || excluded[0] != ".specstory/history/session.md" {
		t.Fatalf("excluded = %v", excluded)
	}
	if got := r.Git("show", "--pretty=", "--name-only", "HEAD"); !strings.Contains(got, "product.txt") || strings.Contains(got, ".specstory") {
		t.Fatalf("amended paths:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(r.Root, ".specstory/history/session.md")); err != nil {
		t.Fatal(err)
	}
	if staged := r.Git("diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("excluded artifact remained staged: %q", staged)
	}
}

func isolateTransactions(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// Git leaves REBASE_HEAD behind after a rebase completes. Treating that file as
// evidence of an in-progress operation permanently blocked retirement for every
// worktree that had ever been rebased, so it must not count.
func TestInProgressIgnoresRebaseHeadLeftByACompletedRebase(t *testing.T) {
	isolateTransactions(t)
	r := gittest.New(t)
	r.Write("feature.txt", "feature\n")
	r.Git("add", "feature.txt")
	r.Git("commit", "-m", "feat: work")

	if err := os.WriteFile(filepath.Join(r.Root, ".git", "REBASE_HEAD"),
		[]byte(r.Git("rev-parse", "HEAD")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	operation, active, err := gitx.InProgress(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatalf("leftover REBASE_HEAD reported %q as in progress", operation)
	}

	// A real interrupted rebase still has to be reported.
	if err := os.MkdirAll(filepath.Join(r.Root, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	operation, active, err = gitx.InProgress(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !active || operation != "rebase-merge" {
		t.Fatalf("in-progress rebase = %q/%v", operation, active)
	}
}
