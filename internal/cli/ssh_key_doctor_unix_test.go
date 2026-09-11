//go:build darwin || linux

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doctorKeyMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func doctorPrivateKey(t *testing.T, f *sshCLIFixture, name string, paired bool) string {
	t.Helper()
	path := filepath.Join(f.home, ".ssh", name)
	if err := os.WriteFile(path, []byte("private-placeholder-not-parsed-by-doctor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if paired {
		if err := os.WriteFile(path+".pub", f.runner.publicLine, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func decodeKeyDoctor(t *testing.T, output string) sshKeyDoctorDocument {
	t.Helper()
	var doc sshKeyDoctorDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("doctor must emit one JSON object: %v\n%s", err, output)
	}
	return doc
}

func TestSSHKeyDoctorDiagnosesAndRepairsAllRecognizedKeys(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	custom := doctorPrivateKey(t, f, "azure_vm1", true)
	standard := doctorPrivateKey(t, f, "id_ed25519", false)
	unrelated := filepath.Join(f.home, ".ssh", "notes")
	if err := os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, err := f.run("ssh", "key", "doctor", "--json")
	if err != nil {
		t.Fatal(err)
	}
	plan := decodeKeyDoctor(t, out)
	if plan.Kind != "ssh_key_doctor_plan" || plan.Status != "repairable" || !plan.Complete || len(plan.Plan.Changes) != 2 || plan.Scope != "discovered" {
		t.Fatalf("unexpected diagnosis: %s", out)
	}
	for _, path := range []string{custom, standard} {
		if doctorKeyMode(t, path) != 0o700 {
			t.Fatal("read-only doctor changed permissions")
		}
	}
	out, _, err = f.run("ssh", "key", "doctor", "--fix", "--yes", "--json")
	if err != nil {
		t.Fatalf("fix: %v\n%s", err, out)
	}
	result := decodeKeyDoctor(t, out)
	if result.Kind != "ssh_key_doctor_result" || result.Status != "applied" || result.Result == nil || len(result.Result.Outcomes) != 2 || result.Recheck == nil || len(result.Recheck.Changes) != 0 {
		t.Fatalf("unexpected repair result: %s", out)
	}
	for _, path := range []string{custom, standard} {
		if doctorKeyMode(t, path) != 0o600 {
			t.Fatalf("key not repaired: %s", path)
		}
	}
	if doctorKeyMode(t, custom+".pub") != 0o644 || doctorKeyMode(t, unrelated) != 0o755 {
		t.Fatal("doctor changed public or unrelated directory permissions")
	}
	out, _, err = f.run("ssh", "key", "doctor", "--fix", "--json")
	if err != nil || decodeKeyDoctor(t, out).Status != "ready" || f.runner.callCount() != 0 {
		t.Fatalf("healthy rerun should be read-only and need no confirmation: %v\n%s", err, out)
	}
	if strings.Contains(out, "private-placeholder") || strings.Contains(out, strings.Fields(string(f.runner.publicLine))[1]) {
		t.Fatal("doctor exposed key contents")
	}
}

func TestSSHKeyDoctorExplicitScopeDoesNotRepairOtherKeys(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	selected := doctorPrivateKey(t, f, "custom-private-only", false)
	other := doctorPrivateKey(t, f, "other-key", true)
	out, _, err := f.run("ssh", "key", "doctor", "--key", selected, "--fix", "--yes", "--json")
	if err != nil {
		t.Fatalf("selected fix: %v\n%s", err, out)
	}
	result := decodeKeyDoctor(t, out)
	if result.Scope != "selected" || len(result.KeyPaths) != 1 || doctorKeyMode(t, selected) != 0o600 || doctorKeyMode(t, other) != 0o700 {
		t.Fatalf("explicit scope expanded: %s", out)
	}
}

func TestSSHKeyDoctorConfirmationAndUnsafeScope(t *testing.T) {
	for _, scenario := range []string{"no-yes", "decline", "unsafe-scan"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSSHCLIFixture(t)
			f.initSSH()
			key := doctorPrivateKey(t, f, "azure_vm1", true)
			var out string
			var err error
			switch scenario {
			case "decline":
				out, _, err = f.runInteractive("n\n", true, "ssh", "key", "doctor", "--fix")
				if !errors.Is(err, errPromptCanceled) || !strings.Contains(out, "0700 → 0600") {
					t.Fatalf("missing reviewed cancellation: %v\n%s", err, out)
				}
			case "no-yes":
				out, _, err = f.run("ssh", "key", "doctor", "--fix", "--json")
				if err == nil || decodeKeyDoctor(t, out).Status != "confirmation_required" {
					t.Fatalf("noninteractive fix bypassed confirmation: %v\n%s", err, out)
				}
			case "unsafe-scan":
				if err := os.Symlink(key+".pub", filepath.Join(f.home, ".ssh", "linked.pub")); err != nil {
					t.Fatal(err)
				}
				out, _, err = f.run("ssh", "key", "doctor", "--fix", "--yes", "--json")
				if err == nil || decodeKeyDoctor(t, out).Complete {
					t.Fatalf("unsafe default scope was treated as complete: %v\n%s", err, out)
				}
			}
			if doctorKeyMode(t, key) != 0o700 || f.runner.callCount() != 0 {
				t.Fatal("blocked/declined doctor changed permissions or invoked a tool")
			}
		})
	}
}

type doctorReviewOutput struct {
	bytes.Buffer
	beforeConfirm func()
}

func (w *doctorReviewOutput) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(sshKeyDoctorConfirmation)) && w.beforeConfirm != nil {
		w.beforeConfirm()
	}
	return w.Buffer.Write(data)
}

func TestSSHKeyDoctorRevalidatesReviewedKeyBeforeFix(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	key := doctorPrivateKey(t, f, "azure_vm1", true)
	out := &doctorReviewOutput{beforeConfirm: func() {
		if err := os.Rename(key, key+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(key, []byte("replacement"), 0o700); err != nil {
			t.Fatal(err)
		}
	}}
	var stderr bytes.Buffer
	app := &App{In: strings.NewReader("y\n"), Out: out, Err: &stderr, sshHostRunner: f.runner, interactiveCheck: func() bool { return true }}
	root := newRootCommand(app)
	root.SetContext(t.Context())
	root.SetArgs([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never", "ssh", "key", "doctor", "--fix"})
	if err := root.Execute(); err == nil || doctorKeyMode(t, key) != 0o700 || doctorKeyMode(t, key+".old") != 0o700 {
		t.Fatalf("stale reviewed key was repaired: %v\n%s\n%s", err, out.String(), stderr.String())
	}
}
