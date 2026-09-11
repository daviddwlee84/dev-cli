package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

func herdrCatalog() FleetHerdrCatalog {
	return FleetHerdrCatalog{Status: "ready", ObservedAt: time.Now(), Profiles: []FleetHerdrProfile{
		{ID: "one", Label: "Builder", Target: "ssh-alpha", Session: "agents", Enabled: true},
		{ID: "two", Label: "Notebook", Target: "ssh-alpha", Session: "work", Enabled: false},
	}}
}

func TestFleetHerdrSharedReadUpdatesRowsAndWaitingMenu(t *testing.T) {
	native, menus, ssh := 0, 0, 0
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { native++; return herdrCatalog(), nil },
		ListFleetHostActions: func(_ context.Context, d FleetHostDescriptor, c FleetHerdrCatalog) ([]FleetHostAction, error) {
			menus++
			if c.Status != "ready" {
				t.Fatal(c)
			}
			return []FleetHostAction{{ID: "ssh", Label: "SSH"}}, nil
		},
		LoadFleetHost: func(context.Context, FleetHostDescriptor) (fleet.HostResult, error) {
			ssh++
			return fleet.HostResult{}, errors.New("offline")
		},
	}).WithFleetBackgroundRefresh(false)
	m, _ = treeAccept(m, treeHosts())
	if native != 0 {
		t.Fatal("descriptor read queried Herdr")
	}
	next, read := m.afterViewSwitch()
	m = next.(Model)
	treeSelect(t, &m, "a", "")
	next, duplicate := m.openActionMenuCommand()
	m = next.(Model)
	if duplicate != nil {
		t.Fatal("menu duplicated in-flight shared catalog query")
	}
	m = treeRun(t, m, read)
	if native != 1 || menus != 1 || ssh != 0 {
		t.Fatalf("native=%d menus=%d ssh=%d", native, menus, ssh)
	}
	rows := m.visibleFleet()
	if rows[0].Herdr != "1/2 enabled" || rows[1].Herdr != "not added" {
		t.Fatalf("metadata=%+v", rows)
	}
	m.overlay = overlayState{}
	m.filter = "Notebook work disabled"
	if rows := m.visibleFleet(); len(rows) != 1 || rows[0].HostKey != "a" {
		t.Fatalf("profile metadata not searchable: %+v", rows)
	}
	if native != 1 || ssh != 0 {
		t.Fatal("search caused IO")
	}
	m.filter = ""
	m.fleetTree.hosts[1].result = fleet.HostResult{State: fleet.HostUnreachable, Error: "offline"}
	if m.visibleFleet()[0].Herdr != "1/2 enabled" {
		t.Fatal("SSH failure erased local Herdr observation")
	}
}

func TestFleetHerdrFailurePreservesStaleTruthAndDisableIsNotChecked(t *testing.T) {
	response := herdrCatalog()
	var failure error
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { return response, failure }})
	m, _ = treeAccept(m, treeHosts())
	m = treeRun(t, m, m.requestFleetHerdr(false))
	response = FleetHerdrCatalog{Status: "ready"}
	failure = errors.New("partial catalog")
	m = treeRun(t, m, m.requestFleetHerdr(true))
	if m.visibleFleet()[0].Herdr != "~1/2 enabled" || m.fleetTree.herdr.latest.Status == "ready" {
		t.Fatal("partial ready+error became fresh empty")
	}
	response = FleetHerdrCatalog{Status: "unsupported"}
	failure = errors.New("machine CLI unavailable")
	m = treeRun(t, m, m.requestFleetHerdr(true))
	row := m.visibleFleet()[0]
	if row.Herdr != "~1/2 enabled" || !strings.Contains(row.HerdrDetail, "Stale") || m.fleetTree.herdr.latest.Status != "unsupported" {
		t.Fatalf("stale=%+v", row)
	}
	response = FleetHerdrCatalog{Status: "disabled", Detail: "--no-runtime"}
	failure = nil
	m = treeRun(t, m, m.requestFleetHerdr(true))
	if row := m.visibleFleet()[0]; row.Herdr != "not checked" || !strings.Contains(row.HerdrDetail, "--no-runtime") {
		t.Fatalf("disabled=%+v", row)
	}
	response = FleetHerdrCatalog{Status: "ready", ObservedAt: time.Now()}
	m = treeRun(t, m, m.requestFleetHerdr(true))
	if m.visibleFleet()[0].Herdr != "not added" {
		t.Fatal("fresh empty catalog was not accepted")
	}
}

func TestFleetHerdrUnknownIsNotUnregistered(t *testing.T) {
	for _, status := range []string{"unavailable", "unsupported", "invalid"} {
		t.Run(status, func(t *testing.T) {
			m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) {
				return FleetHerdrCatalog{Status: status}, errors.New("failed")
			}})
			m, _ = treeAccept(m, treeHosts())
			m = treeRun(t, m, m.requestFleetHerdr(false))
			if got := m.visibleFleet()[0].Herdr; got != "unknown" {
				t.Fatalf("status %s became %s", status, got)
			}
		})
	}
}

func TestFleetHerdrMutationCompletionRefreshesOnlyCatalog(t *testing.T) {
	catalogs, descriptors, snapshots := 0, 0, 0
	m := treeModel(Actions{LoadFleetHosts: func(context.Context) (FleetHostsResult, error) { descriptors++; return treeHosts(), nil }, LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { catalogs++; return herdrCatalog(), nil }, LoadFleetHost: func(context.Context, FleetHostDescriptor) (fleet.HostResult, error) {
		snapshots++
		return fleet.HostResult{}, nil
	}})
	m, _ = treeAccept(m, treeHosts())
	d := m.fleetTree.hosts[1].descriptor
	m.fleetTree.background = false
	m.pendingFleetHandoff = &FleetHandoff{host: d, action: "herdr-remove:one:fingerprint"}
	m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{RefreshHerdr: true}, nil)
	m = treeRun(t, m, m.Init())
	if catalogs != 1 || descriptors != 0 || snapshots != 0 || m.fleetTree.afterAction != "" {
		t.Fatalf("catalog=%d descriptors=%d snapshots=%d", catalogs, descriptors, snapshots)
	}
}

func TestFleetHerdrEndpointReloadDiscardsEarlierCatalog(t *testing.T) {
	var oldCtx context.Context
	m := treeModel(Actions{LoadFleetHerdr: func(ctx context.Context) (FleetHerdrCatalog, error) { oldCtx = ctx; return herdrCatalog(), nil }})
	m, _ = treeAccept(m, treeHosts())
	oldRead := m.requestFleetHerdr(false)
	old := oldRead().(fleetHerdrMsg)
	_ = m.beginFleetHostsLoad()
	if oldCtx.Err() == nil {
		t.Fatal("configuration refresh did not cancel old catalog read")
	}
	hosts := treeHosts()
	hosts.Hosts[1].EndpointID = "replacement"
	hosts.Hosts[1].SSHAlias = "other-alias"
	m, latest := treeAccept(m, hosts)
	m, _ = treeSend(m, old)
	if m.fleetTree.herdr.catalog.Status == "ready" {
		t.Fatal("old generation was accepted")
	}
	m = treeRun(t, m, latest)
	if m.visibleFleet()[0].Herdr != "not added" {
		t.Fatal("new alias inherited old association")
	}
}

func TestFleetHerdrColumnSurvivesNarrowWidthAndChildrenAreBlank(t *testing.T) {
	cached := treeSnapshot("alpha", "repo")
	hosts := treeHosts()
	hosts.Hosts[1].Cached = &cached
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { return herdrCatalog(), nil }})
	m, _ = treeAccept(m, hosts)
	m = treeRun(t, m, m.requestFleetHerdr(false))
	m.copyFleetHosts()
	m.fleetTree.hosts[1].expanded = true
	for _, width := range []int{60, 80, 100, 160} {
		m.width = width
		lines := strings.Split(ansi.Strip(m.renderFleetTree()), "\n")
		if !strings.Contains(lines[0], "HERDR") {
			t.Fatalf("width=%d %s", width, lines[0])
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d overflows: %s", width, line)
			}
		}
		if strings.Contains(lines[2], "enabled") {
			t.Fatalf("child repeated host metadata: %s", lines[2])
		}
		if width == 160 && strings.Contains(lines[1], "posix") {
			t.Fatal("OS is still under BRANCH")
		}
	}
	treeSelect(t, &m, "a", "")
	if !strings.Contains(m.renderDetail(), "posix") {
		t.Fatal("OS not present in host details")
	}
}

func TestFleetLocalHiddenTogglePinnedLastAndCoverage(t *testing.T) {
	m := treeModel(Actions{})
	m, _ = treeAccept(m, treeHosts())
	if m.fleetCount() != 2 || strings.Contains(m.fleetCoverage(), "3 hosts") {
		t.Fatal(m.fleetCoverage())
	}
	m.filter = "local-repo"
	if len(m.visibleFleet()) != 0 {
		t.Fatal("search revealed hidden local")
	}
	next, cmd := m.openActionMenuCommand()
	m = next.(Model)
	if cmd != nil {
		t.Fatal("empty menu caused IO")
	}
	index := -1
	for i := 0; i < m.overlay.optionCount; i++ {
		if m.overlay.options[i].action == listActionFleetLocal {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("empty menu has no local toggle")
	}
	m.overlay.optionIndex = index
	next, cmd = m.runOverlayAction()
	m = next.(Model)
	if cmd != nil || !m.showLocalFleet || m.fleetTree.hosts[0].expanded {
		t.Fatal("show did not retain collapsed local default")
	}
	if len(m.visibleFleet()) != 2 {
		t.Fatal("shown local known repo not searchable")
	}
	m.filter = ""
	treeSelect(t, &m, "b", "")
	m.tableSorts[int(ViewFleet)] = tableSort{column: "host", descending: true}
	treeSelect(t, &m, "b", "")
	rows := m.visibleFleet()
	if !rows[len(rows)-1].Local || len(rows) != 3 {
		t.Fatal("local not pinned last")
	}
	next, _ = m.toggleFleetLocal()
	m = next.(Model)
	if row, _ := m.currentFleet(); row.HostKey != "b" {
		t.Fatal("hiding local moved remote selection")
	}
	next, _ = m.toggleFleetLocal()
	m = next.(Model)
	treeSelect(t, &m, "local", "")
	next, _ = m.toggleFleetLocal()
	m = next.(Model)
	if m.fleetCursor != 0 || m.visibleFleet()[0].Local {
		t.Fatal("hiding selected local did not select first remote")
	}
}

func TestFleetHerdrProfilePickerReachesAll64ExactIdentities(t *testing.T) {
	catalog := FleetHerdrCatalog{Status: "ready"}
	var actions []FleetHostAction
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("profile-%02d", i)
		catalog.Profiles = append(catalog.Profiles, FleetHerdrProfile{ID: id, Label: "Same label", Target: "ssh-alpha", Session: fmt.Sprintf("session-%02d", i), Enabled: true})
		for _, verb := range []string{"disable", "remove"} {
			actions = append(actions, FleetHostAction{ID: "herdr-" + verb + ":" + id + ":exact", Label: verb, ProfileID: id, ProfileLabel: "Same label", ProfileSession: fmt.Sprintf("session-%02d", i)})
		}
	}
	runID := ""
	native := 0
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { native++; return catalog, nil }, ListFleetHostActions: func(context.Context, FleetHostDescriptor, FleetHerdrCatalog) ([]FleetHostAction, error) {
		return actions, nil
	}, RunFleetHostAction: func(_ context.Context, _ FleetHostDescriptor, id string) (*FleetExecution, error) {
		runID = id
		return nil, nil
	}})
	m, _ = treeAccept(m, treeHosts())
	treeSelect(t, &m, "a", "")
	next, cmd := m.openActionMenuCommand()
	m = treeRun(t, next.(Model), cmd)
	index := -1
	for i := 0; i < m.overlay.optionCount; i++ {
		if m.overlay.options[i].action == listActionFleetProfiles {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("missing profile selector")
	}
	m.overlay.optionIndex = index
	next, cmd = m.runOverlayAction()
	m = treeRun(t, next.(Model), cmd)
	if m.overlay.optionCount != 64 || m.overlay.options[63].fleetProfile != "profile-63" {
		t.Fatalf("profile picker truncated: %d", m.overlay.optionCount)
	}
	m.overlay.optionIndex = 63
	next, cmd = m.runOverlayAction()
	m = treeRun(t, next.(Model), cmd)
	if m.overlay.optionCount != 2 {
		t.Fatalf("profile action count=%d", m.overlay.optionCount)
	}
	m.overlay.optionIndex = 1
	next, cmd = m.runOverlayAction()
	m = treeRun(t, next.(Model), cmd)
	if runID != "herdr-remove:profile-63:exact" || native != 1 {
		t.Fatalf("action=%s native=%d", runID, native)
	}
}

func TestFleetHerdrProfilePickerCancelHasNoMutation(t *testing.T) {
	runs := 0
	m := treeModel(Actions{RunFleetHostAction: func(context.Context, FleetHostDescriptor, string) (*FleetExecution, error) { runs++; return nil, nil }})
	m, _ = treeAccept(m, treeHosts())
	treeSelect(t, &m, "a", "")
	m.overlay = overlayState{kind: overlayActionMenu, fleetHost: m.fleetTree.hosts[1].descriptor}
	m.fleetTree.menuActions = []FleetHostAction{{ProfileID: "one", ProfileLabel: "One", ID: "herdr-remove:one:exact"}, {ProfileID: "two", ProfileLabel: "Two", ID: "herdr-remove:two:exact"}}
	next, cmd := m.openFleetProfiles()
	m = treeRun(t, next.(Model), cmd)
	next, cmd = m.updateOverlay(tea.KeyMsg{Type: tea.KeyEsc})
	m = treeRun(t, next.(Model), cmd)
	if runs != 0 || m.overlay.kind != overlayNone {
		t.Fatal("profile cancel mutated")
	}
}

func TestFleetHerdrConnectionOverridesKeepAliasObservation(t *testing.T) {
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { return herdrCatalog(), nil }})
	hosts := treeHosts()
	hosts.Hosts[1].ConnectionNote = "fleet user/port overrides are not used by Herdr"
	m, _ = treeAccept(m, hosts)
	m = treeRun(t, m, m.requestFleetHerdr(false))
	row := m.visibleFleet()[0]
	if row.Herdr != "1/2 enabled" || !strings.Contains(row.HerdrDetail, "overrides") {
		t.Fatalf("alias observation lost: %+v", row)
	}
}

func TestFleetHerdrLateMenuResponseUsesCurrentCatalogGeneration(t *testing.T) {
	catalog := herdrCatalog()
	derived := 0
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { return catalog, nil }, ListFleetHostActions: func(_ context.Context, _ FleetHostDescriptor, c FleetHerdrCatalog) ([]FleetHostAction, error) {
		derived++
		return []FleetHostAction{{ID: fmt.Sprintf("herdr-remove:g%d", len(c.Profiles)), Label: "remove"}}, nil
	}})
	m, _ = treeAccept(m, treeHosts())
	treeSelect(t, &m, "a", "")
	next, load := m.openActionMenuCommand()
	m = next.(Model)
	m, oldMenu := treeSend(m, load())
	if oldMenu == nil {
		t.Fatal("menu derivation missing")
	}
	catalog.Profiles = catalog.Profiles[:1]
	m = treeRun(t, m, m.requestFleetHerdr(true))
	m = treeRun(t, m, oldMenu)
	last := m.overlay.options[m.overlay.optionCount-1]
	if last.fleetID != "herdr-remove:g1" || derived != 2 {
		t.Fatalf("stale menu installed: %+v derived=%d", last, derived)
	}
}

func TestFleetHerdrRefreshSortingCannotRetargetSelectedHost(t *testing.T) {
	var targets []string
	m := treeModel(Actions{LoadFleetHerdr: func(context.Context) (FleetHerdrCatalog, error) { return herdrCatalog(), nil }, LoadFleetHost: func(_ context.Context, d FleetHostDescriptor) (fleet.HostResult, error) {
		targets = append(targets, d.Key)
		return treeSnapshot(d.Name), nil
	}})
	hosts := treeHosts()
	hosts.Hosts[2].SSHAlias = ""
	m, _ = treeAccept(m, hosts)
	m = treeRun(t, m, m.requestFleetHerdr(false))
	m.tableSorts[int(ViewFleet)] = tableSort{column: "herdr"}
	treeSelect(t, &m, "a", "")
	next, command := m.refreshSelectedFleetHost()
	m = treeRun(t, next.(Model), command)
	if len(targets) != 1 || targets[0] != "a" {
		t.Fatalf("refresh retargeted after loading sort: %v", targets)
	}
}
