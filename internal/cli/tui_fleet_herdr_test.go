package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func TestTUIFleetSharedHerdrCatalogIsPassiveAndMenusReuseIt(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='one'\nssh_alias='one'\n[[hosts]]\nname='two'\nssh_alias='two'\n")
	profile := herdrremote.Profile{ID: "0123456789abcdef0123456789abcdef", Label: "Custom", Target: "one", Session: "agents", Enabled: false}
	fake := &tuiFleetHerdrRunner{profiles: []herdrremote.Profile{profile}}
	app.sshHostRunner = fake
	catalog, err := backend.LoadHerdr(t.Context())
	if err != nil || catalog.Status != "ready" || catalog.ObservedAt.IsZero() || len(catalog.Profiles) != 1 || catalog.Profiles[0].ID != profile.ID || catalog.Profiles[0].Enabled {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
	before := len(fake.calls)
	if before != 2 {
		t.Fatalf("shared catalog made %d native calls, want help plus one list", before)
	}
	for _, host := range hosts {
		if _, err := backend.ListActions(t.Context(), fleetDescriptor(host), catalog); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.calls) != before {
		t.Fatal("individual host menus repeated native catalog reads")
	}
	process, err := backend.RunAction(t.Context(), fleetDescriptor(hosts[0]), tuiFleetProfileAction("herdr-remove", profile))
	if err != nil || process == nil || len(fake.calls) != before {
		t.Fatalf("building a bound handoff reloaded native metadata: %v calls=%v", err, fake.calls)
	}
}

func TestTUIFleetHerdrUnknownCatalogNeverBecomesNotAdded(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	fake := &tuiFleetHerdrRunner{unsupportedMachines: true}
	app.sshHostRunner = fake
	catalog, err := backend.LoadHerdr(t.Context())
	if err == nil || catalog.Status != "unsupported" {
		t.Fatalf("unsupported catalog = %+v %v", catalog, err)
	}
	actions, err := backend.ListActions(t.Context(), fleetDescriptor(hosts[0]), catalog)
	if err != nil || hasTUIFleetAction(actions, "herdr-add", false) {
		t.Fatalf("failed inventory became add permission: %+v %v", actions, err)
	}
	fake.unsupportedMachines = false
	catalog, err = backend.LoadHerdr(t.Context())
	if err != nil || catalog.Status != "ready" || len(catalog.Profiles) != 0 {
		t.Fatalf("valid empty catalog = %+v %v", catalog, err)
	}
	actions, err = backend.ListActions(t.Context(), fleetDescriptor(hosts[0]), catalog)
	if err != nil || !hasTUIFleetAction(actions, "herdr-add", false) {
		t.Fatalf("valid empty catalog did not permit add: %+v %v", actions, err)
	}
}

type tuiFleetMutationRunner struct {
	profiles  []herdrremote.Profile
	mutations [][]string
	calls     []sshhost.RunRequest
	help      bool
}

func (f *tuiFleetMutationRunner) Run(_ context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	f.calls = append(f.calls, request)
	if request.Name != "herdr" {
		return sshhost.RunResult{}, errors.New("unexpected network or other program")
	}
	if reflect.DeepEqual(request.Args, []string{"--help"}) {
		if f.help {
			return sshhost.RunResult{Stdout: []byte("--remote --session")}, nil
		}
		return sshhost.RunResult{ExitCode: 2}, nil
	}
	if reflect.DeepEqual(request.Args, []string{"machine", "list", "--json"}) {
		profiles := f.profiles
		if profiles == nil {
			profiles = []herdrremote.Profile{}
		}
		data, _ := json.Marshal(profiles)
		return sshhost.RunResult{Stdout: data}, nil
	}
	if len(request.Args) != 3 || request.Args[0] != "machine" {
		return sshhost.RunResult{}, errors.New("unexpected Herdr server or connection operation")
	}
	for index := range f.profiles {
		if f.profiles[index].ID != request.Args[2] {
			continue
		}
		switch request.Args[1] {
		case "enable":
			f.profiles[index].Enabled = true
		case "disable":
			f.profiles[index].Enabled = false
		case "remove":
			f.profiles = append(f.profiles[:index], f.profiles[index+1:]...)
		default:
			return sshhost.RunResult{}, errors.New("unexpected native mutation")
		}
		f.mutations = append(f.mutations, append([]string(nil), request.Args...))
		return sshhost.RunResult{}, nil
	}
	return sshhost.RunResult{}, errors.New("profile not found")
}

func TestTUIFleetHerdrCleanupBypassesOnlyConnectionGates(t *testing.T) {
	for _, config := range []struct{ name, extra string }{
		{"windows", "remote_os='windows'\n"},
		{"overrides", "user='another'\nport=2222\nidentity_file='/fixture/key'\n"},
		{"password", "[hosts.ssh_login_password_source]\ntype='prompt'\n"},
		{"no-remote-help", ""},
	} {
		for _, verb := range []string{"herdr-disable", "herdr-remove"} {
			t.Run(config.name+"/"+verb, func(t *testing.T) {
				app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n"+config.extra)
				profile := herdrremote.Profile{ID: "0123456789abcdef0123456789abcdef", Label: "Custom label", Target: "lab", Session: "agents", Enabled: true}
				fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{profile}}
				app.sshHostRunner, app.interactiveCheck, app.In = fake, func() bool { return true }, strings.NewReader("yes\n")
				t.Setenv("HERDR_ENV", "")
				catalog, err := backend.LoadHerdr(t.Context())
				if err != nil || catalog.Status != "ready" || catalog.RemoteAvailable {
					t.Fatalf("catalog-only capability was discarded: %+v %v", catalog, err)
				}
				actions, err := backend.ListActions(t.Context(), fleetDescriptor(hosts[0]), catalog)
				action := tuiFleetProfileAction(verb, profile)
				if err != nil || !hasTUIFleetAction(actions, action, false) {
					t.Fatalf("cleanup hidden by connection gate: %+v %v", actions, err)
				}
				if err := runTUIFleetHostAction(t.Context(), app, backend, fleetDescriptor(hosts[0]), hosts[0], action); err != nil {
					t.Fatal(err)
				}
				if len(fake.mutations) != 1 || !reflect.DeepEqual(fake.mutations[0], []string{"machine", strings.TrimPrefix(verb, "herdr-"), profile.ID}) {
					t.Fatalf("cleanup invoked wrong native action: %v", fake.mutations)
				}
				for _, want := range []string{profile.Label, "SSH target: lab", "Session: agents", profile.ID, "remote sessions keep running"} {
					if !strings.Contains(app.Out.(*bytes.Buffer).String(), want) {
						t.Fatalf("confirmation lacks %q: %s", want, app.Out)
					}
				}
			})
		}
	}
}

type tuiFleetPromptReader struct {
	once func()
	in   io.Reader
}

func (r *tuiFleetPromptReader) Read(p []byte) (int, error) {
	if r.once != nil {
		r.once()
		r.once = nil
	}
	return r.in.Read(p)
}

func TestTUIFleetHerdrReviewedIdentitySurvivesPromptUntilApply(t *testing.T) {
	for _, change := range []string{"label", "target", "session", "enabled", "endpoint", "selected", "cancel"} {
		t.Run(change, func(t *testing.T) {
			app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
			profile := herdrremote.Profile{ID: "0123456789abcdef0123456789abcdef", Label: "Lab", Target: "lab", Session: "agents", Enabled: true}
			fake := &tuiFleetMutationRunner{profiles: []herdrremote.Profile{profile}}
			app.sshHostRunner, app.interactiveCheck = fake, func() bool { return true }
			answer := "yes\n"
			if change == "cancel" {
				answer = "no\n"
			}
			app.In = &tuiFleetPromptReader{in: strings.NewReader(answer), once: func() {
				switch change {
				case "label":
					fake.profiles[0].Label = "Changed"
				case "target":
					fake.profiles[0].Target = "other"
				case "session":
					fake.profiles[0].Session = "other"
				case "enabled":
					fake.profiles[0].Enabled = false
				case "selected":
					fake.profiles[0].Selected = true
				case "endpoint":
					if err := os.WriteFile(app.remotesPath, []byte("schema_version=1\n[[hosts]]\nname='lab'\nssh_alias='other'\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}}
			err := runTUIFleetHostAction(t.Context(), app, backend, fleetDescriptor(hosts[0]), hosts[0], tuiFleetProfileAction("herdr-disable", profile))
			if change == "selected" {
				if err != nil || len(fake.mutations) != 1 {
					t.Fatalf("client selection incorrectly invalidated profile authority: %v %v", err, fake.mutations)
				}
			} else if err == nil || len(fake.mutations) != 0 {
				t.Fatalf("changed/canceled plan mutated Herdr: %v %v", err, fake.mutations)
			}
		})
	}
}

func TestTUIFleetHerdrProfileActionsRetainDistinctIDsAndSessions(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	fake := &tuiFleetHerdrRunner{profiles: []herdrremote.Profile{
		{ID: "0123456789abcdef0123456789abcdef", Label: "Duplicate", Target: "lab", Session: "agents", Enabled: true},
		{ID: "1123456789abcdef0123456789abcdef", Label: "Duplicate", Target: "lab", Session: "default", Enabled: false},
		{ID: "2123456789abcdef0123456789abcdef", Label: "Other", Target: "alias-for-same-ip", Session: "default", Enabled: true},
	}}
	app.sshHostRunner = fake
	catalog, err := backend.LoadHerdr(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actions, err := backend.ListActions(t.Context(), fleetDescriptor(hosts[0]), catalog)
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]string{}
	for _, action := range actions {
		if action.ProfileID != "" {
			profiles[action.ProfileID] = action.ProfileSession
		}
	}
	if len(profiles) != 2 || profiles[fake.profiles[0].ID] != "agents" || profiles[fake.profiles[1].ID] != "default" {
		t.Fatalf("profile picker metadata lost exact identity: %v", profiles)
	}
}
