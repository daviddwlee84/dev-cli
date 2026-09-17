package hygiene

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/privatefile"
)

func summaryFixture(t *testing.T) (*Service, string) {
	t.Helper()
	s, r := testService(t)
	s.Policy.Generic = Warn
	put(t, r.Root, "a.txt", "private-host-unique private-host-unique\nuser@example.org\n")
	put(t, r.Root, "b.txt", "private-host-unique\n")
	put(t, r.Root, "c.txt", "a@example.org b@example.org a@example.org\n")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s, report.ID
}

func TestSummaryAggregatesSeverityRuleAndFileDeterministically(t *testing.T) {
	s, id := summaryFixture(t)
	summary, err := s.Summarize(t.Context(), SummaryRequest{By: []string{SummaryBySeverity, SummaryByRule, SummaryByFile, SummaryByCategory}})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Kind != "hygiene_summary" || summary.ReportID != id || !summary.PolicyCurrent || !summary.CheckoutCurrent {
		t.Fatalf("summary identity = %+v", summary)
	}
	if got := summary.Totals; got.Findings != 5 || got.Occurrences != 7 || got.Blocked != 2 || got.Warnings != 3 || got.Files != 3 || got.Rules != 2 {
		t.Fatalf("totals = %+v", got)
	}
	wantSeverities := []SeveritySummary{{"block", 2, 3}, {"warn", 3, 4}, {"accepted", 0, 0}}
	if !reflect.DeepEqual(summary.Severities, wantSeverities) {
		t.Fatalf("severities = %+v", summary.Severities)
	}
	if len(summary.Rules) != 2 || summary.Rules[0].Rule != "private-host" || summary.Rules[0].Occurrences != 3 || summary.Rules[0].Files != 2 ||
		summary.Rules[0].DistinctValues == nil || *summary.Rules[0].DistinctValues != 1 ||
		summary.Rules[1].Rule != "privacy-email" || summary.Rules[1].Findings != 3 || *summary.Rules[1].DistinctValues != 3 {
		t.Fatalf("rules = %+v", summary.Rules)
	}
	var files []string
	for _, file := range summary.Files {
		files = append(files, file.File+":"+file.Disposition)
	}
	if !reflect.DeepEqual(files, []string{"a.txt:block", "b.txt:block", "c.txt:warn"}) {
		t.Fatalf("files = %v", files)
	}
	if len(summary.Categories) != 2 || summary.Categories[0].Category != "known" {
		t.Fatalf("categories = %+v", summary.Categories)
	}

	top, err := s.Summarize(t.Context(), SummaryRequest{ReportID: id, By: []string{SummaryByRule}, Top: 1, Findings: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Rules) != 1 || top.Omitted.Rules != 1 || top.Severities != nil || top.Files != nil ||
		len(top.Findings) != 1 || top.Findings[0].Rule != "private-host" || top.Omitted.Findings != 4 {
		t.Fatalf("top summary = %+v", top)
	}
	filtered, err := s.Summarize(t.Context(), SummaryRequest{ReportID: id, Dispositions: []string{"warn"}, Paths: []string{"c.*"}})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Totals.Findings != 2 || filtered.Totals.Occurrences != 3 || filtered.Totals.Blocked != 0 {
		t.Fatalf("filtered totals = %+v", filtered.Totals)
	}
	for _, bad := range []SummaryRequest{{Top: -1}, {By: []string{"owner"}}, {Dispositions: []string{"off"}}, {Categories: []string{"privacy"}}, {ReportID: "../x"}} {
		if _, err := s.Summarize(t.Context(), bad); err == nil {
			t.Errorf("invalid request accepted: %+v", bad)
		}
	}
}

func TestSummaryCountsAcceptedAndKeepsPartialVisible(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "f", "private-host-unique\n")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PreviewAllow(t.Context(), report.ID, report.Findings[0].ID, "synthetic fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	rules := s.Policy.Rules
	s, err = Open(t.Context(), Options{Root: r.Root, StateDir: filepath.Dir(filepath.Dir(filepath.Dir(s.Dir))), Override: Policy{Secrets: Off, Generic: Off}})
	if err != nil {
		t.Fatal(err)
	}
	s.Policy.Rules = rules
	put(t, r.Root, "broken.md", "ok \xe4\n")
	if _, err = s.Scan(t.Context(), ScanOptions{}); err == nil {
		t.Fatal("scan with an encoding gap reported complete")
	}
	summary, err := s.Summarize(t.Context(), SummaryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "partial" || summary.Totals.Gaps != 1 || len(summary.Gaps) != 1 || summary.Totals.Accepted != 1 || summary.Totals.Blocked != 0 {
		t.Fatalf("partial accepted summary = %+v", summary)
	}
	if summary.Severities[2].Disposition != "accepted" || summary.Severities[2].Findings != 1 {
		t.Fatalf("severities = %+v", summary.Severities)
	}
}

func TestLatestPointersArePerCheckoutAndIgnoreSnapshots(t *testing.T) {
	s, r := testService(t)
	state := filepath.Dir(filepath.Dir(filepath.Dir(s.Dir)))
	put(t, r.Root, "f", "private-host-unique\n")
	r.Git("add", "f")
	r.Git("commit", "-m", "fixture")
	worktreeReport, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InspectSnapshot(t.Context(), "f", []byte("private-host-unique\n"), true); err != nil {
		t.Fatal(err)
	}
	if latest, err := s.LatestReport(t.Context(), ""); err != nil || latest != worktreeReport.ID {
		t.Fatalf("latest after snapshot = %q %v", latest, err)
	}
	if _, err := s.LatestReport(t.Context(), "staged"); !errors.Is(err, ErrNoStoredScan) {
		t.Fatalf("missing staged scan error = %v", err)
	}

	linked := filepath.Join(t.TempDir(), "linked")
	r.Git("worktree", "add", "-b", "linked", linked)
	other, err := Open(t.Context(), Options{Root: linked, StateDir: state, Override: Policy{Secrets: Off, Generic: Off}})
	if err != nil {
		t.Fatal(err)
	}
	other.Policy.Rules = s.Policy.Rules
	if other.Dir != s.Dir {
		t.Fatalf("linked worktree state dir %q != %q", other.Dir, s.Dir)
	}
	if _, err := other.LatestReport(t.Context(), ""); !errors.Is(err, ErrNoStoredScan) {
		t.Fatalf("linked worktree saw the canonical checkout's scan: %v", err)
	}
	staged, err := other.Scan(t.Context(), ScanOptions{Scope: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	if latest, _ := other.LatestReport(t.Context(), "staged"); latest != staged.ID {
		t.Fatalf("linked staged latest = %q want %q", latest, staged.ID)
	}
	if latest, _ := s.LatestReport(t.Context(), ""); latest != worktreeReport.ID {
		t.Fatalf("canonical latest changed to %q", latest)
	}
	summary, err := other.Summarize(t.Context(), SummaryRequest{ReportID: worktreeReport.ID})
	if err != nil || summary.CheckoutCurrent {
		t.Fatalf("explicit foreign-checkout report = %+v %v", summary, err)
	}
}

func TestSummaryRejectsPlanAndForeignRecords(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "settings.txt", "private-host-unique\n")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PreviewRedact(t.Context(), report.ID, []string{"settings.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(t.Context(), SummaryRequest{ReportID: plan.ID}); err == nil {
		t.Fatal("plan record summarized as a scan report")
	}
	if _, err := s.Summarize(t.Context(), SummaryRequest{ReportID: report.ID, Scope: "staged"}); err == nil {
		t.Fatal("report scope mismatch accepted")
	}
	foreign, _ := testService(t)
	if _, err := foreign.Summarize(t.Context(), SummaryRequest{ReportID: report.ID}); err == nil {
		t.Fatal("another repository's report was summarized")
	}
	if path, err := s.ReviewPath(t.Context(), plan.ID); err != nil || !strings.HasSuffix(path, plan.ID+".review.txt") {
		t.Fatalf("plan review path = %q %v", path, err)
	}
	if _, err := s.ReviewPath(t.Context(), report.ID); !errors.Is(err, ErrValuesNotCaptured) {
		t.Fatalf("report without values review error = %v", err)
	}
}

func TestFindingValueIDStableAcrossFiles(t *testing.T) {
	s, r := testService(t)
	s.Policy.Generic = Warn
	put(t, r.Root, "one.txt", "same@example.org\n")
	put(t, r.Root, "two.txt", "same@example.org other@example.org\n")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string][]string{}
	for _, finding := range report.Findings {
		if finding.ValueID == "" || finding.ValueID == finding.ID {
			t.Fatalf("finding value id = %+v", finding)
		}
		ids[finding.ValueID] = append(ids[finding.ValueID], finding.File)
	}
	if len(ids) != 2 {
		t.Fatalf("distinct value ids = %v", ids)
	}
	for _, files := range ids {
		if len(files) == 2 && files[0] == files[1] {
			t.Fatalf("value ids did not separate files: %v", ids)
		}
	}
}

func TestMaskValueByCategory(t *testing.T) {
	s, _ := testService(t)
	for _, tc := range []struct{ category, rule, value, want string }{
		{"secret", "generic-api-key", "keychain-pkcs11", "ke•••11(15)"},
		{"secret", "generic-api-key", "short", "•••(5)"},
		{"known", "private-host", "private-host-unique", "[private:19]"},
		{"generic", "privacy-email", "daviddwlee84@gmail.com", "d•••@g•••.com"},
		{"generic", "privacy-ip", "192.168.31.172", "192.168.•.•"},
		{"generic", "privacy-ipv6", "2001:db8::1", "2001:•••"},
		{"generic", "privacy-home-path", "/Users/zhouhanru", "Users/z•••"},
		{"generic", "privacy-home-path", "/home/dev", "home/d•••"},
		{"generic", "privacy-home-path", `C:\Users\dev`, "Users/d•••"},
		{"generic", "custom", "anything", "•••(8)"},
	} {
		if got := s.maskValue(tc.category, tc.rule, tc.value); got != tc.want {
			t.Errorf("mask %s/%s = %q want %q", tc.category, tc.rule, got, tc.want)
		}
	}
}

func TestValuesCaptureNeverPersistsRawValuesOutsidePrivateReview(t *testing.T) {
	s, r := testService(t)
	s.Policy.Generic = Warn
	put(t, r.Root, "notes.txt", "host private-host-unique\ncontact person@example.org\n")
	report, err := s.Scan(t.Context(), ScanOptions{CaptureValues: true})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summarize(t.Context(), SummaryRequest{ReportID: report.ID, By: []string{SummaryByRule}, Values: true, Findings: true})
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(summary)
	record, err := os.ReadFile(filepath.Join(s.Dir, report.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"private-host-unique", "person@example.org"} {
		if bytes.Contains(public, []byte(raw)) || bytes.Contains(record, []byte(raw)) {
			t.Fatalf("raw value %q escaped the private review", raw)
		}
	}
	if len(summary.Rules) != 2 || len(summary.Rules[0].Values) != 1 || summary.Rules[0].Values[0].Masked != "[private:19]" ||
		summary.Rules[1].Values[0].Masked != "p•••@e•••.org" || summary.Rules[1].Values[0].Occurrences != 1 {
		t.Fatalf("masked values = %+v", summary.Rules)
	}
	path, err := s.ReviewPath(t.Context(), report.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// POSIX mode bits are not Windows ACLs. Verify the native owner/privacy
	// contract on both platforms rather than skipping the Windows assertion.
	if err := privatefile.Check(path, info, false); err != nil {
		t.Fatalf("values review privacy: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !bytes.Contains(body, []byte("PRIVATE REVIEW")) || !bytes.Contains(body, []byte("person@example.org")) || !bytes.Contains(body, []byte("notes.txt:2")) {
		t.Fatalf("values review lacks raw value or location:\n%s", body)
	}
	if err := os.WriteFile(path, append(body, []byte("tampered\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewPath(t.Context(), report.ID); !errors.Is(err, ErrStale) {
		t.Fatalf("tampered values review error = %v", err)
	}

	plain, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(t.Context(), SummaryRequest{ReportID: plain.ID, Values: true}); !errors.Is(err, ErrValuesNotCaptured) {
		t.Fatalf("values without capture error = %v", err)
	}
}
