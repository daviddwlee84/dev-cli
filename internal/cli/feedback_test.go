package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func feedbackCLIFixture(t *testing.T) (*sshCLIFixture, *gittest.Repo, string) {
	t.Helper()
	f := newSSHCLIFixture(t)
	repo := gittest.New(t)
	repo.Git("remote", "add", "origin", "https://github.com/daviddwlee84/dev-cli.git")
	raw := fmt.Sprintf("[runtime]\nbackend = \"none\"\n[paths]\nstate_dir = %q\nworktree_path = %q\n[feedback]\nsource_repo = %q\n", filepath.Join(f.home, "state"), filepath.Join(f.home, "worktrees", "{{repo}}", "{{branch}}"), repo.Root)
	if err := os.WriteFile(f.configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(f.home, "report.md")
	if err := os.WriteFile(body, []byte("## Reproduction\n\ndev ssh\n\nExpected menu; actual panic."), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := f.run("feedback", "draft", "--title", "SSH panic", "--body-file", body, "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var draft feedback.DraftResult
	if json.Unmarshal([]byte(out), &draft) != nil || draft.Report.ID == "" {
		t.Fatal(out)
	}
	return f, repo, draft.Report.ID
}
func TestFeedbackCLIRepairPreservesDirtySourceAndPinsBase(t *testing.T) {
	f, repo, id := feedbackCLIFixture(t)
	repo.Write("user-untracked.txt", "keep this work")
	before := repo.Git("status", "--porcelain=v1", "-uall")
	base := repo.Git("rev-parse", "HEAD")
	out, _, err := f.run("feedback", "repair", id, "--base", "main", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var plan feedback.RepairPlan
	if json.Unmarshal([]byte(out), &plan) != nil || plan.ID == "" {
		t.Fatal(out)
	}
	if plan.Snapshot.BaseOID != strings.TrimSpace(base) {
		t.Fatal("wrong explicit base", out)
	}
	if _, err := os.Stat(plan.Snapshot.Checkout); !os.IsNotExist(err) {
		t.Fatal("preview created checkout")
	}
	out, _, err = f.run("feedback", "repair", id, "--apply", "--plan", plan.ID, "--yes", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var result feedback.RepairResult
	if json.Unmarshal([]byte(out), &result) != nil || result.Status != "prepared" || result.Binding == nil {
		t.Fatal(out)
	}
	if got := repo.Git("status", "--porcelain=v1", "-uall"); got != before {
		t.Fatalf("source changed: %q -> %q", before, got)
	}
	if got := strings.TrimSpace(repo.GitIn(result.Binding.Snapshot.Checkout, "rev-parse", "HEAD")); got != strings.TrimSpace(base) {
		t.Fatal("worktree did not use pinned commit")
	}
	cfg, err := config.Load(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	record, err := task.NewStore(cfg.TasksDir()).Get(plan.Snapshot.TaskID)
	if err != nil || record.Mode != task.ModeWorktree || record.State != task.Hot {
		t.Fatal(record, err)
	}
	if f.runner.callCount() != 0 {
		t.Fatal("repair ran an SSH/provider command")
	}
	out, _, err = f.run("prompt", "render", "feedback-fix", id)
	if err != nil || !strings.Contains(out, result.Binding.Snapshot.Checkout) || !strings.Contains(out, "private local context") {
		t.Fatal(err, out)
	}
	if _, _, err = f.run("prompt", "open", "feedback-fix", id, "--dry-run"); err == nil || !strings.Contains(err.Error(), "explicit --agent") {
		t.Fatal("agent profile was selected implicitly", err)
	}
	repo.GitIn(result.Binding.Snapshot.Checkout, "switch", "-c", "another-task")
	if _, _, err = f.run("prompt", "render", "feedback-fix", id); err == nil {
		t.Fatal("changed checkout binding accepted")
	}
}
func TestFeedbackCLIStaleBaseDoesNotCreateWorktree(t *testing.T) {
	f, repo, id := feedbackCLIFixture(t)
	out, _, err := f.run("feedback", "repair", id, "--base", "main", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var plan feedback.RepairPlan
	if json.Unmarshal([]byte(out), &plan) != nil {
		t.Fatal(out)
	}
	repo.Commit("later.txt", "later change", "advance base")
	out, _, err = f.run("feedback", "repair", id, "--apply", "--plan", plan.ID, "--yes", "--json")
	if err == nil || !strings.Contains(out, "stale_plan") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(plan.Snapshot.Checkout); !os.IsNotExist(err) {
		t.Fatal("stale plan created worktree")
	}
}
func TestFeedbackCLIDraftWorksWithBrokenConfigAndNoNetwork(t *testing.T) {
	f := newSSHCLIFixture(t)
	if err := os.WriteFile(f.configPath, []byte("invalid = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := f.runContext(context.Background(), "Expected menu; actual panic", false, "feedback", "draft", "--title", "Bug", "--body-file", "-", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	var draft feedback.DraftResult
	if json.Unmarshal([]byte(out), &draft) != nil || draft.Report.ID == "" {
		t.Fatal(out)
	}
	if f.runner.callCount() != 0 {
		t.Fatal("draft contacted provider")
	}
}
func TestFeedbackCLIUnapprovedPublicationAndSourceSelection(t *testing.T) {
	f, _, id := feedbackCLIFixture(t)
	for _, args := range [][]string{{"feedback", "issue", id, "--publish", "--json"}, {"feedback", "issue", id, "--publish", "--yes", "--json"}, {"feedback", "issue", id, "--search", "--publish"}, {"feedback", "repair", id, "--apply", "--yes"}} {
		if _, _, err := f.run(args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	out, _, err := f.run("feedback", "issue", id, "--json")
	if err != nil || !strings.Contains(out, "feedback_issue_preview") {
		t.Fatal(out, err)
	}
	out, _, err = f.run("feedback", "repair", id, "--json")
	if err != nil || !strings.Contains(out, "choose_base") {
		t.Fatal("base was inferred", out, err)
	}
	if !passiveCommandSkipsNudge(newFeedbackCmd(&App{})) {
		t.Fatal("feedback could trigger unrelated release lookup")
	}
}

func TestFeedbackCLIRejectsTaskClaimAfterPreview(t *testing.T) {
	f, _, id := feedbackCLIFixture(t)
	out, _, err := f.run("feedback", "repair", id, "--base", "main", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var plan feedback.RepairPlan
	if json.Unmarshal([]byte(out), &plan) != nil {
		t.Fatal(out)
	}
	cfg, err := config.Load(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	existing := &task.Task{Name: "Unrelated completed record", Repo: filepath.Base(plan.Snapshot.Source.Path), RepoPath: plan.Snapshot.Source.Path, Branch: plan.Snapshot.Branch, Base: "main", Mode: task.ModeWorktree, State: task.Done, Owner: config.Hostname()}
	if err = task.NewStore(cfg.TasksDir()).Save(existing); err != nil {
		t.Fatal(err)
	}
	out, _, err = f.run("feedback", "repair", id, "--apply", "--plan", plan.ID, "--yes", "--json")
	if err == nil || !strings.Contains(out, "stale_plan") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(plan.Snapshot.Checkout); !os.IsNotExist(err) {
		t.Fatal("occupied task caused checkout creation")
	}
	retained, err := task.NewStore(cfg.TasksDir()).Get(existing.ID)
	if err != nil || retained.Name != existing.Name {
		t.Fatal("existing task was replaced", err)
	}
}

func TestFeedbackPromptSkipsUnrelatedReleaseLookup(t *testing.T) {
	root := newRootCommand(&App{})
	cmd, _, err := root.Find([]string{"prompt", "render", "feedback-fix"})
	if err != nil || !passiveCommandSkipsNudge(cmd) {
		t.Fatal("feedback render can trigger an unrelated network lookup", err)
	}
}
