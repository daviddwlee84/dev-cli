package sshhost

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapUsesPerHopPublicKeys(t *testing.T) {
	jumpLine, targetLine := testPublicLine(0xb1, "jump"), testPublicLine(0xb2, "target")
	install := func(want []byte) scriptedRun {
		return scriptedRun{call: func(_ context.Context, request RunRequest) (RunResult, error) {
			if string(request.Stdin) != string(want)+"\n" {
				t.Fatalf("wrong per-hop public key: %q", request.Stdin)
			}
			return RunResult{}, nil
		}}
	}
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		failedRun(), failedRun(), install(jumpLine), successRun(), successRun(),
		failedRun(), failedRun(), install(targetLine), successRun(), successRun(),
	}}
	s, _ := bootstrapService(t, runner)
	route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "jump", RemoteOS: RemoteOSPOSIX}, {Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Route: route, HopKeys: map[string]KeyResult{
		"jump": {Candidate: bindTestCandidate(t, s, jumpLine)}, "target": {Candidate: bindTestCandidate(t, s, targetLine)},
	}})
	if err != nil || !result.Ready || len(runner.responses) != 0 {
		t.Fatal(result, err)
	}
}

func TestBootstrapAllowsMissingKeyOnlyForWorkingJump(t *testing.T) {
	for _, working := range []bool{true, false} {
		t.Run(map[bool]string{true: "working", false: "missing"}[working], func(t *testing.T) {
			responses := []scriptedRun{failedRun()}
			if working {
				responses = []scriptedRun{successRun(), successRun(), successRun(), successRun(), successRun()}
			}
			runner := &scriptedBootstrapRunner{t: t, responses: responses}
			s, _ := bootstrapService(t, runner)
			route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "jump", RemoteOS: RemoteOSPOSIX}, {Alias: "target", RemoteOS: RemoteOSPOSIX}})
			result, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Route: route, HopKeys: map[string]KeyResult{"target": {Candidate: bindTestCandidate(t, s, testPublicLine(0xb3, "target"))}}})
			if err != nil || result.Ready != working {
				t.Fatal(result, err)
			}
			if working && !result.Hops[0].Skipped || !working && result.Hops[0].Code != "selected_key_required" {
				t.Fatal(result)
			}
		})
	}
}

func TestBootstrapRejectsUnknownOrDuplicateHopKeyBeforeNetwork(t *testing.T) {
	for _, aliases := range [][]string{{"unknown", "target"}, {"target", "TARGET"}} {
		s, _ := bootstrapService(t, panicRunner{})
		candidate := bindTestCandidate(t, s, testPublicLine(0xb4, "target"))
		keys := map[string]KeyResult{}
		for _, alias := range aliases {
			keys[alias] = KeyResult{Candidate: candidate}
		}
		route := bindTestRoute(t, s, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
		if _, err := s.Bootstrap(t.Context(), BootstrapRequest{Alias: "target", Route: route, HopKeys: keys}); err == nil {
			t.Fatal("unmatched/duplicate key accepted")
		}
	}
}

func TestExactProofKeepsRepeatedAliasProfilesDistinct(t *testing.T) {
	route := []proofRouteHop{
		{alias: "jump", reference: "one@jump:22", hostName: "jump.example", user: "one", port: 22, effective: testProofEffective("jump")},
		{alias: "jump", reference: "two@jump:2200", hostName: "jump.example", user: "two", port: 2200, effective: testProofEffective("jump")},
		{alias: "target", hostName: "target.example", port: 22, effective: testProofEffective("target")},
	}
	content, _, err := renderExactProofConfig(route, keySelector{identity: filepath.Join(t.TempDir(), "synthetic-key")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Host dev-cli-route-hop-1", "Host dev-cli-route-hop-2", "ProxyJump dev-cli-route-hop-1,dev-cli-route-hop-2", "User one", "User two", "Port 2200"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("missing %q in private route", want)
		}
	}
}
