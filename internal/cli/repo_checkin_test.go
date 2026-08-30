package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func TestSeedLazygitPendingCommitPreservesExistingDraft(t *testing.T) {
	root := t.TempDir()
	if _, err := gitx.Run(t.Context(), root, "init"); err != nil {
		t.Fatal(err)
	}
	provider, warning, err := seedLazygitPendingCommit(t.Context(), root, "chore: initial commit")
	if err != nil || provider != "lazygit" || warning != "" {
		t.Fatalf("seed = provider %q warning %q err %v", provider, warning, err)
	}
	repository, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository.GitDir, "LAZYGIT_PENDING_COMMIT")
	if body, err := os.ReadFile(path); err != nil || string(body) != "chore: initial commit\n" {
		t.Fatalf("draft = %q, %v", body, err)
	}
	provider, warning, err = seedLazygitPendingCommit(t.Context(), root, "feat: do not overwrite")
	if err != nil || provider != "" || !strings.Contains(warning, "preserved") {
		t.Fatalf("existing draft = provider %q warning %q err %v", provider, warning, err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "chore: initial commit\n" {
		t.Fatalf("existing draft changed = %q, %v", body, err)
	}
}

func TestSeedLazygitPendingCommitRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if _, err := gitx.Run(t.Context(), root, "init"); err != nil {
		t.Fatal(err)
	}
	repository, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "draft")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository.GitDir, "LAZYGIT_PENDING_COMMIT")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := seedLazygitPendingCommit(t.Context(), root, "replace"); err == nil {
		t.Fatal("symlinked lazygit draft was accepted")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep\n" {
		t.Fatalf("outside draft changed = %q, %v", body, err)
	}
}

func TestSeedLazygitPendingCommitUsesLinkedWorktreeGitDir(t *testing.T) {
	root := t.TempDir()
	if _, err := gitx.Run(t.Context(), root, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), root, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), root, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if _, err := gitx.Run(t.Context(), root, "worktree", "add", "-b", "review", linked); err != nil {
		t.Fatal(err)
	}
	provider, warning, err := seedLazygitPendingCommit(t.Context(), linked, "chore: linked draft")
	if err != nil || provider != "lazygit" || warning != "" {
		t.Fatalf("seed = provider %q warning %q err %v", provider, warning, err)
	}
	repository, err := gitx.Discover(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if repository.GitDir == repository.GitCommonDir {
		t.Fatal("fixture did not create a linked worktree Git directory")
	}
	if body, err := os.ReadFile(filepath.Join(repository.GitDir, "LAZYGIT_PENDING_COMMIT")); err != nil || string(body) != "chore: linked draft\n" {
		t.Fatalf("worktree draft = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(repository.GitCommonDir, "LAZYGIT_PENDING_COMMIT")); !os.IsNotExist(err) {
		t.Fatalf("draft leaked into common Git dir: %v", err)
	}
}

func TestStageRepoForReviewKeepsSuccessfulStagingWhenDraftFails(t *testing.T) {
	root := t.TempDir()
	if _, err := gitx.Run(t.Context(), root, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository.GitDir, "LAZYGIT_PENDING_COMMIT"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged, provider, warning, err := stageRepoForReview(t.Context(), root, "chore: initial commit")
	if err != nil || staged != 1 || provider != "" || !strings.Contains(warning, "could not be prefilled") {
		t.Fatalf("stage = %d provider %q warning %q err %v", staged, provider, warning, err)
	}
	status, err := gitx.StatusOf(t.Context(), root)
	if err != nil || status.Staged != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestResolveRepoCheckInKeepsCommitFlagCompatibility(t *testing.T) {
	mode, err := resolveRepoCheckIn(repoBootstrapFlags{commit: true}, preparedRepoScaffold{}, "")
	if err != nil || mode != repoCheckInCommit {
		t.Fatalf("legacy --commit = %q, %v", mode, err)
	}
	if _, err := resolveRepoCheckIn(repoBootstrapFlags{commit: true, checkIn: "stage"}, preparedRepoScaffold{}, ""); err == nil {
		t.Fatal("--commit and --check-in=stage did not conflict")
	}
}
