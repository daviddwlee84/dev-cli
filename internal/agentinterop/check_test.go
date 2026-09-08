package agentinterop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFakeMCPProcess(t *testing.T) {
	if os.Getenv("DEV_INTEROP_TEST_MODE") != "handshake" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	var req struct {
		Method string `json:"method"`
	}
	if decoder.Decode(&req) != nil || req.Method != "initialize" {
		os.Exit(3)
	}
	if os.Getenv("SERVICE_TOKEN") != "handshake-synthetic" {
		fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"error":{"code":-1}}`)
		os.Exit(4)
	}
	if expected := os.Getenv("EXPECTED_ROOT"); expected != "" {
		cwd, _ := os.Getwd()
		if cwd != expected {
			os.Exit(5)
		}
	}
	fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":%q,\"capabilities\":{},\"serverInfo\":{\"name\":\"fixture\",\"version\":\"1\"}}}\n", mcpProtocol)
	if decoder.Decode(&req) != nil || req.Method != "notifications/initialized" {
		os.Exit(6)
	}
	os.Exit(0)
}

func TestMCPExplicitStdioCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows diagnostic process handoff is unsupported")
	}
	t.Setenv("SERVICE_TOKEN", "handshake-synthetic")
	s, root := testService(t)
	executable, _ := os.Executable()
	doc := map[string]any{"mcpServers": map[string]any{"server": map[string]any{"command": executable, "args": []string{"-test.run=TestFakeMCPProcess"}, "env": map[string]string{"SERVICE_TOKEN": "${SERVICE_TOKEN}", "DEV_INTEROP_TEST_MODE": "handshake"}}}}
	data, _ := json.Marshal(doc)
	writeFixture(t, filepath.Join(root, ".mcp.json"), string(data))
	req := TransferRequest{Kind: "mcp", Mode: "copy", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	result, err := s.Check(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "initialized" || result.Authentication != "not-checked" || result.ClientLoaded != "not-checked" {
		t.Fatal("connection evidence was overstated")
	}
}

func TestMCPHTTPCheckIsExplicitAndDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":%q}}`, mcpProtocol)
		} else {
			if r.Header.Get("MCP-Protocol-Version") != mcpProtocol {
				t.Error("stateless notification omitted negotiated version")
			}
			w.WriteHeader(202)
		}
	}))
	defer server.Close()
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"server":{"type":"http","url":"`+server.URL+`"}}}`)
	req := TransferRequest{Kind: "mcp", Mode: "copy", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}}
	p, err := s.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("static operation contacted server")
	}
	if _, err = s.Check(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected MCP requests")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if err = checkHTTP(context.Background(), mcpDefinition{URL: redirect.URL, Headers: map[string]envValue{}}); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("redirect was followed")
	}
}

func TestLaunchEntrypoint(t *testing.T) {
	id := os.Getenv("DEV_INTEROP_LAUNCH_ID")
	if id == "" {
		return
	}
	s := Service{StateDir: os.Getenv("DEV_INTEROP_LAUNCH_STATE")}
	if s.Launch(context.Background(), id) != nil {
		os.Exit(9)
	}
	os.Exit(0)
}

func TestLauncherHandshakeFromNestedAndWorktreeDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native launcher integration is verified on POSIX")
	}
	for _, kind := range []string{"nested", "submodule", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			s, root := testService(t)
			executable, _ := os.Executable()
			admin := filepath.Join(filepath.Dir(root), "git-admin")
			if err := os.MkdirAll(admin, 0o755); err != nil {
				t.Fatal(err)
			}
			if kind == "worktree" {
				writeFixture(t, filepath.Join(root, ".git"), "gitdir: "+admin+"\n")
			} else {
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			nested := filepath.Join(root, "packages", "nested")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			if kind == "submodule" {
				writeFixture(t, filepath.Join(nested, ".git"), "gitdir: "+admin+"\n")
			}
			doc := map[string]any{"mcpServers": map[string]any{"server": map[string]any{"command": executable, "args": []string{"-test.run=TestFakeMCPProcess"}, "env": map[string]string{"SERVICE_TOKEN": "${INTEROP_LAUNCH_CREDENTIAL}", "DEV_INTEROP_TEST_MODE": "handshake", "EXPECTED_ROOT": root}}}}
			data, _ := json.Marshal(doc)
			writeFixture(t, filepath.Join(root, ".mcp.json"), string(data))
			writeFixture(t, filepath.Join(root, ".claude/settings.local.json"), `{"env":{"INTEROP_LAUNCH_CREDENTIAL":"handshake-synthetic"}}`)
			req := TransferRequest{Kind: "mcp", Mode: "mirror", Name: "server", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "codex"}, EnvFile: ".claude/settings.local.json", Launcher: executable}
			p, err := s.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), p.ID); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=TestLaunchEntrypoint")
			cmd.Dir = nested
			cmd.Env = append(os.Environ(), "DEV_INTEROP_LAUNCH_ID="+p.ID, "DEV_INTEROP_LAUNCH_STATE="+s.StateDir)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_, _ = in.Write(initialization())
			line, err := bufio.NewReader(out).ReadBytes('\n')
			if err != nil {
				_ = cmd.Process.Kill()
				t.Fatal("launcher did not return MCP protocol")
			}
			if err = validateInitialization(line); err != nil {
				t.Fatal(err)
			}
			_, _ = in.Write(initialized)
			_ = in.Close()
			if err = cmd.Wait(); err != nil {
				t.Fatal("launcher/server failed")
			}
		})
	}
}
