package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepoNewFromLocalTemplateSubdirectory(t *testing.T) {
	h := newHarness(t)
	source := t.TempDir()
	starter := filepath.Join(source, "starters", "service")
	if err := os.MkdirAll(filepath.Join(starter, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(starter, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(starter, "README.md"), []byte("template readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(starter, "bin", "run"), []byte("#!/bin/sh\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(starter, ".git", "private"), []byte("not copied\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := h.mustRun("repo", "new", "templated", "--template", source,
		"--template-subdir", "starters/service", "--json")
	var result struct {
		Path      string `json:"path"`
		Committed bool   `json:"committed"`
		Template  struct {
			Source       string `json:"source"`
			Subdir       string `json:"subdir"`
			AppliedFiles int    `json:"applied_files"`
		} `json:"template"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("JSON = %v\n%s", err, out)
	}
	if result.Path == "" || !result.Committed || result.Template.Source != source ||
		result.Template.Subdir != "starters/service" || result.Template.AppliedFiles != 2 {
		t.Fatalf("result = %+v", result)
	}
	if body, err := os.ReadFile(filepath.Join(result.Path, "README.md")); err != nil || string(body) != "template readme\n" {
		t.Fatalf("README = %q, %v", body, err)
	}
	if info, err := os.Stat(filepath.Join(result.Path, "bin", "run")); err != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("template executable = %+v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, ".git", "private")); !os.IsNotExist(err) {
		t.Fatalf("source Git metadata was copied: %v", err)
	}
}

func TestRepoNewTemplateDryRunJSONIncludesSnapshotWithoutMutation(t *testing.T) {
	h := newHarness(t)
	source := h.repo.Root
	ref := h.repo.Git("rev-parse", "HEAD")
	out := h.mustRun("repo", "new", "template-preview", "--template", source,
		"--template-ref", ref, "--dry-run", "--json")
	var result struct {
		DryRun   bool `json:"dry_run"`
		Template struct {
			Source string `json:"source"`
			Ref    string `json:"ref"`
			Commit string `json:"commit"`
			Files  int    `json:"files"`
		} `json:"template"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("JSON = %v\n%s", err, out)
	}
	if !result.DryRun || result.Template.Source != source || result.Template.Ref != ref ||
		result.Template.Commit != ref || result.Template.Files != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(h.scanRoot, "template-preview")); !os.IsNotExist(err) {
		t.Fatalf("dry run created destination: %v", err)
	}
}

func TestRepoNewTemplateDryRunJSONRedactsPresetURLCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file URL userinfo probe is Unix-specific")
	}
	h := newHarness(t)
	credential := "unit-test-secret"
	source := "file://alice:" + credential + "@localhost" + filepath.ToSlash(h.repo.Root)
	scaffolds := filepath.Join(h.home, "credential-template.toml")
	body := fmt.Sprintf("version = 1\n[presets.credential]\nextends = \"minimal\"\ntemplate = %q\n", source)
	if err := os.WriteFile(scaffolds, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("--scaffolds", scaffolds, "repo", "new", "credential-preview", "--preset", "credential", "--dry-run", "--json")
	if strings.Contains(out, credential) || strings.Contains(out, "alice@") {
		t.Fatalf("dry-run JSON leaked template credentials: %s", out)
	}
}

func TestRepoNewTemplateDryRunHumanSummary(t *testing.T) {
	h := newHarness(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "template.txt"), []byte("template\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("repo", "new", "template-summary", "--template", source, "--dry-run")
	for _, want := range []string{"Template snapshot", source, "1 files, 0 directories", `"template.txt"`, "review them before check-in"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestRepoNewTemplateRejectsUnsafeSourceBeforeMutation(t *testing.T) {
	if testing.Short() {
		t.Skip("uses a filesystem symlink")
	}
	h := newHarness(t)
	source := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "unsafe")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, _, err := h.run("repo", "new", "unsafe-template", "--template", source)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.scanRoot, "unsafe-template")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe template created destination: %v", statErr)
	}
}

func TestRepoNewTemplateRefRequiresTemplateSource(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("repo", "new", "missing-template", "--template-ref", "main")
	if err == nil || !strings.Contains(err.Error(), "require --template") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepoNewTemplateCanComeFromPresetAndCLIFlagsOverrideIt(t *testing.T) {
	h := newHarness(t)
	h.repo.Commit("starters/go/version.txt", "old\n", "test: add old starter")
	old := h.repo.Git("rev-parse", "HEAD")
	h.repo.Commit("starters/go/version.txt", "new\n", "test: update starter")
	h.repo.Commit("starters/rust/version.txt", "rust\n", "test: add alternate starter")
	latest := h.repo.Git("rev-parse", "HEAD")
	scaffolds := filepath.Join(h.home, "template-scaffolds.toml")
	body := fmt.Sprintf(`version = 1
[presets.from-template]
extends = "minimal"
template = %q
template_ref = %q
template_subdir = "starters/go"

[presets.overridden-template]
extends = "minimal"
template = "/missing/template-source"
template_ref = "missing-ref"
template_subdir = "missing/subdir"
`, h.repo.Root, old)
	if err := os.WriteFile(scaffolds, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("--scaffolds", scaffolds, "repo", "new", "preset-template", "--preset", "from-template")
	if got, err := os.ReadFile(filepath.Join(h.scanRoot, "preset-template", "version.txt")); err != nil || string(got) != "old\n" {
		t.Fatalf("preset template version = %q, %v", got, err)
	}

	h.mustRun("--scaffolds", scaffolds, "repo", "new", "overridden-template", "--preset", "overridden-template",
		"--template", h.repo.Root, "--template-ref", latest, "--template-subdir", "starters/rust")
	if got, err := os.ReadFile(filepath.Join(h.scanRoot, "overridden-template", "version.txt")); err != nil || string(got) != "rust\n" {
		t.Fatalf("overridden template version = %q, %v", got, err)
	}
}
