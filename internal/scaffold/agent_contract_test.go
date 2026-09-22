package scaffold

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStarterAgentContractDefersProjectSpecificGuidance(t *testing.T) {
	body := StarterAgentContract()
	for _, required := range []string{
		"# Project agent guidance",
		"until the user defines what this project does",
		"information verified in this repository",
		"Do not invent a stack, commands, or policies",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("starter contract missing %q", required)
		}
	}
	if lines := len(strings.Split(strings.TrimSpace(body), "\n")); lines < 8 || lines > 12 {
		t.Fatalf("starter contract has %d lines, want 8–12", lines)
	}
	if strings.Contains(body, "TODO") {
		t.Fatal("deferred contract must not contain a placeholder checklist")
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
