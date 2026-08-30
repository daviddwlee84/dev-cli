package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildPlanRendersFilesHooksAndSelectedSkill(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "scaffolds.toml")
	if err := os.Mkdir(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "templates", "service.md.tmpl"), "# {{name}}\nkind={{input.kind}} enabled={{input.enabled}}\n")
	writeTestFile(t, configPath, `
version = 1
default_preset = "service"
default_agents = ["codex"]

[presets.service]
extends = "minimal"

[[presets.service.inputs]]
id = "kind"
type = "choice"
choices = ["api", "worker"]
default = "api"

[[presets.service.inputs]]
id = "enabled"
type = "bool"
default = true

[[presets.service.files]]
id = "service-doc"
source = "service.md.tmpl"
destination = "docs/{{input.kind}}.md"
mode = "0600"

[[presets.service.hooks]]
id = "verify"
phase = "before_commit"
command = ["verify", "{{name}}", "{{input.kind}}"]

[[presets.service.skills]]
id = "setup"
source = "example/skills"
name = "setup"
default = false

[presets.service.skills.setup]
phase = "before_commit"
interpreter = "bash"
script = "scripts/init.sh"
args = ["--target", "{{path}}", "--kind", "{{input.kind}}"]

[[presets.service.catalog]]
id = "setup"
source = "example/skills"
label = "Setup {{input.kind}}"
default = false
`)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	plan, err := BuildPlan(cfg, PlanOptions{
		Root: repo, Name: "orders", Inputs: map[string]any{"kind": "worker", "enabled": "false"},
		Selections: map[string]bool{"setup": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Inputs["enabled"] != false {
		t.Fatalf("bool input = %#v", plan.Inputs["enabled"])
	}
	if len(plan.Files) != 2 {
		t.Fatalf("files = %#v", plan.Files)
	}
	doc := findFilePlan(plan.Files, "service-doc")
	if doc == nil || doc.RelativePath != "docs/worker.md" || doc.Mode != 0o600 {
		t.Fatalf("service file plan = %#v", doc)
	}
	if doc.Content != "# orders\nkind=worker enabled=false\n" {
		t.Fatalf("rendered content = %q", doc.Content)
	}
	if len(plan.Hooks) != 1 || strings.Join(plan.Hooks[0].Command, "|") != "verify|orders|worker" {
		t.Fatalf("hook plan = %#v", plan.Hooks)
	}
	if len(plan.Skills) != 1 || strings.Join(plan.Skills[0].Agents, ",") != "codex" {
		t.Fatalf("skill plan = %#v", plan.Skills)
	}
	if got := plan.Skills[0].Setup.Args; len(got) != 4 || got[1] != plan.Root || got[3] != "worker" {
		t.Fatalf("setup args = %#v", got)
	}
	if len(plan.Catalog) != 1 || !plan.Catalog[0].Selected || plan.Catalog[0].Label != "Setup worker" {
		t.Fatalf("catalog plan = %#v", plan.Catalog)
	}
}

func TestResolveInputsValidatesTypesChoicesRequiredAndUnknowns(t *testing.T) {
	preset := Preset{Inputs: []Input{
		{ID: "name", Type: InputString, Required: boolp(true)},
		{ID: "flag", Type: InputBool, Default: false},
		{ID: "kind", Type: InputChoice, Choices: []string{"a", "b"}, Default: "a"},
	}}
	if _, err := ResolveInputs(preset, nil); err == nil || !strings.Contains(err.Error(), `"name" is required`) {
		t.Fatalf("required error = %v", err)
	}
	if _, err := ResolveInputs(preset, map[string]any{"name": "x", "kind": "c"}); err == nil {
		t.Fatal("invalid choice was accepted")
	}
	if _, err := ResolveInputs(preset, map[string]any{"name": "x", "flag": "not-bool"}); err == nil {
		t.Fatal("invalid bool was accepted")
	}
	if _, err := ResolveInputs(preset, map[string]any{"name": "x", "extra": true}); err == nil {
		t.Fatal("unknown input was accepted")
	}
	got, err := ResolveInputs(preset, map[string]any{"name": "x", "flag": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got["flag"] != true || got["kind"] != "a" {
		t.Fatalf("resolved = %#v", got)
	}
}

func TestBuildPlanRejectsTraversalAbsoluteAndSymlinkEscapes(t *testing.T) {
	for _, destination := range []string{"../outside", `..\outside`, "/tmp/outside", `C:\outside`} {
		t.Run(strings.ReplaceAll(destination, "/", "_"), func(t *testing.T) {
			cfg := configWithInlineFile(destination, "x")
			_, err := BuildPlan(cfg, PlanOptions{Root: filepath.Join(t.TempDir(), "repo")})
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("destination %q error = %v", destination, err)
			}
		})
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows CI")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "docs")); err != nil {
		t.Fatal(err)
	}
	cfg := configWithInlineFile("docs/file.md", "x")
	if _, err := BuildPlan(cfg, PlanOptions{Root: repo}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("destination symlink escape error = %v", err)
	}
}

func TestBuildPlanConfinesTemplateSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows CI")
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.tmpl")
	writeTestFile(t, outside, "outside")
	if err := os.Mkdir(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "templates", "escape.tmpl")); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "scaffolds.toml")
	writeTestFile(t, configPath, `
version = 1
default_preset = "x"
[presets.x]
[[presets.x.files]]
id = "escape"
destination = "file.txt"
source = "templates/escape.tmpl"
`)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(cfg, PlanOptions{Root: filepath.Join(dir, "repo")}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("template symlink escape error = %v", err)
	}
}

func TestFileOverrideKeepsTemplateRelativeToDeclaringSource(t *testing.T) {
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "global")
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(globalDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(globalDir, "templates", "notice.tmpl"), "hello {{name}}\n")
	global := filepath.Join(globalDir, "scaffolds.toml")
	project := filepath.Join(projectDir, "scaffolds.toml")
	writeTestFile(t, global, `
version = 1
default_preset = "x"
[presets.x]
[[presets.x.files]]
id = "notice"
destination = "NOTICE.md"
source = "templates/notice.tmpl"
`)
	writeTestFile(t, project, `
version = 1
[presets.x]
[[presets.x.files]]
id = "notice"
destination = "docs/NOTICE.md"
`)
	cfg, err := Load(global, project)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(cfg, PlanOptions{Root: filepath.Join(dir, "repo"), Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].RelativePath != "docs/NOTICE.md" || plan.Files[0].Content != "hello demo\n" {
		t.Fatalf("plan files = %#v", plan.Files)
	}
	canonicalGlobal, err := filepath.EvalSymlinks(globalDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Files[0].Source, canonicalGlobal+string(filepath.Separator)) {
		t.Fatalf("template source moved to overriding config directory: %q", plan.Files[0].Source)
	}
}

func TestBuildPlanRejectsFinalDestinationSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows CI")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "actual.txt"), "keep")
	if err := os.Symlink("actual.txt", filepath.Join(repo, "file.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(configWithInlineFile("file.txt", "replace"), PlanOptions{Root: repo}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("final symlink error = %v", err)
	}
}

func TestApplyFilesIsIdempotentAndHonorsConflictPolicy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	plan, err := BuildPlan(configWithInlineFile("nested/file.txt", "one\n"), PlanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := ApplyFiles(plan, ExistingError)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != "created" {
		t.Fatalf("results = %#v", results)
	}
	results, err = ApplyFiles(plan, ExistingError)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != "unchanged" {
		t.Fatalf("idempotent result = %#v", results)
	}

	changed := plan
	changed.Files = append([]FilePlan(nil), plan.Files...)
	changed.Files[0].Content = "two\n"
	if _, err := ApplyFiles(changed, ExistingError); err == nil {
		t.Fatal("different existing file did not conflict")
	}
	results, err = ApplyFiles(changed, ExistingSkip)
	if err != nil || results[0].Action != "skipped" {
		t.Fatalf("skip = %#v, %v", results, err)
	}
	results, err = ApplyFiles(changed, ExistingOverwrite)
	if err != nil || results[0].Action != "overwritten" {
		t.Fatalf("overwrite = %#v, %v", results, err)
	}
	data, err := os.ReadFile(changed.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestRenderTemplateRejectsUnknownAndExecutableExpressions(t *testing.T) {
	vars := map[string]any{"name": "demo", "input": map[string]any{"flag": true}}
	got, err := RenderTemplate("{{name}} {{ input.flag }}", vars)
	if err != nil || got != "demo true" {
		t.Fatalf("render = %q, %v", got, err)
	}
	for _, template := range []string{"{{missing}}", "{{name | printf}}", "{{name"} {
		if _, err := RenderTemplate(template, vars); err == nil {
			t.Fatalf("template %q was accepted", template)
		}
	}
}

func configWithInlineFile(destination, content string) Config {
	cfg := Builtins()
	cfg.DefaultPreset = "x"
	cfg.Presets["x"] = Preset{
		Files: []File{{ID: "file", Destination: destination, Content: stringp(content)}},
	}
	return cfg
}

func findFilePlan(files []FilePlan, id string) *FilePlan {
	for i := range files {
		if files[i].ID == id {
			return &files[i]
		}
	}
	return nil
}
