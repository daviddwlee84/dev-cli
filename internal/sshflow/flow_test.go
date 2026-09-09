//go:build linux || darwin

package sshflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type flowRunner struct {
	profiles   []herdrremote.Profile
	sshCalls   int
	failAlias  string
	nativeAdds int
}

func (f *flowRunner) Run(_ context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
	if r.Name == "ssh" {
		f.sshCalls++
		if len(r.Args) > 1 && r.Args[len(r.Args)-2] == f.failAlias {
			return sshhost.RunResult{ExitCode: 255}, nil
		}
		return sshhost.RunResult{}, nil
	}
	if r.Name != "herdr" {
		return sshhost.RunResult{}, fmt.Errorf("unexpected program")
	}
	if r.Args[1] == "list" {
		rows := f.profiles
		if rows == nil {
			rows = []herdrremote.Profile{}
		}
		b, _ := json.Marshal(rows)
		return sshhost.RunResult{Stdout: b}, nil
	}
	if r.Args[1] == "add" {
		f.nativeAdds++
		f.profiles = append(f.profiles, herdrremote.Profile{ID: fmt.Sprintf("%032x", f.nativeAdds), Target: r.Args[2], Label: r.Args[4], Session: r.Args[6], Enabled: true})
		return sshhost.RunResult{}, nil
	}
	return sshhost.RunResult{ExitCode: 2}, nil
}
func testService(t *testing.T) (Service, *flowRunner) {
	t.Helper()
	home := t.TempDir()
	paths, err := sshhost.NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(paths.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(paths.RootConfig, []byte("Host one alternate\n  HostName 192.0.2.1\nHost two\n  HostName 192.0.2.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &flowRunner{}
	ssh, err := sshhost.NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	fleetDir := filepath.Join(home, "fleet")
	if err = os.Mkdir(fleetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return Service{SSH: ssh, Herdr: herdrremote.Service{Runner: runner}, FleetPath: filepath.Join(fleetDir, "remotes.toml"), RecoveryPath: filepath.Join(home, "recovery")}, runner
}
func TestPlanHasNoNetworkAndExistingNamesArePreserved(t *testing.T) {
	s, f := testService(t)
	ctx := context.Background()
	if err := os.WriteFile(s.FleetPath, []byte("schema_version=1\n[[hosts]]\nname='friendly'\nssh_alias='one'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.profiles = []herdrremote.Profile{{ID: fmt.Sprintf("%032x", 8), Target: "one", Label: "Custom label", Session: "agents", Enabled: false}}
	p, err := s.Plan(ctx, Request{Action: "register", To: "both", Aliases: []string{"one"}, Session: "agents", RemoteOS: "posix"})
	if err != nil {
		t.Fatal(err)
	}
	if f.sshCalls != 0 || f.nativeAdds != 0 {
		t.Fatal("planning contacted remote")
	}
	result, err := s.Apply(ctx, p, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range result.Outcomes {
		if o.Status != "noop" {
			t.Fatalf("existing profile changed: %+v", o)
		}
	}
	if f.sshCalls != 0 || f.profiles[0].Enabled {
		t.Fatal("noop performed authentication or enabled Herdr")
	}
}
func TestBulkRegistrationIsIdempotentAndRetainsPartialResults(t *testing.T) {
	s, f := testService(t)
	f.failAlias = "two"
	ctx := context.Background()
	p, err := s.Plan(ctx, Request{Action: "register", To: "fleet", Aliases: []string{"one", "two"}, RemoteOS: "posix"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Apply(ctx, p, false)
	if err == nil || result.Status != "partial" {
		t.Fatal(result, err)
	}
	cfg, err := fleet.LoadConfig(s.FleetPath)
	if err != nil || len(cfg.Hosts) != 1 || cfg.Hosts[0].SSHAlias != "one" {
		t.Fatal("successful host was lost", cfg, err)
	}
	f.failAlias = ""
	p, err = s.Plan(ctx, Request{Action: "register", To: "fleet", Aliases: []string{"one", "two"}, RemoteOS: "posix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	cfg, err = fleet.LoadConfig(s.FleetPath)
	if err != nil || len(cfg.Hosts) != 2 {
		t.Fatal("retry duplicated registration", cfg, err)
	}
}
func TestStaleSSHSourceAndIncludeMembershipHaveNoEffect(t *testing.T) {
	for _, kind := range []string{"content", "new_include"} {
		t.Run(kind, func(t *testing.T) {
			s, f := testService(t)
			ctx := context.Background()
			root := s.SSH.Paths().RootConfig
			if kind == "new_include" {
				b, _ := os.ReadFile(root)
				b = append([]byte("Include conf.d/*.conf\n"), b...)
				if err := os.WriteFile(root, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := s.Plan(ctx, Request{Action: "register", To: "both", Aliases: []string{"one"}, RemoteOS: "posix"})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "content" {
				if err = os.WriteFile(root, []byte("Host one\n  HostName 192.0.2.50\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				dir := filepath.Join(s.SSH.Paths().SSHDir, "conf.d")
				if err = os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "new.conf"), []byte("Host another\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.Apply(ctx, p, false); err == nil {
				t.Fatal("stale source accepted")
			}
			if f.sshCalls != 0 || f.nativeAdds != 0 {
				t.Fatal("stale plan had an effect")
			}
		})
	}
}
func TestInventoryDoesNotSerializeMatchCommands(t *testing.T) {
	s, _ := testService(t)
	if err := os.WriteFile(s.SSH.Paths().RootConfig, []byte("Host one\nMatch exec \"private-command-marker\"\n  User someone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(inv)
	if strings.Contains(string(b), "private-command-marker") {
		t.Fatal("Match command serialized")
	}
}
func TestBulkPrimaryRemovalUsesOneSourceTransaction(t *testing.T) {
	s, _ := testService(t)
	raw := "schema_version=1\n[[hosts]]\nname='first'\nssh_alias='one'\n[[hosts]]\nname='second'\nssh_alias='two'\n[[hosts]]\nname='keep'\nssh_alias='another'\n"
	if err := os.WriteFile(s.FleetPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := s.Plan(context.Background(), Request{Action: "remove", FleetHosts: []string{"first", "second"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Apply(context.Background(), p, false)
	if err != nil {
		t.Fatal(result, err)
	}
	cfg, err := fleet.LoadConfig(s.FleetPath)
	if err != nil || len(cfg.Hosts) != 1 || cfg.Hosts[0].Name != "keep" {
		t.Fatal(cfg, err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Receipt == "" {
		t.Fatal("missing coalesced receipt")
	}
}

func TestRegistrationPreviewShowsCustomFleetNameAndOS(t *testing.T) {
	s, _ := testService(t)
	p, err := s.Plan(context.Background(), Request{Action: "register", To: "fleet", Aliases: []string{"one"}, FleetName: "workstation", RemoteOS: "windows"})
	if err != nil {
		t.Fatal(err)
	}
	desired := p.Operations[0].FleetRegistration
	if desired == nil || desired.Name != "workstation" || desired.SSHAlias != "one" || desired.RemoteOS != "windows" {
		t.Fatal("registration preview omits desired identity", desired)
	}
	desired.Name = "tampered preview"
	result, err := s.Apply(context.Background(), p, false)
	if err != nil {
		t.Fatal(result, err)
	}
	cfg, err := fleet.LoadConfig(s.FleetPath)
	if err != nil || cfg.Hosts[0].Name != "workstation" {
		t.Fatal("public preview changed apply authority", cfg, err)
	}
}
