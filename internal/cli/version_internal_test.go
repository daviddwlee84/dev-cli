package cli

import (
	"strings"
	"testing"
)

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
