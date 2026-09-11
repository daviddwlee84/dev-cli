package sshflow

import (
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
	"reflect"
	"strings"
	"testing"
	"time"
)

func importFixture(t *testing.T, target string, intermediate bool) FleetImportRequest {
	t.Helper()
	origin := sshremote.Origin{MachineID: "00000000-0000-4000-8000-000000000001", Platform: "linux", User: "operator", Root: "/home/operator/.ssh/config"}
	origin.ID = sshremote.OriginID(origin.MachineID, origin.User, origin.Root)
	profile := sshremote.Profile{ID: sshremote.ProfileID(origin.ID, target), Alias: target, Fingerprint: strings.Repeat("a", 64), State: "active", Source: sshhost.Location{Path: origin.Root, Line: 1}, HostName: target + ".internal", User: "service", Port: 22}
	source := sshremote.Resolved{Header: sshremote.NewHeader(), Kind: "ssh_remote_resolved", Origin: origin, Profile: profile, ObservedAt: time.Unix(1720000000, 0).UTC(), Effective: sshhost.EffectiveConfig{Alias: target, HostName: profile.HostName, User: profile.User, Port: profile.Port}, Route: sshhost.Route{Alias: target}, Importable: true}
	if intermediate {
		source.Route.Hops = append(source.Route.Hops, sshhost.RouteHop{Alias: "bastion", Reference: "bastion", HostName: "10.0.0.7", User: "jump", Port: 22, RemoteOS: sshhost.RemoteOSPOSIX, AdminState: sshhost.AdminUnknown})
	}
	source.Route.Hops = append(source.Route.Hops, sshhost.RouteHop{Alias: target, Reference: target, HostName: profile.HostName, User: profile.User, Port: profile.Port, Target: true, RemoteOS: sshhost.RemoteOSPOSIX, AdminState: sshhost.AdminUnknown})
	for _, hop := range source.Route.Hops {
		source.RouteProfiles = append(source.RouteProfiles, sshremote.RouteProfile{OriginID: origin.ID, ProfileID: sshremote.ProfileID(origin.ID, hop.Alias), Hop: hop})
	}
	if e := source.Validate(); e != nil {
		t.Fatal("invalid test DTO", e)
	}
	return FleetImportRequest{Alias: "local-" + target, Host: fleet.Host{Name: "lab", SSHAlias: "gateway"}, Source: source, Gateway: sshhost.Route{Alias: "gateway", Hops: []sshhost.RouteHop{{Alias: "gateway", Reference: "gateway", HostName: "gateway.example", User: "operator", Port: 22, Target: true}}}, IdentityFile: "/local/.ssh/chosen"}
}
func importWithExisting(r FleetImportRequest, p FleetImportPlan) FleetImportRequest {
	r.Existing = append([]sshhost.ManagedDefinition{}, p.Definitions...)
	r.Registry = machineregistry.Snapshot{SchemaVersion: machineregistry.SchemaVersion, Imports: append([]machineregistry.SSHImport{}, p.Imports...)}
	return r
}
func TestFleetImportPreservesScopedRouteAndUsesOnlyLocalIdentities(t *testing.T) {
	r := importFixture(t, "db", true)
	r.Source.Effective.IdentityFiles = []string{"/remote/.ssh/private"}
	p, e := PlanFleetImport(r)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Definitions) != 2 || len(p.Imports) != 2 || len(p.Route) != 3 {
		t.Fatal(p)
	}
	if p.Definitions[0].ProxyJump != "gateway" || p.Definitions[1].ProxyJump != "gateway,"+p.Definitions[0].Alias {
		t.Fatal("route was not grafted behind source", p.Definitions)
	}
	for _, d := range p.Definitions {
		if d.IdentityFile != r.IdentityFile || strings.Contains(d.IdentityFile, "/remote/") {
			t.Fatal("remote private path imported", d)
		}
	}
	for _, imported := range p.Imports {
		if imported.OriginID != r.Source.Origin.ID || imported.DefinitionFingerprint == "" || imported.RouteFingerprint == "" {
			t.Fatal(imported)
		}
	}
	if p.Route[0].Target || p.Route[1].Target || !p.Route[2].Target {
		t.Fatal("target flags wrong", p.Route)
	}
	second, e := PlanFleetImport(importWithExisting(r, p))
	if e != nil || !reflect.DeepEqual(p, second) {
		t.Fatalf("reimport changed aliases or definitions: %#v %v", second, e)
	}
}
func TestFleetImportSharesIdenticalPrefixWithoutFoldingOtherScopes(t *testing.T) {
	a := importFixture(t, "db", true)
	first, e := PlanFleetImport(a)
	if e != nil {
		t.Fatal(e)
	}
	b := importFixture(t, "cache", true)
	b = importWithExisting(b, first)
	second, e := PlanFleetImport(b)
	if e != nil {
		t.Fatal(e)
	}
	if first.Definitions[0].Alias != second.Definitions[0].Alias || DefinitionFingerprint(first.Definitions[0]) != DefinitionFingerprint(second.Definitions[0]) {
		t.Fatal("shared prefix duplicated")
	}
	if first.Definitions[1].Alias == second.Definitions[1].Alias {
		t.Fatal("different targets folded")
	}
	changed := b
	changed.Gateway.Hops = append([]sshhost.RouteHop{}, b.Gateway.Hops...)
	changed.Gateway.Hops[0].Alias = "other-gateway"
	changed.Gateway.Hops[0].Reference = "other-gateway"
	if _, e = PlanFleetImport(changed); e == nil {
		t.Fatal("same private endpoint silently accepted via different gateway scope")
	}
	changed = importWithExisting(importFixture(t, "cache", true), first)
	changed.Source.Origin.User = "other-account"
	changed.Source.Origin.ID = sshremote.OriginID(changed.Source.Origin.MachineID, changed.Source.Origin.User, changed.Source.Origin.Root)
	changed.Source.Profile.ID = sshremote.ProfileID(changed.Source.Origin.ID, changed.Source.Profile.Alias)
	for i := range changed.Source.RouteProfiles {
		changed.Source.RouteProfiles[i].OriginID = changed.Source.Origin.ID
		changed.Source.RouteProfiles[i].ProfileID = sshremote.ProfileID(changed.Source.Origin.ID, changed.Source.Route.Hops[i].Alias)
	}
	if _, e = PlanFleetImport(changed); e == nil {
		t.Fatal("cross-account private endpoint association accepted")
	}
}
func TestFleetImportRejectsLocalEditsAndOriginConflicts(t *testing.T) {
	for _, kind := range []string{"target_edited", "jump_edited", "alternative_edited", "wrong_origin", "foreign_case_collision"} {
		t.Run(kind, func(t *testing.T) {
			r := importFixture(t, "db", true)
			p, e := PlanFleetImport(r)
			if e != nil {
				t.Fatal(e)
			}
			r = importWithExisting(r, p)
			switch kind {
			case "target_edited":
				r.Existing[1].IdentityFile = "/local/other"
			case "jump_edited":
				r.Existing[0].User = "other"
			case "alternative_edited":
				r.Existing[0].Alias = "alternate-jump"
				r.Registry.Imports[0].LocalAlias = "alternate-jump"
				r.Registry.Imports[0].DefinitionFingerprint = DefinitionFingerprint(r.Existing[0])
				r.Existing[0].User = "edited"
			case "wrong_origin":
				r.Registry.Imports[1].OriginID = "other-origin"
			case "foreign_case_collision":
				r.Existing = []sshhost.ManagedDefinition{{Alias: strings.ToUpper(r.Alias), HostName: "unrelated.example"}}
				r.Registry.Imports = nil
			}
			if _, e = PlanFleetImport(r); e == nil {
				t.Fatal("unsafe reimport accepted")
			}
		})
	}
}
func TestFleetImportGatewayDefinitionMustMatchAndBeOwned(t *testing.T) {
	r := importFixture(t, "db", false)
	hop := r.Gateway.Hops[0]
	definition := sshhost.ManagedDefinition{Alias: hop.Alias, HostName: hop.HostName, User: hop.User, Port: hop.Port, IdentityFile: "/local/.ssh/gateway"}
	r.GatewayDefinition = &definition
	p, e := PlanFleetImport(r)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Definitions) != 2 || p.Imports[0].ProfileID != "gateway:"+r.Source.Origin.ID {
		t.Fatal(p)
	}
	if _, e = PlanFleetImport(importWithExisting(r, p)); e != nil {
		t.Fatal("owned gateway not reusable", e)
	}
	for _, kind := range []string{"unowned", "changed", "wrong_origin_without_file", "unrelated_alias", "wrong_endpoint", "wrong_prefix"} {
		t.Run(kind, func(t *testing.T) {
			test := importWithExisting(r, p)
			copy := definition
			test.GatewayDefinition = &copy
			switch kind {
			case "unowned":
				test.Registry.Imports = nil
			case "changed":
				test.Existing[0].IdentityFile = "/local/other"
			case "wrong_origin_without_file":
				test.Existing = nil
				test.Registry.Imports[0].OriginID = "other-origin"
			case "unrelated_alias":
				copy.Alias = "unrelated"
			case "wrong_endpoint":
				copy.HostName = "wrong.example"
			case "wrong_prefix":
				copy.ProxyJump = "unknown-gateway"
			}
			if _, e = PlanFleetImport(test); e == nil {
				t.Fatal("gateway outside reviewed ownership accepted")
			}
		})
	}
}
func TestFleetImportRejectsRouteCollisionAndUnknownOverrides(t *testing.T) {
	r := importFixture(t, "db", false)
	r.Source.Route.Hops[0].HostName = "gateway.example"
	r.Source.RouteProfiles[0].Hop = r.Source.Route.Hops[0]
	r.Source.Effective.HostName = "gateway.example"
	if _, e := PlanFleetImport(r); e == nil {
		t.Fatal("cross-scope endpoint collision accepted")
	}
	r = importFixture(t, "db", true)
	r.HopIdentityFiles = map[string]string{"absent": "/local/.ssh/other"}
	if _, e := PlanFleetImport(r); e == nil {
		t.Fatal("unknown hop override accepted")
	}
	r = importFixture(t, "db", false)
	r.Alias = "gateway"
	if _, e := PlanFleetImport(r); e == nil {
		t.Fatal("target reused gateway alias")
	}
}
func TestFleetImportDistinctUserInvocationIsNotMachineDeduplicated(t *testing.T) {
	r := importFixture(t, "db", true)
	last := len(r.Source.Route.Hops) - 1
	r.Source.Route.Hops[last].HostName = r.Source.Route.Hops[0].HostName
	r.Source.RouteProfiles[last].Hop = r.Source.Route.Hops[last]
	r.Source.Effective.HostName = r.Source.Route.Hops[last].HostName
	p, e := PlanFleetImport(r)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Route) != 3 || p.Route[1].User == p.Route[2].User || p.Route[1].Alias == p.Route[2].Alias {
		t.Fatal("distinct invocation was folded", p.Route)
	}
	if repeated, e := PlanFleetImport(importWithExisting(r, p)); e != nil || !reflect.DeepEqual(repeated, p) {
		t.Fatalf("finite distinct-user route is not reimportable: %#v %v", repeated, e)
	}
}

func TestFleetImportBatchDeduplicatesOnlyExactSharedProfiles(t *testing.T) {
	a, e := PlanFleetImport(importFixture(t, "db", true))
	if e != nil {
		t.Fatal(e)
	}
	b, e := PlanFleetImport(importFixture(t, "cache", true))
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateFleetImportBatch([]FleetImportPlan{a, b}); e != nil {
		t.Fatal("exact shared prefix rejected", e)
	}
	for _, kind := range []string{"definition", "origin", "provenance"} {
		t.Run(kind, func(t *testing.T) {
			changed := b
			changed.Definitions = append([]sshhost.ManagedDefinition{}, b.Definitions...)
			changed.Imports = append([]machineregistry.SSHImport{}, b.Imports...)
			switch kind {
			case "definition":
				changed.Definitions[0].IdentityFile = "/local/other"
				changed.Imports[0].DefinitionFingerprint = DefinitionFingerprint(changed.Definitions[0])
			case "origin":
				changed.OriginID = "different-origin"
				for i := range changed.Imports {
					changed.Imports[i].OriginID = changed.OriginID
				}
			case "provenance":
				changed.Imports[0].RouteFingerprint = "different-route"
			}
			if e := ValidateFleetImportBatch([]FleetImportPlan{a, changed}); e == nil {
				t.Fatal("incompatible shared profile accepted")
			}
		})
	}
}

func TestFleetImportBatchSharesUnchangedGeneratedGateway(t *testing.T) {
	aRequest := importFixture(t, "db", false)
	bRequest := importFixture(t, "cache", false)
	hop := aRequest.Gateway.Hops[0]
	definition := sshhost.ManagedDefinition{Alias: hop.Alias, HostName: hop.HostName, User: hop.User, Port: hop.Port}
	aRequest.GatewayDefinition = &definition
	bRequest.GatewayDefinition = &definition
	a, e := PlanFleetImport(aRequest)
	if e != nil {
		t.Fatal(e)
	}
	b, e := PlanFleetImport(bRequest)
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateFleetImportBatch([]FleetImportPlan{a, b}); e != nil {
		t.Fatal("identical generated gateway rejected", e)
	}
}
func TestFleetImportBatchRejectsNewCrossScopeEndpointsBeforeWrites(t *testing.T) {
	first := importFixture(t, "db", false)
	a, e := PlanFleetImport(first)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"different_origin", "different_ingress", "source_account"} {
		t.Run(kind, func(t *testing.T) {
			r := importFixture(t, "cache", false)
			if kind != "source_account" {
				r.Source.Effective.HostName = first.Source.Effective.HostName
				r.Source.Route.Hops[0].HostName = r.Source.Effective.HostName
				r.Source.RouteProfiles[0].Hop = r.Source.Route.Hops[0]
			}
			if kind == "different_ingress" {
				r.Gateway.Hops[0].Reference = "another-gateway"
				r.Gateway.Hops[0].Alias = "another-gateway"
				r.Gateway.Hops[0].HostName = "another.example"
			} else {
				r.Source.Origin.User = "another-account"
				r.Source.Origin.ID = sshremote.OriginID(r.Source.Origin.MachineID, r.Source.Origin.User, r.Source.Origin.Root)
				r.Source.Profile.ID = sshremote.ProfileID(r.Source.Origin.ID, r.Source.Profile.Alias)
				r.Source.RouteProfiles[0].OriginID = r.Source.Origin.ID
				r.Source.RouteProfiles[0].ProfileID = r.Source.Profile.ID
			}
			b, e := PlanFleetImport(r)
			if e != nil {
				t.Fatal("individual empty-disk plan failed", e)
			}
			if e = ValidateFleetImportBatch([]FleetImportPlan{a, b}); e == nil {
				t.Fatal("cross-scope batch accepted")
			}
		})
	}
}
