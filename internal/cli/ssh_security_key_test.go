package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func TestSSHHardwareFlagsRejectIncompatibleModesBeforeOperations(t *testing.T) {
	for _, flags := range [][]string{
		{"--key-type", "ed25519"},
		{"--sk-resident=false"},
		{"--sk-provider", "internal", "--to", "fleet"},
		{"--generate-key", "--key-type", "ed25519", "--sk-verify-required=false"},
		{"--generate-key", "--key-type", "rsa-sk"},
		{"--generate-key", "--key-type", "ed25519-sk", "--auth", "existing"},
		{"--generate-key", "--key-type", "ed25519-sk", "--identity-agent", "bitwarden"},
		{"--generate-key", "--key-type", "ed25519-sk", "--identity-file", "/unused.pub"},
		{"--generate-key", "--key-type", "ecdsa-sk", "--from", "fleet:gateway/box"},
	} {
		t.Run(strings.Join(flags, "_"), func(t *testing.T) {
			f := newSSHCLIFixture(t)
			args := append([]string{"ssh", "setup", "lab", "--hostname", "lab.example", "--target-os", "posix", "--yes", "--json"}, flags...)
			if _, _, err := f.run(args...); err == nil || f.runner.callCount() != 0 {
				t.Fatalf("incompatible hardware flags reached an operation: %v %+v", err, f.runner.callSnapshot())
			}
		})
	}
	f := newSSHCLIFixture(t)
	if _, _, err := f.run("ssh", "setup", "--generate-key", "--key-type", "ed25519-sk"); err == nil || f.runner.callCount() != 0 {
		t.Fatal("no-argument wizard silently ignored an explicitly requested type")
	}
}

func TestSSHDefaultGenerationRemainsOrdinaryEd25519(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	path := filepath.Join(f.home, ".ssh", "regular")
	out, _, err := f.run("ssh", "setup", "lab", "--hostname", "lab.example", "--generate-key", "--key-path", path, "--no-passphrase", "--target-os", "posix", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := assertOneSSHJSON(t, out)["key_plan"].(map[string]any)
	if plan["algorithm"] != "ssh-ed25519" || plan["identity_file"] != path || plan["security_key_provider"] != nil || plan["keygen_path"] != nil || plan["ssh_client_path"] != nil {
		t.Fatalf("ordinary generation acquired hardware policy: %s", out)
	}
	if f.runner.callCount() != 0 {
		t.Fatalf("ordinary static plan ran a command: %+v", f.runner.callSnapshot())
	}
}

func TestSSHHardwarePreviewNamesExactToolsAndRetainedEffects(t *testing.T) {
	plan := sshhost.KeyPlan{Operation: sshhost.KeyGenerate, KeyType: sshhost.KeyTypeEd25519SK, IdentityFile: "/home/u/.ssh/work/id_sk", PublicPath: "/home/u/.ssh/work/id_sk.pub", CreateParent: "/home/u/.ssh/work", KeygenPath: "/tools/ssh-keygen", SSHClientPath: "/tools/ssh", SecurityKeyProvider: "/providers/security key.dylib", SecurityKey: sshhost.SecurityKeyOptions{Provider: "/providers/security key.dylib", Resident: true, VerifyRequired: true, Application: "ssh:lab"}}
	var output bytes.Buffer
	app := &App{Out: &output, Err: &output}
	renderSSHSetupPlan(app, sshSetupDocument{Alias: "lab", AliasClass: "managed", KeyPlan: &plan})
	for _, expected := range []string{"ed25519-sk", plan.IdentityFile, plan.PublicPath, plan.KeygenPath, plan.SSHClientPath, plan.SecurityKeyProvider, "resident", "true", "ssh:lab", "PIN/touch", "not an exportable", "mode 0700", "no downgrade", "never claims hardware rollback"} {
		if !strings.Contains(strings.ToLower(output.String()), strings.ToLower(expected)) {
			t.Fatalf("review omits %q: %s", expected, output.String())
		}
	}
}

func TestSSHPartialKeyPublicationObservationsAreNotRecoveryAuthority(t *testing.T) {
	key := sshhost.KeyResult{Operation: sshhost.KeyGenerate, Hardware: &sshhost.HardwareKeyEffect{Kind: "fido", Status: "created"}, LocalFiles: []sshhost.KeyLocalFileObservation{
		{Kind: "identity", Status: "retained", Path: "/home/u/.ssh/id_sk"},
		{Kind: "staging_public", Status: "unknown", Path: "/home/u/.ssh/.stage-sk.pub"},
	}}
	var output bytes.Buffer
	app := &App{Out: &output, Err: &output}
	failed := errors.New("public staging file changed after identity publication")
	_ = finishSSHSetup(app, false, sshSetupDocument{Alias: "lab", Status: "partial", KeyResult: &key}, failed)
	if err := renderSSHOnboardResult(app, sshflow.OnboardExecutionResult{OnboardResult: sshflow.OnboardResult{Status: "partial", Outcomes: []sshflow.OnboardOutcome{{Alias: "lab", Stage: "configure", Status: "failed"}}}, Keys: map[string]sshhost.KeyResult{"lab": key}}, false); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"identity retained /home/u/.ssh/id_sk", "staging_public unknown /home/u/.ssh/.stage-sk.pub", "not a validated recovery pair", "observations do not authorize key use or recovery"} {
		if strings.Count(output.String(), expected) < 2 {
			t.Fatalf("partial filesystem observation omitted from a result: %q in %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Retained key handle:") || strings.Contains(output.String(), "Retained public key:") {
		t.Fatalf("unverified local files were rendered as usable recovery: %s", output.String())
	}
	key.Hardware = nil
	if notes := strings.Join(sshHardwareResultNotes(key), "\n"); !strings.Contains(notes, "Local file observation:") {
		t.Fatal("local filesystem facts depend on an unrelated hardware receipt")
	}
}

func TestSSHHardwareUnknownReceiptsRemainInBothCLIResults(t *testing.T) {
	key := sshhost.KeyResult{Operation: sshhost.KeyGenerate, Hardware: &sshhost.HardwareKeyEffect{Kind: "fido", Status: "unknown", Resident: true, RecoveryIdentityPath: "/home/u/.ssh/.recovery-sk", RecoveryPublicPath: "/home/u/.ssh/.recovery-sk.pub"}}
	var output bytes.Buffer
	app := &App{Out: &output, Err: &output}
	failed := errors.New("native enrollment interrupted")
	if err := finishSSHSetup(app, false, sshSetupDocument{Alias: "lab", Status: "partial", KeyResult: &key}, failed); !errors.Is(err, failed) {
		t.Fatal("setup lost the original failure")
	}
	if err := renderSSHOnboardResult(app, sshflow.OnboardExecutionResult{OnboardResult: sshflow.OnboardResult{Status: "partial", Outcomes: []sshflow.OnboardOutcome{{Alias: "lab", Stage: "configure", Status: "unknown"}}}, Keys: map[string]sshhost.KeyResult{"lab": key}}, false); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"unknown", "no hardware rollback", key.Hardware.RecoveryIdentityPath, key.Hardware.RecoveryPublicPath, "do not retry automatically"} {
		if strings.Count(output.String(), expected) < 2 {
			t.Fatalf("receipt omitted from a result renderer: %q in %s", expected, output.String())
		}
	}
}
