package tui

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestFleetNavigationKeysSeparateTreeMenuAndTerminal(t *testing.T) {
	navigated := 0
	actions := Actions{NavigateFleet: func(context.Context, FleetRow) (*FleetExecution, error) {
		navigated++
		return &FleetExecution{Run: func(context.Context, io.Reader, io.Writer, io.Writer) (FleetExecutionResult, error) {
			return FleetExecutionResult{Summary: "done"}, nil
		}}, nil
	}}
	m := treeModel(actions).WithFleetBackgroundRefresh(false)
	cached := treeSnapshot("alpha", "child")
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &cached
	hosts.Hosts[1].CacheFresh = true
	m, _ = treeAccept(m, hosts)
	treeSelect(t, &m, "a", "")
	m, command := treeSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if command != nil || m.overlay.kind != overlayNone || !m.fleetTree.hosts[1].expanded || navigated != 0 {
		t.Fatal("Space did more than expand")
	}
	treeSelect(t, &m, "a", "/src/child")
	m, _ = treeSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if r, _ := m.currentFleet(); r.Repository != nil || r.HostKey != "a" || m.fleetTree.hosts[1].expanded {
		t.Fatal("child Space did not collapse/select parent")
	}
	m, command = treeSend(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.fleetTerminalActive || m.fleetTree.hosts[1].expanded {
		t.Fatal("Enter expanded instead of starting navigation")
	}
	m = treeRun(t, m, command)
	if navigated != 1 || m.FleetHandoff() == nil || !m.quitting {
		t.Fatal("navigation did not end old Program before native execution")
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{' '}}, {Type: tea.KeyCtrlO}} {
		m, command = treeSend(m, key)
		if command != nil {
			t.Fatal("inactive list acted on old input")
		}
	}
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{Summary: "prepared; select the machine in sidebar"}, nil)
	if m.fleetTerminalActive || m.FleetHandoff() != nil || navigated != 1 {
		t.Fatal("completion navigated again")
	}
	m, command = treeSend(m, tea.KeyMsg{Type: tea.KeyEnter})
	_ = command()
	if navigated != 2 {
		t.Fatal("first fresh Enter was swallowed")
	}
}

func TestSpaceIsNoopInFlatDashboardLists(t *testing.T) {
	for _, view := range []View{ViewTasks, ViewTries, ViewRemote, ViewSkills, ViewMCP} {
		m := treeModel(Actions{})
		m.view = view
		m.rows = []inventory.Row{{Task: &task.Task{ID: "fixture", State: task.Hot}}}
		m, command := treeSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
		if command != nil || m.overlay.kind != overlayNone || m.view != view {
			t.Fatalf("Space acted in %s", view)
		}
	}
}

func TestFleetLocalEnterNavigatesReposWithoutTerminal(t *testing.T) {
	m := treeModel(Actions{})
	m, _ = treeAccept(m, treeHosts())
	m.showLocalFleet = true
	treeSelect(t, &m, "local", "")
	m, command := treeSend(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.view != ViewRepos || command != nil || m.FleetHandoff() != nil {
		t.Fatal("local host Enter did not navigate to REPOS")
	}
}

func TestFleetSearchCollapseIsTemporary(t *testing.T) {
	cached := treeSnapshot("alpha", "match")
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &cached
	hosts.Hosts[1].CacheFresh = true
	m := treeModel(Actions{})
	m, _ = treeAccept(m, hosts)
	m.copyFleetHosts()
	m.fleetTree.hosts[1].expanded = true
	m.filter = "match"
	treeSelect(t, &m, "a", "/src/match")
	m, _ = treeSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if len(m.visibleFleet()) != 1 || !m.fleetTree.hosts[1].expanded {
		t.Fatal("filtered collapse changed saved expansion")
	}
	m, _ = treeSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	if len(m.visibleFleet()) != 3 || !m.visibleFleet()[0].Expanded {
		t.Fatal("clearing search did not restore saved expansion")
	}
}

func TestFleetResumeKeepsCompletedSnapshotsAndNavigation(t *testing.T) {
	reads := 0
	m := treeModel(Actions{Reload: func(context.Context) ([]inventory.Row, error) { reads++; return nil, nil }, ReloadRepos: func(context.Context) ([]RepoRow, error) { reads++; return nil, nil }, Tries: TryActions{Reload: func(context.Context, bool) ([]TryRow, error) { reads++; return nil, nil }}}).WithFleetBackgroundRefresh(false)
	cached := treeSnapshot("alpha", "selected")
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &cached
	m, _ = treeAccept(m, hosts)
	m.copyFleetHosts()
	m.fleetTree.hosts[1].expanded = true
	m.filter = "selected"
	treeSelect(t, &m, "a", "/src/selected")
	m.overlay = overlayState{kind: overlayActionMenu, title: "kept", selection: m.currentToken()}
	beforeRows := append([]RepoRow(nil), m.repos...)
	beforeOverlay := m.overlay
	m.pendingFleetHandoff = &FleetHandoff{host: m.fleetTree.hosts[1].descriptor, action: "navigate"}
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{Summary: "returned"}, nil)
	if command := m.Init(); command != nil {
		m = treeRun(t, m, command)
	}
	if reads != 0 || !reflect.DeepEqual(beforeRows, m.repos) || m.filter != "selected" || !m.fleetTree.hosts[1].expanded || !reflect.DeepEqual(m.overlay, beforeOverlay) {
		t.Fatal("resume reloaded or reset accepted state")
	}
	if row, _ := m.currentFleet(); row.Repository == nil || row.Repository.Name != "selected" {
		t.Fatal("resume lost selection")
	}
}

func TestFleetResumeRestartsOnlyInterruptedLocalGeneration(t *testing.T) {
	oldCtx, cancel := context.WithCancel(t.Context())
	taskReads, repoReads := 0, 0
	m := treeModel(Actions{Reload: func(context.Context) ([]inventory.Row, error) { taskReads++; return nil, nil }, ReloadRepos: func(ctx context.Context) ([]RepoRow, error) {
		repoReads++
		name := "fresh"
		if ctx.Err() != nil {
			name = "obsolete"
		}
		return []RepoRow{{Repo: repo.Repo{Name: name, Path: "/src/" + name}, GitKnown: true}}, nil
	}}).WithContext(oldCtx).WithFleetBackgroundRefresh(false)
	m, _ = treeAccept(m, treeHosts())
	m.beginViewLoad(ViewRepos, loadRefresh)
	oldCommand := m.reloadReposOnly()
	oldGeneration := m.viewLoad(ViewRepos).generation
	m.pendingFleetHandoff = &FleetHandoff{host: m.fleetTree.hosts[1].descriptor}
	cancel()
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{}, nil)
	if m.viewLoad(ViewRepos).generation <= oldGeneration {
		t.Fatal("generation was reused")
	}
	m = treeRun(t, m, m.Init())
	m, _ = treeSend(m, oldCommand())
	if taskReads != 0 || repoReads != 2 || m.viewLoad(ViewRepos).loading || m.repos[0].Repo.Name != "fresh" {
		t.Fatalf("tasks=%d repos=%d state=%+v rows=%+v", taskReads, repoReads, m.viewLoad(ViewRepos), m.repos)
	}
}

func TestFleetResumeRestartsPendingHostCacheAndHerdr(t *testing.T) {
	reads, caches, catalogs := 0, 0, 0
	m := treeModel(Actions{LoadFleetHost: func(ctx context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		reads++
		return treeSnapshot(d.Name, "fresh"), nil
	}, LoadFleetHostCache: func(context.Context, FleetHostDescriptor) (*fleet.HostResult, bool, error) {
		caches++
		return nil, false, nil
	}, LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { catalogs++; return herdrCatalog(), nil }}).WithFleetBackgroundRefresh(false)
	m, _ = treeAccept(m, treeHosts())
	m.copyFleetHosts()
	m.fleetTree.hosts[1].loading = true
	m.fleetTree.hosts[1].request = 7
	m.fleetTree.nextRequest = 7
	m.fleetTree.herdr.loading = true
	m.fleetTree.herdr.generation = 3
	oldGeneration := m.fleetTree.generation
	m.pendingFleetHandoff = &FleetHandoff{host: m.fleetTree.hosts[1].descriptor}
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{}, nil)
	m = treeRun(t, m, m.Init())
	old := treeSnapshot("alpha", "obsolete")
	m, _ = treeSend(m, fleetHostMsg{key: "a", endpoint: "a1", request: 7, result: old})
	m, _ = treeSend(m, fleetHostCacheMsg{key: "a", endpoint: "a1", generation: oldGeneration, request: 7, result: &old})
	if reads != 1 || caches != 2 || catalogs != 1 || m.fleetTree.hosts[1].loading || m.fleetTree.hosts[1].result.Snapshot.Repositories[0].Name != "fresh" {
		t.Fatalf("reads=%d caches=%d catalogs=%d", reads, caches, catalogs)
	}
}

func TestFleetResumePreservesStaleCatalogAndPartialFailureEvidence(t *testing.T) {
	m := treeModel(Actions{}).WithFleetBackgroundRefresh(false)
	m, _ = treeAccept(m, treeHosts())
	m.fleetTree.herdr.catalog = herdrCatalog()
	m.fleetTree.herdr.stale = true
	m.fleetTree.herdr.latest = FleetHerdrCatalog{Status: "unavailable", Detail: "old probe failed"}
	m.pendingFleetHandoff = &FleetHandoff{host: m.fleetTree.hosts[1].descriptor}
	failure := errors.New("could not activate client")
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{Summary: "Added profile; enabled machine; prepared workspace"}, failure)
	if !m.fleetTree.herdr.stale || !errors.Is(m.err, failure) {
		t.Fatal("resume promoted stale facts or lost original error")
	}
	m = m.openActionMenu()
	text := m.currentStatusText()
	if !strings.Contains(text, "Added profile") || !strings.Contains(text, failure.Error()) || !errors.Is(m.err, failure) {
		t.Fatal(text)
	}
}

func TestFleetResumePendingSharedLocalLoaderHasFreshRequests(t *testing.T) {
	starts := 0
	m := treeModel(Actions{Local: LocalActions{Start: func(ctx context.Context, r LocalLoadRequest) LocalLoad {
		starts++
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		results := make(chan LocalResult, 3)
		results <- LocalResult{View: ViewTasks, Generation: r.TasksGeneration, Valid: true}
		results <- LocalResult{View: ViewRepos, Generation: r.ReposGeneration, Valid: true}
		results <- LocalResult{View: ViewTries, Generation: r.TriesGeneration, Valid: true}
		close(results)
		return LocalLoad{ID: 1, Results: results}
	}}}).WithFleetBackgroundRefresh(false).BeginLoading()
	m, _ = treeAccept(m, treeHosts())
	m.pendingFleetHandoff = &FleetHandoff{host: m.fleetTree.hosts[1].descriptor}
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{}, nil)
	m = treeRun(t, m, m.Init())
	if starts != 1 {
		t.Fatalf("shared starts=%d", starts)
	}
	for _, v := range []View{ViewTasks, ViewRepos, ViewTries} {
		if s := m.viewLoad(v); s.loading || s.freshness != perftrace.FreshnessFresh {
			t.Fatalf("%s remains pending: %+v", v, s)
		}
	}
}

func TestFleetResumeRestartsInterruptedSelectedTopology(t *testing.T) {
	reads := 0
	m := treeModel(Actions{LoadRepoTopology: func(ctx context.Context, r repo.Repo) (gitx.RecoveryTopology, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		reads++
		return gitx.RecoveryTopology{}, nil
	}}).WithFleetBackgroundRefresh(false)
	m, _ = treeAccept(m, treeHosts())
	m.repos[0].TopologyPending = true
	m.topologyRequested = []string{m.repos[0].Repo.Path}
	m.pendingFleetHandoff = &FleetHandoff{host: m.fleetTree.hosts[1].descriptor}
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{}, nil)
	if reads != 0 || m.viewLoad(ViewRepos).loading {
		t.Fatal("resume reloaded complete REPOS inventory")
	}
	m, command := treeSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = treeRun(t, m, command)
	if reads != 1 || m.repos[0].TopologyPending {
		t.Fatal("canceled selection topology remained stuck")
	}
}
