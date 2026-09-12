package hygiene

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("DEV_HYGIENE_SCANNER_TEST_HELPER") == "malformed" {
		if len(os.Args) == 2 && os.Args[1] == "version" {
			fmt.Println("8.30.0")
			os.Exit(0)
		}
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

func TestScannerVersionRequiresScopedAllowlistSupport(t *testing.T) {
	for version, want := range map[string]bool{"8.30.0": true, "v8.30.1": true, "8.31.0": true, "8.22.1": false, "9.0.0": false, "8.30.0-dev": false, "unknown": false} {
		if supportedScannerVersion(version) != want {
			t.Errorf("unexpected compatibility for %q", version)
		}
	}
}

func TestScannerBuildVersionSupportsPinnedGoInstallOnly(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Path: "github.com/zricethezav/gitleaks/v8", Version: "v8.30.1"}}
	if !supportedScannerBuild(info) {
		t.Fatal("pinned module rejected")
	}
	info.Main.Version = "v8.22.1"
	if supportedScannerBuild(info) {
		t.Fatal("old module accepted")
	}
	info.Main.Version = "(devel)"
	if supportedScannerBuild(info) {
		t.Fatal("unknown module accepted")
	}
	info.Main.Version = "v8.30.1"
	info.Main.Replace = &debug.Module{Path: "local"}
	if supportedScannerBuild(info) {
		t.Fatal("replaced module accepted")
	}
	info.Main.Replace = nil
	info.Main.Path = "unrelated/scanner"
	if supportedScannerBuild(info) {
		t.Fatal("unrelated module accepted")
	}
}
