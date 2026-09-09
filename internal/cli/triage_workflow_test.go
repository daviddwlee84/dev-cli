package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/triage"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type triageObservedRuntime struct{ runtime.None }

func (triageObservedRuntime) Name() string                                    { return "test" }
func (triageObservedRuntime) List(context.Context) ([]runtime.Session, error) { return nil, nil }

func triageCLISetup(t *testing.T) *startFixture {
	t.Helper()
	f := newStartFixture(t, triageObservedRuntime{})
	f.app.Cfg.Paths.StateDir = filepath.Join(t.TempDir(), "state")
	f.app.Cfg.Paths.TriesRoot = filepath.Join(t.TempDir(), "tries")
	f.app.Catalog = catalog.NewStore(f.app.Cfg.AssetsDir())
	f.app.Registry = catalog.NewRegistry(f.app.Catalog)
	if err := os.Mkdir(f.app.Cfg.Paths.TriesRoot, 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTryForgetCLIRequiresExactApproval(t *testing.T) {
	f := triageCLISetup(t)
	path := filepath.Join(f.app.Cfg.Paths.TriesRoot, "accidental")
	entry := &catalog.Entry{Kind: catalog.KindTry, Name: "accidental", Experiment: &catalog.Experiment{Phase: catalog.PhaseActive, Slug: "accidental", OriginalPath: path}, Locations: map[string]catalog.Location{config.Hostname(): {State: catalog.LocationPresent, CurrentPath: path}}}
	if err := f.app.Catalog.Create(entry); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		f.stdout.Reset()
		cmd := newTriesForgetCmd(f.app)
		cmd.SetContext(t.Context())
		if err := cmd.ParseFlags(args); err != nil {
			return err
		}
		return cmd.RunE(cmd, cmd.Flags().Args())
	}
	if err := run(entry.ID, "--dry-run", "--json"); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(f.stdout.Bytes()) {
		t.Fatal(f.stdout.String())
	}
	if _, err := f.app.Catalog.Get(entry.ID); err != nil {
		t.Fatal("preview deleted entry", err)
	}
	if err := run(entry.ID, "--confirm-forget", "wrong"); err == nil {
		t.Fatal("wrong approval accepted")
	}
	if err := run(entry.ID, "--confirm-forget", entry.ID, "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Catalog.Get(entry.ID); err == nil {
		t.Fatal("entry not forgotten")
	}
}

func TestTriageReturnRefreshIsScoped(t *testing.T) {
	f := triageCLISetup(t)
	f.app.Cfg.Paths.ScanRoots = []string{filepath.Join(t.TempDir(), "unavailable-unrelated-root")}
	if err := os.Mkdir(filepath.Join(f.app.Cfg.Paths.TriesRoot, "unselected"), 0700); err != nil {
		t.Fatal(err)
	}
	d, err := refreshTriageScope(t.Context(), f.app, tui.WorkflowRequest{LocalGeneration: 4}, []triage.Item{{RepositoryPath: f.repo.Root, Path: f.repo.Root, Kind: "repo"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Generation != 4 || !d.ReposValid || len(d.Repos) != 1 || len(d.Tries) != 0 || !d.TriesValid {
		t.Fatalf("%+v", d)
	}
	entries, err := f.app.Catalog.List()
	if err != nil || len(entries) != 0 {
		t.Fatal("return refresh enrolled unrelated Try", entries, err)
	}
}
