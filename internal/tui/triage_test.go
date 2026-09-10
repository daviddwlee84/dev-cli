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

func TestDashboardScopedEntryUsesFilteredIdentity(t *testing.T) {
	var received WorkflowRequest
	m := New(Actions{Workflow: func(_ context.Context, r WorkflowRequest) (Workflow, error) {
		received = r
		return &triageWorkflowStub{}, nil
	}}, nil, []RepoRow{{Repo: repo.Repo{Path: "/one", Name: "alpha", CommonDir: "/one/.git"}}, {Repo: repo.Repo{Path: "/two", Name: "beta", CommonDir: "/two/.git"}}})
	m.view = ViewRepos
	m.filter = "alpha"
	_, cmd := m.openTriage("filtered")
	if cmd == nil || len(received.Selection) != 1 || received.Selection[0].RepositoryID != "/one/.git" || len(received.Snapshots) != 1 {
		t.Fatalf("%+v", received)
	}
	if strings.Contains(m.renderRepos(), "[ ]") {
		t.Fatal("dashboard retained organizer selection UI")
	}
	_, _ = m.openTriage("all")
	if !received.AllLocal {
		t.Fatal("all-local entry lost its scope")
	}
}
func TestDashboardTryEntryIncludesMissingAndUnregistered(t *testing.T) {
	var received WorkflowRequest
	m := New(Actions{Workflow: func(_ context.Context, r WorkflowRequest) (Workflow, error) {
		received = r
		return &triageWorkflowStub{}, nil
	}}, nil, nil)
	m.view = ViewTries
	m.tries = []TryRow{{Item: experiment.Item{ID: "missing", Kind: catalog.KindTry, Phase: catalog.PhaseActive, Live: experiment.LiveFacts{CurrentPath: "/tries/missing", Presence: "missing"}}, Location: &catalog.Location{State: catalog.LocationPresent}}, {Item: experiment.Item{Phase: catalog.PhaseActive, Live: experiment.LiveFacts{CurrentPath: "/tries/new", Present: true, Presence: "present"}}}}
	_, cmd := m.openTriage("filtered")
	if cmd == nil || len(received.Selection) != 2 {
		t.Fatalf("%+v", received)
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
