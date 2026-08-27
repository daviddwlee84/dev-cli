package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/cli"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

// harness runs dev commands against an isolated HOME, config and scan root.
type harness struct {
	t          *testing.T
	home       string
	configPath string
	scanRoot   string
	wtRoot     string
	repo       *gittest.Repo
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	scanRoot := filepath.Join(home, "Program")
	wtRoot := filepath.Join(home, "Worktrees")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// A repo inside the scan root, so `dev` discovers it the way it would in
	// real use rather than via an absolute path.
	r := gittest.New(t)
	dest := filepath.Join(scanRoot, "demo")
	if err := os.Rename(r.Root, dest); err != nil {
		t.Fatal(err)
	}
	r.Root = dest

	// Isolate every path dev touches from the developer's real machine.
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	configPath := filepath.Join(home, "config.toml")
	cfg := "" +
		"[paths]\n" +
		"scan_roots = [\"" + scanRoot + "\"]\n" +
		"project_root = \"" + scanRoot + "\"\n" +
		"tries_root = \"" + filepath.Join(home, "tries") + "\"\n" +
		"worktree_root = \"" + wtRoot + "\"\n" +
		"state_dir = \"" + filepath.Join(home, "state") + "\"\n" +
		"\n[runtime]\nbackend = \"none\"\n" +
		"\n[worktree]\ninclude = [\".env\"]\npost_create = []\n"
	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, home: home, configPath: configPath, scanRoot: scanRoot, wtRoot: wtRoot, repo: r}
}

// run executes one dev invocation, capturing its output. Commands write
// through App, so the writers passed to NewRootCommandWithIO are all that is
// needed — no file-descriptor juggling.
func (h *harness) run(args ...string) (string, string, error) {
	h.t.Helper()
	var out, errBuf bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, &errBuf)
	root.SetArgs(append([]string{"--config", h.configPath}, args...))
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	out, errOut, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("dev %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut)
	}
	return out
}

func TestLifecycleEndToEnd(t *testing.T) {
	h := newHarness(t)

	// An empty inventory should tell the user how to start, not print nothing.
	out := h.mustRun("ls")
	if !strings.Contains(out, "No tasks yet") {
		t.Errorf("empty ls output: %q", out)
	}

	// start: branch + worktree + task entry.
	out = h.mustRun("start", "demo", "--task", "auth", "--branch", "feat/auth", "--base", "main")
	if !strings.Contains(out, "feat/auth") {
		t.Fatalf("start output: %q", out)
	}
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-auth")
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatalf("worktree should exist at the templated path: %v", err)
	}

	// The task appears in the inventory as hot.
	out = h.mustRun("ls")
	if !strings.Contains(out, "auth") || !strings.Contains(out, "HOT") {
		t.Errorf("ls after start: %q", out)
	}

	// JSON is a stable contract for other tools.
	out = h.mustRun("ls", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("ls --json is not valid JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0]["branch"] != "feat/auth" || rows[0]["state"] != "hot" {
		t.Errorf("json row: %+v", rows[0])
	}

	// park: records the next action, closes the session, keeps the worktree.
	out = h.mustRun("park", "feat/auth", "--next", "add the regression test")
	if !strings.Contains(out, "WARM") {
		t.Errorf("park output: %q", out)
	}
	out = h.mustRun("ls")
	if !strings.Contains(out, "WARM") || !strings.Contains(out, "add the regression test") {
		t.Errorf("ls after park: %q", out)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Error("a warm task keeps its worktree")
	}

	// resume: back to hot.
	out = h.mustRun("resume", "auth")
	if !strings.Contains(out, "feat/auth") {
		t.Errorf("resume output: %q", out)
	}
	out = h.mustRun("ls")
	if !strings.Contains(out, "HOT") {
		t.Errorf("ls after resume: %q", out)
	}

	// Commit something on the branch so there is work to integrate.
	h.repo.GitIn(wtPath, "config", "user.email", "dev@example.test")
	h.repo.GitIn(wtPath, "config", "user.name", "dev test")
	os.WriteFile(filepath.Join(wtPath, "auth.go"), []byte("package auth\n"), 0o644)
	h.repo.GitIn(wtPath, "add", "auth.go")
	h.repo.GitIn(wtPath, "commit", "-m", "feat: add auth")

	// done --ff: rebase and fast-forward, then clean up.
	out = h.mustRun("done", "auth", "--ff")
	if !strings.Contains(out, "merged into main") {
		t.Fatalf("done output: %q", out)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("done should remove the worktree")
	}
	// The commit is on main, and the history stayed linear.
	if _, err := gitx.Run(gittest.Ctx(), h.repo.Root, "cat-file", "-e", "main:auth.go"); err != nil {
		t.Error("the branch's commit should be on main")
	}
	merges, _ := gitx.Run(gittest.Ctx(), h.repo.Root, "log", "--merges", "--oneline")
	if merges != "" {
		t.Errorf("--ff must not create a merge commit, got:\n%s", merges)
	}
	// The branch survives by default: merged is not always finished.
	if !gitx.BranchExists(gittest.Ctx(), h.repo.Root, "feat/auth") {
		t.Error("the branch should survive without --delete-branch")
	}

	// sweep reaps the done entry.
	out = h.mustRun("sweep")
	if !strings.Contains(out, "reap") {
		t.Errorf("sweep should offer to reap a done task: %q", out)
	}
}

func TestStartRefusesDuplicate(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "dup", "--branch", "feat/dup", "--base", "main")

	_, _, err := h.run("start", "demo", "--task", "dup", "--branch", "feat/dup", "--base", "main")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a second start on the same branch should be refused, got %v", err)
	}
}

func TestParkColdRefusesUnpushedWork(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "cold", "--branch", "feat/cold", "--base", "main")

	// No remote, so the branch cannot possibly be reconstructible.
	_, _, err := h.run("park", "feat/cold", "--cold")
	if err == nil || !strings.Contains(err.Error(), "not fully pushed") {
		t.Errorf("going cold without a push must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.wtRoot, "demo", "feat-cold")); err != nil {
		t.Error("the worktree must survive a refused cold transition")
	}
}

func TestDoneRefusesDirtyTree(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "dirty", "--branch", "feat/dirty", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-dirty")
	os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("wip\n"), 0o644)

	_, _, err := h.run("done", "feat/dirty", "--ff")
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("done on a dirty tree must be refused, got %v", err)
	}
}

func TestDoneWithoutModeOnlyReports(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "report", "--branch", "feat/report", "--base", "main")

	out := h.mustRun("done", "feat/report")
	if !strings.Contains(out, "Nothing done") {
		t.Errorf("done with no mode should change nothing: %q", out)
	}
	if _, err := os.Stat(filepath.Join(h.wtRoot, "demo", "feat-report")); err != nil {
		t.Error("the worktree must still be there")
	}
}

func TestWorktreeProvisioningCarriesIgnoredFiles(t *testing.T) {
	h := newHarness(t)
	h.repo.Commit(".gitignore", ".env\n", "chore: ignore env")
	h.repo.Write(".env", "TOKEN=secret\n")

	h.mustRun("start", "demo", "--task", "env", "--branch", "feat/env", "--base", "main")
	got, err := os.ReadFile(filepath.Join(h.wtRoot, "demo", "feat-env", ".env"))
	if err != nil || string(got) != "TOKEN=secret\n" {
		t.Errorf(".env should be carried into the worktree: %q %v", got, err)
	}
}

func TestTryAndGraduate(t *testing.T) {
	h := newHarness(t)

	out := h.mustRun("try", "redis-streams")
	if !strings.Contains(out, "redis-streams") {
		t.Fatalf("try output: %q", out)
	}
	triesRoot := filepath.Join(h.home, "tries")
	entries, _ := os.ReadDir(triesRoot)
	if len(entries) != 1 {
		t.Fatalf("want one try directory, got %d", len(entries))
	}
	tryName := entries[0].Name()
	if !strings.HasSuffix(tryName, "-redis-streams") || len(tryName) < 21 {
		t.Errorf("a try should be date-prefixed, got %q", tryName)
	}

	// A second `try` with the same name reuses the existing directory rather
	// than creating a near-duplicate.
	h.mustRun("try", "redis-streams")
	entries, _ = os.ReadDir(triesRoot)
	if len(entries) != 1 {
		t.Errorf("try should reuse an existing match, got %d directories", len(entries))
	}

	out = h.mustRun("graduate", "redis-streams", "--category", "Infra")
	if !strings.Contains(out, "is now a project") {
		t.Fatalf("graduate output: %q", out)
	}
	dest := filepath.Join(h.scanRoot, "Infra", "redis-streams")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("graduated project should exist at %s: %v", dest, err)
	}
	if _, err := os.Stat(filepath.Join(triesRoot, tryName)); !os.IsNotExist(err) {
		t.Error("the try directory should have been moved, not copied")
	}
	// The date prefix is dropped, and it is a repo with history.
	if _, _, err := gitx.LastCommit(gittest.Ctx(), dest); err != nil {
		t.Errorf("a graduated project should have a commit: %v", err)
	}
}

func TestStatusOutsideRepo(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	out := h.mustRun("status")
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("status outside a repo should say so plainly: %q", out)
	}
}

func TestSkillPrintsAndSyncChecks(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("skill", "print")
	if !strings.HasPrefix(out, "---\nname: dev\n") {
		t.Errorf("the skill must start with valid frontmatter naming it: %q", out[:min(80, len(out))])
	}
	if !strings.Contains(out, "worktree-ownership.md") {
		t.Error("the skill should point at its reference files")
	}
}

func TestShellInit(t *testing.T) {
	h := newHarness(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out := h.mustRun("shell-init", shell)
		if !strings.Contains(out, "DEV_SHELL_INIT") {
			t.Errorf("%s wrapper should export the marker doctor looks for", shell)
		}
		if !strings.Contains(out, "cd ") {
			t.Errorf("%s wrapper should handle the cd directive", shell)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
