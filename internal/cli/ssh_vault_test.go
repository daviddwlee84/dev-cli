package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshvault"
)

const (
	vaultTestAccount   = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	vaultTestUser      = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	vaultTestVault     = "cccccccccccccccccccccccccc"
	vaultTestItem      = "dddddddddddddddddddddddddd"
	vaultTestBWUser    = "11111111-1111-4111-8111-111111111111"
	vaultTestBWItem    = "22222222-2222-4222-8222-222222222222"
	vaultPrivateMarker = "VAULT_PRIVATE_RESPONSE_MUST_NOT_ESCAPE"
	vaultSessionMarker = "VAULT_SESSION_RESPONSE_MUST_NOT_ESCAPE"
)

type sshVaultFakeBackend struct {
	plans, applies    int
	request           sshvault.Request
	planErr, applyErr error
	result            sshvault.Result
}

func (b *sshVaultFakeBackend) Plan(_ context.Context, request sshvault.Request) (sshvault.Plan, error) {
	b.plans++
	b.request = request
	plan := sshvault.Plan{Destination: sshvault.Destination{Provider: request.Provider, AccountID: request.AccountID, UserID: request.AccountID, VaultID: request.VaultID}, Title: request.Title, Experimental: request.Experimental, NativeContextApproved: request.NativeContextApproved}
	if request.Provider == sshvault.Bitwarden {
		plan.Version, plan.EndpointStatus = sshvault.BitwardenSchemaVersion, sshvault.EndpointUnverified
		plan.NativeProfile = &sshvault.NativeProfile{Path: "/reviewed/profile", Entrypoint: "/reviewed/bw", Runtime: "/reviewed/node", CWD: "/reviewed/cwd"}
	}
	return plan, b.planErr
}
func (b *sshVaultFakeBackend) Apply(context.Context, sshvault.Plan) (sshvault.Result, error) {
	b.applies++
	return b.result, b.applyErr
}

type sshVaultOPRunner struct {
	t              *testing.T
	public         string
	calls, creates int
	lost, changed  bool
	onCreate       func()
	outputs        [][]byte
}

func (r *sshVaultOPRunner) Run(_ context.Context, name string, args []string, input []byte) ([]byte, error) {
	r.t.Helper()
	r.calls++
	if name != "op" || len(input) != 0 || len(args) == 0 {
		r.t.Fatal("unexpected provider transport; no real native command is permitted")
	}
	for _, arg := range args {
		if strings.Contains(arg, vaultPrivateMarker) || strings.Contains(arg, vaultSessionMarker) || strings.Contains(arg, "PRIVATE KEY") {
			r.t.Fatal("secret reached provider argv")
		}
	}
	var response any
	switch {
	case args[0] == "--version":
		body := []byte("2.20.0\n")
		r.outputs = append(r.outputs, body)
		return body, nil
	case args[0] == "whoami":
		account := vaultTestAccount
		if r.changed && r.creates > 0 {
			account = "eeeeeeeeeeeeeeeeeeeeeeeeee"
		}
		response = map[string]any{"account_uuid": account, "user_uuid": vaultTestUser, "url": "example.1password.com", "email": vaultSessionMarker}
	case len(args) > 2 && args[0] == "vault" && args[1] == "get":
		response = map[string]any{"id": vaultTestVault, "name": "Test vault"}
	case len(args) > 1 && args[0] == "item" && args[1] == "create":
		r.creates++
		if r.onCreate != nil {
			r.onCreate()
		}
		if r.lost {
			return nil, errors.New(vaultPrivateMarker)
		}
		response = map[string]any{"id": vaultTestItem, "title": sshArgValue(args, "--title"), "category": "SSH_KEY", "vault": map[string]string{"id": vaultTestVault}, "private_key": vaultPrivateMarker}
	case len(args) > 2 && args[0] == "item" && args[1] == "get":
		if args[2] != vaultTestItem || sshArgValue(args, "--fields") != "label=public key" {
			r.t.Fatal("public receipt was not fetched by exact item/field")
		}
		response = map[string]any{"id": "public_key", "type": "STRING", "label": "public key", "value": r.public, "ignored_private": vaultPrivateMarker}
	default:
		r.t.Fatal("unexpected native provider command")
	}
	body, err := json.Marshal(response)
	r.outputs = append(r.outputs, body)
	return body, err
}

func runSSHVaultCommand(t *testing.T, f *sshCLIFixture, backend sshVaultBackend, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &stderr, sshHostRunner: f.runner, interactiveCheck: func() bool { return false }}
	root := newRootCommand(app)
	root.SetContext(context.WithValue(t.Context(), sshVaultBackendKey{}, backend))
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never", "ssh", "key", "create"}, args...))
	err := root.Execute()
	return out.String(), stderr.String(), err
}

func assertSSHVaultPublicOutput(t *testing.T, output, publicLine string) {
	t.Helper()
	for _, forbidden := range []string{vaultPrivateMarker, vaultSessionMarker, `"public_line"`, `"privateKey"`, `"private_key"`, "BW_SESSION", "OP_SESSION"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("vault output contains forbidden field/marker %q", forbidden)
		}
	}
	if fields := strings.Fields(publicLine); len(fields) > 1 && strings.Contains(output, fields[1]) {
		t.Fatal("complete public key blob escaped the public receipt projection")
	}
}

func TestSSHVaultDryRunIsUnobservedIntentWithoutNativeCalls(t *testing.T) {
	for _, flags := range [][]string{
		{"--provider", "1password", "--vault", vaultTestVault},
		{"--provider", "bitwarden", "--experimental", "--native-context"},
		{"--provider", "bitwarden", "--desktop"},
	} {
		f := newSSHCLIFixture(t)
		backend := &sshVaultFakeBackend{}
		out, _, err := runSSHVaultCommand(t, f, backend, append(flags, "--dry-run", "--json")...)
		if err != nil {
			t.Fatal(err)
		}
		doc := assertOneSSHJSON(t, out)
		preview, _ := doc["preview"].(map[string]any)
		if doc["kind"] != "ssh_key_create" || doc["status"] != "intent_only" || preview["intent_only"] != true || !strings.Contains(out, "UNOBSERVED") || backend.plans != 0 || backend.applies != 0 || f.runner.callCount() != 0 {
			t.Fatalf("dry-run acquired execution/readiness authority: %s", out)
		}
	}
}

func TestSSHVaultBitwardenApprovalsAreIndependentOfYes(t *testing.T) {
	for _, flags := range [][]string{nil, {"--experimental"}, {"--native-context"}} {
		f := newSSHCLIFixture(t)
		backend := &sshVaultFakeBackend{}
		args := append([]string{"--provider", "bitwarden", "--account", vaultTestBWUser, "--yes", "--json"}, flags...)
		out, _, err := runSSHVaultCommand(t, f, backend, args...)
		if err == nil || backend.plans != 0 || backend.applies != 0 || f.runner.callCount() != 0 || assertOneSSHJSON(t, out)["status"] != "blocked" {
			t.Fatalf("--yes authorized a missing Bitwarden approval: %v %s", err, out)
		}
	}
	f := newSSHCLIFixture(t)
	metadata, err := sshhost.ParsePublicKey(f.runner.publicLine)
	if err != nil {
		t.Fatal(err)
	}
	backend := &sshVaultFakeBackend{result: sshvault.Result{Status: sshvault.StatusCreated, BindingStatus: sshvault.BindingUnknown, NativeContextStatus: sshvault.NativeObservedConsistent, EndpointStatus: sshvault.EndpointUnverified, Receipt: &sshvault.Receipt{Destination: sshvault.Destination{Provider: sshvault.Bitwarden, AccountID: vaultTestBWUser, VaultID: "personal"}, ItemID: vaultTestBWItem, Fingerprint: metadata.Fingerprint, PublicLine: string(f.runner.publicLine)}}}
	out, _, err := runSSHVaultCommand(t, f, backend, "--provider", "bitwarden", "--account", vaultTestBWUser, "--experimental", "--native-context", "--yes", "--json")
	if err != nil || !backend.request.Experimental || !backend.request.NativeContextApproved || backend.applies != 1 {
		t.Fatalf("approvals were not passed distinctly: %v", err)
	}
	assertSSHVaultPublicOutput(t, out, string(f.runner.publicLine))
	doc := assertOneSSHJSON(t, out)
	result := doc["result"].(map[string]any)
	if result["binding_status"] != "unknown" || result["endpoint_status"] != "unverified" || result["native_context_status"] != "observed_consistent" || result["agent_status"] != "not_selected" {
		t.Fatal("Bitwarden projection upgraded context or a fabricated result authorized selection")
	}
}

func TestSSHVaultCreatedWithoutAgentRetainsPublicReceipt(t *testing.T) {
	f := newSSHCLIFixture(t)
	runner := &sshVaultOPRunner{t: t, public: string(f.runner.publicLine)}
	backend := sshvault.NewService(runner)
	out, stderr, err := runSSHVaultCommand(t, f, backend, "--provider", "1password", "--account", vaultTestAccount, "--vault", vaultTestVault, "--title", "Retained SSH key", "--yes", "--json")
	if err != nil {
		t.Fatalf("create without agent: %v %s", err, stderr)
	}
	doc := assertOneSSHJSON(t, out)
	result := doc["result"].(map[string]any)
	receipt := result["receipt"].(map[string]any)
	if doc["status"] != "created" || result["agent_status"] != "unavailable" || result["binding_status"] != "verified" || receipt["item_id"] != vaultTestItem || runner.creates != 1 {
		t.Fatalf("created item was misreported when agent was absent: %s", out)
	}
	assertSSHVaultPublicOutput(t, out+stderr, runner.public)
	if _, err := os.Stat(f.rootConfigPath()); !errors.Is(err, os.ErrNotExist) || f.runner.callCount() != 0 {
		t.Fatal("standalone vault creation configured or authenticated SSH")
	}
	for _, body := range runner.outputs {
		for _, b := range body {
			if b != 0 {
				t.Fatal("provider output buffer was not wiped")
			}
		}
	}
}

func TestSSHVaultUnknownAndChangedContextsKeepReceiptsWithoutRetry(t *testing.T) {
	for _, lost := range []bool{false, true} {
		f := newSSHCLIFixture(t)
		runner := &sshVaultOPRunner{t: t, public: string(f.runner.publicLine), changed: !lost, lost: lost}
		backend := sshvault.NewService(runner)
		out, stderr, err := runSSHVaultCommand(t, f, backend, "--provider", "1password", "--account", vaultTestAccount, "--vault", vaultTestVault, "--yes", "--json")
		if !errors.Is(err, sshvault.ErrUnknown) || runner.creates != 1 {
			t.Fatalf("unknown mutation was retried or hidden: %v", err)
		}
		assertSSHVaultPublicOutput(t, out+stderr, runner.public)
		result := assertOneSSHJSON(t, out)["result"].(map[string]any)
		receipt := result["receipt"].(map[string]any)
		if result["binding_status"] != "unknown" || result["agent_status"] != "not_checked" {
			t.Fatal("changed context was treated as selected-agent authority")
		}
		if !lost && receipt["item_id"] != vaultTestItem {
			t.Fatal("known created item ID was discarded after context changed")
		}
		if lost && (result["status"] != "unknown" || receipt["item_id"] != nil) {
			t.Fatal("lost response fabricated a created item identity")
		}
	}
}

func TestSSHVaultPlanContextMismatchStopsBeforeApply(t *testing.T) {
	f := newSSHCLIFixture(t)
	backend := &sshVaultFakeBackend{planErr: sshvault.ErrStale}
	out, _, err := runSSHVaultCommand(t, f, backend, "--provider", "1password", "--account", vaultTestAccount, "--vault", vaultTestVault, "--yes", "--json")
	if !errors.Is(err, sshvault.ErrStale) || backend.applies != 0 || assertOneSSHJSON(t, out)["error_code"] != "source_changed" {
		t.Fatal("stale native source reached creation")
	}
}

func TestSSHVaultAttemptIdentityIsStablePerPlanAndUniqueAcrossPlans(t *testing.T) {
	f := newSSHCLIFixture(t)
	backend := &sshVaultFakeBackend{result: sshvault.Result{Status: sshvault.StatusUnknown, BindingStatus: sshvault.BindingUnknown}, applyErr: sshvault.ErrUnknown}
	ctx := context.WithValue(t.Context(), sshVaultBackendKey{}, backend)
	app := &App{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, sshHostRunner: f.runner}
	request := sshflow.VaultKeyRequest{Provider: "1password", AccountID: vaultTestAccount, VaultID: vaultTestVault, Title: "same title"}
	first, err := prepareSSHVaultKey(ctx, app, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareSSHVaultKey(ctx, app, request)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.Preview().AttemptID
	if firstID == "" || firstID != first.Preview().AttemptID || firstID == second.Preview().AttemptID {
		t.Fatal("attempt ID was missing, unstable or reused")
	}
	a, _ := applySSHVaultKey(ctx, first)
	b, _ := applySSHVaultKey(ctx, second)
	if a.Receipt == nil || b.Receipt == nil || a.Receipt.AttemptID != first.Preview().AttemptID || b.Receipt.AttemptID != second.Preview().AttemptID || a.Receipt.ItemID != "" || b.Receipt.ItemID != "" {
		t.Fatal("unknown attempts lost controller identity or invented item IDs")
	}
	ledger := sshflow.RetainVaultKeyReceipt(nil, a.Receipt)
	ledger = sshflow.RetainVaultKeyReceipt(ledger, b.Receipt)
	ledger = sshflow.RetainVaultKeyReceipt(ledger, a.Receipt)
	if len(ledger) != 2 {
		t.Fatal("separate same-shaped unknown attempts collapsed")
	}
	if _, err := applySSHVaultKey(ctx, first); !errors.Is(err, sshvault.ErrPlanUsed) || backend.applies != 2 {
		t.Fatal("same reviewed plan repeated creation")
	}
}

func TestSSHVaultPublicReceiptProjectionHasNoKeyMaterialField(t *testing.T) {
	body, err := json.Marshal(sshflow.VaultKeyResult{Receipt: &sshflow.VaultKeyReceipt{Provider: "bitwarden", ItemID: vaultTestBWItem, Status: "created", BindingStatus: "unknown", EndpointStatus: "unverified"}})
	if err != nil {
		t.Fatal(err)
	}
	assertSSHVaultPublicOutput(t, string(body), "")
}
