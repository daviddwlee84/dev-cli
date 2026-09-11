package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func tuiFleetFixture(t *testing.T, hosts string) (*App, *tuiFleetBackend, []fleet.Host) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "remotes.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"+hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Default(), In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, remotesPath: path, sshHostRunner: &tuiFleetHerdrRunner{}}
	backend := newTUIFleetBackend(func() *App { return app })
	cfg, err := loadFleetConfig(app)
	if err != nil {
		t.Fatal(err)
	}
	return app, backend, cfg.Hosts
}

func tuiFleetSnapshot(name string) fleet.Snapshot {
	return fleet.Snapshot{SchemaVersion: fleet.SnapshotSchemaVersion, Host: name, DevVersion: "test", GeneratedAt: time.Now().UTC(), Repositories: []fleet.RepoSnapshot{}}
}

func TestTUIFleetDescriptorsAreIndependentAndCacheIgnoresTTL(t *testing.T) {
	_, backend, hosts := tuiFleetFixture(t, "[defaults]\ncache_ttl = '1ns'\n[[hosts]]\nname = 'cached'\nssh_alias = 'one'\n[[hosts]]\nname = 'no-cache'\nssh_alias = 'two'\n")
	if err := fleet.SaveCache(hosts[0], tuiFleetSnapshot("cached")); err != nil {
		t.Fatal(err)
	}
	backend.run = func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result {
		t.Fatal("descriptor/cache loading contacted SSH")
		return fleet.Result{}
	}
	result, err := backend.LoadHosts(t.Context())
	if err != nil || len(result.Hosts) != 3 || !result.Hosts[0].Local || result.Hosts[2].Name != "no-cache" {
		t.Fatalf("hosts = %+v, %v", result, err)
	}
	for _, descriptor := range result.Hosts {
		if descriptor.Cached != nil {
			t.Fatal("descriptor loading decoded cache before headers could render")
		}
	}
	cached, fresh, err := backend.LoadHostCache(t.Context(), result.Hosts[1])
	if err != nil || cached == nil || fresh || !cached.FromCache || cached.Snapshot.Host != "cached" {
		t.Fatalf("expired but valid cache = %+v fresh=%v err=%v", cached, fresh, err)
	}
	missing, _, err := backend.LoadHostCache(t.Context(), result.Hosts[2])
	if err != nil || missing != nil {
		t.Fatalf("no-cache host = %+v %v", missing, err)
	}
}

func TestTUIFleetBackgroundReadNeverRetriesAndCapsDeadline(t *testing.T) {
	_, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\ncommand_timeout='5m'\n[hosts.ssh_login_password_source]\ntype='bitwarden'\nitem='never-execute'\n")
	calls := 0
	backend.run = func(ctx context.Context, _ fleet.Host, args []string, options fleet.RunOptions) fleet.Result {
		calls++
		if options.Retry != fleet.RetryNever || !reflect.DeepEqual(args, []string{"fleet", "_snapshot"}) {
			t.Fatalf("unexpected background request: %v %+v", args, options)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second || time.Until(deadline) < 29*time.Second {
			t.Fatalf("deadline=%v present=%v", deadline, ok)
		}
		return fleet.Result{ExitCode: 255, Stderr: []byte("Permission denied (publickey)")}
	}
	result, err := backend.LoadHost(t.Context(), fleetDescriptor(hosts[0]))
	if err != nil || result.State != fleet.HostUnreachable || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
}

func TestTUIFleetReadPreservesShortConfiguredDeadline(t *testing.T) {
	_, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\ncommand_timeout='3s'\n")
	backend.run = func(ctx context.Context, _ fleet.Host, _ []string, _ fleet.RunOptions) fleet.Result {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Fatalf("configured timeout lost: %v", deadline)
		}
		data, _ := json.Marshal(tuiFleetSnapshot("lab"))
		return fleet.Result{Stdout: data}
	}
	result, err := backend.LoadHost(t.Context(), fleetDescriptor(hosts[0]))
	if err != nil || result.State != fleet.HostOK {
		t.Fatal(result, err)
	}
}

func TestTUIFleetSupersededReadCannotPublishCache(t *testing.T) {
	_, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	backend.run = func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result {
		name := "new"
		if calls.Add(1) == 1 {
			close(started)
			<-release
			name = "old"
		}
		data, _ := json.Marshal(tuiFleetSnapshot(name))
		return fleet.Result{Stdout: data}
	}
	descriptor := fleetDescriptor(hosts[0])
	done := make(chan error, 1)
	go func() { _, err := backend.LoadHost(t.Context(), descriptor); done <- err }()
	<-started
	if _, err := backend.LoadHost(t.Context(), descriptor); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("superseded request = %v", err)
	}
	snapshot, _, ok := fleet.LoadCache(hosts[0])
	if !ok || snapshot.Host != "new" {
		t.Fatalf("old response replaced cache: %+v", snapshot)
	}
}

func TestTUIFleetCanceledOrChangedEndpointCannotPublish(t *testing.T) {
	for _, mode := range []string{"canceled", "retargeted", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backend.run = func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result {
				snapshot := tuiFleetSnapshot("lab")
				switch mode {
				case "canceled":
					cancel()
				case "retargeted":
					if err := os.WriteFile(app.remotesPath, []byte("schema_version=1\n[[hosts]]\nname='lab'\nssh_alias='another'\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "invalid":
					snapshot.GeneratedAt = time.Time{}
				}
				data, _ := json.Marshal(snapshot)
				return fleet.Result{Stdout: data}
			}
			result, err := backend.LoadHost(ctx, fleetDescriptor(hosts[0]))
			if mode == "invalid" && (err != nil || result.State != fleet.HostInvalid) {
				t.Fatalf("invalid response = %+v %v", result, err)
			}
			if mode != "invalid" && err == nil {
				t.Fatal("obsolete request accepted")
			}
			if _, _, ok := fleet.LoadCache(hosts[0]); ok {
				t.Fatal("obsolete/invalid response reached cache")
			}
		})
	}
}

type tuiFleetHerdrRunner struct {
	profiles            []herdrremote.Profile
	calls               [][]string
	missing             bool
	unsupportedMachines bool
}

func (f *tuiFleetHerdrRunner) Run(_ context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	f.calls = append(f.calls, request.Args)
	if f.missing {
		return sshhost.RunResult{}, errors.New("missing")
	}
	if reflect.DeepEqual(request.Args, []string{"--help"}) {
		return sshhost.RunResult{Stdout: []byte("--remote <target> --session <name>")}, nil
	}
	if reflect.DeepEqual(request.Args, []string{"machine", "list", "--json"}) {
		if f.unsupportedMachines {
			return sshhost.RunResult{ExitCode: 2}, nil
		}
		profiles := f.profiles
		if profiles == nil {
			profiles = []herdrremote.Profile{}
		}
		data, _ := json.Marshal(profiles)
		return sshhost.RunResult{Stdout: data}, nil
	}
	return sshhost.RunResult{}, errors.New("unexpected Herdr mutation")
}

func TestTUIFleetHerdrActionsRespectContextAndExactProfiles(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	fake := &tuiFleetHerdrRunner{}
	app.sshHostRunner = fake
	descriptor := fleetDescriptor(hosts[0])
	t.Setenv("HERDR_ENV", "1")
	actions, err := tuiFleetTestActions(t, backend, descriptor)
	if err != nil || !hasTUIFleetAction(actions, "herdr-add", false) {
		t.Fatalf("inside add = %+v %v", actions, err)
	}
	const enabled = "0123456789abcdef0123456789abcdef"
	const disabled = "1123456789abcdef0123456789abcdef"
	fake.profiles = []herdrremote.Profile{{ID: enabled, Target: "lab", Label: "One", Session: "agents", Enabled: true}, {ID: disabled, Target: "lab", Label: "Two", Session: "default", Enabled: false}}
	actions, err = tuiFleetTestActions(t, backend, descriptor)
	if err != nil || !hasTUIFleetAction(actions, tuiFleetProfileAction("herdr-disable", fake.profiles[0]), false) || !hasTUIFleetAction(actions, tuiFleetProfileAction("herdr-enable", fake.profiles[1]), false) {
		t.Fatalf("exact profiles = %+v %v", actions, err)
	}
	t.Setenv("HERDR_ENV", "")
	actions, err = tuiFleetTestActions(t, backend, descriptor)
	if err != nil || !hasTUIFleetAction(actions, tuiFleetProfileAction("herdr-connect", fake.profiles[0]), false) || !hasTUIFleetAction(actions, tuiFleetProfileAction("herdr-connect", fake.profiles[1]), false) {
		t.Fatalf("outside profiles = %+v %v", actions, err)
	}
	for _, args := range fake.calls {
		if len(args) > 1 && args[1] != "list" {
			t.Fatalf("menu performed a native mutation: %v", args)
		}
	}
}

func tuiFleetTestActions(t *testing.T, backend *tuiFleetBackend, descriptor tui.FleetHostDescriptor) ([]tui.FleetHostAction, error) {
	t.Helper()
	catalog, _ := backend.LoadHerdr(t.Context())
	return backend.ListActions(t.Context(), descriptor, catalog)
}

func hasTUIFleetAction(actions []tui.FleetHostAction, id string, disabled bool) bool {
	for _, action := range actions {
		if action.ID == id && action.Disabled == disabled {
			return true
		}
	}
	return false
}

func TestTUIFleetHostActionsDoNotRequireRemoteDev(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\nremote_os='windows'\n")
	descriptor := fleetDescriptor(hosts[0])
	actions, err := tuiFleetTestActions(t, backend, descriptor)
	if err != nil || !hasTUIFleetAction(actions, "ssh", false) || !hasTUIFleetAction(actions, "dotfile-status", false) || !hasTUIFleetAction(actions, "herdr-unavailable", true) {
		t.Fatalf("no-dev host actions = %+v %v", actions, err)
	}
	process, err := backend.RunAction(t.Context(), descriptor, "dotfile-status")
	if err != nil {
		t.Fatal(err)
	}
	var request tuiFleetHostRequest
	data, err := base64.RawURLEncoding.DecodeString(process.Args[len(process.Args)-1])
	if err != nil || json.Unmarshal(data, &request) != nil || request.Host != "lab" || request.EndpointID != descriptor.EndpointID || request.Action != "dotfile-status" {
		t.Fatalf("host handoff = %+v %v", request, err)
	}
	if !strings.Contains(strings.Join(process.Args, " "), app.remotesPath) {
		t.Fatal("host action lost selected remotes config")
	}
	if err := os.WriteFile(app.remotesPath, []byte("schema_version=1\n[[hosts]]\nname='lab'\nssh_alias='retargeted'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.RunAction(t.Context(), descriptor, "ssh"); err == nil {
		t.Fatal("stale host selection retargeted an action")
	}
}

func TestTUIFleetNativeConnectPinsSessionAndNeverFallsBack(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(bin, "unexpected-ssh")
	t.Setenv("TUI_FLEET_FALLBACK_MARKER", marker)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n: > \"$TUI_FLEET_FALLBACK_MARKER\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_SESSION", "private-local-session")
	err := runTUIFleetHostAction(t.Context(), app, backend, fleetDescriptor(hosts[0]), hosts[0], "herdr-connect")
	if err == nil || app.Out.(*bytes.Buffer).String() != "--remote\nlab\n--session\ndefault\n" {
		t.Fatalf("native argv/output=%q err=%v", app.Out.(*bytes.Buffer).String(), err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit Herdr failure silently fell back to SSH")
	}
}

func TestTUIFleetSSHRetainsConfiguredEndpointAndStreams(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	app, _, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\nuser='deploy'\nport=2222\nidentity_file='/fixture/key'\n")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n/bin/cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	app.In = strings.NewReader("native input\n")
	if err := runTUIFleetSSH(t.Context(), app, hosts[0]); err != nil {
		t.Fatal(err)
	}
	out := app.Out.(*bytes.Buffer).String()
	for _, want := range []string{"-l\ndeploy\n", "-p\n2222\n", "-i\n/fixture/key\n", "--\nlab\n", "native input\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("SSH lost exact endpoint or streams, want %q in %q", want, out)
		}
	}
	if reason := fleetHerdrTargetReason(hosts[0]); reason == "" {
		t.Fatal("Herdr would ignore the fleet connection overrides")
	}
}

func TestTUIFleetNoRuntimeDoesNotProbeHerdr(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	app.noRuntime = true
	fake := &tuiFleetHerdrRunner{}
	app.sshHostRunner = fake
	actions, err := tuiFleetTestActions(t, backend, fleetDescriptor(hosts[0]))
	if err != nil || len(fake.calls) != 0 || !hasTUIFleetAction(actions, "herdr-unavailable", true) {
		t.Fatalf("--no-runtime probed Herdr: calls=%v actions=%+v err=%v", fake.calls, actions, err)
	}
}

func TestTUIFleetHerdrCapabilityDetectionDoesNotRequireMachineCLIForAttach(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	fake := &tuiFleetHerdrRunner{unsupportedMachines: true}
	app.sshHostRunner = fake
	t.Setenv("HERDR_ENV", "")
	actions, err := tuiFleetTestActions(t, backend, fleetDescriptor(hosts[0]))
	if err != nil || !hasTUIFleetAction(actions, "herdr-connect", false) {
		t.Fatalf("native attach incorrectly required saved machines: %+v %v", actions, err)
	}
	t.Setenv("HERDR_ENV", "1")
	actions, err = tuiFleetTestActions(t, backend, fleetDescriptor(hosts[0]))
	if err != nil || !hasTUIFleetAction(actions, "herdr-unavailable", true) {
		t.Fatalf("unsupported machine CLI was offered: %+v %v", actions, err)
	}
	fake.missing = true
	actions, err = tuiFleetTestActions(t, backend, fleetDescriptor(hosts[0]))
	if err != nil || !hasTUIFleetAction(actions, "herdr-unavailable", true) {
		t.Fatalf("missing binary was offered: %+v %v", actions, err)
	}
}

func TestTUIFleetHerdrSessionChangesInvalidateAction(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	profile := herdrremote.Profile{ID: "0123456789abcdef0123456789abcdef", Label: "Lab", Target: "lab", Session: "agents", Enabled: true}
	fake := &tuiFleetHerdrRunner{profiles: []herdrremote.Profile{profile}}
	app.sshHostRunner = fake
	t.Setenv("HERDR_ENV", "")
	action := tuiFleetProfileAction("herdr-connect", profile)
	fake.profiles[0].Session = "unreviewed-session"
	if _, err := backend.RunAction(t.Context(), fleetDescriptor(hosts[0]), action); err != nil {
		t.Fatal("preparing the fingerprint-bound handoff should not reread the native catalog", err)
	}
	if err := runTUIFleetHostAction(t.Context(), app, backend, fleetDescriptor(hosts[0]), hosts[0], action); err == nil {
		t.Fatal("stale process handoff silently switched to a different remote session")
	}
}
