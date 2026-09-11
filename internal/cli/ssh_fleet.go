package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

func sshFleetCacheDir() string { return filepath.Join(sshDiscoveryCacheDir(), "fleet") }
func (a *App) sshRemotes() sshremote.Client {
	runner := a.sshRemoteRunner
	if runner == nil {
		runner = fleet.Transport{Err: a.Err, StdinLimit: sshremote.MaxRequestBytes, StdoutLimit: sshremote.MaxResponseBytes}
	}
	return sshremote.Client{Runner: runner, AllowPrompt: a.interactive()}
}

func sshFleetHost(app *App, name string) (fleet.Host, error) {
	config, err := loadFleetConfig(app)
	if err != nil {
		return fleet.Host{}, err
	}
	for _, host := range config.Hosts {
		if host.Name == name {
			return host, nil
		}
	}
	return fleet.Host{}, fmt.Errorf("fleet host %q not found", name)
}

type sshFleetSource struct {
	Host      string               `json:"host"`
	Status    string               `json:"status"`
	Stale     bool                 `json:"stale"`
	Inventory *sshremote.Inventory `json:"inventory,omitempty"`
	ErrorCode string               `json:"error_code,omitempty"`
}
type sshFleetDiscovery struct {
	SchemaVersion int              `json:"schema_version"`
	Kind          string           `json:"kind"`
	Complete      bool             `json:"complete"`
	Sources       []sshFleetSource `json:"sources"`
}

func selectSSHFleetHosts(ctx context.Context, app *App, names []string) ([]fleet.Host, error) {
	config, err := loadFleetConfig(app)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		if !app.interactive() {
			return nil, errors.New("fleet discovery requires at least one --host outside an interactive terminal")
		}
		choices := make([]picker.Item, 0, len(config.Hosts))
		for _, host := range config.Hosts {
			choices = append(choices, picker.Item{Value: host.Name, Label: host.Name, Description: host.Destination() + " · " + host.EffectiveRemoteOS()})
		}
		if len(choices) == 0 {
			return nil, errors.New("no fleet hosts configured")
		}
		picked, err := sshPick(ctx, app, "Choose fleet sources (no recursive exploration)", choices, true)
		if err != nil {
			return nil, err
		}
		for _, choice := range picked {
			names = append(names, choice.Value)
		}
	}
	if len(names) > 16 {
		return nil, errors.New("select at most 16 fleet sources per discovery")
	}
	seen := map[string]bool{}
	var hosts []fleet.Host
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		found := false
		for _, host := range config.Hosts {
			if host.Name == name {
				hosts = append(hosts, host)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("fleet host %q not found", name)
		}
	}
	return hosts, nil
}

func discoverSSHFleet(ctx context.Context, app *App, names []string, refresh bool) (sshFleetDiscovery, error) {
	report := sshFleetDiscovery{SchemaVersion: 1, Kind: "ssh_fleet_discovery", Complete: true, Sources: []sshFleetSource{}}
	hosts, err := selectSSHFleetHosts(ctx, app, names)
	if err != nil {
		return report, err
	}
	client := app.sshRemotes()
	for _, host := range hosts {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		source := sshFleetSource{Host: host.Name, Status: "ready"}
		cached, found, cacheErr := sshremote.LoadCache(ctx, sshFleetCacheDir(), host, time.Now())
		if cacheErr == nil && found && !refresh && time.Since(cached.ObservedAt) <= sshremote.CacheTTL {
			source.Inventory = &cached
		} else {
			inv, err := client.Inventory(ctx, host)
			if err != nil {
				source.Status = sshFleetError(err)
				source.ErrorCode = source.Status
				report.Complete = false
				if found && cacheErr == nil {
					source.Inventory = &cached
					source.Stale = true
				}
			} else {
				source.Inventory = &inv
				if err := sshremote.SaveCache(ctx, sshFleetCacheDir(), host, inv); err != nil {
					app.warnf("remote SSH cache could not be saved: %v", err)
				}
			}
		}
		if source.Inventory != nil && !source.Inventory.Complete {
			report.Complete = false
			if source.Status == "ready" {
				source.Status = "partial"
			}
		}
		report.Sources = append(report.Sources, source)
	}
	return report, nil
}

func sshFleetError(err error) string {
	switch {
	case errors.Is(err, sshremote.ErrInteractionRequired):
		return "interaction-required"
	case errors.Is(err, sshremote.ErrSourceChanged):
		return "source-changed"
	case errors.Is(err, sshremote.ErrUnavailable):
		return "no-dev"
	case errors.Is(err, sshremote.ErrIncompatible):
		return "incompatible"
	case errors.Is(err, sshremote.ErrTimeout):
		return "timeout"
	case errors.Is(err, sshremote.ErrInvalidData):
		return "invalid-response"
	default:
		return "unreachable"
	}
}

func renderSSHFleetDiscovery(app *App, report sshFleetDiscovery) {
	table := app.newTable("SOURCE", "SELECTOR", "ALIAS", "ENDPOINT", "STATE")
	for _, source := range report.Sources {
		state := source.Status
		if source.Stale {
			state += " (stale)"
		}
		if source.Inventory == nil || len(source.Inventory.Profiles) == 0 {
			table.Add(source.Host, "—", "—", "—", state)
			continue
		}
		for _, profile := range source.Inventory.Profiles {
			table.Add(source.Host, sshremote.EncodeSelector(source.Host, profile.Alias), profile.Alias, dash(profile.HostName), profile.State+" · "+state)
		}
	}
	table.Render(app.Out)
}

func runSSHFleetDiscover(ctx context.Context, app *App, names []string, refresh, jsonOut bool) error {
	report, err := discoverSSHFleet(ctx, app, names, refresh)
	if jsonOut {
		if e := writeSSHJSON(app, report); e != nil {
			return e
		}
	} else {
		renderSSHFleetDiscovery(app, report)
	}
	return err
}

func runSSHListWithFleet(ctx context.Context, app *App, tailscale, lan, jsonOut bool) error {
	rows, cacheErr := sshremote.ReadCache(ctx, sshFleetCacheDir(), time.Now())
	if errors.Is(cacheErr, context.Canceled) || errors.Is(cacheErr, context.DeadlineExceeded) {
		return cacheErr
	}
	report := sshFleetDiscovery{SchemaVersion: 1, Kind: "ssh_fleet_discovery", Complete: cacheErr == nil, Sources: []sshFleetSource{}}
	if cacheErr != nil {
		app.warnf("some cached fleet SSH profiles could not be read; local aliases and independently valid cache entries are still shown")
	}
	config, err := loadFleetConfig(app)
	if err != nil {
		return err
	}
	endpoints := map[string]string{}
	for _, host := range config.Hosts {
		endpoints[host.Name] = fleet.EndpointID(host)
	}
	for _, row := range rows {
		stale := row.Stale || endpoints[row.HostName] != row.EndpointID
		status := "ready"
		if stale {
			status = "stale"
			report.Complete = false
		}
		if !row.Inventory.Complete {
			report.Complete = false
		}
		inv := row.Inventory
		report.Sources = append(report.Sources, sshFleetSource{Host: row.HostName, Status: status, Stale: stale, Inventory: &inv})
	}
	base := func(a *App) error {
		if tailscale || lan {
			return runSSHCombinedList(ctx, a, tailscale, lan, jsonOut, "")
		}
		return runSSHList(ctx, a, jsonOut, "")
	}
	if !jsonOut {
		if err := base(app); err != nil {
			return err
		}
		fmt.Fprintln(app.Out, "\nCached fleet SSH profiles:")
		renderSSHFleetDiscovery(app, report)
		return nil
	}
	var buffer bytes.Buffer
	copy := *app
	copy.Out = &buffer
	if err := base(&copy); err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &document); err != nil {
		return err
	}
	document["fleet_ssh_sources"] = report.Sources
	if cacheErr != nil {
		document["fleet_ssh_diagnostics"] = []map[string]any{{
			"code": "fleet_ssh_cache_incomplete", "incomplete": true,
			"message": "some cached fleet SSH profiles could not be read",
		}}
	}
	if !report.Complete {
		document["complete"] = false
	}
	return writeSSHJSON(app, document)
}

func resolveSSHFleetSelection(ctx context.Context, app *App, selector string, dryRun bool) (fleet.Host, sshremote.Resolved, error) {
	var host fleet.Host
	var resolved sshremote.Resolved
	hostName, alias, err := sshremote.ParseSelector(selector)
	if err != nil && strings.HasPrefix(selector, "fleet-ssh:") {
		rows, readErr := sshremote.ReadCache(ctx, sshFleetCacheDir(), time.Now())
		if readErr != nil {
			return host, resolved, readErr
		}
		for _, row := range rows {
			if p, ok := row.Inventory.Find(selector); ok {
				if hostName != "" {
					return host, resolved, errors.New("remote profile ID has multiple fleet sources; use an explicit fleet:host/alias selector")
				}
				hostName, alias = row.HostName, p.Alias
				err = nil
			}
		}
	}
	if err != nil {
		return host, resolved, err
	}
	host, err = sshFleetHost(app, hostName)
	if err != nil {
		return host, resolved, err
	}
	var inv sshremote.Inventory
	if dryRun {
		var found bool
		inv, found, err = sshremote.LoadCache(ctx, sshFleetCacheDir(), host, time.Now())
		if err != nil {
			return host, resolved, err
		}
		if !found {
			return host, resolved, errors.New("remote SSH source is unresolved; explicitly discover and resolve it before a cached dry-run")
		}
	} else {
		inv, err = app.sshRemotes().Inventory(ctx, host)
		if err != nil {
			return host, resolved, err
		}
		if e := sshremote.SaveCache(ctx, sshFleetCacheDir(), host, inv); e != nil {
			app.warnf("remote SSH cache: %v", e)
		}
	}
	profile, found := inv.Find(alias)
	if !found {
		return host, resolved, sshremote.ErrNotFound
	}
	selection := profile.Selection(inv.Origin)
	if dryRun {
		resolved, found, err = sshremote.LoadResolvedCache(ctx, sshFleetCacheDir(), host, selection, time.Now())
		if err != nil {
			return host, resolved, err
		}
		if !found {
			return host, resolved, errors.New("selected remote route has no cached resolution; dry-run will not contact the source")
		}
	} else {
		resolved, err = app.sshRemotes().Resolve(ctx, host, selection)
		if err != nil {
			return host, resolved, err
		}
		if e := sshremote.SaveResolvedCache(ctx, sshFleetCacheDir(), host, resolved); e != nil {
			app.warnf("remote route cache: %v", e)
		}
	}
	return host, resolved, nil
}
