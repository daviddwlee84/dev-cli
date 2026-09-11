package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/fleetnav"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
)

func TestFleetNavigationOutsideAttachesExactSessionWithoutFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	for _, exitCode := range []string{"0", "1"} {
		t.Run("exit-"+exitCode, func(t *testing.T) {
			app, backend, _ := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
			app.sshHostRunner = &tuiFleetMutationRunner{help: true, profiles: []herdrremote.Profile{navigationAdapterProfile(false)}}
			bin := t.TempDir()
			log := filepath.Join(bin, "herdr-argv")
			sshLog := filepath.Join(bin, "unexpected-ssh")
			t.Setenv("FLEET_ATTACH_LOG", log)
			t.Setenv("FLEET_ATTACH_SSH_LOG", sshLog)
			t.Setenv("FLEET_ATTACH_EXIT", exitCode)
			herdr := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FLEET_ATTACH_LOG\"\nprintf 'session=%s socket=%s client=%s pane=%s\\n' \"${HERDR_SESSION-}\" \"${HERDR_SOCKET_PATH-}\" \"${HERDR_CLIENT_SOCKET_PATH-}\" \"${HERDR_PANE_ID-}\" >> \"$FLEET_ATTACH_LOG\"\nexit \"$FLEET_ATTACH_EXIT\"\n"
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdr), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nprintf unexpected > \"$FLEET_ATTACH_SSH_LOG\"\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("HERDR_ENV", "")
			t.Setenv("HERDR_SESSION", "wrong-local-session")
			t.Setenv("HERDR_SOCKET_PATH", "/wrong/socket")
			t.Setenv("HERDR_CLIENT_SOCKET_PATH", "/wrong/client")
			t.Setenv("HERDR_PANE_ID", "wrong-pane")
			var phases []string
			backend.protocolRun = func(_ context.Context, _ fleet.Host, args []string, input []byte, _ fleet.RunOptions) fleet.Result {
				if !reflect.DeepEqual(args, []string{"fleet", fleet.HerdrRepoHelper}) {
					t.Fatalf("legacy helper invoked: %v", args)
				}
				var request fleet.HerdrRepoRequest
				if err := json.Unmarshal(input, &request); err != nil {
					t.Fatal(err)
				}
				if request.Session != "agents" {
					t.Fatalf("wrong preparation session: %+v", request)
				}
				phases = append(phases, request.Phase)
				data, _ := json.Marshal(navigationAdapterReply(request))
				return fleet.Result{Stdout: data}
			}
			result, err := newFleetNavigation(app, backend).Navigate(t.Context(), fleetnav.Request{Host: "lab", Repository: &fleet.OpenRequest{Path: "/srv/repo"}})
			if (err == nil) != (exitCode == "0") || result.Session != "agents" || result.CatalogState != "unchanged" || result.RepositoryState != "prepared" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if exitCode == "1" && result.State != "partial" {
				t.Fatalf("lost prepared workspace: %+v", result)
			}
			if !reflect.DeepEqual(phases, []string{"check", "prepare"}) {
				t.Fatalf("phases=%v", phases)
			}
			argv, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(argv) != "--remote\nlab\n--session\nagents\nsession= socket= client= pane=\n" {
				t.Fatalf("attach lost explicit session/isolation: %q", argv)
			}
			if _, err := os.Stat(sshLog); !os.IsNotExist(err) {
				t.Fatalf("Herdr completion fell back to SSH: %v", err)
			}
		})
	}
}

func TestFleetNavigationAttachGuardPreventsNestedNativeProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='lab'\n")
	bin := t.TempDir()
	log := filepath.Join(bin, "unexpected-herdr")
	t.Setenv("FLEET_ATTACH_LOG", log)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf unexpected > \"$FLEET_ATTACH_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := newFleetNavigation(app, backend).Attach(t.Context(), fleetnav.Target{Host: hosts[0], EndpointID: fleet.EndpointID(hosts[0])}, "agents")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "herdr") {
		t.Fatalf("nested attach was not refused: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("nested Herdr process started: %v", err)
	}
}
