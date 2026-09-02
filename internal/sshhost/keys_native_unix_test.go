//go:build unix

package sshhost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeSSHKeygenGeneratesAndValidatesStagedPair(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skipf("ssh-keygen unavailable: %v", err)
	}
	paths := fixturePaths(t)
	service, err := NewService(paths, ExecRunner{})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(paths.SSHDir, "id_native")
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: destination,
		Comment: "dev-cli-native-test", NoPassphrase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyKey(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Retained || result.Candidate.Algorithm != "ssh-ed25519" {
		t.Fatalf("result = %#v", result)
	}
	for _, path := range []string{destination, destination + ".pub"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %04o", path, info.Mode().Perm())
		}
	}
}

func TestNativeSSHKeygenVerifiesExistingCompanionAndRejectsMismatchOrEncryption(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skipf("ssh-keygen unavailable: %v", err)
	}
	generate := func(t *testing.T, path, passphrase string) {
		t.Helper()
		command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-f", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generate native fixture: %v: %s", err, output)
		}
	}

	t.Run("matching", func(t *testing.T) {
		paths := fixturePaths(t)
		identity := filepath.Join(paths.SSHDir, "id_existing_native")
		generate(t, identity, "")
		service, err := NewService(paths, ExecRunner{})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.ApplyKey(context.Background(), plan)
		if err != nil || result.Candidate.Fingerprint == "" {
			t.Fatalf("result = %#v, err %v", result, err)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		paths := fixturePaths(t)
		identity := filepath.Join(paths.SSHDir, "id_existing_native")
		other := filepath.Join(paths.SSHDir, "id_other_native")
		generate(t, identity, "")
		generate(t, other, "")
		otherPublic, err := os.ReadFile(other + ".pub")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(identity+".pub", otherPublic, 0o600); err != nil {
			t.Fatal(err)
		}
		service, err := NewService(paths, ExecRunner{})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyKey(context.Background(), plan); err == nil {
			t.Fatal("mismatched native key pair was accepted")
		}
	})

	t.Run("encrypted noninteractive", func(t *testing.T) {
		paths := fixturePaths(t)
		identity := filepath.Join(paths.SSHDir, "id_encrypted_native")
		generate(t, identity, "native-test-passphrase")
		service, err := NewService(paths, ExecRunner{})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyKey(context.Background(), plan); !errors.Is(err, ErrInteractionRequired) {
			t.Fatalf("encrypted native key error = %v, want interaction required", err)
		}
	})
}
