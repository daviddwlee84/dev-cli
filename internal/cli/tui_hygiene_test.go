package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/hygiene"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestDashboardHygieneUsesSelectedWorktreeAndNativeReport(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newStartFixture(t, runtime.None{})
	linked := filepath.Join(t.TempDir(), "linked checkout")
	f.repo.Git("worktree", "add", "-b", "hygiene-test", linked)
	policyDir := filepath.Join(linked, ".dev-cli")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Explicitly disabled policy makes the test independent of installed tools;
	// it must remain "skipped", never a clean scan.
	if err := os.WriteFile(filepath.Join(policyDir, "hygiene.toml"), []byte("version = 1\nsecrets = \"off\"\nknown = \"off\"\ngeneric = \"off\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := f.repo.GitIn(linked, "status", "--porcelain")
	for _, scope := range []string{"worktree", "staged", "history"} {
		f.stdout.Reset()
		request := &tui.HygieneRequest{Operation: "scan", Checkout: linked, Scope: scope}
		w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "hygiene", Hygiene: request})
		request.Checkout = f.repo.Root // the queued workflow owns its target copy
		if err := w.Run(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.stdout.String(), "Hygiene "+scope+" · skipped") || !w.Result().Scoped || w.Result().RefreshRepos {
			t.Fatalf("scan output/result: %s %+v", f.stdout.String(), w.Result())
		}
		service, err := hygiene.Open(t.Context(), hygiene.Options{Root: linked, StateDir: f.app.Cfg.StateDir()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.LatestReport(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
	f.stdout.Reset()
	w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "hygiene", Hygiene: &tui.HygieneRequest{Operation: "report", Checkout: linked}})
	if err := w.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.stdout.String(), "source bytes were not rechecked") || !strings.Contains(f.stdout.String(), "skipped") {
		t.Fatal(f.stdout.String())
	}
	if after := f.repo.GitIn(linked, "status", "--porcelain"); after != before {
		t.Fatalf("scan changed source/index: before=%q after=%q", before, after)
	}
	parent, err := hygiene.Open(t.Context(), hygiene.Options{Root: f.repo.Root, StateDir: f.app.Cfg.StateDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.LatestReport(t.Context(), ""); !errors.Is(err, hygiene.ErrNoStoredScan) {
		t.Fatalf("scan ran on parent checkout: %v", err)
	}
}

func TestDashboardHygieneStatusAndAbsentReportNeverRescan(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newStartFixture(t, runtime.None{})
	for _, operation := range []string{"status", "report"} {
		w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "hygiene", Hygiene: &tui.HygieneRequest{Operation: operation, Checkout: f.repo.Root}})
		err := w.Run()
		if operation == "status" && err != nil {
			t.Fatal(err)
		}
		if operation == "report" && (err == nil || !strings.Contains(err.Error(), "no stored hygiene scan")) {
			t.Fatalf("missing report: %v", err)
		}
		if w.Result().RefreshRepos || !w.Result().Scoped {
			t.Fatal("inspection requested unrelated refresh")
		}
	}
	service, err := hygiene.Open(t.Context(), hygiene.Options{Root: f.repo.Root, StateDir: f.app.Cfg.StateDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.LatestReport(t.Context(), ""); !errors.Is(err, hygiene.ErrNoStoredScan) {
		t.Fatal("inspection scanned implicitly")
	}
	for _, name := range []string{".dev-cli/hygiene.toml", ".gitleaks.toml", ".pre-commit-config.yaml"} {
		if _, err := os.Stat(filepath.Join(f.repo.Root, name)); !os.IsNotExist(err) {
			t.Fatalf("inspection configured %s", name)
		}
	}
}

func TestDashboardHygieneRejectsMissingTargetAndUnsupportedOperation(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	for _, request := range []*tui.HygieneRequest{nil, {Operation: "status"}, {Operation: "redact", Checkout: f.repo.Root}, {Operation: "scan", Checkout: f.repo.Root, Scope: "remote"}} {
		w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "hygiene", Hygiene: request})
		if err := w.Run(); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}
