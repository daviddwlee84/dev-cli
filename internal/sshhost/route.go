package sshhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

const defaultMaxRouteDepth = 16

// RemoteOS selects the fixed remote installer contract for one hop.
type RemoteOS string

const (
	RemoteOSUnknown RemoteOS = "unknown"
	RemoteOSPOSIX   RemoteOS = "posix"
	RemoteOSWindows RemoteOS = "windows"
)

// AdminState records Windows administrator-group membership for one hop.
type AdminState string

const (
	AdminUnknown       AdminState = "unknown"
	AdminStandard      AdminState = "standard"
	AdminAdministrator AdminState = "administrator"
)

// RemoteOSOverride assigns an OS to exactly one route alias. Overrides are
// case-insensitive and duplicate entries are rejected as ambiguous.
type RemoteOSOverride struct {
	Alias    string   `json:"alias"`
	RemoteOS RemoteOS `json:"remote_os"`
}

// RouteRequest asks the service to resolve the target's plain effective
// ProxyJump graph. TargetRemoteOS applies only to Alias; jump hosts remain
// unknown unless explicitly overridden.
type RouteRequest struct {
	Alias          string             `json:"alias"`
	TargetRemoteOS RemoteOS           `json:"target_remote_os,omitempty"`
	OSOverrides    []RemoteOSOverride `json:"os_overrides,omitempty"`
	MaxDepth       int                `json:"max_depth,omitempty"`
}

// RouteHop is a content-safe, outermost-first route element. Reference retains
// an explicit ProxyJump user/port spelling while Alias is its config host name.
type RouteHop struct {
	Alias      string     `json:"alias"`
	Reference  string     `json:"reference"`
	HostName   string     `json:"host_name,omitempty"`
	User       string     `json:"user,omitempty"`
	Port       int        `json:"port,omitempty"`
	RemoteOS   RemoteOS   `json:"remote_os"`
	AdminState AdminState `json:"admin_state"`
	Target     bool       `json:"target,omitempty"`
}

// Route is ordered outermost-first and ends with the requested target. Its
// unexported state binds exact execution destinations to this Service.
type Route struct {
	Alias          string       `json:"alias"`
	Hops           []RouteHop   `json:"hops"`
	TargetRemoteOS RemoteOS     `json:"target_remote_os"`
	Diagnostics    []Diagnostic `json:"diagnostics,omitempty"`
	state          *routeState
}

type routeState struct {
	serviceID  uint64
	safe       Route
	hops       []routeHopState
	request    RouteRequest
	revalidate bool
}

type routeHopState struct {
	safe         RouteHop
	destination  string
	explicitUser string
	explicitPort int
	proxyJump    string
	effective    EffectiveConfig
	proofRoute   []proofRouteHop
}

type proofRouteHop struct {
	alias     string
	reference string
	hostName  string
	user      string
	port      int
	effective EffectiveConfig
}

type jumpSpec struct {
	reference string
	host      string
	user      string
	port      int
}

// RouteInvocation is one local OpenSSH invocation. An explicit comma-list
// prefix overrides the hop's configured ProxyJump, just like ssh -J.
type RouteInvocation struct {
	Alias             string `json:"alias"`
	User              string `json:"user,omitempty"`
	Port              int    `json:"port,omitempty"`
	ProxyJump         string `json:"proxy_jump,omitempty"`
	OverrideProxyJump bool   `json:"override_proxy_jump,omitempty"`
}

// EffectiveResolver supplies reviewed/planned effective configuration. The pure
// resolver never invokes OpenSSH or reads files itself. Callers decide whether
// their resolver uses static snapshots or explicit native -G evaluation.
type EffectiveResolver func(context.Context, RouteInvocation) (EffectiveConfig, error)

func (s *Service) effectiveRouteInvocation(ctx context.Context, invocation RouteInvocation) (EffectiveConfig, error) {
	if invocation.User == "" && invocation.Port == 0 && !invocation.OverrideProxyJump {
		return s.Effective(ctx, invocation.Alias)
	}
	args := []string{"-G"}
	if invocation.User != "" {
		args = append(args, "-l", invocation.User)
	}
	if invocation.Port != 0 {
		args = append(args, "-p", strconv.Itoa(invocation.Port))
	}
	if invocation.OverrideProxyJump {
		args = append(args, "-J", invocation.ProxyJump)
	}
	args = append(args, invocation.Alias)
	return s.evaluateSSHConfig(ctx, invocation.Alias, args, "ssh -G explicit route hop", "evaluate explicit SSH route hop", false)
}

// ResolveInvocation evaluates one invocation with native ssh -G, including
// explicit ProxyJump/user/port overrides. It is never a static discovery read.
func (s *Service) ResolveInvocation(ctx context.Context, invocation RouteInvocation) (EffectiveConfig, error) {
	if err := validateRouteLookupAlias(invocation.Alias); err != nil {
		return EffectiveConfig{}, err
	}
	if invocation.User != "" && !validJumpUser(invocation.User) || invocation.Port < 0 || invocation.Port > 65535 {
		return EffectiveConfig{}, ErrUnsupportedRoute
	}
	if invocation.OverrideProxyJump {
		if _, err := parseProxyJump(invocation.ProxyJump); err != nil {
			return EffectiveConfig{}, err
		}
	}
	return s.effectiveRouteInvocation(ctx, invocation)
}

// ResolvePlannedRoute checks a proposed route without creating execution
// authority. Its result cannot be passed to Bootstrap as a resolved Route.
func ResolvePlannedRoute(ctx context.Context, request RouteRequest, resolver EffectiveResolver) (Route, error) {
	if resolver == nil {
		return Route{}, errors.New("route effective resolver is required")
	}
	return resolveRoute(ctx, request, resolver, 0, false)
}

// ResolveRoute explicitly invokes native ssh -G for each scoped invocation.
// Its result is service-bound and freshly revalidated before Bootstrap.
func (s *Service) ResolveRoute(ctx context.Context, request RouteRequest) (Route, error) {
	return resolveRoute(ctx, request, s.effectiveRouteInvocation, s.id, true)
}

func resolveRoute(ctx context.Context, request RouteRequest, resolver EffectiveResolver, serviceID uint64, revalidate bool) (Route, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRouteLookupAlias(request.Alias); err != nil {
		return Route{}, err
	}
	maxDepth := request.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxRouteDepth
	}
	if maxDepth > 64 {
		return Route{}, fmt.Errorf("route depth %d exceeds hard limit 64: %w", maxDepth, ErrUnsupportedRoute)
	}
	targetOS, err := normalizeRemoteOS(request.TargetRemoteOS)
	if err != nil {
		return Route{}, err
	}
	overrides := make(map[string]RemoteOS)
	overrideNames := make(map[string]string)
	usedOverrides := make(map[string]bool)
	for _, override := range request.OSOverrides {
		if err := validateRouteLookupAlias(override.Alias); err != nil {
			return Route{}, fmt.Errorf("invalid route OS override: %w", err)
		}
		remoteOS, err := normalizeRemoteOS(override.RemoteOS)
		if err != nil || remoteOS == RemoteOSUnknown {
			return Route{}, fmt.Errorf("route OS override for %q must be posix or windows", override.Alias)
		}
		key := foldAlias(override.Alias)
		if previous, exists := overrideNames[key]; exists {
			return Route{}, fmt.Errorf("ambiguous route OS overrides %q and %q", previous, override.Alias)
		}
		overrideNames[key] = override.Alias
		overrides[key] = remoteOS
	}

	active := make(map[string]bool)
	var resolved []routeHopState
	var visit func(jumpSpec, []jumpSpec, bool, int) error
	visit = func(spec jumpSpec, prefix []jumpSpec, target bool, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth >= maxDepth || len(resolved) >= maxDepth {
			return fmt.Errorf("ProxyJump route exceeds depth %d: %w", maxDepth, ErrUnsupportedRoute)
		}
		invocation := RouteInvocation{Alias: spec.host, User: spec.user, Port: spec.port}
		if len(prefix) > 0 {
			invocation.OverrideProxyJump = true
			for _, jump := range prefix {
				if invocation.ProxyJump != "" {
					invocation.ProxyJump += ","
				}
				invocation.ProxyJump += jump.reference
			}
		}
		effective, err := resolver(ctx, invocation)
		if err != nil {
			return err
		}
		if !equalAlias(effective.Alias, spec.host) {
			return fmt.Errorf("effective resolver returned a different route alias: %w", ErrUnsupportedRoute)
		}
		user, port := effective.User, effective.Port
		if spec.user != "" {
			user = spec.user
		}
		if spec.port != 0 {
			port = spec.port
		}
		// Explicit comma prefixes form finite invocations even when a host is
		// revisited. An inherited invocation recursively reaching itself is a
		// true cycle. Do not include an ever-growing prefix in this key.
		key := foldAlias(spec.host)
		invocationKey := fmt.Sprintf("%s\x00%s\x00%d\x00%t", key, user, port, invocation.OverrideProxyJump)
		if !invocation.OverrideProxyJump && active[invocationKey] {
			return fmt.Errorf("ProxyJump cycle at %q: %w", spec.host, ErrUnsupportedRoute)
		}
		if !invocation.OverrideProxyJump {
			active[invocationKey] = true
			defer delete(active, invocationKey)
		}
		var jumps []jumpSpec
		if invocation.OverrideProxyJump {
			jumps = prefix
		} else {
			if proxyCommandEnabled(effective) {
				return fmt.Errorf("ProxyCommand is not supported for %q: %w", spec.host, ErrUnsupportedRoute)
			}
			jumps, err = parseProxyJump(effective.ProxyJump)
			if err != nil {
				return fmt.Errorf("parse ProxyJump for %q: %w", spec.host, err)
			}
		}
		if len(jumps) > 0 {
			last := len(jumps) - 1
			if err := visit(jumps[last], jumps[:last], false, depth+1); err != nil {
				return err
			}
		}

		remoteOS := RemoteOSUnknown
		if override, ok := overrides[key]; ok {
			remoteOS = override
			usedOverrides[key] = true
		}
		if target {
			if remoteOS != RemoteOSUnknown && targetOS != RemoteOSUnknown && remoteOS != targetOS {
				return fmt.Errorf("target OS and per-alias override disagree for %q", spec.host)
			}
			if remoteOS == RemoteOSUnknown {
				remoteOS = targetOS
			}
		}
		reference := spec.reference
		if reference == "" {
			reference = spec.host
		}
		hop := RouteHop{
			Alias: spec.host, Reference: reference, HostName: effective.HostName,
			User: user, Port: port, RemoteOS: remoteOS, AdminState: AdminUnknown, Target: target,
		}
		if !validRouteHop(hop) {
			return fmt.Errorf("effective route values for %q are unsupported: %w", spec.host, ErrUnsupportedRoute)
		}
		resolved = append(resolved, routeHopState{
			safe: hop, destination: spec.host, explicitUser: spec.user, explicitPort: spec.port,
			effective: cloneEffective(effective),
		})
		return nil
	}

	if err := visit(jumpSpec{reference: request.Alias, host: request.Alias}, nil, true, 0); err != nil {
		return Route{}, err
	}
	for key, name := range overrideNames {
		if !usedOverrides[key] {
			return Route{}, fmt.Errorf("route OS override %q does not match a resolved hop", name)
		}
	}
	if len(resolved) == 0 || !resolved[len(resolved)-1].safe.Target {
		return Route{}, errors.New("resolved route has no target")
	}
	invocations := make(map[string]bool)
	declaredHosts := make(map[string]bool)
	var diagnostics []Diagnostic
	for _, state := range resolved {
		hop := state.safe
		key := fmt.Sprintf("%s\x00%s\x00%d", foldAlias(hop.Alias), hop.User, hop.Port)
		if invocations[key] {
			return Route{}, fmt.Errorf("repeated_invocation: ProxyJump repeats %q with the same user and port: %w", hop.Alias, ErrUnsupportedRoute)
		}
		invocations[key] = true
		hostKey := strings.TrimSuffix(strings.ToLower(hop.HostName), ".")
		if address := net.ParseIP(hostKey); address != nil {
			hostKey = address.String()
		}
		if hostKey != "" && declaredHosts[hostKey] {
			diagnostics = append(diagnostics, Diagnostic{Code: "machine_revisited", Message: "Route revisits a declared host through a distinct user, port or alias; connection profiles remain separate."})
		}
		declaredHosts[hostKey] = true
	}
	var outerReferences []string
	for index := range resolved {
		if !resolved[index].safe.Target && len(outerReferences) > 0 {
			resolved[index].proxyJump = strings.Join(outerReferences, ",")
		}
		outerReferences = append(outerReferences, resolved[index].safe.Reference)
		resolved[index].proofRoute = make([]proofRouteHop, index+1)
		for proofIndex := 0; proofIndex <= index; proofIndex++ {
			proof := resolved[proofIndex]
			resolved[index].proofRoute[proofIndex] = proofRouteHop{
				alias: proof.safe.Alias, reference: proof.safe.Reference, hostName: proof.safe.HostName,
				user: proof.safe.User, port: proof.safe.Port, effective: cloneEffective(proof.effective),
			}
		}
	}
	if resolved[len(resolved)-1].safe.RemoteOS != RemoteOSUnknown {
		targetOS = resolved[len(resolved)-1].safe.RemoteOS
	}
	route := Route{Alias: request.Alias, TargetRemoteOS: targetOS, Diagnostics: diagnostics}
	for _, hop := range resolved {
		route.Hops = append(route.Hops, hop.safe)
	}
	safe := cloneRoute(route)
	resolvedRequest := request
	resolvedRequest.MaxDepth = maxDepth
	resolvedRequest.OSOverrides = append([]RemoteOSOverride(nil), request.OSOverrides...)
	state := &routeState{
		serviceID: serviceID, safe: safe, hops: append([]routeHopState(nil), resolved...),
		request: resolvedRequest, revalidate: revalidate,
	}
	if serviceID != 0 {
		route.state = state
	}
	return route, nil
}
func validateRouteLookupAlias(alias string) error {
	if err := ValidateLookupAlias(alias); err != nil {
		return err
	}
	if strings.Contains(alias, "://") || strings.ContainsAny(alias, "%/\\") {
		return fmt.Errorf("unsupported route alias %q: %w", alias, ErrUnsupportedRoute)
	}
	return nil
}

func normalizeRemoteOS(remoteOS RemoteOS) (RemoteOS, error) {
	switch remoteOS {
	case "", RemoteOSUnknown:
		return RemoteOSUnknown, nil
	case RemoteOSPOSIX, RemoteOSWindows:
		return remoteOS, nil
	default:
		return "", fmt.Errorf("unsupported remote OS %q", remoteOS)
	}
}

func proxyCommandEnabled(effective EffectiveConfig) bool {
	for _, value := range effective.Values["proxycommand"] {
		value = strings.TrimSpace(value)
		if value != "" && !strings.EqualFold(value, "none") {
			return true
		}
	}
	return false
}

func parseProxyJump(value string) ([]jumpSpec, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	jumps := make([]jumpSpec, 0, len(parts))
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part || strings.EqualFold(part, "none") {
			return nil, fmt.Errorf("empty, whitespace, or mixed none ProxyJump token: %w", ErrUnsupportedRoute)
		}
		jump, err := parseJumpSpec(part)
		if err != nil {
			return nil, err
		}
		jumps = append(jumps, jump)
	}
	return jumps, nil
}

func parseJumpSpec(value string) (jumpSpec, error) {
	fail := func() (jumpSpec, error) {
		return jumpSpec{}, fmt.Errorf("unsupported ProxyJump token %q: %w", value, ErrUnsupportedRoute)
	}
	if value == "" || strings.Contains(value, "://") || strings.ContainsAny(value, "%/\\") {
		return fail()
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fail()
		}
	}
	user := ""
	hostPort := value
	if strings.Count(value, "@") > 1 {
		return fail()
	}
	if index := strings.IndexByte(value, '@'); index >= 0 {
		user, hostPort = value[:index], value[index+1:]
		if !validJumpUser(user) {
			return fail()
		}
	}
	host := ""
	port := 0
	if strings.HasPrefix(hostPort, "[") {
		close := strings.IndexByte(hostPort, ']')
		if close <= 1 {
			return fail()
		}
		host = hostPort[1:close]
		if net.ParseIP(host) == nil || !strings.Contains(host, ":") {
			return fail()
		}
		suffix := hostPort[close+1:]
		if suffix != "" {
			if !strings.HasPrefix(suffix, ":") || strings.Count(suffix, ":") != 1 {
				return fail()
			}
			parsed, err := parseJumpPort(strings.TrimPrefix(suffix, ":"))
			if err != nil {
				return fail()
			}
			port = parsed
		}
	} else {
		if strings.Count(hostPort, ":") > 1 {
			return fail()
		}
		host = hostPort
		if index := strings.LastIndexByte(hostPort, ':'); index >= 0 {
			host = hostPort[:index]
			parsed, err := parseJumpPort(hostPort[index+1:])
			if err != nil {
				return fail()
			}
			port = parsed
		}
	}
	if err := ValidateLookupAlias(host); err != nil {
		return fail()
	}
	return jumpSpec{reference: value, host: host, user: user, port: port}, nil
}

func validJumpUser(value string) bool {
	if value == "" || len(value) > 255 || !validUTF8NoControl(value) {
		return false
	}
	return !strings.ContainsAny(value, "@:/\\[]")
}

func parseJumpPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid ProxyJump port")
	}
	return port, nil
}

func validRouteHop(hop RouteHop) bool {
	if ValidateLookupAlias(hop.Alias) != nil || !validUTF8NoControl(hop.Reference) || !validUTF8NoControl(hop.HostName) || !validUTF8NoControl(hop.User) {
		return false
	}
	return hop.Port >= 0 && hop.Port <= 65535
}

func cloneRoute(route Route) Route {
	copy := route
	copy.Hops = append([]RouteHop(nil), route.Hops...)
	copy.Diagnostics = append([]Diagnostic(nil), route.Diagnostics...)
	copy.state = nil
	return copy
}

func routesEqual(left, right Route) bool {
	return reflect.DeepEqual(cloneRoute(left), cloneRoute(right))
}

func (s *Service) validateRoute(route Route) (*routeState, error) {
	if route.state == nil || route.state.serviceID != s.id {
		return nil, errors.New("route was not produced by this service")
	}
	if !routesEqual(route, route.state.safe) {
		return nil, errors.New("route public fields were modified")
	}
	return route.state, nil
}
