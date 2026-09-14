package sshvault

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

const (
	bwUser          = "11111111-1111-4111-8111-111111111111"
	bwItem          = "22222222-2222-4222-8222-222222222222"
	opAccount       = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	opUser          = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	opVault         = "cccccccccccccccccccccccccc"
	opItem          = "dddddddddddddddddddddddddd"
	privateSentinel = "PRIVATE_RESPONSE_MUST_NOT_ESCAPE"
	sessionSentinel = "SESSION_RESPONSE_MUST_NOT_ESCAPE"
)

type runStep struct {
	name string
	args []string
	body any
	run  func(context.Context, []byte) ([]byte, error)
}

type scriptedRunner struct {
	t       *testing.T
	steps   []runStep
	calls   int
	outputs [][]byte
	inputs  [][]byte
}

func (runner *scriptedRunner) Run(ctx context.Context, name string, args []string, input []byte) ([]byte, error) {
	runner.t.Helper()
	if runner.calls >= len(runner.steps) {
		runner.t.Fatal("unexpected provider command")
	}
	step := runner.steps[runner.calls]
	runner.calls++
	if name != step.name || !reflect.DeepEqual(args, step.args) {
		runner.t.Fatalf("provider command metadata mismatch at step %d", runner.calls)
	}
	for _, arg := range args {
		if strings.Contains(arg, privateSentinel) || strings.Contains(arg, sessionSentinel) || strings.Contains(arg, "PRIVATE KEY") {
			runner.t.Fatal("secret in provider argv")
		}
	}
	if len(input) > 0 {
		runner.inputs = append(runner.inputs, input)
		if name != "bw" || !reflect.DeepEqual(args, []string{"create", "item"}) {
			runner.t.Fatal("unexpected stdin")
		}
	}
	var body []byte
	var err error
	if step.run != nil {
		body, err = step.run(ctx, input)
	} else if text, ok := step.body.(string); ok {
		body = []byte(text)
	} else {
		body, err = json.Marshal(step.body)
	}
	runner.outputs = append(runner.outputs, body)
	return body, err
}

func (runner *scriptedRunner) verify(t *testing.T) {
	t.Helper()
	if runner.calls != len(runner.steps) {
		t.Fatalf("provider calls = %d, want %d", runner.calls, len(runner.steps))
	}
	for _, bodies := range [][][]byte{runner.outputs, runner.inputs} {
		for _, body := range bodies {
			for _, b := range body {
				if b != 0 {
					t.Fatal("provider response or secret stdin was not wiped")
				}
			}
		}
	}
}

func bwRequest() Request {
	return Request{Provider: Bitwarden, Title: "test SSH key", Experimental: true}
}

func opRequest() Request {
	return Request{Provider: OnePassword, Title: "test SSH key", VaultID: opVault}
}

func bwStatus() map[string]any {
	return map[string]any{"status": "unlocked", "userId": bwUser, "serverUrl": "https://vault.example.test", "userEmail": sessionSentinel}
}

func opObservation(account string) []runStep {
	whoami := []string{"whoami", "--format", "json"}
	if account != "" {
		whoami = append(whoami, "--account", account)
	}
	return []runStep{
		{name: "op", args: []string{"--version"}, body: "2.20.0\n"},
		{name: "op", args: whoami, body: map[string]any{"account_uuid": opAccount, "user_uuid": opUser, "url": "example.1password.com", "email": sessionSentinel}},
		{name: "op", args: []string{"vault", "get", opVault, "--account", opAccount, "--format", "json"}, body: map[string]any{"id": opVault, "name": "Test vault"}},
	}
}

func fixturePublic(t *testing.T) (string, string) {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.PublicKey(bytes.Repeat([]byte{7}, ed25519.PublicKeySize)))
	if err != nil {
		t.Fatal("construct public fixture")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), ssh.FingerprintSHA256(key)
}

func assertPublicOnly(t *testing.T, value any, err error) {
	t.Helper()
	body, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		t.Fatal("marshal public result")
	}
	text := string(body)
	if err != nil {
		text += err.Error()
	}
	for _, secret := range []string{privateSentinel, sessionSentinel, "PRIVATE KEY", "privateKey", "private_key", "BW_SESSION", "OP_SESSION"} {
		if strings.Contains(text, secret) {
			t.Fatal("secret or private field leaked into result")
		}
	}
}

func TestExportedRequestValidationIsStatic(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("BW_SESSION", "")
	for _, request := range []Request{opRequest(), bwRequest(), {Provider: Bitwarden, Title: "test", Experimental: true, NativeContextApproved: true}} {
		if err := ValidateRequest(request); err != nil {
			t.Fatal("static validation depended on provider or execution context")
		}
	}
	if err := ValidateRequest(Request{Provider: Bitwarden, Title: "invalid\xff", Experimental: true}); !errors.Is(err, ErrUnsafe) {
		t.Fatal("static validation accepted invalid request metadata")
	}
}

func TestRequestValidationDoesNotExecuteProviders(t *testing.T) {
	requests := []Request{
		{},
		{Provider: "unknown", Title: "test"},
		{Provider: Bitwarden, Title: "test"},
		{Provider: Bitwarden, Title: "test", Experimental: true, VaultID: "organization"},
		{Provider: Bitwarden, Title: "test", Experimental: true, AccountID: "--session"},
		{Provider: OnePassword, Title: "test", VaultID: "vault name"},
		{Provider: OnePassword, Title: "test", VaultID: opVault, AccountID: "--account"},
		{Provider: OnePassword, Title: "test", VaultID: opVault, Experimental: true},
	}
	for _, title := range []string{"", "-option", " leading", "trailing ", "line\nbreak", "escape\x1b", "bidi‮", strings.Repeat("x", 257)} {
		request := bwRequest()
		request.Title = title
		requests = append(requests, request)
	}
	for i, request := range requests {
		runner := &scriptedRunner{t: t}
		_, err := NewService(runner).Plan(context.Background(), request)
		if !errors.Is(err, ErrUnsafe) && !errors.Is(err, ErrExperimental) {
			t.Fatalf("request %d not rejected", i)
		}
		runner.verify(t)
	}
}

func TestBitwardenNeedsSeparateApprovalAndFrozenRunner(t *testing.T) {
	for _, test := range []struct {
		request Request
		err     error
	}{
		{bwRequest(), ErrUnsupportedContext},
		{Request{Provider: Bitwarden, Title: "test SSH key", NativeContextApproved: true}, ErrExperimental},
		{Request{Provider: Bitwarden, Title: "test SSH key", Experimental: true, NativeContextApproved: true}, ErrNativeContext},
	} {
		runner := &scriptedRunner{t: t}
		plan, err := NewService(runner).Plan(context.Background(), test.request)
		if !errors.Is(err, test.err) || plan.state != nil {
			t.Fatal("Bitwarden approval or frozen-runner guard was bypassed")
		}
		assertPublicOnly(t, plan, err)
		runner.verify(t)
	}
}

func TestOnePasswordPlanningGates(t *testing.T) {
	for _, version := range []string{"1.20.0", "2.19.9", "3.0.0", "2.20.0-beta", "2.20", "2.20.0.1", "2.-1.0"} {
		runner := &scriptedRunner{t: t, steps: []runStep{{name: "op", args: []string{"--version"}, body: version}}}
		if _, err := NewService(runner).Plan(context.Background(), opRequest()); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatal("unsupported 1Password version accepted")
		}
		runner.verify(t)
	}
	for _, test := range []struct {
		name   string
		mutate func([]runStep) []runStep
		err    error
	}{
		{"locked", func(steps []runStep) []runStep {
			steps[1].run = func(context.Context, []byte) ([]byte, error) {
				return []byte(privateSentinel), errors.New(sessionSentinel)
			}
			return steps[:2]
		}, ErrLocked},
		{"malformed", func(steps []runStep) []runStep { steps[1].body = "not JSON"; return steps[:2] }, ErrUnavailable},
		{"missing-user", func(steps []runStep) []runStep {
			steps[1].body = map[string]any{"account_uuid": opAccount, "url": "example.1password.com"}
			return steps[:2]
		}, ErrUnavailable},
		{"wrong-vault", func(steps []runStep) []runStep {
			steps[2].body = map[string]any{"id": opItem, "name": "Test vault"}
			return steps
		}, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{t: t, steps: test.mutate(opObservation(""))}
			plan, err := NewService(runner).Plan(context.Background(), opRequest())
			if !errors.Is(err, test.err) {
				t.Fatalf("planning error = %v, want %v", err, test.err)
			}
			assertPublicOnly(t, plan, err)
			runner.verify(t)
		})
	}
}

func TestPlanAuthorityRejectsTamperingAndDeserialization(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: opObservation("")}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), opRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertPublicOnly(t, plan, nil)
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Title = "different" },
		func(p *Plan) { p.Version = "2.21.0" },
		func(p *Plan) { p.Experimental = true },
		func(p *Plan) { p.Destination.Provider = Bitwarden },
		func(p *Plan) { p.Destination.AccountID = opItem },
		func(p *Plan) { p.Destination.UserID = opItem },
		func(p *Plan) { p.Destination.ServerURL = "https://different.example.test" },
		func(p *Plan) { p.Destination.VaultID = "other" },
		func(p *Plan) { p.Destination.VaultName = "other" },
		func(p *Plan) { p.state = nil },
	} {
		changed := plan
		mutate(&changed)
		result, err := service.Apply(context.Background(), changed)
		if !errors.Is(err, ErrStale) || result.Status != StatusNotStarted {
			t.Fatal("tampered plan accepted")
		}
	}
	body, _ := json.Marshal(plan)
	var decoded Plan
	if json.Unmarshal(body, &decoded) != nil {
		t.Fatal("decode public plan")
	}
	if _, err := service.Apply(context.Background(), decoded); !errors.Is(err, ErrStale) {
		t.Fatal("serialized plan acquired authority")
	}
	otherRunner := &scriptedRunner{t: t}
	if _, err := NewService(otherRunner).Apply(context.Background(), plan); !errors.Is(err, ErrStale) {
		t.Fatal("foreign service accepted plan")
	}
	otherRunner.verify(t)
	runner.verify(t)
}

func TestBitwardenEndpointAttestedModeDoesNotQueryOrCreate(t *testing.T) {
	runner := &scriptedRunner{t: t}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), bwRequest())
	if !errors.Is(err, ErrUnsupportedContext) || plan.state != nil || plan.Destination != (Destination{}) {
		t.Fatal("unsupported endpoint-attested mode acquired authority")
	}
	result, err := service.Apply(context.Background(), plan)
	if !errors.Is(err, ErrStale) || result.Status != StatusNotStarted || result.Receipt != nil {
		t.Fatal("unsupported Bitwarden mode permitted mutation")
	}
	runner.verify(t)
}

func TestBitwardenApplyCannotReachPrivateWriter(t *testing.T) {
	runner := &scriptedRunner{t: t}
	service := NewService(runner)
	// Even a legacy/internal plan carrying only a display base URL must not
	// regain access to creation. This is a refusal test, not native proof.
	state := bitwardenBackendState(service, bwRequest())
	state.dest.ServerURL = "https://vault.example.test"
	plan := Plan{Destination: state.dest, Version: state.version, Title: state.request.Title, Experimental: true, NativeContextApproved: true, state: state}
	result, err := service.Apply(context.Background(), plan)
	if !errors.Is(err, ErrUnsupportedContext) || result.Status != StatusNotStarted || state.attempted {
		t.Fatal("Bitwarden public apply reached the private writer")
	}
	runner.verify(t)
}

func TestInvalidUTF8TitlesNeverExecuteProviders(t *testing.T) {
	for _, request := range []Request{bwRequest(), opRequest()} {
		for _, title := range []string{"invalid\xfftitle", "truncated\xe2\x82"} {
			request.Title = title
			runner := &scriptedRunner{t: t}
			plan, err := NewService(runner).Plan(context.Background(), request)
			if !errors.Is(err, ErrUnsafe) || plan.state != nil {
				t.Fatal("invalid UTF-8 title reached provider execution")
			}
			runner.verify(t)
		}
	}
}

func TestOnePasswordApplyBindsExactCurrentAccountAndVersion(t *testing.T) {
	for _, field := range []string{"account_uuid", "user_uuid", "url", "version"} {
		t.Run(field, func(t *testing.T) {
			applySteps := opObservation(opAccount)
			if field == "version" {
				applySteps[0].body = "2.21.0"
			} else {
				identity := map[string]any{"account_uuid": opAccount, "user_uuid": opUser, "url": "example.1password.com"}
				if field == "url" {
					identity[field] = "other.1password.com"
				} else {
					identity[field] = opItem
				}
				applySteps[1].body = identity
				if field == "account_uuid" {
					applySteps = applySteps[:2]
				}
			}
			runner := &scriptedRunner{t: t, steps: append(opObservation(""), applySteps...)}
			service := NewService(runner)
			plan, err := service.Plan(context.Background(), opRequest())
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Apply(context.Background(), plan)
			if !errors.Is(err, ErrStale) || result.Status != StatusNotStarted {
				t.Fatal("changed 1Password identity permitted mutation")
			}
			runner.verify(t)
		})
	}
}

func TestCancelledPlanOrApplyDoesNotExecute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	emptyRunner := &scriptedRunner{t: t}
	if _, err := NewService(emptyRunner).Plan(ctx, bwRequest()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled plan was executed")
	}
	emptyRunner.verify(t)
	runner := &scriptedRunner{t: t, steps: opObservation("")}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), opRequest())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, plan)
	if !errors.Is(err, context.Canceled) || result.Status != StatusNotStarted {
		t.Fatal("cancelled apply was executed")
	}
	runner.verify(t)
}

func TestProviderOutputBoundAndErrorsAreSanitized(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		runner := &scriptedRunner{t: t, steps: []runStep{{name: "op", args: []string{"--version"}, run: func(context.Context, []byte) ([]byte, error) {
			if oversized {
				return bytes.Repeat([]byte{1}, maxOutput+1), nil
			}
			return []byte(privateSentinel), errors.New(sessionSentinel)
		}}}}
		plan, err := NewService(runner).Plan(context.Background(), opRequest())
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal("provider error or oversized response accepted")
		}
		assertPublicOnly(t, plan, err)
		runner.verify(t)
	}
}

func TestPublicIdentityRequiresExactlyOneEd25519Key(t *testing.T) {
	line, fingerprint := fixturePublic(t)
	canonical, actual, err := publicIdentity(line + " ignored comment")
	if err != nil || canonical != line || actual != fingerprint {
		t.Fatal("public identity did not canonicalize")
	}
	for _, bad := range []string{"", "not a key", line + "\n" + line, "command=\"id\" " + line, strings.Repeat("x", 4097)} {
		if _, _, err := publicIdentity(bad); !errors.Is(err, ErrPublicKey) {
			t.Fatal("invalid public identity accepted")
		}
	}
}

func TestPlanCanOnlyAttemptOneMutationConcurrently(t *testing.T) {
	line, _ := fixturePublic(t)
	steps := append(opObservation(""), opObservation(opAccount)...)
	steps = append(steps, successfulOPCreate("test SSH key", nil), opPublicStep(map[string]any{"id": "public_key", "type": "STRING", "value": line}))
	steps = append(steps, opObservation(opAccount)...)
	runner := &scriptedRunner{t: t, steps: steps}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), opRequest())
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := service.Apply(context.Background(), plan); outcomes <- err }()
	}
	wait.Wait()
	close(outcomes)
	created, refused := 0, 0
	for err := range outcomes {
		if err == nil {
			created++
		} else if errors.Is(err, ErrPlanUsed) {
			refused++
		} else {
			t.Fatal("unexpected concurrent apply outcome")
		}
	}
	if created != 1 || refused != 1 {
		t.Fatal("plan was not single-use")
	}
	runner.verify(t)
}
