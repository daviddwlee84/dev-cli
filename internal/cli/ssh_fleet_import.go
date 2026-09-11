package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

type sshFleetImportSource struct {
	Host     fleet.Host
	Resolved sshremote.Resolved
	Guard    *sshflow.SourceGuard
	Registry machineregistry.Snapshot
}

func prepareSSHFleetImportItem(ctx context.Context, app *App, alias string, host fleet.Host, source sshremote.Resolved, o sshSetupOptions) (sshOnboardItem, error) {
	if o.proxyJumpChanged || o.hostNameChanged {
		return sshOnboardItem{}, errors.New("fleet import derives HostName and ProxyJump from the reviewed source; choose an explicit local profile to override them")
	}
	if !source.Importable {
		return sshOnboardItem{}, errors.New("remote profile cannot be represented as a portable ProxyJump route; use dev ssh connect --on or an explicit local profile")
	}
	s, err := app.sshHosts()
	if err != nil {
		return sshOnboardItem{}, err
	}
	sourceGuard, err := sshflow.GuardSources(ctx, s)
	if err != nil {
		return sshOnboardItem{}, err
	}
	original := source
	// Invocation overrides are local intent, while original remains source authority.
	source.Route.Hops = append([]sshhost.RouteHop(nil), source.Route.Hops...)
	source.RouteProfiles = append([]sshremote.RouteProfile(nil), source.RouteProfiles...)
	n := len(source.Route.Hops) - 1
	if n < 0 {
		return sshOnboardItem{}, sshremote.ErrInvalidData
	}
	if o.userChanged {
		source.Effective.User = o.user
		source.Route.Hops[n].User = o.user
		source.RouteProfiles[n].Hop.User = o.user
	}
	if o.portChanged {
		source.Effective.Port = o.port
		source.Route.Hops[n].Port = o.port
		source.RouteProfiles[n].Hop.Port = o.port
	}
	o.hostName, o.hostNameChanged = source.Effective.HostName, true
	o.user, o.userChanged = source.Effective.User, true
	o.port, o.portChanged = source.Effective.Port, true
	o.connectionChanged = true
	item, err := prepareSSHOnboardItem(ctx, app, alias, nil, o)
	if err != nil {
		return item, err
	}
	if item.Definition == nil {
		return item, errors.New("fleet import requires a new or previously imported managed alias")
	}
	gateway, gatewayDefinition, err := sshFleetGateway(ctx, s, host, source.Origin, o.dryRun)
	if err != nil {
		return item, err
	}
	inv, err := s.Discover(ctx)
	if err != nil {
		return item, err
	}
	existing := []sshhost.ManagedDefinition{}
	for _, a := range inv.Aliases {
		if aliasOwnership(a) == "managed" {
			m, e := s.InspectManaged(a.Name)
			if e != nil {
				return item, e
			}
			existing = append(existing, m.Definition)
		}
	}
	for _, h := range inv.ConnectionHints {
		if h.State == "known" {
			found, _ := inv.Find(h.Alias)
			if aliasOwnership(found) != "managed" {
				existing = append(existing, sshhost.ManagedDefinition{Alias: h.Alias, HostName: h.HostName, User: h.User, Port: h.Port})
			}
		}
	}
	registry, err := app.machineStore().Read(ctx)
	if err != nil {
		return item, err
	}
	identity := item.Definition.IdentityFile
	if item.KeyPlan != nil {
		identity = item.KeyPlan.IdentityFile
	}
	overrides := map[string]string{}
	item.HopKeyPlans = map[string]sshhost.KeyPlan{}
	for _, value := range o.hopKeys {
		name, path, ok := strings.Cut(value, "=")
		if !ok || name == "" || path == "" {
			return item, errors.New("--hop-key requires local-alias=key-path")
		}
		if _, seen := overrides[name]; seen {
			return item, fmt.Errorf("duplicate hop key override %q", name)
		}
		kp, e := s.PlanKey(ctx, sshhost.KeyRequest{Operation: sshhost.KeyUse, Path: path})
		if e != nil {
			return item, e
		}
		if !kp.Ready() {
			return item, errors.New("hop key is blocked")
		}
		overrides[name] = kp.IdentityFile
		item.HopKeyPlans[name] = kp
	}
	imported, err := sshflow.PlanFleetImport(sshflow.FleetImportRequest{Alias: alias, Host: host, Source: source, Gateway: gateway, GatewayDefinition: gatewayDefinition, Existing: existing, Registry: registry, IdentityFile: identity, HopIdentityFiles: overrides})
	if err != nil {
		return item, err
	}
	for _, definition := range imported.Definitions {
		if definition.Alias == alias {
			item.Definition = cloneSSHManagedDefinition(definition)
		}
	}
	item.Import = &imported
	if err := sourceGuard.Check(ctx); err != nil {
		return item, err
	}
	item.ImportSource = &sshFleetImportSource{Host: host, Resolved: original, Guard: &sourceGuard, Registry: registry}
	item.Options.proxyJump, item.Options.proxyJumpChanged = item.Definition.ProxyJump, true
	item.Options.identityFile, item.Options.identityFileChanged = item.Definition.IdentityFile, true
	return item, nil
}

func sshFleetGateway(ctx context.Context, s *sshhost.Service, host fleet.Host, origin sshremote.Origin, readOnly bool) (sshhost.Route, *sshhost.ManagedDefinition, error) {
	alias := host.SSHAlias
	if alias != "" {
		// A different identity would require copying the gateway's complete native
		// authentication policy. Require the user-owned alias to express it instead.
		if host.IdentityFile != "" {
			if readOnly {
				managed, e := s.InspectManaged(alias)
				if e != nil || managed.Definition.IdentityFile != host.IdentityFile {
					return sshhost.Route{}, nil, errors.New("gateway identity override is unresolved in a read-only preview")
				}
			}
			var e sshhost.EffectiveConfig
			var err error
			if readOnly {
				e.IdentityFiles = []string{host.IdentityFile}
			} else {
				e, err = s.Effective(ctx, alias)
			}
			if err != nil {
				return sshhost.Route{}, nil, err
			}
			matched := false
			for _, path := range e.IdentityFiles {
				if path == host.IdentityFile {
					matched = true
				}
			}
			if !matched {
				return sshhost.Route{}, nil, errors.New("fleet gateway identity override is not represented by its SSH alias; configure that alias explicitly before import")
			}
		}
		route, err := planSSHLocalRoute(ctx, s, alias, nil, readOnly, sshhost.RouteInvocation{Alias: alias, User: host.User, Port: host.Port})
		if err != nil {
			return route, nil, err
		}
		last := len(route.Hops) - 1
		if host.User != "" {
			route.Hops[last].User = host.User
		}
		if host.Port != 0 {
			route.Hops[last].Port = host.Port
		}
		ref := alias
		if host.User != "" {
			ref = host.User + "@" + ref
		}
		if host.Port != 0 {
			ref += ":" + strconv.Itoa(host.Port)
		}
		route.Hops[last].Reference = ref
		return route, nil, nil
	}
	user := host.User
	if user == "" {
		user = origin.User
	}
	port := host.Port
	if port == 0 {
		port = 22
	}
	hop := sshhost.RouteHop{Alias: host.Name, HostName: host.Hostname, User: user, Port: port}
	alias = sshflow.ImportedHopAlias(host.Name, origin.ID, "gateway", hop, nil, host.IdentityFile)
	definition := sshhost.ManagedDefinition{Alias: alias, HostName: host.Hostname, User: user, Port: port, IdentityFile: host.IdentityFile}
	route, err := planSSHLocalRoute(ctx, s, alias, []sshhost.ManagedDefinition{definition}, readOnly)
	return route, &definition, err
}

func revalidateSSHFleetImport(ctx context.Context, app *App, item sshOnboardItem) error {
	if item.ImportSource == nil {
		return nil
	}
	source := item.ImportSource
	host, err := sshFleetHost(app, source.Host.Name)
	if err != nil {
		return err
	}
	if fleet.EndpointID(host) != fleet.EndpointID(source.Host) {
		return sshremote.ErrSourceChanged
	}
	fresh, err := app.sshRemotes().Resolve(ctx, host, source.Resolved.Profile.Selection(source.Resolved.Origin))
	if err != nil {
		return err
	}
	before := source.Resolved
	fresh.ObservedAt = before.ObservedAt
	if !reflect.DeepEqual(fresh, before) {
		return sshremote.ErrSourceChanged
	}
	return nil
}

func sshFleetOnboardingWizard(ctx context.Context, app *App) ([]sshOnboardItem, error) {
	hosts, err := selectSSHFleetHosts(ctx, app, nil)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, host := range hosts {
		names = append(names, host.Name)
	}
	report, err := discoverSSHFleet(ctx, app, names, true)
	if err != nil {
		return nil, err
	}
	choices := []picker.Item{}
	for _, source := range report.Sources {
		if source.Inventory == nil || source.Stale {
			continue
		}
		for _, p := range source.Inventory.Profiles {
			if p.State == "active" {
				choices = append(choices, picker.Item{Value: sshremote.EncodeSelector(source.Host, p.Alias), Label: source.Host + " / " + p.Alias, Description: p.HostName + " · " + p.User})
			}
		}
	}
	if len(choices) == 0 {
		return nil, errors.New("no active remote SSH profiles on the selected fleet sources")
	}
	selected, err := sshPick(ctx, app, "Select remote SSH profiles to import", choices, true)
	if err != nil {
		return nil, err
	}
	s, err := app.sshHosts()
	if err != nil {
		return nil, err
	}
	var items []sshOnboardItem
	for _, selection := range selected {
		host, resolved, e := resolveSSHFleetSelection(ctx, app, selection.Value, false)
		if e != nil {
			return nil, e
		}
		if !resolved.Importable {
			return nil, errors.New("selected remote profile cannot be imported; use dev ssh connect --on or an explicit local profile")
		}
		if _, _, e := sshFleetGateway(ctx, s, host, resolved.Origin, false); e != nil {
			return nil, e
		}
		alias, e := newPrompter(app).line("Local SSH alias", suggestSSHAliasForFleet(host.Name, resolved.Profile.Alias))
		if e != nil {
			return nil, e
		}
		o := sshSetupOptions{from: selection.Value, herdrSession: "default"}
		o.user, e = newPrompter(app).line("Remote user", resolved.Effective.User)
		if e != nil {
			return nil, e
		}
		o.userChanged = true
		port, e := newPrompter(app).line("Remote SSH port", strconv.Itoa(resolved.Effective.Port))
		if e != nil {
			return nil, e
		}
		o.port, e = strconv.Atoi(port)
		if e != nil {
			return nil, e
		}
		o.portChanged = true
		auth, e := sshPick(ctx, app, "Authentication for "+alias, []picker.Item{{Value: "config", Label: "Configure only"}, {Value: "existing", Label: "Verify existing authentication"}, {Value: "key", Label: "Install a local public key through the route"}, {Value: "generate", Label: "Generate and install a new local key"}}, false)
		if e != nil {
			return nil, e
		}
		switch auth[0].Value {
		case "config":
			o.configOnly = true
		case "existing":
			o.auth = "existing"
		case "key":
			o, e = selectSSHWizardKey(ctx, app, s, alias, o)
		case "generate":
			o.generateKey = true
			o.keyPath, e = newPrompter(app).line("New local key path", s.Paths().SSHDir+"/id_ed25519_dev_"+alias)
		}
		if e != nil {
			return nil, e
		}
		if o.hasExistingKey() || o.generateKey {
			if e = prepareSSHWizardKey(ctx, app, s, &o); e != nil {
				return nil, e
			}
		}
		if !o.configOnly {
			o.to, e = sshRegistrationDestinations(ctx, app, "Optional registration")
			if e != nil {
				return nil, e
			}
		}
		item, e := prepareSSHFleetImportItem(ctx, app, alias, host, resolved, o)
		if e != nil {
			return nil, e
		}
		if item.KeyPlan != nil {
			var hops []picker.Item
			for _, hop := range item.Import.Route {
				if !hop.Target {
					hops = append(hops, picker.Item{Value: hop.Alias, Label: hop.Alias, Description: hop.User + "@" + hop.HostName})
				}
			}
			if len(hops) > 0 {
				result, attempted, e := app.pick(ctx, picker.Request{Prompt: "Optional hop-specific keys (empty keeps the default)", Items: hops, Multi: true})
				if e != nil {
					return nil, e
				}
				if !attempted {
					return nil, errPromptCanceled
				}
				picked := result.Items
				for _, hop := range picked {
					choice, e := selectSSHWizardKey(ctx, app, s, hop.Value, sshSetupOptions{})
					if e != nil {
						return nil, e
					}
					if e = prepareSSHWizardKey(ctx, app, s, &choice); e != nil {
						return nil, e
					}
					if choice.keyPlan == nil {
						return nil, errors.New("hop key selection has no reviewed plan")
					}
					kp := *choice.keyPlan
					if item.HopKeyPlans == nil {
						item.HopKeyPlans = map[string]sshhost.KeyPlan{}
					}
					item.HopKeyPlans[hop.Value] = kp
				}
			}
		}
		for name, kp := range item.HopKeyPlans {
			for i, d := range item.Import.Definitions {
				if d.Alias == name {
					d.IdentityFile = kp.IdentityFile
					item.Import.Definitions[i] = d
					for n := range item.Import.Imports {
						if item.Import.Imports[n].LocalAlias == name {
							item.Import.Imports[n].DefinitionFingerprint = sshflow.DefinitionFingerprint(d)
						}
					}
					if name == alias {
						item.Definition = cloneSSHManagedDefinition(d)
					}
				}
			}
		}
		for _, d := range item.Import.Definitions {
			if d.Alias == alias {
				continue
			}
			existing, e := s.InspectManaged(d.Alias)
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
			if e == nil && !sameSSHDefinition(existing.Definition, d) {
				return nil, fmt.Errorf("hop key selection changes shared profile %q; choose a separate local profile", d.Alias)
			}
		}
		items = append(items, item)
	}
	return items, nil
}
func suggestSSHAliasForFleet(host, alias string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(host + "-" + alias) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	value := strings.Trim(out.String(), "-")
	if len(value) > 100 {
		value = value[:100]
	}
	if value == "" {
		value = "remote"
	}
	return value
}

func applySSHFleetImportConfiguration(ctx context.Context, app *App, s *sshhost.Service, item sshOnboardItem, guard *sshflow.SourceGuard) (map[string]sshhost.KeyResult, error) {
	keys := map[string]sshhost.KeyResult{}
	if e := revalidateSSHFleetImport(ctx, app, item); e != nil {
		return keys, e
	}
	if e := guard.Check(ctx); e != nil {
		return keys, e
	}
	recordPlan, e := app.machineStore().Plan(ctx, machineregistry.Request{Action: "record-imports", Imports: item.Import.Imports})
	if e != nil {
		return keys, e
	}
	if item.KeyPlan != nil {
		key, e := s.ApplyKey(ctx, *item.KeyPlan)
		if e != nil {
			return keys, e
		}
		keys[item.Target.Alias] = key
	}
	for alias, plan := range item.HopKeyPlans {
		if e := guard.Check(ctx); e != nil {
			return keys, e
		}
		key, e := s.ApplyKey(ctx, plan)
		if e != nil {
			return keys, e
		}
		keys[alias] = key
	}
	for _, definition := range item.Import.Definitions {
		if e := guard.Check(ctx); e != nil {
			return keys, e
		}
		p, e := s.PlanUpsert(ctx, definition)
		if e != nil {
			return keys, e
		}
		if !p.Ready() {
			return keys, sshhost.ErrBlocked
		}
		result, e := s.ApplyManaged(ctx, p)
		if e != nil {
			return keys, e
		}
		next, e := guard.Advance(ctx, []string{result.Path})
		if e != nil {
			return keys, e
		}
		*guard = next
	}
	result, e := app.machineStore().Apply(ctx, recordPlan)
	if e != nil || result.Status == "unknown" {
		return keys, errors.Join(sshflow.ErrOnboardUnknown, e)
	}
	return keys, nil
}
