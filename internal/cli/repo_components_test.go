package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

func TestRepoComponentPickerOffersPythonSkillWithoutAdvancedCustomization(t *testing.T) {
	app, out := newRepoWizardApp(t, "python,node\nn\n")
	flags := repoBootstrapFlags{}
	preset, err := promptScaffoldComposition(newPrompter(app), scaffold.Builtins(), "agent-ready", &flags)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(preset.Components, []string{"python", "node"}) || scaffoldSkillSelected(preset, flags, "python-project-best-practice") {
		t.Fatalf("selection = %+v, flags = %+v", preset.Components, flags)
	}
	if !strings.Contains(out.String(), "Install python-project-best-practice?") {
		t.Fatalf("missing derived skill choice: %s", out.String())
	}
	prompt := strings.Index(out.String(), "Install python-project-best-practice?")
	for _, detail := range []string{"Installs skill guidance only; does not run project setup.", "Source: https://github.com/daviddwlee84/agent-skills/tree/main/skills/local/python-project-best-practice"} {
		if index := strings.Index(out.String(), detail); index < 0 || index > prompt {
			t.Errorf("skill detail %q was not shown before confirmation: %s", detail, out.String())
		}
	}
	for _, unwanted := range []string{"Agent history hygiene", "Project knowledge harness", "Skill agents", "Deployment mechanism"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("unselected legacy/setup question %q appeared: %s", unwanted, out.String())
		}
	}
}

func TestRepoLegacySkillPickerPreservesExplicitAndAuthoredChoices(t *testing.T) {
	preset, err := scaffold.Builtins().ResolvePreset("agent-ready")
	if err != nil {
		t.Fatal(err)
	}
	item := preset.Catalog[0]
	if showScaffoldSkillChoice(preset, repoBootstrapFlags{}, item) {
		t.Fatal("untouched built-in legacy skill was suggested")
	}
	if !showScaffoldSkillChoice(preset, repoBootstrapFlags{enable: []string{item.ID}}, item) {
		t.Fatal("explicit legacy selection was hidden")
	}
	preset.Skills[0].Origin = "user-scaffolds.toml"
	if !showScaffoldSkillChoice(preset, repoBootstrapFlags{}, item) {
		t.Fatal("authored inherited skill was hidden")
	}
}

func TestRepoScaffoldComponentsRespectFinalGitignoreOverride(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	request := repoScaffoldRequest{
		Root: filepath.Join(t.TempDir(), "repo"), Name: "repo", Preset: "agent-ready",
		Components: []string{"python", "node"}, Gitignore: []string{"rust"},
	}
	prepared, err := prepareRepoScaffold(app, request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prepared.Init.Gitignore, []string{"rust"}) || !slices.Equal(prepared.Plan.Settings.Gitignore, []string{"rust"}) || len(prepared.Plan.Skills) != 0 {
		t.Fatalf("prepared = %+v", prepared)
	}
	for _, file := range prepared.Plan.Files {
		if strings.HasSuffix(file.RelativePath, ".gitkeep") {
			t.Fatal("default plan created a placeholder")
		}
	}
}

func TestRepoScaffoldAdvancedOptionsOmitUnselectedDeployment(t *testing.T) {
	app, out := newRepoWizardApp(t, "\n\n\n\n\n\n")
	flags := repoBootstrapFlags{}
	if err := promptScaffoldOptions(newPrompter(app), scaffold.Builtins(), "agent-ready", &flags); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Deployment mechanism") || strings.Contains(out.String(), "Skill agents") {
		t.Fatalf("unrelated legacy prompts appeared: %s", out.String())
	}
	for _, enabled := range flags.enable {
		if enabled == "claude-plans-directory" {
			t.Fatal("plan settings opt-in implicitly enabled the placeholder")
		}
	}
}

func TestRepoComponentSuggestionsDoNotNarrowAdditionalSkillsBrowser(t *testing.T) {
	cfg := scaffold.Builtins()
	plan, err := scaffold.BuildPlan(cfg, scaffold.PlanOptions{Preset: "minimal", Root: t.TempDir(), Components: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	prepared := preparedRepoScaffold{Config: cfg, Plan: plan}
	if source := repoBrowseSkillSource(prepared); source != agentskill.DefaultSource {
		t.Fatalf("component redirected additional skill browsing to %q", source)
	}
	preset := cfg.Presets["minimal"]
	preset.Catalog = []scaffold.SkillCatalog{{ID: "team", Source: "owner/team-skills", Label: "Team"}}
	cfg.Presets["minimal"] = preset
	plan, err = scaffold.BuildPlan(cfg, scaffold.PlanOptions{Preset: "minimal", Root: t.TempDir(), Components: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	if source := repoBrowseSkillSource(preparedRepoScaffold{Config: cfg, Plan: plan}); source != "owner/team-skills" {
		t.Fatalf("authored catalog browsing source was lost: %q", source)
	}
}
