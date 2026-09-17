package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/hygiene"
)

func TestHygieneSummaryFailedValuesAndStoredAgeAreVisible(t *testing.T) {
	var out bytes.Buffer
	h := hygieneCLI{app: &App{Out: &out}}
	h.renderSummary(hygiene.Summary{
		ReportID: "00000000-0000-4000-8000-000000000001", Scope: "staged", Status: "partial",
		Created: time.Date(2026, 9, 17, 1, 2, 3, 0, time.UTC), PolicyCurrent: true, CheckoutCurrent: true,
		ValuesShown: true, ValuesStatus: "failed", Gaps: []hygiene.Gap{{Code: "values_capture_failed"}},
	})
	for _, want := range []string{"2026-09-17T01:02:03Z", "source bytes were not rechecked", "file counts are lower bounds", "scan receipt is retained", "Scan incomplete"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in summary: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "hygiene review-path") {
		t.Fatal("failed capture advertised an unavailable review file")
	}
}
