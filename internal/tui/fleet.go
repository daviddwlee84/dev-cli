package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// FleetHostDescriptor contains local configuration and optional cached facts.
// Cached snapshots are immutable. The backend revalidates EndpointID before IO.
type FleetHostDescriptor struct {
	Key, Name, EndpointID, Target, OS string
	Local                             bool
	Cached                            *fleet.HostResult
	CacheFresh                        bool
}

type FleetHostsResult struct {
	Hosts       []FleetHostDescriptor
	MaxParallel int
}

type FleetHostAction struct {
	ID, Label, Description string
	Disabled               bool
}

type fleetHostState struct {
	descriptor                               FleetHostDescriptor
	result                                   fleet.HostResult
	expanded, loading, background, attempted bool
	acting                                   bool
	cacheLoading, cacheLoaded                bool
	authenticatedCache                       bool
	request                                  uint64
	cancel                                   context.CancelFunc
}

type fleetQueuedRead struct {
	key, endpoint string
	explicit      bool
}

// State is copied before mutation, just like the repository expansion model.
// No worker receives or mutates this slice; results return through messages.
type fleetTreeState struct {
	hosts                                   []fleetHostState
	queue                                   []fleetQueuedRead
	generation, nextRequest, menuGeneration uint64
	background, warmReady                   bool
	maxParallel                             int
	loading                                 bool
	cancel                                  context.CancelFunc
	afterAction                             string
}

type fleetHostsMsg struct {
	generation uint64
	result     FleetHostsResult
	err        error
}
type fleetWarmupMsg struct{}
type fleetHostCacheMsg struct {
	key, endpoint       string
	generation, request uint64
	result              *fleet.HostResult
	fresh               bool
	err                 error
}
type fleetHostMsg struct {
	key, endpoint string
	request       uint64
	result        fleet.HostResult
	err           error
}
type fleetMenuMsg struct {
	key, endpoint string
	generation    uint64
	actions       []FleetHostAction
	err           error
}
type fleetProcessMsg struct {
	host    FleetHostDescriptor
	action  string
	process *exec.Cmd
	err     error
}
type fleetProcessDoneMsg struct {
	host   FleetHostDescriptor
	action string
	err    error
}

func (m Model) hostFleetEnabled() bool { return m.actions.LoadFleetHosts != nil }

func (m Model) WithFleetBackgroundRefresh(enabled bool) Model {
	m.fleetTree.background = enabled
	return m
}

func (m *Model) setFleetBackgroundRefresh(enabled bool) {
	wasEnabled := m.fleetTree.background
	m.fleetTree.background = enabled
	if enabled {
		if !wasEnabled {
			m.fleetTree.warmReady = true
		}
		return
	}
	m.copyFleetHosts()
	queue := []fleetQueuedRead{}
	for _, q := range m.fleetTree.queue {
		if q.explicit {
			queue = append(queue, q)
		}
	}
	m.fleetTree.queue = queue
	for i := range m.fleetTree.hosts {
		h := &m.fleetTree.hosts[i]
		if h.loading && h.background {
			if h.cancel != nil {
				h.cancel()
			}
			h.cancel = nil
			h.loading = false
			h.attempted = false
			m.fleetTree.nextRequest++
			h.request = m.fleetTree.nextRequest
		}
	}
}

func (m Model) loadFleetHosts() tea.Cmd {
	generation := m.fleetTree.generation
	ctx := m.baseContext()
	if m.fleetTree.cancel != nil {
		ctx = m.viewContext(ViewFleet)
	}
	return func() tea.Msg {
		result, err := m.actions.LoadFleetHosts(ctx)
		return fleetHostsMsg{generation, result, err}
	}
}

func (m *Model) beginFleetHostsLoad() tea.Cmd {
	if m.fleetTree.cancel != nil {
		m.fleetTree.cancel()
	}
	m.fleetTree.generation++
	m.fleetTree.loading = true
	ctx, cancel := context.WithCancel(m.baseContext())
	m.fleetTree.cancel = cancel
	m.loadContexts[int(ViewFleet)] = ctx
	return m.loadFleetHosts()
}

func (m Model) fleetWarmupAfterFrame() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.baseContext().Done():
			return nil
		case <-m.firstViewReady:
			return fleetWarmupMsg{}
		}
	}
}

func (m *Model) copyFleetHosts() {
	m.fleetTree.hosts = append([]fleetHostState(nil), m.fleetTree.hosts...)
	m.fleetTree.queue = append([]fleetQueuedRead(nil), m.fleetTree.queue...)
}

func (m Model) fleetHostIndex(key string) int {
	for i, h := range m.fleetTree.hosts {
		if h.descriptor.Key == key {
			return i
		}
	}
	return -1
}

func (m *Model) acceptFleetHosts(result FleetHostsResult) {
	old := m.fleetTree.hosts
	next := make([]fleetHostState, 0, len(result.Hosts))
	for _, d := range result.Hosts {
		if d.Key == "" {
			d.Key = d.Name
		}
		h := fleetHostState{descriptor: d, expanded: d.Local}
		for _, previous := range old {
			if previous.descriptor.Key == d.Key && previous.descriptor.EndpointID == d.EndpointID {
				h = previous
				h.descriptor = d
				break
			}
		}
		// A descriptor reload can bring an authenticated refresh's newer cache.
		if d.Cached != nil && (!h.loading && (h.result.Snapshot == nil || newerFleetCache(*d.Cached, h.result))) {
			h.result = *d.Cached
		}
		h.cacheLoading, h.cacheLoaded = false, d.Local || d.Cached != nil || m.actions.LoadFleetHostCache == nil
		next = append(next, h)
	}
	for _, previous := range old {
		retained := false
		for _, h := range next {
			if h.descriptor.Key == previous.descriptor.Key && h.descriptor.EndpointID == previous.descriptor.EndpointID {
				retained = true
				break
			}
		}
		if !retained && previous.cancel != nil {
			previous.cancel()
		}
	}
	m.fleetTree.hosts = next
	m.syncFleetLocal()
	queue := []fleetQueuedRead{}
	for _, q := range m.fleetTree.queue {
		for _, h := range next {
			if h.descriptor.Key == q.key && h.descriptor.EndpointID == q.endpoint {
				queue = append(queue, q)
				break
			}
		}
	}
	m.fleetTree.queue = queue
	if result.MaxParallel > 0 {
		m.fleetTree.maxParallel = result.MaxParallel
	}
	m.seedViewSnapshot(ViewFleet, perftrace.SourceLive, perftrace.FreshnessFresh, true)
	m.viewErrors[int(ViewFleet)] = nil
}

func newerFleetCache(candidate, current fleet.HostResult) bool {
	if candidate.Snapshot == nil {
		return false
	}
	if current.Snapshot == nil {
		return true
	}
	return candidate.Snapshot.GeneratedAt.After(current.Snapshot.GeneratedAt)
}

func (m *Model) enqueueFleetRead(key string, explicit, force bool) {
	i := m.fleetHostIndex(key)
	if i < 0 || m.fleetTree.hosts[i].descriptor.Local || m.fleetTree.hosts[i].acting || m.actions.LoadFleetHost == nil {
		return
	}
	m.copyFleetHosts()
	h := &m.fleetTree.hosts[i]
	if h.loading {
		if !force {
			return
		}
		if h.cancel != nil {
			h.cancel()
		}
		h.loading = false
		h.cancel = nil
		m.fleetTree.nextRequest++
		h.request = m.fleetTree.nextRequest
	}
	for i, queued := range m.fleetTree.queue {
		if queued.key == key {
			if explicit && !queued.explicit {
				m.fleetTree.queue = append(m.fleetTree.queue[:i], m.fleetTree.queue[i+1:]...)
				break
			}
			return
		}
	}
	q := fleetQueuedRead{key, h.descriptor.EndpointID, explicit}
	if explicit {
		m.fleetTree.queue = append([]fleetQueuedRead{q}, m.fleetTree.queue...)
	} else {
		m.fleetTree.queue = append(m.fleetTree.queue, q)
	}
}

func (m *Model) enqueueFleetWarmup() {
	if !m.fleetTree.background || !m.fleetTree.warmReady {
		return
	}
	for _, h := range m.fleetTree.hosts {
		if h.descriptor.Local || !h.cacheLoaded || h.attempted || h.loading || h.descriptor.CacheFresh {
			continue
		}
		m.enqueueFleetRead(h.descriptor.Key, false, false)
	}
}

func (m *Model) pumpFleetCaches() tea.Cmd {
	if m.actions.LoadFleetHostCache == nil {
		return nil
	}
	m.copyFleetHosts()
	active := 0
	for _, h := range m.fleetTree.hosts {
		if h.cacheLoading {
			active++
		}
	}
	var commands []tea.Cmd
	for i := range m.fleetTree.hosts {
		if active >= 4 {
			break
		}
		h := &m.fleetTree.hosts[i]
		if h.descriptor.Local || h.cacheLoaded || h.cacheLoading {
			continue
		}
		h.cacheLoading = true
		d, request, generation := h.descriptor, h.request, m.fleetTree.generation
		load := m.actions.LoadFleetHostCache
		commands = append(commands, func() tea.Msg {
			result, fresh, err := load(m.baseContext(), d)
			return fleetHostCacheMsg{d.Key, d.EndpointID, generation, request, result, fresh, err}
		})
		active++
	}
	return batchCommands(commands...)
}

func (m *Model) pumpFleetReads() tea.Cmd {
	m.copyFleetHosts()
	active, background := 0, false
	for _, h := range m.fleetTree.hosts {
		if h.loading || h.acting {
			active++
			background = background || h.background
		}
	}
	var commands []tea.Cmd
	for active < max(1, m.fleetTree.maxParallel) && len(m.fleetTree.queue) > 0 {
		index := -1
		for i, q := range m.fleetTree.queue {
			if q.explicit || !background {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		q := m.fleetTree.queue[index]
		m.fleetTree.queue = append(m.fleetTree.queue[:index], m.fleetTree.queue[index+1:]...)
		i := m.fleetHostIndex(q.key)
		if i < 0 {
			continue
		}
		h := &m.fleetTree.hosts[i]
		if h.loading || h.acting || h.descriptor.EndpointID != q.endpoint {
			continue
		}
		ctx, cancel := context.WithCancel(m.baseContext())
		m.fleetTree.nextRequest++
		h.request, h.loading, h.background, h.attempted, h.cancel = m.fleetTree.nextRequest, true, !q.explicit, true, cancel
		d, request := h.descriptor, h.request
		load := m.actions.LoadFleetHost
		commands = append(commands, func() tea.Msg {
			result, err := load(ctx, d)
			return fleetHostMsg{d.Key, d.EndpointID, request, result, err}
		})
		active++
		background = background || !q.explicit
	}
	return batchCommands(commands...)
}

func (m Model) updateFleetTree(message tea.Msg) (Model, tea.Cmd, bool) {
	if !m.hostFleetEnabled() {
		return m, nil, false
	}
	switch msg := message.(type) {
	case fleetWarmupMsg:
		if !m.fleetTree.background {
			return m, nil, true
		}
		m.fleetTree.warmReady = true
		m.enqueueFleetWarmup()
		return m, m.pumpFleetReads(), true
	case fleetHostsMsg:
		if msg.generation != m.fleetTree.generation {
			return m, nil, true
		}
		m.fleetTree.loading = false
		if msg.err != nil {
			if len(m.fleetTree.hosts) == 0 && len(msg.result.Hosts) > 0 {
				m.acceptFleetHosts(msg.result)
			}
			m.viewErrors[int(ViewFleet)] = msg.err
			return m, nil, true
		}
		m.acceptFleetHosts(msg.result)
		if key := m.fleetTree.afterAction; key != "" {
			m.enqueueFleetRead(key, true, false)
			m.fleetTree.afterAction = ""
		}
		m.enqueueFleetWarmup()
		return m, batchCommands(m.pumpFleetCaches(), m.pumpFleetReads()), true
	case fleetHostCacheMsg:
		i := m.fleetHostIndex(msg.key)
		if i < 0 || msg.generation != m.fleetTree.generation || m.fleetTree.hosts[i].descriptor.EndpointID != msg.endpoint {
			return m, nil, true
		}
		m.copyFleetHosts()
		h := &m.fleetTree.hosts[i]
		h.cacheLoading, h.cacheLoaded = false, true
		if msg.err == nil && h.request == msg.request {
			h.descriptor.CacheFresh = msg.fresh
			if msg.result != nil && !h.loading && (h.authenticatedCache || h.result.Snapshot == nil || newerFleetCache(*msg.result, h.result)) {
				h.result = *msg.result
			}
		}
		h.authenticatedCache = false
		m.enqueueFleetWarmup()
		return m, batchCommands(m.pumpFleetCaches(), m.pumpFleetReads()), true
	case fleetHostMsg:
		i := m.fleetHostIndex(msg.key)
		if i < 0 {
			return m, nil, true
		}
		h := m.fleetTree.hosts[i]
		if h.descriptor.EndpointID != msg.endpoint || h.request != msg.request || !h.loading {
			return m, nil, true
		}
		m.copyFleetHosts()
		h = m.fleetTree.hosts[i]
		if h.cancel != nil {
			h.cancel()
		}
		h.cancel, h.loading = nil, false
		if !errors.Is(msg.err, context.Canceled) {
			result := msg.result
			if msg.err != nil {
				result.Error = msg.err.Error()
			}
			if result.Snapshot == nil && h.result.Snapshot != nil {
				result.Snapshot, result.CachedAt, result.FromCache = h.result.Snapshot, h.result.CachedAt, true
			}
			if result.State == "" && result.Error != "" {
				result.State = fleet.HostUnreachable
			}
			result.Name, result.Local, result.EndpointID = h.descriptor.Name, false, h.descriptor.EndpointID
			h.result = result
		}
		m.fleetTree.hosts[i] = h
		return m, m.pumpFleetReads(), true
	case fleetMenuMsg:
		if msg.generation != m.fleetTree.menuGeneration || m.overlay.kind != overlayActionMenu {
			return m, nil, true
		}
		i := m.fleetHostIndex(msg.key)
		if i < 0 || m.fleetTree.hosts[i].descriptor.EndpointID != msg.endpoint {
			return m, nil, true
		}
		if m.overlay.fleetHost.Key != msg.key || m.overlay.fleetHost.EndpointID != msg.endpoint {
			return m, nil, true
		}
		m.overlay.detail = m.fleetTree.hosts[i].descriptor.Target
		if msg.err != nil {
			m.overlay.body = msg.err.Error()
			return m, nil, true
		}
		for _, action := range msg.actions {
			if m.overlay.optionCount >= len(m.overlay.options) {
				break
			}
			if action.Disabled {
				m.overlay.body += action.Label + ": " + action.Description + "\n"
				continue
			}
			m.overlay.addOption(listActionFleetHost, action.Label)
			m.overlay.options[m.overlay.optionCount-1].fleetID = action.ID
		}
		return m, nil, true
	case fleetProcessMsg:
		i := m.fleetHostIndex(msg.host.Key)
		if i < 0 || m.fleetTree.hosts[i].descriptor.EndpointID != msg.host.EndpointID {
			return m, nil, true
		}
		if msg.err != nil {
			m.copyFleetHosts()
			m.fleetTree.hosts[i].acting = false
			m.err = msg.err
			return m, nil, true
		}
		if msg.process == nil {
			m.copyFleetHosts()
			m.fleetTree.hosts[i].acting = false
			return m, nil, true
		}
		return m, runExecProcess(msg.process, func(err error) tea.Msg { return fleetProcessDoneMsg{msg.host, msg.action, err} }), true
	case fleetProcessDoneMsg:
		if i := m.fleetHostIndex(msg.host.Key); i >= 0 {
			m.copyFleetHosts()
			m.fleetTree.hosts[i].acting = false
			m.fleetTree.hosts[i].authenticatedCache = msg.action == "authenticated-refresh" && msg.err == nil
		}
		m.err = msg.err
		m.status = "returned from " + msg.host.Name
		if msg.action != "authenticated-refresh" {
			m.fleetTree.afterAction = msg.host.Key
		}
		return m, m.beginFleetHostsLoad(), true
	}
	return m, nil, false
}

func (m Model) fleetLocalResult(d FleetHostDescriptor) fleet.HostResult {
	result := fleet.HostResult{Name: d.Name, Local: true}
	state := m.viewLoad(ViewRepos)
	if !state.hasSnapshot {
		return result
	}
	snapshot := fleet.Snapshot{SchemaVersion: fleet.SnapshotSchemaVersion, Host: d.Name}
	for _, row := range m.repos {
		if row.IsTry() {
			continue
		}
		if row.ObservedAt.After(snapshot.GeneratedAt) {
			snapshot.GeneratedAt = row.ObservedAt
		}
		counts := fleet.TaskCounts{}
		for _, t := range row.Tasks {
			switch t.State {
			case task.Hot:
				counts.Hot++
			case task.Warm:
				counts.Warm++
			case task.Cold:
				counts.Cold++
			case task.Done:
				counts.Done++
			}
		}
		identities := []string{}
		seen := map[string]bool{}
		for _, remote := range row.Topology.Remotes {
			for _, raw := range append(append([]string(nil), remote.FetchURLs...), remote.PushURLs...) {
				identity := catalog.NormalizeRemoteIdentity(raw)
				if identity != "" && !seen[identity] {
					seen[identity] = true
					identities = append(identities, identity)
				}
			}
		}
		sort.Strings(identities)
		gitKnown := row.GitKnown && row.Pending == ""
		snapshot.Repositories = append(snapshot.Repositories, fleet.RepoSnapshot{Name: row.Repo.Name, Display: row.Repo.Display(), Category: row.Repo.Category, Path: row.Repo.Path, RealPath: row.Repo.RealPath, RemoteIdentities: identities, LastActivity: row.LastActivity, Branch: row.Status.Branch, Status: row.Status, GitKnown: &gitKnown, Tasks: counts, Live: row.Live, Runtime: row.Runtime, RuntimeHandle: row.RuntimeHandle, AgentStatus: row.RuntimeStatus, Worktrees: row.Worktrees, Topology: row.Topology})
	}
	result.Snapshot, result.State = &snapshot, fleet.HostOK
	result.FromCache = state.loading || state.freshness != perftrace.FreshnessFresh
	if err := m.viewError(ViewRepos); err != nil {
		result.Error = err.Error()
	}
	return result
}

// Materialize the accepted local inventory once per repository update, rather
// than rediscovering it or rebuilding every child on each render/layout pass.
func (m *Model) syncFleetLocal() {
	m.copyFleetHosts()
	for i := range m.fleetTree.hosts {
		h := &m.fleetTree.hosts[i]
		if !h.descriptor.Local {
			continue
		}
		h.result = m.fleetLocalResult(h.descriptor)
	}
}

func (m Model) fleetHeader(h fleetHostState) FleetRow {
	d, result := h.descriptor, h.result
	r := FleetRow{HostKey: d.Key, EndpointID: d.EndpointID, Host: d.Name, Local: d.Local, Target: d.Target, OS: d.OS, Expanded: h.expanded, Loading: h.loading, State: result.State, Error: result.Error, FromCache: result.FromCache, Known: result.Snapshot != nil}
	if d.Local {
		r.Loading = m.viewLoad(ViewRepos).loading
		r.FromCache = r.Known && (r.FromCache || r.Loading || m.viewLoad(ViewRepos).freshness != perftrace.FreshnessFresh)
		if r.Error == "" && m.viewError(ViewRepos) != nil {
			r.Error = m.viewError(ViewRepos).Error()
		}
	}
	if result.Snapshot != nil {
		r.RepoCount = len(result.Snapshot.Repositories)
		r.ObservedAt = result.Snapshot.GeneratedAt
	}
	if result.CachedAt != nil {
		r.ObservedAt = *result.CachedAt
	}
	return r
}

func (m Model) visibleFleetTree() []FleetRow {
	hosts := append([]fleetHostState(nil), m.fleetTree.hosts...)
	order := m.tableSorts[int(ViewFleet)]
	sort.SliceStable(hosts, func(i, j int) bool {
		if hosts[i].descriptor.Local != hosts[j].descriptor.Local {
			return hosts[i].descriptor.Local
		}
		a, b := strings.ToLower(hosts[i].descriptor.Name), strings.ToLower(hosts[j].descriptor.Name)
		if order.column == "state" {
			a, b = string(m.fleetHeader(hosts[i]).State), string(m.fleetHeader(hosts[j]).State)
		}
		if order.descending && (order.column == "host" || order.column == "state") {
			return a > b
		}
		return a < b
	})
	var out []FleetRow
	for _, h := range hosts {
		header := m.fleetHeader(h)
		result := h.result
		hostMatch := matches(strings.ToLower(strings.Join([]string{header.Host, header.Target, header.OS, string(header.State), header.Error}, " ")), m.filter)
		var children []FleetRow
		if result.Snapshot != nil {
			for i := range result.Snapshot.Repositories {
				r := header
				r.Repository = &result.Snapshot.Repositories[i]
				r.GitKnown = r.Repository.GitKnown != nil && *r.Repository.GitKnown && (!h.descriptor.Local || !header.Loading)
				if m.filter == "" || hostMatch || matches(r.searchText(), m.filter) {
					children = append(children, r)
				}
			}
		}
		if !hostMatch && len(children) == 0 {
			continue
		}
		sort.SliceStable(children, func(i, j int) bool {
			return strings.ToLower(children[i].Repository.Display) < strings.ToLower(children[j].Repository.Display)
		})
		if order.column != "host" && order.column != "state" {
			children = applyColumnSort(m, children, fleetCell)
		}
		projectExpanded := h.expanded || (m.filter != "" && len(children) > 0)
		header.Expanded = projectExpanded
		out = append(out, header)
		if projectExpanded {
			out = append(out, children...)
		}
	}
	return out
}

func (m *Model) restoreFleetFocus(token selectionToken) {
	if token.key == "" {
		m.fleetCursor = min(m.fleetCursor, max(0, len(m.visibleFleet())-1))
		return
	}
	rows := m.visibleFleet()
	for i, r := range rows {
		if fleetRowKey(r) == token.key {
			m.fleetCursor = i
			return
		}
	}
	// A removed/hidden child returns to its host, not a different machine.
	for i, r := range rows {
		if r.Repository == nil && strings.HasPrefix(token.key, fleetRowKey(r)+"\x00") {
			m.fleetCursor = i
			return
		}
	}
	m.fleetCursor = min(m.fleetCursor, max(0, len(rows)-1))
}

func (m Model) toggleFleetHost() (tea.Model, tea.Cmd) {
	row, ok := m.currentFleet()
	if !ok {
		return m, nil
	}
	i := m.fleetHostIndex(row.HostKey)
	if i < 0 {
		return m, nil
	}
	m.copyFleetHosts()
	h := &m.fleetTree.hosts[i]
	h.expanded = !h.expanded
	if !h.expanded || h.descriptor.Local {
		return m, nil
	}
	if !h.attempted && !h.descriptor.CacheFresh {
		m.enqueueFleetRead(h.descriptor.Key, true, false)
	}
	return m, m.pumpFleetReads()
}

func (m Model) refreshSelectedFleetHost() (tea.Model, tea.Cmd) {
	row, ok := m.currentFleet()
	if !ok {
		m.status = "No host matches; use the action menu to update all hosts"
		return m, nil
	}
	if row.Local {
		m.beginLocalLoads(loadRefresh)
		return m, m.reload()
	}
	m.enqueueFleetRead(row.HostKey, true, true)
	return m, m.pumpFleetReads()
}

func (m Model) refreshAllFleetHosts() (tea.Model, tea.Cmd) {
	for _, h := range m.fleetTree.hosts {
		if !h.descriptor.Local {
			m.enqueueFleetRead(h.descriptor.Key, true, true)
		}
	}
	m.status = "Updating all configured hosts…"
	return m, m.pumpFleetReads()
}

func (m Model) fleetCoverage() string {
	cached, live, unloaded, loading := 0, 0, 0, 0
	for _, h := range m.fleetTree.hosts {
		r := m.fleetHeader(h)
		if !r.Known {
			unloaded++
		} else if r.FromCache || r.Error != "" {
			cached++
		} else if !r.Loading {
			live++
		}
		if r.Loading {
			loading++
		}
	}
	return fmt.Sprintf("%d hosts · %d current · %d cached · %d unloaded · %d loading · search: known", len(m.fleetTree.hosts), live, cached, unloaded, loading)
}

func (m Model) openActionMenuCommand() (tea.Model, tea.Cmd) {
	m = m.openActionMenu()
	if m.view != ViewFleet || !m.hostFleetEnabled() || m.actions.ListFleetHostActions == nil {
		return m, nil
	}
	row, ok := m.currentFleet()
	if !ok {
		return m, nil
	}
	i := m.fleetHostIndex(row.HostKey)
	if i < 0 {
		return m, nil
	}
	d := m.fleetTree.hosts[i].descriptor
	// Host actions use their own parent identity; repository menu entries retain
	// the selected child and its existing open behavior.
	m.overlay.fleetHost = d
	m.fleetTree.menuGeneration++
	generation := m.fleetTree.menuGeneration
	m.overlay.detail = "Loading host actions…"
	return m, func() tea.Msg {
		actions, err := m.actions.ListFleetHostActions(m.baseContext(), d)
		return fleetMenuMsg{d.Key, d.EndpointID, generation, actions, err}
	}
}

func (m Model) runFleetHostActionFor(d FleetHostDescriptor, id string) (tea.Model, tea.Cmd) {
	if m.actions.RunFleetHostAction == nil {
		return m, nil
	}
	i := m.fleetHostIndex(d.Key)
	if i < 0 || m.fleetTree.hosts[i].descriptor.EndpointID != d.EndpointID {
		m.err = errors.New("host configuration changed while its menu was open")
		return m, nil
	}
	if m.fleetTree.hosts[i].acting {
		m.status = "A host action is already pending"
		return m, nil
	}
	m.copyFleetHosts()
	h := &m.fleetTree.hosts[i]
	h.acting = true
	if h.cancel != nil {
		h.cancel()
	}
	h.cancel, h.loading = nil, false
	m.fleetTree.nextRequest++
	h.request = m.fleetTree.nextRequest
	return m, func() tea.Msg {
		process, err := m.actions.RunFleetHostAction(m.baseContext(), d, id)
		return fleetProcessMsg{d, id, process, err}
	}
}

func fleetTreeStateLabel(row FleetRow) string {
	if row.Loading {
		return "loading"
	}
	if row.FromCache {
		if row.Error != "" {
			return "cached/error"
		}
		return "cached"
	}
	if row.State != "" {
		return string(row.State)
	}
	if row.Error != "" {
		return "unavailable"
	}
	return "not loaded"
}

func (m Model) renderFleetTree() string {
	rows := m.visibleFleetTree()
	if len(rows) == 0 {
		if len(m.fleetTree.hosts) == 0 {
			return "  " + styleDim.Render("Loading configured hosts…") + "\n"
		}
		return "  " + styleDim.Render("No known host or repository matches. Space: update all hosts.") + "\n"
	}
	hostW, repoW := clamp(m.width*18/100, 14, 26), clamp(m.width*22/100, 16, 32)
	wide := m.width >= 140
	pathW := max(4, m.width-hostW-repoW-20)
	if wide {
		pathW = max(4, m.width-hostW-repoW-74)
	}
	var b strings.Builder
	heading := fmt.Sprintf("  %-*s  %-*s  %-12s  %s", hostW, "HOST", repoW, "REPO", "STATE", "PATH")
	if wide {
		heading = fmt.Sprintf("  %-*s  %-*s  %-12s  %-14s  %-12s  %-10s  %-8s  %s", hostW, "HOST", repoW, "REPO", "STATE", "BRANCH", "GIT", "LIVE", "TASKS", "PATH")
	}
	b.WriteString(styleHeader.Render(heading) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		r := rows[i]
		host, name, branch, git, live, tasks, path := r.Host, "—", "—", "—", "—", "—", r.Target
		if r.Repository == nil {
			prefix := "▸ "
			if r.Expanded {
				prefix = "▾ "
			}
			host = prefix + host
			if r.Local {
				host += " (local)"
			}
			if r.Known {
				name = fmt.Sprintf("%d repositories", r.RepoCount)
			}
			branch = r.OS
		} else {
			host = ""
			name = "  " + r.Repository.Display
			branch, git, path = r.Repository.Branch, r.Repository.Status.Summary(), r.Repository.Path
			if !r.GitKnown {
				git = "?"
			}
			tasks = fleetTasks(r.Repository.Tasks.Hot, r.Repository.Tasks.Warm, r.Repository.Tasks.Cold, r.Repository.Tasks.Done)
			if r.Repository.Live {
				live = r.Repository.Runtime
				if r.Repository.AgentStatus != "" {
					live += " · " + r.Repository.AgentStatus
				}
			}
		}
		line := fmt.Sprintf("%-*s  %-*s  %-12s  %s", hostW, pad(host, hostW), repoW, pad(name, repoW), pad(fleetTreeStateLabel(r), 12), pad(path, pathW))
		if wide {
			line = fmt.Sprintf("%-*s  %-*s  %-12s  %-14s  %-12s  %-10s  %-8s  %s", hostW, pad(host, hostW), repoW, pad(name, repoW), pad(fleetTreeStateLabel(r), 12), pad(branch, 14), pad(git, 12), pad(live, 10), pad(tasks, 8), pad(path, pathW))
		}
		styled := line
		if r.FromCache {
			styled = styleDim.Render(line)
		} else if r.Error != "" {
			styled = styleDrift.Render(line)
		} else if r.Repository != nil && r.Repository.Live {
			styled = styleLive.Render(line)
		}
		b.WriteString(m.renderLine(i, line, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}
