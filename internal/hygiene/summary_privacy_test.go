package hygiene

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryKeepsMaskedFilesDistinctAndFiltersOriginalPaths(t *testing.T) {
	s, r := testService(t)
	s.Policy.Generic = Warn
	s.Policy.Rules = []Rule{
		{ID: "private-first", Kind: "literal", Value: "node-alpha"},
		{ID: "private-second", Kind: "literal", Value: "node-beta"},
	}
	put(t, r.Root, "node-alpha.txt", "reader@mail.local reader@mail.local\n")
	put(t, r.Root, "node-beta.txt", "reader@mail.local\n")
	report, err := s.Scan(t.Context(), ScanOptions{CaptureValues: true})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summarize(t.Context(), SummaryRequest{ReportID: report.ID, Values: true, Findings: true})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.FileCountsComplete || summary.Totals.Files != 2 || len(summary.Files) != 2 ||
		summary.Rules[0].Files != 2 || summary.Rules[0].Values[0].Files != 2 {
		t.Fatalf("masked paths collapsed file counts: %+v", summary)
	}
	if summary.Files[0].File != summary.Files[1].File || summary.Files[0].FileID == summary.Files[1].FileID || summary.Files[0].FileID == "" {
		t.Fatalf("expected colliding labels with distinct identities: %+v", summary.Files)
	}
	request := SummaryRequest{ReportID: report.ID, Paths: []string{"node-alpha.txt"}}
	filtered, err := s.Summarize(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Totals.Files != 1 || filtered.Totals.Occurrences != 2 || request.Paths[0] != "node-alpha.txt" {
		t.Fatalf("filter did not match the original private path: %+v", filtered)
	}
	body, err := json.Marshal(filtered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "node-alpha") || strings.Contains(string(body), "node-beta") {
		t.Fatal("private path or filter leaked into the public summary")
	}
}

func TestSummaryMasksPrivateRuleFilterEcho(t *testing.T) {
	s, _ := summaryFixture(t)
	summary, err := s.Summarize(t.Context(), SummaryRequest{Rules: []string{"private-host-unique"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private-host-unique") {
		t.Fatal("private literal leaked through rule filter metadata")
	}
}

func TestSummaryRejectsMalformedPathGlobs(t *testing.T) {
	for _, pattern := range []string{"[", "file[abc", "trailing\\"} {
		if err := (SummaryRequest{Paths: []string{pattern}}).Validate(); err == nil {
			t.Fatalf("malformed glob %q accepted", pattern)
		}
	}
	for _, pattern := range []string{"dir/**", "*.txt", "file[ab].txt", "comma,name.txt"} {
		if err := (SummaryRequest{Paths: []string{pattern}}).Validate(); err != nil {
			t.Fatalf("valid glob %q rejected: %v", pattern, err)
		}
	}
}

func TestSummaryMarksLegacyFileCountsAsLowerBounds(t *testing.T) {
	s, id := summaryFixture(t)
	record, err := s.loadReport(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for i := range record.Report.Findings {
		record.Report.Findings[i].FileID = ""
	}
	record.FilePaths = nil
	if err := s.save(t.Context(), id, record); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summarize(t.Context(), SummaryRequest{ReportID: id})
	if err != nil || summary.FileCountsComplete || summary.Totals.Files != 3 {
		t.Fatalf("legacy summary must not promise exact file identities: %+v, %v", summary, err)
	}
}

func TestSummaryRetainsFailedValuesCaptureReport(t *testing.T) {
	s, id := summaryFixture(t)
	record, err := s.loadReport(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	record.ValuesStatus = "failed"
	record.Report.Status = "partial"
	record.Report.Gaps = append(record.Report.Gaps, Gap{Code: "values_capture_failed"})
	if err := s.save(t.Context(), id, record); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summarize(t.Context(), SummaryRequest{ReportID: id, Values: true})
	if err != nil || summary.ValuesStatus != "failed" || summary.Status != "partial" || summary.Totals.Gaps != 1 || summary.Totals.Findings == 0 {
		t.Fatalf("failed sidecar hid the saved scan receipt: %+v, %v", summary, err)
	}
}
