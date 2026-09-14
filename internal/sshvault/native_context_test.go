//go:build darwin || linux

package sshvault

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"golang.org/x/crypto/ssh"
)

const nativeTestSession = "native-test-session-not-a-vault-credential"

type nativeTestConfig struct {
	Version      string `json:"version"`
	Status       string `json:"status"`
	UserID       string `json:"user_id"`
	ServerURL    any    `json:"server_url"`
	Reply        string `json:"reply"`
	CreateLocked bool   `json:"create_locked"`
}

func TestMain(m *testing.M) {
	if os.Getenv("DEV_SSHVAULT_NATIVE_HELPER") == "1" {
		os.Exit(runNativeTestHelper())
	}
	os.Exit(m.Run())
}

// The harmless "bw"/"node" fixture is this test executable. It does not run any
// installed provider/runtime, read native vault storage, or persist a private key.
func runNativeTestHelper() int {
	if os.Getenv("BW_SESSION") != nativeTestSession || os.Getenv("BW_NOINTERACTION") != "true" {
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != os.Getenv("DEV_SSHVAULT_NATIVE_CWD") || os.Getenv("BITWARDENCLI_APPDATA_DIR") != os.Getenv("DEV_SSHVAULT_NATIVE_PROFILE") {
		return 2
	}
	args := os.Args[1:]
	if entry := os.Getenv("DEV_SSHVAULT_NATIVE_ENTRY"); entry != "" {
		if len(args) == 0 || args[0] != entry {
			return 2
		}
		args = args[1:]
	}
	if len(args) < 2 || args[0] != "--nointeraction" {
		return 2
	}
	args = args[1:]
	control, err := os.ReadFile(os.Getenv("DEV_SSHVAULT_NATIVE_CONTROL"))
	if err != nil {
		return 2
	}
	var config nativeTestConfig
	if json.Unmarshal(control, &config) != nil {
		return 2
	}
	log, err := os.OpenFile(os.Getenv("DEV_SSHVAULT_NATIVE_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	_, _ = io.WriteString(log, strings.Join(args, " ")+"\n")
	_ = log.Close()
	if len(args) == 1 && args[0] == "--version" {
		_, _ = io.WriteString(os.Stdout, config.Version+"\n")
		return 0
	}
	if len(args) == 1 && args[0] == "status" {
		return writeNativeTestJSON(map[string]any{"status": config.Status, "userId": config.UserID, "userEmail": "fixture@example.test", "serverUrl": config.ServerURL})
	}
	if len(args) != 2 || args[0] != "create" || args[1] != "item" {
		return 2
	}
	if config.CreateLocked {
		// The fixed flag was checked above. Do not consume any key JSON as
		// a password or prompt response if the provider became locked.
		return 2
	}
	encoded, err := io.ReadAll(io.LimitReader(os.Stdin, maxOutput+1))
	defer sshcredential.Wipe(encoded)
	if err != nil || len(encoded) > maxOutput {
		return 2
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(decoded, encoded)
	defer sshcredential.Wipe(decoded)
	if err != nil {
		return 2
	}
	var item struct {
		Type   int       `json:"type"`
		Name   string    `json:"name"`
		Fields []bwField `json:"fields"`
		SSHKey struct {
			PrivateKey  string `json:"privateKey"`
			PublicKey   string `json:"publicKey"`
			Fingerprint string `json:"keyFingerprint"`
		} `json:"sshKey"`
	}
	if json.Unmarshal(decoded[:n], &item) != nil || item.Type != 5 {
		return 2
	}
	private := []byte(item.SSHKey.PrivateKey)
	defer sshcredential.Wipe(private)
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil || ssh.FingerprintSHA256(signer.PublicKey()) != item.SSHKey.Fingerprint || strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) != item.SSHKey.PublicKey {
		return 2
	}
	if os.WriteFile(os.Getenv("DEV_SSHVAULT_NATIVE_CREATED"), []byte(bwItem), 0o600) != nil {
		return 2
	}
	public := map[string]any{"publicKey": item.SSHKey.PublicKey, "keyFingerprint": item.SSHKey.Fingerprint, "privateKey": privateSentinel}
	response := map[string]any{"id": bwItem, "type": 5, "name": item.Name, "fields": item.Fields, "organizationId": nil, "collectionIds": []string{}, "sshKey": public}
	switch config.Reply {
	case "public-number":
		public["publicKey"] = 7
	case "fingerprint-object":
		public["keyFingerprint"] = map[string]any{"bad": true}
	case "organization":
		response["organizationId"] = bwUser
	case "scope-object":
		response["collectionIds"] = map[string]any{"bad": true}
	case "lost":
		_, _ = io.WriteString(os.Stdout, privateSentinel)
		return 2
	}
	return writeNativeTestJSON(response)
}

func writeNativeTestJSON(value any) int {
	body, err := json.Marshal(value)
	if err != nil {
		return 2
	}
	if _, err := os.Stdout.Write(body); err != nil {
		return 2
	}
	return 0
}

type nativeFixture struct {
	root, bin, profile, control, calls, created, entry, runtime, manifest string
	config                                                                nativeTestConfig
}

func newNativeFixture(t *testing.T, node bool) *nativeFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve fixture root")
	}
	fixture := &nativeFixture{root: root, bin: filepath.Join(root, "bin"), profile: filepath.Join(root, "profile"), control: filepath.Join(root, "control.json"), calls: filepath.Join(root, "calls"), created: filepath.Join(root, "created"), config: nativeTestConfig{Version: BitwardenSchemaVersion, Status: "unlocked", UserID: bwUser, ServerURL: nil}}
	for _, path := range []string{fixture.bin, fixture.profile} {
		if os.Mkdir(path, 0o700) != nil {
			t.Fatal("make own fixture directory")
		}
	}
	if os.WriteFile(filepath.Join(fixture.profile, "data.json"), []byte("{}"), 0o600) != nil {
		t.Fatal("make existing synthetic profile metadata")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("locate own test binary")
	}
	fixture.entry = filepath.Join(fixture.bin, "bw")
	binary := fixture.entry
	if node {
		fixture.runtime = filepath.Join(fixture.bin, "node")
		binary = fixture.runtime
		packageRoot := filepath.Join(root, "lib", "node_modules", "@bitwarden", "cli")
		if os.MkdirAll(filepath.Join(packageRoot, "build"), 0o700) != nil {
			t.Fatal("create synthetic public package")
		}
		fixture.entry = filepath.Join(packageRoot, "build", "bw.js")
		fixture.manifest = filepath.Join(packageRoot, "package.json")
		if os.WriteFile(fixture.entry, []byte("#!/usr/bin/env node\n// Inert @bitwarden/cli test fixture.\n"), 0o700) != nil || os.WriteFile(fixture.manifest, []byte(`{"name":"@bitwarden/cli","version":"2026.3.0","bin":{"bw":"build/bw.js"}}`), 0o600) != nil || os.Symlink(fixture.entry, filepath.Join(fixture.bin, "bw")) != nil {
			t.Fatal("create bound Node entrypoint fixture")
		}
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal("open own test executable")
	}
	defer source.Close()
	target, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal("create own native executable fixture")
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy own native executable fixture")
	}
	t.Chdir(root)
	for _, name := range []string{"NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH"} {
		t.Setenv(name, "")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "DYLD_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("GORACE", "halt_on_error=1 atexit_sleep_ms=0")
	t.Setenv("PATH", fixture.bin)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BITWARDENCLI_APPDATA_DIR", fixture.profile)
	t.Setenv("BW_SESSION", nativeTestSession)
	t.Setenv("DEV_SSHVAULT_NATIVE_HELPER", "1")
	t.Setenv("DEV_SSHVAULT_NATIVE_CONTROL", fixture.control)
	t.Setenv("DEV_SSHVAULT_NATIVE_CALLS", fixture.calls)
	t.Setenv("DEV_SSHVAULT_NATIVE_CREATED", fixture.created)
	t.Setenv("DEV_SSHVAULT_NATIVE_PROFILE", fixture.profile)
	t.Setenv("DEV_SSHVAULT_NATIVE_CWD", root)
	t.Setenv("DEV_SSHVAULT_NATIVE_ENTRY", "")
	if node {
		t.Setenv("DEV_SSHVAULT_NATIVE_ENTRY", fixture.entry)
	}
	fixture.save(t)
	return fixture
}

func (fixture *nativeFixture) save(t *testing.T) {
	t.Helper()
	body, err := json.Marshal(fixture.config)
	if err != nil || os.WriteFile(fixture.control, body, 0o600) != nil {
		t.Fatal("write synthetic provider control")
	}
}

func nativeRequest() Request {
	request := bwRequest()
	request.NativeContextApproved = true
	return request
}

func (fixture *nativeFixture) assertNoCreate(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fixture.created); !os.IsNotExist(err) {
		t.Fatal("unexpected native fake mutation")
	}
}

func countedNativeService(runner sshcredential.CommandRunner) (*Service, *int) {
	service := NewService(runner)
	count := new(int)
	service.generate = func(state *planState) ([]byte, string, string, error) { *count++; return bitwardenInput(state) }
	return service, count
}

func TestBitwardenNativeContextStandaloneAndNode(t *testing.T) {
	for _, node := range []bool{false, true} {
		name := "standalone"
		if node {
			name = "node-package"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newNativeFixture(t, node)
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatalf("native plan: %v", err)
			}
			if plan.NativeProfile == nil || plan.NativeProfile.Path != fixture.profile || plan.NativeProfile.Entrypoint != fixture.entry || plan.NativeProfile.Runtime != fixture.runtime || plan.EndpointStatus != EndpointUnverified || !plan.NativeContextApproved {
				t.Fatal("native preview lost exact tool/profile or authority distinction")
			}
			body, _ := json.Marshal(plan)
			if strings.Contains(string(body), nativeTestSession) || strings.Contains(string(body), hex.EncodeToString(plan.state.native.environment[:])) {
				t.Fatal("session or private comparison hash escaped plan")
			}
			result, err := service.Apply(context.Background(), plan)
			if err != nil || result.Status != StatusCreated || result.NativeContextStatus != NativeObservedConsistent || result.EndpointStatus != EndpointUnverified || result.BindingStatus != BindingUnknown || result.Receipt == nil || result.Receipt.Fingerprint == "" || *generated != 1 {
				t.Fatalf("native creation result: %v", err)
			}
			assertPublicOnly(t, result, err)
			if !CanSelectAgentKey(plan, result) {
				t.Fatal("approved, consistent native receipt could not be matched to fresh agent inventory")
			}
			if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
				t.Fatal("native creation plan reused")
			}
		})
	}
}

func TestBitwardenNativePreflightRejectsChangesBeforeGeneration(t *testing.T) {
	for _, change := range []string{"session", "profile", "profile-link", "tool", "user", "locked", "home", "xdg", "path", "cwd", "portable"} {
		t.Run(change, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			if change == "profile-link" {
				link := filepath.Join(fixture.root, "profile-link")
				if os.Symlink(fixture.profile, link) != nil {
					t.Fatal("create own profile link")
				}
				t.Setenv("BITWARDENCLI_APPDATA_DIR", link)
			}
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatalf("native plan: %v", err)
			}
			switch change {
			case "session":
				t.Setenv("BW_SESSION", "changed-test-session")
			case "home":
				t.Setenv("HOME", filepath.Join(fixture.root, "other-home"))
			case "xdg":
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(fixture.root, "other-config"))
			case "path":
				t.Setenv("PATH", fixture.bin+string(os.PathListSeparator)+filepath.Join(fixture.root, "other-bin"))
			case "cwd":
				other := filepath.Join(fixture.root, "other-cwd")
				if os.Mkdir(other, 0o700) != nil {
					t.Fatal("make other cwd")
				}
				t.Chdir(other)
			case "user":
				fixture.config.UserID = bwItem
				fixture.save(t)
			case "locked":
				fixture.config.Status = "locked"
				fixture.save(t)
			case "tool":
				if os.Rename(fixture.entry, fixture.entry+".original") != nil || os.WriteFile(fixture.entry, []byte("#!/bin/sh\nexit 0\n"), 0o700) != nil {
					t.Fatal("replace own tool fixture")
				}
			case "profile":
				if os.Rename(fixture.profile, fixture.profile+".original") != nil || os.Mkdir(fixture.profile, 0o700) != nil || os.WriteFile(filepath.Join(fixture.profile, "data.json"), []byte("{}"), 0o600) != nil {
					t.Fatal("replace own profile fixture")
				}
			case "profile-link":
				other := filepath.Join(fixture.root, "other-profile")
				link := filepath.Join(fixture.root, "profile-link")
				if os.Mkdir(other, 0o700) != nil || os.WriteFile(filepath.Join(other, "data.json"), []byte("{}"), 0o600) != nil || os.Remove(link) != nil || os.Symlink(other, link) != nil {
					t.Fatal("retarget own profile link")
				}
			case "portable":
				portable := filepath.Join(fixture.bin, "bw-data")
				if os.Mkdir(portable, 0o700) != nil || os.WriteFile(filepath.Join(portable, "data.json"), []byte("{}"), 0o600) != nil {
					t.Fatal("create own portable fixture")
				}
			}
			result, err := service.Apply(context.Background(), plan)
			if err == nil || result.Status != StatusNotStarted || *generated != 0 {
				t.Fatal("changed context reached generation or mutation")
			}
			fixture.assertNoCreate(t)
		})
	}
}

type nativeHookRunner struct {
	before     func([]string)
	after      func([]string)
	loseCreate bool
}

func (*nativeHookRunner) Run(context.Context, string, []string, []byte) ([]byte, error) {
	return nil, ErrNativeContext
}
func (runner *nativeHookRunner) RunWithEnvironment(ctx context.Context, name string, args []string, input []byte, environment []string, directory string) ([]byte, error) {
	if runner.before != nil {
		runner.before(args)
	}
	body, err := (sshcredential.NativeRunner{}).RunWithEnvironment(ctx, name, args, input, environment, directory)
	if runner.after != nil {
		runner.after(args)
	}
	if runner.loseCreate && nativeCreateArgs(args) {
		return body, errors.New(sessionSentinel)
	}
	return body, err
}
func nativeCreateArgs(args []string) bool {
	return len(args) >= 2 && args[len(args)-2] == "create" && args[len(args)-1] == "item"
}

func TestBitwardenNativeFrozenEnvironmentSurvivesAmbientMutation(t *testing.T) {
	fixture := newNativeFixture(t, false)
	runner := &nativeHookRunner{}
	service, generated := countedNativeService(runner)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	runner.before = func(args []string) {
		if nativeCreateArgs(args) {
			t.Setenv("BW_SESSION", "changed-after-capture")
		}
	}
	result, err := service.Apply(context.Background(), plan)
	if !errors.Is(err, ErrUnknown) || result.Status != StatusCreated || result.Receipt == nil || result.Receipt.Fingerprint == "" || result.NativeContextStatus != NativeUnknown || result.EndpointStatus != EndpointUnverified || *generated != 1 {
		t.Fatal("late ambient mutation was inherited or post-change receipt was discarded")
	}
	if _, err := os.Stat(fixture.created); err != nil {
		t.Fatal("frozen native helper did not use reviewed session")
	}
	assertPublicOnly(t, result, err)
}

func TestBitwardenNativeRetainsPartialReceiptsAndNeverVerifiesEndpoints(t *testing.T) {
	for _, reply := range []string{"public-number", "fingerprint-object", "organization", "scope-object", "lost", "post-user", "locked-at-create", "custom-base"} {
		t.Run(reply, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			fixture.config.Reply = reply
			if reply == "custom-base" {
				fixture.config.ServerURL = "https://ignored-user:ignored-password@display.example.test/path?secret=ignored#fragment"
			}
			fixture.save(t)
			runner := &nativeHookRunner{}
			service, _ := countedNativeService(runner)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatal(err)
			}
			if reply == "post-user" {
				runner.after = func(args []string) {
					if nativeCreateArgs(args) {
						fixture.config.UserID = bwItem
						fixture.save(t)
					}
				}
			}
			if reply == "locked-at-create" {
				runner.before = func(args []string) {
					if nativeCreateArgs(args) {
						fixture.config.CreateLocked = true
						fixture.save(t)
					}
				}
			}
			result, err := service.Apply(context.Background(), plan)
			if result.EndpointStatus != EndpointUnverified || result.BindingStatus != BindingUnknown {
				t.Fatal("native delegation became endpoint verification")
			}
			switch reply {
			case "lost", "locked-at-create":
				if !errors.Is(err, ErrUnknown) || result.Status != StatusUnknown || result.Receipt != nil {
					t.Fatal("ambiguous native mutation invented a receipt")
				}
			case "custom-base":
				if err != nil || result.Receipt == nil || result.Receipt.ServerURL != "https://display.example.test" || result.NativeContextStatus != NativeObservedConsistent {
					t.Fatal("advisory base label was not safely separated from endpoints")
				}
			default:
				if err == nil || result.Status != StatusCreated || result.Receipt == nil || result.Receipt.ItemID != bwItem {
					t.Fatal("native partial creation lost its exact receipt")
				}
			}
			assertPublicOnly(t, result, err)
			if reply != "custom-base" && CanSelectAgentKey(plan, result) {
				t.Fatal("partial or uncertain creation authorized agent selection")
			}
			if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
				t.Fatal("partial native creation retried")
			}
		})
	}
}
