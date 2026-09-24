package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func dashboardGraduateFixture(t *testing.T) (*startFixture, experiment.Item) {
	t.Helper()
	f := newStartFixture(t, runtime.None{})
	f.app.Cfg.Paths.TriesRoot = filepath.Join(t.TempDir(), "tries")
	f.app.Cfg.Paths.ProjectRoot = filepath.Join(t.TempDir(), "projects")
	f.app.Catalog = catalog.NewStore(f.app.Cfg.AssetsDir())
	f.app.Registry = catalog.NewRegistry(f.app.Catalog)
	service, err := newExperimentService(f.app)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), experiment.CreateRequest{Name: "prototype"})
	if err != nil {
		t.Fatal(err)
	}
	return f, created.Item
}

func TestDashboardGraduateSharedWizardCancellationAndHandoff(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "apply"}[confirm], func(t *testing.T) {
			f, item := dashboardGraduateFixture(t)
			w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "graduate-try", Try: tui.TryRow{Item: item}})
			answer := "n"
			if confirm {
				answer = "y"
			}
			w.SetStdin(strings.NewReader("renamed\n\nlocal\n" + answer + "\n"))
			if err := w.Run(); err != nil {
				t.Fatalf("%v\n%s", err, f.stdout.String())
			}
			entry, err := f.app.Catalog.Get(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			location, _ := entry.LocationFor(config.Hostname())
			if !confirm {
				if w.Result().AfterExit != nil || w.Result().Status != "canceled" || entry.Kind != catalog.KindTry || location.CurrentPath != item.Live.CurrentPath {
					t.Fatalf("cancellation changed the Try: %+v %+v", w.Result(), entry)
				}
				if _, err := os.Stat(item.Live.CurrentPath); err != nil {
					t.Fatal(err)
				}
				return
			}
			if entry.Kind != catalog.KindRepository || entry.Name != "renamed" || entry.Experiment.GraduatedName != "renamed" || entry.Experiment.OriginalPath != item.Live.CurrentPath {
				t.Fatalf("graduation lost identity or naming: %+v", entry)
			}
			if w.Result().AfterExit == nil {
				t.Fatal("successful graduation did not defer navigation")
			}
			beforeHandoff := f.stdout.Len()
			if err := w.Result().AfterExit(); err != nil {
				t.Fatal(err)
			}
			if f.stdout.Len() <= beforeHandoff {
				t.Fatal("deferred no-runtime navigation did not emit its shell handoff")
			}
			f.stdout.Reset()
			cmd := newTriesListCmd(f.app)
			cmd.SetArgs([]string{item.ID, "--all", "--json"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var rows []tryJSONRow
			if err := json.Unmarshal(f.stdout.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Identity.ID != item.ID || rows[0].Experiment.GraduatedName != "renamed" || rows[0].Experiment.OriginalPath != item.Live.CurrentPath {
				t.Fatalf("graduated history JSON lost provenance: %+v", rows)
			}
		})
	}
}

func TestDashboardGraduateRejectsChangedSelectedCatalog(t *testing.T) {
	f, item := dashboardGraduateFixture(t)
	w := newTUIWorkflow(t.Context(), f.app, tui.WorkflowRequest{Action: "graduate-try", Try: tui.TryRow{Item: item}})
	w.SetStdin(strings.NewReader(""))
	if _, err := f.app.Registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Note = "updated after selection"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale selected catalog was accepted: %v", err)
	}
	if w.Result().AfterExit != nil || strings.Contains(f.stdout.String(), "Project name") {
		t.Fatal("stale selection reached the wizard or navigation")
	}
	if _, err := os.Stat(item.Live.CurrentPath); err != nil {
		t.Fatal(err)
	}
}

func TestGraduateRedirectedCharacterDevicesStayNoninteractive(t *testing.T) {
	f, item := dashboardGraduateFixture(t)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = null, null
	t.Cleanup(func() {
		os.Stdin, os.Stdout = stdin, stdout
		_ = null.Close()
	})
	f.app.In = null
	cmd := newGraduateCmd(f.app)
	cmd.SetArgs([]string{item.ID, "--name", "redirected"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	entry, err := f.app.Catalog.Get(item.ID)
	if err != nil || entry.Kind != catalog.KindRepository || entry.Name != "redirected" {
		t.Fatalf("redirected graduation prompted or silently canceled: %+v %v\n%s", entry, err, f.stdout.String())
	}
	if strings.Contains(f.stdout.String(), "Project name") || strings.Contains(f.stdout.String(), "Canceled") {
		t.Fatal("redirected character devices entered the wizard")
	}
}
