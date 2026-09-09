package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

type triageWorkflowStub struct{}

func (*triageWorkflowStub) Run() error             { return nil }
func (*triageWorkflowStub) SetStdin(io.Reader)     {}
func (*triageWorkflowStub) SetStdout(io.Writer)    {}
func (*triageWorkflowStub) SetStderr(io.Writer)    {}
func (*triageWorkflowStub) Result() WorkflowResult { return WorkflowResult{Scoped: true} }

func TestDashboardTriageSelectionSurvivesFilteringAndUsesIdentity(t *testing.T) {
	var received WorkflowRequest
	a := Actions{Workflow: func(_ context.Context, r WorkflowRequest) (Workflow, error) {
		received = r
		return &triageWorkflowStub{}, nil
	}}
	rows := []RepoRow{{Repo: repo.Repo{Path: "/one", Name: "alpha", CommonDir: "/one/.git"}}, {Repo: repo.Repo{Path: "/two", Name: "two", CommonDir: "/two/.git"}}}
	m := New(a, nil, rows)
	m.view = ViewRepos
	m = m.selectVisibleTriage()
	before := m
	m.filter = "alpha"
	if !strings.Contains(m.selectionSummary(), "2 selected (1 hidden)") {
		t.Fatal(m.selectionSummary())
	}
	m = m.toggleTriageSelection()
	if len(before.repoSelections) != 2 || len(m.repoSelections) != 1 {
		t.Fatal("selection mutates copied model")
	}
	_, cmd := m.openTriageSelection(false)
	if cmd == nil || len(received.Selection) != 1 || received.Selection[0].Path != "/two" || len(received.Snapshots) != 1 {
		t.Fatalf("%+v", received)
	}
	if received.Selection[0].RepositoryID != "/two/.git" {
		t.Fatal("identity lost")
	}
}

func TestDashboardTrySelectionIncludesMissingAndUnregistered(t *testing.T) {
	var received WorkflowRequest
	m := New(Actions{Workflow: func(_ context.Context, r WorkflowRequest) (Workflow, error) {
		received = r
		return &triageWorkflowStub{}, nil
	}}, nil, nil)
	m.view = ViewTries
	m.tries = []TryRow{{Item: experiment.Item{ID: "missing", Kind: catalog.KindTry, Phase: catalog.PhaseActive, Live: experiment.LiveFacts{CurrentPath: "/tries/missing", Presence: "missing"}}, Location: &catalog.Location{State: catalog.LocationPresent}}, {Item: experiment.Item{Phase: catalog.PhaseActive, Live: experiment.LiveFacts{CurrentPath: "/tries/new", Present: true, Presence: "present"}}}}
	m = m.selectVisibleTriage()
	if len(m.trySelections) != 2 {
		t.Fatal(m.trySelections)
	}
	_, cmd := m.openTriageSelection(false)
	if cmd == nil || len(received.Selection) != 2 {
		t.Fatalf("%+v", received)
	}
	for _, target := range received.Selection {
		if target.Kind != "try" {
			t.Fatal(target)
		}
	}
}

func TestTriageReturnMergesOnlyAffectedRowsAndSizes(t *testing.T) {
	var measured []diskusage.Target
	reloads := 0
	m := New(Actions{Sizes: SizeActions{Start: func(_ context.Context, targets []diskusage.Target, force bool) diskusage.Load {
		measured = targets
		if !force {
			t.Fatal("affected cache not invalidated")
		}
		return diskusage.Load{}
	}}, ReloadRepos: func(context.Context) ([]RepoRow, error) { reloads++; return nil, nil }}, nil, []RepoRow{{Repo: repo.Repo{Path: "/one", CommonDir: "/one/.git"}}, {Repo: repo.Repo{Path: "/two", CommonDir: "/two/.git"}, NoteCount: 7}})
	d := TriageDelta{Targets: []triage.Target{{Path: "/one", RepositoryID: "/one/.git"}}, Repos: []RepoRow{{Repo: repo.Repo{Path: "/one", CommonDir: "/one/.git"}, NoteCount: 3, SizeTarget: diskusage.Plain("/one")}}, ReposValid: true, TriesValid: true, TasksValid: true}
	n, _ := m.Update(workflowMsg{result: WorkflowResult{Scoped: true, Local: &d}})
	updated := n.(Model)
	if len(updated.repos) != 2 || reloads != 0 || len(measured) != 1 || measured[0].Checkout != "/one" {
		t.Fatalf("repos=%+v probes=%+v", updated.repos, measured)
	}
	for _, r := range updated.repos {
		if r.Repo.Path == "/two" && r.NoteCount != 7 {
			t.Fatal("unrelated row changed")
		}
	}
	n, _ = updated.Update(workflowMsg{result: WorkflowResult{Scoped: true, Local: &d}})
	if n.(Model).localGeneration != updated.localGeneration {
		t.Fatal("accepted old delta")
	}
	n, cmd := updated.Update(workflowMsg{result: WorkflowResult{Scoped: true}})
	if cmd != nil || n.(Model).localGeneration != updated.localGeneration {
		t.Fatal("no-op workflow triggered rescan")
	}
}

func TestEnterWithoutSelectionKeepsNormalOpen(t *testing.T) {
	called := false
	m := New(Actions{OpenRepo: func(_ context.Context, r RepoRow) (OpenResult, error) { called = true; return OpenResult{}, nil }}, nil, []RepoRow{{Repo: repo.Repo{Path: "/one", Name: "one"}}})
	m.view = ViewRepos
	_, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("open missing")
	}
	cmd()
	if !called {
		t.Fatal("normal open changed")
	}
}

func TestHiddenSelectionCanBeClearedWithoutVisibleRow(t *testing.T) {
	m := New(Actions{Workflow: func(context.Context, WorkflowRequest) (Workflow, error) { return &triageWorkflowStub{}, nil }}, nil, []RepoRow{{Repo: repo.Repo{Path: "/one", Name: "alpha"}}})
	m.view = ViewRepos
	m = m.selectVisibleTriage()
	m.filter = "unmatched"
	m = m.openActionMenu()
	if m.overlay.optionCount != 2 {
		t.Fatal("hidden selection has no actions")
	}
	m.overlay.optionIndex = 1
	n, _ := m.runOverlayAction()
	if n.(Model).triageSelectionCount() != 0 {
		t.Fatal("could not clear hidden selection")
	}
}

func TestScopedSizesKeepUnrelatedLoadAndRejectOlderMeasurements(t *testing.T) {
	one, two := diskusage.Plain("/one"), diskusage.Plain("/two")
	initial := make(chan diskusage.Result)
	scoped := make(chan diskusage.Result)
	m := New(Actions{Sizes: SizeActions{Start: func(context.Context, []diskusage.Target, bool) diskusage.Load {
		return diskusage.Load{ID: 2, Results: scoped}
	}}}, nil, []RepoRow{{Repo: repo.Repo{Path: "/one"}, SizeTarget: one}, {Repo: repo.Repo{Path: "/two"}, SizeTarget: two}})
	m.sizeLoad = diskusage.Load{ID: 1, Results: initial}
	n, _ := m.beginScopedSizes([]diskusage.Target{one}, []diskusage.Target{one})
	m = n.(Model)
	if m.sizeLoad.ID != 1 {
		t.Fatal("unrelated initial load canceled")
	}
	n, _ = m.updateSize(sizeMsg{loadID: 1, result: diskusage.Result{Key: one.Key, Usage: diskusage.Usage{OwnedBytes: 1}}})
	m = n.(Model)
	if m.repos[0].Usage != nil {
		t.Fatal("stale affected size accepted")
	}
	n, _ = m.updateSize(sizeMsg{loadID: 1, result: diskusage.Result{Key: two.Key, Usage: diskusage.Usage{OwnedBytes: 2}}})
	m = n.(Model)
	if m.repos[1].Usage == nil {
		t.Fatal("unrelated size discarded")
	}
	n, _ = m.updateSize(sizeMsg{loadID: 2, result: diskusage.Result{Key: one.Key, Usage: diskusage.Usage{OwnedBytes: 3}}})
	m = n.(Model)
	if m.repos[0].Usage == nil || m.repos[0].Usage.OwnedBytes != 3 {
		t.Fatal("fresh scoped size discarded")
	}
}
