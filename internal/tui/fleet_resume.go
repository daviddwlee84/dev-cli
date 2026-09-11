package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
)

// ResumeFleetHandoff preserves snapshots and user navigation. The next Init
// restarts interrupted producers with fresh contexts and generations; completed
// local inventories are not scanned again.
func (m Model) ResumeFleetHandoff(ctx context.Context, result FleetExecutionResult, runErr error) Model {
	handoff := m.pendingFleetHandoff
	if handoff == nil {
		return m
	}
	focusModel := m
	focusModel.view = ViewFleet
	focus := focusModel.currentToken()
	m.pendingFleetHandoff = nil
	m.quitting = false
	m.fleetTerminalActive = false
	m.runContext = ctx
	m.resumeInitial = true
	m.resumeCommands = nil
	m.initialLoad = false
	// Selection topology reads outlive the bulk REPOS loading flag. Their old
	// messages belong to the stopped Program; pending rows must be eligible for
	// one new read when REPOS is selected again.
	m.topologyRequested = nil
	m.status = result.Summary
	if m.status == "" {
		m.status = "returned from " + handoff.host.Name
	}
	m.err = runErr
	if runErr != nil && result.Summary != "" {
		m.err = fmt.Errorf("%s: %w", result.Summary, runErr)
	}
	if handoff.boundaryErr != nil {
		m.terminalHandoffErr = m.err
		m.quitting = true
		return m
	}
	var interrupted [viewCount]bool
	for _, view := range Views {
		interrupted[int(view)] = m.viewLoad(view).loading
		if cancel := m.loadCancels[int(view)]; cancel != nil {
			cancel()
		}
		m.loadCancels[int(view)], m.loadContexts[int(view)] = nil, nil
		if interrupted[int(view)] {
			m.invalidateView(view)
		}
	}
	if m.localCancel != nil {
		m.localCancel()
	}
	m.localCancel, m.localContext = nil, nil
	m.localGeneration++
	add := func(command tea.Cmd) {
		if command != nil {
			m.resumeCommands = append(m.resumeCommands, command)
		}
	}
	// Resume unfinished local views independently. A completed TASKS or TRY
	// view is not repeated merely because REPOS was still streaming.
	sharedLocal := m.actions.Local.Start != nil && ((interrupted[int(ViewTasks)] && m.actions.Reload == nil) || (interrupted[int(ViewRepos)] && m.actions.ReloadRepos == nil) || (interrupted[int(ViewTries)] && m.actions.Tries.Reload == nil))
	if sharedLocal {
		m.beginLocalLoads(loadAction)
		add(m.reload())
	}
	if !sharedLocal && interrupted[int(ViewTasks)] && m.actions.Reload != nil {
		generation := m.beginViewLoad(ViewTasks, loadAction)
		load := m.actions.Reload
		readCtx := m.viewContext(ViewTasks)
		add(func() tea.Msg {
			rows, err := load(readCtx)
			return reloadMsg{rows: rows, rowsSet: true, rowsValid: snapshotValid(rows, err), rowsGeneration: generation, rowsErr: err}
		})
	}
	if !sharedLocal && interrupted[int(ViewRepos)] && m.actions.ReloadRepos != nil {
		m.beginViewLoad(ViewRepos, loadAction)
		add(m.reloadReposOnly())
	}
	if !sharedLocal && interrupted[int(ViewTries)] && m.actions.Tries.Reload != nil {
		m.beginViewLoad(ViewTries, loadAction)
		add(m.reloadTries(false))
	}
	for _, view := range []View{ViewRemote, ViewSkills, ViewMCP} {
		if !interrupted[int(view)] {
			continue
		}
		m.beginViewLoad(view, loadAction)
		if m.viewWaitsForRepos(view) {
			m.setViewStatus(view, "waiting for local repositories…")
			continue
		}
		switch view {
		case ViewRemote:
			add(m.reloadRemote())
		case ViewSkills:
			add(m.reloadSkills())
		case ViewMCP:
			add(m.reloadMCP())
		}
	}
	if m.configCancel != nil {
		m.configCancel()
		m.beginConfigLoad()
		add(m.reloadConfig(false))
	}
	if !m.startupRepo.loaded {
		add(m.readStartupRepo())
	}
	if !m.viewLoad(ViewRemote).hasSnapshot && !interrupted[int(ViewRemote)] && m.actions.LoadRemoteCache != nil {
		add(m.loadRemoteCache())
	}
	unknownTools := false
	for _, tool := range m.actions.Tools {
		unknownTools = unknownTools || tool.Availability == ToolUnknown
	}
	if unknownTools {
		m.toolGeneration++
		add(m.probeTools())
	}
	if m.sizeLoad.ID != 0 || len(m.scopedSizeLoads) > 0 {
		if m.actions.Sizes.Cancel != nil {
			if m.sizeLoad.ID != 0 {
				m.actions.Sizes.Cancel(m.sizeLoad.ID)
			}
			for _, load := range m.scopedSizeLoads {
				m.actions.Sizes.Cancel(load.ID)
			}
		}
		m.sizeLoad = diskusage.Load{}
		m.scopedSizeLoads = nil
		var command tea.Cmd
		m, command = m.beginSizeLoad(false)
		add(command)
	}
	if m.skillsChecking {
		m.invalidateView(ViewSkills)
		m.skillsChecking = true
		add(m.checkSkills(m.skills))
	}
	if m.statsCancel != nil {
		m.statsCancel()
		m.statsCancel = nil
		m.statsRefreshing = false
	}
	m.noteLoading = false
	m.copyFleetHosts()
	// Cache/descriptor messages queued by the old Program cannot seed a new
	// input epoch, even when their endpoint has not changed.
	m.fleetTree.generation++
	for i := range m.fleetTree.hosts {
		h := &m.fleetTree.hosts[i]
		if h.loading {
			if h.cancel != nil {
				h.cancel()
			}
			h.cancel = nil
			h.loading = false
			m.fleetTree.nextRequest++
			h.request = m.fleetTree.nextRequest
			m.fleetTree.queue = append(m.fleetTree.queue, fleetQueuedRead{key: h.descriptor.Key, endpoint: h.descriptor.EndpointID, explicit: !h.background})
		}
		if h.cacheLoading {
			h.cacheLoading = false
			h.cacheLoaded = false
		}
		if h.descriptor.Key == handoff.host.Key && h.descriptor.EndpointID == handoff.host.EndpointID {
			h.acting = false
			if result.RefreshHost {
				h.authenticatedCache = runErr == nil
				h.result.FromCache = h.result.Snapshot != nil
				h.cacheLoaded = false
				h.cacheLoading = false
			}
		}
	}
	herdrPending := m.fleetTree.herdr.loading
	herdrStale := m.fleetTree.herdr.stale
	m.invalidateFleetHerdr()
	if m.fleetTree.loading {
		if result.RefreshHerdr {
			m.fleetTree.herdr.requested = true
		}
		add(m.beginFleetHostsLoad())
	} else {
		add(m.pumpFleetCaches())
		if result.RefreshHerdr || herdrPending {
			add(m.requestFleetHerdr(false))
		} else {
			m.fleetTree.herdr.stale = herdrStale
		}
	}
	if m.fleetTree.background && !m.fleetTree.warmReady {
		add(tea.Tick(max(0, time.Until(m.fleetWarmupAt)), func(time.Time) tea.Msg { return fleetWarmupMsg{} }))
	}
	m.enqueueFleetWarmup()
	add(m.pumpFleetReads())
	m.syncFleetLocal()
	m.restoreFleetFocus(focus)
	if err := flushTerminalInput(handoff.input); err != nil {
		m.terminalHandoffErr = errors.Join(runErr, fmt.Errorf("restore dashboard input: %w", err))
		m.quitting = true
	}
	return m
}
