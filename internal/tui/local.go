package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
)

// LocalLoadRequest identifies the three view generations sharing one source
// snapshot. Each result remains independently replaceable.
type LocalLoadRequest struct {
	CycleGeneration uint64
	TasksGeneration uint64
	ReposGeneration uint64
	TriesGeneration uint64
	ShowAllTries    bool
	ForceSizes      bool
}

// LocalResult is one terminal TASKS, REPOS, or TRY snapshot.
type LocalResult struct {
	Phase      string
	View       View
	Generation uint64
	Tasks      []inventory.Row
	Repos      []RepoRow
	Tries      []TryRow
	Valid      bool
	Err        error
}

// LocalLoad is a cancellable stream owned by the caller's context.
type LocalLoad struct {
	ID      uint64
	Request LocalLoadRequest
	Results <-chan LocalResult
}

// LocalActions starts one shared local source cycle.
type LocalActions struct {
	Start func(context.Context, LocalLoadRequest) LocalLoad
}

// localMsg carries every result that was already available when the first
// one arrived, so one Update applies a burst of per-repository progress and the
// list re-renders once. done reports the closed stream and carries no results.
type localMsg struct {
	load    LocalLoad
	results []LocalResult
	done    bool
}

// localBatchWindow bounds how long a wait gathers further results after the
// first one. Tests set it to zero to drain only what is already buffered.
var localBatchWindow = 20 * time.Millisecond

const localBatchLimit = 64

func (m Model) startLocalLoad() tea.Cmd {
	if m.actions.Local.Start == nil {
		return nil
	}
	request := LocalLoadRequest{
		CycleGeneration: m.localGeneration,
		TasksGeneration: m.viewLoad(ViewTasks).generation,
		ReposGeneration: m.viewLoad(ViewRepos).generation,
		TriesGeneration: m.viewLoad(ViewTries).generation,
		ShowAllTries:    m.showAllTries,
		ForceSizes:      m.forceSizeReload,
	}
	load := m.actions.Local.Start(m.localReadContext(), request)
	load.Request = request
	if load.ID == 0 || load.Results == nil {
		return nil
	}
	return waitForLocal(load)
}

func waitForLocal(load LocalLoad) tea.Cmd {
	return func() tea.Msg {
		msg := drainLocal(load.Results, localBatchWindow, localBatchLimit)
		msg.load = load
		return msg
	}
}

// drainLocal blocks for the first result, then gathers results that follow
// within window, up to limit. A message carries results or done, never both.
func drainLocal(results <-chan LocalResult, window time.Duration, limit int) localMsg {
	var msg localMsg
	result, ok := <-results
	if !ok {
		msg.done = true
		return msg
	}
	msg.results = append(msg.results, result)
	var deadline <-chan time.Time
	if window > 0 {
		timer := time.NewTimer(window)
		defer timer.Stop()
		deadline = timer.C
	}
	for len(msg.results) < limit {
		select {
		case result, ok = <-results:
			if !ok {
				// Report the close on the next wait so a message carries either
				// results or done, never both.
				return msg
			}
			msg.results = append(msg.results, result)
			continue
		default:
		}
		if deadline == nil {
			return msg
		}
		select {
		case result, ok = <-results:
			if !ok {
				return msg
			}
			msg.results = append(msg.results, result)
		case <-deadline:
			return msg
		}
	}
	return msg
}

func (m Model) applyLocalResult(result LocalResult) (Model, bool) {
	accepted := false
	if result.View == ViewRepos && result.Phase != "" {
		return m.applyRepoProgress(result)
	}
	token := m.currentToken()
	path := m.repoFocusPath()
	switch result.View {
	case ViewTasks:
		accepted = m.applyViewResult(
			ViewTasks, result.Generation, result.Valid, "live", resultFreshness(result.Err),
			len(result.Tasks), result.Err, result.Valid,
		)
		if accepted && result.Valid {
			m.rows = append([]inventory.Row(nil), result.Tasks...)
		}
	case ViewRepos:
		accepted = m.applyViewResult(
			ViewRepos, result.Generation, result.Valid, "live", resultFreshness(result.Err),
			len(result.Repos), result.Err, result.Valid,
		)
		if accepted && result.Valid {
			m.setViewStatus(ViewRepos, "")
			m.acceptCompleteRepoSnapshot(result.Err)
			if result.Err != nil {
				m.repos = mergeRepoProgress(m.repos, result.Repos)
			} else {
				m.repos = completeRepoRows(m.repos, result.Repos)
			}
			m.repoProgressPhase = 3
			m.repoOrder = nil
			m.matchRemoteLocals()
		}
	case ViewTries:
		accepted = m.applyViewResult(
			ViewTries, result.Generation, result.Valid, "live", resultFreshness(result.Err),
			len(result.Tries), result.Err, result.Valid,
		)
		if accepted && result.Valid {
			m.tries = append([]TryRow(nil), result.Tries...)
			m.matchRemoteLocals()
		}
	}
	m.restoreRepoFocus(token, path)
	m.setAt(m.at())
	return m, accepted
}
