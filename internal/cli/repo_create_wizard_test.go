package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
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

func TestRepoClonePickerUsesExactCachedCloneURL(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	remote := forge.RemoteRepo{
		Forge: forge.GitLab, Name: "api", FullName: "group/api",
		CloneURL: "https://gitlab.example.test/group/api.git", Visibility: "private",
	}
	cache := forge.Cache{
		Version: forge.CacheVersion, SourceID: remoteCacheSourceID(app), FetchedAt: time.Now().UTC(), Complete: true,
		Repos: []forge.RemoteRepo{remote},
	}
	if err := forge.SaveCacheState(remoteCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if len(request.Items) != 2 || request.Items[0].Label != remote.Label() {
			t.Fatalf("picker request = %+v", request)
		}
		return picker.Result{Item: request.Items[0]}, nil
	}

	ref, err := promptRepoCloneReference(app, newPrompter(app))
	if err != nil {
		t.Fatal(err)
	}
	if ref != remote.CloneURL {
		t.Fatalf("clone ref = %q, want %q", ref, remote.CloneURL)
	}
}

func TestRepoClonePickerUsesStaleCacheAndCanCancel(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	var errOut bytes.Buffer
	app.Err = &errOut
	remote := forge.RemoteRepo{
		Forge: forge.GitHub, Name: "old", FullName: "owner/old",
		CloneURL: "https://github.com/owner/old.git",
	}
	cache := forge.Cache{
		Version: forge.CacheVersion, SourceID: remoteCacheSourceID(app), FetchedAt: time.Now().UTC(),
		Complete: false, Repos: []forge.RemoteRepo{remote},
	}
	if err := forge.SaveCacheState(remoteCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if request.Items[0].Value != remote.CloneURL {
			t.Fatalf("stale candidate = %+v", request.Items[0])
		}
		return picker.Result{}, picker.ErrCanceled
	}

	_, err := promptRepoCloneReference(app, newPrompter(app))
	if !errors.Is(err, errPromptCanceled) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(errOut.String(), "stale or incomplete") {
		t.Fatalf("warning = %q", errOut.String())
	}
}

func TestRepoClonePickerBackendErrorIsNotCancellation(t *testing.T) {
	app, out := newRepoWizardApp(t, "")
	remote := forge.RemoteRepo{
		Forge: forge.GitHub, Name: "api", FullName: "owner/api",
		CloneURL: "https://github.com/owner/api.git",
	}
	cache := forge.Cache{
		Version: forge.CacheVersion, SourceID: remoteCacheSourceID(app), FetchedAt: time.Now().UTC(), Complete: true,
		Repos: []forge.RemoteRepo{remote},
	}
	if err := forge.SaveCacheState(remoteCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	app.pickerSelect = func(context.Context, picker.Request) (picker.Result, error) {
		return picker.Result{}, errors.New("picker failed")
	}
	cmd := newRepoCloneCmd(app)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "picker failed") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), "Canceled; nothing was cloned") {
		t.Fatalf("backend error was reported as cancellation: %q", out.String())
	}
}

func TestRepoClonePickerRejectsSourceLessCache(t *testing.T) {
	app, _ := newRepoWizardApp(t, "owner/manual\n")
	remote := forge.RemoteRepo{
		Forge: forge.GitHub, Name: "legacy", FullName: "old-host/legacy",
		CloneURL: "https://old.example.test/old-host/legacy.git",
	}
	if err := forge.SaveCacheState(remoteCachePath(), forge.Cache{
		Version: forge.CacheVersion, FetchedAt: time.Now().UTC(), Complete: true,
		Repos: []forge.RemoteRepo{remote},
	}); err != nil {
		t.Fatal(err)
	}
	app.pickerSelect = func(context.Context, picker.Request) (picker.Result, error) {
		t.Fatal("source-less cache must not seed clone candidates")
		return picker.Result{}, nil
	}
	ref, err := promptRepoCloneReference(app, newPrompter(app))
	if err != nil || ref != "owner/manual" {
		t.Fatalf("ref = %q, err = %v", ref, err)
	}
}

func TestRepoClonePickerMissingCacheKeepsManualPrompt(t *testing.T) {
	app, _ := newRepoWizardApp(t, "owner/manual\n")
	called := false
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		called = true
		return picker.Result{}, nil
	}
	ref, err := promptRepoCloneReference(app, newPrompter(app))
	if err != nil {
		t.Fatal(err)
	}
	if called || ref != "owner/manual" {
		t.Fatalf("picker called = %t, ref = %q", called, ref)
	}
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
	agents, err := os.ReadFile(filepath.Join(destination, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantAgents := scaffold.StarterAgentContract()
	if string(agents) != wantAgents {
		t.Fatalf("wizard AGENTS.md drifted from canonical starter:\n%s", agents)
	}
	gitignore, err := os.ReadFile(filepath.Join(destination, ".gitignore"))
	if err != nil || !strings.Contains(string(gitignore), ".specstory/statistics.json") ||
		strings.Contains(string(gitignore), "\n.specstory/\n") {
		t.Fatalf("wizard .gitignore has unsafe SpecStory policy: %v\n%s", err, gitignore)
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
