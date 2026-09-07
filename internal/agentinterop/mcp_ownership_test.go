package agentinterop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPStanzaOwnershipSurvivesUnrelatedEditsAndOtherServers(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"one":{"command":"uvx","args":["one@1"]},"two":{"command":"uvx","args":["two@1"]}}}`)
	req := TransferRequest{Kind: "mcp", Mode: "mirror", Name: "one", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	one, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), one.ID); err != nil {
		t.Fatal(err)
	}
	req.Name = "two"
	two, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), two.ID); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, ".codex/config.toml")
	data, _ := os.ReadFile(target)
	writeFixture(t, target, "model='user-edited'\n"+string(data))
	if _, err = s.Refresh(context.Background(), one.ID); err != nil {
		t.Fatal("unrelated client edit invalidated stanza ownership", err)
	}
	u, err := s.Undo(context.Background(), one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	if !strings.Contains(string(data), "model='user-edited'") || !strings.Contains(string(data), "two@1") || strings.Contains(string(data), "one@1") {
		t.Fatal("stanza undo overwrote unrelated settings")
	}
	if _, err = s.Refresh(context.Background(), two.ID); err != nil {
		t.Fatal("another server lost ownership", err)
	}
}

func TestBridgeRepeatedPlanAndNoopRefreshKeepLiveBinding(t *testing.T) {
	s, root, p := bridgeFixture(t)
	st, err := s.open(false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := st.load(context.Background(), p.ID)
	st.close()
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Plan(context.Background(), r.Request)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Fatal("equivalent bridge rewrote its binding")
	}
	if _, err = s.Apply(context.Background(), again.ID); err != nil {
		t.Fatal(err)
	}
	refresh, err := s.Refresh(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refresh.Changes) != 0 {
		t.Fatal("noop refresh rewrote binding")
	}
	if _, err = s.Apply(context.Background(), refresh.ID); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, ".codex", "extra.txt"), "unrelated")
	if _, err = s.prepareLaunch(context.Background(), p.ID); err != nil {
		t.Fatal("live binding invalidated by no-op or unrelated directory file", err)
	}
}

func TestMCPAdoptsEquivalentForeignStanzaExplicitly(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"server":{"command":"uvx"}}}`)
	writeFixture(t, filepath.Join(root, ".codex/config.toml"), "[mcp_servers.server]\ncommand='uvx'\n")
	req := TransferRequest{Kind: "mcp", Mode: "mirror", Name: "server", Adopt: true, From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 0 {
		t.Fatal("adoption changed equivalent bytes")
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
}
