package cli_test

import (
	"os"
	"path/filepath"
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

func TestPromptOpenRequiresATerminal(t *testing.T) {
	requirePOSIXPromptTest(t)
	h := newHarness(t)
	appendConfig(t, h.configPath, `
[[agent]]
name = "interactive"
default = true
[agent.open]
command = ["sh", "-c", "cat $1; cat", "_", "{{prompt_file}}"]
input = "file"
`)
	_, _, err := h.run("prompt", "open", "session-close")
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err = %v, want terminal requirement", err)
	}
}

func TestPromptRunWithoutAnAgentShowsNestedConfig(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("prompt", "run", "session-close")
	if err == nil {
		t.Fatal("expected missing agent error")
	}
	for _, want := range []string{"[[agent]]", "[agent.run]", "[agent.open]"} {
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
