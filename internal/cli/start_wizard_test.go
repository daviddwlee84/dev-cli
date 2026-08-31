package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (f *startFixture) runInteractive(input string, args ...string) error {
	f.t.Helper()
	f.app.In = strings.NewReader(input)
	f.app.interactiveCheck = func() bool { return true }
	return f.run(args...)
}

func TestStartWizardAcceptsContextDefaults(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	input := "\nwizard task\n\n\n\nrun focused tests\n\n"
	if err := f.runInteractive(input); err != nil {
		t.Fatal(err)
	}

	out := f.stdout.String()
	for _, want := range []string{
		"Start a tracked change stream", "Summary", "mode        worktree",
		"branch      feat/wizard-task", "Create this task? [Y/n]", "🔥 wizard task",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output missing %q:\n%s", want, out)
		}
	}

	saved, err := f.app.Tasks.Get(task.MakeID("repo", "feat/wizard-task"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Next != "run focused tests" || saved.EffectiveMode() != task.ModeWorktree {
		t.Fatalf("saved task = %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(saved.WorktreePath, "README.md")); err != nil {
		t.Fatalf("wizard did not create the worktree: %v", err)
	}
}

func TestStartWizardConfirmsRunWithoutEchoingCommand(t *testing.T) {
	rt := exactStartRuntime()
	f := newStartFixture(t, rt)
	command := "launch --token secret-value"
	input := "\nwizard command\n\n\n\n\n\n"
	if err := f.runInteractive(input, "--run", command); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdout.String(), "configured for the new Herdr root pane") || strings.Contains(f.stdout.String(), command) {
		t.Fatalf("wizard run summary = %q", f.stdout.String())
	}
	if len(rt.runCalls) != 1 {
		t.Fatalf("wizard run calls = %v", rt.runCalls)
	}
}

func TestStartWizardCanCustomizeCoreLifecycle(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	input := "\nsmall fix\nb\nfix/small\nmain\nship the fix\n\n"
	if err := f.runInteractive(input); err != nil {
		t.Fatal(err)
	}

	saved, err := f.app.Tasks.Get(task.MakeID("repo", "fix/small"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.EffectiveMode() != task.ModeBranch || saved.WorktreePath != "" || saved.Next != "ship the fix" {
		t.Fatalf("customized task = %+v", saved)
	}
	if got := f.repo.Git("branch", "--show-current"); got != "fix/small" {
		t.Fatalf("current branch = %q", got)
	}
}

func TestStartWizardDeclineHasNoSideEffects(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	input := "\ncanceled work\n\n\n\n\nn\n"
	if err := f.runInteractive(input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdout.String(), "Canceled; nothing was created.") {
		t.Fatalf("cancel output:\n%s", f.stdout.String())
	}
	if tasks, err := f.app.Tasks.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after cancel = %v, %v", tasks, err)
	}
	if gitx.BranchExists(context.Background(), f.repo.Root, "feat/canceled-work") {
		t.Fatal("cancel created a branch")
	}
	path, err := f.app.Cfg.WorktreePathFor("repo", f.repo.Root, "feat/canceled-work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancel created checkout %s: %v", path, err)
	}
}

func TestStartWizardRepromptsInvalidBranchBeforeMutation(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	input := "\nvalidate input\n\nbad branch\nmain\n\n\nfeat/valid-input\nmain\n\n\n"
	if err := f.runInteractive(input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdout.String(), "invalid branch") {
		t.Fatalf("invalid branch was not reported:\n%s", f.stdout.String())
	}
	if _, err := f.app.Tasks.Get(task.MakeID("repo", "feat/valid-input")); err != nil {
		t.Fatal(err)
	}
	if gitx.BranchExists(context.Background(), f.repo.Root, "bad branch") {
		t.Fatal("invalid branch changed Git state")
	}
}

func TestStartWizardSelectsRepositoryOutsideCheckout(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	f.app.Cfg.Paths.ScanRoots = []string{filepath.Dir(f.repo.Root)}
	outside := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(f.repo.Root); err != nil {
			t.Errorf("restore fixture cwd: %v", err)
		}
	}()

	input := "\noutside task\n\n\n\n\n\n"
	if err := f.runInteractive(input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdout.String(), "Repositories:") || !strings.Contains(f.stdout.String(), f.repo.Root) {
		t.Fatalf("repo selector output:\n%s", f.stdout.String())
	}
	if _, err := f.app.Tasks.Get(task.MakeID("repo", "feat/outside-task")); err != nil {
		t.Fatal(err)
	}
}

func TestStartJSONNeverPrompts(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	f.app.In = strings.NewReader("would become a task\n")
	f.app.interactiveCheck = func() bool { return true }
	if err := f.run("--json"); err == nil || !strings.Contains(err.Error(), "give the work a name") {
		t.Fatalf("missing JSON task error = %v", err)
	}
	if f.stdout.Len() != 0 {
		t.Fatalf("JSON path emitted prompt/output: %q", f.stdout.String())
	}
}

func TestStartMissingTaskOutsideTTYStillErrors(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	f.app.In = strings.NewReader("ignored\n")
	f.app.interactiveCheck = func() bool { return false }
	if err := f.run(); err == nil || !strings.Contains(err.Error(), "give the work a name") {
		t.Fatalf("non-TTY missing task error = %v", err)
	}
	if f.stdout.Len() != 0 {
		t.Fatalf("non-TTY path emitted prompt/output: %q", f.stdout.String())
	}
}

func TestStartExplicitTaskRemainsImmediateInTTY(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	f.app.In = strings.NewReader("must not be consumed\n")
	f.app.interactiveCheck = func() bool { return true }
	if err := f.run("--task", "fast path", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.stdout.String(), "Start a tracked change stream") {
		t.Fatalf("explicit task unexpectedly prompted:\n%s", f.stdout.String())
	}
	if _, err := f.app.Tasks.Get(task.MakeID("repo", "feat/fast-path")); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrExternalWorktreeRequiresExplicitAdopt(t *testing.T) {
	r := gittest.New(t)
	external := filepath.Join(t.TempDir(), ".herdr", "worktrees", "repo", "external")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("branch", "feat/external")
	r.Git("worktree", "add", external, "feat/external")
	externalCanonical, err := filepath.EvalSymlinks(external)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{filepath.Dir(r.Root)}
	cfg.Paths.StateDir = t.TempDir()
	rt := &activityRuntime{sessions: []runtime.Session{{Handle: "w-external", Dirs: []string{externalCanonical}}}}
	var out, errOut bytes.Buffer
	app := &App{
		Cfg: cfg, Tasks: task.NewStore(cfg.TasksDir()), In: strings.NewReader(""),
		Out: &out, Err: &errOut, runtimeInstance: rt,
	}

	cmd := newAdoptCmd(app)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "feat/external") || !strings.Contains(out.String(), external) {
		t.Fatalf("external worktree was not detected:\n%s", out.String())
	}
	if tasks, err := app.Tasks.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("report-only adopt persisted %v, %v", tasks, err)
	}

	out.Reset()
	cmd = newAdoptCmd(app)
	cmd.SetArgs([]string{"--apply", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	saved, err := app.Tasks.Get(task.MakeID(filepath.Base(r.Root), "feat/external"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != task.Hot || saved.WorktreePath != externalCanonical {
		t.Fatalf("adopted external task = %+v", saved)
	}
}
