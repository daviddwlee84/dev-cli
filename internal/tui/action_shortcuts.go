package tui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
)

func (m Model) executeDashboardAction(action listAction) (tea.Model, tea.Cmd) {
	switch action {
	case listActionRefresh:
		if m.snippetsActive() {
			m.err, m.status = nil, ""
			return m.loadSnippets()
		}
		if m.view == ViewSSH {
			m.beginViewLoad(ViewSSH, loadRefresh)
			return m, m.reloadSSH()
		}

		if m.view == ViewFleet {
			if m.hostFleetEnabled() {
				return m.refreshSelectedFleetHost()
			}
			m.beginViewLoad(ViewFleet, loadRefresh)
			if err := m.dependentReposUnavailable(ViewFleet); err != nil {
				fleet := m.viewLoad(ViewFleet)
				m.applyViewResult(ViewFleet, fleet.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(ViewFleet) {
				m.setViewStatus(ViewFleet, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(ViewFleet, "refreshing fleet…")
			return m, m.reloadFleet()
		}
		if m.view == ViewSkills || m.view == ViewMCP {
			view := m.view
			m.err = nil
			m.beginViewLoad(view, loadRefresh)
			if err := m.dependentReposUnavailable(view); err != nil {
				state := m.viewLoad(view)
				m.applyViewResult(view, state.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(view) {
				m.setViewStatus(view, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(view, dependentLoadingStatus(view))
			return m, m.reloadDependentView(view)
		}
		m.beginConfigLoad()
		m.status = "reloading config + data…"
		m.forceSizeReload = true
		return m, m.reloadConfig(m.view == ViewRemote)

	case listActionTryCreate:
		return m.openTryForm(TryCreate, TryRow{})
	case listActionCapabilityScope:
		return m.toggleCapabilityScope()
	case listActionSSHToggle:
		m.toggleSSHEntry()
		return m, nil
	case listActionSkillAdd:
		if m.view == ViewSkills {
			if m.actions.AddSkill == nil {
				return m, nil
			}
			mutation, err := m.actions.AddSkill()
			if err != nil {
				m.err = err
				return m, nil
			}
			m.status = "opening interactive skill installer…"
			return m, runSkillMutation(mutation, func(err error) tea.Msg {
				return skillProcessMsg{action: "add", err: err}
			})
		}
	case listActionToggleHistory:

		if m.view == ViewTries {
			m.showAllTries = !m.showAllTries
			m.status = fmt.Sprintf("Try history visible: %v", m.showAllTries)
			m.setAt(0)
			m.beginTryLoads(false, loadRefresh)
			return m, m.reloadTries(false)
		}
		if m.view == ViewTasks {
			m.showDone, m.states = !m.showDone, nil
			m.setAt(0)
		}

	case listActionSkillCheck:

		if m.view == ViewSkills {
			state := m.viewLoad(ViewSkills)
			if state.loading || !state.hasSnapshot {
				m.setViewStatus(ViewSkills, "wait for local agent skills to finish loading")
				return m, nil
			}
			m.err = nil
			m.beginViewLoad(ViewSkills, loadRefresh)
			m.setViewStatus(ViewSkills, "checking skill sources…")
			m.skillsChecking = true
			return m, m.checkSkills(append([]agentskill.Skill(nil), m.skills...))
		}
	case listActionCycleSort:

		switch m.view {
		case ViewRepos:
			orders := []string{"activity", "latest", "name", "git", "size", "tasks"}
			m.actions.RepoSort = nextSort(m.actions.RepoSort, orders)
			m.status = "repo sort: " + m.actions.RepoSort
		case ViewTries:
			m.trySort = nextSort(m.trySort, []string{"activity", "name", "phase", "size"})
			m.status = "Try sort: " + m.trySort
		default:
			return m, nil
		}
		m.setAt(0)
		return m, nil

	case listActionReverseSort:

		switch m.view {
		case ViewRepos:
			m.actions.RepoReverse = !m.actions.RepoReverse
			m.status = fmt.Sprintf("repo sort reversed: %v", m.actions.RepoReverse)
		case ViewTries:
			m.tryReverse = !m.tryReverse
			m.status = fmt.Sprintf("Try sort reversed: %v", m.tryReverse)
		default:
			return m, nil
		}
		m.setAt(0)
		return m, nil

	}
	return m, nil
}

func (m Model) editSettings() (tea.Model, tea.Cmd) {

	if m.view == ViewSkills || m.view == ViewMCP {
		return m.runListAction(listActionOpenCapabilityFile)
	}
	if m.view == ViewFleet {
		if m.actions.EditFleetConfig == nil {
			return m, nil
		}
		proc, err := m.actions.EditFleetConfig()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.status = "editing remotes.toml…"
		return m, runExecProcess(proc, func(err error) tea.Msg {
			return fleetConfigEditedMsg{err: err}
		})
	}
	if m.actions.EditConfig == nil {
		return m, nil
	}
	proc, err := m.actions.EditConfig()
	if err != nil {
		m.err = err
		return m, nil
	}
	m.status = "editing config…"
	return m, runExecProcess(proc, func(err error) tea.Msg {
		return configEditedMsg{err: err}
	})

}
