package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
)

func configWithUpdateCheck(v bool) config.Config {
	return config.Config{Update: config.Update{Check: v}}
}

func TestNewerReleaseNote(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		cached  releaseCheck
		version string
		want    bool
	}{
		"newer release, never nudged": {
			releaseCheck{TagName: "v0.3.0"}, "v0.2.0", true,
		},
		"build past an older tag, newer release out": {
			releaseCheck{TagName: "v0.3.0"}, "v0.2.0-5-gabc1234", true,
		},
		"already current": {
			releaseCheck{TagName: "v0.2.0"}, "v0.2.0", false,
		},
		"cache older than build": {
			releaseCheck{TagName: "v0.1.0"}, "v0.2.0", false,
		},
		"source build never nags": {
			releaseCheck{TagName: "v0.3.0"}, "dev", false,
		},
		"nudged within the day": {
			releaseCheck{TagName: "v0.3.0", NudgedAt: now.Add(-time.Hour)}, "v0.2.0", false,
		},
		"nudged over a day ago": {
			releaseCheck{TagName: "v0.3.0", NudgedAt: now.Add(-48 * time.Hour)}, "v0.2.0", true,
		},
		"empty cache": {
			releaseCheck{}, "v0.2.0", false,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			note, ok := newerReleaseNote(c.cached, c.version, now)
			if ok != c.want {
				t.Fatalf("newerReleaseNote ok = %v, want %v (note %q)", ok, c.want, note)
			}
			if ok && !strings.Contains(note, c.cached.TagName) {
				t.Errorf("note %q should name the tag %q", note, c.cached.TagName)
			}
		})
	}
}

func TestUpdateCheckEnabledEnvOverride(t *testing.T) {
	cfg := configWithUpdateCheck(true)
	t.Setenv("DEV_NO_UPDATE_CHECK", "1")
	if updateCheckEnabled(cfg) {
		t.Error("DEV_NO_UPDATE_CHECK=1 must disable the check")
	}
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	if !updateCheckEnabled(cfg) {
		t.Error("with the env unset the config value should win")
	}
	if updateCheckEnabled(configWithUpdateCheck(false)) {
		t.Error("check=false must disable it")
	}
}

func TestBuildDescriptionSeparatesReleaseFromDistance(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		release string
		ahead   int
		dev     bool
	}{
		"exact release tag":  {"v0.1.11", "v0.1.11", 0, false},
		"commits past a tag": {"v0.1.11-6-gda7d7ef", "v0.1.11", 6, true},
		// The Makefile's --dirty and Go's +dirty mean the same thing.
		"dirty describe":  {"v0.1.11-6-gda7d7ef-dirty", "v0.1.11", 6, true},
		"dirty tag":       {"v0.1.11-dirty", "v0.1.11", 0, false},
		"pseudo-version":  {"v0.1.12-0.20260829042323-da7d7ef4d6ca", "", 0, true},
		"dirty pseudo":    {"v0.1.12-0.20260829042323-da7d7ef4d6ca+dirty", "", 0, true},
		"untagged pseudo": {"v0.0.0-20260829042323-da7d7ef4d6ca", "", 0, true},
		"no version":      {"dev", "", 0, true},
		// A prerelease is a real version, not a distance from one.
		"prerelease": {"v0.2.0-rc.1", "v0.2.0-rc.1", 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			release, ahead, dev := buildDescription(tc.version)
			if release != tc.release || ahead != tc.ahead || dev != tc.dev {
				t.Errorf("buildDescription(%q) = (%q, %d, %v), want (%q, %d, %v)",
					tc.version, release, ahead, dev, tc.release, tc.ahead, tc.dev)
			}
		})
	}
}

func TestVersionSummarySaysWhenABuildIsNotAPublishedRelease(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	Version = "v0.1.11-6-gda7d7ef"
	got := versionSummary()
	if !strings.Contains(got, "6 commit(s) past v0.1.11") || !strings.Contains(got, "not a published release") {
		t.Errorf("a build past its release should say so: %q", got)
	}

	Version = "v0.1.11"
	if got := versionSummary(); got != "v0.1.11" {
		t.Errorf("an exact release should report only itself: %q", got)
	}
}
