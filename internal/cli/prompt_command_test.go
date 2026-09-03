package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPromptListAndRenderRecipes(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("prompt", "list")
	for _, want := range []string{"pr-triage", "session-close", "workspace-closeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt list missing %q:\n%s", want, out)
		}
	}

	out = h.mustRun("prompt", "render", "session-close")
	for _, want := range []string{"# Agent session close review", `"recipe": "session-close"`, `"runtime": "none"`} {
		if !strings.Contains(out, want) {
			t.Errorf("session-close missing %q:\n%s", want, out)
		}
	}

	out = h.mustRun("prompt", "render", "workspace-closeout", h.repo.Root)
	for _, want := range []string{"# Workspace closeout", `"recipe": "workspace-closeout"`, `"ownership": "canonical"`, `"status": "not-applicable"`} {
		if !strings.Contains(out, want) {
			t.Errorf("workspace-closeout missing %q:\n%s", want, out)
		}
	}
}

func TestPromptRenderDoesNotReparseProviderBraces(t *testing.T) {
	h := newHarness(t)
	installFakeGHResponses(t, "[]", `[{"number":7,"title":"Fix {{unknown}} rendering","url":"u","state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"acme/demo"}}]`)
	out := h.mustRun("prompt", "render", "pr-triage", "--scope", "account", "--repo", "acme/demo")
	if !strings.Contains(out, `Fix {{unknown}} rendering`) {
		t.Fatalf("provider data was lost or reparsed:\n%s", out)
	}
}

func TestPromptAgentsInventoryIsSortedRedactedAndStable(t *testing.T) {
	h := newHarness(t)
	appendConfig(t, h.configPath, `
[[agent]]
name = "zeta"
description = "Interactive profile"
[agent.open]
shell = 'printf shell-source-secret < "$DEV_PROMPT_FILE"'
input = "file"

[[agent]]
name = "alpha"
description = "Batch profile"
default = true
[agent.run]
command = ["/private/argv-directory-secret/alpha", "--token=argv-value-secret"]
input = "stdin"
`)

	out := h.mustRun("prompt", "agents")
	for _, want := range []string{"PROFILE", "DEFAULT", "RUN", "OPEN", "DESCRIPTION", "alpha", "zeta", "shell"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent inventory missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Errorf("agent inventory is not sorted:\n%s", out)
	}
	for _, secret := range []string{"argv-directory-secret", "argv-value-secret", "shell-source-secret", h.configPath} {
		if strings.Contains(out, secret) {
			t.Errorf("human inventory leaked %q:\n%s", secret, out)
		}
	}

	out = h.mustRun("prompt", "agents", "--json")
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("prompt agents --json: %v\n%s", err, out)
	}
	want := []map[string]any{
		{
			"name": "alpha", "description": "Batch profile", "default": true,
			"run":  map[string]any{"configured": true, "kind": "command", "executable": "alpha"},
			"open": map[string]any{"configured": false, "kind": "none", "executable": ""},
		},
		{
			"name": "zeta", "description": "Interactive profile", "default": false,
			"run":  map[string]any{"configured": false, "kind": "none", "executable": ""},
			"open": map[string]any{"configured": true, "kind": "shell", "executable": ""},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent inventory schema/content mismatch:\n got: %#v\nwant: %#v", got, want)
	}
	for _, secret := range []string{"argv-directory-secret", "argv-value-secret", "shell-source-secret", h.configPath} {
		if strings.Contains(out, secret) {
			t.Errorf("JSON inventory leaked %q:\n%s", secret, out)
		}
	}
}

func TestPromptAgentsEmptyInventory(t *testing.T) {
	h := newHarness(t)
	if out := h.mustRun("prompt", "agents"); out != "No agent profiles configured.\n" {
		t.Errorf("empty human inventory = %q", out)
	}
	if out := h.mustRun("prompt", "agents", "--json"); out != "[]\n" {
		t.Errorf("empty JSON inventory = %q", out)
	}
}

func TestPromptStarterConfigDocumentsAgentDiscovery(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("config", "init", "--stdout")
	for _, want := range []string{
		"dev prompt agents", `description = "Local review and implementation agent"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("starter config missing %q:\n%s", want, out)
		}
	}
}

func TestPromptAgentSelectionIsExplicitDefaultOrSole(t *testing.T) {
	h := newHarness(t)
	appendConfig(t, h.configPath, `
[[agent]]
name = "alpha"
default = true
[agent.run]
command = ["alpha-agent"]
input = "stdin"

[[agent]]
name = "beta"
[agent.run]
command = ["beta-agent"]
input = "stdin"
`)
	if out := h.mustRun("prompt", "run", "session-close", "--dry-run"); !strings.Contains(out, "agent: alpha") {
		t.Fatalf("global default was not selected:\n%s", out)
	}
	if out := h.mustRun("prompt", "run", "session-close", "--dry-run", "--agent", "beta"); !strings.Contains(out, "agent: beta") {
		t.Fatalf("explicit profile was not selected:\n%s", out)
	}

	sole := newHarness(t)
	appendConfig(t, sole.configPath, `
[[agent]]
name = "only"
[agent.run]
command = ["only-agent"]
input = "stdin"
`)
	if out := sole.mustRun("prompt", "run", "session-close", "--dry-run"); !strings.Contains(out, "agent: only") {
		t.Fatalf("sole global profile was not selected:\n%s", out)
	}
}

func TestPromptAgentDiagnosticsAreModeAwareWithoutFallback(t *testing.T) {
	h := newHarness(t)
	appendConfig(t, h.configPath, `
[[agent]]
name = "open-default"
default = true
[agent.open]
command = ["open-agent", "{{prompt_file}}"]
input = "file"

[[agent]]
name = "runner"
[agent.run]
command = ["run-agent"]
input = "stdin"
`)
	_, _, err := h.run("prompt", "run", "session-close", "--dry-run")
	if err == nil {
		t.Fatal("selected profile without run launcher should fail")
	}
	for _, want := range []string{`agent "open-default" has no [agent.run] launcher`, "run-capable profiles: runner", "dev prompt agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-mode error lacks %q: %v", want, err)
		}
	}

	ambiguous := newHarness(t)
	appendConfig(t, ambiguous.configPath, `
[[agent]]
name = "zeta"
[agent.run]
command = ["zeta-agent"]
input = "stdin"
[[agent]]
name = "alpha"
[agent.run]
command = ["alpha-agent"]
input = "stdin"
`)
	_, _, err = ambiguous.run("prompt", "run", "session-close", "--dry-run")
	if err == nil {
		t.Fatal("multiple profiles without a global default should be ambiguous")
	}
	for _, want := range []string{"run-capable profiles: alpha, zeta", "dev prompt agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguous error lacks %q: %v", want, err)
		}
	}

	_, _, err = ambiguous.run("prompt", "run", "session-close", "--dry-run", "--agent", "missing")
	if err == nil {
		t.Fatal("unknown profile should fail")
	}
	for _, want := range []string{`unknown agent "missing"`, "run-capable profiles: alpha, zeta", "dev prompt agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-agent error lacks %q: %v", want, err)
		}
	}
}

func TestPromptSelectionFailurePrecedesForgeCollection(t *testing.T) {
	requirePOSIXPromptTest(t)
	h := newHarness(t)
	appendConfig(t, h.configPath, `
[[agent]]
name = "runner"
[agent.run]
command = ["run-agent"]
input = "stdin"
`)
	calls := installFakeGH(t, "[]")
	_, _, err := h.run("prompt", "run", "pr-triage", "--scope", "account", "--dry-run", "--agent", "missing")
	if err == nil || !strings.Contains(err.Error(), `unknown agent "missing"`) {
		t.Fatalf("selection error = %v", err)
	}
	body, readErr := os.ReadFile(calls)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(body) != 0 {
		t.Fatalf("forge was consulted before profile resolution:\n%s", body)
	}
}

func TestPromptRunUsesDefaultBatchLauncherAndFiniteStdin(t *testing.T) {
	requirePOSIXPromptTest(t)
	h := newHarness(t)
	installFakeGH(t, "[]")
	appendConfig(t, h.configPath, `
[[agent]]
name = "echoer"
default = true
[agent.run]
command = ["sh", "-c", "cat"]
input = "stdin"
`)
	out := h.mustRun("prompt", "run", "pr-triage", "--scope", "account")
	if !strings.Contains(out, "# Pull request triage") || !strings.Contains(out, `"recipe": "pr-triage"`) {
		t.Fatalf("batch agent did not receive the prompt:\n%s", out)
	}
}

func TestPromptDryRunDoesNotExecuteOrNeedTTY(t *testing.T) {
	requirePOSIXPromptTest(t)
	h := newHarness(t)
	marker := filepath.Join(h.home, "agent-ran")
	appendConfig(t, h.configPath, `
[[agent]]
name = "tripwire"
default = true
[agent.run]
command = ["sh", "-c", "touch `+filepath.ToSlash(marker)+`"]
input = "stdin"
[agent.open]
command = ["sh", "-c", "cat $1; cat", "_", "{{prompt_file}}"]
input = "file"
`)

	out := h.mustRun("prompt", "run", "session-close", "--dry-run")
	for _, want := range []string{"agent: tripwire", "mode: run", "transport: stdin", "--- prompt ---"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("dry-run executed the batch agent")
	}

	// NewRootCommandWithIO is not a TTY, but open --dry-run is specifically
	// allowed so the launch can be inspected in scripts and tests.
	out = h.mustRun("prompt", "open", "session-close", "--dry-run")
	if !strings.Contains(out, "mode: open") || !strings.Contains(out, "<temporary-prompt-file>") {
		t.Errorf("open dry-run output:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("open dry-run executed an agent")
	}
}

func TestPromptOpenRequiresATerminalBeforeForgeCollection(t *testing.T) {
	requirePOSIXPromptTest(t)
	h := newHarness(t)
	calls := installFakeGH(t, "[]")
	appendConfig(t, h.configPath, `
[[agent]]
name = "interactive"
default = true
[agent.open]
command = ["sh", "-c", "cat $1; cat", "_", "{{prompt_file}}"]
input = "file"
`)
	_, _, err := h.run("prompt", "open", "pr-triage", "--scope", "account")
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err = %v, want terminal requirement", err)
	}
	body, readErr := os.ReadFile(calls)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(body) != 0 {
		t.Fatalf("forge was consulted before the TTY check:\n%s", body)
	}
}

func TestPromptRunWithoutAnAgentShowsNestedConfig(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("prompt", "run", "session-close")
	if err == nil {
		t.Fatal("expected missing agent error")
	}
	for _, want := range []string{"[[agent]]", "[agent.run]", "[agent.open]", "run-capable profiles: none", "dev prompt agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-agent error lacks %q: %v", want, err)
		}
	}
}

func requirePOSIXPromptTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh")
	}
}
