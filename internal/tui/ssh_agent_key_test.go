package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func TestSSHAgentKeyChoiceSendsFingerprintAndSocket(t *testing.T) {
	var got sshflow.OnboardRequest
	actions := sshKeyTestActions(&got)
	actions.ListKeys = func(context.Context, string) ([]SSHKeyChoice, error) {
		return []SSHKeyChoice{
			{Label: "Bitwarden agent: work", Description: "ssh-ed25519 · SHA256:abc", Fingerprint: "SHA256:abc", AgentProvider: "bitwarden", AgentSocket: "/home/u/.bitwarden-ssh-agent.sock"},
			{Label: "+ Generate a new key", Path: "/home/u/.ssh/id_ed25519_dev_box", Generate: true},
		}, nil
	}
	m := New(Actions{SSH: actions}, nil, nil).WithSSH(sshKeyTestInventory())
	m.view = ViewSSH
	next, cmd := m.runSSHAction(listActionSSHInstallKey)
	m = next.(Model)
	next, _ = m.applySSHEvent(cmd().(sshEventMsg))
	m = sshKeyDialogKey(next.(Model), tea.KeyEnter)
	if d := m.sshUI.dialog; d.kind != "onboard" || d.value("key") != "Bitwarden agent: work" || d.onboarding.KeyFingerprint != "SHA256:abc" || d.onboarding.KeyPath != "" {
		t.Fatalf("agent choice not applied: %+v", d)
	}
	next, cmd = m.prepareSSHOnboarding()
	m = sshDrainEvents(t, next.(Model), cmd)
	if got.KeyFingerprint != "SHA256:abc" || got.KeyAgentSocket != "/home/u/.bitwarden-ssh-agent.sock" || got.KeyPath != "" || got.GenerateKey || m.sshUI.dialog.kind != "review" {
		t.Fatalf("request=%+v dialog=%s", got, m.sshUI.dialog.kind)
	}
}

func TestSSHUnavailableAgentChoiceShowsGuidanceWithoutSelecting(t *testing.T) {
	var got sshflow.OnboardRequest
	actions := sshKeyTestActions(&got)
	actions.ListKeys = func(context.Context, string) ([]SSHKeyChoice, error) {
		return []SSHKeyChoice{{Label: "Bitwarden agent (unavailable)", UnavailableReason: "Agent socket not found; open Bitwarden settings, then reopen the picker."}}, nil
	}
	m := New(Actions{SSH: actions}, nil, nil).WithSSH(sshKeyTestInventory())
	m.view = ViewSSH
	next, cmd := m.runSSHAction(listActionSSHInstallKey)
	m = next.(Model)
	next, _ = m.applySSHEvent(cmd().(sshEventMsg))
	m = sshKeyDialogKey(next.(Model), tea.KeyEnter)
	if m.sshUI.dialog.kind != "keys" || m.sshUI.dialog.err == nil || !strings.Contains(m.sshUI.dialog.err.Error(), "socket not found") || got.KeyFingerprint != "" || got.KeyPath != "" {
		t.Fatalf("unavailable choice became an action: dialog=%+v request=%+v", m.sshUI.dialog, got)
	}
}

func TestSSHAgentPreviewPreservesNativeEndpointFields(t *testing.T) {
	for _, test := range []struct {
		name, host, user, want string
		port                   int
	}{
		{name: "native port", host: "host.example", user: "tester", want: "lab → tester@host.example (native port)"},
		{name: "explicit port", host: "host.example", user: "tester", port: 2222, want: "lab → tester@host.example:2222"},
		{name: "native endpoint", want: "lab → native config (native port)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := sshOnboardPreview(sshflow.OnboardPreview{
				Targets:         []sshflow.OnboardTarget{{Alias: "lab", HostName: test.host, User: test.user, Port: test.port, Auth: "key"}},
				PreserveAliases: []string{"lab"},
				Init:            sshhost.InitPlan{Action: sshhost.ActionNoop, Path: "/home/u/.ssh/config"},
			})
			if !strings.Contains(body, test.want) || strings.Contains(body, ":0") || strings.Contains(body, "Managed aliases:") {
				t.Fatalf("native configuration intent lost in preview: %s", body)
			}
		})
	}
}

func TestSSHAgentPublicationUnknownResultShowsRetainedPath(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.finishSSHOnboarding(SSHWorkflowResult{Onboarding: &sshflow.OnboardExecutionResult{
		OnboardResult: sshflow.OnboardResult{Status: "partial", Outcomes: []sshflow.OnboardOutcome{{Alias: "lab", Stage: "configure", Status: "unknown"}}},
		Keys:          map[string]sshhost.KeyResult{"lab": {PublicationUnknown: true, Candidate: sshhost.KeyCandidate{PublicPath: "/home/u/.ssh/dev_agent_bitwarden_lab.pub"}}},
	}})
	body := m.sshUI.dialog.body
	if !strings.Contains(body, "publication unknown") || !strings.Contains(body, "/home/u/.ssh/dev_agent_bitwarden_lab.pub") || !strings.Contains(body, "before retrying") {
		t.Fatalf("unknown publication disappeared from result: %s", body)
	}
}
