package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

func TestSSHRemoteHelpersAreSeparateStrictAndStatic(t *testing.T) {
	f := newSSHCLIFixture(t)
	if err := os.MkdirAll(filepath.Join(f.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".ssh", "config"), []byte("Host target\n HostName target.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(sshremote.CapabilityRequest{Header: sshremote.NewHeader()})
	out, errout, err := f.runInteractive(string(request), false, "fleet", "_ssh-capability")
	if err != nil {
		t.Fatalf("capability: %v %s", err, errout)
	}
	var capability sshremote.Capability
	if err := json.Unmarshal([]byte(out), &capability); err != nil {
		t.Fatal(err)
	}
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
	request, _ = json.Marshal(sshremote.NewRequest(capability.Origin))
	out, errout, err = f.runInteractive(string(request), false, "fleet", "_ssh-inventory")
	if err != nil {
		t.Fatalf("inventory: %v %s", err, errout)
	}
	var inventory sshremote.Inventory
	if err := json.Unmarshal([]byte(out), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Profiles) != 1 || inventory.Profiles[0].Alias != "target" {
		t.Fatalf("profiles %#v", inventory.Profiles)
	}
	if calls := f.runner.callSnapshot(); len(calls) != 0 {
		t.Fatalf("metadata invoked process: %#v", calls)
	}
	out, _, err = f.runInteractive(`{"schema_version":1,"protocol_version":1,"extra":"must-not-echo"}`, false, "fleet", "_ssh-capability")
	if err != nil {
		t.Fatal(err)
	}
	var failure sshremote.ErrorResponse
	if json.Unmarshal([]byte(out), &failure) != nil || failure.Kind != "ssh_remote_error" || strings.Contains(out, "must-not-echo") {
		t.Fatalf("invalid request response %s", out)
	}
	for _, command := range newSSHRemoteHelperCmds(&App{}) {
		if !command.Hidden {
			t.Fatalf("helper %s is public", command.Name())
		}
	}
}
