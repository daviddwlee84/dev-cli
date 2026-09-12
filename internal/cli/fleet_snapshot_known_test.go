package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestFleetSnapshotUnknownGitRemainsUnknownInCLI(t *testing.T) {
	known, unknown := true, false
	for _, tc := range []struct {
		name        string
		known       *bool
		wantUnknown bool
	}{{"legacy", nil, true}, {"failed", &unknown, true}, {"known", &known, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := &App{Out: &out, Err: &out}
			renderFleetList(app, []fleet.HostResult{{Name: "lab", State: fleet.HostOK, Snapshot: &fleet.Snapshot{Repositories: []fleet.RepoSnapshot{{Name: "repo", Display: "repo", Path: "/repos/repo", GitKnown: tc.known}}}}}, "")
			if strings.Contains(out.String(), "unknown") != tc.wantUnknown {
				t.Fatalf("Git observation presence lost: %s", out.String())
			}
			if tc.wantUnknown && strings.Contains(out.String(), "clean") {
				t.Fatalf("unknown Git state became clean: %s", out.String())
			}
		})
	}
}

func TestFleetSnapshotPreservesGitObservationPresence(t *testing.T) {
	rows := []tui.RepoRow{
		{Repo: repo.Repo{Name: "known", Path: "/repos/known"}, GitKnown: true},
		{Repo: repo.Repo{Name: "failed", Path: "/repos/failed"}},
		{Repo: repo.Repo{Name: "pending", Path: "/repos/pending"}, GitKnown: true, Pending: "refreshing"},
	}
	snapshot := fleetSnapshotFromRepoRows(rows, "none")
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded fleet.Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for i, r := range decoded.Repositories {
		if r.GitKnown == nil || *r.GitKnown != (i == 0) {
			t.Fatalf("%s git_known=%v", r.Name, r.GitKnown)
		}
	}
	var legacy fleet.RepoSnapshot
	if err := json.Unmarshal([]byte(`{"name":"old","path":"/repos/old","status":{}}`), &legacy); err != nil || legacy.GitKnown != nil {
		t.Fatalf("legacy snapshot became known: %+v %v", legacy, err)
	}
}
