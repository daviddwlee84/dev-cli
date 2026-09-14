//go:build unix

package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

// shortAgentSocket creates a real Unix socket in a short directory; temporary
// test directories can exceed the platform socket path limit.
func shortAgentSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dev-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "a.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return socket
}

func runSSHCLIWithRunner(t *testing.T, f *sshCLIFixture, runner sshhost.Runner, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errOut, sshHostRunner: runner, interactiveCheck: func() bool { return false }}
	root := newRootCommand(app)
	root.SetContext(context.Background())
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, args...))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestSSHSetupIdentityAgentPlansPublicKeyAndV2Alias(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.createManaged("lab", "lab.example")
	socket := shortAgentSocket(t)
	metadata, err := sshhost.ParsePublicKey(f.runner.publicLine)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sshKeyInventoryRunner{base: f.runner, agentKeys: append(append([]byte(nil), f.runner.publicLine...), '\n')}

	out, stderr, err := runSSHCLIWithRunner(t, f, runner, "ssh", "setup", "lab", "--identity-agent", socket, "--key", metadata.Fingerprint, "--target-os", "posix", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("dry run: %v\n%s\n%s", err, out, stderr)
	}
	document := assertOneSSHJSON(t, out)
	definition, _ := document["definition"].(map[string]any)
	publication, _ := document["agent_public_plan"].(map[string]any)
	identityFile, _ := definition["identity_file"].(string)
	if definition["identity_agent"] != socket || !strings.HasSuffix(identityFile, "dev_agent_custom_lab.pub") || definition["identities_only"] != true || publication["action"] != "create" {
		t.Fatalf("agent setup plan=%#v", document)
	}
	if _, err := os.Stat(identityFile); !os.IsNotExist(err) {
		t.Fatalf("dry run published the agent public key: %v", err)
	}
	managed, err := os.ReadFile(f.managedPath("lab"))
	if err != nil || !strings.HasPrefix(string(managed), sshhost.ManagedHeader+"\n") {
		t.Fatalf("dry run changed the managed alias: %q %v", managed, err)
	}

	f.appendRootConfig("Host foreign\n    HostName foreign.example\n")
	out, _, err = runSSHCLIWithRunner(t, f, runner, "ssh", "setup", "foreign", "--identity-agent", socket, "--key", metadata.Fingerprint, "--target-os", "posix", "--yes", "--json")
	if err == nil || assertOneSSHJSON(t, out)["error_code"] != "identity_agent_manual" || !strings.Contains(err.Error(), "IdentityAgent") {
		t.Fatalf("foreign alias was not blocked with manual instructions: %v\n%s", err, out)
	}

	out, _, err = runSSHCLIWithRunner(t, f, runner, "ssh", "setup", "lab", "--identity-agent", socket, "--key", "SHA256:missing", "--target-os", "posix", "--dry-run", "--json")
	if err == nil || assertOneSSHJSON(t, out)["error_code"] != "agent_key_not_found" {
		t.Fatalf("absent agent key was accepted: %v\n%s", err, out)
	}
}

func TestSSHKeyListIncludesExplicitAgentSocket(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	f := newSSHCLIFixture(t)
	socket := shortAgentSocket(t)
	runner := &sshKeyInventoryRunner{base: f.runner, agentKeys: append(append([]byte(nil), f.runner.publicLine...), '\n')}
	out, stderr, err := runSSHCLIWithRunner(t, f, runner, "ssh", "key", "list", "--agent", socket, "--json")
	if err != nil || !strings.Contains(out, `"provider": "custom"`) || !strings.Contains(out, socket) {
		t.Fatalf("key list --agent: %v\n%s\n%s", err, out, stderr)
	}
	if _, _, err := runSSHCLIWithRunner(t, f, runner, "ssh", "key", "list", "--agent", "bitwarden", "--json"); err == nil {
		t.Fatal("absent Bitwarden agent socket resolved in an empty home")
	}
}
