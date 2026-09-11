package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshOnboardItem struct {
	Target     sshflow.OnboardTarget      `json:"target"`
	Candidate  *sshdiscovery.Candidate    `json:"candidate,omitempty"`
	KeyPlan    *sshhost.KeyPlan           `json:"key_plan,omitempty"`
	Options    sshSetupOptions            `json:"-"`
	Group      string                     `json:"group,omitempty"`
	Definition *sshhost.ManagedDefinition `json:"definition,omitempty"`
}

func runSSHOnboarding(ctx context.Context, app *App, args []string, options sshSetupOptions) error {
	if _, native := app.In.(fileDescriptor); !native {
		copy := *app
		copy.In = bufio.NewReader(app.In)
		app = &copy
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.json || !app.interactive() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, sshNoninteractiveSetupTimeout)
		defer cancel()
	}
	return withSSHMachineJSON(app, options.json, "ssh_onboarding_result", func(a *App) error { return runSSHOnboardingOperation(ctx, a, args, options) })
}

func runSSHOnboardingOperation(ctx context.Context, app *App, args []string, options sshSetupOptions) error {
	if options.dryRun {
		ctx = context.WithValue(ctx, sshDiscoveryReadOnlyKey{}, true)
		if len(args) == 0 && options.from == "" {
			return asUsageError(errors.New("--dry-run requires an explicit alias; choose --from to inspect a discovery candidate"))
		}
	}
	if options.auth != "" && options.auth != "existing" {
		return asUsageError(errors.New("--auth must be existing; use --key or --generate-key for public-key bootstrap"))
	}
	if options.key != "" && options.generateKey || options.auth != "" && (options.key != "" || options.generateKey) {
		return asUsageError(errors.New("choose one authentication mode"))
	}
	if options.configOnly && (options.auth != "" || options.key != "" || options.generateKey || options.to != "" || options.fleet) {
		return asUsageError(errors.New("--config-only cannot install keys, authenticate or register providers"))
	}
	if options.fleet {
		if options.to != "" && options.to != "fleet" {
			return asUsageError(errors.New("--fleet conflicts with --to"))
		}
		options.to = "fleet"
	}
	if options.fleetName != "" && options.to != "fleet" && options.to != "both" {
		return asUsageError(errors.New("--fleet-name requires a fleet destination"))
	}
	if options.herdrLabel != "" && options.to != "herdr" && options.to != "both" {
		return asUsageError(errors.New("--herdr-label requires a Herdr destination"))
	}
	if !options.generateKey && (options.keyPath != "" || options.comment != "" || options.noPassphrase) {
		return asUsageError(errors.New("key generation options require --generate-key"))
	}
	var items []sshOnboardItem
	var err error
	if len(args) == 0 && options.from == "" {
		if options.json || !app.interactive() {
			return asUsageError(errors.New("specify an alias outside an interactive terminal"))
		}
		items, err = sshOnboardingWizard(ctx, app)
	} else {
		var c *sshdiscovery.Candidate
		if options.from != "" {
			value, e := resolveSSHDiscoveryTarget(ctx, app, options.from)
			if e != nil {
				return e
			}
			c = &value
		}
		alias := ""
		if len(args) > 0 {
			alias = args[0]
		} else if app.interactive() && !options.json {
			alias, err = newPrompter(app).line("SSH alias", suggestSSHAlias(*c))
		} else {
			return asUsageError(errors.New("specify an alias outside an interactive terminal"))
		}
		if err != nil {
			return err
		}
		item, e := prepareSSHOnboardItem(ctx, app, alias, c, options)
		err = e
		items = []sshOnboardItem{item}
	}
	if err != nil {
		return err
	}
	return applySSHOnboardItems(ctx, app, items, options.dryRun, options.yes, options.json)
}

func resolveSSHDiscoveryTarget(ctx context.Context, app *App, from string) (sshdiscovery.Candidate, error) {
	source, selector, ok := strings.Cut(from, ":")
	if !ok {
		return sshdiscovery.Candidate{}, asUsageError(errors.New("--from must be a discovery candidate ID, tailscale:<peer> or lan:<ip:port>"))
	}
	if source == "tailscale" {
		report, err := runSSHDiscovery(ctx, app, "tailscale", sshdiscovery.LANRequest{}, false, true)
		if err != nil {
			return sshdiscovery.Candidate{}, err
		}
		// Candidate IDs already contain their source prefix. Try the full
		// selector before interpreting the remainder as a name or address.
		for _, candidate := range report.Candidates {
			if candidate.ID == from {
				return candidate, nil
			}
		}
		return sshdiscovery.ResolveCandidate(report.Candidates, selector)
	}
	if source != "lan" {
		return sshdiscovery.Candidate{}, asUsageError(errors.New("unknown discovery source"))
	}
	reports, cacheErr := sshdiscovery.ReadCache(ctx, sshDiscoveryCacheDir(), nowSSH())
	// An exact cached ID explicitly chooses that source's recorded identity,
	// even if its observation is old. Carry staleness into the setup preview;
	// this is metadata provenance, never an SSH authentication proof.
	for _, report := range reports {
		if report.Source != "lan" {
			continue
		}
		for _, candidate := range report.Candidates {
			if candidate.ID == from || candidate.ID == selector {
				if report.Stale {
					candidate.State = "stale_observation"
				}
				return candidate, nil
			}
		}
	}
	host, portText, err := net.SplitHostPort(selector)
	if err != nil {
		if cacheErr != nil {
			return sshdiscovery.Candidate{}, fmt.Errorf("cannot select a cached LAN identity: %w", cacheErr)
		}
		return sshdiscovery.Candidate{}, asUsageError(errors.New("LAN target must be an exact cached candidate ID or explicit ip:port"))
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return sshdiscovery.Candidate{}, asUsageError(errors.New("LAN target must use a literal IP"))
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return sshdiscovery.Candidate{}, asUsageError(errors.New("invalid LAN port"))
	}
	var matched []sshdiscovery.Candidate
	var matchedReports []sshdiscovery.Report
	seen := map[string]bool{}
	for _, report := range reports {
		if report.Source != "lan" {
			continue
		}
		for _, candidate := range report.Candidates {
			if candidate.Port == port {
				for _, text := range candidate.Addresses {
					ip, parseErr := netip.ParseAddr(text)
					if parseErr == nil && ip.Unmap() == addr.Unmap() && !seen[candidate.ID] {
						seen[candidate.ID] = true
						matched = append(matched, candidate)
						matchedReports = append(matchedReports, report)
					}
				}
			}
		}
	}
	if len(matched) > 1 {
		return sshdiscovery.Candidate{}, fmt.Errorf("LAN endpoint matches %d recorded scopes; select an exact candidate ID from dev ssh discover: %w", len(matched), sshdiscovery.ErrAmbiguous)
	}
	if len(matched) == 1 && cacheErr == nil {
		report := matchedReports[0]
		current, scopeErr := app.sshDiscovery().CurrentLANScope(report.Scope)
		if scopeErr == nil && current && !report.Stale && report.Complete && report.Status == sshdiscovery.StatusReady {
			return matched[0], nil
		}
	}
	if len(matched) > 0 || cacheErr != nil {
		app.warnf("LAN cache does not establish a current interface observation; using the explicitly selected endpoint without reusing a cached machine identity. An exact candidate ID selects its recorded metadata.")
	}
	return sshdiscovery.Candidate{ID: from, Source: "lan", Scope: "explicit-endpoint", NativeID: selector, Name: host, Addresses: []string{addr.String()}, Port: port, State: "unobserved"}, nil
}

func prepareSSHOnboardItem(ctx context.Context, app *App, alias string, c *sshdiscovery.Candidate, o sshSetupOptions) (sshOnboardItem, error) {
	item := sshOnboardItem{Candidate: c, Options: o}
	if o.portChanged && (o.port < 1 || o.port > 65535) {
		return item, asUsageError(errors.New("--port must be between 1 and 65535"))
	}
	if _, err := parseSSHRemoteOS(o.targetOS); err != nil {
		return item, err
	}
	if _, err := parseSSHOSOverrides(o.hopOS); err != nil {
		return item, err
	}
	if o.key == "" && !o.generateKey && (len(o.hopOS) > 0 || o.installOnWorkingJump || o.windowsAdminAuthorizedKeys) {
		return item, errors.New("bootstrap route options require --key or --generate-key")
	}
	s, err := app.sshHosts()
	if err != nil {
		return item, err
	}
	inv, err := s.Discover(ctx)
	if err != nil {
		return item, err
	}
	class, definition, err := classifySetupAlias(s, alias, inv)
	if err != nil {
		return item, err
	}
	if class == "foreign" && o.connectionChanged {
		return item, errors.New("connection-field flags cannot rewrite a foreign alias; choose a new alias")
	}
	if class == "foreign" {
		hints, e := s.ConnectionHints(ctx)
		if e != nil {
			return item, e
		}
		for _, hint := range hints {
			if strings.EqualFold(hint.Alias, alias) {
				definition = sshhost.ManagedDefinition{Alias: alias, HostName: hint.HostName, User: hint.User, Port: hint.Port}
			}
		}
		if c != nil && !sshCandidateAddressMatches(definition.HostName, *c) {
			return item, errors.New("foreign alias does not have a known matching discovery endpoint; choose a new alias or explicitly link its machine identity")
		}
	}
	snapshot, err := app.machineStore().Read(ctx)
	if err != nil {
		return item, err
	}
	knownIDs := map[string]bool{}
	if b, ok := snapshot.LookupBinding("ssh", s.Paths().RootConfig, strings.ToLower(alias)); ok && !b.Suppressed {
		knownIDs[b.MachineID] = true
	}
	if c != nil {
		if b, ok := snapshot.LookupBinding(c.Source, c.Scope, c.NativeID); ok && !b.Suppressed {
			knownIDs[b.MachineID] = true
		}
	}
	if o.machineID != "" {
		machine, ok := snapshot.Find(o.machineID)
		if !ok || machine.ID != o.machineID {
			return item, errors.New("select an existing active canonical machine UUID")
		}
		knownIDs[o.machineID] = true
	}
	if len(knownIDs) > 1 {
		return item, errors.New("selected alias and discovery identity belong to different machines; review an explicit machine merge first")
	}
	for id := range knownIDs {
		o.machineID = id
	}
	if class != "foreign" {
		if c != nil && !o.hostNameChanged {
			o.hostName = preferredSSHAddress(*c)
			o.hostNameChanged = true
		}
		if err := mergeManagedDefinition(app, &definition, o, !o.json && app.interactive()); err != nil {
			return item, err
		}
		if definition.User == "" && c != nil {
			if o.json || !app.interactive() {
				return item, errors.New("new discovery aliases require an explicit --user")
			}
			definition.User, err = newPrompter(app).line("Remote SSH user (confirm the account on this host)", os.Getenv("USER"))
			if err != nil {
				return item, err
			}
		}
		o.hostName = definition.HostName
		o.hostNameChanged = true
		o.user = definition.User
		o.userChanged = true
		o.port = definition.Port
		o.portChanged = definition.Port != 0
		o.connectionChanged = true
	}
	port := definition.Port
	if port == 0 && class != "foreign" {
		port = 22
		if c != nil && c.Port > 0 {
			port = c.Port
		}
	}
	if o.portChanged {
		port = o.port
	}
	if class != "foreign" {
		o.port = port
		o.portChanged = true
	}
	if port < 0 || port > 65535 || port == 0 && class != "foreign" {
		return item, errors.New("SSH port must be 1–65535")
	}
	if o.targetOS == "" && c != nil {
		switch strings.ToLower(c.OS) {
		case "linux", "macos", "darwin":
			o.targetOS = "posix"
		case "windows":
			o.targetOS = "windows"
		}
	}
	auth := "config"
	if o.auth == "existing" || o.to != "" {
		auth = "existing"
	}
	if o.key != "" || o.generateKey {
		auth = "key"
	}
	if o.targetOS == "" && (auth == "key" || o.to != "") && app.interactive() && !o.json {
		o.targetOS, err = newPrompter(app).line("Target OS (posix/windows)", "posix")
		if err != nil {
			return item, err
		}
	}
	label := alias
	advertises := false
	if c != nil {
		label = c.Name
		advertises = c.AdvertisesSSH
	}
	if label == "" {
		label = alias
	}
	item.Target = sshflow.OnboardTarget{Alias: alias, Label: label, HostName: definition.HostName, User: definition.User, Port: port, Auth: auth, To: o.to, RemoteOS: o.targetOS, MachineID: o.machineID, AdvertisesSSH: advertises}
	if _, err := sshflow.PlanOnboarding([]sshflow.OnboardTarget{item.Target}); err != nil {
		return item, err
	}
	if auth == "key" {
		if o.targetOS == "" {
			return item, errors.New("key bootstrap requires --target-os")
		}
		plan, err := planSSHSetupKey(ctx, app, s, o, !o.json && app.interactive())
		if err != nil {
			return item, err
		}
		if !plan.Ready() {
			return item, errors.New("selected key plan is blocked")
		}
		item.KeyPlan = &plan
		if class != "foreign" && !o.identityFileChanged && plan.IdentityFile != "" {
			definition.IdentityFile = plan.IdentityFile
		}
	}
	if class != "foreign" {
		definition.Port = port
		item.Definition = cloneSSHManagedDefinition(definition)
	}
	item.Options = o
	return item, nil
}

func sshCandidateAddressMatches(host string, c sshdiscovery.Candidate) bool {
	normalize := func(s string) string {
		if ip, e := netip.ParseAddr(s); e == nil {
			return ip.Unmap().String()
		}
		return strings.TrimSuffix(strings.ToLower(s), ".")
	}
	if host == "" {
		return false
	}
	key := normalize(host)
	for _, value := range append(append([]string{}, c.Addresses...), c.DNSName) {
		if value != "" && normalize(value) == key {
			return true
		}
	}
	return false
}

func preferredSSHAddress(c sshdiscovery.Candidate) string {
	for _, text := range c.Addresses {
		if ip, e := netip.ParseAddr(text); e == nil && ip.Is4() {
			return ip.String()
		}
	}
	if len(c.Addresses) > 0 {
		return c.Addresses[0]
	}
	return strings.TrimSuffix(c.DNSName, ".")
}
func suggestSSHAlias(c sshdiscovery.Candidate) string {
	value := strings.SplitN(c.DNSName, ".", 2)[0]
	if value == "" {
		value = c.Name
	}
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	alias := strings.Trim(out.String(), "-")
	if len(alias) > 100 {
		alias = strings.Trim(alias[:100], "-")
	}
	if sshhost.ValidateManagedAlias(alias) != nil {
		return "host"
	}
	return alias
}

func sshOnboardingWizard(ctx context.Context, app *App) ([]sshOnboardItem, error) {
	sources, err := sshPick(ctx, app, "Choose hosts to set up", []picker.Item{{Value: "tailscale", Label: "Tailscale peers"}, {Value: "lan", Label: "Discover LAN SSH hosts"}, {Value: "existing", Label: "Existing SSH aliases"}, {Value: "cached", Label: "Cached discovery"}}, false)
	if err != nil {
		return nil, err
	}
	var candidates []sshdiscovery.Candidate
	if sources[0].Value == "tailscale" || sources[0].Value == "lan" {
		report, e := runSSHDiscovery(ctx, app, sources[0].Value, sshdiscovery.LANRequest{Ports: []int{22}}, false, false)
		if e != nil && len(report.Candidates) == 0 {
			return nil, e
		}
		candidates = report.Candidates
	} else if sources[0].Value == "cached" {
		reports, e := sshdiscovery.ReadCache(ctx, sshDiscoveryCacheDir(), nowSSH())
		if e != nil {
			return nil, e
		}
		for _, r := range reports {
			stale := r.Stale
			if r.Source == "lan" {
				current, e := app.sshDiscovery().CurrentLANScope(r.Scope)
				stale = stale || e != nil || !current
			}
			for _, c := range r.Candidates {
				if stale {
					c.State = "stale_observation"
				}
				candidates = append(candidates, c)
			}
		}
	} else {
		s, e := app.sshHosts()
		if e != nil {
			return nil, e
		}
		hints, e := s.ConnectionHints(ctx)
		if e != nil {
			return nil, e
		}
		for _, h := range hints {
			candidates = append(candidates, sshdiscovery.Candidate{ID: "ssh:" + h.Alias, Source: "ssh", NativeID: h.Alias, Name: h.Alias, DNSName: h.Alias, Addresses: []string{h.HostName}, Port: h.Port})
		}
	}
	choices := []picker.Item{}
	for i, c := range candidates {
		status := c.State
		if c.Online != nil {
			if *c.Online {
				status += " online"
			} else {
				status += " offline"
			}
		}
		if c.AdvertisesSSH {
			status += " · Tailscale SSH advertised"
		}
		choices = append(choices, picker.Item{Value: strconv.Itoa(i), Label: c.Name, Description: strings.Join(c.Addresses, ",") + " " + c.OS + " " + status})
	}
	if len(choices) == 0 {
		return nil, errors.New("no candidates; check discovery source status")
	}
	picked, err := sshPick(ctx, app, "Select machines (aliases remain independently configurable)", choices, true)
	if err != nil {
		return nil, err
	}
	items := []sshOnboardItem{}
	for _, choice := range picked {
		i, _ := strconv.Atoi(choice.Value)
		c := candidates[i]
		aliases, e := newPrompter(app).line("SSH aliases (comma-separated)", suggestSSHAlias(c))
		if e != nil {
			return nil, e
		}
		for _, name := range strings.Split(aliases, ",") {
			alias := strings.TrimSpace(name)
			o := sshSetupOptions{herdrSession: "default"}
			s, e := app.sshHosts()
			if e != nil {
				return nil, e
			}
			inv, e := s.Discover(ctx)
			if e != nil {
				return nil, e
			}
			found, exists := inv.Find(alias)
			foreign := exists && aliasOwnership(found) == "foreign"
			var candidate *sshdiscovery.Candidate
			if c.Source != "ssh" {
				candidate = &c
			}
			if !foreign {
				address, e := newPrompter(app).line("HostName (IP or MagicDNS FQDN)", preferredSSHAddress(c))
				if e != nil {
					return nil, e
				}
				o.hostName = address
				o.hostNameChanged = true

				user, e := newPrompter(app).line("Default remote user for "+alias, os.Getenv("USER"))
				if e != nil {
					return nil, e
				}
				o.user = user
				o.userChanged = true
				port := c.Port
				if port == 0 {
					port = 22
				}
				portText, e := newPrompter(app).line("SSH port", strconv.Itoa(port))
				if e != nil {
					return nil, e
				}
				o.port, e = strconv.Atoi(portText)
				if e != nil {
					return nil, errors.New("invalid SSH port")
				}
				o.portChanged = true
			} else {
				fmt.Fprintf(app.Out, "Reuse %s with its existing OpenSSH connection settings.\n", alias)
			}
			auth, e := sshPick(ctx, app, "Authentication for "+alias, []picker.Item{{Value: "config", Label: "Configure only"}, {Value: "existing", Label: "Use existing authentication (including Tailscale SSH)"}, {Value: "key", Label: "Install an existing public key"}, {Value: "generate", Label: "Generate and install a new key"}}, false)
			if e != nil {
				return nil, e
			}
			switch auth[0].Value {
			case "config":
				o.configOnly = true
			case "existing":
				o.auth = "existing"
			case "key":
				o.key, e = newPrompter(app).line("Key or public key path", filepath.Join(s.Paths().Home, ".ssh", "id_ed25519"))
			case "generate":
				o.generateKey = true
				home, _ := os.UserHomeDir()
				o.keyPath, e = newPrompter(app).line("New key path", filepath.Join(home, ".ssh", "id_ed25519_dev_"+alias))
			}
			if e != nil {
				return nil, e
			}
			if !o.configOnly {
				dest, e := sshPick(ctx, app, "Optional registration", []picker.Item{{Value: "none", Label: "No registration"}, {Value: "fleet", Label: "dev fleet"}, {Value: "herdr", Label: "Herdr"}, {Value: "both", Label: "Fleet and Herdr"}}, false)
				if e != nil {
					return nil, e
				}
				o.to = dest[0].Value
				if o.to == "none" {
					o.to = ""
				}
				if o.to == "herdr" || o.to == "both" {
					o.herdrSession, e = newPrompter(app).line("Herdr session", "default")
					if e != nil {
						return nil, e
					}
					o.herdrLabel, e = newPrompter(app).line("Herdr label", alias)
					if e != nil {
						return nil, e
					}
				}
				if o.to == "fleet" || o.to == "both" {
					o.fleetName, e = newPrompter(app).line("Fleet name", alias)
					if e != nil {
						return nil, e
					}
				}
			}
			item, e := prepareSSHOnboardItem(ctx, app, alias, candidate, o)
			if e != nil {
				return nil, e
			}
			if c.Source == "ssh" {
				item.Group = sshflow.ReferenceID("ssh", s.Paths().RootConfig, strings.ToLower(c.NativeID))
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func applySSHOnboardItems(ctx context.Context, app *App, items []sshOnboardItem, dryRun, yes, jsonOut bool) error {
	targets := []sshflow.OnboardTarget{}
	byAlias := map[string]sshOnboardItem{}
	for _, item := range items {
		targets = append(targets, item.Target)
		byAlias[item.Target.Alias] = item
	}
	plan, err := sshflow.PlanOnboarding(targets)
	if err != nil {
		return err
	}
	s, err := app.sshHosts()
	if err != nil {
		return err
	}
	guard, err := sshflow.GuardSources(ctx, s)
	if err != nil {
		return err
	}
	initialRegistry, err := app.machineStore().Read(ctx)
	if err != nil {
		return err
	}
	initPlan := sshhost.InitPlan{Action: sshhost.ActionNoop, Path: s.Paths().RootConfig}
	for _, item := range items {
		if item.Definition != nil {
			initPlan, err = s.PlanInit(ctx)
			break
		}
	}
	if err != nil {
		return err
	}
	if initPlan.Action == sshhost.ActionBlocked {
		return errors.New("managed Include initialization is blocked; inspect dev ssh init")
	}
	preview := struct {
		sshflow.OnboardPlan
		Init        sshhost.InitPlan `json:"init"`
		Connections []sshOnboardItem `json:"connections"`
	}{plan, initPlan, items}
	if dryRun {
		if jsonOut {
			return writeSSHJSON(app, preview)
		}
		renderSSHOnboardPreview(app, items, initPlan)
		return nil
	}
	if !yes {
		if jsonOut || !app.interactive() {
			return errors.New("--yes is required to apply source-aware setup outside an interactive terminal")
		}
		renderSSHOnboardPreview(app, items, initPlan)
		confirmed, e := newPrompter(app).confirm("Apply these configurations, machine mappings and selected remote actions?", false)
		if e != nil {
			return e
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	if err := withSSHOperationLock(ctx, app, func() error {
		if err := guard.Check(ctx); err != nil {
			return err
		}
		current, e := app.machineStore().Read(ctx)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(initialRegistry, current) {
			return errors.New("machine registry changed since preview")
		}
		var freshPeers *sshdiscovery.Report
		for _, item := range items {
			if item.Candidate != nil && item.Candidate.Source == "tailscale" {
				if freshPeers == nil {
					report, e := app.sshDiscovery().Tailscale(ctx)
					if e != nil {
						return e
					}
					freshPeers = &report
				}
				matched := false
				for _, peer := range freshPeers.Candidates {
					if peer.NativeID == item.Candidate.NativeID && peer.Scope == item.Candidate.Scope {
						matched = reflect.DeepEqual(peer.Addresses, item.Candidate.Addresses) && peer.DNSName == item.Candidate.DNSName && peer.AdvertisesSSH == item.Candidate.AdvertisesSSH
					}
				}
				if !matched {
					return errors.New("selected Tailscale identity or endpoint changed since preview")
				}
			}
		}
		if initPlan.Action != sshhost.ActionNoop {
			if _, e := s.ApplyInit(ctx, initPlan); e != nil {
				return e
			}
		}
		guard, e = guard.Advance(ctx, []string{s.Paths().RootConfig})
		return e
	}); err != nil {
		return err
	}
	groups := map[string]string{}
	for _, b := range initialRegistry.Bindings {
		if !b.Suppressed {
			groups[sshflow.ReferenceID(b.Provider, b.Scope, b.NativeID)] = b.MachineID
		}
	}
	keyResults := map[string]sshhost.KeyResult{}
	bootstraps := map[string]sshhost.BootstrapResult{}
	registrations := map[string]sshflow.Result{}
	result, applyErr := sshflow.ApplyOnboarding(ctx, plan, func(ctx context.Context, target sshflow.OnboardTarget, stage string) error {
		item := byAlias[target.Alias]
		switch stage {
		case "configure":
			return withSSHOperationLock(ctx, app, func() error {
				if e := guard.Check(ctx); e != nil {
					return e
				}
				o := item.Options
				if item.KeyPlan != nil {
					key, e := s.ApplyKey(ctx, *item.KeyPlan)
					keyResults[target.Alias] = key
					if e != nil {
						return e
					}
					if item.Definition != nil && !o.identityFileChanged && item.KeyPlan.IdentityFile != "" {
						o.identityFile = item.KeyPlan.IdentityFile
						o.identityFileChanged = true
					}
				}
				o.yes = true
				o.fleet = false
				o.fleetName = ""
				o.to = ""
				o.from = ""
				o.auth = ""
				o.machineID = ""
				o.dryRun = false
				o.json = jsonOut
				o.configOnly = true
				o.key = ""
				o.generateKey = false
				o.targetOS = ""
				o.hopOS = nil
				o.installOnWorkingJump = false
				o.windowsAdminAuthorizedKeys = false
				var captured bytes.Buffer
				copy := *app
				if jsonOut {
					copy.Out = &captured
				}
				if e := runSSHSetupOperation(ctx, &copy, target.Alias, o); e != nil {
					return e
				}
				if item.Definition == nil {
					return guard.Check(ctx)
				}
				path, e := s.Paths().ManagedPath(target.Alias)
				if e != nil {
					return e
				}
				guard, e = guard.Advance(ctx, []string{path})
				return e
			})
		case "authenticate":
			return withSSHOperationLock(ctx, app, func() error {
				if e := guard.Check(ctx); e != nil {
					return e
				}
				if target.Auth == "existing" {
					probe, e := s.Probe(ctx, target.Alias)
					if e != nil {
						return e
					}
					if !probe.Ready {
						return errors.New("ordinary SSH login was not verified; run ssh " + target.Alias + " to complete native interaction")
					}
					return guard.Check(ctx)
				}
				osType, e := parseSSHRemoteOS(target.RemoteOS)
				if e != nil {
					return e
				}
				overrides, e := parseSSHOSOverrides(item.Options.hopOS)
				if e != nil {
					return e
				}
				route, e := s.ResolveRoute(ctx, sshhost.RouteRequest{Alias: target.Alias, TargetRemoteOS: osType, OSOverrides: overrides})
				if e != nil {
					return e
				}
				route, overrides, e = completeSSHRouteOS(ctx, app, s, route, target.Alias, osType, overrides, !jsonOut && app.interactive())
				if e != nil {
					return e
				}
				if e := guard.Check(ctx); e != nil {
					return e
				}
				bootstrap, e := s.Bootstrap(ctx, sshhost.BootstrapRequest{Alias: target.Alias, Route: route, Key: keyResults[target.Alias], TargetRemoteOS: osType, OSOverrides: overrides, Interactive: !jsonOut && app.interactive(), InstallOnWorkingJump: item.Options.installOnWorkingJump, AllowWindowsAdminAuthorizedKeys: item.Options.windowsAdminAuthorizedKeys})
				bootstraps[target.Alias] = bootstrap
				for _, hop := range bootstrap.Hops {
					if hop.Unknown {
						return errors.Join(sshflow.ErrOnboardUnknown, e)
					}
				}
				if e != nil {
					return e
				}
				if e := guard.Check(ctx); e != nil {
					return e
				}
				if !bootstrap.Ready {
					return errors.New("SSH key bootstrap is partial; retained key/configuration and per-hop results are reported")
				}
				return nil
			})
		case "bind":
			return withSSHOperationLock(ctx, app, func() error {
				if e := guard.Check(ctx); e != nil {
					return e
				}
				hints, e := s.ConnectionHints(ctx)
				if e != nil {
					return e
				}
				var bindings []machineregistry.Binding
				for _, hint := range hints {
					if strings.EqualFold(hint.Alias, target.Alias) {
						bindings = append(bindings, machineregistry.Binding{Provider: "ssh", Scope: s.Paths().RootConfig, NativeID: strings.ToLower(hint.Alias), Fingerprint: hint.Fingerprint})
					}
				}
				if len(bindings) == 0 {
					return errors.New("configured alias disappeared before registry binding")
				}
				groupKey := sshflow.ReferenceID("ssh", bindings[0].Scope, bindings[0].NativeID)
				if item.Group != "" {
					groupKey = item.Group
				}
				if item.Candidate != nil {
					c := item.Candidate
					groupKey = sshflow.ReferenceID(c.Source, c.Scope, c.NativeID)
					bindings = append(bindings, machineregistry.Binding{Provider: c.Source, Scope: c.Scope, NativeID: c.NativeID})
				}
				id := target.MachineID
				if id == "" {
					id = groups[groupKey]
				}
				r := machineregistry.Request{Action: "adopt", Label: target.Label, Bindings: bindings}
				if id != "" {
					r.Action = "link"
					r.MachineID = id
					r.Label = ""
				}
				p, e := app.machineStore().Plan(ctx, r)
				if e != nil {
					return e
				}
				applied, applyErr := app.machineStore().Apply(ctx, p)
				if applied.Status == "unknown" {
					return errors.Join(sshflow.ErrOnboardUnknown, applyErr)
				}
				if e = applyErr; e != nil {
					return e
				}
				groups[groupKey] = p.MachineID
				return nil
			})
		case "herdr", "fleet":
			management, e := app.sshManagement()
			if e != nil {
				return e
			}
			p, e := management.Plan(ctx, sshflow.Request{Action: "register", To: stage, Aliases: []string{target.Alias}, RemoteOS: target.RemoteOS, FleetName: item.Options.fleetName, HerdrLabel: item.Options.herdrLabel, Session: item.Options.herdrSession})
			if e != nil {
				return e
			}
			if e := guard.Check(ctx); e != nil {
				return e
			}
			applied, e := management.Apply(ctx, p, !jsonOut && app.interactive())
			registrations[target.Alias+"/"+stage] = applied
			for _, outcome := range applied.Outcomes {
				if outcome.Status == "unknown" {
					return errors.Join(sshflow.ErrOnboardUnknown, e)
				}
			}
			return e
		default:
			return errors.New("unknown onboarding stage")
		}
	})
	if jsonOut {
		if e := writeSSHJSON(app, struct {
			sshflow.OnboardResult
			Keys          map[string]sshhost.KeyResult       `json:"keys,omitempty"`
			Bootstraps    map[string]sshhost.BootstrapResult `json:"bootstraps,omitempty"`
			Registrations map[string]sshflow.Result          `json:"registrations,omitempty"`
		}{result, keyResults, bootstraps, registrations}); e != nil {
			return e
		}
	} else {
		for _, outcome := range result.Outcomes {
			fmt.Fprintf(app.Out, "%s %s: %s\n", outcome.Alias, outcome.Stage, outcome.Status)
			if outcome.Error != "" {
				fmt.Fprintln(app.Err, outcome.Error)
			}
			if outcome.Stage == "authenticate" {
				if bootstrap, ok := bootstraps[outcome.Alias]; ok {
					for _, hop := range bootstrap.Hops {
						fmt.Fprintf(app.Out, "  %s: %s (installed=%t, verified=%t, %s)\n", hop.Alias, hop.Status, hop.Installed, hop.Verified, hop.Code)
					}
				}
			}
		}
	}
	return applyErr
}

func renderSSHOnboardPreview(app *App, items []sshOnboardItem, init sshhost.InitPlan) {
	fmt.Fprintln(app.Out, "SSH setup plan")
	if init.Action != sshhost.ActionNoop {
		fmt.Fprintf(app.Out, "  Initialize managed Include in %s\n", init.Path)
	}
	table := app.newTable("ALIAS", "HOSTNAME", "USER", "PORT", "AUTH", "REGISTER", "MACHINE")
	for _, item := range items {
		t := item.Target
		machine := t.MachineID
		if machine == "" {
			machine = "new / same selected peer"
		}
		port := strconv.Itoa(t.Port)
		if t.Port == 0 {
			port = "native"
		}
		table.Add(t.Alias, dash(t.HostName), dash(t.User), port, t.Auth, dash(t.To), machine)
	}
	table.Render(app.Out)
	for _, item := range items {
		if definition := item.Definition; definition != nil {
			if definition.ProxyJump != "" {
				fmt.Fprintf(app.Out, "  %s ProxyJump: %s\n", item.Target.Alias, definition.ProxyJump)
			}
			if definition.IdentityFile != "" {
				fmt.Fprintf(app.Out, "  %s IdentityFile: %s\n", item.Target.Alias, definition.IdentityFile)
			}
			if definition.IdentitiesOnly != nil {
				fmt.Fprintf(app.Out, "  %s IdentitiesOnly: %t\n", item.Target.Alias, *definition.IdentitiesOnly)
			}
		}
		if item.Candidate != nil && item.Candidate.State == "stale_observation" {
			fmt.Fprintf(app.Out, "  %s: explicitly selected stale discovery observation\n", item.Target.Alias)
		}
		if p := item.KeyPlan; p != nil {
			fmt.Fprintf(app.Out, "  %s key: %s %s %s\n", item.Target.Alias, p.Operation, p.IdentityFile, p.Fingerprint)
		}
		if item.Target.To == "herdr" || item.Target.To == "both" {
			fmt.Fprintf(app.Out, "  %s: Herdr may install/start its remote server; native approvals remain interactive.\n", item.Target.Alias)
		}
	}
}

func nowSSH() time.Time { return time.Now() }
