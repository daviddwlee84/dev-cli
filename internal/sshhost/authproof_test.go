package sshhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedKeyAuthenticationRequiresPrivateMethodEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		log      string
		exit     int
		verified bool
		refused  bool
	}{
		{"modern", "Authenticated to target ([192.0.2.1]:22) using \"publickey\".\n", 0, true, false},
		{"legacy CRLF", "debug1: Authentication succeeded (publickey).\r\n", 0, true, false},
		{"failed auth", "debug1: No more authentication methods to try.\n", 255, false, false},
		{"missing", "", 0, false, true},
		{"none", "Authenticated to target using \"none\".\n", 0, false, true},
		{"legacy none", "debug1: Authentication succeeded (none).\n", 0, false, true},
		{"other", "Authenticated to target using \"password\".\n", 0, false, true},
		{"duplicate", strings.Repeat("Authenticated to target using \"publickey\".\n", 2), 0, false, true},
		{"conflict", "Authenticated to target using \"publickey\".\nAuthenticated to target using \"none\".\n", 0, false, true},
		{"tailscale successful", "debug1: Remote protocol version 2.0, remote software version Tailscale\nAuthenticated to target using \"publickey\".\n", 0, false, true},
		{"tailscale failed", "debug1: Remote protocol version 2.0, remote software version Tailscale\n", 255, false, true},
		{"command failed after auth", "Authenticated to target using \"publickey\".\n", 255, false, true},
		{"remote banner", "debug1: Remote: Authenticated to target using \"publickey\".\n", 0, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			verified, err := selectedKeyAuthentication([]byte(test.log), test.exit)
			if verified != test.verified || errors.Is(err, ErrUnprovenAuthentication) != test.refused {
				t.Fatalf("verified=%v err=%v", verified, err)
			}
		})
	}
}

func TestExactProofIgnoresStderrAndCleansPrivateLog(t *testing.T) {
	for _, test := range []struct {
		name    string
		log     string
		exit    int
		refused bool
	}{
		{"success", "Authenticated to target using \"publickey\".\n", 0, false},
		{"stderr spoof", "", 0, true},
		{"keyless", "Authenticated to target using \"none\".\n", 0, true},
		{"oversized", strings.Repeat("x", maxConfigBytes+1), 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{{proofLog: &test.log, result: RunResult{ExitCode: test.exit, Stderr: []byte("Authenticated to target using \"publickey\".\n")}}}}
			service, paths := bootstrapService(t, runner)
			route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
			identity := filepath.Join(paths.SSHDir, "key.pub")
			verified, err := service.runSSHProof(context.Background(), route.state.hops[0], keySelector{identity: identity}, true)
			if errors.Is(err, ErrUnprovenAuthentication) != test.refused || verified == test.refused {
				t.Fatalf("verified=%v err=%v", verified, err)
			}
			request := runner.requests[0]
			if !hasArgPair(request.Args, "-o", "LogLevel=DEBUG1") || hasArg(request.Args, "-v") {
				t.Fatalf("logging args=%v", request.Args)
			}
			for i, arg := range request.Args {
				if arg == "-E" || arg == "-F" {
					if _, err := os.Lstat(request.Args[i+1]); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("private file retained: %v", err)
					}
				}
			}
		})
	}
}

func TestBootstrapRefusesUnprovenKeyWithoutInstalling(t *testing.T) {
	for _, log := range []string{"", "Authenticated to target using \"none\".\n", "debug1: Remote protocol version 2.0, remote software version Tailscale\n"} {
		runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{successRun(), {proofLog: &log}}}
		service, _ := bootstrapService(t, runner)
		candidate := bindTestCandidate(t, service, testPublicLine(0x77, "unproven"))
		route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
		result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "target", Route: route, Candidate: candidate})
		if err != nil || result.Ready || result.FleetReady || len(runner.requests) != 2 || result.Hops[0].Code != "selected_key_authentication_unproven" || result.Hops[0].Installed || result.Hops[0].Verified {
			t.Fatalf("result=%+v err=%v requests=%d", result, err, len(runner.requests))
		}
	}
}

func TestBootstrapKeylessWorkingJumpRemainsSkippable(t *testing.T) {
	log := "Authenticated to jump using \"none\".\n"
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{successRun(), {proofLog: &log}, successRun(), successRun(), successRun(), successRun()}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x78, "keyless-jump"))
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "jump", RemoteOS: RemoteOSPOSIX}, {Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "target", Route: route, Candidate: candidate})
	if err != nil || !result.Ready || result.Hops[0].Status != HopWorkingSkipped || result.Hops[0].Verified || !result.Hops[0].OrdinaryReady {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExactProofCancellationCleansPrivateFiles(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{{err: context.Canceled}}}
	service, paths := bootstrapService(t, runner)
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	_, err := service.runSSHProof(context.Background(), route.state.hops[0], keySelector{identity: filepath.Join(paths.SSHDir, "key.pub")}, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	for i, argument := range runner.requests[0].Args {
		if argument == "-E" || argument == "-F" {
			if _, err := os.Lstat(runner.requests[0].Args[i+1]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private file remained after cancellation: %v", err)
			}
		}
	}
}

func TestBootstrapKeylessJumpExplicitInstallationIsRefused(t *testing.T) {
	log := "debug1: Remote protocol version 2.0, remote software version Tailscale\n"
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{successRun(), {proofLog: &log, result: RunResult{ExitCode: 255}}}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x79, "keyless-jump"))
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "jump", RemoteOS: RemoteOSPOSIX}, {Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "target", Route: route, Candidate: candidate, InstallOnWorkingJump: true})
	if err != nil || result.Ready || result.Hops[0].Status != HopManual || result.Hops[0].Installed || result.Hops[1].Status != HopNotAttempted || len(runner.requests) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
