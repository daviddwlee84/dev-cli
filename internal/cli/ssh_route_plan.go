package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

// Proposed routes have no execution authority. This guard runs before the
// Include, key generation, or fragment writes, including config-only setup.
func planSSHLocalRoute(ctx context.Context, s *sshhost.Service, alias string, definitions []sshhost.ManagedDefinition, readOnly bool, targetOverrides ...sshhost.RouteInvocation) (sshhost.Route, error) {
	inv, err := s.Discover(ctx)
	if err != nil {
		return sshhost.Route{}, err
	}
	proposed := map[string]sshhost.ManagedDefinition{}
	for _, d := range definitions {
		key := strings.ToLower(d.Alias)
		if previous, ok := proposed[key]; ok && !sameSSHDefinition(previous, d) {
			return sshhost.Route{}, fmt.Errorf("conflicting proposed SSH alias %q", d.Alias)
		}
		proposed[key] = d
	}
	firstInvocation := true
	resolver := func(ctx context.Context, invocation sshhost.RouteInvocation) (sshhost.EffectiveConfig, error) {
		if firstInvocation && len(targetOverrides) > 0 {
			if invocation.User == "" {
				invocation.User = targetOverrides[0].User
			}
			if invocation.Port == 0 {
				invocation.Port = targetOverrides[0].Port
			}
		}
		firstInvocation = false
		key := strings.ToLower(invocation.Alias)
		d, changed := proposed[key]
		var effective sshhost.EffectiveConfig
		var e error
		if !readOnly {
			native := invocation
			if changed {
				if native.User == "" {
					native.User = d.User
				}
				if native.Port == 0 {
					native.Port = d.Port
				}
			}
			effective, e = s.ResolveInvocation(ctx, native)
			if e != nil {
				return effective, e
			}
		} else {
			effective = sshhost.EffectiveConfig{Alias: invocation.Alias, HostName: invocation.Alias, User: os.Getenv("USER"), Port: 22}
			if found, ok := inv.Find(invocation.Alias); ok {
				if aliasOwnership(found) == "managed" {
					managed, err := s.InspectManaged(found.Name)
					if err != nil {
						return effective, err
					}
					effective = effectiveSSHDefinition(managed.Definition)
				} else {
					known := false
					for _, hint := range inv.ConnectionHints {
						if strings.EqualFold(hint.Alias, invocation.Alias) && hint.State == "known" {
							effective.HostName, effective.User, effective.Port = hint.HostName, hint.User, hint.Port
							known = true
						}
					}
					if !known {
						return effective, fmt.Errorf("route for %q is unresolved in a read-only preview; explicit setup must evaluate native SSH first", invocation.Alias)
					}
				}
			}
		}
		if changed {
			effective.HostName = d.HostName
			if d.User != "" {
				effective.User = d.User
			}
			if d.Port != 0 {
				effective.Port = d.Port
			}
			if d.ProxyJump != "" {
				effective.ProxyJump = d.ProxyJump
			}
		}
		effective.Alias = invocation.Alias
		if invocation.User != "" {
			effective.User = invocation.User
		}
		if invocation.Port != 0 {
			effective.Port = invocation.Port
		}
		if invocation.OverrideProxyJump {
			effective.ProxyJump = invocation.ProxyJump
		}
		if effective.User == "" {
			effective.User = os.Getenv("USER")
		}
		if effective.Port == 0 {
			effective.Port = 22
		}
		return effective, nil
	}
	return sshhost.ResolvePlannedRoute(ctx, sshhost.RouteRequest{Alias: alias}, resolver)
}
func effectiveSSHDefinition(d sshhost.ManagedDefinition) sshhost.EffectiveConfig {
	return sshhost.EffectiveConfig{Alias: d.Alias, HostName: d.HostName, User: d.User, Port: d.Port, ProxyJump: d.ProxyJump}
}
func sameSSHDefinition(a, b sshhost.ManagedDefinition) bool {
	left, le := sshhost.RenderManaged(a)
	right, re := sshhost.RenderManaged(b)
	return le == nil && re == nil && string(left) == string(right)
}
func onboardDefinitions(items []sshOnboardItem) []sshhost.ManagedDefinition {
	var definitions []sshhost.ManagedDefinition
	for _, item := range items {
		if item.Import != nil {
			definitions = append(definitions, item.Import.Definitions...)
		} else if item.Definition != nil {
			definitions = append(definitions, *item.Definition)
		}
	}
	return definitions
}

func sameSSHRouteInvocations(left, right []sshhost.RouteHop) bool {
	if len(left) != len(right) {
		return false
	}
	for i, a := range left {
		b := right[i]
		if !strings.EqualFold(a.Alias, b.Alias) || a.Reference != b.Reference || a.HostName != b.HostName || a.User != b.User || a.Port != b.Port {
			return false
		}
	}
	return true
}
