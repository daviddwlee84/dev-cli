package agentmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"howett.net/plist"
)

const secretSentinel = "fixture-secret-must-not-appear"

func TestScannerReadsFiveAdaptersWithoutRetainingSecrets(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	repository := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, home)
	mustMkdir(t, repository)

	copyFixture(t, "claude-project.json", filepath.Join(repository, ".mcp.json"))
	mustWrite(t, filepath.Join(repository, ".claude", "settings.local.json"), `{"enableAllProjectMcpServers":true,"disabledMcpjsonServers":["claude-http"]}`, 0o644)
	copyFixture(t, "codex.toml", filepath.Join(repository, ".codex", "config.toml"))
	copyFixture(t, "cursor.json", filepath.Join(repository, ".cursor", "mcp.json"))
	copyFixture(t, "gemini.json", filepath.Join(repository, ".gemini", "settings.json"))
	copyFixture(t, "opencode.jsonc", filepath.Join(repository, "opencode.jsonc"))

	userFixture, err := os.ReadFile(filepath.Join("testdata", "claude-user.json"))
	if err != nil {
		t.Fatal(err)
	}
	var user map[string]any
	if err := json.Unmarshal(userFixture, &user); err != nil {
		t.Fatal(err)
	}
	projects := user["projects"].(map[string]any)
	projects[repository] = projects["PROJECT_PATH"]
	delete(projects, "PROJECT_PATH")
	nested := filepath.Join(repository, "service")
	mustMkdir(t, nested)
	projects[nested] = map[string]any{
		"mcpServers": map[string]any{"claude-nested": map[string]any{"type": "stdio", "command": "nested-server"}},
	}
	writeJSON(t, filepath.Join(home, ".claude.json"), user)

	marker := filepath.Join(t.TempDir(), "command-ran")
	mustWrite(t, filepath.Join(repository, "claude-server"), "#!/bin/sh\nprintf ran > "+marker+"\n", 0o755)

	target := testTarget(repository)
	scanner := NewScanner(testOptions(home, repository))
	result, err := scanner.Scan(context.Background(), []Target{target})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Declarations) != 15 {
		t.Fatalf("declarations = %d, want 15: %+v", len(result.Declarations), result.Declarations)
	}
	if len(result.Coverage.Agents) != 5 || !result.Coverage.StaticDeclarationsOnly {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured command was executed: %v", err)
	}

	assertDeclaration(t, result.Declarations, AgentClaudeCode, ScopeProject, "claude-stdio", func(row Declaration) {
		if row.Transport != TransportStdio || row.Command != "claude-server" || row.ArgumentCount != 2 || row.Enabled == nil || !*row.Enabled {
			t.Fatalf("Claude stdio row = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentClaudeCode, ScopeProject, "claude-http", func(row Declaration) {
		if row.Enabled == nil || *row.Enabled {
			t.Fatalf("Claude project rejection state = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentClaudeCode, ScopeLocal, "claude-local", func(row Declaration) {
		if row.Transport != TransportWebSocket || row.Enabled == nil || !*row.Enabled {
			t.Fatalf("Claude local row = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentClaudeCode, ScopeLocal, "claude-nested", func(row Declaration) {
		if row.Repository != "group/demo" || row.Checkout != repository {
			t.Fatalf("Claude nested row = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentCodex, ScopeProject, "codex_http", func(row Declaration) {
		if row.Transport != TransportStreamableHTTP || row.Endpoint != "https://codex.example.test/[path]" {
			t.Fatalf("Codex HTTP row = %+v", row)
		}
		hasHelper := false
		for _, credential := range row.Credentials {
			hasHelper = hasHelper || credential.Kind == CredentialHelper
		}
		if !hasHelper || !containsRedaction(row.Redactions, RedactionHelperCommand) {
			t.Fatalf("Codex header helper was not represented safely: %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentCodex, ScopeProject, "plugin_server", func(row Declaration) {
		if row.Source != SourcePlugin || row.Plugin != "demo" || row.Transport != TransportUnknown || row.Enabled == nil || *row.Enabled {
			t.Fatalf("Codex plugin row = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentCursor, ScopeProject, "cursor-remote", func(row Declaration) {
		if row.Transport != TransportRemote || row.Endpoint != "https://cursor.example.test/[path]" {
			t.Fatalf("Cursor remote row = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentGeminiCLI, ScopeProject, "gemini-http", func(row Declaration) {
		if row.Transport != TransportStreamableHTTP || row.Trusted == nil || !*row.Trusted {
			t.Fatalf("Gemini HTTP row = %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentOpenCode, ScopeProject, "opencode-local", func(row Declaration) {
		if row.Transport != TransportLocal || row.Command != "opencode-server" || row.ArgumentCount != 2 {
			t.Fatalf("OpenCode local row = %+v", row)
		}
	})

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secretSentinel, "--token", "user:" + secretSentinel} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("normalized result leaked %q:\n%s", forbidden, encoded)
		}
	}
	for _, row := range result.Declarations {
		if row.Scope == ScopeProject && (row.Repository != "group/demo" || row.RepositoryPath != repository || row.Checkout != repository) {
			t.Fatalf("project identity lost: %+v", row)
		}
	}
}

func TestScannerReadsOpenCodeDotDirectoryConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	repository := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, home)
	mustWrite(t, filepath.Join(repository, ".opencode", "opencode.jsonc"), `{"mcp":{"nested":{"type":"local","command":["server"]}}}`, 0o644)
	result, err := NewScanner(testOptions(home, repository)).Scan(context.Background(), []Target{testTarget(repository)})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaration(t, result.Declarations, AgentOpenCode, ScopeProject, "nested", func(row Declaration) {})
}

func TestScannerKeepsPartialRowsForSourceFailures(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	repository := filepath.Join(t.TempDir(), "repo")
	outside := filepath.Join(t.TempDir(), "outside.json")
	mustMkdir(t, home)
	mustMkdir(t, repository)
	mustWrite(t, outside, `{"mcpServers":{"outside":{"command":"never"}}}`, 0o644)
	if err := os.Symlink(outside, filepath.Join(repository, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repository, ".codex", "config.toml"), "[mcp_servers.bad\nvalue = \""+secretSentinel+"\"", 0o644)
	mustWrite(t, filepath.Join(repository, ".gemini", "settings.json"), `{"mcpServers":{"valid":{"command":"gemini-server"}}}`, 0o644)
	mustWrite(t, filepath.Join(repository, "opencode.json"), `{"mcp":{"bad":`, 0o644)
	mustWrite(t, filepath.Join(home, ".cursor", "mcp.json"), strings.Repeat("x", 300), 0o644)

	options := testOptions(home, repository)
	options.MaxFileBytes = 256
	result, err := NewScanner(options).Scan(context.Background(), []Target{testTarget(repository)})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaration(t, result.Declarations, AgentGeminiCLI, ScopeProject, "valid", func(row Declaration) {})
	codes := map[DiagnosticCode]bool{}
	for _, diagnostic := range result.Diagnostics {
		codes[diagnostic.Code] = true
	}
	for _, want := range []DiagnosticCode{DiagnosticProjectSymlinkEscape, DiagnosticMalformed, DiagnosticTooLarge} {
		if !codes[want] {
			t.Errorf("missing diagnostic %q: %+v", want, result.Diagnostics)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretSentinel) {
		t.Fatalf("diagnostic leaked malformed source: %s", encoded)
	}
}

func TestClaudeNestedDiagnosticsRetainLocalScopeAndRepository(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	repository := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, repository)
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{
		"projects": map[string]any{repository: "malformed-local-project"},
	})
	result, err := NewScanner(testOptions(home, repository)).Scan(context.Background(), []Target{testTarget(repository)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Scope != ScopeLocal ||
		result.Diagnostics[0].Repository != "group/demo" || result.Diagnostics[0].Checkout != repository {
		t.Fatalf("nested Claude diagnostic attribution = %+v", result.Diagnostics)
	}
}

func TestMCPOptionsIgnoreRelativeXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	if got, want := DefaultOptions().XDGConfigHome, filepath.Join(home, ".config"); got != want {
		t.Fatalf("default relative XDG path = %q, want %q", got, want)
	}
	scanner := NewScanner(Options{HomeDir: home, WorkingDirectory: t.TempDir(), XDGConfigHome: "relative/config"})
	if got := scanner.options.XDGConfigHome; got != "" {
		t.Fatalf("explicit relative XDG path was enabled: %q", got)
	}
}

func TestNewScannerDoesNotMutateSharedOptionSlices(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "managed-settings.json")
	shared := []string{"relative.json", valid}
	options := testOptions(t.TempDir(), t.TempDir())
	options.ClaudeManagedSettingsPaths = shared
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			scanner := NewScanner(options)
			if len(scanner.options.ClaudeManagedSettingsPaths) != 1 || scanner.options.ClaudeManagedSettingsPaths[0] != valid {
				t.Errorf("normalized managed settings = %v", scanner.options.ClaudeManagedSettingsPaths)
			}
		}()
	}
	close(start)
	wait.Wait()
	if shared[0] != "relative.json" || shared[1] != valid {
		t.Fatalf("caller option slice was mutated: %v", shared)
	}
}

func TestScannerReadsDocumentedHostScopes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	working := filepath.Join(t.TempDir(), "working")
	mustMkdir(t, home)
	mustMkdir(t, working)
	options := testOptions(home, working)
	options.ClaudeManagedConfigPath = filepath.Join(t.TempDir(), "managed-mcp.json")
	options.GeminiSystemDefaultsPath = filepath.Join(t.TempDir(), "gemini-defaults.json")
	options.GeminiSystemSettingsPath = filepath.Join(t.TempDir(), "gemini-settings.json")
	options.OpenCodeConfigPath = filepath.Join(t.TempDir(), "custom.jsonc")
	options.OpenCodeConfigDir = filepath.Join(t.TempDir(), "custom-dir")
	managedJSON := filepath.Join(t.TempDir(), "managed.json")
	managedPlist := filepath.Join(t.TempDir(), "managed.plist")
	options.OpenCodeManagedConfigPaths = []string{managedJSON, managedPlist}

	mustWrite(t, options.ClaudeManagedConfigPath, `{"mcpServers":{"claude-managed":{"command":"managed"}}}`, 0o644)
	mustWrite(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"cursor-user":{"command":"cursor"}}}`, 0o644)
	mustWrite(t, filepath.Join(options.CodexHome, "config.toml"), "[mcp_servers.codex_user]\ncommand = \"codex\"\n", 0o644)
	mustWrite(t, filepath.Join(options.GeminiCLIHome, ".gemini", "settings.json"), `{"mcpServers":{"gemini-user":{"command":"gemini"}}}`, 0o644)
	mustWrite(t, options.GeminiSystemDefaultsPath, `{"mcpServers":{"gemini-default":{"command":"default"}}}`, 0o644)
	mustWrite(t, options.GeminiSystemSettingsPath, `{"mcpServers":{"gemini-override":{"command":"override"}}}`, 0o644)
	mustWrite(t, filepath.Join(options.XDGConfigHome, "opencode", "opencode.json"), `{"mcp":{"opencode-user":{"type":"local","command":["user"]}}}`, 0o644)
	mustWrite(t, options.OpenCodeConfigPath, `{"mcp":{"opencode-custom":{"type":"local","command":["custom"]}}}`, 0o644)
	mustWrite(t, filepath.Join(options.OpenCodeConfigDir, "opencode.jsonc"), `{"mcp":{"opencode-custom-dir":{"type":"local","command":["custom-dir"]}}}`, 0o644)
	mustWrite(t, managedJSON, `{"mcp":{"opencode-managed":{"type":"remote","url":"https://managed.example.test"}}}`, 0o644)
	plistBody, err := plist.Marshal(map[string]any{
		"mcp": map[string]any{
			"opencode-plist": map[string]any{"type": "local", "command": []string{"plist"}},
		},
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, managedPlist, string(plistBody), 0o644)

	result, err := NewScanner(options).Scan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		agent Agent
		scope Scope
		name  string
	}{
		{AgentClaudeCode, ScopeManaged, "claude-managed"},
		{AgentCursor, ScopeUser, "cursor-user"},
		{AgentCodex, ScopeUser, "codex_user"},
		{AgentGeminiCLI, ScopeUser, "gemini-user"},
		{AgentGeminiCLI, ScopeSystemDefaults, "gemini-default"},
		{AgentGeminiCLI, ScopeSystemOverride, "gemini-override"},
		{AgentOpenCode, ScopeUser, "opencode-user"},
		{AgentOpenCode, ScopeCustom, "opencode-custom"},
		{AgentOpenCode, ScopeCustom, "opencode-custom-dir"},
		{AgentOpenCode, ScopeManaged, "opencode-managed"},
		{AgentOpenCode, ScopeManaged, "opencode-plist"},
	}
	for _, check := range checks {
		assertDeclaration(t, result.Declarations, check.agent, check.scope, check.name, func(row Declaration) {})
	}
	if len(result.Declarations) != len(checks) {
		t.Fatalf("host declarations = %d, want %d: %+v", len(result.Declarations), len(checks), result.Declarations)
	}
}

func TestScannerReadsUserSourcesOnceAndFiltersRows(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, directory := range []string{home, first, second} {
		mustMkdir(t, directory)
	}
	mustWrite(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"shared":{"command":"server"}}}`, 0o644)
	userSettings := filepath.Join(home, ".claude", "settings.json")
	mustWrite(t, userSettings, `{"enableAllProjectMcpServers":true}`, 0o644)
	mustWrite(t, filepath.Join(first, ".mcp.json"), `{"mcpServers":{"first":{"command":"first"}}}`, 0o644)
	mustWrite(t, filepath.Join(second, ".mcp.json"), `{"mcpServers":{"second":{"command":"second"}}}`, 0o644)
	mustWrite(t, filepath.Join(first, ".cursor", "mcp.json"), `{"mcpServers":{"one":{"command":"one"}}}`, 0o644)
	mustWrite(t, filepath.Join(second, ".cursor", "mcp.json"), `{"mcpServers":{"two":{"command":"two"}}}`, 0o644)

	options := testOptions(home, first)
	options.ClaudeUserSettingsPath = userSettings
	scanner := NewScanner(options)
	original := scanner.readSource
	counts := map[string]int{}
	var countsMu sync.Mutex
	scanner.readSource = func(ctx context.Context, spec sourceSpec) readResult {
		countsMu.Lock()
		counts[spec.path]++
		countsMu.Unlock()
		return original(ctx, spec)
	}
	targets := []Target{testTarget(first), {
		RepoName: "second", RepoDisplay: "group/second", RepoPath: second,
		CheckoutRoot: second, CommonDir: second,
	}}
	result, err := scanner.Scan(context.Background(), targets)
	if err != nil {
		t.Fatal(err)
	}
	if counts[filepath.Join(home, ".claude.json")] != 1 {
		t.Fatalf("Claude user source reads = %d", counts[filepath.Join(home, ".claude.json")])
	}
	if counts[userSettings] != 1 {
		t.Fatalf("Claude user approval reads = %d", counts[userSettings])
	}
	filtered := FilterDeclarations(result.Declarations, Filter{Agent: AgentCursor, Repository: "group/second"})
	if len(filtered) != 1 || filtered[0].Name != "two" {
		t.Fatalf("filtered rows = %+v", filtered)
	}
}

func TestInlineCommandArgumentsAreRedactedBeforeNormalization(t *testing.T) {
	payload := []byte(`{"mcpServers":{"leak":{"type":"stdio","command":"/usr/bin/server --token=fixture-secret-must-not-appear"}}}`)
	rows, codes := parseClaudeProject(payload, sourceSpec{agent: AgentClaudeCode, scope: ScopeProject, path: "/tmp/.mcp.json"})
	if len(codes) != 0 || len(rows) != 1 {
		t.Fatalf("rows/codes = %+v/%+v", rows, codes)
	}
	if rows[0].Command != "[redacted]" || !containsRedaction(rows[0].Redactions, RedactionArguments) {
		t.Fatalf("inline command was not redacted: %+v", rows[0])
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretSentinel) || strings.Contains(string(encoded), "--token") {
		t.Fatalf("inline command leaked into normalized JSON: %s", encoded)
	}
}

func TestRedactedLiteralDollarValueDoesNotBecomeCredentialReference(t *testing.T) {
	payload := []byte(`{"mcp":{"remote":{"type":"remote","url":"https://example.test","headers":{"Authorization":"$ABCDEFGHIJKLMNOPQRSTUVWX123456"},"oauth":{"clientSecret":"$ZYXWVUTSRQPONMLKJIHGFEDC654321"}}}}`)
	rows, codes := parseOpenCode(payload, sourceSpec{agent: AgentOpenCode, scope: ScopeProject})
	if len(codes) != 0 || len(rows) != 1 {
		t.Fatalf("rows/codes = %+v/%+v", rows, codes)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ABCDEFGHIJKLMNOPQRSTUVWX123456", "ZYXWVUTSRQPONMLKJIHGFEDC654321"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("redacted literal was republished as a reference: %s", encoded)
		}
	}
}

func TestGeminiTypeAndTCPDetermineTransport(t *testing.T) {
	payload := []byte(`{"mcpServers":{"http":{"type":"http","url":"https://example.test/mcp"},"socket":{"tcp":"socket.example.test:443"}}}`)
	rows, codes := parseGemini(payload, sourceSpec{agent: AgentGeminiCLI, scope: ScopeProject})
	if len(codes) != 0 || len(rows) != 2 {
		t.Fatalf("rows/codes = %+v/%+v", rows, codes)
	}
	if rows[0].Name != "http" || rows[0].Transport != TransportStreamableHTTP {
		t.Fatalf("Gemini HTTP row = %+v", rows[0])
	}
	if rows[1].Name != "socket" || rows[1].Transport != TransportWebSocket || rows[1].Endpoint != "tcp://socket.example.test:443" {
		t.Fatalf("Gemini TCP row = %+v", rows[1])
	}
}

func TestCaseDistinctServerNamesKeepExactPolicyIdentity(t *testing.T) {
	payload := []byte(`{"mcp":{"allowed":["Foo"]},"mcpServers":{"Foo":{"command":"upper"},"foo":{"command":"lower"}}}`)
	rows, codes := parseGemini(payload, sourceSpec{agent: AgentGeminiCLI, scope: ScopeProject})
	if len(codes) != 0 || len(rows) != 2 || rows[0].Name != "Foo" || rows[1].Name != "foo" {
		t.Fatalf("case-distinct rows/codes = %+v/%+v", rows, codes)
	}
	if !hasPolicy(rows[0], PolicyAllowed) || hasPolicy(rows[1], PolicyAllowed) {
		t.Fatalf("allowed policy crossed exact server identities: %+v", rows)
	}
}

func TestClaudeApprovalPrecedenceIncludesUserAndManagedSettings(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	repository := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, home)
	mustMkdir(t, repository)
	mustWrite(t, filepath.Join(repository, ".mcp.json"), `{"mcpServers":{"demo":{"command":"server"},"scalar":{"command":"server"}}}`, 0o644)
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"), `{"enableAllProjectMcpServers":true,"disabledMcpjsonServers":["demo"]}`, 0o644)
	mustWrite(t, filepath.Join(repository, ".claude", "settings.local.json"), `{"enableAllProjectMcpServers":false,"enabledMcpjsonServers":["demo"]}`, 0o644)
	managed := filepath.Join(t.TempDir(), "managed-settings.json")
	mustWrite(t, managed, `{"enableAllProjectMcpServers":false}`, 0o644)

	options := testOptions(home, repository)
	options.ClaudeUserSettingsPath = filepath.Join(home, ".claude", "settings.json")
	options.ClaudeManagedSettingsPaths = []string{managed}
	result, err := NewScanner(options).Scan(context.Background(), []Target{testTarget(repository)})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaration(t, result.Declarations, AgentClaudeCode, ScopeProject, "demo", func(row Declaration) {
		if row.Enabled != nil || !hasPolicy(row, PolicyAllowed) || !hasPolicy(row, PolicyExcluded) {
			t.Fatalf("merged Claude approval = %+v", row)
		}
		if !hasCoverage(row, DiagnosticInvalidDeclaration) {
			t.Fatalf("conflicting approval lacked coverage: %+v", row)
		}
	})
	assertDeclaration(t, result.Declarations, AgentClaudeCode, ScopeProject, "scalar", func(row Declaration) {
		if row.Enabled != nil {
			t.Fatalf("higher-precedence false did not clear enable-all: %+v", row)
		}
	})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("approval diagnostics = %+v", result.Diagnostics)
	}
}

func TestClaudeNestedTargetUsesExactOwnerAndProjectPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	parent := filepath.Join(t.TempDir(), "parent")
	child := filepath.Join(parent, "child")
	nested := filepath.Join(child, "service")
	for _, directory := range []string{home, child, nested} {
		mustMkdir(t, directory)
	}
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{
		"projects": map[string]any{
			child:  map[string]any{"mcpServers": map[string]any{"same": map[string]any{"command": "child"}}},
			nested: map[string]any{"mcpServers": map[string]any{"same": map[string]any{"command": "nested"}}},
		},
	})
	options := testOptions(home, parent)
	targets := []Target{
		{RepoName: "parent", RepoDisplay: "a-parent", RepoPath: parent, CheckoutRoot: parent, CommonDir: parent},
		{RepoName: "child", RepoDisplay: "z-child", RepoPath: child, CheckoutRoot: child, CommonDir: child},
	}
	result, err := NewScanner(options).Scan(context.Background(), targets)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, row := range result.Declarations {
		if row.Agent == AgentClaudeCode && row.Scope == ScopeLocal && row.Name == "same" {
			if row.Repository != "z-child" || row.Checkout != child {
				t.Fatalf("nested Claude owner = %+v", row)
			}
			paths[row.LocalProjectPath] = true
		}
	}
	if !paths[child] || !paths[nested] || len(paths) != 2 {
		t.Fatalf("local project paths = %v, rows=%+v", paths, result.Declarations)
	}
}

func TestMalformedClaudeUserSourceReportsUserAndLocalScopes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	repository := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, repository)
	mustWrite(t, filepath.Join(home, ".claude.json"), `{`, 0o644)
	result, err := NewScanner(testOptions(home, repository)).Scan(context.Background(), []Target{testTarget(repository)})
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[Scope]bool{}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Agent == AgentClaudeCode && diagnostic.Code == DiagnosticMalformed {
			scopes[diagnostic.Scope] = true
		}
	}
	if !scopes[ScopeUser] || !scopes[ScopeLocal] {
		t.Fatalf("Claude malformed scopes = %+v", result.Diagnostics)
	}
}

func TestMCPTransportNormalizationPreservesDocumentedKinds(t *testing.T) {
	geminiRows, codes := parseGemini([]byte(`{"mcpServers":{"url":{"url":"https://example.test/sse"},"precedence":{"url":"https://example.test/sse","tcp":"socket.example.test:443"}}}`), sourceSpec{agent: AgentGeminiCLI})
	if len(codes) != 0 || len(geminiRows) != 2 {
		t.Fatalf("Gemini rows/codes = %+v/%+v", geminiRows, codes)
	}
	for _, row := range geminiRows {
		if row.Transport != TransportSSE || strings.HasPrefix(row.Endpoint, "tcp:") {
			t.Fatalf("Gemini URL transport = %+v", row)
		}
	}
	claudeRows, codes := parseClaudeProject([]byte(`{"mcpServers":{"http":{"type":"http","url":"https://example.test"},"stream":{"type":"streamable-http","url":"https://example.test"}}}`), sourceSpec{agent: AgentClaudeCode})
	if len(codes) != 0 || len(claudeRows) != 2 || claudeRows[0].Transport != TransportHTTP || claudeRows[1].Transport != TransportStreamableHTTP {
		t.Fatalf("Claude transports = %+v/%+v", claudeRows, codes)
	}
}

func TestCredentialReferencesAreProviderAwareAndSecretSafe(t *testing.T) {
	tests := []struct {
		name string
		row  Declaration
		want []string
	}{
		{name: "Claude", row: Declaration{Agent: AgentClaudeCode}, want: []string{"CLAUDE_TOKEN"}},
		{name: "Cursor", row: Declaration{Agent: AgentCursor}, want: []string{"CURSOR_TOKEN"}},
		{name: "Gemini", row: Declaration{Agent: AgentGeminiCLI}, want: []string{"GEMINI_ARG", "GEMINI_TOKEN", "WIN_TOKEN"}},
		{name: "OpenCode", row: Declaration{Agent: AgentOpenCode}, want: []string{"OPENCODE_TOKEN"}},
	}
	values := map[string]string{
		"Claude":   "Bearer ${CLAUDE_TOKEN:-fallback-secret}",
		"Cursor":   "Bearer ${env:CURSOR_TOKEN}",
		"Gemini":   "$GEMINI_TOKEN ${GEMINI_ARG} %WIN_TOKEN%",
		"OpenCode": "Bearer {env:OPENCODE_TOKEN}",
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspectHeaders(&test.row, map[string]string{"Authorization": values[test.name]})
			for _, want := range test.want {
				if !hasCredential(test.row, CredentialEnvironmentReference, want) {
					t.Fatalf("references = %+v, missing %s", test.row.Credentials, want)
				}
			}
			encoded, err := json.Marshal(test.row)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"fallback-secret", "Bearer ", "${", "{env:"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("credential source leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestCodexTopLevelOAuthFactsDoNotRetainValues(t *testing.T) {
	payload := []byte("[mcp_servers.remote]\nurl = \"https://example.test/mcp\"\nscopes = [\"read:fixture-secret\", \"write:fixture-secret\"]\noauth_resource = \"https://resource.fixture-secret.example\"\n")
	rows, codes := parseCodex(payload, sourceSpec{agent: AgentCodex})
	if len(codes) != 0 || len(rows) != 1 {
		t.Fatalf("Codex rows/codes = %+v/%+v", rows, codes)
	}
	row := rows[0]
	if !hasCredential(row, CredentialOAuth, "scopes") || !hasCredential(row, CredentialOAuth, "resource") || !hasPolicy(row, PolicyOAuth) {
		t.Fatalf("Codex OAuth facts = %+v", row)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-secret") || strings.Contains(string(encoded), "read:") {
		t.Fatalf("Codex OAuth values leaked: %s", encoded)
	}
}

func TestJSONAdaptersRejectNullServerMembers(t *testing.T) {
	for _, test := range []struct {
		name  string
		parse func([]byte, sourceSpec) ([]Declaration, []DiagnosticCode)
		body  string
		agent Agent
	}{
		{name: "Claude", parse: parseClaudeProject, body: `{"mcpServers":{"ghost":null}}`, agent: AgentClaudeCode},
		{name: "Cursor", parse: parseCursor, body: `{"mcpServers":{"ghost":null}}`, agent: AgentCursor},
		{name: "Gemini", parse: parseGemini, body: `{"mcpServers":{"ghost":null}}`, agent: AgentGeminiCLI},
		{name: "OpenCode", parse: parseOpenCode, body: `{"mcp":{"ghost":null}}`, agent: AgentOpenCode},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, codes := test.parse([]byte(test.body), sourceSpec{agent: test.agent})
			if len(rows) != 0 || len(codes) != 1 || codes[0] != DiagnosticInvalidDeclaration {
				t.Fatalf("null member rows/codes = %+v/%+v", rows, codes)
			}
		})
	}
}

func TestClaudeConfigDirAndManagedUsernameUseDocumentedIdentity(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home-not-username")
	configDir := filepath.Join(t.TempDir(), "claude-config")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	options := DefaultOptions()
	if options.ClaudeUserConfigPath != filepath.Join(configDir, ".claude.json") || options.ClaudeUserSettingsPath != filepath.Join(configDir, "settings.json") {
		t.Fatalf("Claude config override = %+v", options)
	}
	current, err := user.Current()
	if err == nil && filepath.Base(current.Username) == current.Username && current.Username != "" {
		if got := managedPreferenceUsername(home); got != current.Username {
			t.Fatalf("managed preference username = %q, want %q", got, current.Username)
		}
	}
}

func TestEmptyScannerResultMarshalsArrays(t *testing.T) {
	result, err := NewScanner(testOptions(t.TempDir(), t.TempDir())).Scan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"servers":null`) || strings.Contains(string(encoded), `"diagnostics":null`) {
		t.Fatalf("empty result used null arrays: %s", encoded)
	}
}

func TestScannerReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewScanner(testOptions(t.TempDir(), t.TempDir())).Scan(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if !result.Coverage.StaticDeclarationsOnly {
		t.Fatalf("coverage lost on cancellation: %+v", result.Coverage)
	}
}

func testOptions(home, workingDirectory string) Options {
	return Options{
		HomeDir: home, WorkingDirectory: workingDirectory,
		XDGConfigHome: filepath.Join(home, ".config"), CodexHome: filepath.Join(home, ".codex"),
		GeminiCLIHome: home, Concurrency: 2, MaxFileBytes: DefaultMaxFileBytes,
		MaxSymlinkDepth: 4,
	}
}

func testTarget(repository string) agenttarget.Target {
	return agenttarget.Target{
		RepoName: "demo", RepoDisplay: "group/demo", RepoPath: repository,
		CheckoutRoot: repository, CommonDir: repository,
	}
}

func assertDeclaration(t *testing.T, rows []Declaration, agent Agent, scope Scope, name string, check func(Declaration)) {
	t.Helper()
	for _, row := range rows {
		if row.Agent == agent && row.Scope == scope && row.Name == name {
			check(row)
			return
		}
	}
	t.Fatalf("missing %s/%s/%s in %+v", agent, scope, name, rows)
}

func containsRedaction(values []Redaction, want Redaction) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasPolicy(row Declaration, want PolicyKind) bool {
	for _, policy := range row.Policies {
		if policy.Kind == want {
			return true
		}
	}
	return false
}

func hasCredential(row Declaration, kind CredentialKind, name string) bool {
	for _, credential := range row.Credentials {
		if credential.Kind == kind && credential.Name == name {
			return true
		}
	}
	return false
}

func hasCoverage(row Declaration, code DiagnosticCode) bool {
	for _, item := range row.Coverage {
		if item.Code == code {
			return true
		}
	}
	return false
}

func copyFixture(t *testing.T, name, destination string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, destination, string(body), 0o644)
}

func writeJSON(t *testing.T, filename string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filename, string(body), 0o644)
}

func mustWrite(t *testing.T, filename, body string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(filename))
	if err := os.WriteFile(filename, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, directory string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
}
