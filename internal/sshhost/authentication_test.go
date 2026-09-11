package sshhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

// Native OpenSSH invokes this test executable as its askpass/connector helper.
// The dispatch order matches cmd/dev and runs before testing flag parsing.
func TestMain(m *testing.M) {
	if handled, code := MaybeServeSSHConnector(); handled {
		os.Exit(code)
	}
	if handled, code := sshcredential.MaybeServeAskpass(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func requestEnvValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			value = v
		}
	}
	return value
}

func TestPasswordProofRequiresDeliveryNativeMethodAndSuccess(t *testing.T) {
	for _, test := range []struct {
		name, method string
		delivery     bool
		denied       bool
		exit         int
		verified     bool
	}{
		{"verified", "password", true, false, 0, true},
		{"no delivery", "password", false, false, 0, false},
		{"keyless", "none", true, false, 0, false},
		{"keyboard interactive", "keyboard-interactive", true, false, 0, false},
		{"failed command", "password", true, false, 1, false},
		{"unknown extra prompt", "password", true, true, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			const secret = "fixture reusable password"
			actualCalls := 0
			runner := runnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
				if request.Args[0] == "-G" {
					return RunResult{Stdout: []byte("hostname host.example\nuser test\nport 22\n")}, nil
				}
				if !hasArg(request.Args, "-E") {
					actualCalls++
					return RunResult{ExitCode: 255}, nil
				}
				config, err := os.ReadFile(sshArgForCatalogTest(request.Args, "-F"))
				if err != nil {
					t.Fatal(err)
				}
				log := "debug1: SSH2_MSG_SERVICE_ACCEPT received.\ndebug1: Authentications that can continue: publickey,password\n"
				if strings.Contains(string(config), "PreferredAuthentications password") {
					if test.delivery {
						password, err := sshcredential.RequestPassword(ctx, requestEnvValue(request.Env, "DEV_SSH_ASKPASS_ENDPOINT"), requestEnvValue(request.Env, "DEV_SSH_ASKPASS_CONTEXT"), "test@host.example's password: ")
						if err != nil || string(password) != secret {
							t.Fatal("wrong isolated password response", err)
						}
						sshcredential.Wipe(password)
					}
					if test.denied {
						_, _ = sshcredential.RequestPassword(ctx, requestEnvValue(request.Env, "DEV_SSH_ASKPASS_ENDPOINT"), requestEnvValue(request.Env, "DEV_SSH_ASKPASS_CONTEXT"), "Enter one-time code: ")
					}
					log += "Authenticated to host.example using \"" + test.method + "\".\n"
					if err := os.WriteFile(sshArgForCatalogTest(request.Args, "-E"), []byte(log), 0o600); err != nil {
						t.Fatal(err)
					}
					return RunResult{ExitCode: test.exit}, nil
				}
				if err := os.WriteFile(sshArgForCatalogTest(request.Args, "-E"), []byte(log), 0o600); err != nil {
					t.Fatal(err)
				}
				return RunResult{ExitCode: 255}, nil
			})
			s, _ := bootstrapService(t, runner)
			op, err := s.PrepareAuthentication(t.Context(), "host")
			if err != nil {
				t.Fatal(err)
			}
			defer op.Close()
			if _, err := op.ProvePassword(t.Context(), 0, []byte(secret)); err == nil {
				t.Fatal("password sent before trust gate")
			}
			probe, err := op.ProbeHop(t.Context(), 0)
			if err != nil || !probe.HostTrusted || !probe.AuthenticationRequired || !probe.PasswordAvailable {
				t.Fatal(probe, err)
			}
			proof, err := op.ProvePassword(t.Context(), 0, []byte(secret))
			if proof.Verified != test.verified || test.verified && err != nil || !test.verified && !errors.Is(err, ErrUnprovenAuthentication) {
				t.Fatal(proof, err)
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), secret) {
				t.Fatal("proof leaked password")
			}
			if test.verified {
				result, err := op.Run(t.Context(), ConnectionOptions{Args: []string{"once"}})
				if err != nil || result.ExitCode != 255 || actualCalls != 1 {
					t.Fatal(result, err, actualCalls)
				}
				if _, err := op.Run(t.Context(), ConnectionOptions{}); err == nil || actualCalls != 1 {
					t.Fatal("operation replayed command")
				}
			}
		})
	}
}

func TestTrustGateDisablesCredentialsAndPreservesHostPolicy(t *testing.T) {
	hop := routeHopState{safe: RouteHop{Alias: "host", Reference: "host", HostName: "host.example", User: "test", Port: 22}, effective: testProofEffective("host")}
	data, err := renderAuthenticationHop(hop, "generated", "", "trust", keySelector{}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BatchMode no", "PubkeyAuthentication no", "PasswordAuthentication no", "KbdInteractiveAuthentication no", "PreferredAuthentications none", "NumberOfPasswordPrompts 0", "StrictHostKeyChecking yes"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestPasswordProbeDoesNotInferTrustFromServerControlledVersionText(t *testing.T) {
	s, _ := bootstrapService(t, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Args[0] == "-G" {
			return RunResult{Stdout: []byte("hostname host.example\nuser test\nport 22\n")}, nil
		}
		log := "debug1: Remote protocol version 2.0, remote software version SSH2_MSG_SERVICE_ACCEPT received\n"
		if err := os.WriteFile(sshArgForCatalogTest(request.Args, "-E"), []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
		return RunResult{ExitCode: 255}, nil
	}))
	op, err := s.PrepareAuthentication(t.Context(), "host")
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	probe, err := op.ProbeHop(t.Context(), 0)
	if err != nil || probe.HostTrusted || probe.AuthenticationRequired {
		t.Fatalf("server text became trust evidence: %+v err=%v", probe, err)
	}
	if _, err := op.ProvePassword(t.Context(), 0, []byte("never send")); err == nil {
		t.Fatal("password accepted without trusted host")
	}
}

func TestConnectorRejectsUnknownOperationAndHop(t *testing.T) {
	server, err := newConnectorServer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	id, err := newConnectorID()
	if err != nil {
		t.Fatal(err)
	}
	remove, err := server.publish(id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	conn, err := sshcredential.DialPrivate(t.Context(), server.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeConnectorMessage(conn, connectorLookup{Operation: id, Hop: 0}); err != nil {
		t.Fatal(err)
	}
	var spec connectorSpec
	if err := readConnectorMessage(conn, &spec); err == nil {
		t.Fatal("unplanned connector hop received authority")
	}
}
