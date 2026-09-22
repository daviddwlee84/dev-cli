package cli

import (
	"context"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestRetainRemoteMetricsRequiresSameResource(t *testing.T) {
	previous := forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/repo", URL: "https://github.com/owner/repo", Metrics: &forgemetrics.Metrics{OpenPRs: forgemetrics.Known(2, time.Now())}}
	same := previous
	same.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(0, time.Now())}
	foreign := same
	foreign.URL = "https://other.example/owner/repo"
	invalid := previous
	invalid.URL = ""
	out := retainRemoteMetrics([]forge.RemoteRepo{previous, invalid}, []forge.RemoteRepo{same, foreign, {Forge: forge.GitHub, FullName: "invalid"}})
	if out[0].Metrics.OpenPRs == nil || *out[0].Metrics.Stars.Value != 0 {
		t.Fatal("fresh inventory lost previous observations or measured zero")
	}
	if out[1].Metrics.OpenPRs != nil || out[2].Metrics != nil {
		t.Fatal("stats crossed resource identity")
	}
}

func TestRemoteMetricsNeverQueryAnotherConfiguredHost(t *testing.T) {
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("PATH", t.TempDir())
	app := &App{Cfg: config.Default()}
	load := newTUIRemoteMetrics(func() *App { return app })
	var patches []tui.RemoteRow
	err := load(context.Background(), []tui.RemoteRow{{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/repo", URL: "https://unrelated.example/owner/repo"}}}, func(rows []tui.RemoteRow) { patches = append(patches, rows...) })
	if err != nil || len(patches) != 1 || patches[0].Repo.Metrics.OpenPRs.State != forgemetrics.StateError || patches[0].Repo.Metrics.OpenPRs.Error != "repository does not match the configured metrics endpoint" {
		t.Fatalf("unexpected metrics result: %+v %v", patches, err)
	}
}
