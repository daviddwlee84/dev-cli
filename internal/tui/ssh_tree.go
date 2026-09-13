package tui

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshactivity"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

type sshUIState struct {
	expanded             map[string]bool
	activity             map[string]sshactivity.ProfileRecord
	reports              []sshdiscovery.Report
	cachedReports        []sshdiscovery.Report
	dialog               sshDialog
	generation           uint64
	events               <-chan sshEventMsg
	cancel               context.CancelFunc
	running              bool
	background           bool
	lastBackground       time.Time
	backgroundScheduled  bool
	backgroundGeneration uint64
	onlyDiscovery        bool
	discoveryKeys        map[string]bool
	savedFilter          string
	savedSelection       selectionToken
	selectAlias          string
}

type sshEntry struct {
	machine  SSHRow
	profile  *sshflow.ConnectionProfile
	expanded bool
}

func (e sshEntry) key() string {
	if e.profile != nil {
		return "profile:" + e.profile.ID
	}
	return e.machine.ID
}
func (m Model) currentSSHEntry() (sshEntry, bool) {
	if m.view != ViewSSH {
		return sshEntry{}, false
	}
	rows := m.visibleSSHEntries()
	if m.sshCursor < 0 || m.sshCursor >= len(rows) {
		return sshEntry{}, false
	}
	return rows[m.sshCursor], true
}
func (m Model) sshLastUsed(row SSHRow) time.Time {
	var last time.Time
	for _, p := range row.Profiles {
		if value := m.sshProfileLastUsed(p); value.After(last) {
			last = value
		}
	}
	return last
}
func (m Model) sshProfiles(row SSHRow) []sshflow.ConnectionProfile {
	profiles := slices.Clone(row.Profiles)
	slices.SortStableFunc(profiles, func(a, b sshflow.ConnectionProfile) int {
		ta, tb := m.sshProfileLastUsed(a), m.sshProfileLastUsed(b)
		if !ta.Equal(tb) {
			if ta.After(tb) {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Alias), strings.ToLower(b.Alias))
	})
	return profiles
}
func (m Model) visibleSSHEntries() []sshEntry {
	machines := slices.Clone(m.ssh.Machines)
	slices.SortStableFunc(machines, func(a, b SSHRow) int {
		if (len(a.Profiles) > 0) != (len(b.Profiles) > 0) {
			if len(a.Profiles) > 0 {
				return -1
			}
			return 1
		}
		ta, tb := m.sshLastUsed(a), m.sshLastUsed(b)
		if !ta.Equal(tb) {
			if ta.After(tb) {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	machines = applyColumnSort(m, machines, func(row SSHRow, col string) sortCell {
		if col == "used" {
			return timeCell(m.sshLastUsed(row))
		}
		if col == "connection" {
			return textCell(row.Label)
		}
		if col == "endpoint" {
			if ps := m.sshProfiles(row); len(ps) > 0 {
				return textCell(ps[0].HostName)
			}
		}
		return textCell(sshCell(row, col))
	})
	var entries []sshEntry
	for _, row := range machines {
		if m.sshUI.onlyDiscovery && !m.sshDiscoveredRow(row) {
			continue
		}
		matchesParent := matches(sshRowSummary(row), m.filter)
		if !matchesParent {
			continue
		}
		expanded := m.sshUI.expanded[row.ID] || strings.TrimSpace(m.filter) != ""
		entries = append(entries, sshEntry{machine: row, expanded: expanded})
		if !expanded {
			continue
		}
		for _, profile := range m.sshProfiles(row) {
			if strings.TrimSpace(m.filter) != "" && !matches(profile.Alias+" "+profile.HostName+" "+row.Label, m.filter) {
				continue
			}
			p := profile
			entries = append(entries, sshEntry{machine: row, profile: &p})
		}
	}
	return entries
}
func (m *Model) toggleSSHEntry() {
	entry, ok := m.currentSSHEntry()
	if !ok || len(entry.machine.Profiles) == 0 {
		return
	}
	expanded := make(map[string]bool, len(m.sshUI.expanded)+1)
	for k, v := range m.sshUI.expanded {
		expanded[k] = v
	}
	expanded[entry.machine.ID] = !entry.expanded && entry.profile == nil
	m.sshUI.expanded = expanded
	m.selectToken(selectionToken{view: ViewSSH, key: entry.machine.ID})
}
func (m Model) sshEntryCell(entry sshEntry, column string) string {
	profile := entry.profile
	if profile == nil {
		ps := m.sshProfiles(entry.machine)
		if len(ps) > 0 {
			profile = &ps[0]
		}
	}
	switch column {
	case "connection":
		if entry.profile != nil {
			return "  └ " + entry.profile.Alias
		}
		marker := "  "
		if len(entry.machine.Profiles) > 0 {
			marker = "▸ "
			if entry.expanded {
				marker = "▾ "
			}
		}
		return marker + entry.machine.Label
	case "endpoint":
		if profile != nil {
			if profile.HostName == "" {
				return "native config"
			}
			if profile.Port > 0 && profile.Port != 22 {
				return fmt.Sprintf("%s:%d", profile.HostName, profile.Port)
			}
			return profile.HostName
		}
		candidates := append(slices.Clone(entry.machine.LAN), entry.machine.Tailscale...)
		if len(candidates) > 0 && len(candidates[0].Addresses) > 0 {
			return fmt.Sprintf("%s:%d", candidates[0].Addresses[0], candidates[0].Port)
		}
		return "—"
	case "sources":
		values := []string{"·", "·", "·", "·", "·"}
		for i, present := range []bool{len(entry.machine.Profiles) > 0, len(entry.machine.Tailscale) > 0, len(entry.machine.LAN) > 0, len(entry.machine.Fleet) > 0, len(entry.machine.Herdr) > 0} {
			if present {
				values[i] = []string{"S", "T", "L", "F", "H"}[i]
			}
		}
		return strings.Join(values, " ")
	case "used":
		if entry.profile != nil {
			return sshAge(m.sshProfileLastUsed(*entry.profile))
		}
		return sshAge(m.sshLastUsed(entry.machine))
	case "check":
		if entry.profile != nil {
			return m.sshProfileCheck(*entry.profile)
		}
		if len(entry.machine.Profiles) == 0 {
			return "unverified"
		}
		if len(entry.machine.Profiles) == 1 {
			return m.sshProfileCheck(entry.machine.Profiles[0])
		}
		tested, authenticated, network, _ := m.sshCheckSummary(entry.machine)
		if tested == 0 {
			return "not tested"
		}
		if authenticated == 0 && network == 0 {
			return fmt.Sprintf("last 0/%d", tested)
		}
		return fmt.Sprintf("last %dS/%dN", authenticated, network)

	}
	return "—"
}
func (m Model) sshProfileCheck(profile sshflow.ConnectionProfile) string {
	record := m.sshUI.activity[profile.ID]
	if record.LastTest == nil {
		return "not tested"
	}
	if record.LastTest.Fingerprint != profile.Fingerprint {
		return "stale"
	}
	if record.LastTest.Status == "network_ready" {
		return "last net"
	}
	if record.LastTest.Status == "ready" {
		return "last SSH"
	}
	return "last " + record.LastTest.Status
}
func sshAge(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	elapsed := time.Since(t)
	if elapsed < time.Minute {
		return "just now"
	}
	if elapsed < time.Hour {
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	}
	if elapsed < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
}
func candidateKey(c sshdiscovery.Candidate) string {
	native := c.NativeID
	if native == "" {
		native = c.ID
	}
	return c.Source + "\x00" + c.Scope + "\x00" + native
}

func (m Model) sshDiscoveredRow(row SSHRow) bool {
	for _, c := range append(slices.Clone(row.LAN), row.Tailscale...) {
		if m.sshUI.discoveryKeys[candidateKey(c)] {
			return true
		}
	}
	return false
}
func (m *Model) leaveSSHDiscovery() {
	if !m.sshUI.onlyDiscovery {
		return
	}
	m.sshUI.onlyDiscovery = false
	m.filter = m.sshUI.savedFilter
	currentView := m.view
	m.view = ViewSSH
	m.selectToken(m.sshUI.savedSelection)
	m.setAt(m.at())
	m.view = currentView
}
func (m *Model) leaveSSHView() {
	m.leaveSSHDiscovery()
	if m.sshUI.background && m.sshUI.cancel != nil {
		m.sshUI.cancel()
		m.sshUI.generation++
		m.sshUI.running = false
		m.sshUI.background = false
	}
	m.sshUI.backgroundScheduled = false
	m.sshUI.backgroundGeneration++
}

// Directly retain result observations even when the cache or registry could not
// be read. This projection never establishes a machine binding.
func (m Model) withSessionDiscovery(inv SSHInventory) SSHInventory {
	latest := map[string]sshdiscovery.Candidate{}
	for _, report := range m.sshUI.reports {
		for _, candidate := range report.Candidates {
			latest[candidateKey(candidate)] = candidate
		}
	}
	inv.Machines = slices.Clone(inv.Machines)
	existing := map[string]bool{}
	for i, row := range inv.Machines {
		row.LAN = slices.Clone(row.LAN)
		row.Tailscale = slices.Clone(row.Tailscale)
		for j, c := range row.LAN {
			key := candidateKey(c)
			existing[key] = true
			if fresh, ok := latest[key]; ok {
				row.LAN[j] = fresh
			}
		}
		for j, c := range row.Tailscale {
			key := candidateKey(c)
			existing[key] = true
			if fresh, ok := latest[key]; ok {
				row.Tailscale[j] = fresh
			}
		}
		inv.Machines[i] = row
	}
	for _, report := range m.sshUI.reports {
		for _, c := range report.Candidates {
			key := candidateKey(c)
			if existing[key] {
				continue
			}
			existing[key] = true
			row := SSHRow{ID: sshflow.ReferenceID("observation", c.Source, key), Label: c.Name, State: "candidate"}
			if row.Label == "" && len(c.Addresses) > 0 {
				row.Label = c.Addresses[0]
			}
			if row.Label == "" {
				row.Label = c.ID
			}
			if c.Source == sshdiscovery.SourceLAN {
				row.LAN = []sshdiscovery.Candidate{c}
			} else {
				row.Tailscale = []sshdiscovery.Candidate{c}
			}
			inv.Machines = append(inv.Machines, row)
		}
	}
	return inv
}

func (m Model) sshProfileLastUsed(profile sshflow.ConnectionProfile) time.Time {
	record := m.sshUI.activity[profile.ID]
	if record.Fingerprint != profile.Fingerprint {
		return time.Time{}
	}
	return record.LastUsed
}
func (m Model) WithSSHBackgroundRefresh(enabled bool) Model {
	m.setSSHBackgroundRefresh(enabled)
	return m
}
func (m *Model) setSSHBackgroundRefresh(enabled bool) {
	m.actions.SSH.BackgroundRefresh = enabled
	if !enabled {
		m.pauseSSHBackground()
		m.sshUI.backgroundScheduled = false
		m.sshUI.backgroundGeneration++
	}
}

func (m *Model) selectSSHKey(key string) bool {
	for i, row := range m.visibleSSHEntries() {
		if row.key() == key {
			m.setAt(i)
			return true
		}
	}
	if strings.HasPrefix(key, "profile:") {
		id := strings.TrimPrefix(key, "profile:")
		for _, row := range m.ssh.Machines {
			for _, profile := range row.Profiles {
				if profile.ID == id {
					expanded := make(map[string]bool, len(m.sshUI.expanded)+1)
					for k, v := range m.sshUI.expanded {
						expanded[k] = v
					}
					expanded[row.ID] = true
					m.sshUI.expanded = expanded
					for i, entry := range m.visibleSSHEntries() {
						if entry.key() == key {
							m.setAt(i)
							return true
						}
					}
					return false
				}
			}
		}
	}
	return false
}

func (m Model) sshCheckSummary(row SSHRow) (tested, authenticated, network int, last time.Time) {
	for _, profile := range row.Profiles {
		record := m.sshUI.activity[profile.ID]
		if test := record.LastTest; test != nil && test.Fingerprint == profile.Fingerprint {
			tested++
			if test.ObservedAt.After(last) {
				last = test.ObservedAt
			}
			if test.Status == "ready" && test.Mode == "full" {
				authenticated++
			}
			if test.Status == "network_ready" {
				network++
			}
		}
	}
	return
}

func (m Model) sshProfileName(id string) string {
	for _, row := range m.ssh.Machines {
		for _, profile := range row.Profiles {
			if profile.ID == id {
				return profile.Alias
			}
		}
	}
	return id
}

// A report subset carries one exact observation and its original timestamp.
// Matching only the endpoint key could attach old provenance to changed bytes.
func (m Model) sshCandidateReport(candidate sshdiscovery.Candidate) *sshdiscovery.Report {
	for _, reports := range [][]sshdiscovery.Report{m.sshUI.reports, m.sshUI.cachedReports} {
		for i := len(reports) - 1; i >= 0; i-- {
			report := reports[i]
			if report.Source != candidate.Source || report.Scope != candidate.Scope {
				continue
			}
			for _, observed := range report.Candidates {
				if reflect.DeepEqual(observed, candidate) {
					report.Candidates = []sshdiscovery.Candidate{observed}
					return &report
				}
			}
		}
	}
	return nil
}
func (m Model) sshDiscoveryDates(row SSHRow) []string {
	var lines []string
	seen := map[string]bool{}
	for _, candidate := range append(slices.Clone(row.LAN), row.Tailscale...) {
		report := m.sshCandidateReport(candidate)
		if report == nil {
			continue
		}
		state := report.Status
		if report.Stale || !report.Complete {
			state += "; stale/incomplete"
		}
		key := report.Source + report.Scope + report.ObservedAt.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		lines = append(lines, fmt.Sprintf("  %s observed %s (%s; %s)", report.Source, report.ObservedAt.Format("2006-01-02 15:04 MST"), sshAge(report.ObservedAt), state))
	}
	return lines
}
func mergeSSHTestActivity(previous, next sshactivity.ProfileRecord) sshactivity.ProfileRecord {
	if next.ProfileID == "" {
		next.ProfileID = previous.ProfileID
	}
	if next.SchemaVersion == 0 {
		next.SchemaVersion = previous.SchemaVersion
	}
	// Testing cannot remove or fabricate a use observation; retain whichever
	// complete source-bound usage record is newer.
	if next.LastUsed.IsZero() || next.Fingerprint == "" || previous.LastUsed.After(next.LastUsed) {
		next.LastUsed = previous.LastUsed
		if !previous.LastUsed.IsZero() || next.Fingerprint == "" {
			next.Fingerprint = previous.Fingerprint
		}
	}
	if next.Fingerprint == "" {
		next.Fingerprint = previous.Fingerprint
	}
	if previous.LastTest != nil && (next.LastTest == nil || previous.LastTest.ObservedAt.After(next.LastTest.ObservedAt)) {
		next.LastTest = previous.LastTest
	}
	if next.LastSuccessFingerprint == "" || previous.LastSuccess.After(next.LastSuccess) {
		next.LastSuccess = previous.LastSuccess
		next.LastSuccessFingerprint = previous.LastSuccessFingerprint
	}
	return next
}
