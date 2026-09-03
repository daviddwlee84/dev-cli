package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestSkillListAllIsNativeAndRepositoryQualified(t *testing.T) {
	h := newHarness(t)
	isolateSkillHomes(t, h.home)
	otherRepo := gittest.New(t)
	otherPath := filepath.Join(h.scanRoot, "other")
	if err := os.Rename(otherRepo.Root, otherPath); err != nil {
		t.Fatal(err)
	}
	otherRepo.Root = otherPath

	writeCLISkill(t, filepath.Join(h.repo.Root, ".agents", "skills", "demo-skill"), "demo-skill")
	writeCLISkill(t, filepath.Join(otherPath, ".agents", "skills", "other-skill"), "other-skill")
	writeCLISkill(t, filepath.Join(h.home, ".agents", "skills", "global-skill"), "global-skill")

	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "provider-ran")
	for _, name := range []string{"skills", "npx", "npm"} {
		writeCLIExecutable(t, filepath.Join(bin, name), "#!/bin/sh\nprintf ran > \""+marker+"\"\nexit 99\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var rows []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun("skill", "list", "--all", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want two project and one global: %+v", len(rows), rows)
	}
	repos := map[string]bool{}
	global := 0
	for _, row := range rows {
		if row["scope"] == "global" {
			global++
			if _, exists := row["repo"]; exists {
				t.Fatalf("global row has repository: %+v", row)
			}
			continue
		}
		repos[row["repo"].(string)] = true
		for _, key := range []string{"repo_path", "checkout", "presence", "integrity", "registry_source", "registry_version", "installations"} {
			if _, ok := row[key]; !ok {
				t.Errorf("project row missing %s: %+v", key, row)
			}
		}
	}
	if global != 1 || !repos["demo"] || !repos["other"] {
		t.Fatalf("global/repositories = %d/%v", global, repos)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native list executed provider: %v", err)
	}

	table := h.mustRun("skill", "list", "--all", "--project")
	for _, want := range []string{"REPO", "demo", "other", "demo-skill", "other-skill"} {
		if !strings.Contains(table, want) {
			t.Errorf("all-project table missing %q:\n%s", want, table)
		}
	}
	if _, _, err := h.run("skill", "list", "--all", "--repo", "demo"); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("--all/--repo error = %v", err)
	}
	if _, _, err := h.run("skill", "list", "--global", "--repo", "demo"); err == nil || !strings.Contains(err.Error(), "require project") {
		t.Fatalf("global target error = %v", err)
	}
}

func TestSkillUpdateRepoUsesSelectedCheckout(t *testing.T) {
	h := newHarness(t)
	isolateSkillHomes(t, h.home)
	otherRepo := gittest.New(t)
	otherPath := filepath.Join(h.scanRoot, "other")
	if err := os.Rename(otherRepo.Root, otherPath); err != nil {
		t.Fatal(err)
	}
	otherRepo.Root = otherPath
	writeCLISkill(t, filepath.Join(otherPath, ".agents", "skills", "shared"), "Shared Skill")
	if err := os.WriteFile(filepath.Join(otherPath, "skills-lock.json"), []byte(`{
  "version": 1,
  "skills": {"shared-skill": {"source": "owner/repo", "sourceType": "github", "skillPath": "skills/shared/SKILL.md", "computedHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	writeCLIExecutable(t, filepath.Join(bin, "skills"), "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output := h.mustRun("skill", "update", "Shared Skill", "--project", "--repo", "other", "--yes")
	if !strings.Contains(output, otherPath+"|update --yes --project shared-skill") {
		t.Fatalf("selected update root = %q", output)
	}
}

func isolateSkillHomes(t *testing.T, home string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	for name, directory := range map[string]string{
		"CODEX_HOME": ".codex", "CLAUDE_CONFIG_DIR": ".claude", "VIBE_HOME": ".vibe",
		"HERMES_HOME": ".hermes", "AUTOHAND_HOME": ".autohand", "GROK_HOME": ".grok",
	} {
		t.Setenv(name, filepath.Join(home, directory))
	}
}

func writeCLISkill(t *testing.T, directory, name string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: CLI fixture\n---\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCLIExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
