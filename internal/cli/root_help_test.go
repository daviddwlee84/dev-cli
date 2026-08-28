package cli_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/cli"
)

func TestRootHelpShowsWorkflowWithoutConfig(t *testing.T) {
	out := executeHelp(t, "--config", filepath.Join(t.TempDir(), "missing.toml"), "--help")

	if !strings.Contains(out, "keep four things separate") {
		t.Errorf("root help does not describe all four responsibilities:\n%s", out)
	}
	if strings.Contains(out, "keep three things separate") {
		t.Errorf("root help retains the old responsibility count:\n%s", out)
	}
	assertWorkflowTLDR(t, out, "Usage:")
}

func TestHelpIndexUsesSharedWorkflow(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.toml")
	rootOut := executeHelp(t, "--config", configPath, "--help")
	helpOut := executeHelp(t, "--config", configPath, "help")

	rootFlow := assertWorkflowTLDR(t, rootOut, "Usage:")
	helpFlow := assertWorkflowTLDR(t, helpOut, "TOPIC")
	if helpFlow != rootFlow {
		t.Errorf("root help and topic index use different workflows:\nroot:\n%s\n\nindex:\n%s", rootFlow, helpFlow)
	}
}

func executeHelp(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, &errOut)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("dev %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut.String())
	}
	return out.String()
}

func assertWorkflowTLDR(t *testing.T, out, nextSection string) string {
	t.Helper()
	const marker = "TL;DR: default managed-task loop"
	start := strings.Index(out, marker)
	if start < 0 {
		t.Fatalf("workflow missing before %s section:\n%s", nextSection, out)
	}
	end := strings.Index(out[start:], "\n\n"+nextSection)
	if end < 0 {
		t.Fatalf("following %s section missing:\n%s", nextSection, out)
	}
	flow := out[start : start+end]
	for _, b := range []byte(flow) {
		if b >= 0x80 {
			t.Fatalf("workflow must be 7-bit ASCII, found byte %#x:\n%s", b, flow)
		}
	}
	for _, want := range []string{
		"dev start", "HOT", "dev park --next", "dev resume", "WARM",
		"dev done", "dev done --ff", "dev done --pr", "push / review handoff",
		"feedback --> resume if parked --> work", "DONE", "dev sweep (report)",
		"dev sweep --apply (reap)", "Remote merge detection and cleanup are not automatic",
	} {
		if !strings.Contains(flow, want) {
			t.Errorf("workflow missing %q:\n%s", want, flow)
		}
	}
	const prBranch = "+-- branch/worktree: dev done --pr --> push / review handoff"
	if !strings.Contains(flow, prBranch) {
		t.Errorf("workflow PR branch does not terminate at the handoff: \n%s", flow)
	}
	if strings.Contains(flow, "dev done --pr --> DONE") {
		t.Errorf("PR handoff must not transition directly to DONE:\n%s", flow)
	}
	return flow
}
