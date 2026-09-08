package agentinterop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bridgeFixture(t *testing.T) (Service, string, Plan) {
	t.Helper()
	s, root := testService(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"mcpServers": map[string]any{"server": map[string]any{"command": executable, "env": map[string]string{"SERVICE_TOKEN": "${INTEROP_FIXTURE_CREDENTIAL}", "EMPTY": "${INTEROP_FIXTURE_EMPTY:-fallback}", "PORT": "${INTEROP_FIXTURE_PORT:-1234}"}}}}
	data, _ := json.Marshal(doc)
	writeFixture(t, filepath.Join(root, ".mcp.json"), string(data))
	writeFixture(t, filepath.Join(root, ".claude/settings.local.json"), `{"env":{"INTEROP_FIXTURE_CREDENTIAL":"first-synthetic-value","UNRELATED_PROVIDER_KEY":"unrelated-synthetic-value"}}`)
	req := TransferRequest{Kind: "mcp", Mode: "mirror", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}, EnvFile: ".claude/settings.local.json", Launcher: executable}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	return s, root, p
}

func TestLauncherResolvesOnlySelectedEnvironmentAndRotation(t *testing.T) {
	t.Setenv("INTEROP_FIXTURE_EMPTY", "")
	t.Setenv("UNRELATED_INHERITED_KEY", "not-for-server")
	s, root, p := bridgeFixture(t)
	for _, token := range []string{"first-synthetic-value", "rotated-synthetic-value"} {
		writeFixture(t, filepath.Join(root, ".claude/settings.local.json"), `{"env":{"INTEROP_FIXTURE_CREDENTIAL":"`+token+`","UNRELATED_PROVIDER_KEY":"unrelated-synthetic-value"}}`)
		spec, err := s.prepareLaunch(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(spec.Env, "\n")
		if !strings.Contains(joined, "SERVICE_TOKEN="+token) || !strings.Contains(joined, "EMPTY=\n") || !strings.Contains(joined, "PORT=1234") {
			t.Fatal("reference/default/empty semantics changed")
		}
		if strings.Contains(joined, "UNRELATED") {
			t.Fatal("unrelated credentials forwarded")
		}
		if spec.Dir != root {
			t.Fatal("launcher root depends on cwd")
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, ".codex/config.toml"))
	if strings.Contains(string(data), "synthetic-value") {
		t.Fatal("credential embedded in target")
	}
	entries, _ := os.ReadDir(s.StateDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(s.StateDir, e.Name()))
		if strings.Contains(string(data), "first-synthetic-value") || strings.Contains(string(data), "rotated-synthetic-value") {
			t.Fatal("resolved source credential persisted")
		}
	}
}

func TestLauncherRejectsChangedDefinitionUnsafeSecretFileAndMissingReference(t *testing.T) {
	for _, kind := range []string{"definition", "permissions", "missing", "escape"} {
		t.Run(kind, func(t *testing.T) {
			s, root, p := bridgeFixture(t)
			path := filepath.Join(root, ".claude/settings.local.json")
			switch kind {
			case "definition":
				writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"server":{"command":"changed"}}}`)
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing":
				writeFixture(t, path, `{"env":{}}`)
			case "escape":
				_ = os.Remove(path)
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
					t.Skip(err)
				}
			}
			_, err := s.prepareLaunch(context.Background(), p.ID)
			if err == nil {
				t.Fatal("invalid launch accepted")
			}
			if strings.Contains(err.Error(), "synthetic-value") {
				t.Fatal("diagnostics leaked credential")
			}
		})
	}
}
