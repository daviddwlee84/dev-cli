package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

func TestSSHFleetListKeepsLocalAndValidRowsWhenCacheIsIncomplete(t *testing.T) {
	f := newSSHCLIFixture(t)
	// macOS temporary homes may use /var, whose ancestor is a symlink. Cache
	// writes deliberately require a direct canonical path.
	canonicalHome, err := filepath.EvalSymlinks(f.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(canonicalHome, ".cache"))
	if err := os.MkdirAll(filepath.Join(f.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".ssh", "config"), []byte("Host local-alias\n HostName local.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.remotesPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.remotesPath, []byte("schema_version = 1\n[[hosts]]\nname = 'lab'\nssh_alias = 'gateway'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configured, err := fleet.LoadConfig(f.remotesPath)
	if err != nil {
		t.Fatal(err)
	}
	origin := sshremote.Origin{MachineID: "11111111-1111-4111-8111-111111111111", Platform: "linux", User: "remote", Root: "/home/remote/.ssh/config"}
	origin.ID = sshremote.OriginID(origin.MachineID, origin.User, origin.Root)
	profile := sshremote.Profile{ID: sshremote.ProfileID(origin.ID, "remote-api"), Alias: "remote-api", Fingerprint: "sha256:" + strings.Repeat("a", 64), State: "active", Source: sshhost.Location{Path: origin.Root, Line: 1}, HostName: "api.internal"}
	inventory := sshremote.Inventory{Header: sshremote.NewHeader(), Kind: "ssh_remote_inventory", Origin: origin, Complete: true, ObservedAt: time.Now().UTC(), Profiles: []sshremote.Profile{profile}}
	if err := sshremote.SaveCache(context.Background(), sshFleetCacheDir(), configured.Hosts[0], inventory); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(sshFleetCacheDir(), "inventory-"+strings.Repeat("b", 64)+".json")
	if err := os.WriteFile(corrupt, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostics, err := f.run("ssh", "list", "--fleet", "--json")
	if err != nil {
		t.Fatalf("list: %v %s", err, diagnostics)
	}
	var document struct {
		Complete bool `json:"complete"`
		Aliases  []struct {
			Name string `json:"name"`
		} `json:"aliases"`
		Sources     []sshFleetSource `json:"fleet_ssh_sources"`
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"fleet_ssh_diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	if document.Complete || len(document.Aliases) != 1 || document.Aliases[0].Name != "local-alias" || len(document.Sources) != 1 || document.Sources[0].Inventory.Profiles[0].Alias != "remote-api" || len(document.Diagnostics) != 1 || document.Diagnostics[0].Code != "fleet_ssh_cache_incomplete" {
		t.Fatalf("lost partial inventory: %s", out)
	}
	if !strings.Contains(diagnostics, "independently valid") {
		t.Fatalf("missing cache warning: %s", diagnostics)
	}
	out, _, err = f.run("ssh", "list", "--fleet")
	if err != nil || !strings.Contains(out, "local-alias") || !strings.Contains(out, "remote-api") {
		t.Fatalf("human partial listing: %v %s", err, out)
	}
	if f.runner.callCount() != 0 {
		t.Fatal("cached fleet listing invoked SSH or an agent")
	}
	if body, err := os.ReadFile(corrupt); err != nil || string(body) != "{broken" {
		t.Fatal("passive listing rewrote corrupt cache")
	}
}
