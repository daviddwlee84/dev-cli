package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
)

func TestCollectAgentSkillsScansProjectsAndGlobalOnce(t *testing.T) {
	home := isolateSkillCollectorEnvironment(t)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	writeCollectorSkill(t, filepath.Join(first, ".agents", "skills", "shared"), "shared")
	writeCollectorSkill(t, filepath.Join(second, ".agents", "skills", "shared"), "shared")
	writeCollectorSkill(t, filepath.Join(home, ".agents", "skills", "shared"), "shared")
	targets := []agenttarget.Target{
		{RepoName: "first", RepoDisplay: "group/first", RepoPath: first, CheckoutRoot: first, CommonDir: first},
		{RepoName: "second", RepoDisplay: "group/second", RepoPath: second, CheckoutRoot: second, CommonDir: second},
	}

	result, err := CollectAgentSkills(context.Background(), targets, AgentSkillOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 3 {
		t.Fatalf("skills = %d, want two project rows and one global row: %+v", len(result.Skills), result.Skills)
	}
	counts := map[agentskill.Scope]int{}
	repositories := map[string]bool{}
	for _, row := range result.Skills {
		counts[row.Scope]++
		if row.Repository != "" {
			repositories[row.Repository] = true
		}
	}
	if counts[agentskill.ScopeProject] != 2 || counts[agentskill.ScopeGlobal] != 1 {
		t.Fatalf("scope counts = %v", counts)
	}
	if !repositories["group/first"] || !repositories["group/second"] {
		t.Fatalf("repository labels = %v", repositories)
	}
}

func TestCollectAgentSkillsReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CollectAgentSkills(ctx, []agenttarget.Target{{CheckoutRoot: t.TempDir(), CommonDir: t.TempDir()}}, AgentSkillOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func isolateSkillCollectorEnvironment(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	for name, directory := range map[string]string{
		"CODEX_HOME": ".codex", "CLAUDE_CONFIG_DIR": ".claude", "VIBE_HOME": ".vibe",
		"HERMES_HOME": ".hermes", "AUTOHAND_HOME": ".autohand", "GROK_HOME": ".grok",
	} {
		t.Setenv(name, filepath.Join(home, directory))
	}
	return home
}

func writeCollectorSkill(t *testing.T, directory, name string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: collector fixture\n---\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
