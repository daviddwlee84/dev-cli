package tui

import (
	"fmt"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
)

func mergeRepoProgress(previous, updates []RepoRow) []RepoRow {
	rows := append([]RepoRow(nil), previous...)
	for _, row := range updates {
		found := false
		for i, old := range rows {
			if old.Repo.Path == row.Repo.Path {
				if row.Pending == "loading" && old.Pending != "loading" {
					found = true
					break
				}
				if row.TopologyPending && !old.TopologyPending && old.Pending == "" {
					row.Topology, row.TopologyErr, row.TopologyPending = old.Topology, old.TopologyErr, false
				}
				if row.Usage == nil {
					row.Usage, row.SizeError = old.Usage, old.SizeError
				}
				rows[i] = row
				found = true
				break
			}
		}
		if !found {
			rows = append(rows, row)
		}
	}
	return rows
}

func (m Model) applyRepoProgress(result LocalResult) (Model, bool) {
	state := m.viewLoad(ViewRepos)
	if result.Generation != state.generation || m.repoProgressPhase == 3 {
		return m, false
	}
	phase := 1
	if result.Phase != "cache" {
		phase = 2
	}
	if phase < m.repoProgressPhase {
		return m, false
	}
	m.repoProgressPhase = phase
	token := m.currentToken()
	path := m.repoFocusPath()
	m.repos = mergeRepoProgress(m.repos, result.Repos)
	m.loads[int(ViewRepos)].hasSnapshot = true
	m.loads[int(ViewRepos)].freshness = perftrace.FreshnessStale
	m.loads[int(ViewRepos)].actionable = false
	m.loads[int(ViewRepos)].source = perftrace.SourceLive
	if phase == 1 {
		m.loads[int(ViewRepos)].source = perftrace.SourceCache
	}
	count := len(m.repos)
	m.trace.Mark(perftrace.TUIViewSnapshotAccepted, perftrace.Fields{View: perftrace.ViewRepos, Stage: perftrace.StageFirstEnrichment, Source: m.loads[int(ViewRepos)].source, Freshness: perftrace.FreshnessStale, Generation: result.Generation, Rows: &count})
	message := fmt.Sprintf("Refreshing local repositories… %d visible", count)
	if phase == 1 && len(result.Repos) > 0 {
		message = "Cached " + result.Repos[0].ObservedAt.Local().Format("Jan 2 15:04") + " · " + message
	}
	m.setViewStatus(ViewRepos, message)
	m.restoreRepoFocus(token, path)
	m.setAt(m.at())
	return m, true
}

func (m Model) repoFocusPath() string {
	if item, ok := m.currentRepoItem(); ok {
		if checkout, child := item.checkout(); child {
			return checkout.Worktree.Path
		}
		return item.Repo.Repo.Path
	}
	return ""
}
func (m *Model) restoreRepoFocus(token selectionToken, path string) {
	if m.selectToken(token) || m.view != ViewRepos || path == "" {
		return
	}
	for i, item := range m.visibleRepoItems() {
		candidate := item.Repo.Repo.Path
		if checkout, child := item.checkout(); child {
			candidate = checkout.Worktree.Path
		}
		if candidate == path {
			m.setAt(i)
			return
		}
	}
}

func completeRepoRows(previous, rows []RepoRow) []RepoRow {
	merged := mergeRepoProgress(previous, rows)
	out := make([]RepoRow, 0, len(rows))
	for _, row := range rows {
		for _, candidate := range merged {
			if row.Repo.Path == candidate.Repo.Path {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}
