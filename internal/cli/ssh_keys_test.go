package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshKeyInventoryRunner struct {
	base       sshhost.Runner
	agentKeys  []byte
	agentError bool
	calls      []sshhost.RunRequest
}

func (r *sshKeyInventoryRunner) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	r.calls = append(r.calls, request)
	if request.Name == "ssh-add" {
		if r.agentError {
			return sshhost.RunResult{ExitCode: 2}, nil
		}
		return sshhost.RunResult{Stdout: r.agentKeys}, nil
	}
	return r.base.Run(ctx, request)
}

func writeSSHCLIKey(t *testing.T, f *sshCLIFixture, name string) string {
	t.Helper()
	path := filepath.Join(f.home, ".ssh", name)
	if err := os.WriteFile(path, []byte("PRIVATE-KEY-BYTES-MUST-NOT-LEAK\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", f.runner.publicLine, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSSHKeyListLocalSourcesAndExplicitAlias(t *testing.T) {
	for _, mode := range []string{"local", "files", "alias", "missing-agent"} {
		t.Run(mode, func(t *testing.T) {
			f := newSSHCLIFixture(t)
			f.initSSH()
			keyPath := writeSSHCLIKey(t, f, "work-key")
			t.Setenv("SSH_AUTH_SOCK", filepath.Join(f.home, "agent.sock"))
			runner := &sshKeyInventoryRunner{base: f.runner, agentKeys: f.runner.publicLine, agentError: mode == "missing-agent"}
			args := []string{"ssh", "key", "list", "--json"}
			if mode == "files" {
				args = append(args, "--no-agent")
			}
			if mode == "alias" {
				args = append(args, "--alias", "lab")
			}
			out, err := runMachineCLI(t, f, runner, args...)
			if err != nil {
				t.Fatalf("list: %v\n%s", err, out)
			}
			var document sshKeyListDocument
			if err := json.Unmarshal([]byte(out), &document); err != nil {
				t.Fatal(err)
			}
			if document.Kind != "ssh_key_list" || document.SchemaVersion != 1 || len(document.Candidates) != 1 || document.Candidates[0].IdentityFile != keyPath {
				t.Fatalf("unexpected catalog: %s", out)
			}
			key := document.Candidates[0]
			if !key.Provenance.Private || key.Provenance.Agent != (mode == "local" || mode == "alias") {
				t.Fatalf("source provenance: %+v", key)
			}
			if document.Complete != (mode != "missing-agent") || document.Status == "partial" != (mode == "missing-agent") {
				t.Fatalf("partial catalog status: %s", out)
			}
			for _, call := range runner.calls {
				if call.Name == "ssh-add" && mode != "files" {
					continue
				}
				if call.Name == "ssh" && mode == "alias" && hasSSHArg(call.Args, "-G") {
					continue
				}
				t.Fatalf("listing executed unexpected command: %+v", call)
			}
			if strings.Contains(out, "PRIVATE-KEY-BYTES") || strings.Contains(out, strings.Fields(string(f.runner.publicLine))[1]) {
				t.Fatal("listing exposed key material")
			}
		})
	}
}

func TestSSHKeyListMissingDirectoryIsReadOnly(t *testing.T) {
	f := newSSHCLIFixture(t)
	out, _, err := f.run("ssh", "key", "list", "--no-agent")
	if err != nil || !strings.Contains(out, "No local SSH keys found") || f.runner.callCount() != 0 {
		t.Fatalf("empty list: %s error=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".ssh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listing created SSH directory: %v", err)
	}
}

func runSSHKeyWizard(t *testing.T, f *sshCLIFixture, runner sshhost.Runner, input string, chooseKey func(picker.Request) (picker.Result, error), beforeRegistration func()) (string, string, error) {
	t.Helper()
	var output, stderr bytes.Buffer
	app := &App{In: strings.NewReader(input), Out: &output, Err: &stderr, sshHostRunner: runner, interactiveCheck: func() bool { return true }}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		value := ""
		switch request.Prompt {
		case "Choose hosts to set up":
			value = "tailscale"
		case "Select machines (aliases remain independently configurable)":
			return picker.Result{Items: request.Items[:1]}, nil
		case "Authentication for lab":
			value = "key"
		case "SSH key for lab":
			return chooseKey(request)
		case "Optional registration":
			if beforeRegistration != nil {
				beforeRegistration()
			}
			return picker.Result{Items: []picker.Item{}}, nil
		default:
			t.Fatalf("unexpected picker: %+v", request)
		}
		for _, item := range request.Items {
			if item.Value == value {
				return picker.Result{Item: item}, nil
			}
		}
		t.Fatalf("missing picker choice %q: %+v", value, request)
		return picker.Result{}, errors.New("missing choice")
	}
	root := newRootCommand(app)
	root.SetContext(t.Context())
	root.SetArgs([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never", "ssh", "setup"})
	err := root.Execute()
	return output.String(), stderr.String(), err
}

func TestSSHKeyWizardPreservesFileAndAgentCandidates(t *testing.T) {
	for _, agentOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "agent-only"}[agentOnly], func(t *testing.T) {
			f := newSSHCLIFixture(t)
			f.initSSH()
			keyPath := ""
			if !agentOnly {
				keyPath = writeSSHCLIKey(t, f, "work-key")
			}
			t.Setenv("SSH_AUTH_SOCK", filepath.Join(f.home, "agent.sock"))
			runner := &sshKeyInventoryRunner{base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}, agentKeys: f.runner.publicLine}
			meta, err := sshhost.ParsePublicKey(f.runner.publicLine)
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err := runSSHKeyWizard(t, f, runner, "lab\n\ntester\n22\ny\n", func(request picker.Request) (picker.Result, error) {
				if request.Multi || len(request.Items) != 2 || request.Items[0].Value != meta.Fingerprint || !strings.Contains(request.Items[0].Description, "agent") {
					t.Fatalf("key picker lacks safe exact identity metadata: %+v", request)
				}
				return picker.Result{Item: request.Items[0]}, nil
			}, nil)
			if err != nil {
				t.Fatalf("wizard: %v\n%s\n%s", err, out, stderr)
			}
			data, err := os.ReadFile(f.managedPath("lab"))
			if err != nil {
				t.Fatal(err)
			}
			definition, err := sshhost.ParseManaged(data)
			if err != nil || definition.IdentityFile != keyPath {
				t.Fatalf("selected identity was lost or agent acquired a synthetic path: %+v %v", definition, err)
			}
			proof := false
			for _, call := range runner.calls {
				proof = proof || call.Display == "ssh selected-key-only authentication proof"
			}
			if !proof || !strings.Contains(out, "lab authenticate: ready") {
				t.Fatalf("candidate did not reach exact bootstrap: %s", out)
			}
		})
	}
}

func TestSSHKeyWizardCancellationBeforeConfiguration(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	runner := &sshKeyInventoryRunner{base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}}
	_, _, err := runSSHKeyWizard(t, f, runner, "lab\n\ntester\n22\n", func(picker.Request) (picker.Result, error) { return picker.Result{}, picker.ErrCanceled }, nil)
	if !errors.Is(err, errPromptCanceled) {
		t.Fatalf("cancellation=%v", err)
	}
	for _, path := range []string{f.managedPath("lab"), registryForFixture(f).Path} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancel created %s: %v", path, err)
		}
	}
	for _, call := range runner.calls {
		if call.Name == "ssh" || call.Name == "ssh-keygen" {
			t.Fatalf("canceled selection invoked %s", call.Display)
		}
	}
}

func TestSSHKeyWizardAgentDisappearsBeforeAnyConfiguration(t *testing.T) {
	f := newSSHCLIFixture(t)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(f.home, "agent.sock"))
	runner := &sshKeyInventoryRunner{base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}, agentKeys: f.runner.publicLine}
	out, stderr, err := runSSHKeyWizard(t, f, runner, "lab\n\ntester\n22\ny\n", func(request picker.Request) (picker.Result, error) {
		return picker.Result{Item: request.Items[0]}, nil
	}, func() { runner.agentKeys = nil })
	if err == nil {
		t.Fatalf("disappeared key still applied: %s\n%s", out, stderr)
	}
	for _, path := range []string{f.rootConfigPath(), f.managedPath("lab"), registryForFixture(f).Path} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("disappeared key caused configuration at %s: %v", path, err)
		}
	}
	for _, call := range runner.calls {
		if call.Name == "ssh" || call.Name == "ssh-keygen" {
			t.Fatalf("disappeared key reached authentication or generation: %s", call.Display)
		}
	}
}

func TestSSHKeyWizardManualCompanionDerivationIsReviewedOnce(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline-setup", true: "apply"}[apply], func(t *testing.T) {
			f := newSSHCLIFixture(t)
			f.initSSH()
			path := filepath.Join(f.home, ".ssh", "private-without-companion")
			if err := os.WriteFile(path, []byte("PRIVATE-KEY-BYTES-MUST-NOT-LEAK\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := &sshKeyInventoryRunner{base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}}
			answer := "n"
			if apply {
				answer = "y"
			}
			input := "lab\n\ntester\n22\n" + path + "\ny\n" + answer + "\n"
			out, stderr, err := runSSHKeyWizard(t, f, runner, input, func(request picker.Request) (picker.Result, error) {
				for _, item := range request.Items {
					if item.Value == "manual" {
						return picker.Result{Item: item}, nil
					}
				}
				t.Fatal("missing manual path fallback")
				return picker.Result{}, nil
			}, func() {
				if _, err := os.Stat(path + ".pub"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("derived companion before overall confirmation: %v", err)
				}
			})
			if apply && err != nil || !apply && !errors.Is(err, errPromptCanceled) {
				t.Fatalf("manual selection: %v\n%s\n%s", err, out, stderr)
			}
			if strings.Count(out, "Derive and save the missing public key companion?") != 1 {
				t.Fatalf("public derivation prompted repeatedly: %s", out)
			}
			_, statErr := os.Stat(path + ".pub")
			if apply && statErr != nil || !apply && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("companion outcome does not match approval: %v", statErr)
			}
		})
	}
}
