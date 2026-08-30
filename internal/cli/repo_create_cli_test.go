package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

func TestRepoNewExplicitNameKeepsMinimalCompatibility(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("repo", "new", "fresh")
	destination := filepath.Join(h.scanRoot, "fresh")
	if !strings.Contains(out, "created") || !strings.Contains(out, "fresh") {
		t.Fatalf("output = %q", out)
	}
	if body, err := os.ReadFile(filepath.Join(destination, "README.md")); err != nil || string(body) != "# fresh\n" {
		t.Fatalf("README = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("minimal creation added AGENTS.md: %v", err)
	}
	if count, err := gitx.Run(t.Context(), destination, "rev-list", "--count", "HEAD"); err != nil || count != "1" {
		t.Fatalf("commit count = %q, %v", count, err)
	}
}

func TestRepoCreateAliasAndDryRun(t *testing.T) {
	h := newHarness(t)
	destination := filepath.Join(h.scanRoot, "preview")
	out := h.mustRun("repo", "create", "preview", "--dry-run")
	for _, want := range []string{"nothing will be changed", "README.md", "commit     yes", "upstream   local only", "handoff    stay"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("dry-run created destination: %v", err)
	}
}

func TestRepoNewDryRunJSONIncludesWorkflowDecisions(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("repo", "new", "preview-json", "--dry-run", "--json")
	var result struct {
		Operation string `json:"operation"`
		DryRun    bool   `json:"dry_run"`
		Path      string `json:"path"`
		Commit    bool   `json:"commit"`
		Handoff   string `json:"handoff"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("JSON = %v\n%s", err, out)
	}
	if result.Operation != "new" || !result.DryRun || !result.Commit || result.Path == "" || result.Handoff != "stay" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRepoNewRejectsCloneReference(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("repo", "new", "https://github.com/owner/repo.git")
	if err == nil || !strings.Contains(err.Error(), "repo clone") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepoCloneRejectsSetupOnlyFlagsWithoutPreset(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("repo", "clone", h.repo.Root, "--browse-skills")
	if err == nil || !strings.Contains(err.Error(), "require --preset") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepoCloneJSONRejectsInteractiveSkillBrowserBeforeMutation(t *testing.T) {
	h := newHarness(t)
	destination := filepath.Join(h.scanRoot, "clone-json-browser")
	_, _, err := h.run("repo", "clone", h.repo.Root, "--path", destination,
		"--preset", "agent-ready", "--browse-skills", "--yes", "--json")
	if err == nil || !strings.Contains(err.Error(), "--json cannot") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("invalid clone request mutated destination: %v", statErr)
	}
}

func TestRepoNewRejectsNonInteractiveStartHandoffBeforeMutation(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("repo", "new", "later", "--handoff", "start")
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.scanRoot, "later")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid handoff created repository: %v", statErr)
	}
}

func TestRepoCloneLocalThenSetupAgentReady(t *testing.T) {
	h := newHarness(t)
	destination := filepath.Join(h.scanRoot, "cloned")
	out := h.mustRun("repo", "clone", h.repo.Root, "--path", destination)
	if !strings.Contains(out, "cloned") {
		t.Fatalf("clone output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); err != nil {
		t.Fatalf("clone missing git metadata: %v", err)
	}

	out = h.mustRun("repo", "setup", destination, "--preset", "agent-ready")
	if !strings.Contains(out, "set up") || !strings.Contains(out, "agent-ready") {
		t.Fatalf("setup output = %q", out)
	}
	for _, path := range []string{"AGENTS.md", ".gitignore", filepath.Join(".claude", "settings.json")} {
		if _, err := os.Stat(filepath.Join(destination, path)); err != nil {
			t.Fatalf("setup missing %s: %v", path, err)
		}
	}
	status, err := gitx.StatusOf(t.Context(), destination)
	if err != nil || !status.Dirty() {
		t.Fatalf("setup should leave reviewable changes: %+v, %v", status, err)
	}
}

func TestRepoNewJSONIsPureAndStructured(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("repo", "new", "json-demo", "--json")
	var result struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Created   bool   `json:"created"`
		Commit    bool   `json:"committed"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("JSON = %v\n%s", err, out)
	}
	if result.Operation != "new" || !result.Created || !result.Commit || result.Path == "" {
		t.Fatalf("result = %+v", result)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if hooks, exists := raw["hooks"]; exists {
		t.Fatalf("hook-free result should omit hooks, got %#v", hooks)
	}
}

func TestRepoCloneSetupFailureReportsPreservedCheckout(t *testing.T) {
	h := newHarness(t)
	destination := filepath.Join(h.scanRoot, "clone-kept")
	_, _, err := h.run("repo", "clone", h.repo.Root, "--path", destination,
		"--preset", "missing-preset", "--yes")
	if err == nil || !strings.Contains(err.Error(), "clone is ready at") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination, ".git")); statErr != nil {
		t.Fatalf("checkout was not preserved: %v", statErr)
	}
}

func TestRepoNewPresetFeatureOverridesDoNotGetRecreatedNatively(t *testing.T) {
	h := newHarness(t)
	h.mustRun("repo", "new", "no-claude", "--preset", "agent-ready",
		"--disable", "claude-settings", "--disable", "claude-plans-directory")
	destination := filepath.Join(h.scanRoot, "no-claude")
	if _, err := os.Stat(filepath.Join(destination, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("disabled Claude settings were recreated: %v", err)
	}
}

func TestRepoSetupRequiresAndRecordsProjectConfigTrust(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.repo.Root, ".dev-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	project := "version = 1\n[repo.setup]\npreset = \"trusted\"\n"
	scaffolds := `version = 1
[presets.trusted]
extends = "minimal"
readme = false
initial_commit = false

[[presets.trusted.hooks]]
id = "trusted-hook"
phase = "before_commit"
command = ["sh", "-c", "printf trusted > generated-by-hook.txt"]
required = true
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scaffolds.toml"), []byte(scaffolds), 0o644); err != nil {
		t.Fatal(err)
	}
	h.repo.Git("add", ".dev-cli")
	h.repo.Git("commit", "-m", "chore: configure dev")
	shown := h.mustRun("config", "show", "demo", "--project")
	if !strings.Contains(shown, "[repo.setup]") || !strings.Contains(shown, `preset = "trusted"`) {
		t.Fatalf("project config show:\n%s", shown)
	}
	configPath := strings.TrimSpace(h.mustRun("config", "path", "demo", "--project"))
	wantConfigPath, _ := pathx.Canonical(filepath.Join(h.repo.Root, ".dev-cli", "config.toml"))
	if configPath != wantConfigPath {
		t.Fatalf("project config path = %q", configPath)
	}

	_, _, err := h.run("repo", "setup", "demo", "--preset", "trusted", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("untrusted setup error = %v", err)
	}
	h.mustRun("config", "trust", "demo", "--yes")
	h.mustRun("repo", "setup", "demo", "--preset", "trusted", "--yes")
	if body, err := os.ReadFile(filepath.Join(h.repo.Root, "generated-by-hook.txt")); err != nil || string(body) != "trusted" {
		t.Fatalf("hook output = %q, %v", body, err)
	}
}

func TestRepoSetupRequiresTrustForProjectSelectedInheritedHook(t *testing.T) {
	h := newHarness(t)
	global := filepath.Join(h.home, "global-scaffolds.toml")
	if err := os.WriteFile(global, []byte(`
version = 1
[presets.global-exec]
extends = "minimal"
[[presets.global-exec.hooks]]
id = "inherited-hook"
phase = "before_commit"
command = ["sh", "-c", "printf inherited > inherited-hook.txt"]
required = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.repo.Root, ".dev-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scaffolds.toml"), []byte(`
version = 1
default_preset = "project"
[presets.project]
extends = "global-exec"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.repo.Git("add", ".dev-cli")
	h.repo.Git("commit", "-m", "chore: select project setup")

	_, _, err := h.run("--scaffolds", global, "repo", "setup", "demo", "--preset", "project", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("project-selected inherited hook was not trust-gated: %v", err)
	}
	h.mustRun("config", "trust", "demo", "--yes")
	h.mustRun("--scaffolds", global, "repo", "setup", "demo", "--preset", "project", "--yes")
	if body, err := os.ReadFile(filepath.Join(h.repo.Root, "inherited-hook.txt")); err != nil || string(body) != "inherited" {
		t.Fatalf("inherited hook output = %q, %v", body, err)
	}
}

func TestRepoSetupRequiresTrustForProjectFileExecutedByInheritedHook(t *testing.T) {
	h := newHarness(t)
	global := filepath.Join(h.home, "global-file-scaffolds.toml")
	if err := os.WriteFile(global, []byte(`
version = 1
[presets.global-exec]
extends = "minimal"
[[presets.global-exec.files]]
id = "hook-script"
destination = "generated/hook.sh"
content = "printf global > inherited-file-hook.txt\n"
[[presets.global-exec.hooks]]
id = "inherited-hook"
phase = "before_commit"
command = ["sh", "generated/hook.sh"]
required = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.repo.Root, ".dev-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scaffolds.toml"), []byte(`
version = 1
[presets.global-exec]
[[presets.global-exec.files]]
id = "hook-script"
content = "printf project > inherited-file-hook.txt\n"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.repo.Git("add", ".dev-cli")
	h.repo.Git("commit", "-m", "chore: override inherited hook script")

	_, _, err := h.run("--scaffolds", global, "repo", "setup", "demo", "--preset", "global-exec", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("project file used by inherited hook was not trust-gated: %v", err)
	}
	h.mustRun("config", "trust", "demo", "--yes")
	h.mustRun("--scaffolds", global, "repo", "setup", "demo", "--preset", "global-exec", "--yes")
	if body, err := os.ReadFile(filepath.Join(h.repo.Root, "inherited-file-hook.txt")); err != nil || string(body) != "project" {
		t.Fatalf("inherited file hook output = %q, %v", body, err)
	}
}

func TestRepoNewPublishesThroughReadyGitHubCLI(t *testing.T) {
	h := newHarness(t)
	bin := t.TempDir()
	gh := filepath.Join(bin, "gh")
	script := `#!/bin/sh
set -eu
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit 0
fi
if [ "$1" = "repo" ] && [ "$2" = "create" ]; then
  printf 'https://github.com/acme/published\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := h.mustRun("repo", "new", "published", "--remote", "--forge", "github", "--namespace", "acme", "--push=false")
	if !strings.Contains(out, "https://github.com/acme/published") {
		t.Fatalf("output = %q", out)
	}
	destination := filepath.Join(h.scanRoot, "published")
	if remote := gitx.Remote(t.Context(), destination, "origin"); remote != "https://github.com/acme/published.git" {
		t.Fatalf("origin = %q", remote)
	}
}

func TestConfigScaffoldsInitShowAndPath(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.home, "custom-scaffolds.toml")
	h.mustRun("--scaffolds", path, "config", "scaffolds", "init")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(h.mustRun("--scaffolds", path, "config", "scaffolds", "path")); got != path {
		t.Fatalf("path = %q", got)
	}
	out := h.mustRun("--scaffolds", path, "config", "scaffolds", "show")
	if !strings.Contains(out, "[presets.minimal]") || !strings.Contains(out, "[presets.agent-ready]") {
		t.Fatalf("show output:\n%s", out)
	}
	if _, err := scaffold.Decode([]byte(out), "config-scaffolds-show.toml"); err != nil {
		t.Fatalf("effective scaffold output is not reusable: %v\n%s", err, out)
	}
}

func TestWorktreePlanWriteUsesProjectConfigDirectory(t *testing.T) {
	h := newHarness(t)
	h.mustRun("wt", "plan", "--repo", "demo", "--write")
	path := filepath.Join(h.repo.Root, ".dev-cli", "config.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "version = 1") || !strings.Contains(string(body), "[worktree]") {
		t.Fatalf("project config:\n%s", body)
	}
}

func TestRepoNewInstallsSelectedSkillAndRunsDeclaredSetup(t *testing.T) {
	h := newHarness(t)
	bin := t.TempDir()
	provider := filepath.Join(bin, "skills")
	script := `#!/bin/sh
set -eu
case "$1" in
  add)
    mkdir -p "$PWD/.agents/skills/demo/scripts"
    printf '%s\n' '---' 'name: demo' '---' > "$PWD/.agents/skills/demo/SKILL.md"
    printf '%s\n' '#!/bin/sh' 'printf setup > setup-ran.txt' > "$PWD/.agents/skills/demo/scripts/setup.sh"
    chmod +x "$PWD/.agents/skills/demo/scripts/setup.sh"
    ;;
  list)
    printf '[{"name":"demo","path":"%s/.agents/skills/demo","scope":"project","agents":["Codex"],"source":"test/catalog","sourceUrl":null,"sourceType":"github"}]\n' "$PWD"
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(provider, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	scaffoldsPath := filepath.Join(h.home, "skill-scaffolds.toml")
	scaffolds := `version = 1
[presets.skill-test]
extends = "minimal"

[[presets.skill-test.skills]]
id = "demo"
source = "test/catalog"
name = "demo"
agents = ["codex"]
default = true
setup = { phase = "before_commit", interpreter = "sh", script = "scripts/setup.sh", required = true }
`
	if err := os.WriteFile(scaffoldsPath, []byte(scaffolds), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustRun("--scaffolds", scaffoldsPath, "repo", "new", "skill-demo", "--preset", "skill-test", "--yes")
	destination := filepath.Join(h.scanRoot, "skill-demo")
	if body, err := os.ReadFile(filepath.Join(destination, "setup-ran.txt")); err != nil || string(body) != "setup" {
		t.Fatalf("setup output = %q, %v", body, err)
	}
	if _, err := gitx.Run(t.Context(), destination, "cat-file", "-e", "HEAD:setup-ran.txt"); err != nil {
		t.Fatalf("skill setup output was not committed: %v", err)
	}
}

func TestRepoNewRunsGeneratedRepoLocalHookExecutable(t *testing.T) {
	h := newHarness(t)
	scaffoldsPath := filepath.Join(h.home, "local-hook-scaffolds.toml")
	scaffolds := `version = 1
[presets.local-hook]
extends = "minimal"

[[presets.local-hook.files]]
id = "bootstrap-script"
destination = "scripts/bootstrap.sh"
content = "#!/bin/sh\nprintf generated > generated-by-local-hook.txt\n"
mode = "0755"

[[presets.local-hook.hooks]]
id = "bootstrap"
phase = "before_commit"
command = ["./scripts/bootstrap.sh"]
required = true
`
	if err := os.WriteFile(scaffoldsPath, []byte(scaffolds), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustRun("--scaffolds", scaffoldsPath, "repo", "new", "local-hook", "--preset", "local-hook", "--yes")
	body, err := os.ReadFile(filepath.Join(h.scanRoot, "local-hook", "generated-by-local-hook.txt"))
	if err != nil || string(body) != "generated" {
		t.Fatalf("generated hook output = %q, %v", body, err)
	}
}

func TestRepoNewSelectedRecommendedSkillsRunBuiltInInitializers(t *testing.T) {
	h := newHarness(t)
	bin := t.TempDir()
	provider := filepath.Join(bin, "skills")
	providerScript := `#!/bin/sh
set -eu
mkdir -p "$PWD/.agents/skills/agent-history-hygiene" "$PWD/.agents/skills/project-knowledge-harness"
exit 0
`
	if err := os.WriteFile(provider, []byte(providerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(bin, "pre-commit")
	if err := os.WriteFile(preCommit, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h.mustRun("repo", "new", "recommended", "--preset", "agent-ready", "--yes",
		"--enable", "agent-history-hygiene", "--enable", "project-knowledge-harness",
		"--set", "deployment=none", "--agent", "codex")
	destination := filepath.Join(h.scanRoot, "recommended")
	for _, relative := range []string{
		".pre-commit-config.yaml", ".gitleaks.toml", "TODO.md",
		"backlog/README.md", "backlog/inbox.md", "pitfalls/README.md",
	} {
		if _, err := gitx.Run(t.Context(), destination, "cat-file", "-e", "HEAD:"+relative); err != nil {
			t.Fatalf("built-in initializer output %s was not committed: %v", relative, err)
		}
	}
}
