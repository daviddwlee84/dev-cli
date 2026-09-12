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

func dotfileTestHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	return h
}

func dotfileTestWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDotfilePassiveCommandsSkipStartupAndNativeProcesses(t *testing.T) {
	h := dotfileTestHome(t)
	badConfig := filepath.Join(h, "invalid-dev.toml")
	dotfileTestWrite(t, badConfig, "invalid = [toml")
	for _, args := range [][]string{{"dotfile", "--json"}, {"dotfile", "status", "--json"}, {"fleet", "_dotfile-status"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &App{In: strings.NewReader(""), Out: &out, Err: &errOut}
			cleanups := 0
			root := newRootCommandWithCleanup(app, func() { cleanups++ })
			root.SetArgs(append([]string{"--config", badConfig}, args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			var status dotfile.Status
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.SourceState != "absent" || status.DeploymentDrift != "unknown" {
				t.Fatalf("status = %+v", status)
			}
			if cleanups != 0 || app.Catalog != nil || app.Tasks != nil || app.Sizes != nil || app.deferredReleaseRefresh {
				t.Fatal("passive command ran startup services")
			}
		})
	}
	for _, name := range []string{"config", "data", "cache"} {
		if _, err := os.Stat(filepath.Join(h, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("passive status touched %s", name)
		}
	}
}

func dotfileFakeNative(t *testing.T, h string, exit string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX native command fixture")
	}
	log := filepath.Join(h, "native-args")
	bin := filepath.Join(h, "bin", "chezmoi")
	dotfileTestWrite(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DEV_DOTFILE_TEST_LOG\"\nprintf native-stdout\nprintf native-stderr >&2\nexit "+exit+"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_DOTFILE_TEST_LOG", log)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestDotfileNativeStreamsArgumentsAndFailure(t *testing.T) {
	h := dotfileTestHome(t)
	log := dotfileFakeNative(t, h, "23")
	var out, errOut bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errOut}
	root := newRootCommand(app)
	root.SetArgs([]string{"dotfile", "--chezmoi-config", "/tmp/custom.toml", "diff", "--", "--exclude=scripts", "a path"})
	err := root.Execute()
	var native *dotfile.NativeError
	if !errors.As(err, &native) || native.ExitCode() != 23 {
		t.Fatalf("native error = %v", err)
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "--config\n/tmp/custom.toml\ndiff\n--exclude=scripts\na path\n" {
		t.Fatalf("argv = %q", args)
	}
	if out.String() != "native-stdout" || errOut.String() != "native-stderr" {
		t.Fatalf("streams %q %q", &out, &errOut)
	}
}

func TestDotfileSetupReportsThenInitializesAndPreservesExistingSource(t *testing.T) {
	h := dotfileTestHome(t)
	log := dotfileFakeNative(t, h, "0")
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		root := newRootCommand(&App{In: strings.NewReader(""), Out: &out, Err: &errOut, interactiveCheck: func() bool { return false }})
		root.SetArgs(append([]string{"dotfile", "setup"}, args...))
		err := root.Execute()
		return out.String(), err
	}
	if _, err := run("--repo", "https://example.test/dotfiles.git"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("report-only setup ran native init")
	}
	if _, err := run("--repo", "https://example.test/dotfiles.git", "--yes", "--apply"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(log)
	if string(args) != "init\n--apply\nhttps://example.test/dotfiles.git\n" {
		t.Fatalf("argv = %q", args)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	dotfileTestWrite(t, filepath.Join(h, "data", "chezmoi", "existing"), "keep me")
	out, err := run("--preset", "david", "--yes", "--apply")
	if err != nil || !strings.Contains(out, "Existing source preserved") {
		t.Fatalf("setup = %v %s", err, out)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("setup replaced existing source")
	}
}

func TestDotfileSetupPreservesConfigWhoseSourceIsMissing(t *testing.T) {
	h := dotfileTestHome(t)
	log := dotfileFakeNative(t, h, "0")
	path := filepath.Join(h, "config", "chezmoi", "chezmoi.toml")
	body := "sourceDir = '~/temporarily-missing'\n[data]\nname = 'preserve-me'\n"
	dotfileTestWrite(t, path, body)
	var out, errOut bytes.Buffer
	root := newRootCommand(&App{In: strings.NewReader(""), Out: &out, Err: &errOut, interactiveCheck: func() bool { return false }})
	root.SetArgs([]string{"dotfile", "setup", "--repo", "https://example.test/different.git", "--yes", "--apply"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Existing configuration preserved") {
		t.Fatalf("output = %s", &out)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing source led to native init")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != body {
		t.Fatal("existing configuration changed")
	}
}

func TestFleetDotfileStatusRequiresExplicitHosts(t *testing.T) {
	dotfileTestHome(t)
	var out, errOut bytes.Buffer
	root := newRootCommand(&App{In: strings.NewReader(""), Out: &out, Err: &errOut})
	root.SetArgs([]string{"fleet", "dotfile", "status", "--json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("unscoped status = %v", err)
	}
	if _, err := collectFleetDotfileStatus(context.Background(), fleet.Config{}, nil); err == nil {
		t.Fatal("unscoped collector accepted")
	}
}

type fakeDotfileFleetRunner struct {
	t        *testing.T
	response fleet.Result
}

func (r fakeDotfileFleetRunner) RunWithOptions(_ context.Context, _ fleet.Host, args []string, stdin []byte, _ fleet.RunOptions) fleet.Result {
	r.t.Helper()
	if !reflect.DeepEqual(args, []string{"fleet", "_dotfile-status"}) || len(stdin) != 0 {
		r.t.Fatalf("unsafe status request: %v / %d bytes", args, len(stdin))
	}
	return r.response
}

func TestFleetDotfileStatusProtocolFailuresRemainUnknown(t *testing.T) {
	valid, _ := json.Marshal(dotfile.Status{SchemaVersion: 1, Platform: "linux", ConfigState: "absent", SourceState: "absent", GitState: "absent", DeploymentDrift: "unknown"})
	changed := func(edit func(map[string]any)) []byte {
		var fields map[string]any
		_ = json.Unmarshal(valid, &fields)
		edit(fields)
		data, _ := json.Marshal(fields)
		return data
	}
	for _, test := range []struct {
		name     string
		response fleet.Result
		state    fleet.HostState
	}{
		{"ok", fleet.Result{Stdout: valid}, fleet.HostOK},
		{"missing-dev", fleet.Result{ExitCode: 127}, fleet.HostNoDev},
		{"old-dev", fleet.Result{ExitCode: 1, Stderr: []byte("unknown command")}, fleet.HostIncompatible},
		{"unreachable", fleet.Result{ExitCode: 255}, fleet.HostUnreachable},
		{"timeout", fleet.Result{TimedOut: true}, fleet.HostTimeout},
		{"invalid", fleet.Result{Stdout: []byte("{}")}, fleet.HostInvalid},
		{"omitted-installed", fleet.Result{Stdout: changed(func(f map[string]any) { delete(f, "installed") })}, fleet.HostInvalid},
		{"null-installed", fleet.Result{Stdout: changed(func(f map[string]any) { f["installed"] = nil })}, fleet.HostInvalid},
		{"missing-git-state", fleet.Result{Stdout: changed(func(f map[string]any) { delete(f, "git_state") })}, fleet.HostInvalid},
		{"invalid-git-state", fleet.Result{Stdout: changed(func(f map[string]any) { f["git_state"] = "clean" })}, fleet.HostInvalid},
		{"invalid-revision", fleet.Result{Stdout: changed(func(f map[string]any) { f["git_state"], f["git_revision"] = "present", "not-an-oid" })}, fleet.HostInvalid},
		{"missing-revision", fleet.Result{Stdout: changed(func(f map[string]any) { f["git_state"] = "present" })}, fleet.HostInvalid},
		{"inconsistent-revision", fleet.Result{Stdout: changed(func(f map[string]any) { f["git_revision"] = strings.Repeat("a", 40) })}, fleet.HostInvalid},
		{"revision-without-source", fleet.Result{Stdout: changed(func(f map[string]any) { f["git_state"], f["git_revision"] = "present", strings.Repeat("a", 40) })}, fleet.HostInvalid},
		{"git-revision", fleet.Result{Stdout: changed(func(f map[string]any) {
			f["source_state"], f["git_state"], f["git_revision"] = "present", "present", strings.Repeat("a", 40)
		})}, fleet.HostOK},
		{"trailing", fleet.Result{Stdout: append(valid, []byte(" {}")...)}, fleet.HostInvalid},
		{"oversized", fleet.Result{Stdout: bytes.Repeat([]byte(" "), int(dotfile.MaxStatusBytes)+1)}, fleet.HostInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := collectFleetDotfileHost(context.Background(), fleet.Host{Name: "lab"}, fakeDotfileFleetRunner{t, test.response})
			if result.State != test.state {
				t.Fatalf("result = %+v", result)
			}
			if test.state != fleet.HostOK && result.Status != nil {
				t.Fatal("failure became a configured host")
			}
		})
	}
}
