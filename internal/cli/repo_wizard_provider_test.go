package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

const wizardProviderMarker = `package main
import "os"
func main(){ _ = os.WriteFile(os.Getenv("DEV_WIZARD_PROVIDER_MARKER"), []byte("executed"),0600) }
`

func TestRepoWizardMissingSkillSkipRetainsPythonStageOpen(t *testing.T) {
	input := strings.Join([]string{"quick-market-data", "", "", "", "", "python", "y", "skip", "n", "n", "stage", "open", "y"}, "\n") + "\n"
	app, out := newRepoWizardApp(t, input)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	testutil.GoCommand(t, bin, "npx", wizardProviderMarker)
	testutil.GoCommand(t, bin, "npm", wizardProviderMarker)
	t.Setenv("PATH", bin)
	t.Setenv("DEV_WIZARD_PROVIDER_MARKER", marker)
	request, confirmed, err := runRepoNewWizard(app, repoBootstrapFlags{private: true, push: true, forge: "auto"})
	if err != nil || !confirmed {
		t.Fatalf("confirmed=%v err=%v\n%s", confirmed, err, out)
	}
	if request.CheckIn != repoCheckInStage || request.Handoff != repoHandoffOpen || len(request.Prepared.Plan.Skills) != 0 || !slices.Contains(request.Prepared.Plan.Components, "python") || !slices.Contains(request.Prepared.Plan.Settings.Gitignore, "python") {
		t.Fatalf("lost chosen settings: %+v", request)
	}
	if strings.Contains(out.String(), "Skill agents") || !strings.Contains(out.String(), "Create this repository?") {
		t.Fatalf("incorrect followup prompts: %s", out)
	}
	for _, path := range []string{marker, request.Destination} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preflight created %s: %v", path, err)
		}
	}
}

func TestRepoWizardMissingSkillCancelAndEOF(t *testing.T) {
	for _, answer := range []string{"cancel\n", ""} {
		t.Run(answer, func(t *testing.T) {
			app, out := newRepoWizardApp(t, "cancel-me\n\n\n\n\npython\ny\n"+answer)
			t.Setenv("PATH", t.TempDir())
			_, confirmed, err := runRepoNewWizard(app, repoBootstrapFlags{private: true, push: true, forge: "auto"})
			if confirmed || !errors.Is(err, errPromptCanceled) {
				t.Fatalf("confirmed=%v err=%v", confirmed, err)
			}
			if strings.Contains(out.String(), "Afterwards") {
				t.Fatal("cancellation continued wizard")
			}
			if _, err := os.Stat(filepath.Join(app.Cfg.Paths.ProjectRoot, "cancel-me")); !os.IsNotExist(err) {
				t.Fatal("cancellation created repository")
			}
		})
	}
}

func TestRepoWizardTrustedProviderIsNotExecutedAndUsesTargetRoot(t *testing.T) {
	app, out := newRepoWizardApp(t, "python\ny\n\n")
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	testutil.GoCommand(t, bin, "skills", wizardProviderMarker)
	t.Setenv("PATH", bin)
	t.Setenv("DEV_WIZARD_PROVIDER_MARKER", marker)
	flags := repoBootstrapFlags{}
	preset, err := promptScaffoldComposition(newPrompter(app), scaffold.Builtins(), "agent-ready", t.TempDir(), &flags)
	if err != nil || !scaffoldSkillSelected(preset, flags, "python-project-best-practice") || strings.Contains(out.String(), "skip/cancel") {
		t.Fatalf("selection failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preflight executed skills")
	}
	// The exact target, not the wizard's current directory, owns the trust boundary.
	app.In = strings.NewReader("skip\n")
	keep, err := promptRepoSkillProvider(newPrompter(app), bin, "local provider")
	if err != nil || keep {
		t.Fatalf("accepted provider in target checkout: %v", err)
	}
}

func TestRepoWizardPresetSkillsWithoutCatalogCanBeSkipped(t *testing.T) {
	app, _ := newRepoWizardApp(t, "\nskip\nskip\n")
	t.Setenv("PATH", t.TempDir())
	cfg := scaffold.Builtins()
	selected := true
	preset := cfg.Presets["minimal"]
	preset.Skills = []scaffold.Skill{{ID: "one", Name: "one", Source: "owner/skills", Default: &selected}, {ID: "two", Name: "two", Source: "owner/skills"}}
	cfg.Presets["custom"] = preset
	flags := repoBootstrapFlags{enable: []string{"two"}}
	resolved, err := promptScaffoldComposition(newPrompter(app), cfg, "custom", t.TempDir(), &flags)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if scaffoldSkillSelected(resolved, flags, id) {
			t.Fatal("selected skill not skipped", id)
		}
	}
}

func TestRepoWizardAdditionalBrowserMissingProvider(t *testing.T) {
	app, out := newRepoWizardApp(t, "\n\n\ny\nskip\n")
	t.Setenv("PATH", t.TempDir())
	flags := repoBootstrapFlags{}
	if err := promptScaffoldOptions(newPrompter(app), scaffold.Builtins(), "minimal", t.TempDir(), &flags); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if flags.browseSkills || !strings.Contains(out.String(), "additional skills browser") {
		t.Fatalf("unavailable browser survived: %s", out)
	}
}

func TestRepoWizardNewAndSetupErrorsAreNotCancellation(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		app, out := newRepoWizardApp(t, "bad\n\n\n\n\n\nn\nn\nstage\nstart\n")
		cmd := newRepoNewCmd(app)
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --check-in=commit") {
			t.Fatalf("wrong error: %v\n%s", err, out)
		}
		if strings.Contains(out.String(), "Canceled") {
			t.Fatal("real error swallowed")
		}
	})
	t.Run("setup", func(t *testing.T) {
		app, out := newRepoWizardApp(t, "minimal\n\nn\nstage\nstart\n")
		root := t.TempDir()
		if _, err := gitx.Run(t.Context(), root, "init", "-b", "main"); err != nil {
			t.Fatal(err)
		}
		cmd := newRepoSetupCmd(app)
		cmd.SetArgs([]string{root})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --check-in=commit") {
			t.Fatalf("wrong error: %v\n%s", err, out)
		}
		if strings.Contains(out.String(), "Canceled") {
			t.Fatal("real error swallowed")
		}
	})
}
