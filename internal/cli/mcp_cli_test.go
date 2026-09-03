package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPLocalScopeRetainsSharedClaudeSourceFailure(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.WriteFile(filepath.Join(h.home, ".claude.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := h.run("mcp", "list", "--agent", "claude-code", "--scope", "local", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("JSON diagnostics leaked to stderr: %q", stderr)
	}
	var envelope struct {
		Diagnostics []struct {
			Scope string `json:"scope"`
			Code  string `json:"code"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Scope != "local" || envelope.Diagnostics[0].Code != "config_malformed" {
		t.Fatalf("local diagnostics = %+v\n%s", envelope.Diagnostics, stdout)
	}
}

func TestMCPListEmitsSanitizedEnvelopeAndFilters(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CODEX_HOME", filepath.Join(h.home, ".codex"))
	project := `{
  "mcpServers": {
    "project-http": {
      "type": "http",
      "url": "https://user:fixture-secret-must-not-appear@example.test/private?token=fixture-secret-must-not-appear",
      "headers": {"Authorization": "fixture-secret-must-not-appear"}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(h.repo.Root, ".mcp.json"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.repo.Root, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.repo.Root, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"cursor-local":{"command":"/usr/bin/cursor-server","args":["fixture-secret-must-not-appear"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	output := h.mustRun("mcp", "list", "--repo", "demo", "--json")
	if !strings.Contains(output, `"diagnostics":`) {
		t.Fatalf("MCP envelope omitted diagnostics: %s", output)
	}
	if strings.Contains(output, "fixture-secret-must-not-appear") || strings.Contains(output, "/private") {
		t.Fatalf("MCP JSON leaked source values:\n%s", output)
	}
	var envelope struct {
		Servers  []map[string]any `json:"servers"`
		Coverage map[string]any   `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Servers) != 2 || envelope.Coverage["static_declarations_only"] != true {
		t.Fatalf("MCP envelope = %+v", envelope)
	}
	for _, row := range envelope.Servers {
		if row["repo"] != "demo" || row["repo_path"] == "" || row["checkout"] == "" {
			t.Fatalf("repository-qualified MCP row = %+v", row)
		}
	}

	cursorOnly := h.mustRun("mcp", "list", "--repo", "demo", "--agent", "cursor")
	if !strings.Contains(cursorOnly, "cursor-local") || strings.Contains(cursorOnly, "project-http") {
		t.Fatalf("agent filter output:\n%s", cursorOnly)
	}
	for _, want := range []string{"REPO", "SCOPE", "AGENT", "SERVER", "TRANSPORT", "STATE", "TARGET"} {
		if !strings.Contains(cursorOnly, want) {
			t.Errorf("MCP table missing %q:\n%s", want, cursorOnly)
		}
	}

	if err := os.MkdirAll(filepath.Join(h.home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.home, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"user-only":{"command":"cursor"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	userOnly := h.mustRun("mcp", "list", "--agent", "cursor", "--scope", "user", "--json")
	if !strings.Contains(userOnly, "user-only") {
		t.Fatalf("user-only MCP query unexpectedly required project/Git resolution: %s", userOnly)
	}

	if _, _, err := h.run("mcp", "list", "--all", "--repo", "demo"); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("target conflict error = %v", err)
	}
	if _, _, err := h.run("mcp", "list", "--agent", "unknown"); err == nil || !strings.Contains(err.Error(), "unsupported MCP agent") {
		t.Fatalf("agent error = %v", err)
	}
	if _, _, err := h.run("mcp", "list", "--scope", "connected"); err == nil || !strings.Contains(err.Error(), "unsupported MCP scope") {
		t.Fatalf("scope error = %v", err)
	}
}
