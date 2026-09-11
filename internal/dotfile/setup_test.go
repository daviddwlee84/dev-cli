package dotfile

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetupPolicyPreservesExistingState(t *testing.T) {
	for _, test := range []struct{ config, source, mode string }{
		{"absent", "present", "existing-source"},
		{"present", "absent", "existing-config"},
		{"unknown", "unknown", "blocked"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			status := Status{ConfigState: test.config, SourceState: test.source, Installed: true, Platform: "linux"}
			plan, err := planSetupStatus(Options{}, SetupRequest{RepoURL: "https://example.test/new.git", Apply: true}, status)
			if plan.Mode != test.mode || plan.ready {
				t.Fatalf("plan = %+v", plan)
			}
			if test.mode == "blocked" && err == nil {
				t.Fatal("unknown setup was accepted")
			}
			if err := ApplySetup(context.Background(), plan, nil, io.Discard, io.Discard); err == nil {
				t.Fatal("preserved state authorized native init")
			}
		})
	}
}

func TestSetupCustomRepoBypassesOnlyDavidBootstrap(t *testing.T) {
	for _, platform := range []string{"termux", "ish", "openwrt"} {
		t.Run(platform, func(t *testing.T) {
			status := Status{ConfigState: "absent", SourceState: "absent", Installed: true, Platform: platform, Recommendation: Recommended(platform)}
			for _, request := range []SetupRequest{{}, {Preset: "david"}} {
				plan, err := planSetupStatus(Options{}, request, status)
				if err != nil || plan.Mode != "bootstrap" || plan.ready {
					t.Fatalf("preset plan = %+v / %v", plan, err)
				}
			}
			plan, err := planSetupStatus(Options{}, SetupRequest{RepoURL: "https://example.test/own.git"}, status)
			if err != nil || plan.Mode != "initialize" || !plan.ready {
				t.Fatalf("custom plan = %+v / %v", plan, err)
			}
		})
	}
}

func TestSetupAuthorityIncludesNativeConfigContents(t *testing.T) {
	h := isolatedHome(t)
	path := filepath.Join(h, "chezmoi.toml")
	write(t, path, "[data]\nrole='first'\n")
	status := Status{ConfigPath: path, ConfigState: "present", SourceState: "absent"}
	before := setupIdentity(status)
	write(t, path, "[data]\nrole='changed'\n")
	if setupIdentity(status) == before {
		t.Fatal("same-path config mutation retained authority")
	}
}

func TestApplySetupRejectsNewConfigAfterReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	h := isolatedHome(t)
	bin := filepath.Join(h, "bin", "chezmoi")
	sentinel := filepath.Join(h, "native-executed")
	write(t, bin, "#!/bin/sh\ntouch '"+sentinel+"'\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	plan, err := PlanSetup(context.Background(), Options{}, SetupRequest{RepoURL: "https://example.test/dotfiles.git"})
	if err != nil || !plan.ready {
		t.Fatalf("plan = %+v / %v", plan, err)
	}
	// Keep the same effective source while adding native initialization data.
	write(t, filepath.Join(h, "config", "chezmoi", "chezmoi.toml"), "[data]\nname='new configuration'\n")
	if err := ApplySetup(context.Background(), plan, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("stale plan initialized")
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale plan invoked chezmoi")
	}
}
