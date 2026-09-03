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
	Alias          string     `json:"alias"`
	Hops           []RouteHop `json:"hops"`
	TargetRemoteOS RemoteOS   `json:"target_remote_os"`
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

func (s *Service) effectiveRouteHop(ctx context.Context, spec jumpSpec) (EffectiveConfig, error) {
	if spec.user == "" && spec.port == 0 {
		return s.Effective(ctx, spec.host)
	}
	args := []string{"-G"}
	if spec.user != "" {
		args = append(args, "-l", spec.user)
	}
	if spec.port != 0 {
		args = append(args, "-p", strconv.Itoa(spec.port))
	}
	args = append(args, spec.host)
	return s.evaluateSSHConfig(
		ctx,
		spec.host,
		args,
		"ssh -G explicit route hop",
		"evaluate explicit SSH route hop",
		false,
	)
}

// ResolveRoute is explicitly effectful: it invokes plain ssh -G independently
// for the target and every discovered ProxyJump host.
func (s *Service) ResolveRoute(ctx context.Context, request RouteRequest) (Route, error) {
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
	appended := make(map[string]bool)
	var resolved []routeHopState
	var visit func(jumpSpec, bool) error
	visit = func(spec jumpSpec, target bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := foldAlias(spec.host)
		if active[key] || appended[key] {
			return fmt.Errorf("ProxyJump cycle or repeated hop at %q: %w", spec.host, ErrUnsupportedRoute)
		}
		if len(active) >= maxDepth || len(resolved) >= maxDepth {
			return fmt.Errorf("ProxyJump route exceeds depth %d: %w", maxDepth, ErrUnsupportedRoute)
		}
		active[key] = true
		defer delete(active, key)

		effective, err := s.effectiveRouteHop(ctx, spec)
		if err != nil {
			return err
		}
		if proxyCommandEnabled(effective) {
			return fmt.Errorf("ProxyCommand is not supported for %q: %w", spec.host, ErrUnsupportedRoute)
		}
		jumps, err := parseProxyJump(effective.ProxyJump)
		if err != nil {
			return fmt.Errorf("parse ProxyJump for %q: %w", spec.host, err)
		}
		for _, jump := range jumps {
			if err := visit(jump, false); err != nil {
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
		user := effective.User
		if spec.user != "" {
			user = spec.user
		}
		port := effective.Port
		if spec.port != 0 {
			port = spec.port
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
		appended[key] = true
		return nil
	}

	if err := visit(jumpSpec{reference: request.Alias, host: request.Alias}, true); err != nil {
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
	route := Route{Alias: request.Alias, TargetRemoteOS: targetOS}
	for _, hop := range resolved {
		route.Hops = append(route.Hops, hop.safe)
	}
	safe := cloneRoute(route)
	resolvedRequest := request
	resolvedRequest.MaxDepth = maxDepth
	resolvedRequest.OSOverrides = append([]RemoteOSOverride(nil), request.OSOverrides...)
	state := &routeState{
		serviceID: s.id, safe: safe, hops: append([]routeHopState(nil), resolved...),
		request: resolvedRequest, revalidate: true,
	}
	route.state = state
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
