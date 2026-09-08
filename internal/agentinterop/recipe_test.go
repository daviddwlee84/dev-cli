package agentinterop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecipeRebuildsRelationWithoutMachinePaths(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, ".agents/skills/example/SKILL.md"), skillFixture)
	p, err := s.Plan(context.Background(), skillRequest(root, "mirror"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	recipe, err := s.ExportRecipe(context.Background(), p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recipe), root) || strings.Contains(string(recipe), s.StateDir) || strings.Contains(string(recipe), p.ID) {
		t.Fatal("machine authority leaked into recipe")
	}
	other := filepath.Join(filepath.Dir(root), "clone")
	writeFixture(t, filepath.Join(other, ".agents/skills/example/SKILL.md"), skillFixture)
	writeFixture(t, filepath.Join(other, ".agents/interop.toml"), string(recipe))
	p, err = s.PlanRecipe(context.Background(), other, ".agents/interop.toml", "", "skill")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(other, ".claude/skills/example/SKILL.md"))
	if err != nil || string(data) != skillFixture {
		t.Fatal("recipe did not recreate relative link")
	}
}

func TestRecipeChangesInvalidatePlanAndUnknownFieldsFail(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, "AGENTS.md"), "instructions\n")
	recipe := "version=1\n[transfers.rules]\nkind='instructions'\nmode='mirror'\n[transfers.rules.from]\nscope='project'\n[transfers.rules.to]\nscope='project'\n"
	path := filepath.Join(root, ".agents/interop.toml")
	writeFixture(t, path, recipe)
	p, err := s.PlanRecipe(context.Background(), root, ".agents/interop.toml", "rules", "instructions")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, recipe+"# edited intent\n")
	if _, err = s.Apply(context.Background(), p.ID); err == nil {
		t.Fatal("edited recipe accepted stale plan")
	}
	writeFixture(t, path, recipe+"root='/different/machine'\n")
	if _, err = s.PlanRecipe(context.Background(), root, ".agents/interop.toml", "rules", "instructions"); err == nil {
		t.Fatal("unknown machine root field accepted")
	}
}

func TestUndoRefreshRestoresPriorOwnership(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"server":{"command":"uvx","args":["fixture@1"]}}}`)
	req := TransferRequest{Kind: "mcp", Mode: "mirror", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"server":{"command":"uvx","args":["fixture@2"]}}}`)
	r, err := s.Refresh(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	old, err := s.Inspect(context.Background(), p.ID)
	if err != nil || old.Status != "superseded" {
		t.Fatal("old relation stayed active")
	}
	u, err := s.Undo(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	old, err = s.Inspect(context.Background(), p.ID)
	if err != nil || old.Status != "applied" {
		t.Fatal("previous ownership was not restored")
	}
	if _, err = s.Refresh(context.Background(), p.ID); err != nil {
		t.Fatal("restored relation cannot refresh")
	}
}
