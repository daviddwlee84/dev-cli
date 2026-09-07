package agentinterop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCPAllTwentyConversionDirections(t *testing.T) {
	for _, transport := range []string{"stdio", "streamable-http"} {
		for _, from := range MCPProfiles() {
			for _, to := range MCPProfiles() {
				if from.Agent == to.Agent {
					continue
				}
				t.Run(transport+"/"+from.Agent+"/"+to.Agent, func(t *testing.T) {
					s, root := testService(t)
					d := mcpDefinition{Transport: transport, Command: "uvx", Args: []string{"fixture-mcp@1.0"}, Env: map[string]envValue{"API_TOKEN": {Variable: "API_TOKEN"}}, Headers: map[string]envValue{}}
					if transport == "streamable-http" {
						d.Command = ""
						d.Args = nil
						d.Env = map[string]envValue{}
						d.URL = "https://example.test/mcp"
						d.Headers = map[string]envValue{"Authorization": {Variable: "API_TOKEN", Prefix: "Bearer "}}
					}
					fromRef, e := resolveMCP(ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: from.Agent})
					if e != nil {
						t.Fatal(e)
					}
					toRef, e := resolveMCP(ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: to.Agent})
					if e != nil {
						t.Fatal(e)
					}
					data := nativeMCPFixture(t, from.Agent, transport)
					writeFixture(t, filepath.Join(root, fromRef.Path), string(data))
					var base string
					if to.Agent == "codex" {
						base = "# user settings\nmodel = \"keep-this\"\n"
					} else {
						base = "{\"unrelated\": \"keep-this\"}\n"
					}
					writeFixture(t, filepath.Join(root, toRef.Path), base)
					req := TransferRequest{Kind: "mcp", Mode: "copy", Name: "test.server", From: fromRef, To: toRef, Transport: "streamable-http"}
					p, e := s.Plan(context.Background(), req)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = s.Apply(context.Background(), p.ID); e != nil {
						t.Fatal(e)
					}
					actual, e := os.ReadFile(filepath.Join(root, toRef.Path))
					if e != nil {
						t.Fatal(e)
					}
					if !strings.Contains(string(actual), "keep-this") {
						t.Fatal("unrelated config lost")
					}
					raw, found, e := mcpFields(actual, to.Agent, "test.server")
					if e != nil || !found {
						t.Fatalf("missing converted server: %v", e)
					}
					expected, ok, err := mcpFields(nativeMCPFixture(t, to.Agent, transport), to.Agent, "test.server")
					if err != nil || !ok {
						t.Fatal("invalid handwritten fixture")
					}
					if !fieldEquivalent(raw, anyFields(expected)) {
						t.Fatalf("output differs from documented native fields for %s", to.Agent)
					}
					got, e := parseMCP(to.Agent, raw, "streamable-http")
					if e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(d, got) {
						t.Fatalf("conversion changed semantics: %+v", got)
					}
					p, e = s.Plan(context.Background(), req)
					if e != nil {
						t.Fatal(e)
					}
					if len(p.Changes) != 0 {
						t.Fatal("repeat copy is not a no-op")
					}
				})
			}
		}
	}
}

func nativeMCPFixture(t *testing.T, agent, transport string) []byte {
	t.Helper()
	extension := ".json"
	if agent == "codex" {
		extension = ".toml"
	}
	data, err := os.ReadFile(filepath.Join("testdata", agent+"-"+transport+extension))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTOMLPatchPreservesUnrelatedBytes(t *testing.T) {
	input := []byte("# top\nmodel = \"keep\"\ntext = '''\n[mcp_servers.fake]\n'''\n\n[mcp_servers.\"a.b\"]\ncommand = \"old\"\n\n[mcp_servers.\"a.b\".env]\nDEBUG = \"1\"\n\n# belongs to other\n[mcp_servers.other]\ncommand = \"untouched\"\n")
	out, err := patchTOML(input, "a.b", map[string]any{"command": "new", "args": []string{"a b", "quote\""}}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"# top\nmodel = \"keep\"\ntext = '''\n[mcp_servers.fake]\n'''\n", "# belongs to other\n[mcp_servers.other]\ncommand = \"untouched\"\n"} {
		if !strings.Contains(string(out), keep) {
			t.Fatal("unrelated bytes changed")
		}
	}
	if strings.Contains(string(out), "DEBUG") {
		t.Fatal("old nested server fields remained")
	}
	for _, bad := range []string{`mcp_servers = {x={command="old"}}`, `mcp_servers.x.command = "old"`} {
		if _, err := patchTOML([]byte(bad), "x", map[string]any{"command": "new"}, false); err == nil {
			t.Fatal("unsafe layout accepted")
		}
	}
}

func TestMCPConflictPolicySecretAndRefresh(t *testing.T) {
	s, root := testService(t)
	source := filepath.Join(root, ".mcp.json")
	writeFixture(t, source, `{"mcpServers":{"server":{"command":"uvx","args":["fixture@1"]}}}`)
	req := TransferRequest{Kind: "mcp", Mode: "mirror", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, source, `{"mcpServers":{"server":{"command":"uvx","args":["fixture@2"]}}}`)
	refresh, err := s.Refresh(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, ".codex/config.toml"))
	if strings.Contains(string(before), "fixture@2") {
		t.Fatal("refresh changed files before apply")
	}
	if _, err = s.Apply(context.Background(), refresh.ID); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, ".claude/settings.local.json"), `{"disabledMcpjsonServers":["server"]}`)
	if _, err = s.Plan(context.Background(), req); err == nil {
		t.Fatal("disabled source policy was dropped")
	}
	_ = os.Remove(filepath.Join(root, ".claude/settings.local.json"))
	writeFixture(t, source, `{"mcpServers":{"server":{"command":"uvx","env":{"API_TOKEN":"synthetic-secret"}}}}`)
	_, err = s.Plan(context.Background(), req)
	if !errors.Is(err, ErrCredentials) || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("credential not rejected/redacted")
	}
}

func TestMCPPolicyRevocationBeforeApply(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"server":{"command":"uvx"}}}`)
	req := TransferRequest{Kind: "mcp", Mode: "copy", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, ".claude/settings.local.json"), `{"disabledMcpjsonServers":["server"]}`)
	if r, err := s.Apply(context.Background(), p.ID); err == nil || r.Completed != 0 {
		t.Fatal("revoked policy applied")
	}
}

func TestJSONPatchKeepsCommentsAndRejectsDuplicateKeys(t *testing.T) {
	data := []byte("{\n// preference\n\"theme\":\"dark\",\n\"mcp\":{}\n}\n")
	out, err := patchJSON(data, true, []string{"mcp", "a/b"}, map[string]string{"type": "remote", "url": "https://example.test/mcp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "// preference\n\"theme\":\"dark\"") {
		t.Fatal("comment/format lost")
	}
	if _, err = jsonDocument([]byte(`{"mcp":{},"mcp":{}}`), true); err == nil {
		t.Fatal("duplicate keys accepted")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"command":"uvx","futurePolicy":true}`), &fields)
	if _, err = parseMCP("claude-code", fields, ""); err == nil {
		t.Fatal("unsupported field dropped")
	}
}
