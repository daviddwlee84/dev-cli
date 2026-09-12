package sshflow

import (
	"reflect"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func machineTestLocal() Inventory {
	return Inventory{Root: "/home/test/.ssh/config", Complete: true, FleetStatus: "ready", Herdr: herdrremote.Inventory{Status: "ready"}}
}

func machineTestCandidate(source, id, name, address string, port int) sshdiscovery.Candidate {
	return sshdiscovery.Candidate{ID: source + "-" + id, Source: source, Scope: source + "-scope", NativeID: id, Name: name, Addresses: []string{address}, Port: port, State: sshdiscovery.StateDiscovered}
}

func machineTestReport(source string, candidates ...sshdiscovery.Candidate) sshdiscovery.Report {
	return sshdiscovery.Report{Source: source, Scope: source + "-scope", Status: sshdiscovery.StatusReady, Complete: true, Candidates: candidates}
}

func machineRowForProfile(t *testing.T, inventory MachineInventory, alias string) MachineRow {
	t.Helper()
	for _, row := range inventory.Machines {
		for _, profile := range row.Profiles {
			if profile.Alias == alias {
				return row
			}
		}
	}
	t.Fatalf("missing profile %q: %#v", alias, inventory)
	return MachineRow{}
}

func machineRowByID(t *testing.T, inventory MachineInventory, id string) MachineRow {
	t.Helper()
	for _, row := range inventory.Machines {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing row %q: %#v", id, inventory)
	return MachineRow{}
}

func TestJoinMachinesGroupsDeclaredTailscaleProfilesWithoutCreatingIdentity(t *testing.T) {
	local := machineTestLocal()
	local.Fleet = []FleetHost{{Name: "remote-build", Alias: "build", OS: "posix"}}
	local.Herdr.Profiles = []herdrremote.Profile{{ID: "herdr1", Label: "workspace", Target: "BUILD", Session: "dev"}}
	peer := machineTestCandidate("tailscale", "node1", "node", "100.64.0.1", 22)
	peer.DNSName = "node.tail123.ts.net"
	hints := []sshhost.ConnectionHint{
		{Alias: "build", HostName: "100.64.0.1", Port: 22, User: "developer", Fingerprint: "first", State: "known"},
		{Alias: "admin", HostName: "NODE.TAIL123.TS.NET.", Port: 2222, User: "admin", Fingerprint: "second", State: "known"},
	}
	out := JoinMachines(local, hints, machineregistry.Snapshot{}, []sshdiscovery.Report{machineTestReport("tailscale", peer)}, "fleet.toml", "herdr-home", time.Now())
	if !out.Complete || len(out.Machines) != 1 {
		t.Fatalf("inventory=%#v", out)
	}
	row := out.Machines[0]
	if row.MachineID != "" || len(row.Profiles) != 2 || len(row.Tailscale) != 1 || len(row.Fleet) != 1 || len(row.Herdr) != 1 {
		t.Fatalf("row=%#v", row)
	}
	for _, ref := range row.References {
		if ref.Provider == "ssh" && ref.State != "declared" {
			t.Fatalf("association was not explicitly labelled declared: %#v", ref)
		}
	}
}

func TestJoinMachinesLANRequiresIPAndPortAndNeverPTRAuthority(t *testing.T) {
	local := machineTestLocal()
	ssh := machineTestCandidate("lan", "192.168.1.20:22", "server", "192.168.1.20", 22)
	ssh.DNSName = "server.local"
	alt := machineTestCandidate("lan", "192.168.1.20:2222", "server", "192.168.1.20", 2222)
	alt.DNSName = "server.local"
	hints := []sshhost.ConnectionHint{
		{Alias: "normal", HostName: "192.168.1.20", State: "known", Fingerprint: "a"},
		{Alias: "alternate", HostName: "192.168.1.20", Port: 2222, State: "known", Fingerprint: "b"},
		{Alias: "ptr-only", HostName: "server.local", Port: 22, State: "known", Fingerprint: "c"},
		{Alias: "wrong-port", HostName: "192.168.1.20", Port: 2200, State: "known", Fingerprint: "d"},
	}
	local.Herdr.Profiles = []herdrremote.Profile{{ID: "default-port", Target: "192.168.1.20", Label: "server"}, {ID: "ptr", Target: "server.local", Label: "server"}}
	out := JoinMachines(local, hints, machineregistry.Snapshot{}, []sshdiscovery.Report{machineTestReport("lan", ssh, alt)}, "fleet", "herdr", time.Now())
	normal := machineRowForProfile(t, out, "normal")
	alternate := machineRowForProfile(t, out, "alternate")
	if normal.ID == alternate.ID || len(normal.LAN) != 1 || normal.LAN[0].Port != 22 || len(alternate.LAN) != 1 || alternate.LAN[0].Port != 2222 {
		t.Fatalf("normal=%#v alternate=%#v", normal, alternate)
	}
	if len(normal.Herdr) != 1 || normal.Herdr[0].ID != "default-port" {
		t.Fatalf("Herdr did not use default endpoint port: %#v", normal)
	}
	for _, alias := range []string{"ptr-only", "wrong-port"} {
		row := machineRowForProfile(t, out, alias)
		if len(row.LAN) != 0 {
			t.Fatalf("%s claimed LAN evidence: %#v", alias, row)
		}
	}
}

func TestJoinMachinesNamesAndCompetingIPsRemainSeparate(t *testing.T) {
	a := machineTestCandidate("tailscale", "one", "same", "100.64.0.1", 22)
	b := machineTestCandidate("tailscale", "two", "same", "100.64.0.1", 22)
	hints := []sshhost.ConnectionHint{{Alias: "same", HostName: "100.64.0.1", Port: 22, State: "known", Fingerprint: "ssh"}}
	out := JoinMachines(machineTestLocal(), hints, machineregistry.Snapshot{}, []sshdiscovery.Report{machineTestReport("tailscale", a, b)}, "fleet", "herdr", time.Now())
	if len(out.Machines) != 3 {
		t.Fatalf("competing identities merged: %#v", out)
	}
	row := machineRowForProfile(t, out, "same")
	if len(row.Tailscale) != 0 || row.MachineID != "" || len(row.Suggestions) != 2 {
		t.Fatalf("row=%#v", row)
	}
}

func TestJoinMachinesChangedBoundSSHProfileRemainsStaleAndSeparate(t *testing.T) {
	local := machineTestLocal()
	peer := machineTestCandidate("tailscale", "new-node", "new-machine", "100.64.0.2", 22)
	registry := machineregistry.Snapshot{Machines: []machineregistry.Machine{{ID: "old-machine", Label: "original"}, {ID: "new-machine", Label: "replacement"}}, Bindings: []machineregistry.Binding{
		{Provider: "ssh", Scope: local.Root, NativeID: "build", Fingerprint: "old-source", MachineID: "old-machine"},
		{Provider: "tailscale", Scope: peer.Scope, NativeID: peer.NativeID, MachineID: "new-machine"},
	}}
	hints := []sshhost.ConnectionHint{{Alias: "build", HostName: "100.64.0.2", Port: 22, State: "known", Fingerprint: "retargeted-source"}}
	out := JoinMachines(local, hints, registry, []sshdiscovery.Report{machineTestReport("tailscale", peer)}, "fleet", "herdr", time.Now())
	old := machineRowByID(t, out, "old-machine")
	replacement := machineRowByID(t, out, "new-machine")
	current := machineRowForProfile(t, out, "build")
	if old.State != "stale" || len(old.Profiles) != 0 || len(old.References) != 1 || old.References[0].Fingerprint != "old-source" || old.References[0].State != "stale" {
		t.Fatalf("old association was silently rewritten: %#v", old)
	}
	if len(replacement.Profiles) != 0 || current.MachineID != "" || len(current.Tailscale) != 0 || current.ID == old.ID || current.ID == replacement.ID {
		t.Fatalf("retargeted profile silently moved: current=%#v replacement=%#v", current, replacement)
	}
	if current.Profiles[0].Fingerprint != "retargeted-source" {
		t.Fatal("current evidence was lost")
	}
}

func TestJoinMachinesSuppressedBindingPreventsRediscoveryRelink(t *testing.T) {
	local := machineTestLocal()
	peer := machineTestCandidate("tailscale", "node", "node", "100.64.0.1", 22)
	registry := machineregistry.Snapshot{Machines: []machineregistry.Machine{{ID: "machine", Label: "node"}}, Bindings: []machineregistry.Binding{
		{Provider: "tailscale", Scope: peer.Scope, NativeID: peer.NativeID, MachineID: "machine"},
		{Provider: "ssh", Scope: local.Root, NativeID: "build", Fingerprint: "fingerprint", Suppressed: true},
	}}
	out := JoinMachines(local, []sshhost.ConnectionHint{{Alias: "build", HostName: "100.64.0.1", Port: 22, State: "known", Fingerprint: "fingerprint"}}, registry, []sshdiscovery.Report{machineTestReport("tailscale", peer)}, "fleet", "herdr", time.Now())
	row := machineRowForProfile(t, out, "build")
	machine := machineRowByID(t, out, "machine")
	if row.MachineID != "" || row.ID == machine.ID || len(machine.Profiles) != 0 {
		t.Fatalf("suppressed association restored: %#v", out)
	}
}

func TestJoinMachinesPreservesUnresolvedIntentAndSourceNamespaces(t *testing.T) {
	local := machineTestLocal()
	registry := machineregistry.Snapshot{Machines: []machineregistry.Machine{{ID: "machine", Label: "saved"}}, Bindings: []machineregistry.Binding{{Provider: "ssh", Scope: "/another/home/.ssh/config", NativeID: "build", Fingerprint: "same", MachineID: "machine"}}}
	out := JoinMachines(local, []sshhost.ConnectionHint{{Alias: "build", State: "unknown", Fingerprint: "same"}}, registry, nil, "fleet", "herdr", time.Now())
	saved := machineRowByID(t, out, "machine")
	current := machineRowForProfile(t, out, "build")
	if saved.State != "unresolved" || len(saved.References) != 1 || saved.References[0].State != "unresolved" || current.ID == saved.ID || current.MachineID != "" {
		t.Fatalf("scope-free match or unresolved intent loss: %#v", out)
	}
}

func TestJoinMachinesAggregatesWorstSourceStateIndependentOfOrder(t *testing.T) {
	fresh := machineTestReport("tailscale")
	stale := fresh
	stale.Scope = "old-scope"
	stale.Stale = true
	failed := fresh
	failed.Scope = "failed-scope"
	failed.Complete = false
	failed.Status = sshdiscovery.StatusFailed
	for _, reports := range [][]sshdiscovery.Report{{stale, fresh}, {fresh, stale}, {fresh, stale, failed}, {failed, stale, fresh}} {
		out := JoinMachines(machineTestLocal(), nil, machineregistry.Snapshot{}, reports, "fleet", "herdr", time.Now())
		want := "stale"
		if len(reports) == 3 {
			want = sshdiscovery.StatusFailed
		}
		if out.Complete || out.Sources["tailscale"] != want {
			t.Fatalf("incomplete source was masked: %#v", out)
		}
	}
}

func TestJoinMachinesKeepsOfflineAndStaleDiscoveryObservations(t *testing.T) {
	offline := false
	peer := machineTestCandidate("tailscale", "node", "node", "100.64.0.1", 22)
	peer.Online = &offline
	report := machineTestReport("tailscale", peer)
	report.Stale = true
	out := JoinMachines(machineTestLocal(), nil, machineregistry.Snapshot{}, []sshdiscovery.Report{report}, "fleet", "herdr", time.Now())
	if out.Complete || len(out.Machines) != 1 || out.Machines[0].References[0].State != "stale_observation" || !reflect.DeepEqual(out.Machines[0].Tailscale[0].Online, &offline) {
		t.Fatalf("observations discarded or promoted: %#v", out)
	}
}
