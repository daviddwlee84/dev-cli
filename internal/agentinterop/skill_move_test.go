package agentinterop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderMovePreservesNativeLocksAndUndo(t *testing.T) {
	s, root := testService(t)
	req := setupProvider(t, s, root, SkillsProviderVersion, "exit 91\n")
	req.Mode = "move"
	alias := filepath.Join(root, ".claude/skills/example")
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/example", alias); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	p, err := s.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(alias); !os.IsNotExist(err) {
		t.Fatal("source native projection remained")
	}
	if _, err = os.Stat(filepath.Join(root, ".agents/skills/example")); !os.IsNotExist(err) {
		t.Fatal("source canonical tree remained")
	}
	data, _ := os.ReadFile(filepath.Join(root, "skills-lock.json"))
	var lock map[string]any
	_ = json.Unmarshal(data, &lock)
	if len(lock["skills"].(map[string]any)) != 0 {
		t.Fatal("source membership was not retired")
	}
	data, _ = os.ReadFile(filepath.Join(req.To.Root, "skills-lock.json"))
	if _, _, err = readLock(data, "project", "example"); err != nil {
		t.Fatal(err)
	}
	u, err := s.Undo(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.ReadFile(filepath.Join(alias, "SKILL.md")); err != nil {
		t.Fatal("source projection was not restored")
	}
	if _, err = os.Stat(filepath.Join(req.To.Root, "skills-lock.json")); !os.IsNotExist(err) {
		t.Fatal("undo retained new destination lock")
	}
}

func TestSkillProjectToGlobalMoveAndBack(t *testing.T) {
	s, root := testService(t)
	req := setupProvider(t, s, root, SkillsProviderVersion, "exit 92\n")
	home := filepath.Join(filepath.Dir(root), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "VIBE_HOME", "HERMES_HOME", "AUTOHAND_HOME", "GROK_HOME"} {
		t.Setenv(key, "")
	}
	req.Mode = "move"
	req.To = ArtifactRef{ScopeRef: ScopeRef{"user", home}, Agent: "claude-code"}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".agents/.skill-lock.json"))
	if _, _, err = readLock(data, "user", "example"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.ReadFile(filepath.Join(home, ".claude/skills/example/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	u, err := s.Undo(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, ".agents/skills/example/SKILL.md")); err != nil {
		t.Fatal("scope move undo lost original")
	}
}
