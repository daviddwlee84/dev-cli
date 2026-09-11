package hygiene

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

type engineFunc func(context.Context, EngineRequest) ([]Detection, error)

func (f engineFunc) Scan(c context.Context, q EngineRequest) ([]Detection, error) { return f(c, q) }
func testService(t *testing.T) (*Service, *gittest.Repo) {
	t.Helper()
	r := gittest.New(t)
	s, err := Open(t.Context(), Options{Root: r.Root, StateDir: t.TempDir(), Override: Policy{Secrets: Off, Generic: Off}, Engine: engineFunc(func(context.Context, EngineRequest) ([]Detection, error) { return nil, nil })})
	if err != nil {
		t.Fatal(err)
	}
	s.Policy.Rules = []Rule{{ID: "private-host", Kind: "literal", Value: "private-host-unique", Replacement: "host.example.invalid"}}
	return s, r
}
func put(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if e := os.MkdirAll(filepath.Dir(p), 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), 0o644); e != nil {
		t.Fatal(e)
	}
}
func TestScanStagedUsesIndexAndIncludesHiddenIgnoredTracked(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, ".gitignore", "ignored\n")
	put(t, r.Root, ".specstory/history/test.md", "private-host-unique\n")
	put(t, r.Root, "ignored", "private-host-unique\n")
	r.Git("add", "-f", ".specstory/history/test.md", "ignored", ".gitignore")
	put(t, r.Root, "ignored", "safe worktree\n")
	put(t, r.Root, ".specstory/history/test.md", "safe worktree\n")
	report, err := s.Scan(t.Context(), ScanOptions{Scope: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Blocked != 2 || report.Files != 3 {
		t.Fatalf("report: %+v", report)
	}
	b, _ := json.Marshal(report)
	if bytes.Contains(b, []byte("private-host-unique")) {
		t.Fatal("finding value escaped into public JSON")
	}
	for _, f := range report.Findings {
		if f.CanRedact {
			t.Fatal("staged content must not authorize working file rewrite")
		}
	}
}
func TestRedactExactPlanPreservesIndexAndCanRestore(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "settings.txt", "before private-host-unique after\n")
	r.Git("add", "settings.txt")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PreviewRedact(t.Context(), report.ID, []string{"settings.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.Apply(t.Context(), plan.ID, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, "settings.txt"))
	if string(got) != "before host.example.invalid after\n" {
		t.Fatalf("after=%q", got)
	}
	index := r.Git("show", ":settings.txt")
	if !strings.Contains(index, "private-host-unique") {
		t.Fatal("index was changed")
	}
	if len(applied.Recovery) != 1 {
		t.Fatalf("recovery=%+v", applied)
	}
	if _, err = s.Restore(t.Context(), applied.Recovery[0], true, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(r.Root, "settings.txt"))
	if !strings.Contains(string(got), "private-host-unique") {
		t.Fatal("restore failed")
	}
}
func TestRedactRejectsStaleFileAndRequiresArtifactProof(t *testing.T) {
	s, r := testService(t)
	name := ".specstory/history/test.md"
	put(t, r.Root, name, "private-host-unique\n")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PreviewRedact(t.Context(), report.ID, []string{name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{}); err == nil {
		t.Fatal("missing writer proof accepted")
	}
	put(t, r.Root, name, "private-host-unique changed\n")
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{WriterStopped: true}); !errors.Is(err, ErrStale) {
		t.Fatalf("expected stale: %v", err)
	}
}
func TestScannerFailureAndChangedIndexAreNotClean(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "file", "plain\n")
	r.Git("add", "file")
	s.Policy.Secrets = Block
	s.Engine = engineFunc(func(context.Context, EngineRequest) ([]Detection, error) { return nil, errors.New("backend failed") })
	report, err := s.Scan(t.Context(), ScanOptions{Scope: "staged"})
	if err == nil || report.OK() || report.Status != "partial" {
		t.Fatalf("%+v %v", report, err)
	}
	s.Engine = engineFunc(func(ctx context.Context, q EngineRequest) ([]Detection, error) {
		put(t, r.Root, "new", "new\n")
		r.Git("add", "new")
		return nil, nil
	})
	report, err = s.Scan(t.Context(), ScanOptions{Scope: "staged"})
	if err == nil || report.OK() {
		t.Fatalf("changing index accepted: %+v", report)
	}
}
func TestSnapshotEngineHasNoHiddenPathOrFilterGap(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, ".hidden/file.txt", "secret-canary\n")
	s.Policy.Secrets = Block
	s.Engine = engineFunc(func(ctx context.Context, q EngineRequest) ([]Detection, error) {
		got, e := gitBytes(ctx, q.Root, nil, "show", ":.hidden/file.txt")
		if e != nil || string(got) != "secret-canary\n" {
			t.Fatalf("snapshot: %q %v", got, e)
		}
		return []Detection{{RuleID: "canary", File: ".hidden/file.txt", Secret: "secret-canary", StartLine: 1, EndLine: 1}}, nil
	})
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil || report.Blocked != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}
func TestHistoryIncludesDeletedIgnoredAndMergeOnlyContent(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "removed", "private-host-unique\n")
	r.Git("add", "removed")
	r.Git("commit", "-m", "add canary")
	r.Git("rm", "removed")
	put(t, r.Root, ".gitignore", "removed\n")
	r.Git("add", ".gitignore")
	r.Git("commit", "-m", "remove canary")
	r.Git("checkout", "-b", "side")
	put(t, r.Root, "side", "side\n")
	r.Git("add", "side")
	r.Git("commit", "-m", "side")
	r.Git("checkout", "main")
	r.Git("merge", "--no-ff", "--no-commit", "side")
	put(t, r.Root, "merge-only", "private-host-unique\n")
	r.Git("add", "merge-only")
	r.Git("commit", "-m", "merge canary")
	report, err := s.Scan(t.Context(), ScanOptions{Scope: "history"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, f := range report.Findings {
		files[f.File] = true
	}
	if !files["removed"] || !files["merge-only"] {
		t.Fatalf("missing history coverage: %+v", report)
	}
}
func TestRuleBoundariesCIDRAndJSONPaths(t *testing.T) {
	literal, e := compileRule(Rule{ID: "home", Kind: "literal", Value: `C:\Users\private`, Replacement: "user"})
	if e != nil {
		t.Fatal(e)
	}
	if n := len(literal.indices("f", []byte(`C:\Users\private C:\\Users\\private C:\Users\private-other`))); n != 2 {
		t.Fatalf("literal matches=%d", n)
	}
	c, e := compileRule(Rule{ID: "net", Kind: "cidr", Value: "2001:db8::/32"})
	if e != nil {
		t.Fatal(e)
	}
	if n := len(c.indices("f", []byte("2001:db8::1 fe80::1 [2001:db8:0:1::2]"))); n != 2 {
		t.Fatalf("CIDR matches=%d", n)
	}
}
func TestLocalExceptionIsExactAndPublicOnlyOmitsLocalValues(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "f", "private-host-unique\n")
	report, e := s.Scan(t.Context(), ScanOptions{})
	if e != nil {
		t.Fatal(e)
	}
	plan, e := s.PreviewAllow(t.Context(), report.ID, report.Findings[0].ID, "synthetic fixture")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Apply(t.Context(), plan.ID, ApplyOptions{}); e != nil {
		t.Fatal(e)
	}
	previousRules := s.Policy.Rules
	s, e = Open(t.Context(), Options{Root: r.Root, StateDir: filepath.Dir(filepath.Dir(filepath.Dir(s.Dir))), Override: Policy{Secrets: Off, Generic: Off}})
	if e != nil {
		t.Fatal(e)
	}
	s.Policy.Rules = previousRules
	report, e = s.Scan(t.Context(), ScanOptions{})
	if e != nil || report.Blocked != 0 || report.Findings[0].Disposition != "accepted" {
		t.Fatalf("%+v %v", report, e)
	}
}
func TestNativeGitleaksDoesNotAllowSameLinePlaceholder(t *testing.T) {
	if _, e := exec.LookPath("gitleaks"); e != nil {
		t.Skip("gitleaks unavailable")
	}
	s, r := testService(t)
	s.Policy.Secrets = Block
	s.Engine = Gitleaks{}
	fake := "sk-proj-" + strings.Repeat("aB3dE5gH7jK9mN2pQ4sT6vW8xY0z", 4)
	put(t, r.Root, ".hidden/f", "[REDACTED:example] "+fake+"\n")
	report, e := s.Scan(t.Context(), ScanOptions{Timeout: 30 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if report.Blocked == 0 {
		t.Fatalf("synthetic secret escaped: %+v", report)
	}
	wire, _ := json.Marshal(report)
	if bytes.Contains(wire, []byte(fake)) {
		t.Fatal("raw key in report")
	}
}
