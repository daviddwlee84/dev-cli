package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillTransferCLIPlansBeforeApply(t *testing.T) {
	h := newHarness(t)
	source := filepath.Join(h.repo.Root, ".agents/skills/example")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: example\ndescription: fixture\n---\nTest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := h.mustRun("skill", "transfer", "plan", "example", "--from-repo", "demo", "--to-agent", "claude-code", "--mode", "mirror", "--json")
	var p struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(h.repo.Root, ".claude/skills/example")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("plan changed agent files")
	}
	h.mustRun("skill", "transfer", "apply", "--plan", p.ID, "--json")
	if _, err := os.Readlink(target); err != nil {
		t.Fatal(err)
	}
	output = h.mustRun("skill", "transfer", "undo", p.ID, "--json")
	if err := json.Unmarshal([]byte(output), &p); err != nil {
		t.Fatal(err)
	}
	h.mustRun("skill", "transfer", "apply", "--plan", p.ID)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("undo retained owned link")
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatal("undo deleted canonical skill")
	}
}
