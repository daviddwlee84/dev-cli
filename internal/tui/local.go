package tui

import (
	"context"

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

type localMsg struct {
	load   LocalLoad
	result LocalResult
	done   bool
}

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
		result, ok := <-load.Results
		if !ok {
			return localMsg{load: load, done: true}
		}
		return localMsg{load: load, result: result}
	}
}

func (m Model) applyLocalResult(result LocalResult) (Model, bool) {
	accepted := false
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
			m.repos = append([]RepoRow(nil), result.Repos...)
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
	m.setAt(m.at())
	return m, accepted
}
