package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
)

func TestToolAvailabilityNamesConfiguredTool(t *testing.T) {
	for _, test := range []struct {
		name         string
		availability ToolAvailability
		want         string
	}{
		{"pending", ToolUnknown, "lazygit availability is still being checked"},
		{"unavailable", ToolUnavailable, "lazygit is unavailable (command lookup failed)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(Actions{Tools: []Tool{{
				Key: "L", Name: "lazygit",
				Command:      []string{"/data/data/com.termux/files/usr/bin/zsh", "-c", "lazygit"},
				Availability: test.availability,
				Probe: func(context.Context) bool {
					t.Fatal("key handling must not probe or launch the tool")
					return false
				},
			}}}, nil, nil)
			m.view = ViewSSH
			_, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
			if command == nil {
				t.Fatal("missing tool feedback")
			}
			message, ok := command().(actionMsg)
			if !ok || message.err == nil || message.err.Error() != test.want {
				t.Fatalf("tool feedback error = %v, want %q", message.err, test.want)
			}
		})
	}
}

func TestSSHDiscoveryUsesCWithoutExternalTools(t *testing.T) {
	m := New(Actions{
		Tools: []Tool{{
			Key: "L", Name: "lazygit", Command: []string{"unused-shell", "-c", "lazygit"},
			Availability: ToolUnavailable,
			Probe: func(context.Context) bool {
				t.Fatal("SSH discovery must not probe external tools")
				return false
			},
		}},
		SSH: SSHActions{
			Discover: func(context.Context, SSHDiscoveryRequest, func(sshdiscovery.Progress)) (SSHDiscoveryResult, error) {
				t.Fatal("opening the discovery menu must not scan")
				return SSHDiscoveryResult{}, nil
			},
		},
	}, nil, nil)
	m.view = ViewSSH
	next, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = next.(Model)
	if command != nil || m.sshUI.dialog.kind != "source" || len(m.sshUI.dialog.options) != 2 {
		t.Fatalf("c did not open the native discovery source menu: %+v", m.sshUI.dialog)
	}
}
