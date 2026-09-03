package sshhost

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type routeEffective struct {
	hostName     string
	user         string
	port         int
	proxyJump    string
	proxyCommand string
}

type routeRunner struct {
	effective map[string]routeEffective
	requests  []RunRequest
}

func (r *routeRunner) Run(_ context.Context, request RunRequest) (RunResult, error) {
	r.requests = append(r.requests, request)
	if request.Name != "ssh" || len(request.Args) < 2 || request.Args[0] != "-G" {
		return RunResult{}, fmt.Errorf("unexpected route request %#v", request)
	}
	alias := request.Args[len(request.Args)-1]
	value, ok := r.effective[alias]
	if !ok {
		return RunResult{ExitCode: 255}, nil
	}
	for index := 1; index+1 < len(request.Args)-1; index += 2 {
		switch request.Args[index] {
		case "-l":
			value.user = request.Args[index+1]
		case "-p":
			port, err := strconv.Atoi(request.Args[index+1])
			if err != nil {
				return RunResult{}, err
			}
			value.port = port
		default:
			return RunResult{}, fmt.Errorf("unexpected route option %#v", request.Args[index:index+2])
		}
	}
	var output strings.Builder
	if value.hostName == "" {
		value.hostName = alias
	}
	fmt.Fprintf(&output, "hostname %s\n", value.hostName)
	if value.user != "" {
		fmt.Fprintf(&output, "user %s\n", value.user)
	}
	if value.port != 0 {
		fmt.Fprintf(&output, "port %d\n", value.port)
	}
	if value.proxyJump != "" {
		fmt.Fprintf(&output, "proxyjump %s\n", value.proxyJump)
	}
	if value.proxyCommand == "" {
		value.proxyCommand = "none"
	}
	fmt.Fprintf(&output, "proxycommand %s\n", value.proxyCommand)
	return RunResult{Stdout: []byte(output.String())}, nil
}

func newRouteService(t *testing.T, runner Runner) *Service {
	t.Helper()
	service, err := NewService(fixturePaths(t), runner)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestResolveRouteFlattensNestedCommaAndExplicitFormsOutermostFirst(t *testing.T) {
	runner := &routeRunner{effective: map[string]routeEffective{
		"target":      {hostName: "target.example", user: "target-user", port: 22, proxyJump: "edge,user@mid:2200,[2001:db8::1]:2222"},
		"edge":        {hostName: "edge.example", user: "edge-user", port: 22, proxyJump: "outer"},
		"outer":       {hostName: "outer.example", user: "outer-user", port: 22},
		"mid":         {hostName: "mid.example", user: "ignored-user", port: 2022},
		"2001:db8::1": {hostName: "2001:db8::1", user: "ipv6-user", port: 22},
	}}
	service := newRouteService(t, runner)
	route, err := service.ResolveRoute(context.Background(), RouteRequest{
		Alias: "target", TargetRemoteOS: RemoteOSWindows,
		OSOverrides: []RemoteOSOverride{
			{Alias: "outer", RemoteOS: RemoteOSPOSIX},
			{Alias: "mid", RemoteOS: RemoteOSWindows},
			{Alias: "2001:db8::1", RemoteOS: RemoteOSPOSIX},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var aliases []string
	for _, hop := range route.Hops {
		aliases = append(aliases, hop.Alias)
	}
	wantAliases := []string{"outer", "edge", "mid", "2001:db8::1", "target"}
	if !reflect.DeepEqual(aliases, wantAliases) {
		t.Fatalf("route aliases = %v, want %v", aliases, wantAliases)
	}
	if route.Hops[1].RemoteOS != RemoteOSUnknown {
		t.Fatalf("target OS leaked onto edge: %#v", route.Hops[1])
	}
	if route.Hops[2].User != "user" || route.Hops[2].Port != 2200 || route.Hops[2].Reference != "user@mid:2200" {
		t.Fatalf("explicit user/port hop = %#v", route.Hops[2])
	}
	if route.Hops[3].Port != 2222 || route.Hops[3].Reference != "[2001:db8::1]:2222" {
		t.Fatalf("IPv6 hop = %#v", route.Hops[3])
	}
	wantPrefixes := []string{"", "outer", "outer,edge", "outer,edge,user@mid:2200", ""}
	for index, want := range wantPrefixes {
		if route.state.hops[index].proxyJump != want {
			t.Errorf("hop %d ProxyJump prefix = %q, want %q", index, route.state.hops[index].proxyJump, want)
		}
	}
	if !route.Hops[len(route.Hops)-1].Target || route.TargetRemoteOS != RemoteOSWindows || route.Hops[len(route.Hops)-1].RemoteOS != RemoteOSWindows {
		t.Fatalf("target = %#v, route OS %q", route.Hops[len(route.Hops)-1], route.TargetRemoteOS)
	}
	for _, hop := range route.Hops {
		if hop.AdminState != AdminUnknown {
			t.Fatalf("route guessed admin state: %#v", hop)
		}
	}
	var midRequest, ipv6Request RunRequest
	for _, request := range runner.requests {
		switch request.Args[len(request.Args)-1] {
		case "mid":
			midRequest = request
		case "2001:db8::1":
			ipv6Request = request
		}
	}
	if !hasArgPair(midRequest.Args, "-l", "user") || !hasArgPair(midRequest.Args, "-p", "2200") ||
		hasArgPair(midRequest.Args, "-p", "2022") {
		t.Fatalf("explicit mid evaluation = %#v", midRequest.Args)
	}
	if !hasArgPair(ipv6Request.Args, "-p", "2222") {
		t.Fatalf("explicit IPv6 evaluation = %#v", ipv6Request.Args)
	}
}

func TestBootstrapRejectsStaleResolvedRouteBeforeNetworkProbe(t *testing.T) {
	runner := &routeRunner{effective: map[string]routeEffective{"target": {hostName: "one.example"}}}
	service := newRouteService(t, runner)
	route, err := service.ResolveRoute(context.Background(), RouteRequest{Alias: "target", TargetRemoteOS: RemoteOSPOSIX})
	if err != nil {
		t.Fatal(err)
	}
	candidate := bindTestCandidate(t, service, testPublicLine(0x74, "route-state"))
	runner.effective["target"] = routeEffective{hostName: "two.example"}
	if _, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Candidate: candidate,
	}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Bootstrap error = %v, want source changed", err)
	}
	for _, request := range runner.requests {
		if len(request.Args) == 0 || request.Args[0] != "-G" {
			t.Fatalf("stale route reached network proof: %#v", runner.requests)
		}
	}
}

func TestResolveRouteRejectsCyclesDepthProxyCommandAndAmbiguousOverrides(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		runner := &routeRunner{effective: map[string]routeEffective{
			"target": {proxyJump: "jump"},
			"jump":   {proxyJump: "target"},
		}}
		service := newRouteService(t, runner)
		if _, err := service.ResolveRoute(context.Background(), RouteRequest{Alias: "target"}); err == nil || !strings.Contains(err.Error(), ErrUnsupportedRoute.Error()) {
			t.Fatalf("cycle error = %v", err)
		}
	})

	t.Run("depth", func(t *testing.T) {
		effective := make(map[string]routeEffective)
		for index := 0; index < 5; index++ {
			name := "hop" + strconv.Itoa(index)
			next := ""
			if index < 4 {
				next = "hop" + strconv.Itoa(index+1)
			}
			effective[name] = routeEffective{proxyJump: next}
		}
		service := newRouteService(t, &routeRunner{effective: effective})
		if _, err := service.ResolveRoute(context.Background(), RouteRequest{Alias: "hop0", MaxDepth: 3}); err == nil {
			t.Fatal("depth-limited route succeeded")
		}
	})

	t.Run("ProxyCommand", func(t *testing.T) {
		service := newRouteService(t, &routeRunner{effective: map[string]routeEffective{
			"target": {proxyCommand: "custom-command"},
		}})
		if _, err := service.ResolveRoute(context.Background(), RouteRequest{Alias: "target"}); err == nil || !strings.Contains(err.Error(), "ProxyCommand") {
			t.Fatalf("ProxyCommand error = %v", err)
		}
	})

	t.Run("ambiguous overrides", func(t *testing.T) {
		runner := &routeRunner{effective: map[string]routeEffective{"target": {}}}
		service := newRouteService(t, runner)
		_, err := service.ResolveRoute(context.Background(), RouteRequest{
			Alias: "target",
			OSOverrides: []RemoteOSOverride{
				{Alias: "Target", RemoteOS: RemoteOSPOSIX},
				{Alias: "target", RemoteOS: RemoteOSWindows},
			},
		})
		if err == nil || len(runner.requests) != 0 {
			t.Fatalf("ambiguous override error = %v, requests = %#v", err, runner.requests)
		}
	})

	t.Run("unused override", func(t *testing.T) {
		service := newRouteService(t, &routeRunner{effective: map[string]routeEffective{"target": {}}})
		if _, err := service.ResolveRoute(context.Background(), RouteRequest{
			Alias: "target", OSOverrides: []RemoteOSOverride{{Alias: "missing", RemoteOS: RemoteOSPOSIX}},
		}); err == nil {
			t.Fatal("unused override succeeded")
		}
	})
}

func TestParseProxyJumpAcceptsDocumentedFormsAndRejectsUnsupportedTokens(t *testing.T) {
	valid := map[string]jumpSpec{
		"alias":                   {host: "alias"},
		"user@alias":              {host: "alias", user: "user"},
		"alias:2200":              {host: "alias", port: 2200},
		"user@alias:2200":         {host: "alias", user: "user", port: 2200},
		"[2001:db8::1]":           {host: "2001:db8::1"},
		"user@[2001:db8::1]:2200": {host: "2001:db8::1", user: "user", port: 2200},
	}
	for input, want := range valid {
		got, err := parseJumpSpec(input)
		if err != nil {
			t.Errorf("parseJumpSpec(%q): %v", input, err)
			continue
		}
		want.reference = input
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseJumpSpec(%q) = %#v, want %#v", input, got, want)
		}
	}
	for _, input := range []string{
		"ssh://host", "host/path", "%h", "host:notaport", "2001:db8::1", "user@@host", "[not-ipv6]", "none,host", "host,,two",
	} {
		if _, err := parseProxyJump(input); err == nil {
			t.Errorf("parseProxyJump(%q) succeeded", input)
		}
	}
}
