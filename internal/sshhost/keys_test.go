package sshhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sshWireString(value []byte) []byte {
	encoded := make([]byte, 4+len(value))
	binary.BigEndian.PutUint32(encoded, uint32(len(value)))
	copy(encoded[4:], value)
	return encoded
}

func testPublicLine(seed byte, comment string) []byte {
	algorithm := "ssh-ed25519"
	key := bytes.Repeat([]byte{seed}, 32)
	blob := append(sshWireString([]byte(algorithm)), sshWireString(key)...)
	line := algorithm + " " + base64.StdEncoding.EncodeToString(blob)
	if comment != "" {
		line += " " + comment
	}
	return []byte(line)
}

func testSecurityKeyLine(seed byte, comment string) []byte {
	algorithm := "sk-ssh-ed25519@openssh.com"
	blob := append(sshWireString([]byte(algorithm)), sshWireString(bytes.Repeat([]byte{seed}, 32))...)
	blob = append(blob, sshWireString([]byte("ssh:dev-cli-test"))...)
	line := algorithm + " " + base64.StdEncoding.EncodeToString(blob)
	if comment != "" {
		line += " " + comment
	}
	return []byte(line)
}

func TestParsePublicKeyValidatesWireAndRedactsMaterial(t *testing.T) {
	line := testPublicLine(0x42, "safe comment")
	metadata, err := ParsePublicKey(append(append([]byte(nil), line...), '\n'))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Algorithm != "ssh-ed25519" || metadata.Comment != "safe comment" || !strings.HasPrefix(metadata.Fingerprint, "SHA256:") {
		t.Fatalf("metadata = %#v", metadata)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	blobToken := strings.Fields(string(line))[1]
	if bytes.Contains(encoded, line) || bytes.Contains(encoded, []byte(blobToken)) {
		t.Fatalf("metadata JSON leaked public material: %s", encoded)
	}

	mismatched := append(sshWireString([]byte("ssh-rsa")), sshWireString(bytes.Repeat([]byte{1}, 32))...)
	cases := [][]byte{
		[]byte("ssh-ed25519 !!!"),
		[]byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString(mismatched)),
		append(append([]byte(nil), line...), []byte("\nsecond")...),
		bytes.Repeat([]byte{'x'}, MaxPublicKeyLineBytes+1),
	}
	for _, input := range cases {
		if _, err := ParsePublicKey(input); err == nil {
			t.Errorf("ParsePublicKey accepted invalid input of %d bytes", len(input))
		}
	}
}

func TestCatalogKeysDeduplicatesEffectiveFileAndAgentWithoutMaterialLeak(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "id_ed25519")
	privateMarker := "PRIVATE-KEY-MARKER-MUST-NOT-LEAK"
	writeFixture(t, identity, privateMarker)
	line := testPublicLine(0x11, "catalog-key")
	writeFixture(t, identity+".pub", string(line)+"\n")

	runner := &recordingRunner{result: RunResult{Stdout: append(append([]byte(nil), line...), '\n')}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveConfig{
		Alias: "lab", IdentityFiles: []string{"~/.ssh/id_ed25519"},
		Values: map[string][]string{"identityagent": {"SSH_AUTH_SOCK"}},
	}
	catalog, err := service.Catalog(context.Background(), KeyCatalogRequest{Effective: &effective})
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Complete || len(catalog.Candidates) != 1 {
		t.Fatalf("catalog = %#v", catalog)
	}
	candidate := catalog.Candidates[0]
	if candidate.Source != KeySourceEffectiveIdentity || !candidate.Provenance.Effective || !candidate.Provenance.Private || !candidate.Provenance.Agent {
		t.Fatalf("candidate provenance = %#v", candidate)
	}
	wantSources := []KeySource{KeySourceEffectiveIdentity, KeySourcePublicFile, KeySourceAgent}
	if !reflect.DeepEqual(candidate.Sources, wantSources) {
		t.Fatalf("sources = %v, want %v", candidate.Sources, wantSources)
	}
	if candidate.PublicPath != identity+".pub" || candidate.IdentityFile != identity {
		t.Fatalf("candidate paths = %#v", candidate)
	}
	if len(runner.requests) != 1 || runner.requests[0].Name != "ssh-add" || !reflect.DeepEqual(runner.requests[0].Args, []string{"-L"}) {
		t.Fatalf("catalog requests = %#v", runner.requests)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	blobToken := strings.Fields(string(line))[1]
	for _, forbidden := range []string{string(line), blobToken, privateMarker} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("catalog JSON leaked %q: %s", forbidden, encoded)
		}
	}
	for _, request := range runner.requests {
		if request.Name == "bw" {
			t.Fatal("catalog invoked bw")
		}
	}
}

func TestCatalogTreatsTruncatedAgentOutputAsIncomplete(t *testing.T) {
	paths := fixturePaths(t)
	runner := &recordingRunner{result: RunResult{
		Stdout: append(testPublicLine(0x18, "partial-agent"), []byte(outputTruncationMarker)...), StdoutTruncated: true,
	}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Catalog(context.Background(), KeyCatalogRequest{
		Effective: &EffectiveConfig{Alias: "lab", Values: map[string][]string{"identityagent": {"SSH_AUTH_SOCK"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Complete || len(catalog.Candidates) != 0 || !hasDiagnostic(catalog.Diagnostics, "agent_output_limit_exceeded") {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestCatalogDistinguishesSecurityKeyStubProvenance(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "id_ed25519_sk")
	writeFixture(t, identity, "opaque security-key stub")
	writeFixture(t, identity+".pub", string(testSecurityKeyLine(0x19, "hardware"))+"\n")
	service, err := NewService(paths, panicRunner{})
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveConfig{
		Alias: "lab", IdentityFiles: []string{identity}, Values: map[string][]string{"identityagent": {"none"}},
	}
	catalog, err := service.Catalog(context.Background(), KeyCatalogRequest{Effective: &effective})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatalf("catalog = %#v, err %v", catalog, err)
	}
	provenance := catalog.Candidates[0].Provenance
	if !provenance.SecurityKeyStub || provenance.Private {
		t.Fatalf("security-key provenance = %#v", provenance)
	}
}

func TestCatalogKeysHonorsIdentityAgentNoneAndBoundsInvalidFiles(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, filepath.Join(paths.SSHDir, "bad.pub"), "not a public key\n")
	service, err := NewService(paths, panicRunner{})
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveConfig{Alias: "lab", Values: map[string][]string{"identityagent": {"none"}}}
	catalog, err := service.Catalog(context.Background(), KeyCatalogRequest{Effective: &effective})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Complete || len(catalog.Candidates) != 0 || !hasDiagnostic(catalog.Diagnostics, "public_key_unreadable") {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestUseExistingPrivateCompanionRequiresMatchingNativeDerivation(t *testing.T) {
	for _, test := range []struct {
		name        string
		public      []byte
		derived     []byte
		interactive bool
		exitCode    int
		wantErr     error
		wantText    string
	}{
		{name: "matching private key", public: testPublicLine(0x20, "companion"), derived: testPublicLine(0x20, "derived")},
		{name: "matching security-key stub", public: testSecurityKeyLine(0x21, "companion"), derived: testSecurityKeyLine(0x21, "derived")},
		{name: "mismatched companion", public: testPublicLine(0x22, "companion"), derived: testPublicLine(0x23, "different"), wantText: "do not match"},
		{name: "encrypted batch key", public: testPublicLine(0x24, "encrypted"), exitCode: 1, wantErr: ErrInteractionRequired},
		{name: "interactive encrypted key may prompt", public: testPublicLine(0x25, "encrypted"), derived: testPublicLine(0x25, "derived"), interactive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := fixturePaths(t)
			identity := filepath.Join(paths.SSHDir, "id_existing")
			writeFixture(t, identity, "opaque private or security-key material")
			writeFixture(t, identity+".pub", string(test.public)+"\n")
			runner := &recordingRunner{result: RunResult{ExitCode: test.exitCode}}
			if len(test.derived) > 0 {
				runner.result.Stdout = append(append([]byte(nil), test.derived...), '\n')
			}
			service, err := NewService(paths, runner)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity, Interactive: test.interactive})
			if err != nil || !plan.Ready() || plan.Operation != KeyUse {
				t.Fatalf("plan = %#v, err %v", plan, err)
			}
			result, applyErr := service.ApplyKey(context.Background(), plan)
			switch {
			case test.wantErr != nil && !errors.Is(applyErr, test.wantErr):
				t.Fatalf("ApplyKey error = %v, want %v", applyErr, test.wantErr)
			case test.wantText != "" && (applyErr == nil || !strings.Contains(applyErr.Error(), test.wantText)):
				t.Fatalf("ApplyKey error = %v, want text %q", applyErr, test.wantText)
			case test.wantErr == nil && test.wantText == "" && applyErr != nil:
				t.Fatal(applyErr)
			}
			if applyErr == nil && result.Candidate.Fingerprint == "" {
				t.Fatalf("result = %#v", result)
			}
			if len(runner.requests) != 1 {
				t.Fatalf("derivation requests = %#v", runner.requests)
			}
			request := runner.requests[0]
			if request.Name != "ssh-keygen" || !reflect.DeepEqual(request.Args, []string{"-y", "-f", identity}) ||
				request.Interactive != test.interactive || !request.CaptureStdout {
				t.Fatalf("derivation request = %#v", request)
			}
			if test.interactive {
				if len(request.Env) != 0 {
					t.Fatalf("interactive derivation environment = %#v", request.Env)
				}
			} else if !reflect.DeepEqual(request.Env, noninteractiveKeygenEnv()) {
				t.Fatalf("batch derivation environment = %#v", request.Env)
			}
		})
	}
}

func TestUsePublicOnlyCandidateDoesNotDerivePrivateMaterial(t *testing.T) {
	paths := fixturePaths(t)
	publicPath := filepath.Join(paths.SSHDir, "orphan.pub")
	writeFixture(t, publicPath, string(testPublicLine(0x26, "public-only"))+"\n")
	service, err := NewService(paths, panicRunner{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanKey(context.Background(), KeyRequest{Path: publicPath})
	if err != nil || !plan.Ready() || plan.Operation != KeyUse {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	result, err := service.ApplyKey(context.Background(), plan)
	if err != nil || result.Candidate.Provenance.Private || result.Candidate.Provenance.SecurityKeyStub {
		t.Fatalf("result = %#v, err %v", result, err)
	}
}

func TestPlanAndApplyMissingPublicCompanionDerivation(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "id_derive")
	writeFixture(t, identity, "opaque local identity")
	line := testPublicLine(0x22, "derived")
	runner := &recordingRunner{result: RunResult{Stdout: append(append([]byte(nil), line...), '\n')}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}

	blocked, err := service.PlanKey(context.Background(), KeyRequest{Path: identity})
	if err != nil || blocked.Action != ActionBlocked || !hasDiagnostic(blocked.Diagnostics, "derive_confirmation_required") {
		t.Fatalf("blocked plan = %#v, err %v", blocked, err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("planning invoked runner: %#v", runner.requests)
	}

	plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity, AllowDerive: true})
	if err != nil || !plan.Ready() || plan.Operation != KeyDerive || plan.Action != ActionCreate {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	result, err := service.ApplyKey(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Candidate.Fingerprint == "" || result.Candidate.PublicPath != identity+".pub" {
		t.Fatalf("result = %#v", result)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("requests = %#v", runner.requests)
	}
	request := runner.requests[0]
	if request.Name != "ssh-keygen" || !reflect.DeepEqual(request.Args, []string{"-y", "-f", identity}) || request.Interactive {
		t.Fatalf("derivation request = %#v", request)
	}
	if strings.Contains(strings.Join(request.Args, " "), "opaque local identity") || !reflect.DeepEqual(request.Env, noninteractiveKeygenEnv()) {
		t.Fatalf("derivation exposed identity material or allowed askpass: %#v", request)
	}
	published, err := os.ReadFile(identity + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if !publicLinesEqual(published, line) {
		t.Fatalf("published public key differs")
	}
}

func TestNoninteractiveEncryptedDerivationReturnsInteractionRequired(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "id_encrypted")
	writeFixture(t, identity, "encrypted opaque identity")
	runner := &recordingRunner{result: RunResult{ExitCode: 1, Stderr: []byte("passphrase prompt")}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity, AllowDerive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyKey(context.Background(), plan); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("ApplyKey error = %v, want interaction required", err)
	}
	if _, err := os.Lstat(identity + ".pub"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed derivation published companion: %v", err)
	}
	for _, argument := range runner.requests[0].Args {
		if strings.Contains(strings.ToLower(argument), "passphrase") {
			t.Fatalf("passphrase appeared in argv: %#v", runner.requests[0].Args)
		}
	}
}

type keygenRunner struct {
	t              *testing.T
	line           []byte
	derivedLine    []byte
	requests       []RunRequest
	failAfterWrite bool
}

func (r *keygenRunner) Run(_ context.Context, request RunRequest) (RunResult, error) {
	r.requests = append(r.requests, request)
	if request.Name != "ssh-keygen" {
		return RunResult{}, fmt.Errorf("unexpected command %s", request.Name)
	}
	index := -1
	for i := range request.Args {
		if request.Args[i] == "-f" && i+1 < len(request.Args) {
			index = i + 1
			break
		}
	}
	if index < 0 {
		return RunResult{}, errors.New("ssh-keygen request has no -f")
	}
	if len(request.Args) > 0 && request.Args[0] == "-y" {
		line := r.derivedLine
		if len(line) == 0 {
			line = r.line
		}
		return RunResult{Stdout: append(append([]byte(nil), line...), '\n')}, nil
	}
	if err := os.WriteFile(request.Args[index], []byte("GENERATED-IDENTITY-MATERIAL"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(request.Args[index]+".pub", append(append([]byte(nil), r.line...), '\n'), 0o600); err != nil {
		r.t.Fatal(err)
	}
	if r.failAfterWrite {
		return RunResult{ExitCode: 1}, nil
	}
	return RunResult{}, nil
}

func TestGeneratedKeyInteractiveAndNoninteractiveArgv(t *testing.T) {
	for _, test := range []struct {
		name         string
		interactive  bool
		noPassphrase bool
		wantEmptyN   bool
	}{
		{name: "interactive native prompt", interactive: true},
		{name: "noninteractive explicit empty passphrase", noPassphrase: true, wantEmptyN: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := fixturePaths(t)
			runner := &keygenRunner{t: t, line: testPublicLine(0x33, "generated")}
			service, err := NewService(paths, runner)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(paths.SSHDir, "id_dev")
			plan, err := service.PlanKey(context.Background(), KeyRequest{
				Operation: KeyGenerate, DestinationIdentity: destination, Comment: "generated",
				Interactive: test.interactive, NoPassphrase: test.noPassphrase,
			})
			if err != nil || !plan.Ready() {
				t.Fatalf("plan = %#v, err %v", plan, err)
			}
			result, err := service.ApplyKey(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Created || !result.Retained || !result.Candidate.Provenance.Private {
				t.Fatalf("result = %#v", result)
			}
			request := runner.requests[0]
			if request.Interactive != test.interactive {
				t.Fatalf("interactive = %v", request.Interactive)
			}
			nIndex := -1
			for index, arg := range request.Args {
				if arg == "-N" {
					nIndex = index
				}
			}
			if test.wantEmptyN {
				if nIndex < 0 || nIndex+1 >= len(request.Args) || request.Args[nIndex+1] != "" {
					t.Fatalf("noninteractive argv = %#v", request.Args)
				}
			} else if nIndex >= 0 {
				t.Fatalf("interactive argv supplied -N: %#v", request.Args)
			}
			for _, path := range []string{destination, destination + ".pub"} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("generated path %s mode = %v, err %v", path, info.Mode(), err)
				}
			}
		})
	}
}

func TestNoninteractiveGenerationRequiresExplicitNoPassphraseWithoutEffects(t *testing.T) {
	paths := fixturePaths(t)
	service, err := NewService(paths, panicRunner{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: filepath.Join(paths.SSHDir, "id_dev"),
	})
	if err != nil || plan.Action != ActionBlocked || !hasDiagnostic(plan.Diagnostics, "interaction_required") {
		t.Fatalf("plan = %#v, err %v", plan, err)
	}
	ready, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: filepath.Join(paths.SSHDir, "id_dev_ready"), NoPassphrase: true,
	})
	if err != nil || !ready.Ready() {
		t.Fatalf("side-effect-free ready plan = %#v, err %v", ready, err)
	}
}

func TestApplyKeyRejectsModifiedPublicPlanFields(t *testing.T) {
	paths := fixturePaths(t)
	service, err := NewService(paths, panicRunner{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: filepath.Join(paths.SSHDir, "id_original"), NoPassphrase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan.IdentityFile = filepath.Join(paths.SSHDir, "id_forged")
	if _, err := service.ApplyKey(context.Background(), plan); err == nil {
		t.Fatal("ApplyKey accepted a modified plan")
	}
}

func TestGeneratedPairNoReplaceRaceNeverOverwritesAndRollsBackFirstHalf(t *testing.T) {
	paths := fixturePaths(t)
	generatedLine := testPublicLine(0x44, "generated")
	concurrentLine := testPublicLine(0x55, "concurrent")
	runner := &keygenRunner{t: t, line: generatedLine}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(paths.SSHDir, "id_race")
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: destination, NoPassphrase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.beforeKeyCommit = func() {
		writeFixture(t, destination+".pub", string(concurrentLine)+"\n")
	}
	if _, err := service.ApplyKey(context.Background(), plan); !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("ApplyKey error = %v, want collision", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first published half was not rolled back: %v", err)
	}
	content, err := os.ReadFile(destination + ".pub")
	if err != nil || !publicLinesEqual(content, concurrentLine) {
		t.Fatalf("concurrent public key changed, err %v", err)
	}
}

func TestGeneratedPairMismatchIsRejectedBeforePublication(t *testing.T) {
	paths := fixturePaths(t)
	runner := &keygenRunner{
		t: t, line: testPublicLine(0x70, "public"), derivedLine: testPublicLine(0x71, "derived"),
	}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(paths.SSHDir, "id_mismatch")
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: destination, NoPassphrase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyKey(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("ApplyKey mismatch error = %v", err)
	}
	for _, path := range []string{destination, destination + ".pub"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("mismatched pair published %s: %v", path, err)
		}
	}
}

func TestCatalogUsesEffectiveCustomIdentityAgentSocket(t *testing.T) {
	paths := fixturePaths(t)
	line := testPublicLine(0x73, "custom-agent")
	runner := &recordingRunner{result: RunResult{Stdout: append(append([]byte(nil), line...), '\n')}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(paths.Home, "custom-agent.sock")
	effective := EffectiveConfig{Alias: "lab", Values: map[string][]string{"identityagent": {socket}}}
	catalog, err := service.Catalog(context.Background(), KeyCatalogRequest{Effective: &effective})
	if err != nil || !catalog.Complete || len(catalog.Candidates) != 1 {
		t.Fatalf("catalog = %#v, err %v", catalog, err)
	}
	if len(runner.requests) != 1 || !reflect.DeepEqual(runner.requests[0].Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + socket}) {
		t.Fatalf("ssh-add environment = %#v", runner.requests)
	}
}

func TestRunnerJSONCannotExposeExecutionBuffers(t *testing.T) {
	line := testPublicLine(0x72, "runner")
	requestJSON, err := json.Marshal(RunRequest{
		Name: "ssh", Args: []string{"host", string(line)}, Env: []string{"SECRET=value"}, Stdin: line,
		Display: "safe display",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := json.Marshal(RunResult{Stdout: line, Stderr: []byte("sensitive agent payload"), ExitCode: 7})
	if err != nil {
		t.Fatal(err)
	}
	combined := string(requestJSON) + string(resultJSON)
	for _, forbidden := range []string{string(line), "SECRET=value", "sensitive agent payload", "host"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("runner JSON leaked %q: %s", forbidden, combined)
		}
	}
	if !strings.Contains(string(requestJSON), "safe display") || !strings.Contains(string(resultJSON), "7") {
		t.Fatalf("runner JSON omitted safe fields: %s %s", requestJSON, resultJSON)
	}
}
