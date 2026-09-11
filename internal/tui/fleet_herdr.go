package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// FleetHerdrCatalog is an observation of this client's saved machines. It is
// independent of SSH connectivity, remote repository snapshots and their cache.
type FleetHerdrCatalog struct {
	Status, Detail  string
	ObservedAt      time.Time
	Profiles        []FleetHerdrProfile
	RemoteAvailable bool
	InHerdr         bool
}

type FleetHerdrProfile struct {
	ID, Label, Target, Session string
	Enabled                    bool
}

type fleetHerdrState struct {
	catalog, latest           FleetHerdrCatalog
	loading, stale, requested bool
	generation                uint64
	hostGeneration            uint64
	cancel                    context.CancelFunc
}

type fleetHerdrMsg struct {
	generation, hostGeneration uint64
	catalog                    FleetHerdrCatalog
	err                        error
}

func (m *Model) requestFleetHerdr(force bool) tea.Cmd {
	if m.actions.LoadFleetHerdr == nil {
		return m.loadFleetMenuActions()
	}
	focusModel := *m
	focusModel.view = ViewFleet
	focus := focusModel.currentToken()
	h := &m.fleetTree.herdr
	h.requested = true
	if h.loading && !force {
		return nil
	}
	if h.cancel != nil {
		h.cancel()
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	h.cancel, h.loading = cancel, true
	h.generation++
	generation, hostGeneration := h.generation, m.fleetTree.generation
	h.hostGeneration = hostGeneration
	m.restoreFleetFocus(focus)
	load := m.actions.LoadFleetHerdr
	return func() tea.Msg {
		catalog, err := load(ctx)
		return fleetHerdrMsg{generation, hostGeneration, catalog, err}
	}
}

func (m *Model) invalidateFleetHerdr() {
	h := &m.fleetTree.herdr
	if h.cancel != nil {
		h.cancel()
	}
	h.cancel, h.loading = nil, false
	h.generation++
	h.stale = h.catalog.Status == "ready"
}

func (m Model) acceptFleetHerdr(msg fleetHerdrMsg) (Model, tea.Cmd) {
	h := &m.fleetTree.herdr
	if msg.generation != h.generation || msg.hostGeneration != m.fleetTree.generation {
		return m, nil
	}
	if h.cancel != nil {
		h.cancel()
	}
	h.cancel, h.loading = nil, false
	catalog := msg.catalog
	if msg.err != nil {
		catalog.Detail = msg.err.Error()
		if catalog.Status == "" || catalog.Status == "ready" {
			catalog.Status = "unavailable"
		}
	}
	if catalog.Status == "" {
		catalog.Status = "unavailable"
	}
	catalog.Profiles = append([]FleetHerdrProfile(nil), catalog.Profiles...)
	h.latest = catalog
	if catalog.Status == "ready" || catalog.Status == "disabled" || h.catalog.Status != "ready" {
		h.catalog = catalog
		h.stale = false
	} else {
		h.stale = true
	}
	return m, m.loadFleetMenuActions()
}

func (m Model) fleetHerdrProfiles(d FleetHostDescriptor) []FleetHerdrProfile {
	if d.Local || d.SSHAlias == "" {
		return nil
	}
	var profiles []FleetHerdrProfile
	for _, p := range m.fleetTree.herdr.catalog.Profiles {
		if p.Target == d.SSHAlias {
			profiles = append(profiles, p)
		}
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		if profiles[i].Label != profiles[j].Label {
			return profiles[i].Label < profiles[j].Label
		}
		return profiles[i].ID < profiles[j].ID
	})
	return profiles
}

func (m Model) fleetHerdrSummary(d FleetHostDescriptor) (label, detail, search string) {
	if d.Local {
		return "—", "This host is not a remote saved-machine registration.", ""
	}
	h := m.fleetTree.herdr
	if h.latest.Status == "disabled" {
		return "not checked", h.latest.Detail, "not checked"
	}
	if h.catalog.Status != "ready" {
		if h.loading {
			return "loading", "Reading this client's Herdr machine catalog.", "loading"
		}
		return "unknown", h.latest.Detail, "unknown"
	}
	if d.SSHAlias == "" {
		return "unmapped", "An exact SSH alias is required to associate a saved machine.", "unmapped"
	}
	profiles := m.fleetHerdrProfiles(d)
	enabled := 0
	parts := []string{}
	for _, p := range profiles {
		state := "disabled"
		if p.Enabled {
			enabled++
			state = "enabled"
		}
		parts = append(parts, fmt.Sprintf("%s · session %s · %s · %s", p.Label, p.Session, state, p.ID))
	}
	switch {
	case len(profiles) == 0:
		label = "not added"
		detail = "No saved Herdr profile matches SSH alias " + d.SSHAlias
	case len(profiles) == 1 && enabled == 1:
		label = "enabled"
	case len(profiles) == 1:
		label = "disabled"
	default:
		label = fmt.Sprintf("%d/%d enabled", enabled, len(profiles))
	}
	if len(parts) > 0 {
		detail = strings.Join(parts[:min(3, len(parts))], "\n")
		if len(parts) > 3 {
			detail += fmt.Sprintf("\n%d more profiles; use the Herdr profile menu.", len(parts)-3)
		}
	}
	search = label + " " + strings.Join(parts, " ")
	detail = "Local Herdr catalog · SSH alias " + d.SSHAlias + "\n" + detail
	if d.ConnectionNote != "" {
		detail += "\nConnection: " + d.ConnectionNote
	}
	if h.stale {
		label = "~" + label
		detail += "\nStale catalog: " + h.latest.Detail
		search += " stale"
	} else if h.loading {
		label = "~" + label
		detail += "\nRefreshing local catalog…"
	}
	if !h.catalog.ObservedAt.IsZero() {
		detail += "\nObserved " + h.catalog.ObservedAt.Local().Format("2006-01-02 15:04:05")
	}
	return label, detail, search
}

func (m *Model) loadFleetMenuActions() tea.Cmd {
	if !m.fleetTree.menuWaiting || m.overlay.kind != overlayActionMenu || m.actions.ListFleetHostActions == nil {
		return nil
	}
	d := m.overlay.fleetHost
	i := m.fleetHostIndex(d.Key)
	if i < 0 || m.fleetTree.hosts[i].descriptor.EndpointID != d.EndpointID {
		m.fleetTree.menuWaiting = false
		return nil
	}
	m.fleetTree.menuWaiting = false
	generation := m.fleetTree.menuGeneration
	herdrGeneration := m.fleetTree.herdr.generation
	catalog := m.fleetTree.herdr.latest
	load := m.actions.ListFleetHostActions
	return func() tea.Msg {
		actions, err := load(m.baseContext(), d, catalog)
		return fleetMenuMsg{key: d.Key, endpoint: d.EndpointID, generation: generation, herdrGeneration: herdrGeneration, actions: actions, err: err}
	}
}

func (m *Model) addFleetMenuActions(actions []FleetHostAction) {
	m.fleetTree.menuActions = append([]FleetHostAction(nil), actions...)
	profiles := map[string]bool{}
	for _, a := range actions {
		if a.ProfileID != "" {
			profiles[a.ProfileID] = true
		}
	}
	for _, a := range actions {
		if len(profiles) > 1 && a.ProfileID != "" {
			continue
		}
		m.addFleetActionOption(a)
	}
	if len(profiles) > 1 {
		m.overlay.addOption(listActionFleetProfiles, "Herdr profiles…")
	}
}

func (m *Model) addFleetActionOption(a FleetHostAction) {
	if a.Disabled {
		m.overlay.body += a.Label + ": " + a.Description + "\n"
		return
	}
	if m.overlay.optionCount >= len(m.overlay.options) {
		return
	}
	m.overlay.addOption(listActionFleetHost, a.Label)
	m.overlay.options[m.overlay.optionCount-1].fleetID = a.ID
}

func (m Model) openFleetProfiles() (tea.Model, tea.Cmd) {
	host := m.overlay.fleetHost
	if host.Key == "" {
		if row, ok := m.currentFleet(); ok {
			if i := m.fleetHostIndex(row.HostKey); i >= 0 {
				host = m.fleetTree.hosts[i].descriptor
			}
		}
	}
	menu := overlayState{kind: overlayActionMenu, title: "Herdr profiles", subject: host.Name, fleetHost: host, selection: m.currentToken()}
	seen := map[string]bool{}
	for _, a := range m.fleetTree.menuActions {
		if a.ProfileID == "" || seen[a.ProfileID] {
			continue
		}
		seen[a.ProfileID] = true
		if menu.optionCount >= len(menu.options) {
			break
		}
		state := "unknown"
		for _, p := range m.fleetHerdrProfiles(host) {
			if p.ID == a.ProfileID {
				state = "disabled"
				if p.Enabled {
					state = "enabled"
				}
				break
			}
		}
		menu.addOption(listActionFleetProfile, fmt.Sprintf("%s · %s · %s · %s", a.ProfileLabel, a.ProfileSession, state, shortFleetProfileID(a.ProfileID)))
		menu.options[menu.optionCount-1].fleetProfile = a.ProfileID
	}
	m.overlay = menu
	return m, nil
}

func (m Model) openFleetProfile(host FleetHostDescriptor, id string) (tea.Model, tea.Cmd) {
	i := m.fleetHostIndex(host.Key)
	if i < 0 || m.fleetTree.hosts[i].descriptor.EndpointID != host.EndpointID {
		m.err = fmt.Errorf("host configuration changed; reopen actions")
		m.overlay = overlayState{}
		return m, nil
	}
	m.overlay = overlayState{kind: overlayActionMenu, title: "Herdr profile actions", subject: host.Name, fleetHost: host, selection: m.currentToken()}
	for _, a := range m.fleetTree.menuActions {
		if a.ProfileID == id {
			m.addFleetActionOption(a)
			m.overlay.subject = host.Name + " · " + a.ProfileLabel
			m.overlay.detail = "Session " + a.ProfileSession + " · profile " + id
		}
	}
	return m, nil
}

func shortFleetProfileID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…" + id[len(id)-4:]
}

func (m Model) toggleFleetLocal() (tea.Model, tea.Cmd) {
	focus := m.currentToken()
	selectedLocal := false
	if row, ok := m.currentFleet(); ok {
		selectedLocal = row.Local
	}
	m.showLocalFleet = !m.showLocalFleet
	if selectedLocal && !m.showLocalFleet {
		m.fleetCursor = 0
	} else {
		m.restoreFleetFocus(focus)
	}
	m.status = "Local host hidden; a shows it last"
	if m.showLocalFleet {
		m.status = "Local host shown last; a hides it"
	}
	return m, nil
}

func (m Model) fleetLocalToggleLabel() string {
	if m.showLocalFleet {
		return "hide local host (a)"
	}
	return "show local host last (a)"
}
