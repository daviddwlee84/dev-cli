package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRepoSkillsBatchesMatchingSourceAndAgents(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	provider := filepath.Join(bin, "skills")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SKILLS_TEST_LOG\"\n"
	if err := os.WriteFile(provider, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SKILLS_TEST_LOG", logPath)
	app := &App{In: bytes.NewBuffer(nil), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	result, err := installRepoSkills(t.Context(), app, root, []repoSkillSpec{
		{Name: "one", Source: "owner/catalog", Agents: []string{"codex", "claude-code"}},
		{Name: "two", Source: "owner/catalog", Agents: []string{"codex", "claude-code"}},
		{Name: "three", Source: "other/catalog", Agents: []string{"codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Installed, ",") != "one,two,three" {
		t.Fatalf("installed = %v", result.Installed)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("provider calls = %q", lines)
	}
	if !strings.Contains(lines[0], "--skill one two --agent codex claude-code") {
		t.Fatalf("batched call = %q", lines[0])
	}
}
