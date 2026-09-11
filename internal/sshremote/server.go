package sshremote

import (
	"context"
	"errors"
	"io/fs"
	"os/user"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

// Server runs on the selected fleet host. SSH paths, agent sockets and key
// selections are resolved there, never through the controller's environment.
// Identity is the only durable write, and only Capability may initialize it.
type Server struct {
	SSH      *sshhost.Service
	Identity *machineid.Store
	Platform string
	User     string
	Now      func() time.Time
}

func NewServer(service *sshhost.Service) *Server {
	return &Server{SSH: service, Identity: machineid.NewStore(""), Platform: runtime.GOOS, Now: time.Now}
}

func (s *Server) origin(ctx context.Context, create bool) (Origin, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Origin{}, err
	}
	if s == nil || s.SSH == nil || s.Identity == nil {
		return Origin{}, ErrUnavailable
	}
	var id string
	var err error
	if create {
		id, err = s.Identity.LoadOrCreate(ctx)
	} else {
		id, err = s.Identity.Load()
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Origin{}, ErrUnavailable
		}
		return Origin{}, ErrInvalidData
	}
	name := s.User
	if name == "" {
		current, lookupErr := user.Current()
		if lookupErr != nil {
			return Origin{}, ErrUnavailable
		}
		name = current.Username
	}
	platform := s.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	origin := Origin{MachineID: id, Platform: platform, User: name, Root: s.SSH.Paths().RootConfig}
	origin.ID = OriginID(origin.MachineID, origin.User, origin.Root)
	return origin, origin.Validate()
}
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) Capability(ctx context.Context, request CapabilityRequest) (Capability, error) {
	if err := request.Header.Validate(); err != nil {
		return Capability{}, err
	}
	origin, err := s.origin(ctx, true)
	if err != nil {
		return Capability{}, err
	}
	return Capability{Header: NewHeader(), Kind: "ssh_remote_capability", Origin: origin, Supported: true, Operations: []string{"inventory", "resolve", "keys", "connect"}}, nil
}

func (s *Server) checkRequest(ctx context.Context, request Request) (Origin, error) {
	if err := request.Validate(); err != nil {
		return Origin{}, err
	}
	origin, err := s.origin(ctx, false)
	if err != nil {
		return Origin{}, err
	}
	if origin.ID != request.OriginID || origin.MachineID != request.MachineID {
		return Origin{}, ErrSourceChanged
	}
	return origin, nil
}

func (s *Server) Inventory(ctx context.Context, request Request) (Inventory, error) {
	origin, err := s.checkRequest(ctx, request)
	if err != nil {
		return Inventory{}, err
	}
	return s.inventory(ctx, origin)
}

func (s *Server) inventory(ctx context.Context, origin Origin) (Inventory, error) {
	observed, err := s.SSH.Discover(ctx)
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{Header: NewHeader(), Kind: "ssh_remote_inventory", Origin: origin, Complete: observed.Complete, ObservedAt: s.now(), Profiles: []Profile{}}
	hints := map[string]sshhost.ConnectionHint{}
	for _, hint := range observed.ConnectionHints {
		hints[strings.ToLower(hint.Alias)] = hint
	}
	for _, alias := range observed.Aliases {
		if len(result.Profiles) == MaxProfiles {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "profile_limit", Incomplete: true})
			break
		}
		if len(alias.Definitions) == 0 || sshhost.ValidateLookupAlias(alias.Name) != nil {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "invalid_alias", Incomplete: true})
			continue
		}
		hint := hints[strings.ToLower(alias.Name)]
		fingerprint := hint.Fingerprint
		if fingerprint == "" {
			fingerprint = sourceDigest(alias.Definitions)
		}
		profile := Profile{ID: ProfileID(origin.ID, alias.Name), Alias: alias.Name, Fingerprint: fingerprint, State: profileState(alias), Source: alias.Definitions[0].Source}
		if hint.State == "known" {
			profile.HostName = hint.HostName
			profile.User = hint.User
			profile.Port = hint.Port
		}
		if err := profile.validate(origin); err != nil {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "invalid_profile", Incomplete: true})
			continue
		}
		result.Profiles = append(result.Profiles, profile)
	}
	for _, diagnostic := range observed.Diagnostics {
		if len(result.Diagnostics) == 4096 {
			result.Complete = false
			break
		}
		if safeText(diagnostic.Code, 128, false) {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: diagnostic.Code, Incomplete: diagnostic.Incomplete})
		}
	}
	sort.Slice(result.Profiles, func(i, j int) bool {
		return strings.ToLower(result.Profiles[i].Alias) < strings.ToLower(result.Profiles[j].Alias)
	})
	if _, err := s.checkRequest(ctx, NewRequest(origin)); err != nil {
		return Inventory{}, err
	}
	return result, result.Validate()
}

func profileState(alias sshhost.Alias) string {
	if alias.Conflict {
		return "conflict"
	}
	state := "active"
	for _, definition := range alias.Definitions {
		if definition.Reachability == sshhost.Unknown {
			return "unknown"
		}
		if definition.Reachability == sshhost.Unreachable {
			state = "inactive"
		}
	}
	return state
}

func (s *Server) selected(ctx context.Context, request Request, selection Selection) (Origin, Profile, error) {
	if selection.Validate() != nil || selection.OriginID != request.OriginID {
		return Origin{}, Profile{}, ErrInvalidData
	}
	origin, err := s.checkRequest(ctx, request)
	if err != nil {
		return Origin{}, Profile{}, err
	}
	inventory, err := s.inventory(ctx, origin)
	if err != nil {
		return Origin{}, Profile{}, err
	}
	profile, found := inventory.Find(selection.ProfileID)
	if !found {
		return Origin{}, Profile{}, ErrNotFound
	}
	if profile.Fingerprint != selection.Fingerprint || !strings.EqualFold(profile.Alias, selection.Alias) || profile.State != "active" {
		return Origin{}, Profile{}, ErrSourceChanged
	}
	return origin, profile, nil
}

func (s *Server) Resolve(ctx context.Context, request ResolveRequest) (Resolved, error) {
	origin, profile, err := s.selected(ctx, request.Request, request.Selection)
	if err != nil {
		return Resolved{}, err
	}
	route, err := s.SSH.ResolveRoute(ctx, sshhost.RouteRequest{Alias: profile.Alias, MaxDepth: MaxRouteHops})
	if err != nil {
		return Resolved{}, err
	}
	effective, err := s.SSH.Effective(ctx, profile.Alias)
	if err != nil {
		return Resolved{}, err
	}
	if len(route.Hops) == 0 {
		return Resolved{}, ErrInvalidData
	}
	target := route.Hops[len(route.Hops)-1]
	if target.HostName != effective.HostName || target.User != effective.User || target.Port != effective.Port {
		return Resolved{}, ErrSourceChanged
	}
	result := Resolved{Header: NewHeader(), Kind: "ssh_remote_resolved", Origin: origin, Profile: profile, ObservedAt: s.now(), Effective: effective, Route: route, RouteProfiles: []RouteProfile{}, Importable: true}
	for _, hop := range route.Hops {
		result.RouteProfiles = append(result.RouteProfiles, RouteProfile{OriginID: origin.ID, ProfileID: ProfileID(origin.ID, hop.Alias), Hop: hop})
	}
	hopConfigurations, err := s.SSH.RouteEffective(route)
	if err != nil {
		return Resolved{}, err
	}
	if len(hopConfigurations) == 0 || !reflect.DeepEqual(hopConfigurations[len(hopConfigurations)-1], effective) {
		return Resolved{}, ErrSourceChanged
	}
	for _, configuration := range hopConfigurations {
		result.ImportDiagnostics = append(result.ImportDiagnostics, portablePolicy(configuration.Alias, configuration)...)
	}
	result.Importable = len(result.ImportDiagnostics) == 0
	// Never retain command-like private fields in a transported or cached DTO.
	result.Effective.Values = nil
	if _, _, err := s.selected(ctx, request.Request, request.Selection); err != nil {
		return Resolved{}, err
	}
	return result, result.Validate()
}

// Local imports choose controller authentication and host-key policy. These
// directives instead change routing, invoke commands, or depend on source-local
// resources, so importing only HostName/User/Port would misrepresent the profile.
func portablePolicy(alias string, effective sshhost.EffectiveConfig) []Diagnostic {
	var result []Diagnostic
	for _, key := range []string{"proxycommand", "knownhostscommand", "localcommand", "remotecommand", "hostkeyalias", "bindaddress", "bindinterface"} {
		for _, value := range effective.Values[key] {
			if value != "" && !strings.EqualFold(value, "none") && !strings.EqualFold(value, "internal") && value != "*" {
				result = append(result, Diagnostic{Code: "source_policy_" + key, Alias: alias})
				break
			}
		}
	}
	for _, key := range []string{"localforward", "remoteforward", "dynamicforward"} {
		if len(effective.Values[key]) > 0 {
			result = append(result, Diagnostic{Code: "source_policy_" + key, Alias: alias})
		}
	}
	if values := effective.Values["tunnel"]; len(values) > 0 && values[0] != "no" && values[0] != "false" {
		result = append(result, Diagnostic{Code: "source_policy_tunnel", Alias: alias})
	}
	if strings.ContainsAny(effective.HostName, "%$`\r\n") || strings.ContainsAny(effective.User, "%$`\r\n") {
		result = append(result, Diagnostic{Code: "source_dynamic_endpoint", Alias: alias})
	}
	return result
}

func (s *Server) Keys(ctx context.Context, request KeysRequest) (Keys, error) {
	origin, err := s.checkRequest(ctx, request.Request)
	if err != nil {
		return Keys{}, err
	}
	if request.Alias != "" {
		if sshhost.ValidateLookupAlias(request.Alias) != nil {
			return Keys{}, ErrInvalidData
		}
		inventory, err := s.inventory(ctx, origin)
		if err != nil {
			return Keys{}, err
		}
		profile, ok := inventory.Find(request.Alias)
		if !ok {
			return Keys{}, ErrNotFound
		}
		if profile.State != "active" {
			return Keys{}, ErrSourceChanged
		}
	}
	catalog, err := s.SSH.Catalog(ctx, sshhost.KeyCatalogRequest{LocalOnly: request.Alias == "", Alias: request.Alias, NoAgent: request.NoAgent})
	if err != nil {
		return Keys{}, err
	}
	// Diagnostic messages can contain provider stderr. Only stable codes and
	// paths cross this protocol; the receiving UI can describe those codes.
	for index := range catalog.Diagnostics {
		catalog.Diagnostics[index].Message = ""
		catalog.Diagnostics[index].Source = nil
	}
	result := Keys{Header: NewHeader(), Kind: "ssh_remote_keys", Origin: origin, Alias: request.Alias, ObservedAt: s.now(), Catalog: catalog}
	if _, err := s.checkRequest(ctx, request.Request); err != nil {
		return Keys{}, err
	}
	return result, result.Validate()
}

// Connect validates the exact source selection again before native SSH starts.
// Key metadata received from a controller is never accepted as signing state.
func (s *Server) Connect(ctx context.Context, request ConnectRequest) (sshhost.RunResult, error) {
	if err := request.Validate(); err != nil {
		return sshhost.RunResult{}, err
	}
	_, profile, err := s.selected(ctx, request.Request, request.Selection)
	if err != nil {
		return sshhost.RunResult{}, err
	}
	var candidate *sshhost.KeyCandidate
	if request.KeyID != "" {
		catalog, err := s.SSH.Catalog(ctx, sshhost.KeyCatalogRequest{Alias: profile.Alias})
		if err != nil {
			return sshhost.RunResult{}, err
		}
		for _, key := range catalog.Candidates {
			if key.Fingerprint == request.KeyID {
				selected := key
				candidate = &selected
				break
			}
		}
		if candidate == nil {
			return sshhost.RunResult{}, ErrNotFound
		}
	}
	connection, err := s.SSH.PrepareConnection(ctx, profile.Alias, candidate)
	if err != nil {
		return sshhost.RunResult{}, err
	}
	defer connection.Close()
	if _, _, err := s.selected(ctx, request.Request, request.Selection); err != nil {
		return sshhost.RunResult{}, err
	}
	return connection.Run(ctx, sshhost.ConnectionOptions{Interactive: true, ForwardAgentNo: true})
}

type ErrorResponse struct {
	Header
	Kind string `json:"kind"`
	Code string `json:"code"`
}

func ProtocolError(err error) ErrorResponse {
	return ErrorResponse{Header: NewHeader(), Kind: "ssh_remote_error", Code: ErrorCode(err)}
}
