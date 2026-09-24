package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
)

func TestTryGraduateUsesPinnedSharedWorkflow(t *testing.T) {
	entry := &catalog.Entry{ID: "selected", Kind: catalog.KindTry, Name: "prototype", Experiment: &catalog.Experiment{Phase: catalog.PhaseActive}}
	location := &catalog.Location{State: catalog.LocationPresent, CurrentPath: "/tries/prototype"}
	row := TryRow{Item: experiment.Item{ID: entry.ID, Entry: entry, Phase: catalog.PhaseActive}, Location: location}
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "available"}[available], func(t *testing.T) {
			var request *WorkflowRequest
			actions := Actions{Tries: TryActions{Apply: func(context.Context, TryRequest) (TryActionResult, error) {
				t.Fatal("graduate bypassed the reviewed workflow")
				return TryActionResult{}, nil
			}}}
			if available {
				actions.Workflow = func(_ context.Context, selected WorkflowRequest) (Workflow, error) {
					request = &selected
					return registryWorkflow{}, nil
				}
			}
			m := New(actions, nil, nil)
			m.view, m.tries = ViewTries, []TryRow{row}
			next, cmd := m.runListAction(listActionTryGraduate)
			if !available {
				if cmd != nil || request != nil || !strings.Contains(next.(Model).status, "unavailable") {
					t.Fatal("missing workflow did not disable graduation")
				}
				return
			}
			if cmd == nil || request == nil || request.Action != "graduate-try" || request.Try.Item.ID != entry.ID {
				t.Fatalf("wrong workflow selection: %+v", request)
			}
			if request.Try.Item.Entry == entry || request.Try.Location == location {
				t.Fatal("workflow aliases mutable row metadata")
			}
			entry.Name, location.CurrentPath = "changed", "/changed"
			if request.Try.Item.Entry.Name != "prototype" || request.Try.Location.CurrentPath != "/tries/prototype" {
				t.Fatal("selection changed after workflow creation")
			}
		})
	}
}
