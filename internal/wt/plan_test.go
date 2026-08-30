package wt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

// projectRepo builds a repo with the given marker files, a .gitignore and the
// dependency directories those markers imply.
func projectRepo(t *testing.T, markers ...string) *gittest.Repo {
	t.Helper()
	r := gittest.New(t)
	r.Commit(".gitignore", ".env\n.venv/\nnode_modules/\n", "chore: ignore local state")
	for _, m := range markers {
		r.Commit(m, "", "chore: add "+m)
	}
	r.Write(".env", "TOKEN=x\n")
	os.MkdirAll(filepath.Join(r.Root, "node_modules", "pkg"), 0o755)
	os.MkdirAll(filepath.Join(r.Root, ".venv", "bin"), 0o755)
	return r
}

func settings(t *testing.T, repoPath string, mutate func(*config.Config)) wt.Settings {
	t.Helper()
	c := config.Default()
	c.Worktree.Include = []string{".env"}
	if mutate != nil {
		mutate(&c)
	}
	return wt.SettingsFor(c, repoPath)
}

func stepFor(p wt.Plan, kind wt.StepKind, what string) (wt.Step, bool) {
	for _, s := range p.Steps {
		if s.Kind == kind && s.What == what {
			return s, true
		}
	}
	return wt.Step{}, false
}

func TestPlanDefaultsToReinstall(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	plan := wt.BuildPlan(context.Background(), settings(t, r.Root, nil), r.Root)

	if _, ok := stepFor(plan, wt.StepCopyFile, ".env"); !ok {
		t.Errorf("the gitignored .env should be copied: %+v", plan.Steps)
	}
	if _, ok := stepFor(plan, wt.StepRun, "npm ci"); !ok {
		t.Errorf("npm ci should be the default: %+v", plan.Steps)
	}
	if _, ok := stepFor(plan, wt.StepCopyDir, "node_modules"); ok {
		t.Error("nothing should be copied without an explicit strategy")
	}
}

func TestPlanCopyStrategy(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategies = map[string]string{"node": "copy"}
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	if _, ok := stepFor(plan, wt.StepCopyDir, "node_modules"); !ok {
		t.Errorf("node_modules should be copied: %+v", plan.Steps)
	}
	// Copying replaces the install; running both would be wasted work.
	if _, ok := stepFor(plan, wt.StepRun, "npm ci"); ok {
		t.Error("the install command should not also run when copying")
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("copying node_modules is sound and should warn about nothing: %v", plan.Warnings)
	}
}

// Copying a virtualenv cannot work, so an unsound choice is narrowed back to
// reinstall with an explanation rather than silently producing a broken tree.
func TestPlanRefusesUnsoundStrategy(t *testing.T) {
	r := projectRepo(t, "uv.lock")
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategies = map[string]string{"python": "copy"}
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	if _, ok := stepFor(plan, wt.StepCopyDir, ".venv"); ok {
		t.Error("a virtualenv must never be copied")
	}
	if _, ok := stepFor(plan, wt.StepRun, "uv sync"); !ok {
		t.Errorf("it should fall back to reinstalling: %+v", plan.Steps)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("the downgrade must be explained")
	}
	if !strings.Contains(plan.Warnings[0], "pyvenv.cfg") {
		t.Errorf("the warning should say why, got %q", plan.Warnings[0])
	}
}

func TestPlanSkipStrategy(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategy = "skip"
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	step, ok := stepFor(plan, wt.StepRun, "npm ci")
	if !ok || !step.Skipped {
		t.Errorf("the install should be listed but skipped: %+v", plan.Steps)
	}
	if len(plan.Runnable()) != 1 {
		t.Errorf("only the .env copy should run, got %+v", plan.Runnable())
	}
}

// A global-cache ecosystem has nothing to copy, so copy collapses to
// reinstall — which for it is nearly free anyway.
func TestPlanCopyIsMootWithoutDepDirs(t *testing.T) {
	r := projectRepo(t, "go.mod")
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategies = map[string]string{"go": "copy"}
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	if _, ok := stepFor(plan, wt.StepRun, "go mod download"); !ok {
		t.Errorf("should fall back to the install command: %+v", plan.Steps)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("this is not worth warning about: %v", plan.Warnings)
	}
}

// Strategy and post_create are separate axes: an explicit command list
// replaces the derived install commands but leaves copy in effect.
func TestPlanExplicitCommandsKeepCopyStrategy(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.PostCreate = config.PostCreate{Commands: []string{"make bootstrap"}}
		c.Worktree.Strategies = map[string]string{"node": "copy"}
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	if _, ok := stepFor(plan, wt.StepCopyDir, "node_modules"); !ok {
		t.Errorf("copy should still apply: %+v", plan.Steps)
	}
	if _, ok := stepFor(plan, wt.StepRun, "make bootstrap"); !ok {
		t.Errorf("the configured command should run: %+v", plan.Steps)
	}
	if _, ok := stepFor(plan, wt.StepRun, "npm ci"); ok {
		t.Error("the derived install should be replaced by the configured list")
	}
}

func TestPlanSkipsAbsentDependencyDirectory(t *testing.T) {
	r := gittest.New(t)
	r.Commit("package-lock.json", "{}", "chore: add lockfile")
	// No node_modules in the source checkout at all.
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategies = map[string]string{"node": "copy"}
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	step, ok := stepFor(plan, wt.StepCopyDir, "node_modules")
	if !ok || !step.Skipped {
		t.Errorf("nothing to copy should be listed as skipped: %+v", plan.Steps)
	}
	if !strings.Contains(step.Why, "not present") {
		t.Errorf("the reason should say so, got %q", step.Why)
	}
}

func TestPlanRepoOverrideWins(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	r.Commit(".dev.toml", "[worktree]\n[worktree.strategies]\nnode = \"copy\"\n", "chore: pin worktree setup")

	c := config.Default()
	c.Worktree.Include = []string{".env"}
	c.Worktree.Strategy = "reinstall"
	plan := wt.BuildPlan(context.Background(), wt.SettingsFor(c, r.Root), r.Root)

	if _, ok := stepFor(plan, wt.StepCopyDir, "node_modules"); !ok {
		t.Errorf("the repo's .dev.toml should win over global config: %+v", plan.Steps)
	}
}

func TestPlanRejectsUnknownStrategyName(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategies = map[string]string{"node": "teleport"}
	})
	plan := wt.BuildPlan(context.Background(), set, r.Root)

	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "not a strategy") {
		t.Errorf("a typo should be reported, got %v", plan.Warnings)
	}
	if _, ok := stepFor(plan, wt.StepRun, "npm ci"); !ok {
		t.Error("it should fall back to reinstalling")
	}
}

// Copying must recreate symlinks rather than follow them: a pnpm node_modules
// is a farm of links into a global store, and dereferencing would turn a small
// copy into gigabytes.
func TestApplyCopyPreservesSymlinks(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	os.Symlink(filepath.Join(r.Root, "package.json"), filepath.Join(r.Root, "node_modules", "linked"))

	dst := filepath.Join(t.TempDir(), "checkout")
	os.MkdirAll(dst, 0o755)

	set := settings(t, r.Root, func(c *config.Config) {
		c.Worktree.Strategies = map[string]string{"node": "copy"}
	})
	p := &wt.Provisioner{Settings: set}
	plan := wt.BuildPlan(context.Background(), set, r.Root)
	res, err := p.Apply(context.Background(), plan, r.Root, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures: %v", res.Failures)
	}
	if _, err := os.Stat(filepath.Join(dst, "node_modules", "pkg")); err != nil {
		t.Errorf("the directory should have been copied: %v", err)
	}
	info, err := os.Lstat(filepath.Join(dst, "node_modules", "linked"))
	if err != nil {
		t.Fatalf("the symlink should exist: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("a symlink must be recreated, not dereferenced")
	}
}

func TestPlanWarnsAboutMissingTool(t *testing.T) {
	r := projectRepo(t, "mix.lock") // elixir's mix is very unlikely to be installed in CI
	plan := wt.BuildPlan(context.Background(), settings(t, r.Root, nil), r.Root)

	step, ok := stepFor(plan, wt.StepRun, "mix deps.get")
	if !ok {
		t.Fatalf("the install step should be listed even without the tool: %+v", plan.Steps)
	}
	if !step.Skipped {
		return // mix really is installed here; nothing to assert
	}
	if len(plan.Warnings) == 0 {
		t.Error("a missing tool should warn, since worktrees will come up incomplete")
	}
}

func TestPlanWarnsAboutInvalidRepoDefaultStrategy(t *testing.T) {
	r := projectRepo(t, "package-lock.json")
	r.Commit(".dev.toml", "[worktree]\nstrategy = \"teleport\"\n", "chore: invalid setup")
	plan := wt.BuildPlan(context.Background(), wt.SettingsFor(config.Default(), r.Root), r.Root)
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "not a strategy") {
		t.Errorf("repo-local typo should be visible in the plan: %v", plan.Warnings)
	}
	if _, ok := stepFor(plan, wt.StepRun, "npm ci"); !ok {
		t.Error("invalid value should fall back to reinstall")
	}
}

func TestProjectWorktreeCommandsRequireContentHashTrust(t *testing.T) {
	r := projectRepo(t, "go.mod")
	projectDir := filepath.Join(r.Root, ".dev-cli")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "version = 1\n[worktree]\npost_create = [\"make bootstrap\"]\n"
	if err := os.WriteFile(filepath.Join(projectDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Git("add", ".dev-cli/config.toml")
	r.Git("commit", "-m", "chore: configure worktrees")
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	if _, err := wt.SettingsForTrusted(context.Background(), cfg, r.Root); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("untrusted settings error = %v", err)
	}
	project, err := projectconfig.Load(r.Root, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := projectconfig.NewTrustStore(filepath.Join(cfg.StateDir(), "trust", "project-config-v1.json"))
	if _, err := store.Approve(context.Background(), r.Root, project.ExecutionHash); err != nil {
		t.Fatal(err)
	}
	settings, err := wt.SettingsForTrusted(context.Background(), cfg, r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Cmds.Commands) != 1 || settings.Cmds.Commands[0] != "make bootstrap" {
		t.Fatalf("settings = %+v", settings)
	}
}
