package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func (m Model) openSkillManagement(action listAction, operation string) (tea.Model, tea.Cmd) {
	request := WorkflowRequest{Action: "skills-manage", LocalGeneration: m.localGeneration, ShowAllTries: m.showAllTries, SkillAction: operation}
	if action == listActionSkillGlobal {
		request.SkillScope = "global"
		return m.runWorkflow(request)
	}
	if m.view == ViewSkills {
		row, ok := m.currentSkill()
		if !ok {
			return m, nil
		}
		if action == listActionSkillManage {
			copy := row
			request.SkillSelected = &copy
		}
		if row.Scope == agentskill.ScopeGlobal {
			request.SkillScope = "global"
		} else {
			request.RepoRefs = []string{row.ScopeRoot}
			request.SkillScope = "project"
		}
		if action == listActionSkillVisible {
			request.SkillScope = "both"
		}
	} else if m.view == ViewRepos {
		rows := m.visibleRepos()
		if action != listActionSkillFiltered {
			if row, ok := m.currentRepo(); ok {
				rows = []RepoRow{row}
			} else {
				return m, nil
			}
		}
		for _, row := range rows {
			if row.Pending != "" {
				m.status = "Wait for repository refresh before managing this scope"
				return m, nil
			}
			request.RepoRefs = append(request.RepoRefs, row.Repo.Path)
			request.Snapshots = append(request.Snapshots, triage.RepositorySnapshot{Repo: row.Repo, Context: row.Context, Topology: row.Topology, TopologyErr: row.TopologyErr, Asset: row.Asset, ObservedAt: row.ObservedAt})
		}
		request.SkillScope = "project"
		if action == listActionSkillFiltered {
			request.SkillScope = "repos"
		}
	}
	return m.runWorkflow(request)
}
