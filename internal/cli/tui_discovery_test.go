package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestTUIStartupRepositoryResolvesCheckoutAliasesAndCustomConfig(t *testing.T) {
	r := gittest.New(t)
	nested := filepath.Join(r.Root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(filepath.Dir(r.Root), "linked")
	r.Git("worktree", "add", "-b", "feature", linked)
	paths := []string{r.Root, nested, linked}
	alias := filepath.Join(filepath.Dir(r.Root), "alias")
	if err := os.Symlink(r.Root, alias); err == nil {
		paths = append(paths, alias)
	}
	cfg := config.Default()
	cfg.Paths.ScanRoots = nil
	cfg.Paths.RepoPaths = []string{r.Root}
	file := filepath.Join(filepath.Dir(r.Root), "alternate.toml")
	app := &App{Cfg: cfg, configPath: file}
	state := newTUIAppState(app)
	for _, cwd := range paths {
		resolver := newTUIProjectRootResolver(nil, t.Context())
		resolver.getwd = func() (string, error) { return cwd, nil }
		actions := tuiDiscoveryActions(state, resolver)
		startup, err := actions.Read(t.Context())
		if err != nil || startup.Path != r.Root || !startup.Covered || startup.CoverageErr != nil {
			t.Fatalf("%s: %+v %v", cwd, startup, err)
		}
		if err := os.WriteFile(file, []byte("[paths]\nscan_roots = []\nrepo_paths = []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := actions.Plan(t.Context(), startup, repo.DiscoveryExact)
		if err != nil || p.File != file || p.Path != r.Root {
			t.Fatal(p, err)
		}
		if (runtime.GOOS == "darwin" || runtime.GOOS == "linux") && p.Blocked != "" {
			t.Fatal(p.Blocked)
		}
	}
}

func TestTUIProjectResolverCapturesStartupDirectory(t *testing.T) {
	r := gittest.New(t)
	t.Chdir(r.Root)
	resolver := newTUIProjectRootResolver(nil, t.Context())
	t.Chdir(t.TempDir())
	target, err := resolver.ResolveTarget(t.Context())
	if err != nil || target.RepoPath != r.Root {
		t.Fatal("lazy lookup lost startup directory", target, err)
	}
}

func TestTUIStartupOutsideGitHasNoRegistrationTarget(t *testing.T) {
	dir := t.TempDir()
	resolver := newTUIProjectRootResolver(nil, t.Context())
	resolver.getwd = func() (string, error) { return dir, nil }
	state := newTUIAppState(&App{Cfg: config.Default()})
	r, err := tuiDiscoveryActions(state, resolver).Read(t.Context())
	if err != nil || r.Path != "" || r.CommonDir != "" {
		t.Fatal(r, err)
	}
}
