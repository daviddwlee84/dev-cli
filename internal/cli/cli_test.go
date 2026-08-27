package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/cli"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/skill"
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
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

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
	if !strings.HasPrefix(out, "---\nname: "+skill.Name+"\n") {
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
	for _, want := range []string{"*.exe", ".claude/worktrees/", ".env"} {
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
		!strings.Contains(out, "[bootstrap]") || !strings.Contains(out, "[[tui.tools]]") {
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
	if len(parsed.EffectiveTools()) != 4 || parsed.Bootstrap.Layout != "flat" || parsed.Forge.CacheTTL.Duration == 0 {
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
			Description: "API service", URL: "https://github.com/owner/demo", CloneURL: remoteURL},
		{Forge: forge.GitLab, Name: "other", FullName: "group/other",
			Description: "unrelated", URL: "https://gitlab.com/group/other",
			CloneURL: "https://gitlab.com/group/other.git"},
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
}

func TestRepoRemoteCachedErrorsWhenMissing(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("repo", "remote", "--cached")
	if err == nil || !strings.Contains(err.Error(), "no fresh remote cache") {
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
