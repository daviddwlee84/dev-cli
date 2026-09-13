package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/hygiene"
)

func TestHygieneCLIEncodingRepairAndCoverage(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.repo.Root, "cut.md")
	if err := os.WriteFile(target, []byte("private-source\n\xe4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := h.run("hygiene", "--repo", h.repo.Root, "--secrets", "off", "--generic", "off", "scan", "--file", "cut.md")
	if err == nil || !strings.Contains(out, "line 2, byte offset 15") || !strings.Contains(out, "warning findings do not block") || !strings.Contains(out, "repair-encoding --help") || strings.Contains(out, "private-source") {
		t.Fatalf("coverage output: %s (%v)", out, err)
	}
	out = h.mustRun("hygiene", "--repo", h.repo.Root, "--json", "repair-encoding", "--file", "cut.md")
	var p hygiene.Plan
	if err = json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.Kind != "hygiene_repair_encoding" || p.Files[0].Encoding.InvalidBytes != 1 || strings.Contains(out, "private-source") {
		t.Fatal("unsafe or missing repair output")
	}
	for _, args := range [][]string{
		{"repair-encoding"},
		{"repair-encoding", "--file", "cut.md", "--invalid", "auto"},
		{"repair-encoding", "--apply", "--plan", p.ID, "--file", "cut.md", "--yes"},
		{"repair-encoding", "--apply", "--plan", p.ID, "--invalid", "remove", "--yes"},
		{"redact", "--apply", "--plan", p.ID, "--yes"},
	} {
		if _, _, err = h.run(append([]string{"hygiene", "--repo", h.repo.Root}, args...)...); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
	h.mustRun("hygiene", "--repo", h.repo.Root, "repair-encoding", "--apply", "--plan", p.ID, "--yes")
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, []byte("private-source\n�\n")) {
		t.Fatal("CLI repair did not preserve text")
	}
}

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

func TestHygieneManageJSONPreviewsWithoutRepositoryMutation(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("hygiene", "manage", h.repo.Root, "--json")
	var batch hygiene.SetupBatch
	if err := json.Unmarshal([]byte(out), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Kind != "hygiene_setup_batch" || len(batch.Entries) != 1 {
		t.Fatal(out)
	}
	for _, rel := range []string{".pre-commit-config.yaml", ".gitleaks.toml", ".dev-cli/hygiene.toml"} {
		if _, err := os.Stat(filepath.Join(h.repo.Root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("preview wrote %s", rel)
		}
	}
}
