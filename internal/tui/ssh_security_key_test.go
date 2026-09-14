package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func TestSSHHardwareKeyFormPreservesSelectionsAndClearsOnExistingKey(t *testing.T) {
	var got sshflow.OnboardRequest
	m := New(Actions{SSH: sshKeyTestActions(&got)}, nil, nil)
	parent := sshDialog{kind: "onboard", onboarding: sshflow.OnboardRequest{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix"}}
	parent.addField("alias", "Alias", "lab")
	parent.addField("host", "Host", "lab.example")
	parent.addField("user", "User", "tester")
	parent.addField("port", "Port", "22")
	parent.addField("auth", "Auth", "key")
	parent.addField("key", "Key", "")
	parent.addField("os", "OS", "posix")
	m.sshUI.dialog = sshKeyGenerationForm(parent, SSHKeyChoice{Generate: true, KeyType: sshhost.KeyTypeEd25519SK, Path: "/home/u/.ssh/id_sk"})
	for name, value := range map[string]string{"path": "/home/u/.ssh/work/id_box", "comment": "box laptop", "keytype": "ecdsa-sk", "skprovider": "/providers/custom.so", "skresident": "yes", "skverify": "yes", "skapplication": "ssh:box"} {
		m.sshUI.dialog.setField(name, value)
	}
	m = sshKeyDialogKey(m, tea.KeyCtrlS)
	request := m.sshUI.dialog.onboarding
	if !request.GenerateKey || request.KeyType != sshhost.KeyTypeECDSASK || request.KeyPath != "/home/u/.ssh/work/id_box" || request.KeyComment != "box laptop" || request.SecurityKey != (sshhost.SecurityKeyOptions{Provider: "/providers/custom.so", Resident: true, VerifyRequired: true, Application: "ssh:box"}) {
		t.Fatalf("hardware form lost selections: %+v", request)
	}
	next, cmd := m.prepareSSHOnboarding()
	m = sshDrainEvents(t, next.(Model), cmd)
	if got.KeyType != request.KeyType || got.SecurityKey != request.SecurityKey || got.KeyPath != request.KeyPath || got.KeyComment != request.KeyComment {
		t.Fatalf("hardware fields lost before adapter: %+v", got)
	}
	reopened := parent
	reopened.onboarding = request
	form := sshKeyGenerationForm(reopened, SSHKeyChoice{Generate: true, KeyType: sshhost.KeyTypeEd25519SK})
	if form.value("keytype") != "ecdsa-sk" || form.value("path") != request.KeyPath || form.value("comment") != request.KeyComment || form.value("skprovider") != request.SecurityKey.Provider {
		t.Fatalf("reopening hardware form changed reviewed values: %+v", form)
	}
	for _, agent := range []bool{false, true} {
		d := parent
		d.onboarding = request
		if agent {
			d.applyAgentKeyChoice(SSHKeyChoice{Fingerprint: "SHA256:test", AgentSocket: "/agent.sock", Label: "Agent"})
		} else {
			d.applyKeyChoice("/home/u/.ssh/existing", false)
		}
		if d.onboarding.GenerateKey || d.onboarding.KeyType != "" || d.onboarding.SecurityKey != (sshhost.SecurityKeyOptions{}) || d.onboarding.KeyComment != "" {
			t.Fatalf("existing selection retained hardware intent: %+v", d.onboarding)
		}
	}
}

func TestSSHKeyTypeCycleClearsHardwareOptionsWithoutLosingName(t *testing.T) {
	m := New(Actions{}, nil, nil)
	parent := sshDialog{onboarding: sshflow.OnboardRequest{GenerateKey: true, KeyPath: "/home/u/.ssh/custom", KeyComment: "keep comment", KeyType: sshhost.KeyTypeEd25519SK, SecurityKey: sshhost.SecurityKeyOptions{Provider: "/custom.so", Resident: true, VerifyRequired: true, Application: "ssh:custom"}}}
	m.sshUI.dialog = sshKeyGenerationForm(parent, SSHKeyChoice{Generate: true, KeyType: sshhost.KeyTypeEd25519SK})
	m.focusSSHField(2)
	m = sshKeyDialogKey(m, tea.KeyLeft)
	d := m.sshUI.dialog
	if d.value("keytype") != "ed25519" || d.value("skprovider") != "" || d.value("skresident") != "no" || d.value("skverify") != "no" || d.value("skapplication") != "" || d.value("path") != parent.onboarding.KeyPath || d.value("comment") != parent.onboarding.KeyComment {
		t.Fatalf("switching type lost name or kept stale hardware flags: %+v", d)
	}
	m.width, m.height = 160, 40
	if view := m.renderSSHDialog(); !strings.Contains(view, "[ed25519] · ed25519-sk · ecdsa-sk") {
		t.Fatal(view)
	}
}

func TestSSHPartialKeyPublicationObservationsDoNotClaimRecovery(t *testing.T) {
	key := sshhost.KeyResult{Operation: sshhost.KeyGenerate, Hardware: &sshhost.HardwareKeyEffect{Kind: "fido", Status: "created"}, LocalFiles: []sshhost.KeyLocalFileObservation{
		{Kind: "identity", Status: "retained", Path: "/home/u/.ssh/id_sk"},
		{Kind: "staging_public", Status: "unknown", Path: "/home/u/.ssh/.stage-sk.pub"},
	}}
	m := New(Actions{}, nil, nil)
	m.finishSSHOnboarding(SSHWorkflowResult{Onboarding: &sshflow.OnboardExecutionResult{
		OnboardResult: sshflow.OnboardResult{Status: "partial", Outcomes: []sshflow.OnboardOutcome{{Alias: "lab", Stage: "configure", Status: "failed"}}},
		Keys:          map[string]sshhost.KeyResult{"lab": key},
	}})
	body := m.sshUI.dialog.body
	for _, expected := range []string{"identity retained /home/u/.ssh/id_sk", "staging_public unknown /home/u/.ssh/.stage-sk.pub", "not a validated recovery pair", "observations do not authorize key use or recovery"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("partial file observation omitted: %q in %s", expected, body)
		}
	}
	if strings.Contains(body, "Retained key handle:") || strings.Contains(body, "Retained public key:") {
		t.Fatalf("unverified files acquired recovery labels: %s", body)
	}
	key.Hardware = nil
	if lines := strings.Join(sshHardwareResultLines(key), "\n"); !strings.Contains(lines, "Local file observation:") {
		t.Fatal("local filesystem facts disappeared without a hardware receipt")
	}
}

func TestSSHHardwareReceiptShowsDeviceAndRecoveryState(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.finishSSHOnboarding(SSHWorkflowResult{Onboarding: &sshflow.OnboardExecutionResult{
		OnboardResult: sshflow.OnboardResult{Status: "partial", Outcomes: []sshflow.OnboardOutcome{{Alias: "lab", Stage: "configure", Status: "unknown"}}},
		Keys:          map[string]sshhost.KeyResult{"lab": {Hardware: &sshhost.HardwareKeyEffect{Kind: "fido", Status: "unknown", Resident: true, RecoveryIdentityPath: "/home/u/.ssh/recovery", RecoveryPublicPath: "/home/u/.ssh/recovery.pub"}}},
	}})
	for _, expected := range []string{"fido: unknown", "resident=true", "/home/u/.ssh/recovery", "/home/u/.ssh/recovery.pub", "no hardware rollback", "do not retry automatically"} {
		if !strings.Contains(m.sshUI.dialog.body, expected) {
			t.Fatalf("result omits %q: %s", expected, m.sshUI.dialog.body)
		}
	}
}
