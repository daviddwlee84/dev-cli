package claudeplan_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/claudeplan"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

func TestSetupCreatesAndMergesSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := claudeplan.Setup(t.Context(), root, claudeplan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SettingsEdit || result.SettingsMade {
		t.Fatalf("result = %+v", result)
	}
	body, _ := os.ReadFile(result.SettingsPath)
	var settings map[string]any
	if json.Unmarshal(body, &settings) != nil || settings["model"] != "opus" || settings["plansDirectory"] != "./.claude/plans" {
		t.Fatalf("settings = %s", body)
	}
}

func TestSetupImportsOnlyAuthoritativeOrphanPaths(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := pathx.Canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".claude", "plans")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(global, "wanted.md")
	ignored := filepath.Join(global, "mentioned.md")
	_ = os.WriteFile(wanted, []byte("wanted\n"), 0o644)
	_ = os.WriteFile(ignored, []byte("ignored\n"), 0o644)
	encoded := strings.NewReplacer("/", "-", ".", "-").Replace(canonicalRoot)
	sessions := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := `{"toolUseResult":{"filePath":` + quote(wanted) + `}}` + "\n" +
		`{"message":{"content":"mentioned ` + ignored + ` in chat"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "session.jsonl"), []byte(rows), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := claudeplan.Setup(t.Context(), root, claudeplan.Options{Home: home, ImportOrphans: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 1 || filepath.Base(result.Imported[0]) != "wanted.md" {
		t.Fatalf("imported = %v", result.Imported)
	}
	if _, err := os.Stat(filepath.Join(result.PlansPath, "mentioned.md")); !os.IsNotExist(err) {
		t.Fatalf("chat mention was imported: %v", err)
	}
}

func TestSetupRejectsInvalidExistingJSON(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{"), 0o644)
	if _, err := claudeplan.Setup(t.Context(), root, claudeplan.Options{}); err == nil {
		t.Fatal("invalid settings should fail without overwrite")
	}
}

func quote(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
