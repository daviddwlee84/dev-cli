package hygiene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func goNoiseProgram() string {
	// Build detector reproductions at runtime, keeping them out of shipped bytes.
	access, secret := "S3AccessKeyEnv", "S3SecretKeyEnv"
	return fmt.Sprintf(`package demo
import "strings"
type spec struct { %s, %s string }
func f(s spec, value []byte, secrets map[string][]byte, err error) {
 _ = "%s=" + string(secrets["postgres_password"]) + "\n"
 _ = "%s=" + string(value) + "\n"
 if _ = []string{"%s="}; err == nil || !strings.Contains(err.Error(), "must not be empty") {}
 s.%s, s.%s = "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"
}
`, access, secret, "POSTGRES_PASSWORD", "password", "PASSWORD", access, secret)
}

func TestGoSourceContextPreservesLiteralCommentAndUncertainMatches(t *testing.T) {
	value := "s." + "S3SecretKeyEnv"
	for _, tc := range []struct {
		name, source, value string
		codeOnly            bool
	}{
		{"selector", "package p\nfunc f() { _ = " + value + " }", value, true},
		{"quoted", "package p\nvar x = " + fmt.Sprintf("%q", value), value, false},
		{"raw", "package p\nvar x = `" + value + "`", value, false},
		{"raw-crlf", "package p\nvar x = `\r\n\r\n" + value + "`", value, false},
		{"escaped", "package p\nvar x = " + fmt.Sprintf("%q", "a\""+value), value, false},
		{"comment", "package p\n// " + value, value, false},
		{"block-comment-crlf", "package p\n/*\r\n\r\n" + value + "*/", value, false},
		{"same-line-literal", "package p\nfunc f() { _ = " + value + "; _ = " + fmt.Sprintf("%q", value) + " }", value, false},
		{"invalid", "package p\nfunc f( { _ = " + value, value, false},
		{"not-found", "package p\nvar x = 1", value, false},
		{"line-directive", "package p\n//line other.go:200\nvar x = `" + value + "`", value, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.source)
			d := Detection{Secret: tc.value, StartLine: 1, EndLine: strings.Count(tc.source, "\n") + 1}
			if got := parseGoSource(data).codeOnly(data, d); got != tc.codeOnly {
				t.Fatalf("codeOnly = %v, want %v", got, tc.codeOnly)
			}
		})
	}
}

func TestSourceContextRulesPreserveCustomDetectors(t *testing.T) {
	for _, id := range []string{"generic-api-key", "generic-password-assignment", "generic-password-assignment-single-quoted"} {
		if !sourceContextRules(DefaultGitleaks)[id] {
			t.Fatalf("bundled rule %s not recognized", id)
		}
	}
	for _, tc := range []struct{ name, config, rule string }{
		{"custom-password", strings.Replace(string(DefaultGitleaks), "Quoted password / passphrase assignment", "Custom password semantics", 1), "generic-password-assignment"},
		{"custom-api", string(DefaultGitleaks) + "\n[[rules]]\nid = 'generic-api-key'\nregex = 'custom'\n", "generic-api-key"},
		{"path", strings.Replace(string(DefaultGitleaks), "useDefault = true", "path = 'other.toml'", 1), "generic-password-assignment"},
		{"url", strings.Replace(string(DefaultGitleaks), "useDefault = true", "url = 'https://host.invalid/rules'", 1), "generic-api-key"},
		{"invalid", "not TOML", "generic-api-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sourceContextRules([]byte(tc.config))[tc.rule] {
				t.Fatal("custom or unknown detector was eligible for suppression")
			}
		})
	}
}

func TestGitleaksGoNoiseAcrossScopes(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks unavailable")
	}
	for _, scope := range []string{"worktree", "staged", "history", "snapshot"} {
		t.Run(scope, func(t *testing.T) {
			s, r := testService(t)
			s.Policy.Secrets, s.Engine = Block, Gitleaks{}
			noise := goNoiseProgram()
			put(t, r.Root, "noise.go", noise)
			if scope == "staged" || scope == "history" {
				r.Git("add", "noise.go")
				if scope == "history" {
					r.Git("commit", "-m", "syntax fixture")
				}
				// Source verification must use the index or historical object.
				put(t, r.Root, "noise.go", "invalid working source")
			}
			var report Report
			var err error
			if scope == "snapshot" {
				result, e := s.InspectSnapshot(t.Context(), "noise.go", []byte(noise), false)
				report, err = result.Report, e
			} else {
				report, err = s.Scan(t.Context(), ScanOptions{Scope: scope, Audit: true})
			}
			if err != nil || report.Status != "complete" || report.Blocked != 0 {
				t.Fatalf("syntax-only source blocked: status=%s blocked=%d err=%v", report.Status, report.Blocked, err)
			}
		})
	}
}

func TestGitleaksGoNoiseDoesNotHideCredentials(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks unavailable")
	}
	s, r := testService(t)
	s.Policy.Secrets, s.Engine = Block, Gitleaks{}
	value := "corpus-" + strings.Repeat("q7", 15)
	assignment := fmt.Sprintf("%s = %q", "password", value)
	for name, source := range map[string]string{
		"literal.go": "package p\nvar " + assignment,
		"raw.go":     "package p\nvar text = `" + assignment + "`",
		"comment.go": "package p\n// " + assignment,
		"invalid.go": "package p\nfunc broken(\n" + assignment,
		"mixed.go":   strings.Replace(goNoiseProgram(), "_ = \"password=\" + string(value) + \"\\n\"", "_ = \"password=\" + string(value) + \"\\n\"; "+assignment, 1),
	} {
		put(t, r.Root, name, source)
	}
	report, err := s.Scan(t.Context(), ScanOptions{Audit: true})
	if err != nil || report.Status != "complete" {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range report.Findings {
		found[f.File] = true
	}
	for _, name := range []string{"literal.go", "raw.go", "comment.go", "invalid.go", "mixed.go"} {
		if !found[name] {
			t.Errorf("credential missed in %s", name)
		}
	}
	public, _ := json.Marshal(report)
	if bytes.Contains(public, []byte(value)) {
		t.Fatal("credential leaked into public report")
	}
}

func TestGitleaksGoHistoryRetainsDeletedAndMergeOnlyCredentials(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks unavailable")
	}
	s, r := testService(t)
	s.Policy.Secrets, s.Engine = Block, Gitleaks{}
	value := "corpus-" + strings.Repeat("v8", 15)
	source := fmt.Sprintf("package p\nvar %s = %q\n", "password", value)
	put(t, r.Root, "removed.go", source)
	r.Git("add", "removed.go")
	r.Git("commit", "-m", "old credential fixture")
	r.Git("rm", "removed.go")
	r.Git("commit", "-m", "remove fixture")
	r.Git("checkout", "-b", "side")
	put(t, r.Root, "side.txt", "side\n")
	r.Git("add", "side.txt")
	r.Git("commit", "-m", "side")
	r.Git("checkout", "main")
	r.Git("merge", "--no-ff", "--no-commit", "side")
	put(t, r.Root, "merged.go", source)
	r.Git("add", "merged.go")
	r.Git("commit", "-m", "merge fixture")
	put(t, r.Root, "merged.go", goNoiseProgram())
	report, err := s.Scan(t.Context(), ScanOptions{Scope: "history", Audit: true})
	if err != nil || report.Status != "complete" {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range report.Findings {
		if f.Category == "secret" {
			found[f.File] = true
		}
	}
	if !found["removed.go"] || !found["merged.go"] {
		t.Fatal("historical credential suppressed or unscanned")
	}
}
