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

func hygieneReportFixture(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	sample := "contact person@example.org\ncopy person@example.org and other@example.org\n"
	if err := os.WriteFile(filepath.Join(h.repo.Root, "sample.txt"), []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHygieneCLIReportSummarizesLatestScanWithoutValues(t *testing.T) {
	h := hygieneReportFixture(t)
	policy := []string{"hygiene", "--repo", h.repo.Root, "--secrets", "off", "--generic", "warn"}
	h.mustRun(append(policy, "scan", "--file", "sample.txt")...)
	out := h.mustRun(append(policy, "--json", "report", "--by", "severity,rule,file,category", "--findings")...)
	var summary hygiene.Summary
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("summary JSON: %v\n%s", err, out)
	}
	if summary.Kind != "hygiene_summary" || summary.SchemaVersion != 1 || summary.Status != "complete" || !summary.PolicyCurrent ||
		summary.Totals.Findings != 2 || summary.Totals.Occurrences != 3 || summary.Totals.Warnings != 2 ||
		len(summary.Rules) != 1 || summary.Rules[0].Rule != "privacy-email" || *summary.Rules[0].DistinctValues != 2 ||
		len(summary.Files) != 1 || len(summary.Categories) != 1 || len(summary.Findings) != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	human := h.mustRun(append(policy, "report")...)
	for _, want := range []string{"Hygiene worktree · complete", "findings 2 (occ 3)", "BY SEVERITY", "TOP RULES (occurrences desc)", "privacy-email", "TOP FILES", "sample.txt"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human report lacks %q:\n%s", want, human)
		}
	}
	for _, raw := range []string{"person@example.org", "other@example.org"} {
		if strings.Contains(out, raw) || strings.Contains(human, raw) {
			t.Fatalf("raw value %q reached report output", raw)
		}
	}
}

func TestHygieneCLIReportValuesUsePrivateReviewPath(t *testing.T) {
	h := hygieneReportFixture(t)
	policy := []string{"hygiene", "--repo", h.repo.Root, "--secrets", "off", "--generic", "warn"}
	human := h.mustRun(append(policy, "report", "--values", "--file", "sample.txt")...)
	if !strings.Contains(human, "VALUES · privacy-email (masked)") || !strings.Contains(human, "p•••@e•••.org") ||
		!strings.Contains(human, "dev hygiene review-path ") || strings.Contains(human, "person@example.org") {
		t.Fatalf("values report:\n%s", human)
	}
	out := h.mustRun(append(policy, "--json", "report", "--values", "--file", "sample.txt")...)
	var summary hygiene.Summary
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.Rescanned || !summary.ValuesShown || len(summary.Rules[0].Values) != 2 || summary.Rules[0].Values[0].Occurrences != 2 || strings.Contains(out, "example.org\"") {
		t.Fatalf("values summary = %s", out)
	}
	path := strings.TrimSpace(h.mustRun("hygiene", "--repo", h.repo.Root, "review-path", summary.ReportID))
	if strings.Contains(path, "person@example.org") {
		t.Fatal("review-path printed a raw value")
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "person@example.org") || !strings.Contains(string(body), "sample.txt:1") {
		t.Fatalf("private values review: %v\n%s", err, body)
	}
}

func TestHygieneCLIReportFlagContracts(t *testing.T) {
	h := hygieneReportFixture(t)
	base := []string{"hygiene", "--repo", h.repo.Root, "--secrets", "off", "--generic", "warn"}
	if _, _, err := h.run(append(base, "report")...); err == nil || !strings.Contains(err.Error(), "no stored hygiene scan") {
		t.Fatalf("report without a stored scan: %v", err)
	}
	for _, args := range [][]string{
		{"report", "--range", "a..b"},
		{"report", "--file", "sample.txt"},
		{"report", "--audit"},
		{"report", "--report", "00000000-0000-4000-8000-000000000000", "--rescan"},
		{"report", "--rescan", "--top", "-1"},
		{"report", "--rescan", "--by", "owner"},
		{"report", "--rescan", "--disposition", "off"},
		{"report", "--report", "not-a-report"},
	} {
		if _, _, err := h.run(append(base, args...)...); err == nil {
			t.Errorf("invalid report flags accepted: %v", args)
		}
	}
}
