//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshvault"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sshVaultAgentFixture(t *testing.T, provider string) (*App, *sshCLIFixture, *sshAgentSetupRunner, string) {
	t.Helper()
	app, f, agent := newSSHAgentSetupFixture(t)
	socket := filepath.Join(f.home, ".bitwarden-ssh-agent.sock")
	if provider == "1password" {
		dir := filepath.Join(f.home, ".1password")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		socket = filepath.Join(dir, "agent.sock")
	}
	listenSSHAgentAt(t, socket)
	agent.agents[socket] = []byte{} // A complete exact empty baseline, not a generic exit-1 failure.
	return app, f, agent, socket
}

func TestSSHVaultCreationThenNormalSSHSetupUsesOneCreatedItem(t *testing.T) {
	app, f, agent, socket := sshVaultAgentFixture(t, "1password")
	runner := &sshVaultOPRunner{t: t, public: string(f.runner.publicLine), onCreate: func() { agent.agents[socket] = f.runner.publicLine }}
	ctx := context.WithValue(t.Context(), sshVaultBackendKey{}, sshvault.NewService(runner))
	actions := sshTUIActions(newTUIAppState(app))
	plan, err := actions.PrepareVaultKey(ctx, sshflow.VaultKeyRequest{Provider: "1password", AccountID: vaultTestAccount, VaultID: vaultTestVault, Title: "Created for lab"})
	if err != nil || runner.creates != 0 {
		t.Fatalf("vault prepare mutated: %v", err)
	}
	workflow, err := actions.Workflow(ctx, tui.SSHWorkflowRequest{Action: "vault-key", Vault: plan})
	if err != nil {
		t.Fatal(err)
	}
	var nativeOut bytes.Buffer
	workflow.SetStdout(&nativeOut)
	if err := workflow.Run(); err != nil {
		t.Fatal(err)
	}
	created := workflow.Result().Vault
	if created == nil || created.Status != "created" || created.Receipt == nil || created.Receipt.ItemID != vaultTestItem || len(created.Keys) != 1 || created.AgentStatus != "offered" || runner.creates != 1 {
		t.Fatalf("created item was not matched to its exact agent: %+v", created)
	}
	assertSSHVaultPublicOutput(t, nativeOut.String(), runner.public)
	if _, err := os.Stat(f.managedPath("lab")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("vault creation implicitly configured SSH")
	}
	selected := created.Keys[0]
	request := sshflow.OnboardRequest{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix", KeyFingerprint: selected.Fingerprint, KeyAgentSocket: selected.Socket, VaultReceipts: []sshflow.VaultKeyReceipt{*created.Receipt}}
	// Canceling a normal SSH review and preparing another one must never create another vault item.
	first, err := actions.PrepareOnboarding(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(first.Preview().Notes, "\n"), vaultTestItem) {
		t.Fatal("normal SSH preview discarded retained vault receipt")
	}
	second, err := actions.PrepareOnboarding(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	sshWorkflow, err := actions.Workflow(ctx, tui.SSHWorkflowRequest{Action: "onboard", Onboarding: second})
	if err != nil {
		t.Fatal(err)
	}
	if err := sshWorkflow.Run(); err != nil {
		t.Fatal(err)
	}
	result := sshWorkflow.Result().Onboarding
	if result == nil || result.Status != "ready" || len(result.VaultReceipts) != 1 || result.VaultReceipts[0].ItemID != vaultTestItem || runner.creates != 1 {
		t.Fatalf("normal SSH result lost receipt or repeated creation: %+v", result)
	}
	body, err := os.ReadFile(f.managedPath("lab"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := sshhost.ParseManaged(body)
	if err != nil || definition.IdentityAgent != socket || !strings.HasSuffix(definition.IdentityFile, "dev_agent_1password_lab.pub") {
		t.Fatal("normal SSH setup did not use the selected provider key")
	}
	if err := workflow.Run(); !errors.Is(err, sshvault.ErrPlanUsed) || runner.creates != 1 || workflow.Result().Vault.Receipt.ItemID != vaultTestItem {
		t.Fatal("repeated vault workflow lost its receipt or repeated mutation")
	}
}

func TestSSHVaultCreatedNotVisibleRetainsItemWithoutAnotherCreation(t *testing.T) {
	app, f, _, _ := sshVaultAgentFixture(t, "1password")
	runner := &sshVaultOPRunner{t: t, public: string(f.runner.publicLine)}
	ctx := context.WithValue(t.Context(), sshVaultBackendKey{}, sshvault.NewService(runner))
	plan, err := prepareSSHVaultKey(ctx, app, sshflow.VaultKeyRequest{Provider: "1password", AccountID: vaultTestAccount, VaultID: vaultTestVault, Title: "Not yet offered"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := applySSHVaultKey(ctx, plan)
	if err != nil || result.Status != "created" || result.AgentStatus != "not_visible" || result.Receipt == nil || result.Receipt.ItemID != vaultTestItem || len(result.Keys) != 0 {
		t.Fatalf("not-visible key was treated as absent creation: %+v %v", result, err)
	}
	if _, err := applySSHVaultKey(ctx, plan); !errors.Is(err, sshvault.ErrPlanUsed) || runner.creates != 1 {
		t.Fatal("missing agent key caused creation retry")
	}
}

type sshVaultUnsealedBackend struct{ service *sshvault.Service }

func (b sshVaultUnsealedBackend) Plan(ctx context.Context, request sshvault.Request) (sshvault.Plan, error) {
	return b.service.Plan(ctx, request)
}
func (b sshVaultUnsealedBackend) Apply(ctx context.Context, plan sshvault.Plan) (sshvault.Result, error) {
	result, err := b.service.Apply(ctx, plan)
	body, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return sshvault.Result{}, encodeErr
	}
	defer sshcredential.Wipe(body)
	var public sshvault.Result
	if decodeErr := json.Unmarshal(body, &public); decodeErr != nil {
		return public, decodeErr
	}
	return public, err
}

func TestSSHVaultPublicDTOCannotAuthorizeCreatedAgentSelection(t *testing.T) {
	app, f, agent, socket := sshVaultAgentFixture(t, "1password")
	runner := &sshVaultOPRunner{t: t, public: string(f.runner.publicLine), onCreate: func() { agent.agents[socket] = f.runner.publicLine }}
	ctx := context.WithValue(t.Context(), sshVaultBackendKey{}, sshVaultUnsealedBackend{sshvault.NewService(runner)})
	plan, err := prepareSSHVaultKey(ctx, app, sshflow.VaultKeyRequest{Provider: "1password", AccountID: vaultTestAccount, VaultID: vaultTestVault, Title: "Unsealed receipt"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := applySSHVaultKey(ctx, plan)
	if err != nil || result.Receipt == nil || result.Receipt.ItemID != vaultTestItem || len(result.Keys) != 0 || result.AgentStatus != "not_selected" {
		t.Fatalf("public DTO became creation-to-agent authority: %+v %v", result, err)
	}
}

func TestSSHBitwardenDesktopHandoffUsesOnlyNewlyVisibleExactAgentKeys(t *testing.T) {
	app, f, agent, socket := sshVaultAgentFixture(t, "bitwarden")
	agent.agents[socket] = f.runner.publicLine
	backend := &sshVaultFakeBackend{}
	ctx := context.WithValue(t.Context(), sshVaultBackendKey{}, backend)
	plan, err := prepareSSHVaultKey(ctx, app, sshflow.VaultKeyRequest{Provider: "bitwarden", Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	newLine := sshCLITestPublicLine(0x63, "newly visible, not a creation receipt")
	agent.agents[socket] = append(append(append([]byte(nil), f.runner.publicLine...), '\n'), newLine...)
	other := filepath.Join(f.home, "other-agent.sock")
	listenSSHAgentAt(t, other)
	agent.agents[other] = sshCLITestPublicLine(0x15, "wrong ambient key")
	t.Setenv("SSH_AUTH_SOCK", other)
	result, err := applySSHVaultKey(ctx, plan)
	if err != nil || result.Status != "observed" || result.Receipt != nil || len(result.Keys) != 1 || result.Keys[0].Socket != socket || backend.plans != 0 || backend.applies != 0 {
		t.Fatalf("desktop handoff fabricated a vault result or changed sockets: %+v %v", result, err)
	}
	metadata, err := sshhost.ParsePublicKey(newLine)
	if err != nil || result.Keys[0].Fingerprint != metadata.Fingerprint {
		t.Fatal("handoff did not select only newly visible fingerprint")
	}
	picked := false
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		picked = true
		return picker.Result{Item: request.Items[0]}, nil
	}
	if key, err := pickSSHVaultAgentKey(ctx, app, result); err != nil || key == nil || !picked {
		t.Fatal("a single newly visible fingerprint was auto-selected without a picker")
	}
}

type sshVaultAgentFailure struct {
	base  sshhost.Runner
	reads int
}

func (r *sshVaultAgentFailure) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	if request.Name == "ssh-add" {
		r.reads++
		return sshhost.RunResult{ExitCode: 1, Stderr: []byte("transport failed")}, nil
	}
	return r.base.Run(ctx, request)
}

func TestSSHDesktopIncompleteBaselineCannotBecomeEmptyHandoff(t *testing.T) {
	app, _, agent, _ := sshVaultAgentFixture(t, "bitwarden")
	failure := &sshVaultAgentFailure{base: agent}
	app.sshHostRunner, app.sshHostService = failure, nil
	if _, err := prepareSSHVaultKey(t.Context(), app, sshflow.VaultKeyRequest{Provider: "bitwarden", Desktop: true}); err == nil || failure.reads != 1 {
		t.Fatal("failed baseline was treated as complete empty")
	}
}

func TestSSHDesktopReplacedSocketRejectsBeforeAnotherQuery(t *testing.T) {
	app, f, agent := newSSHAgentSetupFixture(t)
	socket := filepath.Join(f.home, ".bitwarden-ssh-agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	agent.agents[socket] = []byte{}
	plan, err := prepareSSHVaultKey(t.Context(), app, sshflow.VaultKeyRequest{Provider: "bitwarden", Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	reads := len(agent.calls)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	result, err := applySSHVaultKey(t.Context(), plan)
	if err == nil || result.Status != "unavailable" || result.Receipt != nil || len(result.Keys) != 0 || len(agent.calls) != reads {
		t.Fatal("socket replacement was accepted as the captured provider")
	}
}

const vaultFrozenSession = "synthetic-adapter-session-not-a-credential"

type sshVaultFrozenBWRunner struct {
	t                            *testing.T
	entry, runtime, profile, cwd string
	agent                        *sshAgentSetupRunner
	socket                       string
	calls, creates               int
	public                       string
}

func (r *sshVaultFrozenBWRunner) Run(context.Context, string, []string, []byte) ([]byte, error) {
	r.t.Fatal("Bitwarden used ambient execution instead of a frozen native context")
	return nil, sshvault.ErrNativeContext
}
func (r *sshVaultFrozenBWRunner) RunWithEnvironment(_ context.Context, name string, args []string, input []byte, environment []string, directory string) ([]byte, error) {
	r.t.Helper()
	r.calls++
	executable := r.entry
	if r.runtime != "" {
		executable = r.runtime
		if len(args) == 0 || args[0] != r.entry {
			r.t.Fatal("Node entrypoint was not pinned")
		}
		args = args[1:]
	}
	if name != executable || directory != r.cwd || len(args) < 2 || args[0] != "--nointeraction" {
		r.t.Fatal("native execution lost its reviewed tool, directory or noninteraction scope")
	}
	env := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	if env["BW_SESSION"] != vaultFrozenSession || env["BW_NOINTERACTION"] != "true" || env["BITWARDENCLI_APPDATA_DIR"] != r.profile {
		r.t.Fatal("native environment was not frozen to the reviewed context")
	}
	args = args[1:]
	if args[0] == "--version" {
		return []byte(sshvault.BitwardenSchemaVersion + "\n"), nil
	}
	if args[0] == "status" {
		return json.Marshal(map[string]any{"status": "unlocked", "userId": vaultTestBWUser, "userEmail": "fixture@example.test", "serverUrl": "https://display.example.test/native?not-endpoint-proof"})
	}
	if len(args) != 2 || args[0] != "create" || args[1] != "item" {
		r.t.Fatal("unexpected native Bitwarden operation")
	}
	r.creates++
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(input)))
	n, err := base64.StdEncoding.Decode(decoded, input)
	defer sshcredential.Wipe(decoded)
	if err != nil {
		r.t.Fatal("creation input was not bounded stdin JSON")
	}
	var item struct {
		Type   int    `json:"type"`
		Name   string `json:"name"`
		Fields []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
			Type  int    `json:"type"`
		} `json:"fields"`
		SSHKey struct {
			PublicKey   string `json:"publicKey"`
			Fingerprint string `json:"keyFingerprint"`
		} `json:"sshKey"`
	}
	if json.Unmarshal(decoded[:n], &item) != nil || item.Type != 5 || item.SSHKey.PublicKey == "" {
		r.t.Fatal("creation did not use the SSH-key item shape")
	}
	r.public = item.SSHKey.PublicKey
	r.agent.agents[r.socket] = []byte(r.public)
	return json.Marshal(map[string]any{"id": vaultTestBWItem, "type": 5, "name": item.Name, "fields": item.Fields, "organizationId": nil, "collectionIds": []string{}, "sshKey": map[string]string{"publicKey": item.SSHKey.PublicKey, "keyFingerprint": item.SSHKey.Fingerprint, "privateKey": vaultPrivateMarker}})
}

func configureSSHVaultFrozenBW(t *testing.T, app *App, f *sshCLIFixture, agent *sshAgentSetupRunner, socket string, node bool) *sshVaultFrozenBWRunner {
	t.Helper()
	bin, profile := filepath.Join(f.home, "vault-bin"), filepath.Join(f.home, "vault-profile")
	for _, dir := range []string{bin, profile} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profile, "data.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &sshVaultFrozenBWRunner{t: t, entry: filepath.Join(bin, "bw"), profile: profile, cwd: f.home, agent: agent, socket: socket}
	binary := runner.entry
	if node {
		root := filepath.Join(f.home, "package", "@bitwarden", "cli")
		if err := os.MkdirAll(filepath.Join(root, "build"), 0700); err != nil {
			t.Fatal(err)
		}
		runner.entry, runner.runtime = filepath.Join(root, "build", "bw.js"), filepath.Join(bin, "node")
		binary = runner.runtime
		if err := os.WriteFile(runner.entry, []byte("#!/usr/bin/env node\n// Inert test entrypoint; never executed.\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@bitwarden/cli","version":"2026.3.0","bin":{"bw":"build/bw.js"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(runner.entry, filepath.Join(bin, "bw")); err != nil {
			t.Fatal(err)
		}
	}
	// Only a native-format marker is needed: the frozen fake runner never executes this file.
	if err := os.WriteFile(binary, []byte("\x7fELF-inert-adapter-fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.home)
	for _, name := range []string{"NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH"} {
		t.Setenv(name, "")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "DYLD_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("BITWARDENCLI_APPDATA_DIR", profile)
	t.Setenv("BW_SESSION", vaultFrozenSession)
	return runner
}

func TestSSHBitwardenNativeContextAdapterKeepsEndpointsUnverified(t *testing.T) {
	for _, node := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone-inert-tool", true: "node-inert-tool"}[node], func(t *testing.T) {
			app, f, agent, socket := sshVaultAgentFixture(t, "bitwarden")
			runner := configureSSHVaultFrozenBW(t, app, f, agent, socket, node)
			ctx := context.WithValue(t.Context(), sshVaultBackendKey{}, sshvault.NewService(runner))
			actions := sshTUIActions(newTUIAppState(app))
			request := sshflow.VaultKeyRequest{Provider: "bitwarden", AccountID: vaultTestBWUser, VaultID: "personal", Title: "Native-context key", Experimental: true, NativeContextApproved: true}
			plan, err := actions.PrepareVaultKey(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			preview := plan.Preview()
			if preview.NativeProfilePath != runner.profile || preview.NativeEntrypoint != runner.entry || preview.NativeRuntime != runner.runtime || preview.EndpointStatus != "unverified" || runner.creates != 0 {
				t.Fatal("review did not show exact native delegation before creation")
			}
			workflow, err := actions.Workflow(ctx, tui.SSHWorkflowRequest{Action: "vault-key", Vault: plan})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			workflow.SetStdout(&output)
			if err := workflow.Run(); err != nil {
				t.Fatal(err)
			}
			result := workflow.Result().Vault
			if result == nil || result.Status != "created" || result.BindingStatus != "unknown" || result.NativeContextStatus != "observed_consistent" || result.EndpointStatus != "unverified" || result.Receipt == nil || result.Receipt.ItemID != vaultTestBWItem || result.AgentStatus != "offered" || len(result.Keys) != 1 || runner.creates != 1 {
				t.Fatal("Bitwarden result upgraded endpoint/binding authority or lost its created receipt")
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			assertSSHVaultPublicOutput(t, output.String()+string(body), runner.public)
			if strings.Contains(output.String()+string(body), vaultFrozenSession) {
				t.Fatal("native session escaped a public projection")
			}
		})
	}
}
