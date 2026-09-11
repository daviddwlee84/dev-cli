package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/dotfile"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

func TestTUIFleetDotfileStatusKeepsValidatedEndpointThroughFinalTransport(t *testing.T) {
	app, backend, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='reviewed'\n")
	descriptor := fleetDescriptor(hosts[0])
	validated, err := backend.host(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	// The name changes after the outer handoff validated the reviewed host.
	if err := os.WriteFile(app.remotesPath, []byte("schema_version=1\n[[hosts]]\nname='lab'\nssh_alias='unreviewed'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	backend.run = func(_ context.Context, host fleet.Host, args []string, options fleet.RunOptions) fleet.Result {
		calls++
		if host.SSHAlias != "reviewed" || fleet.EndpointID(host) != descriptor.EndpointID || !reflect.DeepEqual(args, []string{"fleet", "_dotfile-status"}) {
			t.Fatalf("dotfile status was retargeted: %+v %v", host, args)
		}
		data, _ := json.Marshal(dotfile.Status{SchemaVersion: 1, Platform: "linux", ConfigState: "absent", SourceState: "absent", GitState: "absent", DeploymentDrift: "unknown"})
		return fleet.Result{Stdout: data}
	}
	if err := runTUIFleetHostAction(t.Context(), app, backend, descriptor, validated, "dotfile-status"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(app.Out.(*bytes.Buffer).String(), "lab") {
		t.Fatalf("calls=%d output=%s", calls, app.Out)
	}
}

func TestTUIFleetLocalDotfileStatusDoesNotInvokeNativeChezmoi(t *testing.T) {
	h := dotfileTestHome(t)
	log := dotfileFakeNative(t, h, "99")
	app, backend, _ := tuiFleetFixture(t, "")
	if err := runTUIFleetHostAction(t.Context(), app, backend, localFleetDescriptor(), fleet.Host{}, "dotfile-status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(app.Out.(*bytes.Buffer).String(), "Deployment drift: unknown") {
		t.Fatalf("status = %s", app.Out)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("local status invoked native chezmoi")
	}
}

func TestFleetOpenRejectsRetargetedSelectedEndpointBeforeSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake-SSH sentinel")
	}
	app, _, hosts := tuiFleetFixture(t, "[[hosts]]\nname='lab'\nssh_alias='reviewed'\n")
	expected := fleet.EndpointID(hosts[0])
	if err := os.WriteFile(app.remotesPath, []byte("schema_version=1\n[[hosts]]\nname='lab'\nssh_alias='unreviewed'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	sentinel := filepath.Join(bin, "ssh-executed")
	t.Setenv("DEV_FLEET_OPEN_SENTINEL", sentinel)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n: > \"$DEV_FLEET_OPEN_SENTINEL\"\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cmd := newFleetOpenCmd(app)
	cmd.SetArgs([]string{"lab", "/same/path/on/both/hosts", "--expected-endpoint", expected})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "connection changed") {
		t.Fatalf("retargeted open = %v", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retargeted selected row contacted SSH")
	}
}
