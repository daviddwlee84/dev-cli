package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/sshactivity"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func testDiscoveryCandidate() sshdiscovery.Candidate {
	return sshdiscovery.Candidate{ID: "candidate", NativeID: "192.0.2.8:22", Source: "lan", Scope: "eth0/192.0.2.0/24", Name: "new-host", Addresses: []string{"192.0.2.8"}, Port: 22}
}
func sshDrainEvents(t *testing.T, m Model, command tea.Cmd) Model {
	t.Helper()
	for count := 0; command != nil; count++ {
		if count > 100 {
			t.Fatal("SSH command chain did not terminate")
		}
		message := command()
		next, follow := m.Update(message)
		m = next.(Model)
		command = follow
	}
	return m
}
func TestSSHTableDisplayWidthsAndSourceColumns(t *testing.T) {
	inv := sshTestInventory("工作站 with long name")
	inv.Machines[0].Profiles[0].HostName = "192.0.2.10"
	inv.Machines = append(inv.Machines, SSHRow{ID: "second", Label: "a", Profiles: []sshflow.ConnectionProfile{{ID: "second-p", Alias: "短別名", HostName: "192.0.2.11"}}})
	for _, width := range []int{32, 40, 75, 100, 110, 160} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := New(Actions{}, nil, nil).WithSSH(inv)
			m.view = ViewSSH
			m.width = width
			m.height = 30
			lines := strings.Split(strings.TrimSuffix(ansi.Strip(m.renderSSH()), "\n"), "\n")
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Fatalf("%d columns overflow %d: %q", lipgloss.Width(line), width, line)
				}
			}
			cols := m.sshColumns()
			if width < 75 && strings.Contains(lines[0], "ENDPOINT") {
				t.Fatal("narrow view retained endpoint")
			}
			if (width >= 110) != strings.Contains(lines[0], "SOURCES") {
				t.Fatal("source visibility threshold")
			}
			if width >= 75 {
				offset := 2 + cols[0].width + 2
				for _, line := range lines[1:] {
					if !strings.HasPrefix(ansi.Cut(line, offset, offset+10), "192.0.2.") {
						t.Fatalf("endpoint does not align at %d: %q", offset, line)
					}
				}
			}
		})
	}
}
func TestSSHTreeConfiguredFirstAndRecentUseDoesNotUseTestTime(t *testing.T) {
	now := time.Now()
	inv := sshTestInventory("zeta")
	profile := inv.Machines[0].Profiles[0]
	inv.Machines = append(inv.Machines, SSHRow{ID: "discovery", Label: "aaa", LAN: []sshdiscovery.Candidate{testDiscoveryCandidate()}}, SSHRow{ID: "alpha", Label: "alpha", Profiles: []sshflow.ConnectionProfile{{ID: "alpha-p", Alias: "alpha", Fingerprint: "alpha-f"}}})
	m := New(Actions{}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	m.sshUI.activity = map[string]sshactivity.ProfileRecord{profile.ID: {Fingerprint: profile.Fingerprint, LastUsed: now.Add(-time.Hour)}, "alpha-p": {Fingerprint: "alpha-f", LastTest: &sshactivity.TestRecord{ObservedAt: now}}}
	rows := m.visibleSSHEntries()
	if rows[0].machine.ID != "machine-one" || rows[2].machine.ID != "discovery" {
		t.Fatal("default ordering ignores configured/recent usage")
	}
	m.sshUI.activity[profile.ID] = sshactivity.ProfileRecord{Fingerprint: "old-config", LastUsed: now}
	if m.visibleSSHEntries()[0].machine.ID != "alpha" {
		t.Fatal("repurposed alias inherited old recent use")
	}
}
func TestSSHTreeExpansionSelectionSurvivesGroupingAndFilter(t *testing.T) {
	inv := sshTestInventory("machine")
	inv.Machines[0].Profiles = append(inv.Machines[0].Profiles, sshflow.ConnectionProfile{ID: "p-two", Alias: "second", Fingerprint: "two"})
	m := New(Actions{}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	m.toggleSSHEntry()
	if len(m.visibleSSHEntries()) != 3 {
		t.Fatal("Space did not expose profiles")
	}
	m.selectSSHKey("profile:p-two")
	token := m.currentToken()
	inv.Machines = append([]SSHRow(nil), inv.Machines...)
	inv.Machines[0].ID = "new-associated-machine"
	generation := m.beginViewLoad(ViewSSH, loadRefresh)
	next, _ := m.applySSHLoad(sshLoadedMsg{generation: generation, inventory: inv})
	m = next.(Model)
	if m.currentToken() != token {
		t.Fatal("profile selection lost when discovery regrouped parent")
	}
	m.toggleSSHEntry()
	if entry, _ := m.currentSSHEntry(); entry.profile != nil {
		t.Fatal("collapse did not select parent")
	}
	m.filter = "second"
	if len(m.visibleSSHEntries()) != 2 {
		t.Fatal("matching profile did not temporarily expand parent")
	}
	m.filter = ""
	if len(m.visibleSSHEntries()) != 1 {
		t.Fatal("filter changed saved expansion")
	}
}
func TestSSHEmptyMachineNativeDiscoveryKeepsResultsWhenCacheAndReloadFail(t *testing.T) {
	candidate := testDiscoveryCandidate()
	report := sshdiscovery.Report{Source: "lan", Scope: candidate.Scope, Status: "ready", Complete: true, ObservedAt: time.Now(), Candidates: []sshdiscovery.Candidate{candidate}}
	calls := 0
	actions := SSHActions{
		Load: func(context.Context) (SSHInventory, error) { return SSHInventory{}, errors.New("registry unavailable") },
		LoadWithReports: func(_ context.Context, reports []sshdiscovery.Report) (SSHInventory, error) {
			if len(reports) != 1 {
				t.Fatal("discovery handoff lost direct report")
			}
			return SSHInventory{}, errors.New("registry unavailable")
		},
		Interfaces: func(context.Context) ([]sshdiscovery.InterfaceScope, error) {
			return []sshdiscovery.InterfaceScope{{Interface: "eth0", Address: "192.0.2.1", Prefix: "192.0.2.0/24"}}, nil
		},
		ValidateLAN: func(_ context.Context, r sshdiscovery.LANRequest) (string, error) {
			if r.Interface != "eth0" || len(r.Ports) != 1 || r.Ports[0] != 22 {
				t.Fatalf("scope=%+v", r)
			}
			return candidate.Scope, nil
		},
		Discover: func(_ context.Context, r SSHDiscoveryRequest, emit func(sshdiscovery.Progress)) (SSHDiscoveryResult, error) {
			calls++
			emit(sshdiscovery.Progress{Completed: 1, Total: 1, Candidate: &candidate})
			return SSHDiscoveryResult{Report: report, CacheError: "read-only cache"}, nil
		},
	}
	m := New(Actions{SSH: actions}, nil, nil)
	m.view = ViewSSH
	m.filter = "old search"
	next, cmd := m.runSSHAction(listActionSSHDiscover)
	m = next.(Model)
	if cmd != nil || m.sshUI.dialog.kind != "source" {
		t.Fatal("discovery left native TUI")
	}
	m.sshUI.dialog.index = 1
	next, cmd = m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyEnter})
	m = sshDrainEvents(t, next.(Model), cmd)
	next, _ = m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	next, cmd = m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = sshDrainEvents(t, next.(Model), cmd)
	if calls != 1 || !m.sshUI.onlyDiscovery || m.filter != "" || len(m.visibleSSHEntries()) != 1 {
		t.Fatalf("scan not visible: calls=%d only=%v rows=%d", calls, m.sshUI.onlyDiscovery, len(m.visibleSSHEntries()))
	}
	if m.sshUI.reports[0].Candidates[0].ID != candidate.ID {
		t.Fatal("source result lost")
	}
	next, _, _ = m.updateSSHKey("esc")
	m = next.(Model)
	if m.filter != "old search" || m.sshUI.onlyDiscovery {
		t.Fatal("all connections did not restore prior filter")
	}
}
func TestSSHDiscoveryCancellationRetainsCompletedCandidates(t *testing.T) {
	candidate := testDiscoveryCandidate()
	started := make(chan struct{})
	calls := 0
	m := New(Actions{SSH: SSHActions{Discover: func(ctx context.Context, _ SSHDiscoveryRequest, emit func(sshdiscovery.Progress)) (SSHDiscoveryResult, error) {
		emit(sshdiscovery.Progress{Completed: 1, Total: 8, Candidate: &candidate})
		close(started)
		<-ctx.Done()
		calls++
		return SSHDiscoveryResult{Report: sshdiscovery.Report{Source: "lan", Scope: candidate.Scope, Status: "partial", Candidates: []sshdiscovery.Candidate{candidate}}}, ctx.Err()
	}}}, nil, nil)
	m.view = ViewSSH
	next, cmd := m.startSSHDiscovery(SSHDiscoveryRequest{Source: "lan"}, false)
	m = next.(Model)
	msg := cmd()
	<-started
	next, cmd = m.Update(msg)
	m = next.(Model)
	next, _ = m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyEsc})
	m = sshDrainEvents(t, next.(Model), cmd)
	if calls != 1 || len(m.visibleSSHEntries()) != 1 || !strings.Contains(m.status, "canceled") {
		t.Fatal("cancel lost completed observation")
	}
}
func TestSSHDiscoveryDoesNotMergeSameAddressAcrossScopes(t *testing.T) {
	c := testDiscoveryCandidate()
	other := c
	other.Scope = "different-network"
	m := New(Actions{}, nil, nil)
	m.sshUI.reports = []sshdiscovery.Report{{Source: "lan", Scope: c.Scope, Candidates: []sshdiscovery.Candidate{c}}, {Source: "lan", Scope: other.Scope, Candidates: []sshdiscovery.Candidate{other}}}
	inv := m.withSessionDiscovery(SSHInventory{})
	if len(inv.Machines) != 2 {
		t.Fatal("same address erased scope")
	}
	inv = m.withSessionDiscovery(inv)
	if len(inv.Machines) != 2 {
		t.Fatal("session/cache projection duplicated candidates")
	}
}

type testSSHOnboardPlan struct{ preview sshflow.OnboardPreview }

func (p testSSHOnboardPlan) Preview() sshflow.OnboardPreview { return p.preview }
func TestSSHDiscoveryCandidateNativeFormRetainsProvenanceAndDefaultsSSHOnly(t *testing.T) {
	c := testDiscoveryCandidate()
	report := sshdiscovery.Report{Source: "lan", Scope: c.Scope, Candidates: []sshdiscovery.Candidate{c}}
	var got sshflow.OnboardRequest
	m := New(Actions{SSH: SSHActions{PrepareOnboarding: func(_ context.Context, r sshflow.OnboardRequest) (SSHOnboardingPlan, error) {
		got = r
		return testSSHOnboardPlan{preview: sshflow.OnboardPreview{Targets: []sshflow.OnboardTarget{{Alias: r.Alias, HostName: r.HostName, Port: r.Port, Auth: r.Auth}}}}, nil
	}}}, nil, nil)
	m.view = ViewSSH
	m.sshUI.reports = []sshdiscovery.Report{report}
	next, _ := m.openSSHOnboarding(SSHRow{LAN: []sshdiscovery.Candidate{c}})
	m = next.(Model)
	if m.sshUI.dialog.value("fleet") != "no" || m.sshUI.dialog.value("herdr") != "no" || m.sshUI.dialog.value("auth") != "config" {
		t.Fatal("new connection default authorizes external registration")
	}
	next, cmd := m.prepareSSHOnboarding()
	m = sshDrainEvents(t, next.(Model), cmd)
	if got.Candidate == nil || got.Report == nil || got.Candidate.ID != c.ID || got.HostName != c.Addresses[0] || got.To != "" {
		t.Fatalf("source-to-form handoff=%+v", got)
	}
	if m.sshUI.dialog.kind != "review" || !strings.Contains(m.sshUI.dialog.body, "save configuration only") {
		t.Fatal("no concrete review")
	}
}
func TestSSHSelectedChildPassesExactProfileToWorkflow(t *testing.T) {
	var got SSHWorkflowRequest
	inv := sshTestInventory("box")
	inv.Machines[0].Profiles = append(inv.Machines[0].Profiles, sshflow.ConnectionProfile{ID: "second", Alias: "alternate", Fingerprint: "other"})
	m := New(Actions{SSH: SSHActions{Workflow: func(_ context.Context, r SSHWorkflowRequest) (SSHWorkflow, error) {
		got = r
		return &testSSHWorkflow{}, nil
	}}}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	m.selectSSHKey("profile:second")
	_, cmd := m.runSSHAction(listActionSSHConnect)
	if cmd == nil || got.Profile == nil || got.Profile.ID != "second" || len(got.Selected.Profiles) != 2 {
		t.Fatal("selected profile was reselected or parent inventory narrowed")
	}
}
func TestSSHBackgroundRefreshDoesNotRunOutsideSSHOrDuringDialog(t *testing.T) {
	calls := 0
	m := New(Actions{SSH: SSHActions{BackgroundRefresh: true, Discover: func(context.Context, SSHDiscoveryRequest, func(sshdiscovery.Progress)) (SSHDiscoveryResult, error) {
		calls++
		return SSHDiscoveryResult{}, nil
	}}}, nil, nil)
	if m.scheduleSSHBackground() != nil {
		t.Fatal("background work scheduled outside SSH")
	}
	m.view = ViewSSH
	m.sshUI.dialog = sshDialog{kind: "onboard"}
	if m.scheduleSSHBackground() != nil {
		t.Fatal("background interferes with reviewed form")
	}
	m.sshUI.dialog = sshDialog{}
	m.sshUI.lastBackground = time.Now()
	next, cmd := m.applySSHBackground(sshBackgroundMsg{at: time.Now()})
	m = next.(Model)
	if cmd != nil || calls != 0 {
		t.Fatal("retry was not throttled")
	}
	m.setSSHBackgroundRefresh(false)
	if m.scheduleSSHBackground() != nil {
		t.Fatal("disabled preference ignored")
	}
}
func TestSSHNetworkObservationDoesNotClaimAuthentication(t *testing.T) {
	inv := sshTestInventory("box")
	p := inv.Machines[0].Profiles[0]
	m := New(Actions{}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	m.sshUI.activity = map[string]sshactivity.ProfileRecord{p.ID: {LastTest: &sshactivity.TestRecord{Fingerprint: p.Fingerprint, Mode: "network", Status: "network_ready", Stages: []sshhost.DiagnosticStage{{Name: "tcp", State: "ready"}}}}}
	if m.sshProfileCheck(p) != "last net" {
		t.Fatal("network observation mislabeled as SSH authentication")
	}
	p.Fingerprint = "changed"
	if m.sshProfileCheck(p) != "stale" {
		t.Fatal("changed profile retained fresh check")
	}
}

func TestSSHPartialRefreshKeepsIndependentProfilesAndBlocksOnlyMappingActions(t *testing.T) {
	inv := sshTestInventory("readable alias")
	inv.Sources["registry"] = "unavailable"
	inv.Machines[0].State = "mapping_unknown"
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	generation := m.beginViewLoad(ViewSSH, loadVisit)
	next, _ := m.applySSHLoad(sshLoadedMsg{generation: generation, inventory: inv, err: errors.New("registry unavailable")})
	m = next.(Model)
	if len(m.visibleSSHEntries()) != 1 || !m.viewLoad(ViewSSH).hasSnapshot || m.viewErrors[ViewSSH] == nil {
		t.Fatal("independent profile discarded or source error lost")
	}
	next, cmd := m.runSSHAction(listActionSSHMappings)
	m = next.(Model)
	if cmd != nil || !strings.Contains(m.status, "unavailable") {
		t.Fatal("registry action did not fail closed")
	}
}
func TestSSHPartialDiscoveryKeepsPriorCandidateObservationTime(t *testing.T) {
	old := testDiscoveryCandidate()
	fresh := old
	fresh.ID = "new-candidate"
	fresh.NativeID = "192.0.2.9:22"
	fresh.Addresses = []string{"192.0.2.9"}
	before := time.Now().Add(-time.Hour)
	now := time.Now()
	m := New(Actions{}, nil, nil)
	m.retainSSHReport(sshdiscovery.Report{Source: "lan", Scope: old.Scope, ObservedAt: before, Complete: true, Candidates: []sshdiscovery.Candidate{old}})
	m.retainSSHReport(sshdiscovery.Report{Source: "lan", Scope: old.Scope, ObservedAt: now, Complete: false, Candidates: []sshdiscovery.Candidate{fresh}})
	if len(m.sshUI.reports) != 2 || !m.sshUI.reports[0].ObservedAt.Equal(before) || len(m.sshUI.reports[0].Candidates) != 1 {
		t.Fatal("partial scan renewed an unobserved older candidate")
	}
}
func TestSSHBackgroundScheduleIsSingleAndSupersededByNavigation(t *testing.T) {
	m := New(Actions{SSH: SSHActions{BackgroundRefresh: true, Discover: func(context.Context, SSHDiscoveryRequest, func(sshdiscovery.Progress)) (SSHDiscoveryResult, error) {
		t.Fatal("stale timer started discovery")
		return SSHDiscoveryResult{}, nil
	}}}, nil, nil)
	m.view = ViewSSH
	cmd := m.scheduleSSHBackground()
	if cmd == nil || m.scheduleSSHBackground() != nil {
		t.Fatal("background timer not unique")
	}
	generation := m.sshUI.backgroundGeneration
	m.view = ViewRepos
	m.leaveSSHView()
	m.view = ViewSSH
	_, cmd = m.applySSHBackground(sshBackgroundMsg{at: time.Now(), generation: generation})
	if cmd != nil {
		t.Fatal("navigation did not invalidate previous background timer")
	}
}
func TestSSHReviewStartsAtBeginningAndAllowsScrolling(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.width = 45
	m.height = 14
	m.sshUI.dialog = sshDialog{kind: "test-review", title: "Review", body: "first target\nsecond\nthird\nfourth\nfifth\nsixth\nlast target"}
	if view := m.renderSSHDialog(); !strings.Contains(view, "first target") || strings.Contains(view, "last target") {
		t.Fatal("review initially hid first target")
	}
	next, _ := m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(Model)
	if !strings.Contains(m.renderSSHDialog(), "last target") {
		t.Fatal("review cannot expose all targets")
	}
}

func TestSSHConfigOnlyFormCannotRequestKeyGeneration(t *testing.T) {
	called := false
	m := New(Actions{SSH: SSHActions{PrepareOnboarding: func(context.Context, sshflow.OnboardRequest) (SSHOnboardingPlan, error) {
		called = true
		return testSSHOnboardPlan{}, nil
	}}}, nil, nil)
	next, _ := m.openSSHOnboardingForm(sshflow.OnboardRequest{Alias: "box", HostName: "192.0.2.1", User: "user", Port: 22, Auth: "config"})
	m = next.(Model)
	m.sshUI.dialog.onboarding.GenerateKey, m.sshUI.dialog.onboarding.KeyPath = true, "/home/user/.ssh/new-key"
	next, cmd := m.prepareSSHOnboarding()
	m = next.(Model)
	if cmd != nil || called || m.sshUI.dialog.err == nil {
		t.Fatal("configuration-only form authorized key generation")
	}
}
func TestSSHNativeDialogMouseCannotActivateDashboard(t *testing.T) {
	m := New(Actions{}, nil, nil).WithSSH(sshTestInventory("box"))
	m.view = ViewSSH
	m.width = 120
	m.height = 30
	m.sshUI.dialog = sshDialog{kind: "review", body: "review"}
	next, cmd := m.Update(mouseMessage(1, 0, tea.MouseButtonLeft, tea.MouseActionPress))
	m = next.(Model)
	if cmd != nil || m.view != ViewSSH || m.sshUI.dialog.kind != "review" {
		t.Fatal("review click reached underlying dashboard")
	}
}
func TestSSHLANValidationFailurePreservesEditableScope(t *testing.T) {
	m := New(Actions{SSH: SSHActions{ValidateLAN: func(context.Context, sshdiscovery.LANRequest) (string, error) {
		return "", errors.New("range no longer on-link")
	}, Discover: func(context.Context, SSHDiscoveryRequest, func(sshdiscovery.Progress)) (SSHDiscoveryResult, error) {
		t.Fatal("invalid range scanned")
		return SSHDiscoveryResult{}, nil
	}}}, nil, nil)
	m.view = ViewSSH
	request := SSHDiscoveryRequest{Source: "lan", LAN: sshdiscovery.LANRequest{Interface: "en0", Ranges: []string{"192.0.2.0/24"}, Ports: []int{22}}}
	next, cmd := m.startSSHDiscovery(request, false)
	m = sshDrainEvents(t, next.(Model), cmd)
	if m.sshUI.dialog.kind != "lan" || m.sshUI.dialog.value("ranges") != "192.0.2.0/24" || m.sshUI.dialog.err == nil || m.sshUI.onlyDiscovery {
		t.Fatal("invalid scope did not return to its editable form")
	}
}

func TestSSHOldChecksRemainHistoricalRatherThanCurrentHealth(t *testing.T) {
	inv := sshTestInventory("box")
	profile := inv.Machines[0].Profiles[0]
	m := New(Actions{}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	m.width = 200
	m.sshUI.activity = map[string]sshactivity.ProfileRecord{profile.ID: {LastTest: &sshactivity.TestRecord{Fingerprint: profile.Fingerprint, Mode: "full", Status: "ready", ObservedAt: time.Now().Add(-365 * 24 * time.Hour)}}}
	if m.sshProfileCheck(profile) != "last SSH" {
		t.Fatal("historical test became current health")
	}
	m.selectSSHKey("profile:" + profile.ID)
	if !strings.Contains(m.renderSSHDetail(), "current route not revalidated") {
		t.Fatal("historical test omits current-route uncertainty")
	}
}

func TestSSHTestSaveFailureRetainsUseOrderAndNewerHistory(t *testing.T) {
	inv := sshTestInventory("box")
	profile := inv.Machines[0].Profiles[0]
	now := time.Now()
	before := now.Add(-time.Hour)
	previous := sshactivity.ProfileRecord{ProfileID: profile.ID, Fingerprint: profile.Fingerprint, LastUsed: before, LastSuccess: before, LastSuccessFingerprint: profile.Fingerprint, LastTest: &sshactivity.TestRecord{ObservedAt: before, Fingerprint: profile.Fingerprint, Mode: "full", Status: "ready"}}
	m := New(Actions{}, nil, nil).WithSSH(inv)
	m.view = ViewSSH
	m.sshUI.generation = 1
	m.sshUI.activity = map[string]sshactivity.ProfileRecord{profile.ID: previous}
	next, _ := m.applySSHEvent(sshEventMsg{generation: 1, kind: "test-progress", test: SSHTestProgress{ProfileID: profile.ID, Record: sshactivity.ProfileRecord{ProfileID: profile.ID, LastTest: &sshactivity.TestRecord{ObservedAt: now, Fingerprint: profile.Fingerprint, Mode: "network", Status: "network_ready"}}, Err: errors.New("activity persistence unavailable")}})
	m = next.(Model)
	record := m.sshUI.activity[profile.ID]
	if !record.LastUsed.Equal(before) || record.Fingerprint != profile.Fingerprint || !record.LastSuccess.Equal(before) || record.LastSuccessFingerprint != profile.Fingerprint || !record.LastTest.ObservedAt.Equal(now) {
		t.Fatal("test persistence failure erased usage or historical success")
	}
	generation := m.beginViewLoad(ViewSSH, loadAction)
	next, _ = m.applySSHLoad(sshLoadedMsg{generation: generation, inventory: inv, activity: map[string]sshactivity.ProfileRecord{profile.ID: previous}})
	m = next.(Model)
	if !m.sshUI.activity[profile.ID].LastTest.ObservedAt.Equal(now) {
		t.Fatal("cache reload discarded newer session-only test result")
	}
	older := mergeSSHTestActivity(record, previous)
	if !older.LastTest.ObservedAt.Equal(now) {
		t.Fatal("out-of-order result replaced newer observation")
	}
}
func TestSSHCachedCandidateUsesExactOriginalReportDate(t *testing.T) {
	c := testDiscoveryCandidate()
	other := c
	other.ID = "other"
	other.NativeID = "192.0.2.9:22"
	other.Addresses = []string{"192.0.2.9"}
	observed := time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC)
	report := sshdiscovery.Report{Source: c.Source, Scope: c.Scope, ObservedAt: observed, Status: "ready", Stale: true, Complete: true, Candidates: []sshdiscovery.Candidate{c, other}}
	inv := SSHInventory{Complete: true, Machines: []SSHRow{{ID: "cached-machine", Label: c.Name, LAN: []sshdiscovery.Candidate{c}}}}
	calls := 0
	m := New(Actions{SSH: SSHActions{Load: func(context.Context) (SSHInventory, error) { return inv, nil }, LoadReports: func(context.Context) ([]sshdiscovery.Report, error) {
		calls++
		return []sshdiscovery.Report{report}, nil
	}}}, nil, nil)
	m.view = ViewSSH
	m.width = 160
	m.height = 30
	m.beginViewLoad(ViewSSH, loadVisit)
	m = sshDrainEvents(t, m, m.reloadSSH())
	if calls != 1 || len(m.sshUI.reports) != 0 || len(m.sshUI.cachedReports) != 1 {
		t.Fatal("cache reports were not read-only separate metadata")
	}
	if !strings.Contains(m.renderSSHDetail(), "2025-01-02 03:04 UTC") {
		t.Fatal("cached observation date missing from source detail")
	}
	next, _ := m.openSSHOnboarding(inv.Machines[0])
	m = next.(Model)
	selected := m.sshUI.dialog.onboarding.Report
	if selected == nil || !selected.ObservedAt.Equal(observed) || len(selected.Candidates) != 1 || selected.Candidates[0].ID != c.ID {
		t.Fatal("setup lost original cached subset provenance")
	}
	changed := c
	changed.Name = "different observed bytes"
	if m.sshCandidateReport(changed) != nil {
		t.Fatal("endpoint key attached mismatched cached provenance")
	}
}

func TestSSHCompletedOutcomesSurviveFullProgressBufferAndCancellation(t *testing.T) {
	events := make(chan sshEventMsg, 32)
	outcomes := make([]SSHTestProgress, 33)
	for i := range outcomes {
		id := strconv.Itoa(i)
		outcomes[i] = SSHTestProgress{ProfileID: id, Record: sshactivity.ProfileRecord{ProfileID: id, Fingerprint: "source", LastTest: &sshactivity.TestRecord{Fingerprint: "source", ObservedAt: time.Now(), Mode: "network", Status: "network_ready"}}}
		if i == 0 {
			outcomes[i].Err = errors.New("test result could not be persisted")
		}
		if i < cap(events) {
			events <- sshEventMsg{generation: 1, kind: "test-progress", test: outcomes[i]}
		}
	}
	finishSSHEvent(events, sshEventMsg{generation: 1, kind: "tested", testResult: SSHTestResult{Completed: 33, Total: 40, Canceled: true, Outcomes: outcomes}})
	close(events)
	m := New(Actions{}, nil, nil)
	m.view = ViewSSH
	m.sshUI.generation = 1
	m.sshUI.events = events
	m.sshUI.running = true
	m.sshUI.dialog = sshDialog{kind: "testing"}
	m = sshDrainEvents(t, m, waitSSHEvent(events))
	if len(m.sshUI.activity) != 33 || m.sshUI.dialog.completed != 33 || !strings.Contains(m.sshUI.dialog.body, "test result could not be persisted") || !strings.Contains(m.sshUI.dialog.body, "Canceled") {
		t.Fatal("completed result or error was lost with the dropped progress event")
	}
}
