//go:build darwin || linux

package sshvault

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func nativeManifestBinFixture(t *testing.T, fixture *nativeFixture, bin string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": "@bitwarden/cli", "version": BitwardenSchemaVersion, "bin": map[string]string{"bw": bin}})
	if err != nil || os.WriteFile(fixture.manifest, body, 0o600) != nil {
		t.Fatal("write own public manifest fixture")
	}
}

func TestNativeNodeDocumentedManifestBinForms(t *testing.T) {
	for _, form := range []string{"build/bw.js", "./build/bw.js", "././build/bw.js"} {
		t.Run(form, func(t *testing.T) {
			fixture := newNativeFixture(t, true)
			nativeManifestBinFixture(t, fixture, form)
			service, _ := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if err != nil {
				t.Fatalf("documented leading-dot form was refused: %v", err)
			}
			if plan.state.native.tool.binName != form || plan.state.native.tool.bin == nil || plan.state.native.tool.bin.canonical != fixture.entry {
				t.Fatal("requested and actual package bin identity were not both bound")
			}
			result, err := service.Apply(context.Background(), plan)
			if err != nil || !CanSelectAgentKey(plan, result) {
				t.Fatalf("documented Node packaging fixture could not apply: %v", err)
			}
		})
	}
}

func TestNativeNodeRejectsAmbiguousManifestBinTraversal(t *testing.T) {
	for _, form := range []string{"../build/bw.js", "./../build/bw.js", "build/../build/bw.js", "build/./bw.js", "build/bw.js/"} {
		t.Run(form, func(t *testing.T) {
			fixture := newNativeFixture(t, true)
			nativeManifestBinFixture(t, fixture, form)
			service, generated := countedNativeService(nil)
			if _, err := service.Plan(context.Background(), nativeRequest()); err == nil || *generated != 0 {
				t.Fatal("ambiguous manifest traversal acquired native authority")
			}
			if _, err := os.Stat(fixture.calls); !os.IsNotExist(err) {
				t.Fatal("ambiguous manifest was executed")
			}
		})
	}
}

func TestNativePATHDoesNotCleanAwayIntermediateTraversal(t *testing.T) {
	fixture := newNativeFixture(t, false)
	unsafe := filepath.Join(fixture.root, "unsafe")
	if os.Mkdir(unsafe, 0o700) != nil || os.Chmod(unsafe, 0o777) != nil {
		t.Fatal("make own unsafe PATH component")
	}
	// Keep the raw dot traversal; filepath.Join would erase the regression.
	t.Setenv("PATH", fixture.root+"/unsafe/../bin")
	service, generated := countedNativeService(nil)
	if _, err := service.Plan(context.Background(), nativeRequest()); err == nil || *generated != 0 {
		t.Fatal("PATH normalization hid an unsafe component")
	}
	if _, err := os.Stat(fixture.calls); !os.IsNotExist(err) {
		t.Fatal("unsafe PATH was executed")
	}
}

func TestNativePATHCurrentDirectoryRemainsExplicitlyBound(t *testing.T) {
	fixture := newNativeFixture(t, false)
	t.Chdir(fixture.bin)
	t.Setenv("PATH", ".")
	t.Setenv("DEV_SSHVAULT_NATIVE_CWD", fixture.bin)
	service, _ := countedNativeService(nil)
	plan, err := service.Plan(context.Background(), nativeRequest())
	if err != nil || plan.NativeProfile.CWD != fixture.bin || plan.NativeProfile.Entrypoint != fixture.entry {
		t.Fatalf("explicit current-directory PATH could not be bound: %v", err)
	}
}

func TestNativeManifestBinIntermediatePathsAreGuarded(t *testing.T) {
	for _, change := range []string{"unsafe-before", "unsafe-after", "retarget-same-final"} {
		t.Run(change, func(t *testing.T) {
			fixture := newNativeFixture(t, true)
			middle := filepath.Join(fixture.root, "middle")
			hop := filepath.Join(middle, "hop")
			build := filepath.Dir(fixture.entry)
			alias := filepath.Join(filepath.Dir(build), "alias")
			if os.Mkdir(middle, 0o700) != nil || os.Symlink(build, hop) != nil || os.Symlink(hop, alias) != nil {
				t.Fatal("create own indirect manifest path")
			}
			nativeManifestBinFixture(t, fixture, "alias/bw.js")
			if change == "unsafe-before" && os.Chmod(middle, 0o777) != nil {
				t.Fatal("change own intermediate directory mode")
			}
			service, generated := countedNativeService(nil)
			plan, err := service.Plan(context.Background(), nativeRequest())
			if change == "unsafe-before" {
				if err == nil || *generated != 0 {
					t.Fatal("unsafe manifest reference acquired a plan")
				}
				if _, err := os.Stat(fixture.calls); !os.IsNotExist(err) {
					t.Fatal("unsafe manifest reference was executed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if change == "unsafe-after" {
				if os.Chmod(middle, 0o777) != nil {
					t.Fatal("change own intermediate directory mode")
				}
			} else {
				other := filepath.Join(fixture.root, "other-hop")
				if os.Symlink(build, other) != nil || os.Remove(hop) != nil || os.Symlink(other, hop) != nil {
					t.Fatal("retarget own intermediate manifest reference")
				}
			}
			result, err := service.Apply(context.Background(), plan)
			if err == nil || result.Status != StatusNotStarted || *generated != 0 {
				t.Fatal("changed manifest reference reached generation")
			}
			fixture.assertNoCreate(t)
		})
	}
}
