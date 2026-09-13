package sshhost

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPingFailureDoesNotGateSSHAndNetworkOnlyDoesNotAuthenticate(t *testing.T) {
	for _, networkOnly := range []bool{false, true} {
		calls := 0
		hooks := diagnosticTestNetwork()
		hooks.Ping = func(context.Context, DiagnosticRouteQuery) (DiagnosticStage, error) {
			return DiagnosticStage{State: "unknown", Code: "ping_no_reply"}, context.DeadlineExceeded
		}
		service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
			calls++
			if r.Args[0] == "-G" {
				return RunResult{Stdout: diagnosticConfig()}, nil
			}
			return RunResult{}, nil
		}), hooks)
		d, err := service.Diagnose(t.Context(), DiagnoseRequest{Target: "alias", Ping: true, NetworkOnly: networkOnly})
		if err != nil {
			t.Fatal(err)
		}
		wantCalls, wantStatus := 2, "ready"
		if networkOnly {
			wantCalls, wantStatus = 1, "network_ready"
		}
		if calls != wantCalls || d.Status != wantStatus {
			t.Fatalf("calls %d status %s", calls, d.Status)
		}
		found := false
		for _, stage := range d.Stages {
			if stage.Name == "ping" {
				found = true
				if stage.State != "unknown" {
					t.Fatal(stage)
				}
			}
		}
		if !found {
			t.Fatal("missing ping")
		}
	}
}

func TestProxyNetworkCheckDoesNotBypassRoute(t *testing.T) {
	hooks := diagnosticTestNetwork()
	hooks.Ping = func(context.Context, DiagnosticRouteQuery) (DiagnosticStage, error) {
		t.Fatal("proxy pinged directly")
		return DiagnosticStage{}, nil
	}
	service := diagnosticTestService(t, diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
		if r.Args[0] != "-G" {
			t.Fatal("network-only authenticated")
		}
		return RunResult{Stdout: append(diagnosticConfig(), []byte("proxyjump bastion\n")...)}, nil
	}), hooks)
	d, err := service.Diagnose(t.Context(), DiagnoseRequest{Target: "alias", Ping: true, NetworkOnly: true})
	if err == nil || len(d.Attempts) != 0 {
		t.Fatal("proxy was treated as ready")
	}
	for _, stage := range d.Stages {
		if stage.Name == "ping" && (stage.State != "skipped" || stage.Code != "proxy_path") {
			t.Fatal(stage)
		}
	}
}

func TestPingArgumentsAndRoundTripClassification(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, address := range []string{"192.0.2.4", "2001:db8::4"} {
			runner := diagnosticRunnerFunc(func(_ context.Context, r RunRequest) (RunResult, error) {
				if goos == "windows" {
					if !strings.Contains(string(r.Stdin), address) {
						t.Fatal("missing structured Windows endpoint")
					}
					return RunResult{Stdout: []byte(`{"success":true,"round_trip_ms":1}`)}, nil
				}
				if r.Args[len(r.Args)-1] != address || strings.Contains(strings.Join(r.Args, " "), "sh -c") {
					t.Fatal(r.Args)
				}
				return RunResult{Stdout: []byte("reply time<1ms")}, nil
			})
			stage, err := collectDiagnosticPing(t.Context(), runner, goos, DiagnosticRouteQuery{Address: address})
			if err != nil || stage.State != "passed" || stage.RoundTripMS == nil || *stage.RoundTripMS != 1 || (goos != "windows" && !stage.RoundTripUpperBound) {
				t.Fatalf("%+v %v", stage, err)
			}
		}
	}
	stage, err := collectDiagnosticPing(t.Context(), diagnosticRunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		return RunResult{Stdout: []byte("Destination host unreachable.")}, nil
	}), "windows", DiagnosticRouteQuery{Address: "192.0.2.4"})
	if err != nil || stage.State == "passed" {
		t.Fatal("Windows zero status inferred a reply")
	}
	_, err = collectDiagnosticPing(t.Context(), nil, "linux", DiagnosticRouteQuery{Address: "host; command"})
	if err == nil {
		t.Fatal("nonliteral endpoint accepted")
	}
}

func TestDiagnoseRejectsNetworkOnlyQoSBeforeExecution(t *testing.T) {
	service := diagnosticTestService(t, diagnosticRunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		t.Fatal("executed invalid request")
		return RunResult{}, errors.New("unexpected")
	}), DiagnosticHooks{})
	if _, err := service.Diagnose(t.Context(), DiagnoseRequest{Target: "alias", NetworkOnly: true, CompareQoS: true}); err == nil {
		t.Fatal("accepted incompatible modes")
	}
}
