package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func newRepoWizardApp(t *testing.T, input string) (*App, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	gitConfig := "[user]\n\temail = dev@example.test\n\tname = dev test\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(home, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.ProjectRoot = projects
	cfg.Paths.ScanRoots = []string{projects}
	cfg.Paths.StateDir = filepath.Join(home, "state")
	cfg.Runtime.Backend = "none"
	var out bytes.Buffer
	app := &App{
		Cfg: cfg, Tasks: task.NewStore(cfg.TasksDir()), Catalog: catalog.NewStore(cfg.AssetsDir()),
		In: strings.NewReader(input), Out: &out, Err: &bytes.Buffer{}, runtimeInstance: runtime.None{},
		interactiveCheck: func() bool { return true },
	}
	app.Registry = catalog.NewRegistry(app.Catalog)
	return app, &out
}

func TestRepoNewWizardCreatesAgentReadyRepoAndCDs(t *testing.T) {
	// name, category, destination, preset, description, deployment, README,
	// gitignore, license, Claude plans, AGENTS, two setup skills, upstream
	// browser, remote, handoff, final confirmation.
	input := strings.Join([]string{
		"wizard", "", "", "", "", "", "", "", "", "", "",
		"n", "n", "", "n", "n", "", "",
	}, "\n") + "\n"
	app, out := newRepoWizardApp(t, input)
	cmd := newRepoNewCmd(app)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), "wizard")
	for _, relative := range []string{"README.md", "AGENTS.md", ".gitignore", filepath.Join(".claude", "settings.json")} {
		if _, err := os.Stat(filepath.Join(destination, relative)); err != nil {
			t.Fatalf("missing %s: %v\n%s", relative, err, out.String())
		}
	}
	if !strings.Contains(out.String(), "Create a repository") || !strings.Contains(out.String(), "cd ") {
		t.Fatalf("wizard output:\n%s", out.String())
	}
}

func TestRepoNewWizardDeclineMutatesNothing(t *testing.T) {
	input := "cancel-me\n\n\nminimal\n\n\n\n\n\nn\nn\nn\nn\nstay\nn\n"
	app, out := newRepoWizardApp(t, input)
	cmd := newRepoNewCmd(app)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), "cancel-me")); !os.IsNotExist(err) {
		t.Fatalf("declined wizard created a path: %v\n%s", err, out.String())
	}
}
