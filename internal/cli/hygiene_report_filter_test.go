package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/hygiene"
)

func TestHygieneReportValidatesGlobBeforeCaptureAndPreservesCommaPath(t *testing.T) {
	h := hygieneReportFixture(t)
	base := []string{"hygiene", "--repo", h.repo.Root, "--secrets", "off", "--generic", "warn"}
	if _, _, err := h.run(append(base, "report", "--rescan", "--values", "--path", "[")...); err == nil || !strings.Contains(err.Error(), "invalid --path glob") {
		t.Fatalf("invalid glob did not fail before scanning: %v", err)
	}
	if _, _, err := h.run(append(base, "report")...); err == nil || !strings.Contains(err.Error(), "no stored hygiene scan") {
		t.Fatalf("invalid filter unexpectedly produced a scan receipt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.repo.Root, "comma,name.txt"), []byte("reader@mail.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun(append(base, "--json", "report", "--rescan", "--file", "comma,name.txt", "--path", "comma,name.txt")...)
	var summary hygiene.Summary
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Findings != 1 || summary.Totals.Files != 1 || len(summary.Filters.Paths) != 1 || summary.Filters.Paths[0] != "comma,name.txt" {
		t.Fatalf("comma in a single path pattern split the filter: %+v", summary)
	}
}
