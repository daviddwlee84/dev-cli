package sshflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

type FleetImportRequest struct {
	Alias             string
	Host              fleet.Host
	Source            sshremote.Resolved
	Gateway           sshhost.Route
	GatewayDefinition *sshhost.ManagedDefinition
	Existing          []sshhost.ManagedDefinition
	Registry          machineregistry.Snapshot
	IdentityFile      string
	HopIdentityFiles  map[string]string
}

// FleetImportPlan contains portable local definitions and explicit origin
// intent. Remote keys, agent paths and host-key stores never enter definitions.
// The calling coordinator binds/rechecks this plan with remote and local guards.
type FleetImportPlan struct {
	OriginID          string                      `json:"origin_id"`
	ProfileID         string                      `json:"profile_id"`
	FleetHost         string                      `json:"fleet_host"`
	SourceFingerprint string                      `json:"source_fingerprint"`
	RouteFingerprint  string                      `json:"route_fingerprint"`
	Definitions       []sshhost.ManagedDefinition `json:"definitions"`
	Imports           []machineregistry.SSHImport `json:"imports"`
	Route             []sshhost.RouteHop          `json:"route"`
}

func DefinitionFingerprint(value sshhost.ManagedDefinition) string { return importDigest(value) }
func importDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ImportedHopAlias(host, origin, profile string, hop sshhost.RouteHop, prefix []string, identity string) string {
	label := strings.ToLower(host + "-" + hop.Alias)
	var safe strings.Builder
	for _, r := range label {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			safe.WriteRune(r)
		} else {
			safe.WriteByte('-')
		}
	}
	label = strings.Trim(safe.String(), "-")
	if len(label) > 64 {
		label = label[:64]
	}
	if label == "" {
		label = "hop"
	}
	return "via-" + label + "-" + importDigest([]any{origin, profile, hop.HostName, hop.User, hop.Port, prefix, identity})[:10]
}

func importedDefinition(request FleetImportRequest, index int, refs []string) (sshhost.ManagedDefinition, string) {
	remote := request.Source.RouteProfiles[index]
	hop := remote.Hop
	alias, identity := request.Alias, request.IdentityFile
	if index != len(request.Source.RouteProfiles)-1 {
		alias = ImportedHopAlias(request.Host.Name, request.Source.Origin.ID, remote.ProfileID, hop, refs, identity)
	}
	if selected, ok := request.HopIdentityFiles[alias]; ok {
		identity = selected
	}
	definition := sshhost.ManagedDefinition{Alias: alias, HostName: hop.HostName, User: hop.User, Port: hop.Port, ProxyJump: strings.Join(refs, ","), IdentityFile: identity}
	return definition, importDigest([]any{request.Source.Origin.ID, remote.ProfileID, hop.HostName, hop.User, hop.Port, refs})
}

func PlanFleetImport(request FleetImportRequest) (FleetImportPlan, error) {
	source := request.Source
	plan := FleetImportPlan{OriginID: source.Origin.ID, ProfileID: source.Profile.ID, FleetHost: request.Host.Name, SourceFingerprint: source.Profile.Fingerprint}
	if err := sshhost.ValidateManagedAlias(request.Alias); err != nil {
		return plan, err
	}
	if err := source.Validate(); err != nil {
		return plan, err
	}
	if !source.Importable {
		return plan, errors.New("remote profile has nonportable settings; configure an explicit local profile instead")
	}
	if len(request.Gateway.Hops) == 0 {
		return plan, errors.New("fleet source has no reviewed local route")
	}
	if len(source.Route.Hops)+len(request.Gateway.Hops) > 16 {
		return plan, errors.New("imported route exceeds 16 hops")
	}
	refs := []string{}
	usedNames := map[string]bool{}
	endpoints := map[string]string{}
	for _, hop := range request.Gateway.Hops {
		if sshhost.ValidateLookupAlias(hop.Alias) != nil || hop.HostName == "" || hop.User == "" || hop.Port < 1 || hop.Port > 65535 {
			return plan, errors.New("fleet source contains an invalid reviewed gateway hop")
		}
		if hop.Reference == "" {
			hop.Reference = hop.Alias
		}
		refs = append(refs, hop.Reference)
		hop.Target = false
		plan.Route = append(plan.Route, hop)
		usedNames[strings.ToLower(hop.Alias)] = true
		endpoints[importEndpoint(hop.HostName, hop.Port)] = "local"
	}
	if request.GatewayDefinition != nil {
		definition := request.GatewayDefinition
		if err := sshhost.ValidateManagedDefinition(*definition); err != nil {
			return plan, err
		}
		last := request.Gateway.Hops[len(request.Gateway.Hops)-1]
		if !strings.EqualFold(definition.Alias, last.Alias) || importEndpoint(definition.HostName, definition.Port) != importEndpoint(last.HostName, last.Port) || definition.User != "" && definition.User != last.User || definition.ProxyJump != "" && definition.ProxyJump != strings.Join(refs[:len(refs)-1], ",") {
			return plan, errors.New("gateway definition does not match its reviewed route")
		}
		if previous, found := request.Registry.LookupImport(definition.Alias); found && (previous.OriginID != source.Origin.ID || previous.ProfileID != "gateway:"+source.Origin.ID) {
			return plan, fmt.Errorf("gateway alias %q belongs to a different import origin", definition.Alias)
		}
		for _, existing := range request.Existing {
			if strings.EqualFold(existing.Alias, request.GatewayDefinition.Alias) {
				previous, found := request.Registry.LookupImport(existing.Alias)
				if !found || previous.OriginID != source.Origin.ID || previous.ProfileID != "gateway:"+source.Origin.ID || previous.DefinitionFingerprint != DefinitionFingerprint(existing) || DefinitionFingerprint(existing) != DefinitionFingerprint(*request.GatewayDefinition) {
					return plan, fmt.Errorf("gateway alias %q is not the unchanged owned import", existing.Alias)
				}
			}
		}
		plan.Definitions = append(plan.Definitions, *request.GatewayDefinition)
		plan.Imports = append(plan.Imports, machineregistry.SSHImport{LocalAlias: request.GatewayDefinition.Alias, OriginID: source.Origin.ID, ProfileID: "gateway:" + source.Origin.ID, FleetHost: request.Host.Name, RemoteAlias: "@gateway", SourceFingerprint: source.Profile.Fingerprint, RouteFingerprint: importDigest(request.Gateway.Hops), DefinitionFingerprint: DefinitionFingerprint(*request.GatewayDefinition)})
	}
	// Resolve the whole planned set before checking existing alternatives: an
	// unchanged second invocation of this same route is not a foreign profile.
	type plannedInvocation struct{ profile, route string }
	planned := map[string]plannedInvocation{}
	plannedRefs := append([]string{}, refs...)
	for i, remote := range source.RouteProfiles {
		definition, fingerprint := importedDefinition(request, i, plannedRefs)
		planned[strings.ToLower(definition.Alias)] = plannedInvocation{remote.ProfileID, fingerprint}
		plannedRefs = append(plannedRefs, definition.Alias)
	}
	for i, remote := range source.RouteProfiles {
		hop := remote.Hop
		endpoint := importEndpoint(hop.HostName, hop.Port)
		if prior, exists := endpoints[endpoint]; exists && prior != "remote:"+source.Origin.ID {
			return plan, fmt.Errorf("endpoint %s occurs in different route scopes; reuse an explicitly configured local profile", endpoint)
		}
		endpoints[endpoint] = "remote:" + source.Origin.ID
		definition, prefixFingerprint := importedDefinition(request, i, refs)
		alias := definition.Alias
		if usedNames[strings.ToLower(alias)] {
			return plan, fmt.Errorf("generated alias %q repeats a reviewed route profile", alias)
		}
		usedNames[strings.ToLower(alias)] = true
		if err := sshhost.ValidateManagedDefinition(definition); err != nil {
			return plan, err
		}
		for _, existing := range request.Existing {
			if strings.EqualFold(existing.Alias, alias) {
				continue
			}
			if importEndpoint(existing.HostName, existing.Port) != endpoint {
				continue
			}
			previous, known := request.Registry.LookupImport(existing.Alias)
			if invocation, partOfPlan := planned[strings.ToLower(existing.Alias)]; partOfPlan && known && previous.OriginID == source.Origin.ID && previous.ProfileID == invocation.profile && previous.RouteFingerprint == invocation.route && previous.DefinitionFingerprint == DefinitionFingerprint(existing) {
				continue
			}
			if !known || previous.OriginID != source.Origin.ID || previous.ProfileID != remote.ProfileID || previous.RouteFingerprint != prefixFingerprint || previous.DefinitionFingerprint != DefinitionFingerprint(existing) {
				return plan, fmt.Errorf("endpoint %s already belongs to a different or unscoped local profile %q; review its host-key namespace manually", endpoint, existing.Alias)
			}
		}
		if previous, found := request.Registry.LookupImport(alias); found && (previous.OriginID != source.Origin.ID || previous.ProfileID != remote.ProfileID) {
			return plan, fmt.Errorf("alias %q has a different import origin; choose a new alias", alias)
		}
		for _, existing := range request.Existing {
			if !strings.EqualFold(existing.Alias, alias) {
				continue
			}
			previous, found := request.Registry.LookupImport(alias)
			if !found || previous.DefinitionFingerprint != DefinitionFingerprint(existing) {
				return plan, fmt.Errorf("existing alias %q is not an unchanged profile owned by this import", alias)
			}
			// Shared bastions are immutable from another target's import. A key
			// override needs a separately named profile, never a shared rewrite.
			if i < len(source.RouteProfiles)-1 && DefinitionFingerprint(existing) != DefinitionFingerprint(definition) {
				return plan, fmt.Errorf("shared jump profile %q has different settings; select a separate local profile", alias)
			}
		}
		plan.Definitions = append(plan.Definitions, definition)
		plan.Imports = append(plan.Imports, machineregistry.SSHImport{LocalAlias: alias, OriginID: source.Origin.ID, ProfileID: remote.ProfileID, FleetHost: request.Host.Name, RemoteAlias: hop.Alias, SourceFingerprint: source.Profile.Fingerprint, RouteFingerprint: prefixFingerprint, DefinitionFingerprint: DefinitionFingerprint(definition)})
		hop.Alias, hop.Reference, hop.Target = alias, alias, i == len(source.RouteProfiles)-1
		plan.Route = append(plan.Route, hop)
		refs = append(refs, alias)
	}
	for alias := range request.HopIdentityFiles {
		if !usedNames[strings.ToLower(alias)] {
			return plan, fmt.Errorf("hop key override %q does not occur in the imported route", alias)
		}
	}
	plan.RouteFingerprint = importDigest(plan.Route)
	return plan, nil
}

func importEndpoint(host string, port int) string {
	if port == 0 {
		port = 22
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	}
	return host + "|" + strconv.Itoa(port)
}

// ValidateFleetImportBatch checks the union before any config initialization or
// publication. Individual plans can each be valid against the same empty disk
// snapshot yet conflict with one another. It never merges machine identities.
func ValidateFleetImportBatch(plans []FleetImportPlan) error {
	type plannedDefinition struct {
		definition sshhost.ManagedDefinition
		provenance machineregistry.SSHImport
	}
	type endpointScope struct {
		plan            int
		origin, ingress string
		gateway         bool
	}
	aliases := map[string]plannedDefinition{}
	endpoints := map[string][]endpointScope{}
	for index, plan := range plans {
		if plan.OriginID == "" || len(plan.Definitions) == 0 || len(plan.Definitions) != len(plan.Imports) {
			return errors.New("incomplete SSH import batch plan")
		}
		imports := map[string]machineregistry.SSHImport{}
		for _, provenance := range plan.Imports {
			key := strings.ToLower(provenance.LocalAlias)
			if _, exists := imports[key]; exists {
				return fmt.Errorf("duplicate import provenance for %q", provenance.LocalAlias)
			}
			if provenance.OriginID != plan.OriginID {
				return errors.New("import definition has a different origin from its plan")
			}
			imports[key] = provenance
		}
		remoteAliases := map[string]bool{}
		for _, definition := range plan.Definitions {
			if err := sshhost.ValidateManagedDefinition(definition); err != nil {
				return err
			}
			key := strings.ToLower(definition.Alias)
			provenance, found := imports[key]
			if !found || provenance.DefinitionFingerprint != DefinitionFingerprint(definition) {
				return fmt.Errorf("import provenance does not match definition %q", definition.Alias)
			}
			if prior, exists := aliases[key]; exists {
				if DefinitionFingerprint(prior.definition) != DefinitionFingerprint(definition) || prior.provenance != provenance {
					return fmt.Errorf("shared alias %q has incompatible imported definitions or origins", definition.Alias)
				}
			} else {
				aliases[key] = plannedDefinition{definition, provenance}
			}
			if !strings.HasPrefix(provenance.ProfileID, "gateway:") {
				remoteAliases[key] = true
			}
			endpoint := importEndpoint(definition.HostName, definition.Port)
			scope := endpointScope{index, plan.OriginID, definition.ProxyJump, strings.HasPrefix(provenance.ProfileID, "gateway:")}
			for _, prior := range endpoints[endpoint] {
				if prior.plan != index && (prior.origin != scope.origin || prior.gateway != scope.gateway || !scope.gateway && prior.ingress != scope.ingress) {
					return fmt.Errorf("endpoint %s occurs in different import route scopes in this batch", endpoint)
				}
			}
			endpoints[endpoint] = append(endpoints[endpoint], scope)
		}
		// The final local hop identifies the source account/config-root scope.
		// Earlier native bastions may be shared as ordinary local route prefixes.
		for hopIndex, hop := range plan.Route {
			if !remoteAliases[strings.ToLower(hop.Alias)] {
				continue
			}
			if hopIndex > 0 {
				gateway := plan.Route[hopIndex-1]
				endpoint := importEndpoint(gateway.HostName, gateway.Port)
				scope := endpointScope{index, plan.OriginID, "", true}
				for _, prior := range endpoints[endpoint] {
					if prior.plan != index && (prior.origin != scope.origin || !prior.gateway) {
						return fmt.Errorf("source gateway endpoint %s has different account or config-root origins in this batch", endpoint)
					}
				}
				endpoints[endpoint] = append(endpoints[endpoint], scope)
			}
			break
		}
	}
	return nil
}
