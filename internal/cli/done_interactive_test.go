package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func runDoneForTest(f *startFixture, input string, interactive bool, args ...string) error {
	f.t.Helper()
	f.app.In = strings.NewReader(input)
	f.app.interactiveCheck = func() bool { return interactive }
	cmd := newDoneCmd(f.app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func mergedTaskFixture(t *testing.T, name string) (*startFixture, string) {
	t.Helper()
	f := newStartFixture(t, runtime.None{})
	branch := "feat/" + name
	if err := f.run("--task", name, "--branch", branch, "--base", "main"); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(f.app.Cfg.Paths.WorktreeRoot, "repo", strings.ReplaceAll(branch, "/", "-"))
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(worktree, "add", "feature.txt")
	f.repo.GitIn(worktree, "commit", "-m", "feat: "+name)
	f.repo.Git("merge", "--ff-only", branch)
	return f, worktree
}

func TestDoneInteractiveDiscardUniqueAlreadyMergedCheckout(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "discard-finish")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("drop me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runDoneForTest(f, "d\nDROP\n", true, "discard-finish", "--delete-branch"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree was not removed: %v", err)
	}
	if gitx.BranchExists(ctxOf(), f.repo.Root, "feat/discard-finish") {
		t.Fatal("merged branch was not deleted")
	}
	stored, err := f.app.Tasks.Get(task.MakeID("repo", "feat/discard-finish"))
	if err != nil || stored.State != task.Done {
		t.Fatalf("task = %+v, %v", stored, err)
	}
	for _, want := range []string{"already equal to main", "1 unique", "Type DROP", "cleanup only"} {
		if !strings.Contains(f.stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, f.stdout.String())
		}
	}
}

func TestDoneInteractiveCommitThenFastForwardsNewCommit(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "commit-finish")
	if err := os.WriteFile(filepath.Join(worktree, "after-merge.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runDoneForTest(f, "c\n\n\ny\n", true, "commit-finish"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(ctxOf(), f.repo.Root, "cat-file", "-e", "main:after-merge.txt"); err != nil {
		t.Fatalf("final commit was not integrated: %v", err)
	}
	if subject := f.repo.Git("log", "-1", "--format=%s", "main"); subject != "chore: finalize commit-finish" {
		t.Fatalf("final commit subject = %q", subject)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree was not removed: %v", err)
	}
}

func TestDoneInteractiveWrongDropConfirmationCancels(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "keep-unique")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runDoneForTest(f, "d\nNO\n", true, "keep-unique"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "scratch.txt")); err != nil {
		t.Fatalf("canceled discard removed content: %v", err)
	}
	stored, err := f.app.Tasks.Get(task.MakeID("repo", "feat/keep-unique"))
	if err != nil || stored.State != task.Hot {
		t.Fatalf("task after cancel = %+v, %v", stored, err)
	}
}

func TestDoneInteractiveEquivalentDirtyNeedsOnlyNormalConfirmation(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	if err := f.run("--task", "equivalent", "--branch", "feat/equivalent", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(f.app.Cfg.Paths.WorktreeRoot, "repo", "feat-equivalent")
	f.repo.Commit("README.md", "main version\n", "feat: advance main")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("main version\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runDoneForTest(f, "d\ny\n", true, "equivalent", "--delete-branch"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.stdout.String(), "Type DROP") {
		t.Fatalf("base-equivalent discard requested DROP:\n%s", f.stdout.String())
	}
	if !strings.Contains(f.stdout.String(), "1 match main, 0 unique") {
		t.Fatalf("equivalent classification missing:\n%s", f.stdout.String())
	}
}

func TestDoneNonInteractiveCommitPolicy(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "scripted-commit")
	if err := os.WriteFile(filepath.Join(worktree, "scripted.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDoneForTest(f, "", false, "scripted-commit", "--ff", "--dirty=commit", "--message", "chore: scripted finish"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(ctxOf(), f.repo.Root, "cat-file", "-e", "main:scripted.txt"); err != nil {
		t.Fatalf("scripted commit was not integrated: %v", err)
	}
}

func TestDoneNonInteractiveDiscardRequiresYes(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "scripted-discard")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("drop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runDoneForTest(f, "", false, "scripted-discard", "--ff", "--dirty=discard")
	if err == nil || !strings.Contains(err.Error(), "requires --yes") {
		t.Fatalf("discard without --yes = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, "scratch.txt")); statErr != nil {
		t.Fatalf("refused discard changed checkout: %v", statErr)
	}
}

func TestDoneNonInteractiveDiscardYesDropsAllDirtyLayers(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "scripted-drop-all")
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("staged change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(worktree, "add", "feature.txt")
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("unstaged change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDoneForTest(f, "", false, "scripted-drop-all", "--ff", "--dirty=discard", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := f.repo.Git("show", "main:feature.txt"); got != "done" {
		t.Fatalf("discard changed integrated content: %q", got)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree was not removed: %v", err)
	}
}

func TestDoneContainedPRRefusesBeforeDiscard(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "contained-pr")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runDoneForTest(f, "", false, "contained-pr", "--pr", "--dirty=discard", "--yes")
	if err == nil || !strings.Contains(err.Error(), "already contained") {
		t.Fatalf("contained PR error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "scratch.txt")); err != nil {
		t.Fatalf("invalid PR plan discarded content: %v", err)
	}
}

func TestDoneBareInteractiveChoosesFastForward(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	if err := f.run("--task", "choose-ff", "--branch", "feat/choose-ff", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(f.app.Cfg.Paths.WorktreeRoot, "repo", "feat-choose-ff")
	if err := os.WriteFile(filepath.Join(worktree, "choice.txt"), []byte("ff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(worktree, "add", "choice.txt")
	f.repo.GitIn(worktree, "commit", "-m", "feat: choose ff")

	if err := runDoneForTest(f, "\ny\n", true, "choose-ff"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(ctxOf(), f.repo.Root, "cat-file", "-e", "main:choice.txt"); err != nil {
		t.Fatalf("interactive FF did not integrate: %v", err)
	}
	if !strings.Contains(f.stdout.String(), "Integration (f=fast-forward") {
		t.Fatalf("integration prompt missing:\n%s", f.stdout.String())
	}
}

func TestDoneBareNonInteractiveReportsDirtyRelation(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "report-dirty")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("unique\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDoneForTest(f, "", false, "report-dirty"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"already equal to main", "1 unique", "Nothing done"} {
		if !strings.Contains(f.stdout.String(), want) {
			t.Errorf("report missing %q:\n%s", want, f.stdout.String())
		}
	}
}

type mutateOnRead struct {
	reader io.Reader
	once   bool
	mutate func()
}

func (r *mutateOnRead) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if !r.once {
		r.once = true
		r.mutate()
	}
	return n, err
}

func TestDoneInteractiveDetectsWriterRaceBeforeDiscard(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "writer-race")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.app.In = &mutateOnRead{
		reader: strings.NewReader("d\nDROP\n"),
		mutate: func() {
			if err := os.WriteFile(filepath.Join(worktree, "late.txt"), []byte("late\n"), 0o644); err != nil {
				t.Errorf("late write: %v", err)
			}
		},
	}
	f.app.interactiveCheck = func() bool { return true }
	cmd := newDoneCmd(f.app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"writer-race"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "changed while the finish plan was open") {
		t.Fatalf("writer race error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "scratch.txt")); err != nil {
		t.Fatalf("race detection discarded original content: %v", err)
	}
}

func TestDoneRejectsInvalidDirtyPolicy(t *testing.T) {
	f, _ := mergedTaskFixture(t, "invalid-policy")
	err := runDoneForTest(f, "", false, "invalid-policy", "--ff", "--dirty=magic")
	if err == nil || !strings.Contains(err.Error(), "want auto, fail, commit or discard") {
		t.Fatalf("invalid policy error = %v", err)
	}
}

func TestDoneConflictedCheckoutRefusesEveryDirtyPolicy(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	f.repo.Commit("conflict.txt", "base\n", "chore: add conflict fixture")
	if err := f.run("--task", "conflicted", "--branch", "feat/conflicted", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(f.app.Cfg.Paths.WorktreeRoot, "repo", "feat-conflicted")
	if err := os.WriteFile(filepath.Join(worktree, "conflict.txt"), []byte("branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(worktree, "add", "conflict.txt")
	f.repo.GitIn(worktree, "commit", "-m", "feat: conflict branch")
	f.repo.Commit("conflict.txt", "main\n", "feat: conflict main")
	if _, err := gitx.Run(ctxOf(), worktree, "rebase", "main"); err == nil {
		t.Fatal("expected rebase conflict")
	}

	err := runDoneForTest(f, "", false, "conflicted", "--ff", "--dirty=discard", "--yes")
	if err == nil || !strings.Contains(err.Error(), "conflicted path") {
		t.Fatalf("conflicted checkout error = %v", err)
	}
	status, statusErr := gitx.StatusOf(ctxOf(), worktree)
	if statusErr != nil || status.Conflicted == 0 {
		t.Fatalf("done changed conflict state: %+v, %v", status, statusErr)
	}
}

func TestDoneCommitHookFailureKeepsTaskAndWorktree(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "hook-failure")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(worktree, "config", "core.hooksPath", hooks)

	err := runDoneForTest(f, "", false, "hook-failure", "--ff", "--dirty=commit", "--message", "chore: blocked")
	if err == nil || !strings.Contains(err.Error(), "commit dirty checkout") {
		t.Fatalf("hook failure error = %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("hook failure removed worktree: %v", err)
	}
	stored, err := f.app.Tasks.Get(task.MakeID("repo", "feat/hook-failure"))
	if err != nil || stored.State != task.Hot {
		t.Fatalf("task after hook failure = %+v, %v", stored, err)
	}
}

func TestDoneDetectsHookCreatedDirtyFileAfterCommit(t *testing.T) {
	f, worktree := mergedTaskFixture(t, "hook-writer")
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\nprintf late > late-after-hook.txt\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	f.repo.GitIn(worktree, "config", "core.hooksPath", hooks)

	err := runDoneForTest(f, "", false, "hook-writer", "--ff", "--dirty=commit", "--message", "chore: hook writer")
	if err == nil || !strings.Contains(err.Error(), "changed again during finalization") {
		t.Fatalf("post-commit writer error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "late-after-hook.txt")); err != nil {
		t.Fatalf("hook output missing: %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("writer race removed worktree: %v", err)
	}
}

func TestDoneInteractiveDirectTaskCommitsAndCompletes(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	if err := f.run("--task", "direct-finish", "--direct"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo.Root, "direct.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDoneForTest(f, "c\n\ny\n", true, "direct-finish"); err != nil {
		t.Fatal(err)
	}
	if got := f.repo.Git("show", "HEAD:direct.txt"); got != "done" {
		t.Fatalf("direct commit content = %q", got)
	}
	stored, err := f.app.Tasks.Get(task.MakeID("repo", "main"))
	if err != nil || stored.State != task.Done {
		t.Fatalf("direct task = %+v, %v", stored, err)
	}
}

func TestDoneInteractiveBranchOnlyUsesSameFinishFlow(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	if err := f.run("--task", "branch-finish", "--branch-only", "--branch", "feat/branch-finish", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo.Root, "branch.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDoneForTest(f, "c\n\n\ny\n", true, "branch-finish"); err != nil {
		t.Fatal(err)
	}
	if got := f.repo.Git("branch", "--show-current"); got != "main" {
		t.Fatalf("canonical checkout branch = %q", got)
	}
	if got := f.repo.Git("show", "main:branch.txt"); got != "done" {
		t.Fatalf("branch-only commit content = %q", got)
	}
}
