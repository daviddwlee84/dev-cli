package tui

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

func (m Model) renderRemoteTree() string {
	items := m.visibleRemoteItems()
	if len(items) == 0 {
		if m.viewLoad(ViewRemote).loading {
			return "  " + styleDim.Render("Loading repositories from forge CLIs…") + "\n"
		}
		if m.filter != "" {
			return "  " + styleDim.Render("No loaded repositories or pull requests match /"+snippetDisplayText(m.filter)) + "\n"
		}
		if !m.viewLoad(ViewRemote).hasSnapshot {
			return "  " + styleDim.Render("Remote repositories load when this view is opened.") + "\n"
		}
		return "  " + styleDim.Render("No remote repositories returned. Check forge authentication and `dev doctor`.") + "\n"
	}
	columns := m.remoteColumns(false)
	var output strings.Builder
	if m.viewLoad(ViewRemote).loading {
		output.WriteString("  " + fitCell(styleDim.Render("Showing cached repositories while refreshing…"), max(1, m.width-4)) + "\n")
	}
	output.WriteString(renderMetricHeader(columns))
	from, to := m.window(len(items))
	for index := from; index < to; index++ {
		item := items[index]
		var cells []string
		for _, column := range columns {
			cells = append(cells, fitCell(snippetDisplayText(m.remoteItemValue(item, column.key)), column.width))
		}
		line := strings.Join(cells, "  ")
		styled := line
		if item.PR != nil {
			switch {
			case item.PR.PR.Checks == forge.ChecksFailing:
				styled = styleErr.Render(line)
			case item.PR.PR.Draft:
				styled = styleDim.Render(line)
			case item.PR.PR.Checks == forge.ChecksPending || item.PR.PR.ReviewDecision == "changes_requested" || item.PR.PR.Mergeable == "conflicting":
				styled = styleWarm.Render(line)
			case item.PR.Readiness == "ready" && m.remotePRDetailFresh(*item.PR):
				styled = styleOK.Render(line)
			case len(item.PR.PR.Roles) > 0:
				styled = styleWarm.Render(line)
			}
		} else if item.more {
			styled = styleDim.Render(line)
		} else if item.Repository.Cloned() || m.remoteCloneTargets(item.Repository) {
			styled = styleLive.Render(line)
		} else if item.Repository.CloneProblemPath != "" {
			styled = styleDrift.Render(line)
		} else if item.Repository.Repo.Archived {
			styled = styleDim.Render(line)
		}
		output.WriteString(m.renderLine(index, line, styled))
	}
	output.WriteString(m.scrollNote(len(items), from, to))
	return output.String()
}

func (m Model) remoteItemValue(item remoteItem, column string) string {
	connector := "├─ "
	if item.last {
		connector = "└─ "
	}
	if item.more {
		if column == "repository" {
			if m.remotePRState(item.Repository.Repo).loading {
				return connector + "Loading more pull requests…"
			}
			return connector + fmt.Sprintf("Load next %d…", m.remotePRPageLimit())
		}
		return ""
	}
	if item.PR != nil {
		row := *item.PR
		switch column {
		case "repository":
			label := connector + fmt.Sprintf("#%d ", row.PR.Number)
			if relationship := remotePRRelationship(row); relationship != "" {
				label += "[" + relationship + "] "
			}
			label += row.PR.Title
			return label
		case "forge":
			return ""
		case "vis":
			return remotePRStateLabel(row)
		case "updated":
			if row.PR.UpdatedAt.IsZero() {
				return "—"
			}
			return row.PR.UpdatedAt.Format("2006-01-02")
		case "local":
			if row.LocalPath != "" {
				return "worktree"
			}
			return "—"
		case "stars", "issues", "prs":
			return ""
		case "forks":
			return ""
		case "description":
			return remotePRStateLabel(row) + " · " + row.PR.Author
		}
		return ""
	}
	repository := item.Repository
	switch column {
	case "forge":
		return string(repository.Repo.Forge)
	case "repository":
		label := repository.Repo.FullName
		if m.remotePRSupported(repository.Repo) {
			marker := "▸ "
			if item.expanded {
				marker = "▾ "
			}
			label = marker + label
			state := m.remotePRState(repository.Repo)
			if state.loading {
				label += " [loading PRs]"
			} else if state.loaded {
				if state.result.Scope == PRScopeRelated {
					label += " [related to me]"
				} else {
					label += " [all open]"
				}
				if m.remotePRStale(state) {
					label += " ~"
				}
			} else if state.err != nil {
				label += " [PR error]"
			}
		}
		return label
	case "vis":
		return dashCell(strings.ToLower(repository.Repo.Visibility))
	case "updated":
		return remoteAge(repository)
	case "local":
		if m.remoteCloneTargets(repository) {
			if m.remoteClone.phase == remoteCloneRunning {
				return m.remoteCloneSpinner.View() + " clone"
			}
			return m.remoteCloneSpinner.View() + " repo"
		}
		if repository.CloneProblemPath != "" {
			return "inspect"
		}
		if repository.Cloned() {
			if repository.LocalKind != "" && repository.LocalKind != "repository" {
				return string(repository.LocalKind)
			}
			return "repo"
		}
		return "—"
	case "description":
		return repository.Repo.Description
	default:
		return m.metricText(repoMetric(repository.Repo.Metrics, column))
	}
}
