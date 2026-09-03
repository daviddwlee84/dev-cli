package agentskill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	bundledskill "github.com/daviddwlee84/dev-cli/internal/skill"
)

func TestNativeReadsNeverExecuteProvidersOrProjectCode(t *testing.T) {
	home := isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "safe"), "safe", "")
	projectMarker := filepath.Join(t.TempDir(), "project-code-ran")
	writeExecutable(t, filepath.Join(repository, ".agents", "skills", "safe", "run.sh"), "#!/bin/sh\nprintf ran > \""+projectMarker+"\"\n")
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"safe": map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/safe/SKILL.md", "computedHash": strings.Repeat("a", 64)},
	})

	bin := t.TempDir()
	providerMarker := filepath.Join(t.TempDir(), "provider-ran")
	for _, name := range []string{"skills", "npx", "npm"} {
		writeExecutable(t, filepath.Join(bin, name), "#!/bin/sh\nprintf '"+name+" %s' \"$*\" > \""+providerMarker+"\"\nexit 99\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	rows, err := List(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "safe" {
		t.Fatalf("rows = %+v", rows)
	}
	if !Managed(context.Background(), repository, "safe", ScopeProject) {
		t.Error("lock-managed skill was not recognized")
	}
	if _, err := FindProject(context.Background(), repository, "safe"); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{providerMarker, projectMarker} {
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read executed marker %s: %v", marker, err)
		}
	}
	_ = home
}

func TestListMergesScopesAtCurrentCheckout(t *testing.T) {
	home := isolateAgentEnvironment(t)
	repository := initRepository(t)
	nested := filepath.Join(repository, "one", "two")
	mustMkdir(t, nested)
	projectPath := filepath.Join(repository, ".agents", "skills", "shared")
	globalPath := filepath.Join(home, ".agents", "skills", "shared")
	writeSkill(t, projectPath, "shared", "")
	writeSkill(t, globalPath, "shared", "")
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"shared": map[string]any{"source": "owner/project", "sourceType": "github", "skillPath": "skills/shared/SKILL.md", "computedHash": strings.Repeat("a", 64)},
	})
	writeLock(t, GlobalLockPath(), 3, map[string]any{
		"shared": map[string]any{"source": "owner/global", "sourceType": "github", "sourceUrl": "https://github.com/owner/global.git", "skillPath": "skills/shared/SKILL.md", "skillFolderHash": strings.Repeat("b", 40)},
	})

	rows, err := List(context.Background(), nested, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want project and global", rows)
	}
	if rows[0].Scope != ScopeProject || rows[0].ScopeRoot != repository || rows[0].Path != projectPath || rows[0].Checkout != repository {
		t.Errorf("project row = %+v", rows[0])
	}
	if rows[1].Scope != ScopeGlobal || rows[1].Path != globalPath || !contains(rows[1].Agents, "Cline") {
		t.Errorf("global row = %+v", rows[1])
	}
	if contains(rows[1].Attribution.AgentIDs, "promptscript") || contains(rows[1].Attribution.AgentIDs, "codex") {
		t.Errorf("global attribution included project-only or differently rooted agents: %+v", rows[1].Attribution)
	}
	for _, row := range rows {
		if row.ManagedBy != ManagedBySkills || row.Presence != PresencePresent || row.UpdateStatus != UpdateUnchecked {
			t.Errorf("managed row = %+v", row)
		}
		if len(row.Agents) < 2 || row.Attribution.RegistryVersion != RegistryVersion {
			t.Errorf("registry attribution missing: %+v", row)
		}
	}
}

func TestScanImmediateChildrenWithBoundedFrontmatter(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	root := filepath.Join(repository, ".agents", "skills")
	writeSkill(t, filepath.Join(root, "large"), "large", strings.Repeat("body\n", int(maxFrontmatterBytes)))
	writeSkill(t, filepath.Join(root, "container", "nested"), "nested", "")
	mustMkdir(t, filepath.Join(root, "bad"))
	if err := os.WriteFile(filepath.Join(root, "bad", "SKILL.md"), []byte("---\nname: [not, scalar]\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, "no-description"))
	if err := os.WriteFile(filepath.Join(root, "no-description", "SKILL.md"), []byte("---\nname: no-description\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, "oversized"))
	oversized := "---\nname: oversized\ndescription: " + strings.Repeat("x", int(maxFrontmatterBytes)) + "\n---\n"
	if err := os.WriteFile(filepath.Join(root, "oversized", "SKILL.md"), []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Name != "large" {
		t.Fatalf("immediate valid skills = %+v", result.Skills)
	}
	if len(result.Diagnostics) != 3 {
		t.Fatalf("diagnostics = %+v, want malformed, missing-description, and oversized", result.Diagnostics)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Kind != DiagnosticSkillFrontmatter {
			t.Errorf("diagnostic = %+v", diagnostic)
		}
	}
}

func TestScanRejectsTerminalControlNamesAndRedactsSourceCredentials(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "unsafe"), "unsafe\x1b[2J", "")
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "safe"), "safe", "")
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"safe": map[string]any{
			"source":     "https://user:token-value@example.test/catalog.git?token=secret#fragment",
			"sourceUrl":  "https://user:token-value@example.test/catalog.git?token=secret#fragment",
			"sourceType": "git", "skillPath": "skills/safe/SKILL.md",
			"computedHash": strings.Repeat("a", 64),
		},
	})
	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Name != "safe" {
		t.Fatalf("unsafe skill name reached rows: %+v", result.Skills)
	}
	row := result.Skills[0]
	if strings.Contains(row.Source, "token-value") || strings.Contains(row.SourceURL, "token-value") ||
		strings.Contains(row.SourceURL, "?token") || strings.Contains(row.SourceURL, "#fragment") {
		t.Fatalf("source credentials were not redacted: %+v", row)
	}
	if len(result.Diagnostics) == 0 || result.Diagnostics[0].Kind != DiagnosticSkillFrontmatter {
		t.Fatalf("unsafe name diagnostic = %+v", result.Diagnostics)
	}
}

func TestSymlinkedInstallsPreserveLogicalPathsAndDedupePhysicalTree(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	physical := filepath.Join(repository, ".agents", "skills", "demo")
	writeSkill(t, physical, "demo", "")
	claudeRoot := filepath.Join(repository, ".claude", "skills")
	mustMkdir(t, claudeRoot)
	alias := filepath.Join(claudeRoot, "demo")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}

	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || len(result.Skills[0].Installations) != 1 {
		t.Fatalf("skills = %+v", result.Skills)
	}
	installation := result.Skills[0].Installations[0]
	if installation.RealPath != physical {
		t.Errorf("real path = %q, want %q", installation.RealPath, physical)
	}
	wantPaths := map[string]bool{physical: true, alias: true}
	for _, path := range installation.LogicalPaths {
		delete(wantPaths, path)
	}
	if len(wantPaths) != 0 {
		t.Errorf("logical paths = %v, missing %v", installation.LogicalPaths, wantPaths)
	}
	if !contains(installation.AgentIDs, "claude-code") || !contains(installation.AgentIDs, "universal") {
		t.Errorf("registry-compatible agents = %v", installation.AgentIDs)
	}
}

func TestProjectScanIncludesLockedEveSubagentPaths(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeSkill(t, filepath.Join(repository, "agent", "subagents", "reviewer", "skills", "eve-skill"), "eve-skill", "")
	writeSkill(t, filepath.Join(repository, "agent", "subagents", "manual", "skills", "manual-eve"), "manual-eve", "")
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"eve-skill": map[string]any{
			"source": "owner/repo", "sourceType": "github", "skillPath": "skills/eve-skill/SKILL.md",
			"computedHash": strings.Repeat("a", 64), "subagents": []string{"reviewer"},
		},
	})
	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	row := skillsByName(result.Skills)["eve-skill"]
	if row.Presence != PresencePresent || row.ManagedBy != ManagedBySkills || !contains(row.Attribution.AgentIDs, "eve") {
		t.Fatalf("Eve subagent row = %+v", row)
	}
}

func TestNormalizedInstalledNamesRemainDistinct(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	first := filepath.Join(repository, ".agents", "skills", "foo-bar")
	second := filepath.Join(repository, ".agents", "skills", "foo.bar")
	writeSkill(t, first, "foo-bar", "")
	writeSkill(t, second, "foo.bar", "")

	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 2 {
		t.Fatalf("normalized-collision rows = %+v", result.Skills)
	}
	row, err := FindProject(context.Background(), repository, "foo.bar")
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "foo.bar" || row.Path != second {
		t.Fatalf("exact installed match = %+v", row)
	}
}

func TestLockOnlyBundledNameRemainsDirectManaged(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"dev-cli": map[string]any{
			"source": "attacker/repo", "sourceType": "github", "skillPath": "skills/dev-cli/SKILL.md",
			"computedHash": strings.Repeat("a", 64),
		},
	})
	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].ManagedBy != ManagedByDev || result.Skills[0].Lock != nil || CanUpdate(result.Skills[0]) {
		t.Fatalf("lock-only bundled row = %+v", result.Skills)
	}
}

func TestPresenceExternalAndBundledIntegrityAreIndependentFromFreshness(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "external"), "external", "")
	bundledDir := filepath.Join(repository, ".agents", "skills", bundledskill.Name)
	if _, err := bundledskill.Install(bundledDir, false); err != nil {
		t.Fatal(err)
	}
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"missing": map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/missing/SKILL.md", "computedHash": strings.Repeat("a", 64)},
	})

	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	byName := skillsByName(result.Skills)
	if byName["missing"].Presence != PresenceMissing || byName["missing"].ManagedBy != ManagedBySkills || byName["missing"].Agents == nil {
		t.Errorf("missing row = %+v", byName["missing"])
	}
	if byName["external"].Presence != PresencePresent || byName["external"].ManagedBy != ManagedByExternal {
		t.Errorf("external row = %+v", byName["external"])
	}
	dev := byName[bundledskill.Name]
	if dev.ManagedBy != ManagedByDev || dev.Integrity != IntegrityVerified || dev.UpdateStatus != UpdateCurrent {
		t.Errorf("dev row = %+v", dev)
	}
	lookalikeRepository := initRepository(t)
	writeSkill(t, filepath.Join(lookalikeRepository, ".agents", "skills", "lookalike"), "dev_cli", "")
	lookalikeResult, err := Inventory(context.Background(), lookalikeRepository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(lookalikeResult.Skills) != 1 || lookalikeResult.Skills[0].ManagedBy != ManagedByExternal || lookalikeResult.Skills[0].Integrity != IntegrityUnknown {
		t.Errorf("normalized lookalike was treated as bundled: %+v", lookalikeResult.Skills)
	}

	if err := os.WriteFile(filepath.Join(bundledDir, "SKILL.md"), []byte("---\nname: dev-cli\ndescription: drift fixture\n---\ndrift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := skillsByName(result.Skills)[bundledskill.Name]; got.Integrity != IntegrityDrifted || got.UpdateStatus != UpdateAvailable {
		t.Errorf("drifted dev row = %+v", got)
	}
}

func isolateAgentEnvironment(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	mustMkdir(t, home)
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

func initRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "", "git", "init", "-q", repository)
	resolved, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeSkill(t *testing.T, directory, name, body string) {
	t.Helper()
	mustMkdir(t, directory)
	content := "---\nname: " + name + "\ndescription: test skill\n---\n" + body
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLock(t *testing.T, filename string, version int, skills map[string]any) {
	t.Helper()
	mustMkdir(t, filepath.Dir(filename))
	body, err := json.Marshal(map[string]any{"version": version, "skills": skills})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, filename, body string) {
	t.Helper()
	if err := os.WriteFile(filename, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, directory string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, directory, binary string, args ...string) string {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, args, err, output)
	}
	return string(output)
}

func skillsByName(rows []Skill) map[string]Skill {
	result := make(map[string]Skill, len(rows))
	for _, row := range rows {
		result[row.Name] = row
	}
	return result
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
