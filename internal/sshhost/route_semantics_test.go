package sshhost

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func plannedRouteFixture(configs map[string]EffectiveConfig, seen *[]RouteInvocation) EffectiveResolver {
	return func(_ context.Context, invocation RouteInvocation) (EffectiveConfig, error) {
		if seen != nil {
			*seen = append(*seen, invocation)
		}
		config, ok := configs[invocation.Alias]
		if !ok {
			return EffectiveConfig{}, fmt.Errorf("missing fixture alias %s", invocation.Alias)
		}
		config = cloneEffective(config)
		config.Alias = invocation.Alias
		if config.HostName == "" {
			config.HostName = invocation.Alias
		}
		return config, nil
	}
}

func TestPlannedRouteCommaPrefixOverridesInnerConfiguredJump(t *testing.T) {
	configs := map[string]EffectiveConfig{
		"target": {ProxyJump: "edge,operator@middle:2200"},
		"middle": {ProxyJump: "must-not-resolve", User: "other", Port: 22},
		"edge":   {ProxyJump: "outer"},
		"outer":  {},
	}
	var seen []RouteInvocation
	route, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target"}, plannedRouteFixture(configs, &seen))
	if err != nil {
		t.Fatal(err)
	}
	var aliases []string
	for _, hop := range route.Hops {
		aliases = append(aliases, hop.Alias)
	}
	if !reflect.DeepEqual(aliases, []string{"outer", "edge", "middle", "target"}) {
		t.Fatal(aliases)
	}
	if !seen[1].OverrideProxyJump || seen[1].ProxyJump != "edge" || seen[1].User != "operator" || seen[1].Port != 2200 {
		t.Fatalf("inner invocation lost explicit prefix: %+v", seen)
	}
	if route.state != nil {
		t.Fatal("pure route acquired execution authority")
	}
}

func TestPlannedRouteFiniteRepeatedInvocationsAndTrueCycles(t *testing.T) {
	for _, test := range []struct {
		name    string
		configs map[string]EffectiveConfig
		want    []string
		cycle   bool
	}{
		{"different explicit users", map[string]EffectiveConfig{"target": {ProxyJump: "one@jump,two@jump:2200"}, "jump": {}}, []string{"jump", "jump", "target"}, false},
		{"inherited cycle", map[string]EffectiveConfig{"target": {ProxyJump: "jump"}, "jump": {ProxyJump: "target"}}, nil, true},
		{"cycle inside shrinking prefix", map[string]EffectiveConfig{"target": {ProxyJump: "target,jump"}, "jump": {}}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			route, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target"}, plannedRouteFixture(test.configs, nil))
			if test.cycle {
				if !errors.Is(err, ErrUnsupportedRoute) || !strings.Contains(err.Error(), "cycle") {
					t.Fatalf("cycle accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var aliases []string
			for _, hop := range route.Hops {
				aliases = append(aliases, hop.Alias)
			}
			if !reflect.DeepEqual(aliases, test.want) {
				t.Fatal(aliases)
			}
		})
	}
}

func TestPlannedRouteBoundsExplicitListsAndRequiresMatchingResolver(t *testing.T) {
	configs := map[string]EffectiveConfig{"target": {ProxyJump: strings.Repeat("jump,", 16) + "jump"}, "jump": {}}
	if _, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target"}, plannedRouteFixture(configs, nil)); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("unbounded explicit route: %v", err)
	}
	if _, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target", MaxDepth: 65}, plannedRouteFixture(configs, nil)); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("hard limit ignored: %v", err)
	}
	if _, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target"}, func(context.Context, RouteInvocation) (EffectiveConfig, error) {
		return EffectiveConfig{Alias: "different"}, nil
	}); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("resolver alias substitution accepted: %v", err)
	}
}

func TestPlannedRouteRejectsIdenticalFiniteRepetitionAfterResolution(t *testing.T) {
	configs := map[string]EffectiveConfig{"target": {ProxyJump: "jump,jump,jump"}, "jump": {}}
	_, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target"}, plannedRouteFixture(configs, nil))
	if !errors.Is(err, ErrUnsupportedRoute) || !strings.Contains(err.Error(), "repeated_invocation") {
		t.Fatalf("identical finite repetition accepted: %v", err)
	}
	configs["target"] = EffectiveConfig{ProxyJump: "one@jump,two@jump:2200"}
	route, err := ResolvePlannedRoute(t.Context(), RouteRequest{Alias: "target"}, plannedRouteFixture(configs, nil))
	if err != nil || len(route.Diagnostics) != 1 || route.Diagnostics[0].Code != "machine_revisited" {
		t.Fatalf("distinct profiles were merged or diagnostic lost: %+v err=%v", route, err)
	}
}
