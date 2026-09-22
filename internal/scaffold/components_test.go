package scaffold

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestComponentsComposeIgnoresAndOptionalPythonSkill(t *testing.T) {
	options := PlanOptions{Preset: "agent-ready", Root: filepath.Join(t.TempDir(), "repo"), Components: []string{"python", "node", "python"}}
	plan, err := BuildPlan(Builtins(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Components, []string{"python", "node"}) || !slices.Equal(plan.Settings.Gitignore, []string{"common", "python", "node"}) {
		t.Fatalf("composition = %+v, ignores = %v", plan.Components, plan.Settings.Gitignore)
	}
	if len(plan.Skills) != 0 {
		t.Fatalf("selecting a language implicitly installed skills: %+v", plan.Skills)
	}
	if !slices.ContainsFunc(plan.Catalog, func(item CatalogPlan) bool { return item.ID == "python-project-best-practice" && !item.Selected }) {
		t.Fatal("component skill is missing from the picker catalog")
	}
	if findFilePlan(plan.Files, "claude-plans-directory") != nil {
		t.Fatal("default agent-ready plan contains an eager placeholder")
	}
	options.Selections = map[string]bool{"python-project-best-practice": true, "claude-plans-directory": true}
	plan, err = BuildPlan(Builtins(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Skills) != 1 || plan.Skills[0].Setup != nil || !strings.Contains(plan.Skills[0].Source, "/skills/local/python-project-best-practice") {
		t.Fatalf("Python selection = %+v", plan.Skills)
	}
	if findFilePlan(plan.Files, "claude-plans-directory") == nil {
		t.Fatal("explicit legacy placeholder selection was lost")
	}
}

func TestComponentsConfigurationAndPresetCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scaffolds.toml")
	writeTestFile(t, path, `
version = 1
[presets.team]
extends = "minimal"
components = ["python", "editor"]
[[presets.team.files]]
id = "later"
destination = "later.txt"
content = "later"
default = false
[components.editor]
description = "Our editor"
gitignore = ["Python", "VisualStudioCode"]
[[components.editor.skills]]
id = "style"
name = "style"
source = "owner/team-skills"
default = false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(cfg, PlanOptions{Preset: "team", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Settings.Gitignore, []string{"python", "VisualStudioCode"}) || len(plan.Files) != 1 || plan.Files[0].ID != "readme" {
		t.Fatalf("composed plan = %+v", plan)
	}
	if len(plan.Catalog) != 2 || plan.Catalog[1].ID != "style" || plan.Catalog[1].Origin != path {
		t.Fatalf("catalog should be derived from skills with source provenance: %+v", plan.Catalog)
	}
	for _, selection := range [][]string{{"none"}, {}} {
		cleared, err := cfg.ResolveComposition("team", selection)
		if err != nil || len(cleared.Components) != 0 || len(cleared.Skills) != 0 {
			t.Fatalf("clear %v = %+v, %v", selection, cleared, err)
		}
	}
	replaced, err := cfg.ResolveComposition("team", []string{"go"})
	if err != nil || !slices.Equal(replaced.Gitignore, []string{"go"}) {
		t.Fatalf("explicit replacement = %+v, %v", replaced, err)
	}
}

func TestComponentConflictsFailBeforePlanning(t *testing.T) {
	cfg := Builtins()
	python := cfg.Components["python"]
	cfg.Components["copy"] = cloneComponent(python)
	if _, err := cfg.ResolveComposition("minimal", []string{"python", "copy"}); err != nil {
		t.Fatalf("identical skill id should deduplicate: %v", err)
	}
	copy := cloneComponent(python)
	copy.Skills[0].Source = "different/skills"
	cfg.Components["copy"] = copy
	if _, err := cfg.ResolveComposition("minimal", []string{"python", "copy"}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting source accepted: %v", err)
	}
	copy.Skills[0].ID = "another-id"
	cfg.Components["copy"] = copy
	if _, err := cfg.ResolveComposition("minimal", []string{"python", "copy"}); err == nil || !strings.Contains(err.Error(), "same name") {
		t.Fatalf("overlapping installation accepted: %v", err)
	}
	copy.Skills[0].ID = "readme"
	cfg.Components["copy"] = copy
	if _, err := cfg.ResolveComposition("minimal", []string{"copy"}); err == nil || !strings.Contains(err.Error(), "preset file or hook") {
		t.Fatalf("component skill could toggle the preset README: %v", err)
	}
	for _, names := range [][]string{{"missing"}, {"none", "python"}, {""}} {
		if _, err := cfg.ResolveComposition("minimal", names); err == nil {
			t.Errorf("invalid selection %v accepted", names)
		}
	}
	copy.Skills[0].Setup = &SkillSetup{Builtin: "project-knowledge-harness"}
	cfg.Components["copy"] = copy
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cannot declare setup") {
		t.Fatalf("component setup accepted: %v", err)
	}
}

func TestComponentLayersKeepOriginsAndMergeSkills(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scaffolds.toml")
	writeTestFile(t, path, `
version = 1
[components.python]
description = "My Python"
[[components.python.skills]]
id = "python-project-best-practice"
default = true
agents = ["codex"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(cfg, PlanOptions{Preset: "minimal", Root: t.TempDir(), Components: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Skills) != 1 || plan.Skills[0].Source == "" || plan.Skills[0].Origin != path || !slices.Equal(plan.Skills[0].Agents, []string{"codex"}) {
		t.Fatalf("partial component skill overlay = %+v", plan.Skills)
	}
}

func TestComponentSkillNamesConflictAcrossAgentsSharingNativeStorage(t *testing.T) {
	cfg := Builtins()
	cfg.Components["first"] = Component{Skills: []Skill{{ID: "first-style", Name: "style", Source: "source-a/skills", Agents: []string{"codex"}}}}
	cfg.Components["second"] = Component{Skills: []Skill{{ID: "second-style", Name: "style", Source: "source-b/skills", Agents: []string{"cursor"}}}}
	if _, err := cfg.ResolveComposition("minimal", []string{"first", "second"}); err == nil || !strings.Contains(err.Error(), "same name") {
		t.Fatalf("logical agent separation incorrectly authorized native skill replacement: %v", err)
	}
}
