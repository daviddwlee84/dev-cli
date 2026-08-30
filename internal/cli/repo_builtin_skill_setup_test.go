package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func TestBootstrapProjectKnowledgeCreatesRepeatSafeHarness(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapProjectKnowledge(root, "docker"); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapProjectKnowledge(root, "docker"); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"TODO.md", "backlog/README.md", "backlog/inbox.md", "pitfalls/README.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(readme), "<!-- project-knowledge-harness:readme-roadmap -->") != 1 {
		t.Fatalf("managed block duplicated or malformed:\n%s", readme)
	}
}

func TestEmbeddedAgentHistoryTemplatesMatchBundledSkill(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	for _, test := range []struct {
		path string
		got  string
	}{
		{path: ".agents/skills/agent-history-hygiene/assets/pre-commit-config.yaml.template", got: agentHistoryPreCommit},
		{path: ".agents/skills/agent-history-hygiene/assets/gitleaks.toml.template", got: agentHistoryGitleaks},
	} {
		want, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(test.path)))
		if err != nil {
			t.Fatal(err)
		}
		if test.got != string(want) {
			t.Fatalf("embedded template drifted from %s", test.path)
		}
	}
}

func TestBootstrapAgentHistoryWritesConfigAndHonorsGlobalHooksPath(t *testing.T) {
	root := t.TempDir()
	if _, err := gitx.Run(t.Context(), root, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), root, "config", "core.hooksPath", filepath.Join(root, "hooks")); err != nil {
		t.Fatal(err)
	}
	app := &App{In: bytes.NewBuffer(nil), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := bootstrapAgentHistoryHygiene(t.Context(), app, root); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".pre-commit-config.yaml", ".gitleaks.toml"} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
}

func TestRunRepoSkillSetupsRunsBuiltinWithoutScript(t *testing.T) {
	root := t.TempDir()
	app := &App{In: bytes.NewBuffer(nil), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	result, err := runRepoSkillSetups(t.Context(), app, root, repoHookBeforeCommit, []repoSkillSpec{{
		Name: "project-knowledge-harness",
		Setup: &repoSkillSetup{
			Phase: repoHookBeforeCommit, Builtin: "project-knowledge-harness",
			Args: []string{"--deployment", "none"}, Required: true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Setup) != 1 || result.Setup[0] != "project-knowledge-harness" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "TODO.md")); err != nil {
		t.Fatalf("built-in setup did not run: %v", err)
	}
}
