package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/hygiene"
)

func TestHygieneCLIPrivateRulePlanScanAndRedact(t *testing.T) {
	h := newHarness(t)
	input := filepath.Join(t.TempDir(), "value")
	value := "private-host-cli-canary"
	if err := os.WriteFile(input, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("hygiene", "--repo", h.repo.Root, "--json", "rules", "add", "--id", "local-host", "--value-file", input, "--replacement", "host.example.invalid")
	if strings.Contains(out, value) {
		t.Fatal("rule value reached output")
	}
	var plan hygiene.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ID == "" || plan.Revision == "" {
		t.Fatalf("invalid plan: %+v", plan)
	}
	h.mustRun("hygiene", "--repo", h.repo.Root, "--json", "rules", "apply", "--plan", plan.ID, "--yes")
	target := filepath.Join(h.repo.Root, "sample.txt")
	if err := os.WriteFile(target, []byte("target "+value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := h.run("hygiene", "--repo", h.repo.Root, "--json", "--secrets", "off", "--generic", "off", "scan", "--file", "sample.txt")
	if err == nil {
		t.Fatal("blocking finding returned success")
	}
	var report hygiene.Report
	if err = json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "complete" || report.Blocked != 1 || strings.Contains(out, value) {
		t.Fatalf("unsafe or missing scan output: %s", out)
	}
	// CLI policy overrides are run-local. Redaction uses the recorded effective
	// policy, so obtain the same persistent policy before planning a rewrite.
	out = h.mustRun("hygiene", "--repo", h.repo.Root, "--json", "--secrets", "off", "--generic", "off", "rules", "policy")
	if err = json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	h.mustRun("hygiene", "--repo", h.repo.Root, "rules", "apply", "--plan", plan.ID, "--yes")
	out, _, _ = h.run("hygiene", "--repo", h.repo.Root, "--json", "scan", "--file", "sample.txt")
	if err = json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	out = h.mustRun("hygiene", "--repo", h.repo.Root, "--json", "redact", "--report", report.ID, "--file", "sample.txt")
	if err = json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	h.mustRun("hygiene", "--repo", h.repo.Root, "redact", "--apply", "--plan", plan.ID, "--yes")
	body, _ := os.ReadFile(target)
	if string(body) != "target host.example.invalid\n" {
		t.Fatalf("redact=%q", body)
	}
}
func TestHygieneCLIStatusAndMissingEngineDoNotClaimClean(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("hygiene", "--repo", h.repo.Root, "--json", "status")
	var status hygiene.Status
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.Configured || status.Visibility != "unknown" {
		t.Fatalf("wrong initial status: %+v", status)
	}
}
