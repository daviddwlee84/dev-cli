package sshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

type diagnosticRunnerFunc func(context.Context, RunRequest) (RunResult, error)

func (f diagnosticRunnerFunc) Run(ctx context.Context, r RunRequest) (RunResult, error) {
	return f(ctx, r)
}
func diagnosticTestService(t *testing.T, runner Runner, hooks DiagnosticHooks) *Service {
	t.Helper()
	paths, err := NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(paths, runner, ServiceOptions{Diagnostics: hooks})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func diagnosticConfig() []byte {
	return []byte("hostname 192.0.2.30\nport 22\nuser private-user\nidentityfile /private/key\nipqos ef cs0\naddressfamily inet\n")
}
func diagnosticTestNetwork() DiagnosticHooks {
	return DiagnosticHooks{
		Route: func(_ context.Context, q DiagnosticRouteQuery) (DiagnosticRoute, error) {
			return DiagnosticRoute{Address: q.Address, Family: "ipv4", Interface: "test-tunnel", Code: "route_observed"}, nil
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			a, b := net.Pipe()
			go func() { defer b.Close(); _, _ = b.Write([]byte("SSH-2.0-test-server\r\n")) }()
			return a, nil
		},
	}
}
func TestDiagnoseFreshLoginAndPublicProjection(t *testing.T) {
	calls := 0
	service := diagnosticTestService(t, diagnosticRunnerFunc(func(ctx context.Context, r RunRequest) (RunResult, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded subprocess")
		}
		if r.Args[0] == "-G" {
			return RunResult{Stdout: diagnosticConfig()}, nil
		}
		joined := strings.Join(r.Args, " ")
		for _, required := range []string{"-S none", "BatchMode=yes", "StrictHostKeyChecking=yes", "UpdateHostKeys=no", "ClearAllForwardings=yes", "PermitLocalCommand=no", "RemoteCommand=none", "RequestTTY=no", "exit 0"} {
			if !strings.Contains(joined, required) {
				t.Errorf("missing %s", required)
			}
		}
		return RunResult{}, nil
	}), diagnosticTestNetwork())
	result, err := service.Diagnose(context.Background(), DiagnoseRequest{Target: "test-alias"})
	if err != nil || result.Status != "ready" || calls != 2 {
		t.Fatalf("%+v %v calls=%d", result, err, calls)
	}
	data, _ := json.Marshal(result)
	public, err := ParsePublicDiagnosis(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(public)
	for _, private := range []string{"192.0.2.30", "private-user", "/private/key", "test-tunnel", "test-alias", "test-server"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("public projection leaked %q", private)
		}
	}
	if public.Status != "ready" {
		t.Fatal(public)
	}
}
func TestDiagnoseClassifiesSSHStages(t *testing.T) {
	tests := []struct {
		name, log, code string
		err             error
	}{
		{"refused", "connect: Connection refused", "connection_refused", nil},
		{"connect_timeout", "connect: Operation timed out", "connect_timeout", nil},
		{"handshake_timeout", "Connection established.\nConnection timed out during banner exchange", "handshake_timeout", nil},
		{"unknown_key", "No ED25519 host key is known for example and you have requested strict checking.\nHost key verification failed.", "host_key_unknown", nil},
		{"changed_key", "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!", "host_key_changed", nil},
		{"generic_key", "Host key verification failed.", "host_key_rejected", nil},
		{"auth", "Permission denied (publickey).", "authentication_denied", nil},
		{"agent", "sign_and_send_pubkey: signing failed: agent refused operation", "agent_refused", nil},
		{"session", "Authenticated to example using publickey.", "session_failed", nil},
		{"cancel", "", "canceled", context.Canceled},
		{"unknown", "secret-sentinel unexpected vendor error", "ssh_failed", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyDiagnosticSSH("baseline", RunResult{ExitCode: 255, Stderr: []byte(tt.log)}, tt.err)
			if result.Code != tt.code || result.Ready {
				t.Fatalf("%+v", result)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "secret-sentinel") {
				t.Fatal("raw stderr leaked")
			}
		})
	}
}
func TestDiagnoseQoSComparisonAndCapability(t *testing.T) {
	for _, tt := range []struct{ name, marker, endpoint, want string }{{"progress", "debug3: set_sock_tos: set socket 3 IP_TOS 0xb8\n", "192.0.2.30", "qos_correlated_progress"}, {"windows_no_marking", "", "192.0.2.30", "qos_marking_unproven"}, {"different_endpoint", "debug3: set_sock_tos: set socket 3 IP_TOS 0xb8\n", "192.0.2.31", "path_not_comparable"}} {
		t.Run(tt.name, func(t *testing.T) {
			service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
				if r.Args[0] == "-G" {
					return RunResult{Stdout: diagnosticConfig()}, nil
				}
				if strings.Contains(strings.Join(r.Args, " "), "IPQoS=none") {
					return RunResult{ExitCode: 255, Stderr: []byte(fmt.Sprintf("Connecting to test [%s] port 22.\nConnection established.\nHost key verification failed.", tt.endpoint))}, nil
				}
				return RunResult{ExitCode: 255, Stderr: []byte("Connecting to test [192.0.2.30] port 22.\n" + tt.marker + "Connection timed out")}, nil
			}), diagnosticTestNetwork())
			result, _ := service.Diagnose(context.Background(), DiagnoseRequest{Target: "test", CompareQoS: true})
			if got := result.Stages[len(result.Stages)-1].Code; got != tt.want {
				t.Fatalf("got %s want %s: %+v", got, tt.want, result)
			}
			if result.Status == "ready" {
				t.Fatal("comparison changed baseline readiness")
			}
		})
	}
}
func TestDiagnoseProxySkipsDirectNetworkAndCancellation(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := DiagnosticHooks{LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
				t.Fatal("proxied target resolved locally")
				return nil, nil
			}, Dial: func(context.Context, string, string) (net.Conn, error) { t.Fatal("proxy bypass"); return nil, nil }, Route: func(context.Context, DiagnosticRouteQuery) (DiagnosticRoute, error) {
				t.Fatal("proxy bypass")
				return DiagnosticRoute{}, nil
			}}
			service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
				if r.Args[0] == "-G" {
					return RunResult{Stdout: append(diagnosticConfig(), []byte("proxycommand sensitive-command\n")...)}, nil
				}
				if cancelRun {
					cancel()
					return RunResult{}, ctx.Err()
				}
				return RunResult{}, nil
			}), hooks)
			result, err := service.Diagnose(ctx, DiagnoseRequest{Target: "test"})
			if cancelRun && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			for _, stage := range result.Stages {
				if stage.Name == "dns" || stage.Name == "route" || stage.Name == "tcp" || stage.Name == "banner" {
					if stage.Code != "proxy_path" {
						t.Fatal(stage)
					}
				}
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "sensitive-command") {
				t.Fatal("proxy command leaked")
			}
		})
	}
}
func TestDiagnoseNetworkFailureStages(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		dial       func(context.Context, string, string) (net.Conn, error)
	}{
		{"refused", "connection_refused", func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("connection refused at secret-endpoint")
		}},
		{"timeout", "connect_timeout", func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }},
		{"not_ssh", "non_ssh_banner", func(context.Context, string, string) (net.Conn, error) {
			a, b := net.Pipe()
			go func() { defer b.Close(); _, _ = b.Write([]byte("HTTP/1.0 secret-banner\r\n")) }()
			return a, nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hooks := diagnosticTestNetwork()
			hooks.Dial = tt.dial
			service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
				if r.Args[0] == "-G" {
					return RunResult{Stdout: diagnosticConfig()}, nil
				}
				return RunResult{ExitCode: 255}, nil
			}), hooks)
			result, _ := service.Diagnose(context.Background(), DiagnoseRequest{Target: "test"})
			found := false
			for _, stage := range result.Stages {
				found = found || stage.Code == tt.want
			}
			if !found {
				t.Fatal(result)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret-") {
				t.Fatal("network error or banner leaked")
			}
		})
	}
}
func TestDiagnosticTargetAndPublicInputBounds(t *testing.T) {
	for _, target := range []string{"-oProxyCommand=bad", "host\nnext", "host bad", "user@host", "fe80::1%bad/zone"} {
		if ValidateDiagnosticTarget(target) == nil {
			t.Errorf("accepted %q", target)
		}
	}
	for _, target := range []string{"host-alias", "example.test", "192.0.2.30", "2001:db8::1", "fe80::1%en0"} {
		if err := ValidateDiagnosticTarget(target); err != nil {
			t.Errorf("%q: %v", target, err)
		}
	}
	forged := []byte(`{"schema_version":1,"kind":"ssh_diagnosis","status":"secret-status","stages":[{"name":"config","state":"secret-state","code":"secret-code","elapsed_ms":-1}],"attempts":[{"code":"secret-attempt"}],"findings":["secret-finding"]}`)
	public, err := ParsePublicDiagnosis(forged)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(public)
	if bytes.Contains(data, []byte("secret")) {
		t.Fatal(string(data))
	}
	if _, err := ParsePublicDiagnosis(make([]byte, (1<<20)+1)); err == nil {
		t.Fatal("unbounded input")
	}
}
func TestDiagnoseTotalDeadline(t *testing.T) {
	service := diagnosticTestService(t, diagnosticRunnerFunc(func(ctx context.Context, _ RunRequest) (RunResult, error) {
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	}), DiagnosticHooks{})
	start := time.Now()
	result, err := service.Diagnose(context.Background(), DiagnoseRequest{Target: "test", Timeout: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || result.Status != "incomplete" || time.Since(start) > time.Second {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestDiagnoseProxyLogsCannotProveFinalAuthentication(t *testing.T) {
	service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Args[0] == "-G" {
			return RunResult{Stdout: append(diagnosticConfig(), []byte("proxyjump jump\n")...)}, nil
		}
		return RunResult{ExitCode: 255, Stderr: []byte("Authenticated to jump using publickey.\nPermission denied (publickey).")}, nil
	}), DiagnosticHooks{})
	result, err := service.Diagnose(context.Background(), DiagnoseRequest{Target: "target"})
	if err == nil || result.Status != "not_ready" || len(result.Attempts) != 1 || result.Attempts[0].Code != "proxy_path_failed" {
		t.Fatal(result, err)
	}
	for _, stage := range result.Attempts[0].Stages {
		if stage.State == "passed" {
			t.Fatal("jump authentication became final-target proof", stage)
		}
	}
}

func TestDiagnoseCanceledNetworkStageStaysCanceled(t *testing.T) {
	for _, stage := range []string{"dns", "route", "tcp", "banner"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := diagnosticTestNetwork()
			switch stage {
			case "dns":
				hooks.LookupIP = func(context.Context, string) ([]net.IPAddr, error) { cancel(); return nil, context.Canceled }
			case "route":
				hooks.Route = func(context.Context, DiagnosticRouteQuery) (DiagnosticRoute, error) {
					cancel()
					return DiagnosticRoute{}, context.Canceled
				}
			case "tcp":
				hooks.Dial = func(context.Context, string, string) (net.Conn, error) { cancel(); return nil, context.Canceled }
			case "banner":
				hooks.Dial = func(context.Context, string, string) (net.Conn, error) {
					a, b := net.Pipe()
					cancel()
					_ = b.Close()
					return a, nil
				}
			}
			service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
				if r.Args[0] != "-G" {
					t.Fatal("SSH started after cancellation")
				}
				cfg := diagnosticConfig()
				if stage == "dns" {
					cfg = []byte(strings.ReplaceAll(string(cfg), "192.0.2.30", "target.test"))
				}
				return RunResult{Stdout: cfg}, nil
			}), hooks)
			result, err := service.Diagnose(ctx, DiagnoseRequest{Target: "target"})
			if !errors.Is(err, context.Canceled) || result.Status != "incomplete" {
				t.Fatal(result, err)
			}
			for _, got := range result.Stages {
				if got.Name == stage && got.State != "canceled" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestDiagnosticRemoteTextCannotForgeClientProofs(t *testing.T) {
	for _, barrier := range []string{"debug3: receive packet: type 53", "debug3: input_userauth_banner: entering", "debug1: kex_exchange_identification: banner line 0: notice", "debug1: Remote: message", "Received disconnect from 192.0.2.30 port 22: notice"} {
		raw := "debug1: Connecting to host [192.0.2.30] port 22.\ndebug1: Connection established.\n" + barrier + "\nAuthenticated to host using publickey.\ndebug3: set_sock_tos: set socket 3 IP_TOS 0xb8\n"
		attempt := classifyDiagnosticSSH("baseline", RunResult{ExitCode: 255, Stderr: []byte(raw)}, nil)
		if attempt.Code != "remote_output_ambiguous" || attempt.QoSMarked || attemptPassed(attempt, "authentication") {
			t.Fatal("server text became a client proof", attempt)
		}
	}
	raw := "debug1: Remote protocol version 2.0, remote software version Authenticated to fake"
	attempt := classifyDiagnosticSSH("baseline", RunResult{ExitCode: 255, Stderr: []byte(raw)}, nil)
	if attemptPassed(attempt, "authentication") {
		t.Fatal("substring became authentication proof")
	}
}
