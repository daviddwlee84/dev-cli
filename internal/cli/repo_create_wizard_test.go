package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
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
	// name, category, destination, preset, description, customize, remote,
	// check-in, handoff, final confirmation.
	input := strings.Join([]string{
		"wizard", "", "", "", "", "n", "n", "", "", "",
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
	if strings.Contains(out.String(), "Gitignore templates") || strings.Contains(out.String(), "Enable Agent history hygiene") {
		t.Fatalf("default wizard should skip detailed customization questions:\n%s", out.String())
	}
}

func TestRepoNewWizardDeclineMutatesNothing(t *testing.T) {
	input := "cancel-me\n\n\nminimal\n\nn\n\nstay\nn\n"
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

func TestRepoNewWizardRoutesCloneReference(t *testing.T) {
	app, out := newRepoWizardApp(t, "")
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), source, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("from clone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), source, "add", "source.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), source, "commit", "-m", "test: source"); err != nil {
		t.Fatal(err)
	}
	app.In = strings.NewReader(strings.Join([]string{source, "", "", "", ""}, "\n") + "\n")

	cmd := newRepoNewCmd(app)
	cmd.SetArgs([]string{"--preset", "minimal"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), "source")
	if body, err := os.ReadFile(filepath.Join(destination, "source.txt")); err != nil || string(body) != "from clone\n" {
		t.Fatalf("cloned file = %q, %v\n%s", body, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(destination, "README.md")); err != nil {
		t.Fatalf("explicit clone preset was not applied: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "detected a clone reference") || !strings.Contains(out.String(), "setup        minimal") || !strings.Contains(out.String(), "cloned") {
		t.Fatalf("wizard output:\n%s", out.String())
	}
}
