package hygiene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitleaksPasswordCorpus(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks unavailable")
	}
	s, r := testService(t)
	s.Policy.Secrets = Block
	s.Engine = Gitleaks{}
	// Runtime construction keeps detector-valid examples out of shipped bytes.
	secret := "corpus-" + strings.Repeat("z9", 12)
	put(t, r.Root, "code.go", "package demo\nvar x = struct { UsedPassword bool }{UsedPassword: usedPassword}\nfunc f() { password = resolved; r.password = password; usePassword = usePassword || r.UsedPassword }\n")
	put(t, r.Root, ".env.production", fmt.Sprintf("%s=%s\n%s=${FROM_VAULT}\n", "DATABASE_PASSWORD", secret, "PASSWORD"))
	jsonConfig, _ := json.Marshal(map[string]string{"password": secret})
	put(t, r.Root, "config.json", string(jsonConfig))
	put(t, r.Root, "literal.go", "package demo\nvar cfg = map[string]string{\"password\": \""+secret+"\"}\n")
	report, err := s.Scan(t.Context(), ScanOptions{Scope: "worktree"})
	if err != nil || report.Status != "complete" {
		t.Fatalf("scan incomplete: %v", err)
	}
	counts := map[string]int{}
	for _, f := range report.Findings {
		if f.Category == "secret" {
			counts[f.File]++
		}
	}
	if counts["code.go"] != 0 || counts[".env.production"] == 0 || counts["config.json"] == 0 || counts["literal.go"] == 0 {
		t.Fatalf("unexpected file findings: %v", counts)
	}
	data, _ := json.Marshal(report)
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("secret leaked into report")
	}
	// A reviewed exact fixture exception must not suppress another file/value.
	s.Policy.Exceptions = []Exception{{Rule: "generic-password-assignment", Path: "literal.go", Pattern: "^" + secret + "$", Reason: "synthetic corpus"}}
	report, err = s.Scan(t.Context(), ScanOptions{Scope: "worktree"})
	if err != nil || report.Blocked < 2 {
		t.Fatalf("exception widened coverage: %v", err)
	}
	for _, f := range report.Findings {
		if f.File == "literal.go" && f.Rule == "generic-password-assignment" && f.Disposition != "accepted" {
			t.Fatal("fixture exception not applied")
		}
	}
}

func TestQuotedPasswordRedactionPreservesDelimitersAndRemovesEscapes(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks unavailable")
	}
	s, r := testService(t)
	s.Policy.Secrets = Block
	s.Engine = Gitleaks{}
	value := "corpus-" + strings.Repeat("z9", 12) + "'quoted\"suffix"
	data, _ := json.Marshal(map[string]string{"password": value})
	put(t, r.Root, "quoted.json", string(data))
	put(t, r.Root, "quoted.py", fmt.Sprintf("%s = '%s'\n", "password", strings.ReplaceAll(value, "'", "\\'")))
	report, err := s.Scan(t.Context(), ScanOptions{Scope: "worktree"})
	if err != nil || report.Blocked != 2 {
		t.Fatalf("quoted scan failed: %v, count %d", err, report.Blocked)
	}
	plan, err := s.PreviewRedact(t.Context(), report.ID, []string{"quoted.json", "quoted.py"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(r.Root, "quoted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if json.Unmarshal(got, &decoded) != nil || !strings.HasPrefix(decoded["password"], "[REDACTED:") || strings.Contains(string(got), "suffix") {
		t.Fatal("quoted redaction left secret bytes or invalid JSON")
	}
	got, err = os.ReadFile(filepath.Join(r.Root, "quoted.py"))
	if err != nil || strings.Contains(string(got), "suffix") || !strings.Contains(string(got), "'[REDACTED:") {
		t.Fatal("single-quoted redaction left secret bytes")
	}
}
