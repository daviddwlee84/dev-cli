//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type sshAgentSetupRunner struct {
	base      *sshCLIRunner
	agents    map[string][]byte
	effective map[string][]byte
	calls     []sshhost.RunRequest
	proofs    []string
}

func (r *sshAgentSetupRunner) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	r.calls = append(r.calls, request)
	if request.Name == "ssh-add" {
		socket := ""
		for _, value := range request.Env {
			if strings.HasPrefix(value, "SSH_AUTH_SOCK=") {
				socket = strings.TrimPrefix(value, "SSH_AUTH_SOCK=")
			}
		}
		if key, ok := r.agents[socket]; ok {
			return sshhost.RunResult{Stdout: key}, nil
		}
		return sshhost.RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}, nil
	}
	if request.Name == "ssh" && hasSSHArg(request.Args, "-G") {
		if effective, ok := r.effective[sshArgValue(request.Args, "-G")]; ok {
			return sshhost.RunResult{Stdout: effective}, nil
		}
	}
	if request.Display == "ssh selected-key-only authentication proof" {
		body, err := os.ReadFile(sshArgValue(request.Args, "-F"))
		if err != nil {
			return sshhost.RunResult{}, err
		}
		r.proofs = append(r.proofs, string(body))
	}
	return r.base.Run(ctx, request)
}

func newSSHAgentSetupFixture(t *testing.T) (*App, *sshCLIFixture, *sshAgentSetupRunner) {
	t.Helper()
	f := newSSHCLIFixture(t)
	home, err := os.MkdirTemp("/tmp", "dev-agent-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	// /private/tmp inherits wheel on macOS; keep this fixture's descendants
	// restorable by the current test user without relaxing production checks.
	if err := os.Chown(home, os.Getuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	f.home, f.runner.home = home, home
	f.configPath = filepath.Join(home, "config.toml")
	f.remotesPath = filepath.Join(home, ".config", "dev", "remotes.toml")
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"SSH_AUTH_SOCK":   "",
	} {
		t.Setenv(key, value)
	}
	if err := os.WriteFile(f.configPath, []byte("[runtime]\nbackend = \"none\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.initSSH()
	runner := &sshAgentSetupRunner{base: f.runner, agents: map[string][]byte{}, effective: map[string][]byte{}}
	app := &App{Cfg: config.Default(), In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, remotesPath: f.remotesPath, sshHostRunner: runner, interactiveCheck: func() bool { return false }}
	return app, f, runner
}

func listenSSHAgentAt(t *testing.T, path string) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

func TestSSHAgentSetupApplyKeepsProviderAfterPublicCopy(t *testing.T) {
	_, f, runner := newSSHAgentSetupFixture(t)
	socket := filepath.Join(f.home, "selected agent.sock")
	listenSSHAgentAt(t, socket)
	runner.agents[socket] = f.runner.publicLine
	ambient := filepath.Join(f.home, "ambient.sock")
	listenSSHAgentAt(t, ambient)
	t.Setenv("SSH_AUTH_SOCK", ambient)
	runner.agents[ambient] = sshCLITestPublicLine(0x18, "other-agent-key")
	metadata, err := sshhost.ParsePublicKey(f.runner.publicLine)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"first", "second", "first"} {
		args := []string{"ssh", "setup", alias, "--hostname", alias + ".example", "--identity-agent", socket, "--key", metadata.Fingerprint, "--target-os", "posix", "--yes", "--json"}
		out, stderr, err := runSSHCLIWithRunner(t, f, runner, args...)
		if err != nil || assertOneSSHJSON(t, out)["status"] != "ready" {
			t.Fatalf("%s: %v\n%s\n%s", alias, err, out, stderr)
		}
		body, err := os.ReadFile(f.managedPath(alias))
		if err != nil {
			t.Fatal(err)
		}
		definition, err := sshhost.ParseManaged(body)
		if err != nil || !strings.HasPrefix(string(body), sshhost.ManagedHeaderV2+"\n") || definition.IdentityAgent != socket || definition.IdentitiesOnly == nil || !*definition.IdentitiesOnly || !strings.HasSuffix(definition.IdentityFile, "dev_agent_custom_"+alias+".pub") {
			t.Fatalf("provider intent lost: %+v %v", definition, err)
		}
		if _, err := os.Stat(strings.TrimSuffix(definition.IdentityFile, ".pub")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("agent selection created a private file: %v", err)
		}
	}
	if len(runner.proofs) < 3 {
		t.Fatal("successful setup never reached the real exact-proof adapter")
	}
	for _, proof := range runner.proofs {
		if !strings.Contains(proof, socket) || !strings.Contains(proof, "IdentityAgent") {
			t.Fatalf("proof lost selected socket: %s", proof)
		}
	}
	for _, call := range runner.calls {
		if call.Name == "ssh-keygen" {
			t.Fatal("agent-only setup invoked private key generation/derivation")
		}
	}
}

func TestSSHAgentSelectionIgnoresDuplicatePrivateFile(t *testing.T) {
	app, f, runner := newSSHAgentSetupFixture(t)
	private := writeSSHCLIKey(t, f, "local-copy")
	socket := filepath.Join(f.home, "agent.sock")
	listenSSHAgentAt(t, socket)
	runner.agents[socket] = f.runner.publicLine
	service, err := app.sshHosts()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := selectSSHAgentKey(t.Context(), service, sshhost.AgentSocketRef{Provider: sshhost.AgentProviderCustom, Socket: socket}, private+".pub")
	if err != nil || candidate.Agent == nil || candidate.Provenance.Private || candidate.Provenance.SecurityKeyStub || candidate.NeedsPermissionRepair {
		t.Fatalf("explicit agent selection retained private authority: %+v %v", candidate, err)
	}
	if _, err := selectSSHAgentKey(t.Context(), service, *candidate.Agent, private); err == nil {
		t.Fatal("--identity-agent accepted a private path instead of public metadata")
	}
	if err := os.Chmod(private, 0666); err != nil {
		t.Fatal(err)
	}
	candidate, err = selectSSHAgentKey(t.Context(), service, *candidate.Agent, private+".pub")
	if err != nil || candidate.Provenance.Private || candidate.NeedsPermissionRepair {
		t.Fatalf("unsafe unused private companion prevented agent selection: %+v %v", candidate, err)
	}
	for _, call := range runner.calls {
		if call.Name == "ssh-keygen" {
			t.Fatal("agent selection touched private identity")
		}
	}
}

func TestSSHTUIAgentPublicCopyReviewAndApply(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Bitwarden socket discovery is available on macOS and Linux")
	}
	app, f, runner := newSSHAgentSetupFixture(t)
	socket := filepath.Join(f.home, ".bitwarden-ssh-agent.sock")
	listenSSHAgentAt(t, socket)
	runner.agents[socket] = f.runner.publicLine
	if err := os.WriteFile(filepath.Join(f.home, ".ssh", "existing-public.pub"), f.runner.publicLine, 0600); err != nil {
		t.Fatal(err)
	}
	choices, err := listSSHTUIKeys(t.Context(), app, "lab")
	if err != nil {
		t.Fatal(err)
	}
	var selected tui.SSHKeyChoice
	for _, choice := range choices {
		if choice.AgentSocket == socket {
			selected = choice
		}
	}
	if selected.Fingerprint == "" || selected.Path != "" {
		t.Fatalf("TUI lost agent authority for local public copy: %+v", choices)
	}
	request := sshflow.OnboardRequest{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix", To: "fleet", KeyFingerprint: selected.Fingerprint, KeyAgentSocket: selected.AgentSocket}
	plan, err := prepareSSHTUIOnboarding(t.Context(), app, request)
	if err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(f.home, ".ssh", "dev_agent_bitwarden_lab.pub")
	preview := strings.Join(plan.Preview().Notes, "\n")
	for _, expected := range []string{socket, public, "Public key only: create", "Managed SSH v2", "IdentitiesOnly yes", "v0.2.37"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("review omits %q: %s", expected, preview)
		}
	}
	if _, err := os.Stat(public); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("TUI preparation published key before approval")
	}
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: plan}}
	if err := workflow.Run(); err != nil {
		t.Fatal(err)
	}
	result := workflow.Result().Onboarding
	if result == nil || result.Status != "ready" || !result.Keys["lab"].Created || !result.Configurations["lab"].Changed || !workflow.Result().MembershipChanged {
		t.Fatalf("TUI lost real apply results: %+v", result)
	}
	body, err := os.ReadFile(f.managedPath("lab"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := sshhost.ParseManaged(body)
	if err != nil || definition.IdentityAgent != socket || definition.IdentityFile != public {
		t.Fatalf("TUI apply differs from review: %+v %v", definition, err)
	}
	if len(runner.proofs) == 0 || !strings.Contains(runner.proofs[0], socket) {
		t.Fatal("TUI agent selection never reached selected-socket proof")
	}
}

func TestSSHAgentForeignMatchingPolicyDoesNotRewrite(t *testing.T) {
	app, f, runner := newSSHAgentSetupFixture(t)
	socket := filepath.Join(f.home, "agent.sock")
	listenSSHAgentAt(t, socket)
	runner.agents[socket] = f.runner.publicLine
	public := filepath.Join(f.home, ".ssh", "foreign.pub")
	if err := os.WriteFile(public, f.runner.publicLine, 0600); err != nil {
		t.Fatal(err)
	}
	f.appendRootConfig(fmt.Sprintf("Host foreign\n    HostName foreign.example\n    User tester\n    IdentityAgent %s\n    IdentityFile %s\n    IdentitiesOnly yes\n", socket, public))
	runner.effective["foreign"] = []byte(fmt.Sprintf("hostname foreign.example\nuser tester\nport 22\nproxyjump none\nproxycommand none\nidentityagent %s\nidentityfile %s\nidentitiesonly yes\n", socket, public))
	before, err := os.ReadFile(f.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := sshhost.ParsePublicKey(f.runner.publicLine)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runSSHCLIWithRunner(t, f, runner, "ssh", "setup", "foreign", "--identity-agent", socket, "--key", metadata.Fingerprint, "--target-os", "posix", "--yes", "--json")
	if err != nil || assertOneSSHJSON(t, out)["status"] != "ready" {
		t.Fatalf("compatible foreign alias blocked: %v\n%s\n%s", err, out, stderr)
	}
	after, err := os.ReadFile(f.rootConfigPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign configuration was rewritten")
	}
	if _, err := os.Stat(f.managedPath("foreign")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("foreign alias acquired a managed override")
	}
	profile := sshTUIProfileFor(t, app, "foreign")
	plan, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Profile: &profile, Alias: "foreign", Auth: "key", RemoteOS: "posix", KeyFingerprint: metadata.Fingerprint, KeyAgentSocket: socket})
	if err != nil || !strings.Contains(strings.Join(plan.Preview().Notes, "\n"), "no SSH configuration or public key file will be written") {
		t.Fatalf("TUI matching foreign review: %v", err)
	}
	// Changing the effective policy after review must stop before any apply stage.
	runner.effective["foreign"] = []byte("hostname foreign.example\nuser tester\nport 22\nproxyjump none\nproxycommand none\nidentityagent none\nidentitiesonly yes\n")
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: plan}}
	if err := workflow.Run(); err == nil {
		t.Fatal("foreign policy change after review was accepted")
	}
}

func TestSSHAgentOptionsRejectedBeforeSourceOrKeyOperations(t *testing.T) {
	for _, flags := range [][]string{
		{"--to", "fleet", "--generate-key", "--no-passphrase"},
		{"--from", "lan:192.0.2.1:22", "--config-only"},
		{"--to", "both", "--auth", "existing"},
		{"--from", "fleet:gateway/target", "--key", "SHA256:test"},
		{"--identity-file", "/tmp/other.pub", "--key", "SHA256:test"},
		{"--identities-only=false", "--key", "SHA256:test"},
	} {
		t.Run(strings.Join(flags, "_"), func(t *testing.T) {
			_, f, runner := newSSHAgentSetupFixture(t)
			args := append([]string{"ssh", "setup", "lab", "--identity-agent", "bitwarden", "--target-os", "posix", "--yes", "--json"}, flags...)
			_, _, err := runSSHCLIWithRunner(t, f, runner, args...)
			if err == nil || len(runner.calls) != 0 {
				t.Fatalf("conflicting provider flags ran operations: %v %+v", err, runner.calls)
			}
			if _, err := os.Stat(f.managedPath("lab")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid agent flags wrote a managed alias")
			}
		})
	}
	_, f, runner := newSSHAgentSetupFixture(t)
	if _, _, err := runSSHCLIWithRunner(t, f, runner, "ssh", "key", "list", "--no-agent", "--agent", "bitwarden", "--json"); err == nil || len(runner.calls) != 0 {
		t.Fatal("--no-agent and --agent were not rejected before querying sources")
	}
}

func TestSSHNamedProviderPickerRebindsAgentOnly(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Bitwarden socket discovery is available on macOS and Linux")
	}
	app, f, runner := newSSHAgentSetupFixture(t)
	writeSSHCLIKey(t, f, "private-copy")
	socket := filepath.Join(f.home, ".bitwarden-ssh-agent.sock")
	listenSSHAgentAt(t, socket)
	runner.agents[socket] = f.runner.publicLine
	app.interactiveCheck = func() bool { return true }
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		for _, item := range request.Items {
			if item.Label == "Bitwarden agent" {
				return picker.Result{Item: item}, nil
			}
		}
		t.Fatalf("named provider not labeled in picker: %+v", request.Items)
		return picker.Result{}, errors.New("missing named provider")
	}
	service, err := app.sshHosts()
	if err != nil {
		t.Fatal(err)
	}
	options, err := chooseSSHKeyInteractive(t.Context(), app, service, "lab", sshSetupOptions{}, "unused")
	if err != nil || options.keyCandidate == nil || options.keyCandidate.Agent == nil || options.keyCandidate.Provenance.Private || options.keyPlan == nil || options.keyPlan.Agent == nil || options.keyPlan.Agent.Socket != socket {
		t.Fatalf("terminal picker retained local private signer: %+v %v", options.keyCandidate, err)
	}
}

func TestSSHTUIAmbientAgentSocketRemainsCaptured(t *testing.T) {
	app, f, runner := newSSHAgentSetupFixture(t)
	selectedSocket := filepath.Join(f.home, "selected.sock")
	listenSSHAgentAt(t, selectedSocket)
	runner.agents[selectedSocket] = f.runner.publicLine
	t.Setenv("SSH_AUTH_SOCK", selectedSocket)
	choices, err := listSSHTUIKeys(t.Context(), app, "lab")
	if err != nil {
		t.Fatal(err)
	}
	var selected tui.SSHKeyChoice
	for _, choice := range choices {
		if choice.AgentSocket == selectedSocket {
			selected = choice
		}
	}
	if selected.Fingerprint == "" || selected.AgentProvider != "custom" {
		t.Fatalf("ambient agent key hidden: %+v", choices)
	}
	otherSocket := filepath.Join(f.home, "other.sock")
	listenSSHAgentAt(t, otherSocket)
	runner.agents[otherSocket] = sshCLITestPublicLine(0x21, "other-key")
	t.Setenv("SSH_AUTH_SOCK", otherSocket)
	plan, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix", KeyFingerprint: selected.Fingerprint, KeyAgentSocket: selected.AgentSocket})
	if err != nil {
		t.Fatal(err)
	}
	item := plan.(*sshTUIOnboardingPlan).prepared.items[0]
	if item.Definition == nil || item.Definition.IdentityAgent != selectedSocket || item.KeyPlan.Agent.Socket != selectedSocket {
		t.Fatalf("environment change redirected reviewed agent: %+v", item.Definition)
	}
}

func TestSSHTUIUnavailableProviderIsVisible(t *testing.T) {
	app, f, _ := newSSHAgentSetupFixture(t)
	var marker string
	switch runtime.GOOS {
	case "darwin":
		marker = filepath.Join(f.home, "Applications", "Bitwarden.app")
	case "linux":
		marker = filepath.Join(f.home, ".local", "share", "flatpak", "app", "com.bitwarden.desktop")
	default:
		t.Skip("provider application detection is available on macOS and Linux")
	}
	if err := os.MkdirAll(marker, 0700); err != nil {
		t.Fatal(err)
	}
	choices, err := listSSHTUIKeys(t.Context(), app, "lab")
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range choices {
		if choice.AgentProvider == "bitwarden" && choice.UnavailableReason != "" {
			if choice.Fingerprint != "" || choice.AgentSocket != "" || choice.Path != "" || !strings.Contains(choice.UnavailableReason, "socket was not found") {
				t.Fatalf("unavailable provider acquired selection authority: %+v", choice)
			}
			return
		}
	}
	t.Fatalf("unavailable provider hidden from TUI: %+v", choices)
}

func TestSSHFleetNamedAgentPlansBlockedBeforeApply(t *testing.T) {
	app, _, _ := newSSHAgentSetupFixture(t)
	for _, hop := range []bool{false, true} {
		item := sshOnboardItem{Import: &sshflow.FleetImportPlan{}}
		key := sshhost.KeyPlan{Agent: &sshhost.AgentSocketRef{Provider: sshhost.AgentProviderCustom, Socket: "/not-queried.sock"}}
		if hop {
			item.HopKeyPlans = map[string]sshhost.KeyPlan{"hop": key}
		} else {
			item.KeyPlan = &key
		}
		if _, err := planSSHOnboardItems(t.Context(), app, []sshOnboardItem{item}, false); !errors.Is(err, errSSHFleetImportAgent) {
			t.Fatalf("named agent import plan accepted: %v", err)
		}
		if _, err := applySSHFleetImportConfiguration(t.Context(), app, nil, item, nil); !errors.Is(err, errSSHFleetImportAgent) {
			t.Fatalf("named agent import apply was not blocked first: %v", err)
		}
	}
}
