//go:build windows

package sshhost

import (
	"errors"
	"testing"
)

func TestAgentInventoryWindowsPipeRemainsUnattested(t *testing.T) {
	s := newFixtureService(t, fixturePaths(t), DiscoverOptions{})
	inventory, err := s.ObserveAgentKeys(t.Context(), AgentSocketRef{Provider: AgentProviderWindowsPipe, Socket: windowsOpenSSHAgentPipe})
	if !errors.Is(err, ErrManualRemediation) || inventory.Complete || inventory.state != nil {
		t.Fatalf("unattested Windows pipe became a baseline: %+v %v", inventory, err)
	}
}
