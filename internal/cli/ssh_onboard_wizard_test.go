package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const sshOnboardWizardConfirmation = "Apply these configurations, machine mappings and selected remote actions?"

type sshOnboardWizardOutput struct {
	bytes.Buffer
	confirmations int
	beforeConfirm func()
}

func (w *sshOnboardWizardOutput) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(sshOnboardWizardConfirmation)) {
		w.confirmations++
		if w.beforeConfirm != nil {
			w.beforeConfirm()
		}
	}
	return w.Buffer.Write(data)
}

func runSSHOnboardWizardCLI(t *testing.T, f *sshCLIFixture, runner sshhost.Runner, input, source string, authentication map[string]string, beforeConfirm func()) (string, int, error) {
	t.Helper()
	output := &sshOnboardWizardOutput{beforeConfirm: beforeConfirm}
	var stderr bytes.Buffer
	app := &App{In: strings.NewReader(input), Out: output, Err: &stderr, sshHostRunner: runner, interactiveCheck: func() bool { return true }}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		value := ""
		switch {
		case request.Prompt == "Choose hosts to set up":
			value = source
		case request.Prompt == "Select machines (aliases remain independently configurable)":
			if !request.Multi || len(request.Items) != 1 {
				t.Fatalf("expected one multi-select candidate, got %+v", request)
			}
			return picker.Result{Items: []picker.Item{request.Items[0]}}, nil
		case strings.HasPrefix(request.Prompt, "Authentication for "):
			alias := strings.TrimPrefix(request.Prompt, "Authentication for ")
			var found bool
			value, found = authentication[alias]
			if !found {
				t.Fatalf("unexpected authentication alias %q", alias)
			}
		case request.Prompt == "Optional registration":
			if !request.Multi || len(request.Items) != 2 || len(request.Selected) != 0 {
				t.Fatalf("expected unchecked registration destinations, got %+v", request)
			}
			return picker.Result{Items: []picker.Item{}}, nil
		default:
			t.Fatalf("unexpected picker: %+v", request)
		}
		if request.Multi {
			t.Fatalf("unexpected multiple choice: %+v", request)
		}
		for _, item := range request.Items {
			if item.Value == value {
				return picker.Result{Item: item}, nil
			}
		}
		t.Fatalf("picker %q omits nonempty value %q: %+v", request.Prompt, value, request.Items)
		return picker.Result{}, errors.New("missing choice")
	}
	root := newRootCommand(app)
	root.SetContext(t.Context())
	root.SetArgs([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never", "ssh", "setup"})
	err := root.Execute()
	if err != nil {
		t.Logf("wizard stderr: %s", stderr.String())
	}
	return output.String(), output.confirmations, err
}

func assertSSHOnboardWizardUnwritten(t *testing.T, f *sshCLIFixture, aliases ...string) {
	t.Helper()
	paths := []string{f.rootConfigPath(), registryForFixture(f).Path}
	for _, alias := range aliases {
		paths = append(paths, f.managedPath(alias))
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wizard wrote %s before overall confirmation: %v", path, err)
		}
	}
}

func TestSSHOnboardWizardTwoAliasesOnePeerConfirmBeforeWrites(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	input := "lab,lab-admin\nlab.tailtest.ts.net\ndeveloper\n2222\n100.77.43.16\nroot\n2200\ny\n"
	checked := false
	out, confirmations, err := runSSHOnboardWizardCLI(t, f, runner, input, "tailscale", map[string]string{"lab": "config", "lab-admin": "config"}, func() {
		checked = true
		assertSSHOnboardWizardUnwritten(t, f, "lab", "lab-admin")
		for _, call := range runner.calls {
			if strings.HasPrefix(call, "ssh ") || strings.HasPrefix(call, "ssh-keygen ") {
				t.Fatalf("configuration-only preview ran %s", call)
			}
		}
	})
	if err != nil || !checked || confirmations != 1 {
		t.Fatalf("confirmations=%d checked=%v error=%v\n%s", confirmations, checked, err, out)
	}
	if strings.Contains(out, "Apply this local SSH setup plan?") {
		t.Fatal("nested alias setup requested another confirmation")
	}
	for _, want := range []sshhost.ManagedDefinition{{Alias: "lab", HostName: "lab.tailtest.ts.net", User: "developer", Port: 2222}, {Alias: "lab-admin", HostName: "100.77.43.16", User: "root", Port: 2200}} {
		data, err := os.ReadFile(f.managedPath(want.Alias))
		if err != nil {
			t.Fatal(err)
		}
		got, err := sshhost.ParseManaged(data)
		if err != nil || got.Alias != want.Alias || got.HostName != want.HostName || got.User != want.User || got.Port != want.Port || got.IdentityFile != "" {
			t.Fatalf("alias=%+v expected=%+v error=%v", got, want, err)
		}
	}
	snapshot, err := registryForFixture(f).Read(t.Context())
	if err != nil || len(snapshot.Machines) != 1 || len(snapshot.Bindings) != 3 {
		t.Fatalf("registry=%+v error=%v", snapshot, err)
	}
	for _, binding := range snapshot.Bindings {
		if binding.MachineID != snapshot.Machines[0].ID || binding.Suppressed {
			t.Fatalf("aliases did not share one canonical ID: %+v", snapshot)
		}
	}
	paths, err := sshhost.NewPaths(f.home)
	if err != nil {
		t.Fatal(err)
	}
	service, err := sshhost.NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	hints, err := service.ConnectionHints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, hint := range hints {
		binding, ok := snapshot.LookupBinding("ssh", paths.RootConfig, strings.ToLower(hint.Alias))
		if !ok || binding.Fingerprint != hint.Fingerprint {
			t.Fatalf("later configuration immediately staled earlier alias: %+v %+v", hint, binding)
		}
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "authentication proof") || strings.Contains(call, "authentication probe") || strings.Contains(call, "installer") || strings.HasPrefix(call, "ssh-keygen ") {
			t.Fatalf("config-only wizard authenticated or installed a key: %s", call)
		}
	}
}

func TestSSHOnboardWizardExistingAuthenticationWithoutRegistration(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(true)}
	out, confirmations, err := runSSHOnboardWizardCLI(t, f, runner, "lab\n\ntester\n22\ny\n", "tailscale", map[string]string{"lab": "existing"}, nil)
	if err != nil || confirmations != 1 || !strings.Contains(out, "lab authenticate: ready") {
		t.Fatalf("confirmations=%d error=%v\n%s", confirmations, err, out)
	}
	probes := 0
	for _, call := range runner.calls {
		if strings.Contains(call, "fresh authentication probe") {
			probes++
		}
		if strings.HasPrefix(call, "ssh-keygen ") || strings.Contains(call, "selected-key") || strings.Contains(call, "installer") || strings.HasPrefix(call, "herdr ") {
			t.Fatalf("no-registration keyless flow ran %s", call)
		}
	}
	if probes != 1 {
		t.Fatalf("expected one ordinary proof, got %d: %v", probes, runner.calls)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.remotesPath), "remotes.d", "ssh-lab.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("none registration wrote fleet: %v", err)
	}
	data, err := os.ReadFile(f.managedPath("lab"))
	if err != nil || bytes.Contains(data, []byte("IdentityFile")) {
		t.Fatalf("keyless configuration=%s error=%v", data, err)
	}
}

func TestSSHOnboardWizardForeignAliasReusesFieldsWithoutPrompts(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.appendRootConfig("# owned by the user\nHost foreign\n HostName foreign.example\n User foreign-user\n Port 2207\n")
	before, err := os.ReadFile(f.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	out, confirmations, err := runSSHOnboardWizardCLI(t, f, runner, "\ny\n", "existing", map[string]string{"foreign": "existing"}, nil)
	if err != nil || confirmations != 1 {
		t.Fatalf("confirmations=%d error=%v\n%s", confirmations, err, out)
	}
	for _, prompt := range []string{"HostName (IP or MagicDNS FQDN)", "Default remote user for", "SSH port:"} {
		if strings.Contains(out, prompt) {
			t.Fatalf("foreign settings were solicited then discarded: %s", out)
		}
	}
	if !strings.Contains(out, "Reuse foreign with its existing OpenSSH connection settings.") || !strings.Contains(out, "foreign-user") || !strings.Contains(out, "2207") {
		t.Fatalf("preview omitted existing foreign fields: %s", out)
	}
	after, err := os.ReadFile(f.rootConfigPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("foreign config changed: %v\n%s", err, after)
	}
	if _, err := os.Stat(f.managedPath("foreign")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign alias acquired a managed fragment: %v", err)
	}
}

type sshOnboardWizardKeyRunner struct {
	t                        *testing.T
	base                     *sshMachineRunner
	alias, keyPath           string
	exactCalls, installCalls int
}

func (r *sshOnboardWizardKeyRunner) Run(ctx context.Context, q sshhost.RunRequest) (sshhost.RunResult, error) {
	if q.Name == "ssh" && q.Display == "ssh selected-key-only authentication proof" {
		r.exactCalls++
		data, err := os.ReadFile(sshArgValue(q.Args, "-F"))
		if err != nil {
			r.t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(r.keyPath)) {
			r.t.Fatalf("selected identity missing from exact proof: %s", data)
		}
		managed, err := os.ReadFile(filepath.Join(r.base.base.home, ".ssh", "dev.d", r.alias+".conf"))
		if err != nil {
			r.t.Fatal(err)
		}
		definition, err := sshhost.ParseManaged(managed)
		if err != nil || definition.IdentityFile != r.keyPath {
			r.t.Fatalf("bootstrap ran before selected identity was configured: %+v %v", definition, err)
		}
		if r.exactCalls == 1 {
			if err := os.WriteFile(sshArgValue(q.Args, "-E"), nil, 0o600); err != nil {
				r.t.Fatal(err)
			}
			return sshhost.RunResult{ExitCode: 255}, nil
		}
	}
	if q.Name == "ssh" && q.Display == "ssh public-key installer" {
		r.installCalls++
		want := append(append([]byte{}, r.base.base.publicLine...), '\n')
		if !bytes.Equal(q.Stdin, want) {
			r.t.Fatal("installer did not receive the selected public key")
		}
	}
	return r.base.Run(ctx, q)
}

func TestSSHOnboardGeneratedKeyResultAndIdentitySurviveAuthentication(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("installer-failure=%t", failure), func(t *testing.T) {
			f := newSSHCLIFixture(t)
			f.initSSH()
			if failure {
				f.runner.installerExit = 255
			}
			keyPath := filepath.Join(f.home, ".ssh", "selected-key")
			runner := &sshOnboardWizardKeyRunner{t: t, base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}, alias: "generated", keyPath: keyPath}
			out, err := runMachineCLI(t, f, runner, "ssh", "setup", "generated", "--from", "tailscale:lab", "--user", "tester", "--generate-key", "--key-path", keyPath, "--no-passphrase", "--target-os", "posix", "--yes", "--json")
			if (err != nil) != failure {
				t.Fatalf("error=%v\n%s", err, out)
			}
			var result struct {
				sshflow.OnboardResult
				Keys       map[string]sshhost.KeyResult
				Bootstraps map[string]sshhost.BootstrapResult
			}
			if e := json.Unmarshal([]byte(out), &result); e != nil {
				t.Fatalf("result JSON: %v\n%s", e, out)
			}
			key, ok := result.Keys["generated"]
			if !ok || !key.Created || !key.Retained || key.Operation != sshhost.KeyGenerate || key.Candidate.IdentityFile != keyPath || key.Candidate.Fingerprint == "" {
				t.Fatalf("selected key result=%+v", key)
			}
			for _, path := range []string{keyPath, keyPath + ".pub", f.managedPath("generated")} {
				if _, e := os.Stat(path); e != nil {
					t.Fatalf("selected key/configuration was not retained: %s %v", path, e)
				}
			}
			data, e := os.ReadFile(f.managedPath("generated"))
			if e != nil {
				t.Fatal(e)
			}
			definition, e := sshhost.ParseManaged(data)
			if e != nil || definition.IdentityFile != keyPath {
				t.Fatalf("configured identity=%+v error=%v", definition, e)
			}
			bootstrap, ok := result.Bootstraps["generated"]
			if !ok || len(bootstrap.Hops) != 1 || runner.installCalls != 1 {
				t.Fatalf("bootstrap=%+v installerCalls=%d", bootstrap, runner.installCalls)
			}
			if failure {
				if result.Status != "partial" || !bootstrap.Hops[0].Unknown || bootstrap.Ready {
					t.Fatalf("interrupted install invented rollback/success: %+v", result)
				}
			} else if result.Status != "ready" || !bootstrap.Hops[0].Installed || !bootstrap.Hops[0].Verified || !bootstrap.Ready || runner.exactCalls != 2 {
				t.Fatalf("selected key not installed and verified: %+v", result)
			}
			if strings.Contains(out, "PRIVATE-KEY-BYTES-MUST-NOT-LEAK") || strings.Contains(out, strings.Fields(string(f.runner.publicLine))[1]) {
				t.Fatal("result leaked key material")
			}
		})
	}
}

func TestSSHOnboardGeneratedKeyInitializesFreshSSHDirectory(t *testing.T) {
	f := newSSHCLIFixture(t)
	keyPath := filepath.Join(f.home, ".ssh", "first-key")
	runner := &sshOnboardWizardKeyRunner{t: t, base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}, alias: "generated", keyPath: keyPath}
	out, err := runMachineCLI(t, f, runner, "ssh", "setup", "generated", "--from", "tailscale:lab", "--user", "tester", "--generate-key", "--key-path", keyPath, "--no-passphrase", "--target-os", "posix", "--yes", "--json")
	if err != nil {
		t.Fatalf("fresh-home key setup did not apply initialization before key generation: %v\n%s", err, out)
	}
	for _, path := range []string{f.rootConfigPath(), f.managedPath("generated"), keyPath, keyPath + ".pub"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fresh-home setup omitted %s: %v", path, err)
		}
	}
}
