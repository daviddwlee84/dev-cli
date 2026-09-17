package hygiene

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func valueCaptureBuilder(t *testing.T) *scanBuilder {
	t.Helper()
	s, _ := testService(t)
	if err := s.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &scanBuilder{
		s: s, findings: map[string]int{}, gapSet: map[string]bool{}, values: newValueCollector(),
		record: scanRecord{Root: s.Root, Report: Report{
			SchemaVersion: 1, Kind: "hygiene_scan", ID: newID(), RepoID: s.RepoID,
			Scope: "worktree", Status: "complete", Created: time.Now().UTC(),
			PolicyDigest: keyedID(s.key, policyDigest(s.Policy)),
		}},
	}
}

func TestValueCaptureWithoutCollectorDoesNotBuildContext(t *testing.T) {
	b := &scanBuilder{}
	calls := 0
	build := func() string {
		calls++
		return "context must not be built by an ordinary scan"
	}
	allocations := testing.AllocsPerRun(10, func() {
		b.captureValueContext("finding", "rule", "known", "file", "", 1, "value", build)
	})
	if calls != 0 || allocations != 0 {
		t.Fatalf("capture disabled: context calls=%d allocations=%g", calls, allocations)
	}
}

func TestValueCaptureSkipsOversizedValuesWithoutShorteningRetainedRaw(t *testing.T) {
	b := valueCaptureBuilder(t)
	tooLarge := strings.Repeat("x", maxCapturedValueBytes+1)
	for _, value := range []string{tooLarge, strings.Repeat("é", maxCapturedValueBytes/2+1)} {
		b.captureValueContext("large", "rule", "known", "file", "", 1, value, func() string {
			t.Fatal("oversized value requested context")
			return ""
		})
	}
	if !b.values.truncated || len(b.values.entries) != 0 || b.values.bytes != 0 {
		t.Fatalf("oversized capture retained data: entries=%d bytes=%d", len(b.values.entries), b.values.bytes)
	}
	retained := strings.Repeat("r", maxCapturedValueBytes)
	b.captureValue("boundary", "rule", "known", "file", "", 1, retained, "bounded context")
	if err := b.saveReport(); err != nil {
		t.Fatal(err)
	}
	if b.record.ValuesStatus != "truncated" || !b.record.ValuesTruncated || len(b.record.Values) != 1 || b.record.Values[0].Length != len(retained) {
		t.Fatalf("capture metadata = %+v", b.record.Values)
	}
	body, err := os.ReadFile(filepath.Join(b.s.Dir, b.record.ValuesReview))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("\n"+retained+"\n")) || bytes.Contains(body, []byte(tooLarge)) {
		t.Fatal("review did not preserve exactly the complete retained value")
	}
}

func TestValueCaptureAccountsForAggregateRawAndContextBudget(t *testing.T) {
	b := valueCaptureBuilder(t)
	for i := 0; i < maxCapturedBytes/maxCapturedValueBytes+2; i++ {
		value := fmt.Sprintf("%08d", i) + strings.Repeat("v", maxCapturedValueBytes-8)
		b.captureValue(fmt.Sprintf("finding-%d", i), "rule", "known", "file", "commit", 1, value, "sample context")
	}
	if !b.values.truncated || b.values.bytes > maxCapturedBytes {
		t.Fatalf("aggregate limit: truncated=%t bytes=%d", b.values.truncated, b.values.bytes)
	}
	actual := 0
	for _, entry := range b.values.entries {
		actual += len(entry.raw) + len(entry.rule) + len(entry.category)
		for _, sample := range entry.samples {
			actual += len(sample.file) + len(sample.commit) + len(sample.context)
		}
	}
	if actual != b.values.bytes || len(b.values.entries) == 0 || len(b.values.entries) >= maxCapturedBytes/maxCapturedValueBytes {
		t.Fatalf("accounted=%d actual=%d entries=%d", b.values.bytes, actual, len(b.values.entries))
	}
}

func TestValueCaptureBudgetExhaustionKeepsRetainedCountsExact(t *testing.T) {
	b := valueCaptureBuilder(t)
	b.captureValue("first", "rule", "known", "one", "", 1, "retained", "first context")
	entry := b.values.entries[b.values.order[0]]
	// Exercise exhaustion without allocating a full budget for every counter test.
	b.values.bytes = maxCapturedBytes
	noContext := func() string {
		t.Fatal("exhausted capture requested context")
		return ""
	}
	b.captureValueContext("second", "rule", "known", "two", "", 1, "retained", noContext)
	b.captureValueContext("second", "rule", "known", "two", "", 2, "retained", noContext)
	b.captureValueContext("new", "rule", "known", "three", "", 1, "not retained", noContext)
	if !b.values.truncated || b.values.bytes != maxCapturedBytes || len(b.values.entries) != 1 || len(entry.samples) != 1 || entry.occurrences != 3 || len(entry.files) != 2 || len(entry.findings) != 2 {
		t.Fatalf("retained counters: occurrences=%d files=%d findings=%d samples=%d entries=%d", entry.occurrences, len(entry.files), len(entry.findings), len(entry.samples), len(b.values.entries))
	}
}

func TestValueCaptureSkipsOversizedSamplesAndStopsBuildingAfterSampleLimit(t *testing.T) {
	b := valueCaptureBuilder(t)
	b.captureValue("oversized-context", "rule", "known", "first", "", 1, "value", strings.Repeat("c", maxValueContextBytes+1))
	entry := b.values.entries[b.values.order[0]]
	if !b.values.truncated || len(entry.samples) != 0 || entry.occurrences != 1 {
		t.Fatal("oversized context was retained or lost the occurrence")
	}
	calls := 0
	for i := 0; i < maxValueSamples+3; i++ {
		name := fmt.Sprintf("file-%d", i)
		b.captureValueContext(name, "rule", "known", name, "", 1, "value", func() string {
			calls++
			return "context"
		})
	}
	if calls != maxValueSamples || len(entry.samples) != maxValueSamples || entry.occurrences != maxValueSamples+4 || len(entry.files) != maxValueSamples+4 || len(entry.findings) != maxValueSamples+4 {
		t.Fatalf("calls=%d samples=%d occurrences=%d files=%d findings=%d", calls, len(entry.samples), entry.occurrences, len(entry.files), len(entry.findings))
	}
	b.captureValueContext("oversized-location", "rule", "known", strings.Repeat("f", maxValueMetadataBytes+1), "", 1, "other value", func() string {
		t.Fatal("oversized location requested context")
		return ""
	})
	other := b.values.entries[b.values.order[1]]
	if len(other.samples) != 0 || other.occurrences != 1 || len(other.files) != 1 {
		t.Fatal("oversized location was retained or lost the occurrence")
	}
}

func TestLineContextBoundsMatchAndBothSearchWindows(t *testing.T) {
	for _, tc := range []struct {
		name, before, match, after, want string
	}{
		{"line", "previous\nleft ", "value", " right\nnext", "left value right"},
		{"long line", strings.Repeat("a", 1<<20), "value", strings.Repeat("b", 1<<20), "…" + strings.Repeat("a", maxValueContext) + "value" + strings.Repeat("b", maxValueContext) + "…"},
		{"newline at window edge", "\n" + strings.Repeat("a", maxValueContext), "v", strings.Repeat("b", maxValueContext) + "\n", strings.Repeat("a", maxValueContext) + "v" + strings.Repeat("b", maxValueContext)},
		{"oversized match", "", strings.Repeat("x", maxCapturedValueBytes+1), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.before + tc.match + tc.after)
			got := lineContext(data, len(tc.before), len(tc.before)+len(tc.match))
			if got != tc.want || len(got) > maxValueContextBytes {
				t.Fatalf("context mismatch: got %d bytes, want %d", len(got), len(tc.want))
			}
		})
	}
	for _, bounds := range [][2]int{{-1, 0}, {0, 2}, {1, 0}, {0, -1}} {
		if got := lineContext([]byte("x"), bounds[0], bounds[1]); got != "" {
			t.Fatalf("invalid bounds %v produced context", bounds)
		}
	}
}

func TestValuesReviewBufferRejectsAppendBeforeExceedingLimit(t *testing.T) {
	review := valuesReviewBuffer{limit: 8}
	if _, err := fmt.Fprint(&review, "1234567"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(&review, "89"); err == nil || review.err == nil || review.String() != "1234567" {
		t.Fatal("rendered review exceeded its limit or partially appended a rejected write")
	}
	if _, err := fmt.Fprint(&review, "x"); err == nil || review.Len() != 7 {
		t.Fatal("rendering resumed after exceeding its limit")
	}
}

func TestValuesSidecarFailureRetainsPartialScanReceiptAndLatest(t *testing.T) {
	b := valueCaptureBuilder(t)
	id := b.add("private-host", "known", "file", "", 1, "private-host-unique", Warn, false)
	b.captureValue(id, "private-host", "known", "file", "", 1, "private-host-unique", "private context")
	b.record.Report.Warnings = 1
	// Refuse only the sidecar publication; the receipt directory remains writable.
	path := filepath.Join(b.s.Dir, b.record.Report.ID+valuesReviewSuffix)
	if err := os.WriteFile(path, []byte("existing private sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.saveReport(); err == nil || !strings.Contains(err.Error(), "scan receipt retained") {
		t.Fatalf("sidecar collision error = %v", err)
	}
	record, err := b.s.loadReport(t.Context(), b.record.Report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Report.Status != "partial" || record.Report.OK() || record.ValuesStatus != "failed" || len(record.Values) != 0 || record.ValuesReview != "" || record.ValuesDigest != "" || len(record.Report.Findings) != 1 || record.Report.Warnings != 1 || record.Report.Blocked != 0 {
		t.Fatalf("failure receipt status=%s values=%s findings=%d", record.Report.Status, record.ValuesStatus, len(record.Report.Findings))
	}
	if len(record.Report.Gaps) != 1 || record.Report.Gaps[0].Code != "values_capture_failed" {
		t.Fatalf("failure gaps = %+v", record.Report.Gaps)
	}
	latest, err := b.s.LatestReport(t.Context(), "")
	if err != nil || latest != record.Report.ID {
		t.Fatalf("latest=%q error=%v", latest, err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "existing private sidecar" {
		t.Fatal("failed sidecar publication altered an existing file")
	}
}

func TestDisabledPolicyCaptureSavesEmptySuccessfulValuesReview(t *testing.T) {
	s, _ := testService(t)
	s.Policy.Secrets, s.Policy.Known, s.Policy.Generic = Off, Off, Off
	report, err := s.Scan(t.Context(), ScanOptions{CaptureValues: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.loadReport(t.Context(), report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() || report.Status != "skipped" || record.ValuesStatus != "complete" || record.ValuesTruncated || len(record.Values) != 0 {
		t.Fatalf("disabled policy capture status=%s values=%s", report.Status, record.ValuesStatus)
	}
	if path, err := s.ReviewPath(t.Context(), report.ID); err != nil || !strings.HasSuffix(path, valuesReviewSuffix) {
		t.Fatalf("disabled policy review path=%q error=%v", path, err)
	}
}
