package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/cli"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func (h *harness) runRaw(args ...string) (string, string, error) {
	h.t.Helper()
	var out, errBuf bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, &errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func TestCompletionScriptsUseThePublicCommand(t *testing.T) {
	h := newHarness(t)
	for shell, marker := range map[string]string{
		"bash": "__start_dev",
		"zsh":  "#compdef dev",
		"fish": "complete -c dev",
	} {
		out := h.mustRun("completion", shell)
		if !strings.Contains(out, marker) {
			t.Errorf("dev completion %s missing %q", shell, marker)
		}
	}

	if _, _, err := h.run("shell-init", "completion", "zsh"); err == nil {
		t.Fatal("the duplicate shell-init completion command should not exist")
	}
}

func TestDynamicCompletionUsesParsedConfig(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "auth", "--branch", "feat/auth", "--base", "main")

	id := task.MakeID("demo", "feat/auth")
	out, errOut, err := h.runRaw("__complete", "park", "--config", h.configPath, "demo")
	if err != nil {
		t.Fatalf("complete hot task: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, id+"\t") || !strings.Contains(out, "hot · auth · demo · feat/auth") {
		t.Errorf("task completion did not use --config state:\n%s", out)
	}
	h.mustRun("park", id, "--next", "resume completion test")

	out, errOut, err = h.runRaw("__complete", "resume", "--config", h.configPath, "demo")
	if err != nil {
		t.Fatalf("complete warm task: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, id+"\t") || !strings.Contains(out, "warm · auth · demo · feat/auth") {
		t.Errorf("resume completion did not filter lifecycle state:\n%s", out)
	}
	outByTitle, errOut, err := h.runRaw("__complete", "resume", "--config", h.configPath, "auth")
	if err != nil {
		t.Fatalf("complete unique task title: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(outByTitle, "auth\t") {
		t.Errorf("unique title prefix should produce a resolvable candidate:\n%s", outByTitle)
	}
	if !strings.HasSuffix(out, ":4\n") {
		t.Errorf("task completion should disable file completion:\n%s", out)
	}

	out, errOut, err = h.runRaw("__completeNoDesc", "resume", "--config", h.configPath, "demo")
	if err != nil {
		t.Fatalf("complete without descriptions: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, id+"\n") || strings.Contains(out, id+"\t") {
		t.Errorf("__completeNoDesc did not use the parsed config or strip descriptions:\n%s", out)
	}

	out, errOut, err = h.runRaw("__complete", "start", "--config", h.configPath, "dem")
	if err != nil {
		t.Fatalf("complete repo: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "demo\t") {
		t.Errorf("repo completion did not discover demo:\n%s", out)
	}

	out, errOut, err = h.runRaw(
		"__complete", "wt", "open", "--config", h.configPath, "--repo", "demo", "feat",
	)
	if err != nil {
		t.Fatalf("complete worktree: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "feat/auth\t") {
		t.Errorf("worktree completion missing linked branch:\n%s", out)
	}

	out, errOut, err = h.runRaw(
		"__complete", "wt", "open", "--config", h.configPath, "feat/auth", "--repo", "",
	)
	if err != nil {
		t.Fatalf("complete repo flag after branch: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "demo\t") {
		t.Errorf("repo flag completion should work after a positional argument:\n%s", out)
	}

	out, errOut, err = h.runRaw(
		"__complete", "wt", "rm", "--config", h.configPath, "--repo", "demo", "",
	)
	if err != nil {
		t.Fatalf("complete removable worktree: %v\nstderr: %s", err, errOut)
	}
	if strings.Contains(out, "main\t") || !strings.Contains(out, "feat/auth\t") {
		t.Errorf("remove completion should exclude main and include linked worktrees:\n%s", out)
	}
}

func TestCompletionSurvivesInvalidConfig(t *testing.T) {
	h := newHarness(t)
	bad := filepath.Join(h.home, "bad.toml")
	if err := os.WriteFile(bad, []byte("not = [valid"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := h.runRaw("__complete", "--config", bad, "r")
	if err != nil {
		t.Fatalf("static completion should survive invalid config: %v\nstderr: %s", err, errOut)
	}
	for _, want := range []string{"repo\t", "resume\t"} {
		if !strings.Contains(out, want) {
			t.Errorf("static completion missing %q with invalid config:\n%s", want, out)
		}
	}

	out, errOut, err = h.runRaw("__complete", "resume", "--config", bad, "")
	if err != nil {
		t.Fatalf("dynamic completion should degrade quietly: %v\nstderr: %s", err, errOut)
	}
	if out != ":4\n" {
		t.Errorf("invalid config should yield no task candidates, got %q", out)
	}

	out, errOut, err = h.runRaw("--config", bad, "completion", "zsh")
	if err != nil {
		t.Fatalf("script generation should not load config: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "#compdef dev") {
		t.Error("zsh completion script was not generated")
	}
}

func TestEmbeddedAndFixedCompletions(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "help", "park"}, "parking\t"},
		{[]string{"__complete", "gitignore", "py"}, "python\tPython bundled template"},
		{[]string{"__complete", "--runtime", "a"}, "auto"},
		{[]string{"__complete", "--runtime", "t"}, "tmux"},
		{[]string{"__complete", "--runtime", "z"}, "zellij"},
		{[]string{"__complete", "stats", "--source", "w"}, "wakatime"},
		{[]string{"__complete", "stats", "--source", "git,w"}, "git,wakatime"},
		{[]string{"__complete", "ls", "--state", "hot,w"}, "hot,warm"},
	}
	for _, tc := range cases {
		out, errOut, err := h.runRaw(tc.args...)
		if err != nil {
			t.Fatalf("%v: %v\nstderr: %s", tc.args, err, errOut)
		}
		if !strings.Contains(out, tc.want) || !strings.HasSuffix(out, ":4\n") {
			t.Errorf("%v: want %q and no-file directive, got:\n%s", tc.args, tc.want, out)
		}
	}
}

func TestRepoCompletionDisambiguatesDuplicateDisplays(t *testing.T) {
	h := newHarness(t)
	rootA := filepath.Join(h.home, "root-a")
	rootB := filepath.Join(h.home, "root-b")
	first := filepath.Join(rootA, "team", "demo")
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(h.repo.Root, first); err != nil {
		t.Fatal(err)
	}
	h.repo.Root = first

	secondRepo := gittest.New(t)
	second := filepath.Join(rootB, "team", "demo")
	if err := os.MkdirAll(filepath.Dir(second), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(secondRepo.Root, second); err != nil {
		t.Fatal(err)
	}
	secondRepo.Root = second

	body, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body,
		[]byte(`scan_roots = ["`+h.scanRoot+`"]`),
		[]byte(`scan_roots = ["`+rootA+`", "`+rootB+`"]`), 1,
	)
	if err := os.WriteFile(h.configPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := h.runRaw("__complete", "start", "--config", h.configPath, "")
	if err != nil {
		t.Fatalf("complete duplicate repos: %v\nstderr: %s", err, errOut)
	}
	for _, path := range []string{first, second} {
		if !strings.Contains(out, path+"\tteam/demo · ") {
			t.Errorf("duplicate display should use exact path %q:\n%s", path, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "team/demo\t") {
			t.Errorf("ambiguous display name should not be inserted:\n%s", out)
		}
	}
}

func TestStatsCompletionUsesStoredRepoNames(t *testing.T) {
	h := newHarness(t)
	category := filepath.Join(h.scanRoot, "Web")
	if err := os.MkdirAll(category, 0o755); err != nil {
		t.Fatal(err)
	}
	categorized := filepath.Join(category, "api")
	if err := os.Rename(h.repo.Root, categorized); err != nil {
		t.Fatal(err)
	}
	h.repo.Root = categorized

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "stats", "--config", h.configPath, "--repo", ""}, "api\tWeb/api · "},
		{[]string{"__complete", "stats", "clear", "--config", h.configPath, "--repo", ""}, "api\tWeb/api · "},
		{[]string{"__complete", "stats", "backfill", "--config", h.configPath, "--repo", ""}, "Web/api\t"},
	} {
		out, errOut, err := h.runRaw(tc.args...)
		if err != nil {
			t.Fatalf("%v: %v\nstderr: %s", tc.args, err, errOut)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%v: want %q, got:\n%s", tc.args, tc.want, out)
		}
	}
}

func TestNavigationReportsDirectiveFailure(t *testing.T) {
	h := newHarness(t)
	t.Setenv("DEV_SHELL_CD_FD", "999999")
	_, _, err := h.run("repo", "open", "demo", "--runtime", "none")
	if err == nil || !strings.Contains(err.Error(), "shell directory directive") {
		t.Fatalf("navigation should report a broken shell side channel, got %v", err)
	}
}

func TestInjectedVersionIsReported(t *testing.T) {
	original := cli.Version
	cli.Version = "v0.1.0"
	t.Cleanup(func() { cli.Version = original })

	h := newHarness(t)
	out := h.mustRun("--version")
	if !strings.Contains(out, "v0.1.0") {
		t.Errorf("version output did not contain injected version: %q", out)
	}
}
