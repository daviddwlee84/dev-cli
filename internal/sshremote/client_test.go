package sshremote

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

type loopbackRunner struct {
	server       *Server
	helpers      []string
	hosts        []fleet.Host
	options      []fleet.RunOptions
	interactive  int
	connect      []string
	password     bool
	usedPassword bool
	result       *fleet.Result
}

func (r *loopbackRunner) RunWithOptions(ctx context.Context, host fleet.Host, args []string, stdin []byte, options fleet.RunOptions) fleet.Result {
	r.hosts = append(r.hosts, host)
	r.options = append(r.options, options)
	if len(args) != 2 || args[0] != "fleet" {
		return fleet.Result{ExitCode: 1}
	}
	r.helpers = append(r.helpers, args[1])
	if r.result != nil {
		return *r.result
	}
	var response any
	var err error
	switch args[1] {
	case "_ssh-capability":
		var request CapabilityRequest
		err = fleet.UnmarshalStrict(stdin, MaxRequestBytes, &request)
		if err == nil {
			response, err = r.server.Capability(ctx, request)
		}
	case "_ssh-inventory":
		var request Request
		err = fleet.UnmarshalStrict(stdin, MaxRequestBytes, &request)
		if err == nil {
			response, err = r.server.Inventory(ctx, request)
		}
	case "_ssh-resolve":
		var request ResolveRequest
		err = fleet.UnmarshalStrict(stdin, MaxRequestBytes, &request)
		if err == nil {
			response, err = r.server.Resolve(ctx, request)
		}
	case "_ssh-keys":
		var request KeysRequest
		err = fleet.UnmarshalStrict(stdin, MaxRequestBytes, &request)
		if err == nil {
			response, err = r.server.Keys(ctx, request)
		}
	default:
		return fleet.Result{ExitCode: 1}
	}
	if err != nil {
		response = ProtocolError(err)
	}
	data, _ := fleet.MarshalBounded(response, MaxResponseBytes)
	return fleet.Result{Stdout: data, UsedPassword: r.usedPassword}
}
func (r *loopbackRunner) InteractiveNoForward(_ context.Context, _ fleet.Host, args []string, password bool) error {
	r.interactive++
	r.password = password
	r.connect = append([]string(nil), args...)
	return &ExitError{Code: 255}
}

func TestConnectReusesObservedAuthenticationNotConfiguredProvider(t *testing.T) {
	for _, usedPassword := range []bool{false, true} {
		server, _, _ := testServer(t, "Host target\n HostName target.example\n")
		runner := &loopbackRunner{server: server, usedPassword: usedPassword}
		client := Client{Runner: runner}
		host := fleet.Host{Name: "lab", SSHAlias: "lab", SSHLoginPasswordSource: fleet.PasswordSource{Type: "bitwarden", Item: "configured-but-not-always-needed"}}
		inventory, err := client.Inventory(context.Background(), host)
		if err != nil {
			t.Fatal(err)
		}
		profile, _ := inventory.Find("target")
		_ = client.Connect(context.Background(), host, profile.Selection(inventory.Origin), "", false)
		if runner.password != usedPassword || runner.interactive != 1 {
			t.Fatalf("used=%v selected=%v attempts=%d", usedPassword, runner.password, runner.interactive)
		}
	}
}

func TestClientNegotiatesPerOperationAndExecutesOnce(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	runner := &loopbackRunner{server: server}
	client := Client{Runner: runner}
	host := fleet.Host{Name: "lab", SSHAlias: "lab"}
	inventory, err := client.Inventory(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inventory.Find("target")
	selection := profile.Selection(inventory.Origin)
	resolved, err := client.Resolve(context.Background(), host, selection)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Importable || resolved.Effective.Values != nil {
		t.Fatalf("resolved=%#v", resolved)
	}
	if _, err := client.Keys(context.Background(), host, "", true); err != nil {
		t.Fatal(err)
	}
	err = client.Connect(context.Background(), host, selection, "", false)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 255 || runner.interactive != 1 {
		t.Fatalf("connect %v count %d", err, runner.interactive)
	}
	if len(runner.connect) != 4 || runner.connect[1] != "_ssh-connect" || runner.connect[2] != "--request" {
		t.Fatalf("exec args %#v", runner.connect)
	}
	want := "_ssh-capability,_ssh-inventory,_ssh-capability,_ssh-resolve,_ssh-capability,_ssh-keys,_ssh-capability"
	if strings.Join(runner.helpers, ",") != want {
		t.Fatalf("helper order: %v", runner.helpers)
	}
}

func TestClientPinMismatchStopsBeforePayloadAndDoesNotChangeConfig(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	runner := &loopbackRunner{server: server}
	client := Client{Runner: runner}
	host := fleet.Host{Name: "lab", SSHAlias: "lab", MachineID: "11111111-1111-4111-8111-111111111111"}
	if _, err := client.Inventory(context.Background(), host); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("pin mismatch: %v", err)
	}
	if len(runner.helpers) != 1 || host.MachineID != "11111111-1111-4111-8111-111111111111" {
		t.Fatal("mismatch performed payload or changed pin")
	}
}

func TestClientClassifiesOldMissingMalformedAndCredentialSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		result fleet.Result
		want   error
	}{
		{"no-dev", fleet.Result{ExitCode: 127}, ErrUnavailable}, {"old-dev", fleet.Result{ExitCode: 1, Stderr: []byte("private error")}, ErrIncompatible},
		{"unreachable", fleet.Result{ExitCode: 255}, ErrUnreachable}, {"timeout", fleet.Result{ExitCode: 124}, ErrTimeout},
		{"malformed", fleet.Result{Stdout: []byte("private banner")}, ErrInvalidData}, {"bounded", fleet.Result{CaptureError: "private bytes"}, ErrInvalidData},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &loopbackRunner{result: &test.result}
			_, err := (Client{Runner: runner}).Inventory(context.Background(), fleet.Host{Name: "lab", SSHAlias: "lab"})
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private") {
				t.Fatalf("error %v", err)
			}
		})
	}
	denied := fleet.Result{ExitCode: 255}
	runner := &loopbackRunner{result: &denied}
	client := Client{Runner: runner}
	host := fleet.Host{Name: "lab", SSHAlias: "lab", SSHLoginPasswordSource: fleet.PasswordSource{Type: "prompt"}}
	if _, err := client.Capability(context.Background(), host); !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("prompt not gated: %v", err)
	}
	if runner.hosts[0].PasswordKind() != "prompt" || runner.options[0].Retry != fleet.RetryNever {
		t.Fatal("noninteractive metadata must retain explicit source without password retry")
	}
	client.AllowPrompt = true
	_, _ = client.Capability(context.Background(), host)
	if runner.hosts[1].PasswordKind() != "prompt" || runner.options[1].Retry != fleet.RetryAuthentication {
		t.Fatal("explicit configured prompt was lost")
	}
}

func TestClientRejectsUnknownFieldsAndTrailingPayload(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	capability := testCapability(t, server)
	body, _ := fleet.MarshalBounded(capability, MaxResponseBytes)
	for _, payload := range [][]byte{append(append([]byte(nil), body...), []byte("{}")...), []byte(strings.TrimSpace(string(body[:len(body)-2])) + ",\"unexpected\":true}\n")} {
		result := fleet.Result{Stdout: payload}
		_, err := (Client{Runner: &loopbackRunner{result: &result}}).Capability(context.Background(), fleet.Host{Name: "lab", SSHAlias: "lab"})
		if !errors.Is(err, ErrInvalidData) {
			t.Fatalf("accepted unknown/trailing payload: %v", err)
		}
	}
}
