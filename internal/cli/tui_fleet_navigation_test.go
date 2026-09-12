package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/fleetnav"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func navigationAdapterProfile(enabled bool) herdrremote.Profile {
	return herdrremote.Profile{ID: "0123456789abcdef0123456789abcdef", Label: "Exact profile", Target: "lab", Session: "agents", Enabled: enabled}
}

func navigationAdapterReply(request fleet.HerdrRepoRequest) fleet.HerdrRepoResult {
	result := fleet.HerdrRepoResult{SchemaVersion: fleet.HerdrRepoSchemaVersion, Phase: request.Phase, Session: request.Session, Path: request.Repository.Path, RemoteIdentity: request.Repository.RemoteIdentity, RepositoryIdentity: strings.Repeat("a", 64), RuntimeState: "ready"}
	if request.Phase == "prepare" {
		result.Workspace, result.Surface = "w19", "workspace"
	}
	return result
}

func TestTUIFleetNavigationUsesExactRepoProtocolAndNoAcknowledgement(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	profile := navigationAdapterProfile(true)
	fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{profile}}
	app.sshHostRunner = fake
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "wrong-local-session")
	backend.run = func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result {
		t.Fatal("selected repository navigation loaded the broad repository snapshot")
		return fleet.Result{}
	}
	var requests []fleet.HerdrRepoRequest
	backend.protocolRun = func(_ context.Context, host fleet.Host, args []string, stdin []byte, options fleet.RunOptions) fleet.Result {
		if host.SSHAlias != "lab" || !reflect.DeepEqual(args, []string{"fleet", fleet.HerdrRepoHelper}) || options.Retry != fleet.RetryAuthentication {
			t.Fatalf("unsafe navigation protocol: %+v %v", host, args)
		}
		var request fleet.HerdrRepoRequest
		if err := json.Unmarshal(stdin, &request); err != nil {
			t.Fatal(err)
		}
		if request.Session != "agents" || request.Repository.Path != "/srv/exact repo" || request.Repository.RemoteIdentity != "github.com/example/exact" {
			t.Fatalf("selected identity/session lost: %+v", request)
		}
		if len(requests) > 0 && request.ExpectedIdentity != strings.Repeat("a", 64) {
			t.Fatalf("preparation/recheck lost reviewed repository identity: %+v", request)
		}
		requests = append(requests, request)
		data, _ := json.Marshal(navigationAdapterReply(request))
		return fleet.Result{Stdout: data}
	}
	d := fleetDescriptor(hosts[0])
	row := tui.FleetRow{HostKey: d.Key, EndpointID: d.EndpointID, Host: d.Name, Repository: &fleet.RepoSnapshot{Path: "/srv/exact repo", Display: "exact", RemoteIdentities: []string{"github.com/example/exact"}}}
	execution, err := backend.Navigate(t.Context(), row)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	result, err := execution.Run(t.Context(), strings.NewReader(""), &out, &errOut)
	if err != nil || !result.RefreshHerdr || result.RefreshHost || !strings.Contains(result.Summary, "native sidebar") || !strings.Contains(result.Summary, "w19") {
		t.Fatalf("typed navigation result=%+v err=%v", result, err)
	}
	if len(requests) != 3 || requests[0].Phase != "check" || requests[1].Phase != "check" || requests[2].Phase != "prepare" {
		t.Fatalf("navigation order=%+v", requests)
	}
	if strings.Contains(out.String(), "Press Enter") || len(fake.mutations) != 0 {
		t.Fatalf("navigation acknowledged/mutated an enabled profile: %q %v", out.String(), fake.mutations)
	}
}

func TestTUIFleetHostNavigationDoesNotRequireRemoteDev(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	app.sshHostRunner = &tuiFleetMutationRunner{profiles: []herdrremote.Profile{navigationAdapterProfile(true)}}
	t.Setenv("HERDR_ENV", "1")
	backend.run = func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result {
		t.Fatal("host navigation requires remote dev")
		return fleet.Result{}
	}
	backend.protocolRun = func(context.Context, fleet.Host, []string, []byte, fleet.RunOptions) fleet.Result {
		t.Fatal("host navigation sent a repository protocol")
		return fleet.Result{}
	}
	d := fleetDescriptor(hosts[0])
	execution, err := backend.Navigate(t.Context(), tui.FleetRow{HostKey: d.Key, EndpointID: d.EndpointID, Host: d.Name})
	if err != nil {
		t.Fatal(err)
	}
	result, err := execution.Run(t.Context(), strings.NewReader(""), io.Discard, io.Discard)
	if err != nil || !strings.Contains(result.Summary, "enabled") || !strings.Contains(result.Summary, "native sidebar") {
		t.Fatalf("host result=%+v %v", result, err)
	}
}

func TestTUIFleetNavigationMalformedCheckStopsBeforeEnable(t *testing.T) {
	for _, failure := range []string{"old-dev", "session", "path", "identity"} {
		t.Run(failure, func(t *testing.T) {
			app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
			fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{navigationAdapterProfile(false)}}
			app.sshHostRunner = fake
			t.Setenv("HERDR_ENV", "1")
			backend.protocolRun = func(_ context.Context, _ fleet.Host, _ []string, stdin []byte, _ fleet.RunOptions) fleet.Result {
				if failure == "old-dev" {
					return fleet.Result{ExitCode: 1, Stderr: []byte("unknown command")}
				}
				var request fleet.HerdrRepoRequest
				_ = json.Unmarshal(stdin, &request)
				result := navigationAdapterReply(request)
				switch failure {
				case "session":
					result.Session = "other"
				case "path":
					result.Path = "/other"
				case "identity":
					result.RepositoryIdentity = "invalid"
				}
				data, _ := json.Marshal(result)
				return fleet.Result{Stdout: data}
			}
			d := fleetDescriptor(hosts[0])
			execution, err := backend.Navigate(t.Context(), tui.FleetRow{HostKey: d.Key, EndpointID: d.EndpointID, Host: d.Name, Repository: &fleet.RepoSnapshot{Path: "/repo", Display: "repo"}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := execution.Run(t.Context(), strings.NewReader("yes\n"), io.Discard, io.Discard)
			if err == nil || len(fake.mutations) != 0 || !strings.Contains(result.Summary, "check-repository") {
				t.Fatalf("unsafe check reached enable: %+v %v %v", result, err, fake.mutations)
			}
		})
	}
}

type navigationCatalogSequence struct {
	base     *tuiFleetMutationRunner
	lists    int
	changeAt int
	change   func()
}

func (f *navigationCatalogSequence) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	if reflect.DeepEqual(request.Args, []string{"machine", "list", "--json"}) {
		f.lists++
		if f.lists == f.changeAt {
			f.change()
		}
	}
	return f.base.Run(ctx, request)
}

func TestFleetNavigationEnsureDoesNotBlessPostApplyProfileChanges(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	profile := navigationAdapterProfile(false)
	fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{profile}}
	sequence := &navigationCatalogSequence{base: fake, changeAt: 4, change: func() { fake.profiles[0].Label = "concurrent label" }}
	app.sshHostRunner, app.interactiveCheck, app.In = sequence, func() bool { return true }, strings.NewReader("yes\n")
	result, err := ensureFleetNavigationProfile(t.Context(), app, backend, fleetnav.Target{Host: hosts[0], EndpointID: fleet.EndpointID(hosts[0])}, fleetnav.Selection{Exists: true, Profile: profile, Session: profile.Session})
	if err == nil || result.Status != "applied" || len(fake.mutations) != 1 {
		t.Fatalf("post-apply authority/ledger lost: %+v %v %v", result, err, fake.mutations)
	}
}

func TestFleetNavigationConcurrentNondefaultProfileRequiresSelection(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{navigationAdapterProfile(true)}}
	app.sshHostRunner, app.interactiveCheck = fake, func() bool { return true }
	result, err := ensureFleetNavigationProfile(t.Context(), app, backend, fleetnav.Target{Host: hosts[0], EndpointID: fleet.EndpointID(hosts[0])}, fleetnav.Selection{Session: "default"})
	if err == nil || result.Status != "unchanged" || len(fake.mutations) != 0 {
		t.Fatalf("concurrent nondefault profile was ignored: %+v %v", result, err)
	}
}

func TestFleetNavigationTypedActionDoesNotRequireChildAcknowledgement(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	profile := navigationAdapterProfile(true)
	fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{profile}}
	app.sshHostRunner, app.interactiveCheck = fake, func() bool { return true }
	execution, err := backend.RunAction(t.Context(), fleetDescriptor(hosts[0]), tuiFleetProfileAction("herdr-disable", profile))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result, err := execution.Run(t.Context(), strings.NewReader("yes\n"), &out, io.Discard)
	if err != nil || !result.RefreshHerdr || !strings.Contains(result.Summary, "disabled") || strings.Contains(out.String(), "Press Enter") {
		t.Fatalf("typed action retained child acknowledgement: %+v %v %q", result, err, out.String())
	}
}

func TestFleetNavigationSSHDoesNotForceConfiguredPasswordAfterKeySuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n[hosts.ssh_login_password_source]\ntype='plain'\nvalue='fixture-password'\n")
	bin := t.TempDir()
	log := filepath.Join(bin, "argv")
	t.Setenv("NAVIGATION_SSH_LOG", log)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$NAVIGATION_SSH_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	backend.run = func(_ context.Context, _ fleet.Host, _ []string, options fleet.RunOptions) fleet.Result {
		if options.Retry != fleet.RetryAuthentication {
			t.Fatal("explicit SSH validation lost key-first auth retry policy")
		}
		snapshot := tuiFleetSnapshot("lab")
		snapshot.Repositories = []fleet.RepoSnapshot{{Name: "repo", Display: "repo", Path: "/repo", RemoteIdentities: []string{"github.com/example/repo"}}}
		data, _ := json.Marshal(snapshot)
		return fleet.Result{Stdout: data, UsedPassword: false}
	}
	target := fleetnav.Target{Host: hosts[0], EndpointID: fleet.EndpointID(hosts[0]), Repository: &fleet.OpenRequest{Path: "/repo", RemoteIdentity: "github.com/example/repo"}}
	if err := newFleetNavigation(app, backend).SSH(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "BatchMode=yes") || strings.Contains(string(args), "PubkeyAuthentication=no") || strings.Contains(string(args), "fixture-password") {
		t.Fatalf("configured password forced despite key auth success: %s", args)
	}
}

func TestFleetNavigationExecutionRechecksEndpointAtTerminalBoundary(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	d := fleetDescriptor(hosts[0])
	execution, err := backend.Navigate(t.Context(), tui.FleetRow{HostKey: d.Key, EndpointID: d.EndpointID, Host: d.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.remotesPath, []byte("schema_version=1\n[[hosts]]\nname='lab'\nssh_alias='different'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = execution.Run(t.Context(), strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || errors.Is(err, fleetnav.ErrCanceled) {
		t.Fatalf("retargeted execution accepted: %v", err)
	}
}
