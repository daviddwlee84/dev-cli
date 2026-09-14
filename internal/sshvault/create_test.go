package sshvault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"golang.org/x/crypto/ssh"
)

// Exercise the unconnected in-memory backend without pretending native status
// can supply effective-endpoint evidence. This state is never a public Plan.
func bitwardenBackendState(service *Service, request Request) *planState {
	request.NativeContextApproved = true
	return &planState{service: service, request: request, dest: Destination{Provider: Bitwarden, VaultID: PersonalVault}, version: BitwardenSchemaVersion, operation: strings.Repeat("a", 32)}
}

// This direct fake seam exists only in the test build. Public Plan/Apply cannot
// use it to bypass native execution-context guards.
func (s *Service) createBitwarden(ctx context.Context, state *planState) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createBitwardenUsing(ctx, state, func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		return s.run(ctx, "bw", args, input)
	})
}

func successfulBWCreate(t *testing.T, mutate func(map[string]any)) runStep {
	t.Helper()
	return runStep{name: "bw", args: []string{"create", "item"}, run: func(_ context.Context, input []byte) ([]byte, error) {
		t.Helper()
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(input)))
		n, err := base64.StdEncoding.Decode(decoded, input)
		defer sshcredential.Wipe(decoded)
		if err != nil {
			t.Fatal("Bitwarden stdin was not base64 JSON")
		}
		var item struct {
			Type           int             `json:"type"`
			Name           string          `json:"name"`
			Login          json.RawMessage `json:"login"`
			OrganizationID json.RawMessage `json:"organizationId"`
			CollectionIDs  []string        `json:"collectionIds"`
			Fields         []bwField       `json:"fields"`
			SSHKey         struct {
				PrivateKey     string `json:"privateKey"`
				PublicKey      string `json:"publicKey"`
				KeyFingerprint string `json:"keyFingerprint"`
			} `json:"sshKey"`
		}
		if json.Unmarshal(decoded[:n], &item) != nil || item.Type != 5 || item.Name == "" || string(item.Login) != "null" || string(item.OrganizationID) != "null" || item.CollectionIDs == nil || len(item.CollectionIDs) != 0 {
			t.Fatal("incorrect experimental Bitwarden type-5 schema")
		}
		private := []byte(item.SSHKey.PrivateKey)
		defer sshcredential.Wipe(private)
		signer, err := ssh.ParsePrivateKey(private)
		if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
			t.Fatal("generated private key does not parse as Ed25519")
		}
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
		if line != item.SSHKey.PublicKey || fingerprint != item.SSHKey.KeyFingerprint {
			t.Fatal("private/public key fingerprint mismatch")
		}
		if len(item.Fields) != 2 || item.Fields[0].Name != "dev-owner" || item.Fields[0].Value != vaultKeyOwner || item.Fields[1].Name != "dev-operation" || len(item.Fields[1].Value) != 32 {
			t.Fatal("missing exact operation ownership fields")
		}
		response := map[string]any{
			"id": bwItem, "type": 5, "name": item.Name, "organizationId": nil, "collectionIds": []string{}, "fields": item.Fields,
			"sshKey":        map[string]any{"privateKey": privateSentinel, "publicKey": line, "keyFingerprint": fingerprint},
			"ignoredSecret": sessionSentinel,
		}
		if mutate != nil {
			mutate(response)
		}
		return json.Marshal(response)
	}}
}

func successfulOPCreate(title string, mutate func(map[string]any)) runStep {
	return runStep{name: "op", args: []string{"item", "create", "--category", "ssh", "--title", title, "--vault", opVault, "--account", opAccount, "--format", "json"}, run: func(context.Context, []byte) ([]byte, error) {
		response := map[string]any{
			"id": opItem, "title": title, "category": "SSH_KEY", "vault": map[string]any{"id": opVault},
			"fields":        []any{map[string]any{"id": "private_key", "type": "SSHKEY", "value": privateSentinel}},
			"ignoredSecret": sessionSentinel,
		}
		if mutate != nil {
			mutate(response)
		}
		return json.Marshal(response)
	}}
}

func opPublicStep(body any) runStep {
	return runStep{name: "op", args: []string{"item", "get", opItem, "--vault", opVault, "--account", opAccount, "--fields", "label=public key", "--format", "json"}, body: body}
}

func TestBitwardenPrivateBackendCreatesOnlyInMemory(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{successfulBWCreate(t, nil)}}
	service := NewService(runner)
	request := bwRequest()
	request.Title = "工作站 SSH key"
	state := bitwardenBackendState(service, request)
	result, err := service.createBitwarden(context.Background(), state)
	if err != nil || result.Status != StatusCreated || result.BindingStatus != BindingUnknown || result.Receipt == nil || result.Receipt.ItemID != bwItem || result.Receipt.PublicLine == "" || result.Receipt.Fingerprint == "" {
		t.Fatalf("expected public-only backend receipt with unknown binding; error: %v", err)
	}
	assertPublicOnly(t, result, err)
	if _, err := service.createBitwarden(context.Background(), state); !errors.Is(err, ErrPlanUsed) {
		t.Fatal("private creation attempt was reused")
	}
	runner.verify(t)
}

func TestOnePasswordCreatesNativeKeyAndReadsPublicByExactID(t *testing.T) {
	line, fingerprint := fixturePublic(t)
	for _, asArray := range []bool{false, true} {
		field := map[string]any{"id": "public_key", "type": "STRING", "label": "public key", "value": line + " provider comment"}
		var publicResponse any = field
		if asArray {
			publicResponse = []any{field}
		}
		steps := append(opObservation(""), opObservation(opAccount)...)
		steps = append(steps, successfulOPCreate("test SSH key", nil), opPublicStep(publicResponse))
		steps = append(steps, opObservation(opAccount)...)
		runner := &scriptedRunner{t: t, steps: steps}
		service := NewService(runner)
		plan, err := service.Plan(context.Background(), opRequest())
		if err != nil {
			t.Fatal(err)
		}
		if plan.Destination.ServerURL != "https://example.1password.com" || plan.Destination.AccountID != opAccount || plan.Destination.UserID != opUser || plan.Destination.VaultID != opVault {
			t.Fatal("1Password plan identity mismatch")
		}
		result, err := service.Apply(context.Background(), plan)
		if err != nil || result.Status != StatusCreated || result.BindingStatus != BindingVerified || result.Receipt == nil || result.Receipt.ItemID != opItem || result.Receipt.PublicLine != line || result.Receipt.Fingerprint != fingerprint {
			t.Fatalf("1Password creation receipt incomplete: %v", err)
		}
		assertPublicOnly(t, result, err)
		if len(runner.inputs) != 0 {
			t.Fatal("1Password generation transmitted private stdin")
		}
		if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
			t.Fatal("1Password creation plan reused")
		}
		runner.verify(t)
	}
}

func TestAmbiguousBitwardenCreationIsNotRetried(t *testing.T) {
	for _, test := range []struct {
		name   string
		create runStep
	}{
		{"command-error", runStep{name: "bw", args: []string{"create", "item"}, run: func(context.Context, []byte) ([]byte, error) {
			return []byte(privateSentinel), errors.New(sessionSentinel)
		}}},
		{"malformed", runStep{name: "bw", args: []string{"create", "item"}, body: "not JSON " + privateSentinel}},
		{"wrong-id", successfulBWCreate(t, func(item map[string]any) { item["id"] = "--invalid" })},
		{"wrong-type", successfulBWCreate(t, func(item map[string]any) { item["type"] = 1 })},
		{"wrong-title", successfulBWCreate(t, func(item map[string]any) { item["name"] = "other" })},
		{"wrong-operation", successfulBWCreate(t, func(item map[string]any) {
			item["fields"] = []bwField{{Name: "dev-owner", Value: vaultKeyOwner}, {Name: "dev-operation", Value: "wrong"}}
		})},
		{"duplicate-owner", successfulBWCreate(t, func(item map[string]any) {
			item["fields"] = []bwField{{Name: "dev-owner", Value: vaultKeyOwner}, {Name: "dev-owner", Value: vaultKeyOwner}}
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{t: t, steps: []runStep{test.create}}
			service := NewService(runner)
			state := bitwardenBackendState(service, bwRequest())
			result, err := service.createBitwarden(context.Background(), state)
			if !errors.Is(err, ErrUnknown) || result.Status != StatusUnknown || result.Receipt != nil {
				t.Fatal("ambiguous creation was treated as known")
			}
			assertPublicOnly(t, result, err)
			if _, err := service.createBitwarden(context.Background(), state); !errors.Is(err, ErrPlanUsed) {
				t.Fatal("ambiguous private creation automatically retried")
			}
			runner.verify(t)
		})
	}
}

func TestBitwardenPrivateBackendRetainsItemBeforeDecodingPublicFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"key-mismatch", func(item map[string]any) { item["sshKey"].(map[string]any)["publicKey"] = "mismatch" }},
		{"fingerprint-mismatch", func(item map[string]any) { item["sshKey"].(map[string]any)["keyFingerprint"] = "mismatch" }},
		{"key-number", func(item map[string]any) { item["sshKey"].(map[string]any)["publicKey"] = 42 }},
		{"key-array", func(item map[string]any) { item["sshKey"].(map[string]any)["publicKey"] = []string{"invalid"} }},
		{"fingerprint-object", func(item map[string]any) {
			item["sshKey"].(map[string]any)["keyFingerprint"] = map[string]any{"invalid": true}
		}},
		{"ssh-key-number", func(item map[string]any) { item["sshKey"] = 42 }},
		{"ssh-key-missing", func(item map[string]any) { delete(item, "sshKey") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{t: t, steps: []runStep{successfulBWCreate(t, test.mutate)}}
			service := NewService(runner)
			state := bitwardenBackendState(service, bwRequest())
			result, err := service.createBitwarden(context.Background(), state)
			if !errors.Is(err, ErrPublicKey) || result.Status != StatusCreated || result.BindingStatus != BindingUnknown || result.Receipt == nil || result.Receipt.ItemID != bwItem || result.Receipt.PublicLine != "" || result.Receipt.Fingerprint != "" {
				t.Fatal("malformed public fields discarded a verified creation receipt")
			}
			assertPublicOnly(t, result, err)
			runner.verify(t)
		})
	}
}

func TestOnePasswordReceiptRetainedWhenPostWriteContextChanges(t *testing.T) {
	request := opRequest()
	line, _ := fixturePublic(t)
	steps := append(opObservation(""), opObservation(opAccount)...)
	steps = append(steps, successfulOPCreate(request.Title, nil), opPublicStep(map[string]any{"id": "public_key", "type": "STRING", "value": line}))
	post := opObservation(opAccount)
	post[1].run = func(context.Context, []byte) ([]byte, error) {
		return []byte(privateSentinel), errors.New(sessionSentinel)
	}
	steps = append(steps, post[:2]...)
	runner := &scriptedRunner{t: t, steps: steps}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan)
	if !errors.Is(err, ErrUnknown) || result.Status != StatusCreated || result.BindingStatus != BindingUnknown || result.Receipt == nil || result.Receipt.Fingerprint == "" {
		t.Fatal("post-write uncertainty discarded a valid creation receipt")
	}
	assertPublicOnly(t, result, err)
	if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
		t.Fatal("post-write uncertainty retried creation")
	}
	runner.verify(t)
}

func TestOnePasswordPartialPublicReadRetainsCreatedItem(t *testing.T) {
	line, _ := fixturePublic(t)
	for _, test := range []struct {
		name string
		step runStep
	}{
		{"read-error", func() runStep {
			step := opPublicStep(nil)
			step.run = func(context.Context, []byte) ([]byte, error) {
				return []byte(privateSentinel), errors.New(sessionSentinel)
			}
			return step
		}()},
		{"wrong-field", opPublicStep(map[string]any{"id": "private_key", "type": "SSHKEY", "value": privateSentinel})},
		{"invalid-key", opPublicStep(map[string]any{"id": "public_key", "type": "STRING", "value": "not a key"})},
		{"empty-array", opPublicStep([]any{})},
		{"multiple-fields", opPublicStep([]any{map[string]any{"id": "public_key", "type": "STRING", "value": line}, map[string]any{"id": "private_key", "type": "SSHKEY", "value": privateSentinel}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := append(opObservation(""), opObservation(opAccount)...)
			steps = append(steps, successfulOPCreate("test SSH key", nil), test.step)
			steps = append(steps, opObservation(opAccount)...)
			runner := &scriptedRunner{t: t, steps: steps}
			service := NewService(runner)
			plan, err := service.Plan(context.Background(), opRequest())
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Apply(context.Background(), plan)
			if !errors.Is(err, ErrPublicKey) || result.Status != StatusCreated || result.BindingStatus != BindingVerified || result.Receipt == nil || result.Receipt.ItemID != opItem || result.Receipt.PublicLine != "" {
				t.Fatal("public-read failure erased a created item")
			}
			assertPublicOnly(t, result, err)
			runner.verify(t)
		})
	}
}

func TestAmbiguousOnePasswordCreationIsNotRetried(t *testing.T) {
	for _, test := range []struct {
		name string
		step runStep
	}{
		{"error", func() runStep {
			step := successfulOPCreate("test SSH key", nil)
			step.run = func(context.Context, []byte) ([]byte, error) {
				return []byte(privateSentinel), errors.New(sessionSentinel)
			}
			return step
		}()},
		{"wrong-vault", successfulOPCreate("test SSH key", func(item map[string]any) { item["vault"] = map[string]any{"id": opItem} })},
		{"wrong-type", successfulOPCreate("test SSH key", func(item map[string]any) { item["category"] = "LOGIN" })},
		{"wrong-title", successfulOPCreate("test SSH key", func(item map[string]any) { item["title"] = "other" })},
		{"invalid-id", successfulOPCreate("test SSH key", func(item map[string]any) { item["id"] = "not-an-id" })},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := append(opObservation(""), opObservation(opAccount)...)
			steps = append(steps, test.step)
			runner := &scriptedRunner{t: t, steps: steps}
			service := NewService(runner)
			plan, err := service.Plan(context.Background(), opRequest())
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Apply(context.Background(), plan)
			if !errors.Is(err, ErrUnknown) || result.Status != StatusUnknown || result.Receipt != nil {
				t.Fatal("ambiguous 1Password mutation treated as known")
			}
			assertPublicOnly(t, result, err)
			if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
				t.Fatal("ambiguous 1Password mutation retried")
			}
			runner.verify(t)
		})
	}
}

func TestCancellationDuringCreateStaysUnknownAndSingleUse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	steps := append(opObservation(""), opObservation(opAccount)...)
	create := successfulOPCreate("test SSH key", nil)
	create.run = func(context.Context, []byte) ([]byte, error) {
		cancel()
		return []byte(privateSentinel), context.Canceled
	}
	steps = append(steps, create)
	runner := &scriptedRunner{t: t, steps: steps}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), opRequest())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, plan)
	if !errors.Is(err, ErrUnknown) || result.Status != StatusUnknown {
		t.Fatal("interrupted mutation was treated as not started")
	}
	assertPublicOnly(t, result, err)
	if _, err := service.Apply(context.Background(), plan); !errors.Is(err, ErrPlanUsed) {
		t.Fatal("interrupted mutation retried")
	}
	runner.verify(t)
}

func TestPEMJSONEncodingAvoidsImmutableSecretString(t *testing.T) {
	value := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nASCII\\\"\t\r\n-----END OPENSSH PRIVATE KEY-----\n")
	encoded := appendPEMString(nil, value)
	var decoded string
	if json.Unmarshal(encoded, &decoded) != nil || !reflect.DeepEqual([]byte(decoded), value) {
		t.Fatal("direct PEM JSON quoting changed bytes")
	}
	sshcredential.Wipe(encoded)
	sshcredential.Wipe(value)
}
