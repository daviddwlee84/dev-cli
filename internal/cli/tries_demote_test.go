package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func demoteFixture(t *testing.T) (*startFixture, experiment.GraduateResult, *activityRuntime) {
	t.Helper()
	rt := &activityRuntime{}
	f := newStartFixture(t, rt)
	f.app.runtimesByName = map[string]runtime.Runtime{"herdr": rt, "tmux": runtime.None{}, "zellij": runtime.None{}}
	f.app.Cfg.Paths.TriesRoot = filepath.Join(t.TempDir(), "tries")
	f.app.Cfg.Paths.ProjectRoot = filepath.Join(t.TempDir(), "projects")
	f.app.Catalog = catalog.NewStore(f.app.Cfg.AssetsDir())
	f.app.Registry = catalog.NewRegistry(f.app.Catalog)
	service, err := newExperimentService(f.app)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), experiment.CreateRequest{Name: "returning"})
	if err != nil {
		t.Fatal(err)
	}
	graduated, err := service.Graduate(t.Context(), experiment.GraduateRequest{Ref: created.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	return f, graduated, rt
}

func TestDemoteCLIPreviewThenMovePreservesCurrentFiles(t *testing.T) {
	f, graduated, _ := demoteFixture(t)
	file := filepath.Join(graduated.Plan.Destination, "uncommitted.txt")
	if err := os.WriteFile(file, []byte("keep current work"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		cmd := newTriesDemoteCmd(f.app)
		args := []string{graduated.Item.ID}
		if dryRun {
			args = append(args, "--dry-run")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("dry-run=%v: %v", dryRun, err)
		}
		entry, err := f.app.Catalog.Get(graduated.Item.ID)
		if err != nil {
			t.Fatal(err)
		}
		location, _ := entry.LocationFor(config.Hostname())
		if dryRun {
			if location.CurrentPath != graduated.Plan.Destination || entry.Kind != catalog.KindRepository {
				t.Fatal("preview changed catalog")
			}
		} else if location.CurrentPath != graduated.Plan.Source || entry.Kind != catalog.KindTry {
			t.Fatalf("wrong destination: %+v", entry)
		}
		bytes, err := os.ReadFile(filepath.Join(location.CurrentPath, "uncommitted.txt"))
		if err != nil || string(bytes) != "keep current work" {
			t.Fatalf("lost content: %q %v", bytes, err)
		}
	}
}

func TestDemoteObservesOtherInstalledRuntimeBackends(t *testing.T) {
	f, graduated, _ := demoteFixture(t)
	f.app.runtimesByName["tmux"] = &activityRuntime{name: "tmux", sessions: []runtime.Session{{Handle: "other-backend", WorkspaceCheckout: graduated.Plan.Destination}}}
	cmd := newTriesDemoteCmd(f.app)
	cmd.SetArgs([]string{graduated.Item.ID, "--dry-run"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "tmux") {
		t.Fatalf("ignored another backend: %v", err)
	}
}

type demoteConfirmReader struct {
	stage  int
	change func()
}

func (r *demoteConfirmReader) Read(p []byte) (int, error) {
	if r.stage > 1 {
		return 0, io.EOF
	}
	text := "\n"
	if r.stage == 1 {
		r.change()
		text = "y\n"
	}
	r.stage++
	return copy(p, text), nil
}

func TestDemoteWorkflowCancellationAndNewRuntimeClaim(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "new-claim"}[stale], func(t *testing.T) {
			f, graduated, rt := demoteFixture(t)
			request := tui.WorkflowRequest{Action: "demote-repo", Repo: repo.Repo{Path: graduated.Plan.Destination}, RepoAsset: graduated.Item.Entry}
			w := newTUIWorkflow(t.Context(), f.app, request)
			w.SetStdin(strings.NewReader("\nn\n"))
			if stale {
				w.SetStdin(&demoteConfirmReader{change: func() {
					rt.sessions = []runtime.Session{{Handle: "new-owner", Dirs: []string{graduated.Plan.Destination}}}
				}})
			}
			err := w.Run()
			if stale && err == nil {
				t.Fatal("new runtime claim did not reject apply")
			}
			if !stale && (err != nil || w.Result().Status != "canceled") {
				t.Fatalf("cancel=%+v %v", w.Result(), err)
			}
			entry, err := f.app.Catalog.Get(graduated.Item.ID)
			if err != nil || entry.Kind != catalog.KindRepository {
				t.Fatalf("catalog changed: %+v %v", entry, err)
			}
			if _, err := os.Stat(graduated.Plan.Destination); err != nil {
				t.Fatal("source moved despite cancellation/new claim")
			}
		})
	}
}
