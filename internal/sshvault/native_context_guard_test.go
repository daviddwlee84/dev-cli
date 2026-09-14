//go:build darwin || linux

package sshvault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBitwardenNativePlanningFailsBeforeGeneration(t *testing.T) {
	for _, condition := range []string{"no-experimental", "no-native-approval", "no-session", "locked", "unknown-status", "invalid-user", "unreviewed-version", "relative-profile", "missing-data", "unsafe-profile", "node-preload"} {
		t.Run(condition, func(t *testing.T) {
			fixture := newNativeFixture(t, false)
			request := nativeRequest()
			switch condition {
			case "no-experimental":
				request.Experimental = false
			case "no-native-approval":
				request.NativeContextApproved = false
			case "no-session":
				t.Setenv("BW_SESSION", "")
			case "locked":
				fixture.config.Status = "locked"
			case "unknown-status":
				fixture.config.Status = "unknown"
			case "invalid-user":
				fixture.config.UserID = "not-an-account-id"
			case "unreviewed-version":
				fixture.config.Version = "2026.3.1"
			case "relative-profile":
				t.Setenv("BITWARDENCLI_APPDATA_DIR", "relative-profile")
			case "missing-data":
				if os.Remove(filepath.Join(fixture.profile, "data.json")) != nil {
					t.Fatal("remove own fixture metadata")
				}
			case "unsafe-profile":
				if os.Chmod(fixture.profile, 0o777) != nil {
					t.Fatal("change own fixture mode")
				}
			case "node-preload":
				t.Setenv("NODE_OPTIONS", "--require unreviewed-module")
			}
			fixture.save(t)
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), request)
			if err == nil || plan.state != nil || *generated != 0 {
				t.Fatal("unsupported native context reached generation")
			}
			fixture.assertNoCreate(t)
		})
	}
}

func TestBitwardenNativePlanMetadataCannotBeChanged(t *testing.T) {
	fixture := newNativeFixture(t, false)
	service, generated := countedNativeService(nil)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.NativeContextApproved = false },
		func(p *Plan) { p.Experimental = false },
		func(p *Plan) { p.EndpointStatus = "verified" },
		func(p *Plan) { profile := *p.NativeProfile; profile.Path = fixture.root; p.NativeProfile = &profile },
		func(p *Plan) {
			profile := *p.NativeProfile
			profile.Runtime = fixture.entry
			p.NativeProfile = &profile
		},
		func(p *Plan) { p.NativeProfile = nil },
	} {
		changed := plan
		mutate(&changed)
		result, err := service.Apply(context.Background(), changed)
		if !errors.Is(err, ErrStale) || result.Status != StatusNotStarted || *generated != 0 {
			t.Fatal("changed native approval/preview gained authority")
		}
	}
	fixture.assertNoCreate(t)
}

func TestBitwardenNodePackageAndRuntimeAreBound(t *testing.T) {
	for _, component := range []string{"entrypoint", "manifest", "runtime", "runtime-shim"} {
		t.Run(component, func(t *testing.T) {
			fixture := newNativeFixture(t, true)
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatal(err)
			}
			switch component {
			case "entrypoint", "runtime":
				path := fixture.entry
				if component == "runtime" {
					path = fixture.runtime
				}
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal("open own fixture for replacement test")
				}
				_, err = file.WriteString("\nchanged fixture\n")
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal("change own bound fixture")
				}
			case "manifest":
				if os.WriteFile(fixture.manifest, []byte(`{"name":"@bitwarden/cli","version":"2026.3.0","bin":{"bw":"build/bw.js"},"description":"changed fixture"}`), 0o600) != nil {
					t.Fatal("change own public manifest")
				}
			case "runtime-shim":
				if os.WriteFile(fixture.runtime, []byte("#!/bin/sh\nexit 0\n"), 0o700) != nil {
					t.Fatal("replace own runtime with unsupported shim")
				}
			}
			result, err := service.Apply(context.Background(), plan)
			if err == nil || result.Status != StatusNotStarted || *generated != 0 {
				t.Fatal("changed Node execution surface reached generation")
			}
			fixture.assertNoCreate(t)
		})
	}
}

func TestBitwardenUnknownNodePackageAndWrappersDoNotExecute(t *testing.T) {
	for _, component := range []string{"manifest-name", "manifest-bin", "shebang", "runtime"} {
		t.Run(component, func(t *testing.T) {
			fixture := newNativeFixture(t, true)
			switch component {
			case "manifest-name":
				if os.WriteFile(fixture.manifest, []byte(`{"name":"not-bitwarden","version":"2026.3.0","bin":{"bw":"build/bw.js"}}`), 0o600) != nil {
					t.Fatal("change fixture manifest name")
				}
			case "manifest-bin":
				if os.WriteFile(fixture.manifest, []byte(`{"name":"@bitwarden/cli","version":"2026.3.0","bin":{"bw":"other.js"}}`), 0o600) != nil {
					t.Fatal("change fixture bin binding")
				}
			case "shebang":
				if os.WriteFile(fixture.entry, []byte("#!/bin/sh\nexit 0\n"), 0o700) != nil {
					t.Fatal("change fixture wrapper")
				}
			case "runtime":
				if os.WriteFile(fixture.runtime, []byte("#!/usr/bin/env sh\nexit 0\n"), 0o700) != nil {
					t.Fatal("change fixture runtime")
				}
			}
			service, generated := countedNativeService(nil)
			if _, err := service.Plan(context.Background(), nativeRequest()); !errors.Is(err, ErrEntrypoint) || *generated != 0 {
				t.Fatal("unsupported wrapper acquired native execution authority")
			}
			if _, err := os.Stat(fixture.calls); !os.IsNotExist(err) {
				t.Fatal("unsupported wrapper was executed")
			}
		})
	}
}

func TestBitwardenNativeDefaultProfilesMatchPinnedPrecedence(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve fixture")
	}
	bin := filepath.Join(root, "bin")
	if os.Mkdir(bin, 0o700) != nil {
		t.Fatal("make fixture bin")
	}
	executable := filepath.Join(bin, "node")
	for _, test := range []struct {
		goos        string
		environment map[string]string
		want        string
	}{
		{"darwin", map[string]string{"HOME": root}, filepath.Join(root, "Library", "Application Support", "Bitwarden CLI")},
		{"linux", map[string]string{"HOME": root}, filepath.Join(root, ".config", "Bitwarden CLI")},
		{"linux", map[string]string{"HOME": root, "XDG_CONFIG_HOME": filepath.Join(root, "xdg")}, filepath.Join(root, "xdg", "Bitwarden CLI")},
		{"darwin", map[string]string{"HOME": root, "BITWARDENCLI_APPDATA_DIR": filepath.Join(root, "explicit")}, filepath.Join(root, "explicit")},
	} {
		selected, _, portable, err := nativeProfilePath(test.goos, test.environment, executable)
		if err != nil || portable || selected != test.want {
			t.Fatal("profile mapping differs from pinned native source")
		}
	}
	portable := filepath.Join(bin, "bw-data")
	if os.Mkdir(portable, 0o700) != nil {
		t.Fatal("make own portable profile")
	}
	selected, _, present, err := nativeProfilePath("darwin", map[string]string{"HOME": root, "BITWARDENCLI_APPDATA_DIR": filepath.Join(root, "explicit")}, executable)
	if err != nil || !present || selected != portable {
		t.Fatal("portable profile did not take precedence over explicit environment")
	}
}

func TestBitwardenAmbientChangeDuringPreflightAbortsGeneration(t *testing.T) {
	fixture := newNativeFixture(t, false)
	runner := &nativeHookRunner{}
	service, generated := countedNativeService(runner)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	runner.before = func(args []string) {
		if len(args) > 0 && args[len(args)-1] == "status" {
			t.Setenv("BW_SESSION", "changed-before-generation")
		}
	}
	result, err := service.Apply(context.Background(), plan)
	if err == nil || result.Status != StatusNotStarted || *generated != 0 {
		t.Fatal("late ambient preflight change generated private material")
	}
	fixture.assertNoCreate(t)
}
