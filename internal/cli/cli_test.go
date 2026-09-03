package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/cli"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/skill"
	"github.com/daviddwlee84/dev-cli/internal/stats"
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
	home := externalCLITestHome(t)
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
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	gitConfig := "[user]\n\temail = dev@example.test\n\tname = dev test\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(home, "config.toml")
	// TOML basic strings treat backslash as an escape, so a Windows path like
	// C:\Users\... is invalid there. Forward slashes are valid TOML and Go
	// normalizes them back to the OS separator on use.
	slash := filepath.ToSlash
	cfg := "" +
		"[paths]\n" +
		"scan_roots = [\"" + slash(scanRoot) + "\"]\n" +
		"project_root = \"" + slash(scanRoot) + "\"\n" +
		"tries_root = \"" + slash(filepath.Join(home, "tries")) + "\"\n" +
		"worktree_root = \"" + slash(wtRoot) + "\"\n" +
		"state_dir = \"" + slash(filepath.Join(home, "state")) + "\"\n" +
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
	out = h.mustRun("--allow-shared-checkout", "resume", "auth")
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

	// done --ff integrates but leaves physical cleanup to an external retire.
	out = h.mustRun("--allow-shared-checkout", "done", "auth", "--ff")
	if !strings.Contains(out, "merged into main") || !strings.Contains(out, "cleanup pending") {
		t.Fatalf("done output: %q", out)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("done should keep the worktree: %v", err)
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

	// Sweep reports cleanup pending; retire performs it from outside the target.
	out = h.mustRun("sweep")
	if !strings.Contains(out, "guarded retirement is blocked") || !strings.Contains(out, "cleanup-occupancy") {
		t.Errorf("sweep should fail closed on unobserved runtime cleanup: %q", out)
	}
	out = h.mustRun("retire", "auth", "--assume-no-runtime")
	if !strings.Contains(out, "RETIRED") {
		t.Fatalf("retire output: %q", out)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("retire should remove the worktree")
	}
	if inventory := h.mustRun("ls", "--json"); strings.Contains(inventory, `"name": "auth"`) {
		t.Fatalf("retired task remained in inventory: %s", inventory)
	}
}

func TestFleetListIncludesLocalRepositoryWithoutRemoteConfig(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("fleet", "list", "--json")
	var results []struct {
		State    string `json:"state"`
		Snapshot *struct {
			Repositories []struct {
				Name string `json:"name"`
			} `json:"repositories"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("fleet JSON: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].State != "ok" || results[0].Snapshot == nil ||
		len(results[0].Snapshot.Repositories) != 1 || results[0].Snapshot.Repositories[0].Name != "demo" {
		t.Fatalf("fleet results = %+v", results)
	}
}

func TestFleetConfigInitUsesPrivateMode(t *testing.T) {
	h := newHarness(t)
	h.mustRun("fleet", "config", "init")
	path := filepath.Join(h.home, ".config", "dev", "remotes.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("remotes mode = %o", info.Mode().Perm())
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

func TestParkRejectsColdKeepSession(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("park", "missing", "--cold", "--keep-session")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--cold --keep-session should fail before task lookup, got %v", err)
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

func TestDonePRLeavesTaskAndWorktreeForReview(t *testing.T) {
	h := newHarness(t)
	h.repo.WithRemote()
	h.mustRun("start", "demo", "--task", "review", "--branch", "feat/review", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-review")
	h.repo.GitIn(wtPath, "config", "user.email", "dev@example.test")
	h.repo.GitIn(wtPath, "config", "user.name", "dev test")
	if err := os.WriteFile(filepath.Join(wtPath, "review.txt"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.repo.GitIn(wtPath, "add", "review.txt")
	h.repo.GitIn(wtPath, "commit", "-m", "feat: review")

	out := h.mustRun("--allow-shared-checkout", "done", "review", "--pr")
	if !strings.Contains(out, "READY FOR REVIEW") {
		t.Fatalf("PR path should report review state: %q", out)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("--pr must keep the worktree: %v", err)
	}
	if list := h.mustRun("ls", "--json"); !strings.Contains(list, `"state": "hot"`) {
		t.Fatalf("--pr must keep the task active:\n%s", list)
	}
}

func TestDonePROpensAzureDevOpsPullRequestOffline(t *testing.T) {
	h := newHarness(t)
	pushRemote := h.repo.WithRemote()
	h.repo.Git("remote", "set-url", "origin", "https://dev.azure.com/acme/Platform/_git/demo")
	h.repo.Git("remote", "set-url", "--push", "origin", pushRemote)
	h.mustRun("start", "demo", "--task", "azure-review", "--branch", "feat/azure", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-azure")
	h.repo.GitIn(wtPath, "config", "user.email", "dev@example.test")
	h.repo.GitIn(wtPath, "config", "user.name", "dev test")
	if err := os.WriteFile(filepath.Join(wtPath, "azure.txt"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.repo.GitIn(wtPath, "add", "azure.txt")
	h.repo.GitIn(wtPath, "commit", "-m", "feat: Azure review")

	binDir := t.TempDir()
	script := `#!/bin/sh
set -eu
if [ "$1" = "extension" ]; then
  printf 'azure-devops\n'
  exit 0
fi
if [ "$1" = "repos" ] && [ "$2" = "pr" ] && [ "$3" = "create" ]; then
  printf '%s\n' '{"pullRequestId":73,"remoteUrl":"https://dev.azure.com/acme/Platform/_git/demo"}'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(filepath.Join(binDir, "az"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := h.mustRun("--allow-shared-checkout", "done", "azure-review", "--pr")
	if !strings.Contains(out, "https://dev.azure.com/acme/Platform/_git/demo/pullrequest/73") {
		t.Fatalf("Azure PR output: %q", out)
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

func TestTryArchiveRemainsAPositionalName(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("try", "archive", "--no-git")
	if !strings.Contains(out, "archive") {
		t.Fatalf("try archive output: %q", out)
	}
	triesRoot := filepath.Join(h.home, "tries")
	entries, err := os.ReadDir(triesRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("try archive entries = %d, %v", len(entries), err)
	}
	path := filepath.Join(triesRoot, entries[0].Name())
	if !strings.HasSuffix(entries[0].Name(), "-archive") {
		t.Errorf("archive was not treated as a positional name: %q", entries[0].Name())
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
		t.Errorf("--no-git created Git metadata: %v", err)
	}

	// A second positional invocation selects the same Try rather than becoming a
	// future management subcommand or creating a duplicate.
	h.mustRun("try", "archive", "--no-git")
	entries, err = os.ReadDir(triesRoot)
	if err != nil || len(entries) != 1 {
		t.Errorf("second try archive entries = %d, %v", len(entries), err)
	}
}

func TestTryAmbiguousNameDoesNotSilentlyPickOne(t *testing.T) {
	h := newHarness(t)
	triesRoot := filepath.Join(h.home, "tries")
	for _, name := range []string{"2026-08-20-cache-alpha", "2026-08-21-cache-beta"} {
		if err := os.MkdirAll(filepath.Join(triesRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := h.run("try", "cache")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), "cache-alpha") || !strings.Contains(err.Error(), "cache-beta") {
		t.Errorf("ambiguous try error = %v", err)
	}
	entries, readErr := os.ReadDir(triesRoot)
	if readErr != nil || len(entries) != 2 {
		t.Errorf("ambiguous selection created a duplicate: %d, %v", len(entries), readErr)
	}
}

func TestTryListDoesNotUseDirectoryMTimeAsActivity(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.home, "tries", "legacy-non-git")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(365 * 24 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("try", "--list")
	if !strings.Contains(out, "legacy-non-git") || !strings.Contains(out, "unknown") {
		t.Errorf("Try list invented mtime activity: %q", out)
	}
}

func TestTryCloneUsesLocalRepositoryAndVersionsCollisions(t *testing.T) {
	h := newHarness(t)
	h.mustRun("try", "--clone", h.repo.Root)
	h.mustRun("try", "--clone", h.repo.Root)
	entries, err := os.ReadDir(filepath.Join(h.home, "tries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("clone entries = %d, want 2", len(entries))
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !strings.HasSuffix(names[0], "-demo") || !strings.HasSuffix(names[1], "-demo-2") {
		t.Errorf("clone collision names = %v", names)
	}
}

func TestGraduateRemoteRefreshesCatalogOrigin(t *testing.T) {
	h := newHarness(t)
	h.mustRun("try", "remote-origin", "--no-git")

	binDir := t.TempDir()
	gh := filepath.Join(binDir, "gh")
	script := `#!/bin/sh
set -eu
if [ "$1" != "repo" ] || [ "$2" != "create" ]; then
  exit 2
fi
git remote add origin "git@github.com:owner/$3.git"
printf 'https://github.com/owner/%s\n' "$3"
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := h.mustRun("graduate", "remote-origin", "--remote", "--push=false")
	if !strings.Contains(out, "https://github.com/owner/remote-origin") {
		t.Fatalf("graduate remote output: %q", out)
	}

	assets, err := filepath.Glob(filepath.Join(h.home, "state", "assets", "*.toml"))
	if err != nil || len(assets) != 1 {
		t.Fatalf("catalog assets = %d, %v", len(assets), err)
	}
	body, err := os.ReadFile(assets[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "git@github.com:owner/remote-origin.git") ||
		!strings.Contains(string(body), "github.com/owner/remote-origin") {
		t.Errorf("graduated catalog did not retain remote origin:\n%s", body)
	}
}

func TestGraduateRefreshesOriginAfterPartialRemoteFailure(t *testing.T) {
	h := newHarness(t)
	h.mustRun("try", "partial-remote", "--no-git")

	binDir := t.TempDir()
	gh := filepath.Join(binDir, "gh")
	script := `#!/bin/sh
set -eu
if [ "$1" != "repo" ] || [ "$2" != "create" ]; then
  exit 2
fi
git remote add origin "git@github.com:owner/$3.git"
printf 'push failed\n' >&2
exit 1
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, errOut, err := h.run("graduate", "partial-remote", "--remote")
	if err != nil {
		t.Fatalf("partial remote failure should remain nonfatal: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "could not create the remote") {
		t.Errorf("partial remote warning = %q", errOut)
	}

	assets, err := filepath.Glob(filepath.Join(h.home, "state", "assets", "*.toml"))
	if err != nil || len(assets) != 1 {
		t.Fatalf("catalog assets = %d, %v", len(assets), err)
	}
	body, err := os.ReadFile(assets[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "git@github.com:owner/partial-remote.git") ||
		!strings.Contains(string(body), "github.com/owner/partial-remote") {
		t.Errorf("partial remote origin was not retained:\n%s", body)
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
	if !strings.HasPrefix(out, "---\nname: "+skill.Name+"\n") {
		t.Errorf("the skill must start with valid frontmatter naming it: %q", out[:min(80, len(out))])
	}
	if !strings.Contains(out, "worktree-ownership.md") {
		t.Error("the skill should point at its reference files")
	}
	if !strings.Contains(out, "`--allow-shared-checkout`") {
		t.Error("the generated command reference should include root persistent options")
	}
}

func TestSkillInventoryAddAndUpdate(t *testing.T) {
	h := newHarness(t)
	projectRoot, _ := filepath.EvalSymlinks(h.repo.Root)
	projectSkills := filepath.Join(h.repo.Root, ".agents", "skills", "shared")
	globalSkills := filepath.Join(h.home, ".agents", "skills", "shared")
	for _, dir := range []string{projectSkills, globalSkills} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: shared\ndescription: CLI fixture\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.repo.Root, "skills-lock.json"), []byte(`{
  "version": 1,
  "skills": {
    "shared": {
      "source": "owner/repo",
      "sourceType": "github",
      "skillPath": "skills/shared/SKILL.md",
      "computedHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    }
  }
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.home, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.home, ".agents", ".skill-lock.json"), []byte(`{
  "version": 3,
  "skills": {
    "shared": {
      "source": "owner/repo/skills",
      "sourceType": "github",
      "sourceUrl": "https://github.com/owner/repo.git",
      "skillPath": "skills/shared/SKILL.md",
      "skillFolderHash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  }
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo 1.5.23
  exit 0
fi
if [ "$1" = "list" ]; then
  scope=project
  path="$PWD/.agents/skills/shared"
  for arg in "$@"; do
    if [ "$arg" = "--global" ]; then
      scope=global
      path="$HOME/.agents/skills/shared"
    fi
  done
  printf '[{"name":"shared","path":"%s","scope":"%s","agents":["Claude Code","Codex"],"source":"owner/repo","sourceUrl":null,"sourceType":"github"}]\n' "$path" "$scope"
  exit 0
fi
printf '%s|%s\n' "$PWD" "$*"
`
	if err := os.WriteFile(filepath.Join(bin, "skills"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	nested := filepath.Join(h.repo.Root, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	var rows []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun("skill", "list", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["scope"] != "project" || rows[1]["scope"] != "global" {
		t.Fatalf("skill rows = %+v", rows)
	}
	if rows[0]["scope_root"] != h.repo.Root || rows[0]["managed_by"] != "skills" {
		t.Errorf("project row = %+v", rows[0])
	}
	table := h.mustRun("skill", "list", "--project")
	if !strings.Contains(table, "project root") || !strings.Contains(table, "shared") || strings.Contains(table, "global") {
		t.Errorf("project table = %q", table)
	}

	add := h.mustRun("skill", "add")
	if !strings.Contains(add, projectRoot+"|add daviddwlee84/agent-skills/skills") {
		t.Errorf("add shortcut = %q", add)
	}
	if _, _, err := h.run("skill", "update", "shared", "--yes"); err == nil || !strings.Contains(err.Error(), "choose exactly one") {
		t.Fatalf("unscoped update err = %v", err)
	}
	update := h.mustRun("skill", "update", "shared", "--global", "--yes")
	if !strings.Contains(update, "update --yes --global shared") {
		t.Errorf("update = %q", update)
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

func TestRawOutputsNeverContainColor(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"--color=always", "ls", "--json"},
		{"--color=always", "shell-init", "zsh"},
		{"--color=always", "skill", "print"},
		{"--color=always", "completion", "zsh"},
	} {
		out, errOut, err := h.run(args...)
		if err != nil {
			t.Fatalf("dev %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut)
		}
		if strings.Contains(out, "\x1b[") {
			t.Errorf("dev %s emitted ANSI in raw output", strings.Join(args, " "))
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestGitignoreWritesAndIsIdempotent(t *testing.T) {
	h := newHarness(t)
	cwd, _ := os.Getwd()
	os.Chdir(h.repo.Root)
	defer os.Chdir(cwd)

	// Offline, so the test never depends on GitHub being reachable.
	out := h.mustRun("gitignore", "go", "--offline")
	if !strings.Contains(out, "wrote") && !strings.Contains(out, "appended") {
		t.Fatalf("gitignore output: %q", out)
	}
	body, err := os.ReadFile(filepath.Join(h.repo.Root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"*.exe", ".claude/worktrees/", ".specstory/statistics.json", ".env"} {
		if !strings.Contains(string(body), want) {
			t.Errorf(".gitignore missing %q", want)
		}
	}

	out = h.mustRun("gitignore", "go", "--offline")
	if !strings.Contains(out, "already up to date") {
		t.Errorf("re-running should be a no-op, got %q", out)
	}
}

func TestGitignorePreservesHandWrittenRules(t *testing.T) {
	h := newHarness(t)
	cwd, _ := os.Getwd()
	os.Chdir(h.repo.Root)
	defer os.Chdir(cwd)

	path := filepath.Join(h.repo.Root, ".gitignore")
	os.WriteFile(path, []byte("# mine\nproject-specific/\n"), 0o644)

	h.mustRun("gitignore", "go", "--offline")
	h.mustRun("gitignore", "python", "--offline")

	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "project-specific/") {
		t.Error("hand-written rules must survive regeneration")
	}
	if strings.Count(string(body), "# >>> dev gitignore >>>") != 1 {
		t.Errorf("markers duplicated:\n%s", body)
	}
}

func TestGitignoreDetectsLanguage(t *testing.T) {
	h := newHarness(t)
	h.repo.Commit("go.mod", "module demo\n", "chore: add go.mod")
	cwd, _ := os.Getwd()
	os.Chdir(h.repo.Root)
	defer os.Chdir(cwd)

	out := h.mustRun("gitignore", "--offline", "--stdout")
	if !strings.Contains(out, "*.exe") {
		t.Errorf("go.mod should have been detected:\n%s", out)
	}
}

func TestAdoptReportsThenApplies(t *testing.T) {
	h := newHarness(t)

	// A branch ahead of main, which is exactly the unfinished work adopt is for.
	h.repo.Branch("feat/in-flight")
	h.repo.Commit("wip.txt", "half done\n", "feat: in flight")
	h.repo.Git("switch", "main")

	out := h.mustRun("adopt")
	if !strings.Contains(out, "feat/in-flight") {
		t.Fatalf("adopt should find the unmerged branch:\n%s", out)
	}
	if !strings.Contains(out, "--apply") {
		t.Error("reporting run should say how to act on it")
	}
	// Reporting must not have created anything.
	if ls := h.mustRun("ls"); !strings.Contains(ls, "No tasks yet") {
		t.Errorf("adopt without --apply must create nothing, got:\n%s", ls)
	}

	h.mustRun("adopt", "--apply", "--yes")
	ls := h.mustRun("ls")
	if !strings.Contains(ls, "feat/in-flight") {
		t.Errorf("adopted task should appear in the inventory:\n%s", ls)
	}

	// Adopting twice must not duplicate.
	out = h.mustRun("adopt")
	if strings.Contains(out, "feat/in-flight") {
		t.Errorf("an already-tracked branch should not be offered again:\n%s", out)
	}
}

func TestAdoptSkipsMergedAndEphemeralBranches(t *testing.T) {
	h := newHarness(t)
	// Merged into main: the work has landed, so it is not in flight.
	h.repo.Branch("feat/landed")
	h.repo.Git("switch", "main")
	// A harness's own worktree branch, which the harness cleans up itself.
	h.repo.Git("branch", "worktree-ephemeral")

	out := h.mustRun("adopt")
	if strings.Contains(out, "feat/landed") {
		t.Error("a branch contained in main is not work in flight")
	}
	if strings.Contains(out, "worktree-ephemeral") {
		t.Error("harness worktree branches should not be adopted")
	}
}

func TestConfigInitDetectsAndRefusesOverwrite(t *testing.T) {
	h := newHarness(t)

	out := h.mustRun("config", "init", "--stdout")
	if !strings.Contains(out, "[paths]") || !strings.Contains(out, "worktree_path") ||
		!strings.Contains(out, "[picker]") || !strings.Contains(out, "[bootstrap]") ||
		!strings.Contains(out, "[[tui.tools]]") {
		t.Errorf("generated config looks wrong:\n%s", out)
	}
	preview := filepath.Join(h.home, "preview.toml")
	if err := os.WriteFile(preview, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Load(preview)
	if err != nil {
		t.Fatalf("the generated template must parse as its own config: %v", err)
	}
	if len(parsed.EffectiveTools()) != 4 || parsed.Bootstrap.Layout != "flat" ||
		parsed.Forge.CacheTTL.Duration == 0 || len(parsed.Picker.Command) == 0 || parsed.Picker.Command[0] != "fzf" {
		t.Errorf("generated policy did not round-trip: %+v", parsed)
	}

	target := filepath.Join(h.home, "generated.toml")
	os.WriteFile(target, []byte("existing\n"), 0o644)
	root := cli.NewRootCommandWithIO(io.Discard, io.Discard)
	root.SetArgs([]string{"--config", target, "config", "init"})
	if err := root.Execute(); err == nil {
		t.Error("config init must refuse to clobber an existing file")
	}
}

func TestBootstrapScansRecursivelyAndReportsOnly(t *testing.T) {
	h := newHarness(t)
	deep := filepath.Join(h.scanRoot, "host", "owner", "deep-repo")
	os.MkdirAll(filepath.Dir(deep), 0o755)
	cmd := exec.Command("git", "clone", "--quiet", h.repo.Root, deep)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}

	out := h.mustRun("bootstrap", h.scanRoot, "--max-depth", "5")
	if !strings.Contains(out, "deep-repo") || !strings.Contains(out, "canonical") {
		t.Errorf("recursive scan should find the deep repo:\n%s", out)
	}
	// A report changes nothing.
	if _, err := os.Stat(filepath.Join(h.home, "index")); !os.IsNotExist(err) {
		t.Error("bootstrap without an action must not create anything")
	}
}

func TestBootstrapSymlinkIndexAndGeneratedConfig(t *testing.T) {
	h := newHarness(t)
	index := filepath.Join(h.home, "index")
	generated := filepath.Join(h.home, "indexed-config.toml")

	out := h.mustRun("bootstrap", h.scanRoot,
		"--index", index, "--layout", "flat", "--apply",
		"--config-out", generated)
	if !strings.Contains(out, "created 1 symlink") {
		t.Fatalf("bootstrap output:\n%s", out)
	}
	link := filepath.Join(index, "demo")
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("want a symlink at %s: %+v %v", link, info, err)
	}
	if _, err := os.Stat(h.repo.Root); err != nil {
		t.Error("indexing must leave the physical repository untouched")
	}
	body, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), config.Contract(index)) {
		t.Errorf("generated config should scan the index:\n%s", body)
	}
	if !strings.Contains(string(body), `project_root = "`+config.Contract(h.scanRoot)+`"`) {
		t.Errorf("the index is navigation, not where physical new repos should land:\n%s", body)
	}

	// The normal repo inventory follows direct repo symlinks, so the catalog is
	// a real navigation layer rather than a set of links dev itself ignores.
	var listed bytes.Buffer
	root := cli.NewRootCommandWithIO(&listed, io.Discard)
	root.SetArgs([]string{"--config", generated, "repo", "list", "--long"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed.String(), config.Contract(filepath.Join(index, "demo"))) {
		t.Errorf("repo list should retain the index path:\n%s", listed.String())
	}
}

func TestBootstrapIndexIsIdempotent(t *testing.T) {
	h := newHarness(t)
	index := filepath.Join(h.home, "index")
	h.mustRun("bootstrap", h.scanRoot, "--index", index, "--apply")

	out := h.mustRun("bootstrap", h.scanRoot, "--index", index)
	if !strings.Contains(out, "already current") || !strings.Contains(out, "0 ready") {
		t.Errorf("a correct index should be current, not recreated:\n%s", out)
	}
}

func TestBootstrapJSON(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("bootstrap", h.scanRoot, "--json")
	var decoded struct {
		Roots        []string `json:"roots"`
		Repositories []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(decoded.Repositories) != 1 || decoded.Repositories[0].Name != "demo" {
		t.Errorf("decoded: %+v", decoded)
	}
}

func TestBootstrapMoveUpdatesTaskPaths(t *testing.T) {
	h := newHarness(t)
	// A tracked task in the main checkout (no linked worktree, so moving is
	// permitted). The task path has to follow the repository.
	h.mustRun("start", "demo", "--task", "move me", "--branch", "feat/move-me",
		"--base", "main", "--no-worktree")
	destRoot := filepath.Join(h.home, "moved")

	out := h.mustRun("bootstrap", h.scanRoot, "--move", destRoot, "--apply", "--yes")
	if !strings.Contains(out, "moved 1 repository") {
		t.Fatalf("move output:\n%s", out)
	}
	dest := filepath.Join(destRoot, "demo")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("repo not moved: %v", err)
	}
	if _, err := os.Stat(h.repo.Root); !os.IsNotExist(err) {
		t.Error("old physical path should be gone")
	}

	ls := h.mustRun("ls", "--json")
	if !strings.Contains(ls, config.Contract(dest)) {
		t.Errorf("task repo_path should follow the explicit move:\n%s", ls)
	}
}

func TestBootstrapMoveRefusesDirtyRepository(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.repo.Root, "dirty.txt"), []byte("wip"), 0o644)
	dest := filepath.Join(h.home, "moved")

	_, _, err := h.run("bootstrap", h.scanRoot, "--move", dest, "--apply", "--yes")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("dirty repository move should be refused, got %v", err)
	}
	if _, err := os.Stat(h.repo.Root); err != nil {
		t.Error("blocked source must remain in place")
	}
}

func TestBootstrapRejectsIndexAndMoveTogether(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("bootstrap", h.scanRoot, "--index", filepath.Join(h.home, "i"),
		"--move", filepath.Join(h.home, "m"))
	if err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Errorf("want mutually-exclusive error, got %v", err)
	}
}

func TestConfigurableTUIToolsAndKeyClashes(t *testing.T) {
	h := newHarness(t)
	custom := filepath.Join(h.home, "tools.toml")
	os.WriteFile(custom, []byte(`
[[tui.tools]]
key = "V"
name = "nvim"
run = "nvim ."
`), 0o644)

	var out bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, io.Discard)
	root.SetArgs([]string{"--config", custom, "tui", "tools"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "V") || !strings.Contains(out.String(), "nvim .") {
		t.Errorf("custom tool not listed:\n%s", out.String())
	}

	os.WriteFile(custom, []byte(`
[[tui.tools]]
key = "q"
name = "bad"
run = "echo bad"
`), 0o644)
	root = cli.NewRootCommandWithIO(io.Discard, io.Discard)
	root.SetArgs([]string{"--config", custom, "tui", "tools"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "quit") {
		t.Errorf("reserved key should be rejected, got %v", err)
	}
}

func TestRepoRemoteCachedSearchAndLocalMatch(t *testing.T) {
	h := newHarness(t)
	remoteURL := "https://github.com/owner/demo.git"
	h.repo.Git("remote", "add", "origin", remoteURL)
	cachePath := filepath.Join(config.CacheHome(), "dev", "remotes.json")
	if err := forge.SaveCache(cachePath, []forge.RemoteRepo{
		{Forge: forge.GitHub, Name: "demo", FullName: "owner/demo",
			Description: "API service", URL: "https://github.com/owner/demo", CloneURL: remoteURL, Visibility: "private"},
		{Forge: forge.GitLab, Name: "other", FullName: "group/other",
			Description: "unrelated", URL: "https://gitlab.com/group/other",
			CloneURL: "https://gitlab.com/group/other.git", Visibility: "public"},
	}); err != nil {
		t.Fatal(err)
	}

	out := h.mustRun("repo", "remote", "github api", "--cached")
	if !strings.Contains(out, "owner/demo") || strings.Contains(out, "group/other") {
		t.Errorf("term-wise cached search failed:\n%s", out)
	}
	if !strings.Contains(out, config.Contract(h.repo.Root)) {
		t.Errorf("local clone should be matched by origin identity:\n%s", out)
	}

	out = h.mustRun("repo", "remote", "--cached", "--json")
	if !strings.Contains(out, `"local_path"`) || !strings.Contains(out, `"full_name": "owner/demo"`) {
		t.Errorf("cached JSON should use stable keys and local match:\n%s", out)
	}

	out = h.mustRun("repo", "remote", "--cached", "--visibility", "private")
	if !strings.Contains(out, "owner/demo") || strings.Contains(out, "group/other") {
		t.Errorf("visibility filter failed:\n%s", out)
	}
}

func TestJournalJSONAndMarkdown(t *testing.T) {
	h := newHarness(t)
	h.repo.Commit("journal.txt", "work\n", "feat: journal work")
	out := h.mustRun("journal", "--since", "2026-01-01", "--until", "2026-01-01", "--json")
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("journal json: %v\n%s", err, out)
	}
	if report["schema_version"] != float64(1) {
		t.Fatalf("journal schema: %+v", report)
	}
	out = h.mustRun("journal", "--since", "2026-01-01", "--until", "2026-01-01")
	if !strings.Contains(out, "Development journal") || !strings.Contains(out, "feat: journal work") {
		t.Fatalf("journal markdown:\n%s", out)
	}
}

func TestSummaryJSONAndAdaptiveMarkdown(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("summary", "--no-runtime", "--json")
	var report struct {
		SchemaVersion int `json:"schema_version"`
		Capabilities  struct {
			RuntimeCollected bool `json:"runtime_collected"`
		} `json:"capabilities"`
		Projects []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("summary json: %v\n%s", err, out)
	}
	if report.SchemaVersion != 1 || report.Capabilities.RuntimeCollected || len(report.Projects) != 1 || report.Projects[0].Path != h.repo.Root {
		t.Fatalf("summary report = %+v", report)
	}

	h.repo.Write("dirty.txt", "work\n")
	out = h.mustRun("summary", "--no-runtime")
	if !strings.Contains(out, "Active work") || !strings.Contains(out, "### demo") || !strings.Contains(out, "dirty") {
		t.Fatalf("adaptive summary:\n%s", out)
	}
	out = h.mustRun("summary", "--no-runtime", "--detail", "compact")
	if !strings.Contains(out, "Project index") || strings.Contains(out, "### demo") {
		t.Fatalf("compact summary:\n%s", out)
	}
}

func TestRepoRemoteCachedErrorsWhenMissing(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("repo", "remote", "--cached")
	if err == nil || !strings.Contains(err.Error(), "no remote cache") {
		t.Errorf("missing cache should explain how to populate it, got %v", err)
	}
}

func TestEditCreatesDetectedConfigAndOpensIt(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.home, "new config.toml")
	record := filepath.Join(h.home, "opened-path")
	editor := filepath.Join(h.home, "fake-editor")
	body := "#!/bin/sh\nprintf '%s' \"$1\" > \"$DEV_EDIT_RECORD\"\nprintf '\\n# opened by test\\n' >> \"$1\"\n"
	if err := os.WriteFile(editor, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_EDIT_RECORD", record)

	var out, errOut bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, &errOut)
	root.SetArgs([]string{"--config", path, "edit", "--editor", editor})
	if err := root.Execute(); err != nil {
		t.Fatalf("edit: %v\nstderr: %s", err, errOut.String())
	}
	opened, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("fake editor was not invoked: %v", err)
	}
	if string(opened) != path {
		t.Errorf("editor opened %q, want %q", opened, path)
	}
	generated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "[paths]") ||
		!strings.Contains(string(generated), "# opened by test") {
		t.Errorf("missing generated template or editor change:\n%s", generated)
	}
	if !strings.Contains(errOut.String(), "created") {
		t.Errorf("user should be told the file was generated: %q", errOut.String())
	}
}

func TestConfigEditUsesExistingFileAndVisualFirst(t *testing.T) {
	h := newHarness(t)
	record := filepath.Join(h.home, "which-editor")
	visual := filepath.Join(h.home, "visual-editor")
	fallback := filepath.Join(h.home, "fallback-editor")
	for path, label := range map[string]string{visual: "visual", fallback: "editor"} {
		body := "#!/bin/sh\nprintf '" + label + ":%s' \"$1\" > \"$DEV_EDIT_RECORD\"\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEV_EDIT_RECORD", record)
	t.Setenv("VISUAL", visual)
	t.Setenv("EDITOR", fallback)

	root := cli.NewRootCommandWithIO(io.Discard, io.Discard)
	root.SetArgs([]string{"--config", h.configPath, "config", "edit"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(record)
	if string(got) != "visual:"+h.configPath {
		t.Errorf("$VISUAL should win over $EDITOR, got %q", got)
	}
}

func TestEditEditorFlagOverridesEnvironment(t *testing.T) {
	h := newHarness(t)
	record := filepath.Join(h.home, "which-editor")
	chosen := filepath.Join(h.home, "chosen")
	os.WriteFile(chosen, []byte("#!/bin/sh\nprintf chosen > \"$DEV_EDIT_RECORD\"\n"), 0o755)
	t.Setenv("DEV_EDIT_RECORD", record)
	t.Setenv("VISUAL", "/definitely/not/the/editor")

	root := cli.NewRootCommandWithIO(io.Discard, io.Discard)
	root.SetArgs([]string{"--config", h.configPath, "edit", "--editor", chosen})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(record)
	if string(got) != "chosen" {
		t.Errorf("--editor should win, got %q", got)
	}
}

func TestStartDirectTracksCurrentBranchWithoutCreatingAnything(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("start", "demo", "--task", "quick fix", "--direct")
	if !strings.Contains(out, "main (direct)") {
		t.Errorf("start output should make the mode explicit: %q", out)
	}
	if branches := h.repo.Git("branch", "--format=%(refname:short)"); branches != "main" {
		t.Errorf("direct mode should create no branch, got %q", branches)
	}
	if worktrees := h.repo.Git("worktree", "list", "--porcelain"); strings.Count(worktrees, "worktree ") != 1 {
		t.Errorf("direct mode should create no worktree:\n%s", worktrees)
	}
	ls := h.mustRun("ls", "--json")
	if !strings.Contains(ls, `"mode": "direct"`) || !strings.Contains(ls, `"branch": "main"`) {
		t.Errorf("task should explicitly record direct/main:\n%s", ls)
	}
}

func TestDirectTaskWarmResumeAndDone(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "quick fix", "--direct")
	os.WriteFile(filepath.Join(h.repo.Root, "quick.txt"), []byte("done\n"), 0o644)
	h.repo.Git("add", "quick.txt")
	h.repo.Git("commit", "-m", "fix: quick change")

	h.mustRun("park", "quick fix", "--next", "check the result")
	out := h.mustRun("--allow-shared-checkout", "resume", "quick fix")
	if !strings.Contains(out, "(direct)") {
		t.Errorf("resume should preserve direct mode: %q", out)
	}
	if _, err := os.Stat(filepath.Join(h.wtRoot, "demo")); !os.IsNotExist(err) {
		t.Error("resuming a direct task must not invent a worktree")
	}

	out = h.mustRun("--allow-shared-checkout", "done", "quick fix")
	if !strings.Contains(out, "completed directly on main") {
		t.Errorf("direct done should need no integration mode: %q", out)
	}
	// A done direct task used repo__main as its identity. The next quick task
	// on main must be able to replace it without a manual sweep first.
	out = h.mustRun("start", "demo", "--task", "another quick fix", "--direct")
	if !strings.Contains(out, "another quick fix") {
		t.Errorf("done direct task should not block reuse of main: %q", out)
	}
}

func TestDirectTaskCannotGoCold(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "quick fix", "--direct")
	_, _, err := h.run("park", "quick fix", "--cold")
	if err == nil || !strings.Contains(err.Error(), "cannot go cold") {
		t.Errorf("direct task cannot remove the canonical checkout, got %v", err)
	}
}

func TestStartBranchOnlyActuallySwitchesBranch(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("start", "demo", "--task", "local feature",
		"--branch", "feat/local", "--base", "main", "--branch-only")
	if !strings.Contains(out, "(branch)") {
		t.Errorf("mode should be explicit: %q", out)
	}
	if branch := h.repo.Git("branch", "--show-current"); branch != "feat/local" {
		t.Errorf("branch-only must switch the canonical checkout, got %q", branch)
	}
	if worktrees := h.repo.Git("worktree", "list", "--porcelain"); strings.Count(worktrees, "worktree ") != 1 {
		t.Errorf("branch-only should create no linked worktree:\n%s", worktrees)
	}
	if ls := h.mustRun("ls", "--json"); !strings.Contains(ls, `"mode": "branch"`) {
		t.Errorf("task mode not recorded:\n%s", ls)
	}
}

func TestDirectRejectsDifferentBranchFlag(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("start", "demo", "--task", "bad", "--direct", "--branch", "feat/not-main")
	if err == nil || !strings.Contains(err.Error(), "already checked out") {
		t.Errorf("direct must not claim a branch it is not on, got %v", err)
	}
}

func TestStatusAndJSONExposeRichChangeCounts(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "inspect changes", "--direct")
	// One path staged and then modified again, plus one untracked path: two
	// unique paths, but staged=1, unstaged=1, untracked=1.
	h.repo.Write("both.txt", "staged\n")
	h.repo.Git("add", "both.txt")
	h.repo.Write("both.txt", "modified after stage\n")
	h.repo.Write("untracked.txt", "new\n")

	cwd, _ := os.Getwd()
	os.Chdir(h.repo.Root)
	defer os.Chdir(cwd)
	out := h.mustRun("status")
	if !strings.Contains(out, "branch     main  +1 !1 ?1") ||
		!strings.Contains(out, "2 changed paths (+1 staged, !1 unstaged, ?1 untracked)") {
		t.Errorf("status should show rich counts:\n%s", out)
	}

	out = h.mustRun("ls", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("json: %v %s", err, out)
	}
	for key, want := range map[string]float64{
		"changed": 2, "staged": 1, "unstaged": 1, "untracked": 1,
	} {
		if got := rows[0][key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

func TestCacheListAndClearLeavesStatsDataAlone(t *testing.T) {
	h := newHarness(t)
	remote := filepath.Join(config.CacheHome(), "dev", "remotes.json")
	notesIndex := filepath.Join(config.CacheHome(), "dev", "notes.db")
	size := filepath.Join(config.CacheHome(), "dev", "sizes-v1.json")
	gitignore := filepath.Join(config.CacheHome(), "dev", "gitignore", "Go.gitignore")
	license := filepath.Join(config.CacheHome(), "dev", "licenses", "mit.json")
	os.MkdirAll(filepath.Dir(remote), 0o755)
	os.MkdirAll(filepath.Dir(gitignore), 0o755)
	os.MkdirAll(filepath.Dir(license), 0o755)
	os.WriteFile(remote, []byte("remote cache"), 0o600)
	os.WriteFile(notesIndex, []byte("note index"), 0o600)
	os.WriteFile(size, []byte("size cache"), 0o600)
	os.WriteFile(gitignore, []byte("*.test\n"), 0o644)
	os.WriteFile(license, []byte("{}\n"), 0o644)

	// Stats and Markdown notes live under XDG data, not cache.
	noteSource := filepath.Join(h.home, "state", "notes", "repo", "note.md")
	os.MkdirAll(filepath.Dir(noteSource), 0o700)
	os.WriteFile(noteSource, []byte("durable note"), 0o600)
	statsPath := filepath.Join(h.home, "state", "stats.db")
	os.MkdirAll(filepath.Dir(statsPath), 0o755)
	os.WriteFile(statsPath, []byte("durable"), 0o600)

	out := h.mustRun("cache", "list")
	if !strings.Contains(out, "remote") || !strings.Contains(out, "notes") ||
		!strings.Contains(out, "size") || !strings.Contains(out, "gitignore") || !strings.Contains(out, "licenses") || !strings.Contains(out, "not cache") {
		t.Errorf("cache list:\n%s", out)
	}
	h.mustRun("cache", "clear", "all")
	if _, err := os.Stat(remote); !os.IsNotExist(err) {
		t.Error("remote cache should be gone")
	}
	if _, err := os.Stat(notesIndex); !os.IsNotExist(err) {
		t.Error("notes FTS cache should be gone")
	}
	if _, err := os.Stat(size); !os.IsNotExist(err) {
		t.Error("size cache should be gone")
	}
	if _, err := os.Stat(gitignore); !os.IsNotExist(err) {
		t.Error("gitignore cache should be gone")
	}
	if _, err := os.Stat(license); !os.IsNotExist(err) {
		t.Error("license cache should be gone")
	}
	if _, err := os.Stat(statsPath); err != nil {
		t.Error("cache clear must never touch stats data")
	}
	if _, err := os.Stat(noteSource); err != nil {
		t.Error("cache clear must never touch durable Markdown notes")
	}
}

func TestStatsClearRequiresScopeAndDeletesSelectedRows(t *testing.T) {
	h := newHarness(t)
	store, err := stats.Open(stats.Path(filepath.Join(h.home, "state")))
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now()
	store.Add(
		stats.Entry{Day: day, Repo: "api", Source: stats.SourceGit, Seconds: 100},
		stats.Entry{Day: day, Repo: "web", Source: stats.SourceGit, Seconds: 100},
	)
	store.Close()

	if _, _, err := h.run("stats", "clear", "--yes"); err == nil || !strings.Contains(err.Error(), "choose --repo") {
		t.Errorf("unscoped clear must be refused, got %v", err)
	}
	out := h.mustRun("stats", "clear", "--repo", "api", "--yes")
	if !strings.Contains(out, "deleted 1 activity row") {
		t.Errorf("clear output: %q", out)
	}
	store, _ = stats.Open(stats.Path(filepath.Join(h.home, "state")))
	defer store.Close()
	rows, _ := store.RepoTotals(stats.Query{Since: day.Add(-time.Hour), Until: day.Add(time.Hour)})
	if len(rows) != 1 || rows[0].Repo != "web" {
		t.Errorf("only web should remain: %+v", rows)
	}
}

func TestStatsBackfillSingleRepo(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("stats", "backfill", "--repo", "demo", "--since", "1y")
	if !strings.Contains(out, "scanning 1 repositories") {
		t.Errorf("single repo backfill: %q", out)
	}
	out = h.mustRun("stats", "--repo", "demo", "--since", "1y", "--by-repo")
	if !strings.Contains(out, "demo") {
		t.Errorf("backfilled repo missing from stats:\n%s", out)
	}
}

func TestRepoListShowsLatestDirtyEdit(t *testing.T) {
	h := newHarness(t)
	// gittest pins commit dates, while this file has a current mtime. The LATEST
	// column should therefore reflect the edit, not only the old commit.
	h.repo.Write("dirty-now.txt", "wip\n")
	out := h.mustRun("repo", "list")
	if !strings.Contains(out, "LATEST") || !strings.Contains(out, "0m") {
		t.Errorf("repo list should show latest dirty edit:\n%s", out)
	}
}

func TestRepoContextReportsWholeRepoFromLinkedWorktree(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "auth", "--branch", "feat/auth", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-auth")

	out := h.mustRun("repo", "context", "demo")
	for _, want := range []string{
		"# dev repo context: demo", "Linked worktrees: 1", h.repo.Root, wtPath,
		"demo__feat-auth", "feat/auth — dev",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("repo context missing %q:\n%s", want, out)
		}
	}

	t.Chdir(wtPath)
	fromChild := h.mustRun("repo", "context")
	if !strings.Contains(fromChild, "# dev repo context: demo") ||
		!strings.Contains(fromChild, h.repo.Root) || !strings.Contains(fromChild, wtPath) {
		t.Errorf("linked cwd should still report the whole repo:\n%s", fromChild)
	}
}

func TestNoteAddListShowSearchAndPath(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("note", "add", "try event subscription", "--repo", "demo", "--tag", "Idea", "--tag", "git")
	id := strings.Fields(out)[0]
	if len(id) != 36 || !strings.Contains(out, "try event subscription") {
		t.Fatalf("add output: %q", out)
	}

	out = h.mustRun("note", "list", "demo")
	if !strings.Contains(out, id[:8]) || !strings.Contains(out, "git,idea") {
		t.Errorf("list output: %q", out)
	}
	out = h.mustRun("note", "show", id[:8])
	if !strings.Contains(out, id) || !strings.Contains(out, "try event subscription") {
		t.Errorf("show output: %q", out)
	}
	out = h.mustRun("note", "search", "subscription event", "--repo", "demo")
	if !strings.Contains(out, id[:8]) {
		t.Errorf("search output: %q", out)
	}
	repoJSON := h.mustRun("repo", "list", "--json")
	if !strings.Contains(repoJSON, `"count": 1`) || !strings.Contains(repoJSON, `"latest_preview": "try event subscription"`) {
		t.Errorf("repo JSON note summary missing: %s", repoJSON)
	}
	out = strings.TrimSpace(h.mustRun("note", "path", "demo"))
	if !strings.HasPrefix(out, filepath.Join(h.home, "state", "notes")) {
		t.Errorf("note path = %q", out)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 || entries[0].Name() != id+".md" {
		t.Errorf("durable source entries=%v err=%v", entries, err)
	}
}

func TestNoteSearchRebuildsAfterCacheClear(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("note", "add", "survive index deletion", "--repo", "demo")
	id := strings.Fields(out)[0]
	if out = h.mustRun("note", "search", "survive"); !strings.Contains(out, id[:8]) {
		t.Fatal("initial search did not find note")
	}
	index := filepath.Join(config.CacheHome(), "dev", "notes.db")
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("search index not created: %v", err)
	}
	h.mustRun("cache", "clear", "notes")
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Error("cache should be gone")
	}
	out = h.mustRun("note", "search", "survive")
	if !strings.Contains(out, id[:8]) {
		t.Errorf("search should rebuild from Markdown: %q", out)
	}
}

func TestNoteEditUsesTemporaryBodyAndPreservesMetadata(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("note", "add", "original thought", "--repo", "demo", "--tag", "idea")
	id := strings.Fields(out)[0]
	editor := filepath.Join(h.home, "note-editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'revised by editor\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out = h.mustRun("note", "edit", id[:8], "--editor", editor)
	if !strings.Contains(out, "revised by editor") {
		t.Errorf("edit output: %q", out)
	}
	out = h.mustRun("note", "show", id, "--json")
	if !strings.Contains(out, `"body":"revised by editor\n"`) || !strings.Contains(out, `"idea"`) {
		t.Errorf("body changed but tags should survive: %q", out)
	}
}

func TestNoteDeleteRequiresYesOutsideTTY(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("note", "add", "delete me", "--repo", "demo")
	id := strings.Fields(out)[0]
	if _, _, err := h.run("note", "delete", id[:8]); err == nil || !strings.Contains(err.Error(), "without --yes") {
		t.Errorf("noninteractive delete should be refused, got %v", err)
	}
	h.mustRun("note", "delete", id[:8], "--yes")
	if out := h.mustRun("note", "list", "demo"); !strings.Contains(out, "No notes") {
		t.Errorf("deleted note remains: %q", out)
	}
	if out := strings.TrimSpace(h.mustRun("note", "list", "demo", "--json")); out != "[]" {
		t.Errorf("empty JSON note list = %q, want []", out)
	}
}

func TestNoteAddEditorAndEmptyBodyGuard(t *testing.T) {
	h := newHarness(t)
	editor := filepath.Join(h.home, "note-editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'written interactively\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("note", "add", "--repo", "demo", "--editor", "--editor-command", editor)
	if !strings.Contains(out, "written interactively") {
		t.Errorf("editor add: %q", out)
	}
	if _, _, err := h.run("note", "add", "--repo", "demo"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty add should fail, got %v", err)
	}
}

func TestNoteListAllAndTagFilter(t *testing.T) {
	h := newHarness(t)
	h.mustRun("note", "add", "first", "--repo", "demo", "--tag", "one")
	h.mustRun("note", "add", "second", "--repo", "demo", "--tag", "two")
	out := h.mustRun("note", "list", "--all", "--tag", "two")
	if !strings.Contains(out, "second") || strings.Contains(out, "first") {
		t.Errorf("tag filter: %q", out)
	}
	out = h.mustRun("note", "list", "--all", "--json")
	var notes []map[string]any
	if err := json.Unmarshal([]byte(out), &notes); err != nil || len(notes) != 2 {
		t.Errorf("json notes=%v err=%v", notes, err)
	}
}

func TestNoteFromLinkedWorktreeAttachesToCanonicalRepository(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "feature", "--branch", "feat/note", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-note")
	cwd, _ := os.Getwd()
	if err := os.Chdir(wtPath); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("note", "add", "thought from worktree")
	_ = os.Chdir(cwd)
	id := strings.Fields(out)[0]

	out = h.mustRun("note", "list", "demo", "--json")
	if !strings.Contains(out, id) || !strings.Contains(out, `"repository": "demo"`) {
		t.Errorf("canonical repo should find worktree note: %q", out)
	}
	assets, err := os.ReadDir(filepath.Join(h.home, "state", "assets"))
	if err != nil || len(assets) != 1 {
		t.Errorf("one canonical catalog asset expected: entries=%v err=%v", assets, err)
	}
}

func TestNoteResolutionRejectsEmptyAndTooShortPrefixes(t *testing.T) {
	h := newHarness(t)
	h.mustRun("note", "add", "only note", "--repo", "demo")
	for _, ref := range []string{"", "2", "1234567"} {
		if _, _, err := h.run("note", "delete", ref, "--yes"); err == nil || !strings.Contains(err.Error(), "at least 8") {
			t.Errorf("delete ref %q = %v", ref, err)
		}
	}
	if out := h.mustRun("note", "list", "demo"); !strings.Contains(out, "only note") {
		t.Error("invalid prefix deleted the note")
	}
}

// An intent whose transcript was never written, or whose HEAD is gone after a
// rebase, can never be finalized. Before `dev artifact discard` existed it
// blocked integration and retirement forever with no tool-mediated way out.
func writeIntentJSON(t *testing.T, dir, id string, h *harness, status string) {
	t.Helper()
	record, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"id":             id,
		"run_id":         "codex-run",
		"provider":       "codex",
		"session_id":     "11111111-2222-3333-4444-555555555555",
		"repo_path":      h.repo.Root,
		"git_common_dir": filepath.Join(h.repo.Root, ".git"),
		"worktree_path":  h.repo.Root,
		"branch":         "feat/gone",
		"head":           "0000000000000000000000000000000000000000",
		"status":         status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactDiscardUnblocksAnIntentThatCanNeverFinalize(t *testing.T) {
	h := newHarness(t)
	intentDir := filepath.Join(h.home, "state", "artifact-intents", "v1")
	if err := os.MkdirAll(intentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIntentJSON(t, intentDir, "intent-deadbeef", h, "failed")

	// Without --yes and without a TTY the loss must not happen silently.
	if _, _, err := h.run("artifact", "discard", "intent-deadbeef"); err == nil ||
		!strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("unconfirmed discard = %v", err)
	}
	if out := h.mustRun("artifact", "list"); !strings.Contains(out, "failed") {
		t.Fatalf("refused discard changed the intent:\n%s", out)
	}

	out := h.mustRun("artifact", "discard", "intent-deadbeef", "--yes")
	for _, want := range []string{"DISCARDING", "codex:", "feat/gone", "cannot be undone", "DISCARDED"} {
		if !strings.Contains(out, want) {
			t.Errorf("discard output missing %q:\n%s", want, out)
		}
	}
	if listed := h.mustRun("artifact", "list"); !strings.Contains(listed, "discarded") {
		t.Fatalf("intent was not recorded as discarded:\n%s", listed)
	}
	// Re-running is a no-op rather than an error, so cleanup scripts are safe.
	if again := h.mustRun("artifact", "discard", "intent-deadbeef", "--yes"); !strings.Contains(again, "already discarded") {
		t.Fatalf("second discard = %q", again)
	}
}

// An armed intent still has a live path to preserving its transcript, so
// discarding it must be refused rather than offered as a shortcut.
func TestArtifactDiscardRefusesAnArmedIntent(t *testing.T) {
	h := newHarness(t)
	intentDir := filepath.Join(h.home, "state", "artifact-intents", "v1")
	if err := os.MkdirAll(intentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIntentJSON(t, intentDir, "intent-armed01", h, "armed")
	_, _, err := h.run("artifact", "discard", "intent-armed01", "--yes")
	if err == nil || !strings.Contains(err.Error(), "finalize") {
		t.Fatalf("discarding an armed intent = %v", err)
	}
}

// A branch-backed task whose branch was deleted after integration can be
// finished by no path at all: done, resume and retire all resolve the branch
// first. Sweep must offer to reap the record instead of leaving it forever.
func TestSweepReapsATaskWhoseBranchIsGone(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "landed", "--branch", "feat/landed", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-landed")
	// Simulate out-of-band deletion: managed `dev wt rm` now refuses to create
	// task metadata drift and directs the caller to lifecycle/reconciliation.
	h.repo.Git("worktree", "remove", wtPath)
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	h.repo.Git("branch", "-D", "feat/landed")

	report := h.mustRun("sweep")
	if !strings.Contains(report, "branch feat/landed no longer exists") ||
		!strings.Contains(report, "reap the task entry") {
		t.Fatalf("sweep did not report the dead branch:\n%s", report)
	}
	// Report-only until --apply, like every other suggestion.
	if listed := h.mustRun("ls", "--json"); !strings.Contains(listed, "landed") {
		t.Fatalf("report-only sweep removed the task:\n%s", listed)
	}

	h.mustRun("sweep", "--apply", "--yes")
	if listed := h.mustRun("ls", "--json"); strings.Contains(listed, "feat/landed") {
		t.Fatalf("task was not reaped:\n%s", listed)
	}
}

func TestSweepReapsATaskWhoseRepositoryIsGone(t *testing.T) {
	h := newHarness(t)
	// A direct task is the shape that was unreachable: the dead-branch rule
	// excludes direct mode, the stale-worktree rule needs a recorded worktree
	// path, and every lifecycle command resolves the repository first.
	h.mustRun("start", "demo", "--task", "leaked", "--direct")
	if err := os.RemoveAll(h.repo.Root); err != nil {
		t.Fatal(err)
	}

	report := h.mustRun("sweep")
	if !strings.Contains(report, "no longer exists") || !strings.Contains(report, "reap the task entry") {
		t.Fatalf("sweep did not report the missing repository:\n%s", report)
	}
	if listed := h.mustRun("ls", "--json"); !strings.Contains(listed, "leaked") {
		t.Fatalf("report-only sweep removed the task:\n%s", listed)
	}

	h.mustRun("sweep", "--apply", "--yes")
	if listed := h.mustRun("ls", "--json"); strings.Contains(listed, "leaked") {
		t.Fatalf("task was not reaped:\n%s", listed)
	}
}

func TestSweepRefusesToRemoveAnOrphanHoldingUnsavedTranscripts(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "gone", "--branch", "feat/gone", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-gone")
	// Remove it behind dev's back, which is how the real orphan appears: raw
	// git from another checkout, leaving dev's record still pointing here.
	// Going through `dev wt rm` would clear the recorded path and produce a
	// different, already-handled kind of drift.
	h.repo.Git("worktree", "remove", "--force", wtPath)

	// The state under test is a task that still records a worktree Git no
	// longer registers. Assert it rather than assume it: an earlier version of
	// this test removed the worktree through dev, which clears the recorded
	// path, and it passed on macOS only because a /private symlink made dev's
	// own path match fail there.
	if listed := h.mustRun("ls", "--json"); !strings.Contains(listed, `"worktree_path"`) ||
		!strings.Contains(listed, filepath.Base(wtPath)) {
		t.Fatalf("precondition lost: the task no longer records its worktree:\n%s", listed)
	}

	// Recreate the path the way a transcript writer that outlived its worktree
	// does: artifacts only, and content the repository has never seen.
	history := filepath.Join(wtPath, ".specstory", "history")
	if err := os.MkdirAll(history, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "session.md"), []byte("the only copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := h.mustRun("sweep")
	if !strings.Contains(report, "abandoned agent workspace") {
		t.Fatalf("sweep did not notice the orphan:\n%s", report)
	}
	if !strings.Contains(report, "salvage") || !strings.Contains(report, "not removed automatically") {
		t.Fatalf("sweep offered to remove an orphan holding the only copy:\n%s", report)
	}

	h.mustRun("sweep", "--apply", "--yes")
	if _, err := os.Stat(filepath.Join(history, "session.md")); err != nil {
		t.Fatalf("--apply destroyed an unsalvaged transcript: %v", err)
	}
}

func TestSweepRemovesAnOrphanWhoseFilesTheRepositoryAlreadyHas(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "shell", "--branch", "feat/shell", "--base", "main")
	wtPath := filepath.Join(h.wtRoot, "demo", "feat-shell")
	h.repo.Git("worktree", "remove", "--force", wtPath)

	// The state under test is a task that still records a worktree Git no
	// longer registers. Assert it rather than assume it: an earlier version of
	// this test removed the worktree through dev, which clears the recorded
	// path, and it passed on macOS only because a /private symlink made dev's
	// own path match fail there.
	if listed := h.mustRun("ls", "--json"); !strings.Contains(listed, `"worktree_path"`) ||
		!strings.Contains(listed, filepath.Base(wtPath)) {
		t.Fatalf("precondition lost: the task no longer records its worktree:\n%s", listed)
	}

	const transcript = "already committed\n"
	for _, root := range []string{wtPath, h.repo.Root} {
		history := filepath.Join(root, ".specstory", "history")
		if err := os.MkdirAll(history, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(history, "session.md"), []byte(transcript), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	report := h.mustRun("sweep")
	if !strings.Contains(report, "remove the empty shell") {
		t.Fatalf("sweep did not offer to remove a redundant shell:\n%s", report)
	}

	h.mustRun("sweep", "--apply", "--yes")
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("orphan shell survived --apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.repo.Root, ".specstory", "history", "session.md")); err != nil {
		t.Fatalf("sweep touched the repository's own copy: %v", err)
	}
}
