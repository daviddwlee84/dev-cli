package tui

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
)

func TestSSHEmptyUnreachableLANScanExplainsItselfInStatus(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	m.sshUI.generation = 1
	report := sshdiscovery.Report{Source: sshdiscovery.SourceLAN, Scope: "scope", Status: sshdiscovery.StatusReady, Complete: true, ObservedAt: time.Now().UTC(), Candidates: []sshdiscovery.Candidate{},
		Probes: &sshdiscovery.ProbeSummary{Attempted: 2, Refused: 1, Unreachable: 1}, Warnings: []string{sshdiscovery.WarningNoReachableEndpoints}}
	next, _ := m.applySSHEvent(sshEventMsg{generation: 1, kind: "discovered", discovery: SSHDiscoveryResult{Report: report}})
	m = next.(Model)
	want := sshdiscovery.LANGuidance(report, runtime.GOOS)
	if want == "" || !strings.HasPrefix(m.status, want) || !strings.Contains(m.status, "Esc shows all") {
		t.Fatalf("status=%q", m.status)
	}
	m.sshUI.generation = 2
	report.Warnings = nil
	next, _ = m.applySSHEvent(sshEventMsg{generation: 2, kind: "discovered", discovery: SSHDiscoveryResult{Report: report}})
	if status := next.(Model).status; !strings.HasPrefix(status, "0 discoveries") {
		t.Fatalf("status without warning=%q", status)
	}
}
