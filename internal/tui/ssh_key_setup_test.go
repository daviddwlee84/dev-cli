package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func sshKeyTestActions(got *sshflow.OnboardRequest) SSHActions {
	return SSHActions{
		Workflow: func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error) { return &testSSHWorkflow{}, nil },
		PrepareOnboarding: func(_ context.Context, r sshflow.OnboardRequest) (SSHOnboardingPlan, error) {
			*got = r
			return testSSHOnboardPlan{preview: sshflow.OnboardPreview{Targets: []sshflow.OnboardTarget{{Alias: r.Alias, HostName: r.HostName, Port: r.Port, Auth: r.Auth}}}}, nil
		},
		ListKeys: func(_ context.Context, alias string) ([]SSHKeyChoice, error) {
			return []SSHKeyChoice{
				{Label: "~/.ssh/id_ed25519", Description: "ed25519 · private file", Path: "/home/u/.ssh/id_ed25519"},
				{Label: "+ Generate a new key", Path: "/home/u/.ssh/id_ed25519_dev_" + alias, Generate: true},
			}, nil
		},
	}
}

func sshKeyTestInventory() SSHInventory {
	inv := sshTestInventory("box")
	c := testDiscoveryCandidate()
	inv.Machines[0].LAN = []sshdiscovery.Candidate{c}
	inv.Machines[0].Profiles[0].HostName = c.Addresses[0]
	inv.Machines[0].Profiles[0].User = "tester"
	inv.Machines[0].Profiles[0].Port = 22
	return inv
}

func sshKeyDialogKey(m Model, key tea.KeyType) Model {
	next, _ := m.updateSSHDialog(tea.KeyMsg{Type: key})
	return next.(Model)
}

func TestSSHFormChoiceFieldsShowOptionsAndCycleBothWays(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.width, m.height = 160, 40
	next, _ := m.openSSHOnboardingForm(sshflow.OnboardRequest{Alias: "box", HostName: "192.0.2.1", User: "user", Port: 22, Auth: "config", RemoteOS: "posix"})
	m = next.(Model)
	for i := 0; i < m.sshUI.dialog.fieldCount; i++ {
		if m.sshUI.dialog.fields[i].key == "auth" {
			m.focusSSHField(i)
		}
	}
	if view := m.renderSSHDialog(); !strings.Contains(view, "[config] · existing · key") || !strings.Contains(view, "[posix] · windows") || !strings.Contains(view, "Enter: choose an existing key") {
		t.Fatal(view)
	}
	if m = sshKeyDialogKey(m, tea.KeyLeft); m.sshUI.dialog.value("auth") != "key" {
		t.Fatalf("left cycled to %q", m.sshUI.dialog.value("auth"))
	}
	if m = sshKeyDialogKey(m, tea.KeyRight); m.sshUI.dialog.value("auth") != "config" {
		t.Fatalf("right cycled to %q", m.sshUI.dialog.value("auth"))
	}
}

func TestSSHMenuSetsUpDiscoveredTargetWithPrefilledUser(t *testing.T) {
	var got sshflow.OnboardRequest
	m := New(Actions{SSH: sshKeyTestActions(&got)}, nil, nil).WithSSH(sshKeyTestInventory())
	m.view = ViewSSH
	m = m.openActionMenu()
	present := map[listAction]bool{}
	for i := 0; i < m.overlay.optionCount; i++ {
		present[m.overlay.options[i].action] = true
	}
	if !present[listActionSSHSetupTarget] || !present[listActionSSHInstallKey] || !present[listActionSSHSetup] {
		t.Fatal(m.View())
	}
	m.overlay = overlayState{}
	next, _ := m.runSSHAction(listActionSSHSetupTarget)
	d := next.(Model).sshUI.dialog
	if d.kind != "onboard" || d.value("host") != "192.0.2.8" || d.value("user") != "tester" || d.onboarding.Candidate == nil {
		t.Fatalf("target form=%+v", d)
	}
}

func TestSSHInstallKeyWizardPicksKeyAndSendsExistingProfile(t *testing.T) {
	var got sshflow.OnboardRequest
	m := New(Actions{SSH: sshKeyTestActions(&got)}, nil, nil).WithSSH(sshKeyTestInventory())
	m.view = ViewSSH
	next, cmd := m.runSSHAction(listActionSSHInstallKey)
	m = next.(Model)
	if m.sshUI.dialog.kind != "keys-loading" || cmd == nil {
		t.Fatalf("install key did not start with the key picker: %+v", m.sshUI.dialog)
	}
	next, _ = m.applySSHEvent(cmd().(sshEventMsg))
	m = next.(Model)
	if d := m.sshUI.dialog; d.kind != "keys" || len(d.options) != 3 || !strings.Contains(d.options[1], "Generate") || !strings.Contains(d.options[2], "manually") {
		t.Fatalf("key picker=%+v", d.options)
	}
	m = sshKeyDialogKey(m, tea.KeyEsc)
	if d := m.sshUI.dialog; d.kind != "onboard" || d.onboarding.Profile == nil || d.fieldCount != 4 || d.onboarding.KeyPath != "" {
		t.Fatalf("Esc did not return to the reduced form: %+v", d)
	}
	next, cmd = m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	next, _ = m.applySSHEvent(cmd().(sshEventMsg))
	m = sshKeyDialogKey(sshKeyDialogKey(next.(Model), tea.KeyDown), tea.KeyEnter)
	if d := m.sshUI.dialog; d.kind != "onboard" || !d.onboarding.GenerateKey || !strings.HasPrefix(d.value("key"), "new: ") {
		t.Fatalf("generate choice not applied: %+v", d)
	}
	next, cmd = m.prepareSSHOnboarding()
	m = sshDrainEvents(t, next.(Model), cmd)
	if got.Profile == nil || got.Profile.Alias != "box" || got.Alias != "box" || got.Auth != "key" || !got.GenerateKey || got.KeyPath != "/home/u/.ssh/id_ed25519_dev_box" || got.Candidate != nil {
		t.Fatalf("request=%+v", got)
	}
	if m.sshUI.dialog.kind != "review" {
		t.Fatalf("no review: %+v", m.sshUI.dialog)
	}
}

func TestSSHInstallKeyAsksForExactProfileWhenSeveralExist(t *testing.T) {
	var got sshflow.OnboardRequest
	inv := sshKeyTestInventory()
	inv.Machines[0].Profiles = append(inv.Machines[0].Profiles, sshflow.ConnectionProfile{ID: "second", Alias: "alternate", HostName: "192.0.2.9", Fingerprint: "other"})
	m := New(Actions{SSH: sshKeyTestActions(&got)}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	next, _ := m.runSSHAction(listActionSSHInstallKey)
	m = next.(Model)
	if m.sshUI.dialog.kind != "profiles" || m.sshUI.dialog.workflow != "install-key" {
		t.Fatalf("dialog=%+v", m.sshUI.dialog)
	}
	want := m.sshUI.dialog.profiles[1].Alias
	m = sshKeyDialogKey(m, tea.KeyDown)
	next, cmd := m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.sshUI.dialog.kind != "keys-loading" || cmd == nil || m.sshUI.dialog.parent == nil || m.sshUI.dialog.parent.onboarding.Profile.Alias != want {
		t.Fatalf("selected profile did not open its key wizard: %+v", m.sshUI.dialog)
	}
}
