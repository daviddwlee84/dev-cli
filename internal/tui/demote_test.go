package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestRepoDemoteActionRequiresGraduatedIdentity(t *testing.T) {
	for _, graduated := range []bool{false, true} {
		row := RepoRow{Repo: repo.Repo{Name: "project", Path: "/project"}}
		if graduated {
			row.Asset = &catalog.Entry{ID: "selected", Kind: catalog.KindRepository, Experiment: &catalog.Experiment{Phase: catalog.PhaseGraduated}}
		}
		var request *WorkflowRequest
		actions := Actions{Workflow: func(_ context.Context, r WorkflowRequest) (Workflow, error) {
			request = &r
			return registryWorkflow{}, nil
		}}
		m := New(actions, nil, []RepoRow{row})
		m.view = ViewRepos
		next, cmd := m.runListAction(listActionRepoDemote)
		if !graduated {
			if cmd != nil || request != nil || !strings.Contains(next.(Model).status, "previously graduated") {
				t.Fatal("ordinary repository was accepted")
			}
			continue
		}
		if cmd == nil || request == nil || request.Action != "demote-repo" || request.Repo.Path != row.Repo.Path || request.RepoAsset == row.Asset || request.RepoAsset.ID != row.Asset.ID {
			t.Fatalf("selection was not pinned: %+v", request)
		}
		row.Asset.Experiment.Phase = catalog.PhaseActive
		if request.RepoAsset.Experiment.Phase != catalog.PhaseGraduated {
			t.Fatal("selection aliases mutable row metadata")
		}
	}
}
