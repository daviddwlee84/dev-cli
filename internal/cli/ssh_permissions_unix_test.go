//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/picker"
)

func TestSSHWizardRepairsPermissionsBeforeAnyPicker(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline", true: "confirm-then-cancel"}[confirm], func(t *testing.T) {
			f := newSSHCLIFixture(t)
			f.initSSH()
			sshDir := filepath.Join(f.home, ".ssh")
			if err := os.Chmod(sshDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(f.rootConfigPath(), 0o644); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			input := "n\n"
			if confirm {
				input = "y\n"
			}
			app := &App{In: strings.NewReader(input), Out: &out, Err: &stderr, sshHostRunner: f.runner, interactiveCheck: func() bool { return true }}
			picked := false
			app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
				picked = true
				if !confirm || request.Prompt != "Choose hosts to set up" {
					t.Fatalf("unexpected picker before permission repair: %+v", request)
				}
				for path, mode := range map[string]os.FileMode{sshDir: 0o700, f.rootConfigPath(): 0o600} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != mode {
						t.Fatalf("picker began before %s repair: %v", path, err)
					}
				}
				return picker.Result{}, picker.ErrCanceled
			}
			root := newRootCommand(app)
			root.SetContext(t.Context())
			root.SetArgs([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never", "ssh", "setup"})
			err := root.Execute()
			if err == nil || picked != confirm || f.runner.callCount() != 0 {
				t.Fatalf("permission gate picked=%v calls=%d error=%v\n%s\n%s", picked, f.runner.callCount(), err, out.String(), stderr.String())
			}
			if !strings.Contains(out.String(), "0755 → 0700") || !strings.Contains(out.String(), "0644 → 0600") || !strings.Contains(out.String(), "remain in place") {
				t.Fatalf("permission preview omitted concrete effects: %s", out.String())
			}
			want := os.FileMode(0o755)
			if confirm {
				want = 0o700
			}
			info, err := os.Stat(sshDir)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("later cancellation undid approved repair: %v", err)
			}
			if _, err := os.Stat(registryForFixture(f).Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("permission-only operation created registry: %v", err)
			}
		})
	}
}

func TestSSHKeyWizardRepairsSelectedIdentityBeforeRegistration(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	keyPath := writeSSHCLIKey(t, f, "work-key")
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &sshKeyInventoryRunner{base: &sshMachineRunner{base: f.runner, status: machineStatus(false)}}
	registration := false
	out, stderr, err := runSSHKeyWizard(t, f, runner, "lab\n\ntester\n22\ny\nn\n", func(request picker.Request) (picker.Result, error) {
		if !strings.Contains(request.Items[0].Description, "permission repair needed") {
			t.Fatalf("bad permissions were mistaken for a public-only key: %+v", request)
		}
		return picker.Result{Item: request.Items[0]}, nil
	}, func() {
		registration = true
		info, err := os.Stat(keyPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("registration prompted before key repair: %v", err)
		}
	})
	if !errors.Is(err, errPromptCanceled) || !registration || !strings.Contains(out, "0644 → 0600") {
		t.Fatalf("repair order registration=%v error=%v\n%s\n%s", registration, err, out, stderr)
	}
	info, err := os.Stat(keyPath + ".pub")
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("valid public-key mode changed: %v", err)
	}
	if _, err := os.Stat(f.managedPath("lab")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("declined setup still configured alias: %v", err)
	}
	for _, call := range runner.calls {
		if call.Name == "ssh" || call.Name == "ssh-keygen" {
			t.Fatalf("preview executed %s", call.Display)
		}
	}
}

func TestSSHUnsafePermissionsDryRunDoesNotRepair(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	sshDir := filepath.Join(f.home, ".ssh")
	if err := os.Chmod(sshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &sshMachineRunner{base: f.runner, status: machineStatus(false)}
	_, err := runMachineCLI(t, f, runner, "ssh", "setup", "lab", "--from", "tailscale:lab", "--user", "tester", "--config-only", "--dry-run", "--json")
	if err == nil {
		t.Fatal("unsafe permissions unexpectedly passed setup planning")
	}
	info, err := os.Stat(sshDir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("dry-run repaired permissions: %v", err)
	}
}
