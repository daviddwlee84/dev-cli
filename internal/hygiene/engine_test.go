package hygiene

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("DEV_HYGIENE_SCANNER_TEST_HELPER") == "malformed" {
		for i, arg := range os.Args {
			if arg == "--report-path" && i+1 < len(os.Args) {
				_ = os.WriteFile(os.Args[i+1], []byte("not-json"), 0o600)
			}
		}
		_, _ = os.Stderr.WriteString("private diagnostic must never escape")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestMalformedEngineReportNeverBecomesClean(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "f", "safe\n")
	s.Policy.Secrets = Block
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_HYGIENE_SCANNER_TEST_HELPER", "malformed")
	s.Engine = Gitleaks{Binary: binary}
	report, err := s.Scan(context.Background(), ScanOptions{})
	if err == nil || report.OK() || report.Status != "partial" {
		t.Fatal("malformed report was accepted")
	}
	if strings.Contains(err.Error(), "private diagnostic") {
		t.Fatal("private stderr escaped")
	}
}
func TestPrivateReviewImageIsBoundToPlan(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "f", "private-host-unique\n")
	report, e := s.Scan(t.Context(), ScanOptions{})
	if e != nil {
		t.Fatal(e)
	}
	plan, e := s.PreviewRedact(t.Context(), report.ID, []string{"f"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	path, e := s.ReviewPath(t.Context(), plan.ID)
	if e != nil {
		t.Fatal(e)
	}
	if filepath.Dir(path) != s.Dir {
		t.Fatal("review escaped private state")
	}
	if e = os.WriteFile(path, []byte("different proposal"), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Apply(t.Context(), plan.ID, ApplyOptions{}); e == nil {
		t.Fatal("changed review accepted")
	}
}
