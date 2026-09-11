package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func sshKey(key string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)} }
func sshTestInventory(label string) SSHInventory {
	return SSHInventory{Complete: true, Sources: map[string]string{"ssh": "ready", "tailscale": "stale"}, Machines: []SSHRow{{ID: "machine-one", MachineID: "machine-one", Label: label, State: "managed", Profiles: []sshflow.ConnectionProfile{{ID: "profile-one", Alias: "box", Fingerprint: "source-fingerprint"}}}}}
}

type testSSHWorkflow struct{}

func (*testSSHWorkflow) Run() error                { return nil }
func (*testSSHWorkflow) SetStdin(io.Reader)        {}
func (*testSSHWorkflow) SetStdout(io.Writer)       {}
func (*testSSHWorkflow) SetStderr(io.Writer)       {}
func (*testSSHWorkflow) Result() SSHWorkflowResult { return SSHWorkflowResult{} }

func TestSSHViewLoadsOnlyPassiveInventoryIndependentlyOfRepositories(t *testing.T) {
	loads, workflows := 0, 0
	m := New(Actions{SSH: SSHActions{Load: func(context.Context) (SSHInventory, error) { loads++; return sshTestInventory("machine"), nil }, Workflow: func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error) {
		workflows++
		return &testSSHWorkflow{}, nil
	}}, ReloadFleet: func(context.Context) ([]FleetRow, error) { t.Fatal("SSH loaded fleet repositories"); return nil, nil }}, nil, nil)
	m.beginViewLoad(ViewRepos, loadInitial)
	_ = m.View()
	if loads != 0 || workflows != 0 {
		t.Fatal("render invoked SSH work")
	}
	next, command := m.updateList(sshKey("8"))
	m = next.(Model)
	if m.view != ViewSSH || command == nil || m.viewWaitsForRepos(ViewSSH) {
		t.Fatal("SSH depends on REPOS")
	}
	next, _ = m.Update(command())
	m = next.(Model)
	for range 2 {
		_ = m.View()
		m.openActionMenu()
		m.renderSSHDetail()
		m.visibleSSH()
	}
	if loads != 1 || workflows != 0 || !strings.Contains(m.View(), "tailscale=stale") {
		t.Fatalf("loads=%d workflows=%d view=%s", loads, workflows, m.View())
	}
	next, command = m.updateList(sshKey("r"))
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	if loads != 2 || workflows != 0 {
		t.Fatal("passive refresh invoked a workflow")
	}
}

func TestSSHViewCustomEightKeepsToolBindingAndTabAccess(t *testing.T) {
	m := New(Actions{Tools: []Tool{{Key: "8", Name: "custom-eight", Command: []string{"never-run"}}}}, nil, nil)
	next, command := m.updateList(sshKey("8"))
	m = next.(Model)
	if m.view != ViewTasks || command == nil {
		t.Fatal("custom 8 was stolen")
	}
	m.view = ViewMCP
	next, _ = m.updateList(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.view != ViewSSH {
		t.Fatal("Tab cannot reach SSH with custom 8")
	}
	if strings.Contains(m.buildHeaderLayout().line, "8 SSH") || !strings.Contains(m.renderFooter(), "1–7 / Tab") {
		t.Fatal("help advertises shadowed 8 shortcut")
	}
	m.view = ViewTasks
	for _, hit := range m.buildHeaderLayout().tabs {
		if hit.view == ViewSSH {
			next, _ := m.updateMouse(mouseMessage(hit.from, 0, tea.MouseButtonLeft, tea.MouseActionPress))
			m = next.(Model)
			break
		}
	}
	if m.view != ViewSSH {
		t.Fatal("tab click cannot reach SSH with custom 8")
	}
}

func TestSSHViewSupersededLoadsCancelAndKeepLatest(t *testing.T) {
	m := New(Actions{}, nil, nil).WithSSH(sshTestInventory("old"))
	m.view = ViewSSH
	oldGeneration := m.beginViewLoad(ViewSSH, loadRefresh)
	oldContext := m.viewContext(ViewSSH)
	newGeneration := m.beginViewLoad(ViewSSH, loadRefresh)
	if !errors.Is(oldContext.Err(), context.Canceled) {
		t.Fatal("superseded load was not canceled")
	}
	next, _ := m.Update(sshLoadedMsg{generation: newGeneration, inventory: sshTestInventory("new")})
	m = next.(Model)
	next, _ = m.Update(sshLoadedMsg{generation: oldGeneration, inventory: sshTestInventory("stale")})
	m = next.(Model)
	if m.ssh.Machines[0].Label != "new" {
		t.Fatal("older result replaced current SSH inventory")
	}
	generation := m.beginViewLoad(ViewSSH, loadRefresh)
	next, _ = m.Update(sshLoadedMsg{generation: generation, err: errors.New("source unavailable")})
	m = next.(Model)
	if m.ssh.Machines[0].Label != "new" || m.viewLoad(ViewSSH).freshness != perftrace.FreshnessStale {
		t.Fatal("failed refresh lost usable stale rows")
	}
	generation = m.beginViewLoad(ViewSSH, loadRefresh)
	next, _ = m.Update(sshLoadedMsg{generation: generation, inventory: SSHInventory{Complete: true}})
	m = next.(Model)
	if len(m.ssh.Machines) != 0 {
		t.Fatal("successful empty refresh retained obsolete rows")
	}
}

func TestSSHWorkflowPinsSelectionAndInvalidatesFleetWithoutFanout(t *testing.T) {
	var request SSHWorkflowRequest
	loads, fleetLoads := 0, 0
	m := New(Actions{SSH: SSHActions{Load: func(context.Context) (SSHInventory, error) { loads++; return sshTestInventory("machine"), nil }, Workflow: func(_ context.Context, r SSHWorkflowRequest) (SSHWorkflow, error) {
		request = r
		return &testSSHWorkflow{}, nil
	}}, ReloadFleet: func(context.Context) ([]FleetRow, error) { fleetLoads++; return nil, nil }}, nil, nil).WithSSH(sshTestInventory("machine"))
	m.view = ViewSSH
	m.seedViewSnapshot(ViewFleet, perftrace.SourceLive, perftrace.FreshnessFresh, true)
	next, command := m.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if command == nil || request.Action != "connect" || request.Selected.ID != "machine-one" {
		t.Fatalf("request=%+v", request)
	}
	next, command = m.Update(sshWorkflowMsg{result: SSHWorkflowResult{Status: "partial registrations", MembershipChanged: true}, err: errors.New("Herdr interrupted")})
	m = next.(Model)
	if m.viewLoad(ViewFleet).freshness != perftrace.FreshnessStale || command == nil {
		t.Fatal("fleet snapshot was not invalidated")
	}
	next, _ = m.Update(command())
	m = next.(Model)
	if loads != 1 || fleetLoads != 0 || m.status != "partial registrations" || m.err == nil {
		t.Fatalf("loads=%d fleet=%d status=%s error=%v", loads, fleetLoads, m.status, m.err)
	}
}

func TestSSHEmptyViewExposesSetupDiscoveryAndMappings(t *testing.T) {
	var requested string
	m := New(Actions{SSH: SSHActions{Workflow: func(_ context.Context, r SSHWorkflowRequest) (SSHWorkflow, error) {
		requested = r.Action
		return &testSSHWorkflow{}, nil
	}}}, nil, nil)
	m.view = ViewSSH
	m = m.openActionMenu()
	if !strings.Contains(m.View(), "import connections") || !strings.Contains(m.View(), "discover Tailscale") {
		t.Fatal(m.View())
	}
	for index := 0; index < m.overlay.optionCount; index++ {
		if m.overlay.options[index].action == listActionSSHSetup {
			m.overlay.optionIndex = index
			break
		}
	}
	next, command := m.runOverlayAction()
	m = next.(Model)
	if command == nil || requested != "setup" {
		t.Fatal("empty view cannot launch setup")
	}
}

func TestSSHSortingPreservesCanonicalSelectionAndMenusRemainLocal(t *testing.T) {
	inv := sshTestInventory("z-machine")
	inv.Machines = append(inv.Machines, SSHRow{ID: "other", Label: "a-machine", State: "candidate"})
	m := New(Actions{}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	focus := m.currentToken()
	next, _ := m.cycleTableSort("machine")
	m = next.(Model)
	if m.currentToken() != focus || m.sshCursor != 1 {
		t.Fatal("sorting changed canonical selection")
	}
	next, _ = m.runSSHAction(listActionSSHDetails)
	m = next.(Model)
	if !strings.Contains(m.View(), "machine-one") {
		t.Fatal("full detail omits identity")
	}
}
