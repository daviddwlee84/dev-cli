package scaffold

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStarterAgentContractIsExplicitlyIncompleteAndRendered(t *testing.T) {
	body := StarterAgentContract()
	for _, required := range []string{
		"# Project agent guidance",
		"Bootstrap status: incomplete",
		"## Project purpose",
		"## Toolchain and verified commands",
		"## Architecture",
		"## Behavioral contracts",
		"## Handoff requirements",
		"TODO",
		"Never claim a check passed unless it was actually run",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("starter contract missing %q", required)
		}
	}
	if strings.Contains(body, "{{") || strings.Contains(body, "run the repository's documented checks") {
		t.Fatalf("starter contract contains unresolved or misleading guidance:\n%s", body)
	}
}

func TestAgentReadyPlanUsesCanonicalStarterContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "demo")
	plan, err := BuildPlan(Builtins(), PlanOptions{
		Preset: "agent-ready", Root: root, Name: "demo-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := findFilePlan(plan.Files, "agent-contract")
	if file == nil {
		t.Fatal("agent-ready plan omitted AGENTS.md")
	}
	want := StarterAgentContract()
	if file.RelativePath != "AGENTS.md" || file.Content != want {
		t.Fatalf("agent-ready AGENTS.md drifted from canonical starter:\n%s", file.Content)
	}
}
