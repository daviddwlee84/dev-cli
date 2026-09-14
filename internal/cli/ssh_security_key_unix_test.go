//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type sshHardwareAdapterRunner struct {
	base           *sshCLIRunner
	calls          []sshhost.RunRequest
	cancelGenerate bool
	collision      string
	starts         int
	extraIdentity  string
	nativeGates    []sshhost.RunRequest
	nativeConfigs  []string
	exactConfigs   []string
}

func (r *sshHardwareAdapterRunner) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	r.calls = append(r.calls, request)
	name := filepath.Base(request.Name)
	if name == "ssh-add" {
		return sshhost.RunResult{ExitCode: 1}, nil
	}
	if name == "ssh" && hasSSHArg(request.Args, "-G") && r.extraIdentity != "" {
		request.Name = name
		result, err := r.base.Run(ctx, request)
		result.Stdout = fmt.Appendf(result.Stdout, "identityfile %s\n", r.extraIdentity)
		return result, err
	}
	if name == "ssh" && (request.Display == "ssh native hardware public-key authentication gate" || request.Display == "ssh selected-key-only authentication proof") {
		body, err := os.ReadFile(sshArgValue(request.Args, "-F"))
		if err != nil {
			return sshhost.RunResult{}, err
		}
		if request.Display == "ssh native hardware public-key authentication gate" {
			r.nativeGates = append(r.nativeGates, request)
			r.nativeConfigs = append(r.nativeConfigs, string(body))
			if log := sshArgValue(request.Args, "-E"); log != "" {
				if err := os.WriteFile(log, []byte("Authenticated to target using \"publickey\".\n"), 0600); err != nil {
					return sshhost.RunResult{}, err
				}
			}
			return sshhost.RunResult{}, nil
		}
		r.exactConfigs = append(r.exactConfigs, string(body))
	}
	if name == "ssh-keygen" {
		if !hasSSHArg(request.Args, "-y") {
			r.starts++
			if request.OnStarted != nil {
				request.OnStarted(time.Now())
			}
			if r.cancelGenerate {
				return sshhost.RunResult{}, context.Canceled
			}
		} else if r.collision != "" {
			if err := os.WriteFile(r.collision, []byte("unrelated destination created during native generation"), 0600); err != nil {
				return sshhost.RunResult{}, err
			}
			r.collision = ""
		}
	}
	request.Name = name
	return r.base.Run(ctx, request)
}

func sshHardwareAdapterPublicKey() []byte {
	wire := func(value []byte) []byte {
		result := make([]byte, 4+len(value))
		binary.BigEndian.PutUint32(result, uint32(len(value)))
		copy(result[4:], value)
		return result
	}
	algorithm := "sk-ssh-ed25519@openssh.com"
	blob := append(wire([]byte(algorithm)), wire(bytes.Repeat([]byte{0x34}, 32))...)
	blob = append(blob, wire([]byte("ssh:adapter"))...)
	return []byte(algorithm + " " + base64.StdEncoding.EncodeToString(blob) + " adapter key")
}

func sshHardwareAdapterFixture(t *testing.T) (*App, *sshCLIFixture, *sshHardwareAdapterRunner, string) {
	t.Helper()
	app, f, _ := newSSHAgentSetupFixture(t)
	bin := filepath.Join(f.home, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssh", "ssh-keygen"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("inert test executable; must never be executed"), 0500); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.runner.publicLine = sshHardwareAdapterPublicKey()
	runner := &sshHardwareAdapterRunner{base: f.runner}
	app.sshHostRunner, app.sshHostService = runner, nil
	app.interactiveCheck = func() bool { return true }
	return app, f, runner, bin
}

func runSSHHardwareAdapterCLI(t *testing.T, app *App, f *sshCLIFixture, flags ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app.Out, app.Err = &out, &out
	root := newRootCommand(app)
	root.SetContext(t.Context())
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, flags...))
	err := root.Execute()
	return out.String(), err
}

func TestSSHHardwareCLIFlagsReachNativeGeneratorAndManagedV2(t *testing.T) {
	app, f, runner, bin := sshHardwareAdapterFixture(t)
	provider := filepath.Join(bin, "custom provider.so")
	if err := os.WriteFile(provider, []byte("inert provider fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(f.home, ".ssh", "hardware")
	out, err := runSSHHardwareAdapterCLI(t, app, f, "ssh", "setup", "lab", "--hostname", "lab.example", "--user", "tester", "--generate-key", "--key-type", "ed25519-sk", "--key-path", identity, "--comment", "adapter key", "--sk-provider", provider, "--sk-resident", "--sk-verify-required", "--sk-application", "ssh:adapter", "--no-passphrase", "--target-os", "posix", "--yes")
	if err != nil {
		t.Fatalf("hardware adapter setup: %v\n%s", err, out)
	}
	var generated *sshhost.RunRequest
	for i := range runner.calls {
		call := &runner.calls[i]
		if filepath.Base(call.Name) == "ssh-keygen" && hasSSHArg(call.Args, "-t") {
			generated = call
		}
	}
	if generated == nil || generated.Name != filepath.Join(bin, "ssh-keygen") || !generated.Interactive || sshArgValue(generated.Args, "-t") != "ed25519-sk" || sshArgValue(generated.Args, "-w") != provider || !hasSSHArg(generated.Args, "resident") || !hasSSHArg(generated.Args, "verify-required") || !hasSSHArg(generated.Args, "application=ssh:adapter") || !hasSSHArg(generated.Args, "-N") || sshArgValue(generated.Args, "-C") != "adapter key" {
		t.Fatalf("native options lost: %+v", generated)
	}
	if runner.starts != 1 {
		t.Fatalf("hardware generation repeated: %d", runner.starts)
	}
	body, err := os.ReadFile(f.managedPath("lab"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := sshhost.ParseManaged(body)
	if err != nil || !strings.HasPrefix(string(body), sshhost.ManagedHeaderV2+"\n") || definition.SecurityKeyProvider != provider || definition.IdentityFile != identity || definition.IdentitiesOnly == nil || !*definition.IdentitiesOnly {
		t.Fatalf("hardware provider intent lost: %+v %v", definition, err)
	}
}

func TestSSHHardwareFinalGateAllowsNativeInteractionWithoutSelectingOnlyOneIdentity(t *testing.T) {
	app, f, runner, bin := sshHardwareAdapterFixture(t)
	runner.base.ordinaryReady = false // The historical final BatchMode proof cannot complete hardware interaction.
	runner.extraIdentity = filepath.Join(f.home, ".ssh", "ordinary-extra")
	identity := filepath.Join(f.home, ".ssh", "native-hardware")
	out, err := runSSHHardwareAdapterCLI(t, app, f, "ssh", "setup", "lab", "--hostname", "lab.example", "--user", "tester", "--generate-key", "--key-type", "ed25519-sk", "--key-path", identity, "--sk-verify-required", "--target-os", "posix", "--yes")
	if err != nil {
		t.Fatalf("explicit hardware setup failed its native final gate: %v\n%s", err, out)
	}
	if len(runner.nativeGates) != 1 || len(runner.exactConfigs) == 0 {
		t.Fatalf("setup did not perform independent exact and native proofs: native=%d exact=%d", len(runner.nativeGates), len(runner.exactConfigs))
	}
	gate := runner.nativeGates[0]
	if !gate.Interactive || gate.Name != filepath.Join(bin, "ssh") {
		t.Fatalf("final gate lost native interaction or captured client: %+v", gate)
	}
	native := runner.nativeConfigs[0]
	if !strings.Contains(native, identity) || !strings.Contains(native, runner.extraIdentity) {
		t.Fatalf("ordinary gate discarded native IdentityFile policy: %s", native)
	}
	for _, exact := range runner.exactConfigs {
		if strings.Contains(exact, runner.extraIdentity) || exact == native {
			t.Fatalf("selected-key proof was weakened or reused as the ordinary gate: %s", exact)
		}
	}
	for _, required := range []string{"batchmode no", "passwordauthentication no", "kbdinteractiveauthentication no", "gssapiauthentication no", "hostbasedauthentication no", "preferredauthentications publickey"} {
		if !strings.Contains(strings.ToLower(native), required) {
			t.Fatalf("native hardware gate omits %q: %s", required, native)
		}
	}
}

func TestSSHTUIHardwareAdapterCarriesOptionsAndUnknownEffect(t *testing.T) {
	app, f, runner, bin := sshHardwareAdapterFixture(t)
	request := sshflow.OnboardRequest{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix", GenerateKey: true, KeyPath: "hardware", KeyComment: "adapter key", KeyType: sshhost.KeyTypeEd25519SK, SecurityKey: sshhost.SecurityKeyOptions{Provider: "internal", Resident: true, VerifyRequired: true, Application: "ssh:adapter"}}
	plan, err := prepareSSHTUIOnboarding(t.Context(), app, request)
	if err != nil {
		t.Fatal(err)
	}
	item := plan.(*sshTUIOnboardingPlan).prepared.items[0]
	if item.KeyPlan.KeyType != request.KeyType || item.KeyPlan.SecurityKey != request.SecurityKey || item.Definition.SecurityKeyProvider != "internal" {
		t.Fatalf("TUI adapter lost hardware options: %+v %+v", item.KeyPlan, item.Definition)
	}
	preview := strings.Join(plan.Preview().Notes, "\n")
	for _, value := range []string{filepath.Join(bin, "ssh-keygen"), filepath.Join(bin, "ssh"), "ed25519-sk", "internal", "ssh:adapter", "PIN/touch", "resident", "cancellation"} {
		if !strings.Contains(strings.ToLower(preview), strings.ToLower(value)) {
			t.Fatalf("hardware preview omits %q: %s", value, preview)
		}
	}
	if runner.starts != 0 {
		t.Fatal("TUI preview started hardware generation")
	}
	runner.cancelGenerate = true
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: plan}}
	if err := workflow.Run(); err == nil {
		t.Fatal("interrupted hardware generation reported success")
	}
	result := workflow.Result().Onboarding
	if result == nil || result.Status != "partial" || len(result.Outcomes) == 0 || result.Outcomes[0].Status != "unknown" || result.Keys["lab"].Hardware == nil || result.Keys["lab"].Hardware.Status != "unknown" || !result.Keys["lab"].Hardware.Resident {
		t.Fatalf("unknown hardware effect disappeared: %+v", result)
	}
	if _, err := os.Stat(f.managedPath("lab")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted key generation installed managed configuration")
	}
	_ = workflow.Run()
	if runner.starts != 1 {
		t.Fatal("interrupted enrollment was retried")
	}
}

func TestSSHTUIHardwareSuccessPersistsReviewedProvider(t *testing.T) {
	app, f, _, bin := sshHardwareAdapterFixture(t)
	provider := filepath.Join(bin, "tui-provider.so")
	if err := os.WriteFile(provider, []byte("inert provider fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	request := sshflow.OnboardRequest{Alias: "lab", HostName: "lab.example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix", GenerateKey: true, KeyPath: "tui-hardware", KeyType: sshhost.KeyTypeEd25519SK, SecurityKey: sshhost.SecurityKeyOptions{Provider: provider, VerifyRequired: true, Application: "ssh:adapter"}}
	plan, err := prepareSSHTUIOnboarding(t.Context(), app, request)
	if err != nil {
		t.Fatal(err)
	}
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: plan}}
	if err := workflow.Run(); err != nil {
		t.Fatal(err)
	}
	result := workflow.Result().Onboarding
	if result == nil || result.Status != "ready" || result.Keys["lab"].Hardware == nil || result.Keys["lab"].Hardware.Status != "created" || !result.Keys["lab"].Candidate.Provenance.SecurityKeyStub || result.Keys["lab"].Candidate.Provenance.Private {
		t.Fatalf("TUI success lost hardware key provenance: %+v", result)
	}
	body, err := os.ReadFile(f.managedPath("lab"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := sshhost.ParseManaged(body)
	if err != nil || definition.SecurityKeyProvider != provider || definition.IdentityFile != filepath.Join(f.home, ".ssh", request.KeyPath) {
		t.Fatalf("TUI apply did not persist reviewed hardware provider: %+v %v", definition, err)
	}
}

func TestSSHHardwareCLIReportsValidatedRecoveryAfterPublicationCollision(t *testing.T) {
	app, f, runner, _ := sshHardwareAdapterFixture(t)
	identity := filepath.Join(f.home, ".ssh", "collision")
	runner.collision = identity
	out, err := runSSHHardwareAdapterCLI(t, app, f, "ssh", "setup", "lab", "--hostname", "lab.example", "--generate-key", "--key-type", "ed25519-sk", "--key-path", identity, "--target-os", "posix", "--yes")
	if err == nil || !strings.Contains(out, "Hardware key fido: created") || !strings.Contains(out, "Retained key handle:") || !strings.Contains(out, "Retained public key:") || !strings.Contains(out, "no hardware rollback") {
		t.Fatalf("validated recovery receipt lost: %v\n%s", err, out)
	}
	body, err := os.ReadFile(identity)
	if err != nil || string(body) != "unrelated destination created during native generation" {
		t.Fatal("hardware publication overwrote a conflicting destination")
	}
	if runner.starts != 1 {
		t.Fatal("failed publication retried hardware enrollment")
	}
}

func TestSSHHardwareNoninteractiveAndSecureEnclaveRequestsNeverGenerate(t *testing.T) {
	for _, provider := range []string{"internal", "apple-secure-enclave"} {
		t.Run(provider, func(t *testing.T) {
			app, f, runner, _ := sshHardwareAdapterFixture(t)
			args := []string{"ssh", "setup", "lab", "--hostname", "lab.example", "--generate-key", "--key-type", "ed25519-sk", "--sk-provider", provider, "--key-path", filepath.Join(f.home, ".ssh", "not-created"), "--target-os", "posix", "--yes"}
			if provider == "internal" {
				args = append(args, "--json", "--no-passphrase")
			}
			if out, err := runSSHHardwareAdapterCLI(t, app, f, args...); err == nil || runner.starts != 0 {
				t.Fatalf("unsupported hardware request reached generation: %v\n%s", err, out)
			}
		})
	}
}

func TestSSHHardwareForeignProviderMismatchStopsBeforeEnrollment(t *testing.T) {
	app, f, runner, bin := sshHardwareAdapterFixture(t)
	f.appendRootConfig("Host foreign\n    HostName foreign.example\n    User tester\n")
	before, err := os.ReadFile(f.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(bin, "external.so")
	if err := os.WriteFile(provider, []byte("inert provider"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runSSHHardwareAdapterCLI(t, app, f, "ssh", "setup", "foreign", "--generate-key", "--key-type", "ed25519-sk", "--sk-provider", provider, "--key-path", filepath.Join(f.home, ".ssh", "foreign-sk"), "--target-os", "posix", "--yes")
	if err == nil || runner.starts != 0 || !strings.Contains(out, "foreign") {
		t.Fatalf("foreign provider mismatch was not blocked: %v\n%s", err, out)
	}
	after, err := os.ReadFile(f.rootConfigPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign provider check rewrote native configuration")
	}
}

func TestSSHHardwarePickerCapabilityIsStatOnly(t *testing.T) {
	app, _, runner, _ := sshHardwareAdapterFixture(t)
	service, err := app.sshHosts()
	if err != nil {
		t.Fatal(err)
	}
	choices, err := sshSecurityKeyChoices(t.Context(), service, "lab", false)
	if err != nil || len(choices) == 0 || choices[0].UnavailableReason != "" || choices[0].KeyType != sshhost.KeyTypeEd25519SK || !strings.Contains(choices[0].Description, "unverified") {
		t.Fatalf("stat-only unknown capability mislabeled: %+v %v", choices, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("picker capability executed a command: %+v", runner.calls)
	}
	blocked, err := sshSecurityKeyChoices(t.Context(), service, "lab", true)
	if err != nil || blocked[0].UnavailableReason == "" || !strings.Contains(blocked[0].UnavailableReason, "FROM fleet") {
		t.Fatalf("fleet import exposed unsupported hardware generation: %+v %v", blocked, err)
	}
}
