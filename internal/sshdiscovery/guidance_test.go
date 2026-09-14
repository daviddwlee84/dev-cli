package sshdiscovery

import (
	"strings"
	"testing"
)

func TestLANGuidanceExplainsOnlyWarnedLANReports(t *testing.T) {
	warned := Report{Source: SourceLAN, Probes: &ProbeSummary{Attempted: 13, Refused: 1, Timeout: 1, Unreachable: 10, Other: 1}, Warnings: []string{WarningNoReachableEndpoints}}
	darwin := LANGuidance(warned, "darwin")
	if !strings.Contains(darwin, "13 probed: 1 refused, 10 unreachable, 1 timed out, 1 other") || !strings.Contains(darwin, "Local Network") {
		t.Fatalf("darwin guidance=%q", darwin)
	}
	if linux := LANGuidance(warned, "linux"); strings.Contains(linux, "Local Network") || !strings.Contains(linux, "firewall") {
		t.Fatalf("linux guidance=%q", linux)
	}
	for _, report := range []Report{
		{Source: SourceLAN, Probes: warned.Probes},
		{Source: SourceLAN, Warnings: warned.Warnings},
		{Source: SourceTailscale, Probes: warned.Probes, Warnings: warned.Warnings},
	} {
		if got := LANGuidance(report, "darwin"); got != "" {
			t.Fatalf("report=%#v guidance=%q", report, got)
		}
	}
}
