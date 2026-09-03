package agentskill

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestRegistrySnapshotHasEveryUniqueAgentID(t *testing.T) {
	got := Registry()
	if len(got) != 77 {
		t.Fatalf("registry count = %d, want 77", len(got))
	}
	ids := make([]string, 0, len(got))
	seen := map[string]bool{}
	for _, agent := range got {
		if seen[agent.ID] {
			t.Fatalf("duplicate agent ID %q", agent.ID)
		}
		seen[agent.ID] = true
		if agent.ProjectSkillsDir == "" {
			t.Errorf("agent %q has no project path", agent.ID)
		}
		if agent.RegistrySource != RegistrySource || agent.RegistryVersion != RegistryVersion {
			t.Errorf("agent %q has unlabelled provenance: %+v", agent.ID, agent)
		}
		ids = append(ids, agent.ID)
	}
	sort.Strings(ids)
	want := strings.Fields(`
		adal aider-desk amp antigravity antigravity-cli astrbot autohand-code augment bob
		claude-code cline codearts-agent codebuddy codemaker codestudio codex command-code
		continue cortex crush cursor deepagents devin dexto droid eve firebender forgecode
		gemini-cli github-copilot goose grok hermes-agent iflow-cli inference-sh jazz junie
		kilo kimchi kimi-code-cli kiro-cli kode lingma loaf mcpjam minimax-code mistral-vibe
		moxby mux neovate ona openclaw opencode openhands pi pochi posit-assistant promptscript
		qoder qoder-cn qwen-code reasonix replit roo rovodev tabnine-cli terramind tinycloud trae
		trae-cn universal warp windsurf zcode zed zencoder zenflow
	`)
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("agent IDs differ\n got: %v\nwant: %v", ids, want)
	}
	if RegistryVersion != "v1.5.23" || RegistrySHA256 != "82ad7a09e3a33b6314baab7414d53c72b64a63e1efae7468eba3ad6c4fb2ea5f" {
		t.Fatalf("registry provenance changed: %s %s", RegistryVersion, RegistrySHA256)
	}
}

func TestRegistryResolvesEveryEnvironmentOverride(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	overrides := map[string]string{
		"XDG_CONFIG_HOME":   filepath.Join(t.TempDir(), "xdg"),
		"CODEX_HOME":        filepath.Join(t.TempDir(), "codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(t.TempDir(), "claude"),
		"VIBE_HOME":         filepath.Join(t.TempDir(), "vibe"),
		"HERMES_HOME":       filepath.Join(t.TempDir(), "hermes"),
		"AUTOHAND_HOME":     filepath.Join(t.TempDir(), "autohand"),
		"GROK_HOME":         filepath.Join(t.TempDir(), "grok"),
	}
	for name, value := range overrides {
		t.Setenv(name, value)
	}
	byID := registryByID(Registry())
	checks := map[string]struct {
		path, environment string
	}{
		"amp":           {filepath.Join(overrides["XDG_CONFIG_HOME"], "agents", "skills"), "XDG_CONFIG_HOME"},
		"codex":         {filepath.Join(overrides["CODEX_HOME"], "skills"), "CODEX_HOME"},
		"claude-code":   {filepath.Join(overrides["CLAUDE_CONFIG_DIR"], "skills"), "CLAUDE_CONFIG_DIR"},
		"mistral-vibe":  {filepath.Join(overrides["VIBE_HOME"], "skills"), "VIBE_HOME"},
		"hermes-agent":  {filepath.Join(overrides["HERMES_HOME"], "skills"), "HERMES_HOME"},
		"autohand-code": {filepath.Join(overrides["AUTOHAND_HOME"], "skills"), "AUTOHAND_HOME"},
		"grok":          {filepath.Join(overrides["GROK_HOME"], "skills"), "GROK_HOME"},
	}
	for id, want := range checks {
		got := byID[id]
		if got.GlobalSkillsDir != want.path || got.GlobalEnvironment != want.environment {
			t.Errorf("%s global path = %q via %q, want %q via %q", id, got.GlobalSkillsDir, got.GlobalEnvironment, want.path, want.environment)
		}
	}
	if byID["eve"].GlobalSkillsDir != "" || byID["promptscript"].GlobalSkillsDir != "" {
		t.Error("project-only agents unexpectedly have global paths")
	}
}

func TestRegistryIgnoresRelativeXDGAndUsesOpenClawFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "VIBE_HOME", "HERMES_HOME", "AUTOHAND_HOME", "GROK_HOME"} {
		t.Setenv(name, "")
	}
	if err := os.MkdirAll(filepath.Join(home, ".clawdbot"), 0o755); err != nil {
		t.Fatal(err)
	}
	byID := registryByID(Registry())
	if got := byID["amp"].GlobalSkillsDir; got != filepath.Join(home, ".config", "agents", "skills") {
		t.Errorf("relative XDG_CONFIG_HOME was used: %q", got)
	}
	if got := byID["openclaw"].GlobalSkillsDir; got != filepath.Join(home, ".clawdbot", "skills") {
		t.Errorf("OpenClaw fallback = %q", got)
	}
}

func TestRegistryDoesNotCreateRelativeGlobalPathsWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	for _, name := range []string{"XDG_CONFIG_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "VIBE_HOME", "HERMES_HOME", "AUTOHAND_HOME", "GROK_HOME"} {
		t.Setenv(name, "")
	}
	for _, definition := range Registry() {
		if definition.GlobalSkillsDir != "" && !filepath.IsAbs(definition.GlobalSkillsDir) {
			t.Errorf("%s global path became relative: %q", definition.ID, definition.GlobalSkillsDir)
		}
	}
	if path := GlobalLockPath(); path != "" {
		t.Fatalf("global lock path without HOME = %q", path)
	}
}

func registryByID(definitions []AgentDefinition) map[string]AgentDefinition {
	result := make(map[string]AgentDefinition, len(definitions))
	for _, definition := range definitions {
		result[definition.ID] = definition
	}
	return result
}
