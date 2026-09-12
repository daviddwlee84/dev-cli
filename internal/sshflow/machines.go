package sshflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

// MachineReference names source-owned intent. Its ID is a selector, never a
// remote authentication proof or a replacement for fleet's machine_id pin.
type MachineReference struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Scope       string `json:"scope"`
	NativeID    string `json:"native_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
	State       string `json:"state"`
}

type ConnectionProfile struct {
	ID          string           `json:"id"`
	Alias       string           `json:"alias"`
	HostName    string           `json:"host_name,omitempty"`
	User        string           `json:"user,omitempty"`
	Port        int              `json:"port,omitempty"`
	Fingerprint string           `json:"fingerprint,omitempty"`
	State       string           `json:"state"`
	Source      sshhost.Location `json:"source"`
}

type MachineRow struct {
	ID          string                   `json:"id"`
	MachineID   string                   `json:"machine_id,omitempty"`
	Label       string                   `json:"label"`
	State       string                   `json:"state"`
	References  []MachineReference       `json:"references"`
	Profiles    []ConnectionProfile      `json:"profiles"`
	Tailscale   []sshdiscovery.Candidate `json:"tailscale"`
	LAN         []sshdiscovery.Candidate `json:"lan"`
	Fleet       []FleetHost              `json:"fleet"`
	Herdr       []herdrremote.Profile    `json:"herdr"`
	Suggestions []string                 `json:"suggestions,omitempty"`
}

type MachineInventory struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	Complete      bool              `json:"complete"`
	ObservedAt    time.Time         `json:"observed_at"`
	Sources       map[string]string `json:"sources"`
	Machines      []MachineRow      `json:"machines"`
}

func ReferenceID(provider, scope, nativeID string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + scope + "\x00" + nativeID))
	return provider + ":" + hex.EncodeToString(sum[:16])
}

func sourceFingerprint(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func ReferenceBinding(ref MachineReference) machineregistry.Binding {
	return machineregistry.Binding{Provider: ref.Provider, Scope: ref.Scope, NativeID: ref.NativeID, Fingerprint: ref.Fingerprint}
}

// JoinMachines is a pure projection. Only explicit registry transactions create
// durable identity; lexical endpoint matches are labelled declared associations.
func JoinMachines(local Inventory, hints []sshhost.ConnectionHint, registry machineregistry.Snapshot, reports []sshdiscovery.Report, fleetScope, herdrScope string, now time.Time) MachineInventory {
	out := MachineInventory{SchemaVersion: 1, Kind: "ssh_machine_inventory", Complete: local.Complete, ObservedAt: now, Sources: map[string]string{"ssh": "ready", "fleet": local.FleetStatus, "herdr": local.Herdr.Status}, Machines: []MachineRow{}}
	if local.FleetStatus != "ready" || local.Herdr.Status != "ready" {
		out.Complete = false
	}
	rows := map[string]*MachineRow{}
	refIndexes := map[string]map[string]int{}
	newRow := func(id, label, machineID string) *MachineRow {
		if row := rows[id]; row != nil {
			return row
		}
		row := &MachineRow{ID: id, MachineID: machineID, Label: label, State: "candidate", References: []MachineReference{}, Profiles: []ConnectionProfile{}, Tailscale: []sshdiscovery.Candidate{}, LAN: []sshdiscovery.Candidate{}, Fleet: []FleetHost{}, Herdr: []herdrremote.Profile{}}
		if machineID != "" {
			row.State = "unresolved"
		}
		rows[id] = row
		refIndexes[id] = map[string]int{}
		return row
	}
	for _, machine := range registry.Machines {
		if machine.MergedInto == "" {
			newRow(machine.ID, machine.Label, machine.ID)
		}
	}
	bindings := map[string]machineregistry.Binding{}
	for _, binding := range registry.Bindings {
		id := ReferenceID(binding.Provider, binding.Scope, binding.NativeID)
		bindings[id] = binding
		if !binding.Suppressed {
			if row := rows[binding.MachineID]; row != nil {
				refIndexes[row.ID][id] = len(row.References)
				row.References = append(row.References, MachineReference{ID: id, Provider: binding.Provider, Scope: binding.Scope, NativeID: binding.NativeID, Fingerprint: binding.Fingerprint, State: "unresolved"})
			}
		}
	}
	attach := func(ref MachineReference, label string, suggested *MachineRow) *MachineRow {
		binding, bound := bindings[ref.ID]
		var row *MachineRow
		if bound && !binding.Suppressed {
			if binding.Fingerprint == ref.Fingerprint {
				row = rows[binding.MachineID]
			} else if old := rows[binding.MachineID]; old != nil {
				old.State = "stale"
				for i := range old.References {
					if old.References[i].ID == ref.ID {
						old.References[i].State = "stale"
					}
				}
			}
		}
		if row == nil && !bound {
			row = suggested
		}
		if row == nil {
			row = newRow(ref.ID, label, "")
		}
		if row.MachineID != "" && row.State != "stale" {
			row.State = "managed"
		}
		if i, exists := refIndexes[row.ID][ref.ID]; exists {
			row.References[i] = ref
		} else {
			refIndexes[row.ID][ref.ID] = len(row.References)
			row.References = append(row.References, ref)
		}
		return row
	}
	endpoints := map[string][]*MachineRow{}
	lanEndpoints := map[string][]*MachineRow{}
	for _, report := range reports {
		state := report.Status
		if report.Stale {
			state = "stale"
		}
		out.Sources[report.Source] = worseSourceState(out.Sources[report.Source], state)
		if !report.Complete || report.Stale {
			out.Complete = false
		}
		for _, c := range report.Candidates {
			ref := MachineReference{ID: ReferenceID(c.Source, c.Scope, c.NativeID), Provider: c.Source, Scope: c.Scope, NativeID: c.NativeID, State: "observed"}
			if report.Stale {
				ref.State = "stale_observation"
			}
			row := attach(ref, c.Name, nil)
			if c.Source == "tailscale" {
				row.Tailscale = append(row.Tailscale, c)
			} else {
				row.LAN = append(row.LAN, c)
			}
			addresses := append([]string{}, c.Addresses...)
			if c.Source == "tailscale" {
				addresses = append(addresses, c.DNSName)
			}
			for _, addr := range addresses {
				if key := literalEndpoint(addr); key != "" {
					if c.Source == "tailscale" {
						endpoints[key] = append(endpoints[key], row)
					} else {
						key += "\x00" + strconv.Itoa(c.Port)
						lanEndpoints[key] = append(lanEndpoints[key], row)
					}
				}
			}
		}
	}
	aliasRows := map[string]*MachineRow{}
	for _, hint := range hints {
		ref := MachineReference{ID: ReferenceID("ssh", local.Root, strings.ToLower(hint.Alias)), Provider: "ssh", Scope: local.Root, NativeID: strings.ToLower(hint.Alias), Fingerprint: hint.Fingerprint, State: hint.State}
		var suggested *MachineRow
		if hint.State == "known" {
			key := literalEndpoint(hint.HostName)
			port := hint.Port
			if port == 0 {
				port = 22
			}
			matches := append(append([]*MachineRow{}, endpoints[key]...), lanEndpoints[key+"\x00"+strconv.Itoa(port)]...)
			for _, match := range matches {
				if suggested != nil && suggested != match {
					suggested = nil
					break
				}
				suggested = match
			}
			if suggested != nil {
				ref.State = "declared"
			}
		}
		row := attach(ref, hint.Alias, suggested)
		row.Profiles = append(row.Profiles, ConnectionProfile{ID: ref.ID, Alias: hint.Alias, HostName: hint.HostName, User: hint.User, Port: hint.Port, Fingerprint: hint.Fingerprint, State: hint.State, Source: hint.Source})
		aliasRows[strings.ToLower(hint.Alias)] = row
	}
	for _, alias := range local.Aliases {
		if aliasRows[strings.ToLower(alias.Name)] != nil {
			continue
		}
		ref := MachineReference{ID: ReferenceID("ssh", local.Root, strings.ToLower(alias.Name)), Provider: "ssh", Scope: local.Root, NativeID: strings.ToLower(alias.Name), Fingerprint: sourceFingerprint(alias), State: "unknown"}
		row := attach(ref, alias.Name, nil)
		row.Profiles = append(row.Profiles, ConnectionProfile{ID: ref.ID, Alias: alias.Name, Fingerprint: ref.Fingerprint, State: "unknown"})
		aliasRows[strings.ToLower(alias.Name)] = row
	}
	for _, host := range local.Fleet {
		ref := MachineReference{ID: ReferenceID("fleet", fleetScope, host.Name), Provider: "fleet", Scope: fleetScope, NativeID: host.Name, Fingerprint: sourceFingerprint(host), State: "observed"}
		row := attach(ref, host.Name, aliasRows[strings.ToLower(host.Alias)])
		row.Fleet = append(row.Fleet, host)
	}
	for _, profile := range local.Herdr.Profiles {
		ref := MachineReference{ID: ReferenceID("herdr", herdrScope, profile.ID), Provider: "herdr", Scope: herdrScope, NativeID: profile.ID, Fingerprint: sourceFingerprint(struct{ Target, Session string }{profile.Target, profile.Session}), State: "observed"}
		match := aliasRows[strings.ToLower(profile.Target)]
		if match == nil {
			key := literalEndpoint(profile.Target)
			candidates := append(append([]*MachineRow{}, endpoints[key]...), lanEndpoints[key+"\x0022"]...)
			if len(candidates) == 1 {
				match = candidates[0]
			}
		}
		row := attach(ref, profile.Label, match)
		row.Herdr = append(row.Herdr, profile)
	}
	nameGroups := map[string][]string{}
	for _, row := range rows {
		if row.Label != "" {
			nameGroups[normalizedMachineName(row.Label)] = append(nameGroups[normalizedMachineName(row.Label)], row.ID)
		}
	}
	for _, group := range nameGroups {
		sort.Strings(group)
	}
	for _, row := range rows {
		for _, id := range nameGroups[normalizedMachineName(row.Label)] {
			if id != row.ID {
				row.Suggestions = append(row.Suggestions, id)
				if len(row.Suggestions) == 16 {
					break
				}
			}
		}
		sort.Slice(row.References, func(i, j int) bool { return row.References[i].ID < row.References[j].ID })
		sort.Slice(row.Profiles, func(i, j int) bool { return row.Profiles[i].Alias < row.Profiles[j].Alias })
		out.Machines = append(out.Machines, *row)
	}
	sort.Slice(out.Machines, func(i, j int) bool {
		a, b := out.Machines[i], out.Machines[j]
		if a.Label == b.Label {
			return a.ID < b.ID
		}
		return a.Label < b.Label
	})
	return out
}

func worseSourceState(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "":
			return 0
		case "ready":
			return 1
		case "stale":
			return 2
		case "partial":
			return 3
		default:
			return 4
		}
	}
	if rank(a) > rank(b) {
		return a
	}
	return b
}

func literalEndpoint(value string) string {
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap().String()
	}
	value = strings.TrimSuffix(strings.ToLower(value), ".")
	if !strings.Contains(value, ".") || strings.ContainsAny(value, " %$/@:*?[]\\\t\r\n") {
		return ""
	}
	return value
}

func normalizedMachineName(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.SplitN(value, ".", 2)[0]), "-local")
}
