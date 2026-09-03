package tui

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/perftrace"
)

const viewCount = int(ViewMCP) + 1

func (m Model) baseContext() context.Context {
	if m.runContext != nil {
		return m.runContext
	}
	return context.Background()
}

func (m Model) viewContext(view View) context.Context {
	if ctx := m.loadContexts[int(view)]; ctx != nil {
		return ctx
	}
	return m.baseContext()
}

func (m *Model) beginConfigLoad() uint64 {
	if m.configCancel != nil {
		m.configCancel()
	}
	m.configGeneration++
	m.configContext, m.configCancel = context.WithCancel(m.baseContext())
	return m.configGeneration
}

func (m Model) configReadContext() context.Context {
	if m.configContext != nil {
		return m.configContext
	}
	return m.baseContext()
}

type loadCause string

const (
	loadInitial loadCause = "initial"
	loadVisit   loadCause = "visit"
	loadRefresh loadCause = "refresh"
	loadConfig  loadCause = "config"
	loadAction  loadCause = "action"
)

// viewLoadState is deliberately plain value state. Model is copied by Bubble
// Tea, so request state must not hide a mutable map or shared slice.
type viewLoadState struct {
	generation  uint64
	cause       loadCause
	requested   bool
	loading     bool
	hasSnapshot bool
	source      perftrace.Source
	freshness   perftrace.Freshness
	outcome     perftrace.Outcome
	actionable  bool
}

func (m *Model) prepareRepoDependentsForReload() {
	m.prepareRepoDependentForReload(ViewFleet, m.actions.ReloadFleetWithRepos != nil)
	m.prepareRepoDependentForReload(ViewSkills, m.actions.ReloadSkillsWithRepos != nil)
	m.prepareRepoDependentForReload(ViewMCP, m.actions.ReloadMCPWithRepos != nil)
}

func (m *Model) prepareRepoDependentForReload(view View, enabled bool) {
	if !enabled {
		return
	}
	state := m.viewLoad(view)
	switch {
	case state.loading || m.view == view:
		m.beginViewLoad(view, loadRefresh)
		m.setViewStatus(view, "waiting for local repositories…")
	case state.hasSnapshot:
		m.invalidateView(view)
	}
}

func (m *Model) beginViewLoad(view View, cause loadCause) uint64 {
	if view == ViewRepos {
		m.prepareRepoDependentsForReload()
	}
	index := int(view)
	if cancel := m.loadCancels[index]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.loadContexts[index] = ctx
	m.loadCancels[index] = cancel
	state := &m.loads[index]
	state.generation++
	state.cause = cause
	state.requested = true
	state.loading = true
	state.outcome = ""
	m.viewErrors[int(view)] = nil
	m.viewStatuses[int(view)] = ""
	m.trace.Mark(perftrace.TUIViewLoadRequested, perftrace.Fields{
		View: view.traceView(), Stage: perftrace.StageRequested, Generation: state.generation,
	})
	return state.generation
}

func (m *Model) beginLocalLoads(cause loadCause) (tasks, repos, tries uint64) {
	if m.localCancel != nil {
		m.localCancel()
	}
	m.localContext, m.localCancel = context.WithCancel(m.baseContext())
	m.localGeneration++
	return m.beginViewLoad(ViewTasks, cause),
		m.beginViewLoad(ViewRepos, cause),
		m.beginViewLoad(ViewTries, cause)
}

func (m Model) localReadContext() context.Context {
	if m.localContext != nil {
		return m.localContext
	}
	return m.baseContext()
}

func (m *Model) finishLocalLoad() {
	if m.localCancel != nil {
		m.localCancel()
		m.localCancel, m.localContext = nil, nil
	}
}

func (m *Model) invalidateView(view View) {
	index := int(view)
	if cancel := m.loadCancels[index]; cancel != nil {
		cancel()
	}
	m.loadCancels[index], m.loadContexts[index] = nil, nil
	state := &m.loads[index]
	state.generation++
	state.requested = false
	state.loading = false
	state.outcome = ""
	if state.hasSnapshot {
		state.freshness = perftrace.FreshnessStale
	}
	m.viewErrors[index] = nil
	m.viewStatuses[index] = ""
}

func (m *Model) beginTryLoads(includeRepos bool, cause loadCause) {
	m.beginViewLoad(ViewTries, cause)
	if includeRepos {
		m.beginViewLoad(ViewRepos, cause)
	}
}

func (m *Model) seedViewSnapshot(view View, source perftrace.Source, freshness perftrace.Freshness, actionable bool) {
	state := &m.loads[int(view)]
	state.hasSnapshot = true
	state.source = source
	state.freshness = freshness
	state.outcome = perftrace.OutcomeSuccess
	state.actionable = actionable
}

func (m *Model) applyCacheSeed(view View, generation uint64, rows int, freshness perftrace.Freshness) bool {
	state := &m.loads[int(view)]
	if generation != state.generation {
		m.trace.Mark(perftrace.TUIViewResultDiscarded, perftrace.Fields{
			View: view.traceView(), Stage: perftrace.StageDiscarded,
			Generation: generation, Outcome: perftrace.OutcomeSuperseded,
		})
		return false
	}
	m.seedViewSnapshot(view, perftrace.SourceCache, freshness, true)
	count := rows
	m.trace.Mark(perftrace.TUIViewSnapshotAccepted, perftrace.Fields{
		View: view.traceView(), Stage: perftrace.StageCacheAccepted,
		Source: perftrace.SourceCache, Freshness: freshness,
		Outcome: perftrace.OutcomeSuccess, Generation: generation, Rows: &count,
	})
	return true
}

// applyViewResult accepts only the current request generation. valid says the
// producer returned a real snapshot; it is independent of slice nil-ness so a
// successful empty result can replace older rows.
func (m *Model) applyViewResult(view View, generation uint64, valid bool, source perftrace.Source,
	freshness perftrace.Freshness, rows int, err error, actionable bool) bool {

	state := &m.loads[int(view)]
	if generation != state.generation {
		m.trace.Mark(perftrace.TUIViewResultDiscarded, perftrace.Fields{
			View: view.traceView(), Stage: perftrace.StageDiscarded,
			Generation: generation, Outcome: perftrace.OutcomeSuperseded,
		})
		return false
	}

	state.loading = false
	outcome := resultOutcome(valid, err)
	state.outcome = outcome
	if valid {
		state.hasSnapshot = true
		state.source = source
		state.freshness = freshness
		state.actionable = actionable
		count := rows
		m.trace.Mark(perftrace.TUIViewSnapshotAccepted, perftrace.Fields{
			View: view.traceView(), Stage: perftrace.StageSnapshotAccepted,
			Source: source, Freshness: freshness, Outcome: outcome,
			Generation: generation, Rows: &count,
		})
	} else if state.hasSnapshot {
		state.freshness = perftrace.FreshnessStale
	}
	m.viewErrors[int(view)] = err
	m.trace.Mark(perftrace.TUIViewLoadFinished, perftrace.Fields{
		View: view.traceView(), Stage: perftrace.StageFinished,
		Source: state.source, Freshness: state.freshness, Outcome: outcome,
		Generation: generation,
	})
	if cancel := m.loadCancels[int(view)]; cancel != nil {
		cancel()
		m.loadCancels[int(view)] = nil
		m.loadContexts[int(view)] = nil
	}
	return true
}

func resultFreshness(err error) perftrace.Freshness {
	if err != nil {
		return perftrace.FreshnessStale
	}
	return perftrace.FreshnessFresh
}

func resultOutcome(valid bool, err error) perftrace.Outcome {
	switch {
	case errors.Is(err, context.Canceled):
		return perftrace.OutcomeCanceled
	case err != nil && valid:
		return perftrace.OutcomePartial
	case err != nil || !valid:
		return perftrace.OutcomeFailed
	default:
		return perftrace.OutcomeSuccess
	}
}

func (m Model) viewLoad(view View) viewLoadState { return m.loads[int(view)] }

func (m Model) viewNeedsLoad(view View) bool {
	state := m.viewLoad(view)
	if state.loading {
		return false
	}
	if state.hasSnapshot && state.freshness == perftrace.FreshnessFresh {
		return false
	}
	return !state.hasSnapshot || state.freshness == perftrace.FreshnessStale || state.outcome == perftrace.OutcomeFailed
}

func (m *Model) setViewStatus(view View, status string) {
	m.viewStatuses[int(view)] = status
}

func (m Model) viewError(view View) error { return m.viewErrors[int(view)] }

func (m Model) viewStatus(view View) string { return m.viewStatuses[int(view)] }

func (v View) traceView() perftrace.View { return perftrace.View(v.String()) }
