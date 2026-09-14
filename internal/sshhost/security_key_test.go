package sshhost

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type securityKeyFixture struct {
	service  *Service
	tools    map[string]string
	line     []byte
	requests []RunRequest
	generate func(RunRequest) (RunResult, error)
	derive   func(RunRequest) (RunResult, error)
	proof    func(RunRequest) (RunResult, error)
}

func newSecurityKeyFixture(t *testing.T) *securityKeyFixture {
	t.Helper()
	requireUnixSSHProviderFixture(t)
	paths := fixturePaths(t)
	f := &securityKeyFixture{tools: map[string]string{}, line: testSecurityKeyLine(0xc1, "hardware test")}
	for _, name := range []string{"ssh", "ssh-keygen"} {
		f.tools[name] = filepath.Join(paths.Home, "inert-tools", name)
		writeFixture(t, f.tools[name], "inert test executable")
		resolved, err := filepath.EvalSymlinks(f.tools[name])
		if err != nil {
			t.Fatal(err)
		}
		f.tools[name] = resolved
	}
	s, err := NewService(paths, keyCatalogRunnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		f.requests = append(f.requests, request)
		if request.Name == f.tools["ssh"] {
			if f.proof != nil {
				return f.proof(request)
			}
			t.Fatalf("unexpected native client request: %+v", request)
		}
		if request.Name != f.tools["ssh-keygen"] {
			t.Fatalf("unexpected tool: %s", request.Name)
		}
		switch request.Args[0] {
		case "-q":
			if request.OnStarted != nil {
				request.OnStarted(time.Now())
			}
			if f.generate != nil {
				return f.generate(request)
			}
			base := sshArgForCatalogTest(request.Args, "-f")
			writeFixture(t, base, "opaque FIDO handle; not an exportable private key")
			writeFixture(t, base+".pub", string(f.line)+"\n")
			return RunResult{}, nil
		case "-y":
			if f.derive != nil {
				return f.derive(request)
			}
			return RunResult{Stdout: append(append([]byte(nil), f.line...), '\n')}, nil
		default:
			t.Fatalf("unexpected probe or enrollment command: %+v", request)
			return RunResult{}, nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	f.service = s
	s.securityKeyLookPath = func(name string) (string, error) { return f.tools[name], nil }
	s.securityKeyGOOS = "linux"
	s.securityKeyToolCheck = func(_ string, info fs.FileInfo) error {
		if !info.Mode().IsRegular() {
			return ErrUnsafePath
		}
		return nil
	}
	return f
}

func (f *securityKeyFixture) plan(t *testing.T, keyType KeyType, options SecurityKeyOptions) KeyPlan {
	t.Helper()
	plan, err := f.service.PlanKey(t.Context(), KeyRequest{Operation: KeyGenerate, Type: keyType, SecurityKey: options,
		DestinationIdentity: filepath.Join(f.service.paths.SSHDir, "hardware-key"), Interactive: true, NoPassphrase: true, Comment: "test token"})
	if err != nil || !plan.Ready() {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	return plan
}

func ecdsaSecurityKeyLine() []byte {
	algorithm := "sk-ecdsa-sha2-nistp256@openssh.com"
	x, y := elliptic.P256().ScalarBaseMult([]byte{1})
	blob := sshWireString([]byte(algorithm))
	blob = append(blob, sshWireString([]byte("nistp256"))...)
	blob = append(blob, sshWireString(elliptic.Marshal(elliptic.P256(), x, y))...)
	blob = append(blob, sshWireString([]byte("ssh:dev"))...)
	return []byte(algorithm + " " + base64.StdEncoding.EncodeToString(blob) + " hardware test")
}

func TestSecurityKeyCapabilitiesArePassiveAndUncertain(t *testing.T) {
	f := newSecurityKeyFixture(t)
	observation, err := f.service.ObserveSecurityKeyCapability(t.Context(), "internal")
	if err != nil || !observation.CanAttempt || observation.Status != "unknown" || len(f.requests) != 0 {
		t.Fatalf("capability=%+v err=%v", observation, err)
	}
	if observation.KeygenPath != f.tools["ssh-keygen"] || observation.SSHClientPath != f.tools["ssh"] {
		t.Fatal("exact tool paths omitted")
	}
	observation, err = f.service.ObserveSecurityKeyCapability(t.Context(), "apple-secure-enclave")
	if err != nil || observation.CanAttempt || !hasDiagnostic(observation.Diagnostics, "secure_enclave_generation_unverified") || len(f.requests) != 0 {
		t.Fatalf("Secure Enclave was not blocked passively: %+v %v", observation, err)
	}
	f.service.securityKeyLookPath = func(string) (string, error) { return "", fs.ErrNotExist }
	observation, err = f.service.ObserveSecurityKeyCapability(t.Context(), "internal")
	if err != nil || observation.CanAttempt || !hasDiagnostic(observation.Diagnostics, "security_key_tool_unavailable") {
		t.Fatalf("missing tools accepted: %+v %v", observation, err)
	}
}

func TestKnownInternalFIDOCapabilityClassificationIsPortable(t *testing.T) {
	for _, test := range []struct {
		goos, provider, keygen, client string
		blocked                        bool
	}{
		{"darwin", "internal", "/usr/bin/ssh-keygen", "/custom/ssh", true},
		{"darwin", "internal", "/custom/ssh-keygen", "/usr/bin/ssh", true},
		{"darwin", "internal", "/custom/ssh-keygen", "/custom/ssh", false},
		{"darwin", "/provider.dylib", "/usr/bin/ssh-keygen", "/usr/bin/ssh", false},
		{"linux", "internal", "/usr/bin/ssh-keygen", "/usr/bin/ssh", false},
	} {
		if got := knownInternalFIDOUnsupported(test.goos, test.provider, test.keygen, test.client); got != test.blocked {
			t.Fatalf("classification %+v=%v", test, got)
		}
	}
}

func TestSecurityKeyGenerationPinsNativeArgumentsAndValidatesStub(t *testing.T) {
	for _, keyType := range []KeyType{KeyTypeEd25519SK, KeyTypeECDSASK} {
		t.Run(string(keyType), func(t *testing.T) {
			f := newSecurityKeyFixture(t)
			if keyType == KeyTypeECDSASK {
				f.line = ecdsaSecurityKeyLine()
			}
			t.Setenv("SSH_SK_PROVIDER", "/must-not-inherit")
			plan := f.plan(t, keyType, SecurityKeyOptions{Resident: true, VerifyRequired: true, Application: "ssh:dev-test"})
			if len(f.requests) != 0 {
				t.Fatal("plan invoked native tools")
			}
			result, err := f.service.ApplyKey(t.Context(), plan)
			if err != nil || !result.Created || !result.Retained || result.Hardware == nil || result.Hardware.Status != "created" || !result.Hardware.Resident {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Candidate.Provenance.Private || !result.Candidate.Provenance.SecurityKeyStub || result.Candidate.Algorithm != keyType.algorithm() {
				t.Fatalf("wrong provenance: %+v", result.Candidate)
			}
			if len(f.requests) != 2 {
				t.Fatalf("unexpected native operations: %+v", f.requests)
			}
			generation := f.requests[0]
			base := sshArgForCatalogTest(generation.Args, "-f")
			want := []string{"-q", "-t", string(keyType), "-f", base, "-w", "internal", "-O", "application=ssh:dev-test", "-O", "resident", "-O", "verify-required", "-C", "test token", "-N", ""}
			if !reflect.DeepEqual(generation.Args, want) || !generation.Interactive || !reflect.DeepEqual(generation.UnsetEnv, []string{"SSH_SK_PROVIDER"}) {
				t.Fatalf("native args=%+v", generation)
			}
			if !f.requests[1].Interactive || !f.requests[1].CaptureStdout {
				t.Fatal("stub derivation lost native interaction")
			}
			if result.Hardware.RecoveryIdentityPath != "" {
				t.Fatal("successful publication advertised recovery")
			}
			usePlan, err := f.service.PlanKey(t.Context(), KeyRequest{Candidate: result.Candidate, Interactive: true})
			if err != nil {
				t.Fatal(err)
			}
			used, err := f.service.ApplyKey(t.Context(), usePlan)
			if err != nil || used.Candidate.state.hardware != result.Candidate.state.hardware {
				t.Fatalf("rebind lost hardware authority: %v", err)
			}
			writeFixture(t, f.tools["ssh"], "different client")
			if err := f.service.RevalidateKeySelection(t.Context(), usePlan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("rebound key accepted a changed client: %v", err)
			}
		})
	}
}

func TestSecurityKeyPlansRejectInvalidOptionsAndMutation(t *testing.T) {
	f := newSecurityKeyFixture(t)
	for _, request := range []KeyRequest{
		{Operation: KeyGenerate, Type: KeyTypeEd25519SK, NoPassphrase: true},
		{Operation: KeyGenerate, Type: KeyTypeEd25519SK, Interactive: true, SecurityKey: SecurityKeyOptions{Application: "not-ssh"}},
		{Operation: KeyGenerate, Type: KeyTypeEd25519, Interactive: true, SecurityKey: SecurityKeyOptions{Resident: true}},
	} {
		plan, err := f.service.PlanKey(t.Context(), request)
		if err == nil && plan.Ready() {
			t.Fatalf("invalid request ready: %+v", request)
		}
	}
	for _, mutate := range []func(*KeyPlan){
		func(plan *KeyPlan) { plan.SecurityKey.Resident = true },
		func(plan *KeyPlan) { plan.SecurityKey.Provider = "/other" },
		func(plan *KeyPlan) { plan.KeygenPath = "/other" },
		func(plan *KeyPlan) { plan.SSHClientPath = "/other" },
		func(plan *KeyPlan) { plan.KeyType = KeyTypeEd25519 },
	} {
		plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{})
		mutate(&plan)
		if _, err := f.service.ApplyKey(t.Context(), plan); err == nil {
			t.Fatal("mutated hardware plan applied")
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("invalid plans touched native tools")
	}
}

func TestSecurityKeyToolAndProviderSubstitutionBlocksEnrollment(t *testing.T) {
	for _, selected := range []string{"ssh", "ssh-keygen", "provider"} {
		t.Run(selected, func(t *testing.T) {
			f := newSecurityKeyFixture(t)
			provider := filepath.Join(f.service.paths.Home, "provider.so")
			writeFixture(t, provider, "inert provider")
			plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{Provider: provider})
			path := provider
			if selected != "provider" {
				path = f.tools[selected]
			}
			writeFixture(t, path, "changed tool or provider")
			if _, err := f.service.ApplyKey(t.Context(), plan); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("substitution not rejected: %v", err)
			}
			if len(f.requests) != 0 {
				t.Fatal("changed toolchain was executed")
			}
		})
	}
}

func TestSecurityKeyRecoveryRetainsOnlyValidatedPairs(t *testing.T) {
	for _, phase := range []string{"ordinary-output", "mismatch", "canceled-before-validation", "canceled-after-validation", "collision"} {
		t.Run(phase, func(t *testing.T) {
			f := newSecurityKeyFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "ordinary-output" {
				f.line = testPublicLine(0xc2, "not hardware")
			}
			if phase == "mismatch" {
				f.derive = func(RunRequest) (RunResult, error) { return RunResult{Stdout: testPublicLine(0xc3, "ordinary")}, nil }
			}
			if phase == "canceled-before-validation" {
				f.derive = func(RunRequest) (RunResult, error) { cancel(); return RunResult{Stdout: f.line}, nil }
			}
			plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{Resident: true})
			if phase == "canceled-after-validation" {
				f.service.beforeKeyCommit = cancel
			}
			if phase == "collision" {
				f.service.beforeKeyCommit = func() { writeFixture(t, plan.IdentityFile, "existing identity must survive") }
			}
			result, err := f.service.ApplyKey(ctx, plan)
			if err == nil || result.Hardware == nil {
				t.Fatalf("missing failed enrollment receipt: %+v %v", result, err)
			}
			wantRecovery := phase == "canceled-after-validation" || phase == "collision"
			if (result.Hardware.RecoveryIdentityPath != "") != wantRecovery || (result.Hardware.RecoveryPublicPath != "") != wantRecovery {
				t.Fatalf("unsafe/incomplete recovery: %+v", result)
			}
			if result.Candidate.state != nil {
				t.Fatal("failed generation returned an actionable key")
			}
			if wantRecovery {
				if result.Hardware.Status != "created" || !result.Retained {
					t.Fatalf("validated pair status=%+v", result)
				}
				assertFixturePrivateFile(t, result.Hardware.RecoveryIdentityPath)
				assertFixturePrivateFile(t, result.Hardware.RecoveryPublicPath)
			} else {
				if result.Hardware.Status != "unknown" {
					t.Fatalf("unvalidated hardware outcome became known: %+v", result)
				}
				left, _ := filepath.Glob(filepath.Join(f.service.paths.SSHDir, ".dev-sk-recovery-*"))
				if len(left) != 0 {
					t.Fatalf("unvalidated native output retained: %v", left)
				}
			}
			before := len(f.requests)
			result.Hardware.Status = "caller mutation"
			again, err := f.service.ApplyKey(t.Context(), plan)
			if !errors.Is(err, ErrBlocked) || len(f.requests) != before || again.Hardware.Status == "caller mutation" {
				t.Fatalf("plan reenrolled or shared receipt: %+v %v", again, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "opaque FIDO handle") || strings.Contains(string(encoded), "existing identity must survive") {
				t.Fatal("receipt leaked identity contents")
			}
		})
	}
}

func TestSecurityKeyConcurrentApplyEnrollsOnlyOnce(t *testing.T) {
	f := newSecurityKeyFixture(t)
	plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{})
	var group sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() { defer group.Done(); _, err := f.service.ApplyKey(t.Context(), plan); errorsSeen <- err }()
	}
	group.Wait()
	close(errorsSeen)
	ready, blocked := 0, 0
	for err := range errorsSeen {
		if err == nil {
			ready++
		} else if errors.Is(err, ErrBlocked) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if ready != 1 || blocked != 1 || len(f.requests) != 2 {
		t.Fatalf("duplicate enrollment: ready=%d blocked=%d requests=%d", ready, blocked, len(f.requests))
	}
}

func TestSecurityKeyExactProofRequiresExplicitNativeInteraction(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "batch", true: "native"}[interactive], func(t *testing.T) {
			f := newSecurityKeyFixture(t)
			plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{VerifyRequired: true})
			key, err := f.service.ApplyKey(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			f.proof = func(request RunRequest) (RunResult, error) {
				if request.Interactive != interactive {
					t.Fatalf("proof interaction=%v want=%v", request.Interactive, interactive)
				}
				data, err := os.ReadFile(sshArgForCatalogTest(request.Args, "-F"))
				if err != nil {
					t.Fatal(err)
				}
				batch := "yes"
				if interactive {
					batch = "no"
				}
				for key, value := range map[string]string{"BatchMode": batch, "PasswordAuthentication": "no", "KbdInteractiveAuthentication": "no", "PreferredAuthentications": "publickey", "IdentitiesOnly": "yes", "SecurityKeyProvider": "internal"} {
					if !proofDirectiveEquals(data, key, value) {
						t.Fatalf("proof weakened %s: %s", key, data)
					}
				}
				if err := os.WriteFile(sshArgForCatalogTest(request.Args, "-E"), []byte("Authenticated to target using \"publickey\".\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return RunResult{}, nil
			}
			selector, cleanup, err := f.service.prepareKeySelector(key.Candidate.state)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			route := bindTestRoute(t, f.service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
			verified, err := f.service.runSSHProof(t.Context(), route.state.hops[0], selector, true, interactive)
			if err != nil || !verified {
				t.Fatalf("proof=%v err=%v", verified, err)
			}
		})
	}
}
