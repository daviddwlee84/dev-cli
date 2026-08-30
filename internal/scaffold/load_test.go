package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinsAreConservativeAndResolvable(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultPreset != "minimal" {
		t.Fatalf("default preset = %q, want minimal", cfg.DefaultPreset)
	}
	minimal, err := cfg.ResolvePreset("minimal")
	if err != nil {
		t.Fatal(err)
	}
	if len(minimal.Files) != 1 || minimal.Files[0].ID != "readme" {
		t.Fatalf("minimal files = %#v, want only readme", minimal.Files)
	}
	agentReady, err := cfg.ResolvePreset("agent-ready")
	if err != nil {
		t.Fatal(err)
	}
	if agentReady.Readme == nil || !*agentReady.Readme || agentReady.InitialBranch != "main" {
		t.Fatalf("agent-ready did not inherit minimal settings: %#v", agentReady)
	}
	if len(agentReady.Skills) != 2 || len(agentReady.Catalog) != 2 {
		t.Fatalf("agent-ready skill surface = %d skills, %d catalog items", len(agentReady.Skills), len(agentReady.Catalog))
	}
	for _, skill := range agentReady.Skills {
		if skill.IsDefault() {
			t.Fatalf("built-in skill %q must remain opt-in", skill.ID)
		}
	}
}

func TestLoadLayersPresetsAndMergesItemsByID(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")
	writeTestFile(t, global, `
version = 1
default_preset = "team"
default_agents = ["codex"]

[presets.team]
extends = "minimal"
description = "global team scaffold"

[[presets.team.inputs]]
id = "service"
type = "choice"
choices = ["api", "worker"]
default = "api"

[[presets.team.files]]
id = "notice"
destination = "NOTICE.md"
content = "service={{input.service}}\n"

[[presets.team.hooks]]
id = "verify"
phase = "before_commit"
command = ["tool", "--service", "{{input.service}}"]
required = true

[[presets.team.skills]]
id = "knowledge"
source = "example/skills"
name = "knowledge"
agents = ["claude-code"]
default = true

[presets.team.skills.setup]
phase = "before_commit"
interpreter = "bash"
script = "scripts/init.sh"
args = ["--target", "{{path}}"]
required = true

[[presets.team.catalog]]
id = "knowledge"
source = "example/skills"
label = "Knowledge"
default = true

[[presets.team.catalog]]
id = "history"
source = "example/skills"
label = "History"
default = false
`)
	writeTestFile(t, project, `
version = 1
default_agents = []

[presets.team]
description = "project team scaffold"
handoff = "open"

[[presets.team.files]]
id = "readme"
enabled = false

[[presets.team.files]]
id = "notice"
destination = "docs/NOTICE.md"

[[presets.team.hooks]]
id = "verify"
required = false
timeout = "30s"

[[presets.team.skills]]
id = "knowledge"
agents = []
default = false

[presets.team.skills.setup]
args = ["--target", "{{path}}", "--project-name", "{{name}}"]

[[presets.team.catalog]]
id = "knowledge"
label = "Project knowledge"
default = false

[[presets.team.catalog]]
id = "history"
enabled = false
`)

	cfg, err := Load(global, project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultPreset != "team" {
		t.Fatalf("default preset = %q", cfg.DefaultPreset)
	}
	if cfg.DefaultAgents == nil || len(cfg.DefaultAgents) != 0 {
		t.Fatalf("explicit empty default_agents did not replace global value: %#v", cfg.DefaultAgents)
	}
	resolved, err := cfg.ResolvePreset("team")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Description != "project team scaffold" || resolved.Handoff != "open" {
		t.Fatalf("scalar overlay failed: %#v", resolved)
	}
	if findFile(resolved.Files, "readme") != nil {
		t.Fatal("enabled=false did not remove inherited readme")
	}
	notice := findFile(resolved.Files, "notice")
	if notice == nil || notice.Destination != "docs/NOTICE.md" || notice.Content == nil {
		t.Fatalf("partial file merge lost inherited content: %#v", notice)
	}
	if len(resolved.Hooks) != 1 || resolved.Hooks[0].IsRequired() || resolved.Hooks[0].Timeout.Duration.String() != "30s" {
		t.Fatalf("hook merge failed: %#v", resolved.Hooks)
	}
	if len(resolved.Skills) != 1 || resolved.Skills[0].Setup == nil || len(resolved.Skills[0].Setup.Args) != 4 {
		t.Fatalf("skill/setup merge failed: %#v", resolved.Skills)
	}
	if resolved.Skills[0].IsDefault() || resolved.Skills[0].Agents == nil || len(resolved.Skills[0].Agents) != 0 {
		t.Fatalf("false/empty skill overrides were not retained: %#v", resolved.Skills[0])
	}
	if len(resolved.Catalog) != 1 || resolved.Catalog[0].Label != "Project knowledge" || resolved.Catalog[0].IsDefault() {
		t.Fatalf("catalog merge/disable failed: %#v", resolved.Catalog)
	}
}

func TestLoadRejectsUnsupportedVersionAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	wrongVersion := filepath.Join(dir, "wrong.toml")
	writeTestFile(t, wrongVersion, "version = 2\n")
	if _, err := Load(wrongVersion); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("version error = %v", err)
	}
	unknown := filepath.Join(dir, "unknown.toml")
	writeTestFile(t, unknown, "version = 1\nsecret_host_policy = true\n")
	if _, err := Load(unknown); err == nil || !strings.Contains(err.Error(), "secret_host_policy") {
		t.Fatalf("unknown-key error = %v", err)
	}
}

func TestResolvePresetReportsMissingParentAndCycle(t *testing.T) {
	cfg := Builtins()
	cfg.Presets["missing"] = Preset{Extends: "nope"}
	if _, err := cfg.ResolvePreset("missing"); !errors.Is(err, ErrPresetNotFound) {
		t.Fatalf("missing-parent error = %v", err)
	}
	cfg.Presets["a"] = Preset{Extends: "b"}
	cfg.Presets["b"] = Preset{Extends: "c"}
	cfg.Presets["c"] = Preset{Extends: "a"}
	if _, err := cfg.ResolvePreset("a"); !errors.Is(err, ErrPresetCycle) || !strings.Contains(err.Error(), "a -> b -> c -> a") {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestInitialCheckInOverridesLegacyParentCommit(t *testing.T) {
	cfg := Builtins()
	cfg.Presets["review"] = Preset{Extends: "minimal", InitialCheckIn: "stage"}
	resolved, err := cfg.ResolvePreset("review")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.InitialCheckIn != "stage" || resolved.InitialCommit != nil {
		t.Fatalf("resolved check-in = %q, legacy commit = %#v", resolved.InitialCheckIn, resolved.InitialCommit)
	}
	_, err = Decode([]byte(`
version = 1
default_preset = "bad"
[presets.bad]
initial_check_in = "stage"
initial_commit = true
`), "memory.toml")
	if err == nil || !strings.Contains(err.Error(), "cannot set both") {
		t.Fatalf("conflicting check-in error = %v", err)
	}
}

func TestTemplateSettingsInheritAndRequireSource(t *testing.T) {
	cfg := Builtins()
	cfg.Presets["template-base"] = Preset{
		Extends: "minimal", Template: "owner/starters", TemplateRef: "v2",
	}
	cfg.Presets["go-service"] = Preset{
		Extends: "template-base", TemplateSubdir: "services/go",
	}
	resolved, err := cfg.ResolvePreset("go-service")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Template != "owner/starters" || resolved.TemplateRef != "v2" || resolved.TemplateSubdir != "services/go" {
		t.Fatalf("resolved template = %#v", resolved)
	}

	cfg.Presets["missing-source"] = Preset{Extends: "minimal", TemplateRef: "main"}
	if _, err := cfg.ResolvePreset("missing-source"); err == nil || !strings.Contains(err.Error(), "require template") {
		t.Fatalf("missing template source error = %v", err)
	}
}

func TestDecodeRejectsDuplicateItemIDs(t *testing.T) {
	_, err := Decode([]byte(`
version = 1
[presets.x]
[[presets.x.files]]
id = "same"
enabled = false
[[presets.x.files]]
id = "same"
enabled = false
`), "memory.toml")
	if err == nil || !strings.Contains(err.Error(), `repeats file id "same"`) {
		t.Fatalf("duplicate-id error = %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findFile(files []File, id string) *File {
	for i := range files {
		if files[i].ID == id {
			return &files[i]
		}
	}
	return nil
}
