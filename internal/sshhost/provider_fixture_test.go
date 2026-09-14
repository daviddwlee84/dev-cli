package sshhost

import (
	"runtime"
	"testing"
)

// These fixtures model successful Unix socket/tool attestation. Substituting
// regular files for sockets does not make native Windows attestation supported.
// Windows refusal paths have dedicated tests without those success seams.
func requireUnixSSHProviderFixture(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("successful explicit-agent/FIDO fixtures require Unix attestation; Windows fail-closed contracts are tested separately")
	}
}

// Pure SSH configuration tests need syntax, not a filesystem-backed socket.
// In particular, CI's Windows temporary directory may contain an 8.3 '~' token.
func managedAgentSocketFixture() string {
	if runtime.GOOS == "windows" {
		return `C:\Users\example\Library\Group Containers\agent.sock`
	}
	return "/home/example/Library/Group Containers/agent.sock"
}
