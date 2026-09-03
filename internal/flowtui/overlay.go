package flowtui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

func (m Model) openRemoteMenu() (tea.Model, tea.Cmd) {
	if m.loadRequest.loading {
		m.status = "wait for the current local reload before planning remote work"
		return m, nil
	}
	row, ok := m.currentSurface()
	if !ok {
		return m, nil
	}
	choices := make([]ActionChoice, 0, row.Actions.Len())
	for _, choice := range row.Actions.Values() {
		if choice.Valid() && choice.Action() == taskflow.RefreshRemote {
			choices = append(choices, choice)
		}
	}
	if len(choices) == 0 {
		m.status = "no remote action choices are available for this surface"
		return m, nil
	}
	m.remoteChoices = NewActionList(choices...)
	m.remoteCursor = 0
	m.overlay = overlayRemoteMenu
	m.status = ""
	return m, nil
}

func (m Model) updateOverlay(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay {
	case overlayHelp:
		switch message.String() {
		case "esc", "?":
			m.overlay = m.overlayReturn
			m.overlayReturn = overlayNone
			return m, nil
		case "q", "ctrl+c":
			return m.quit()
		}
		return m, nil

	case overlayRemoteMenu:
		switch message.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "esc", "R":
			m.overlay = overlayNone
			return m, nil
		case "j", "down":
			if m.remoteCursor+1 < m.remoteChoices.Len() {
				m.remoteCursor++
			}
		case "k", "up":
			if m.remoteCursor > 0 {
				m.remoteCursor--
			}
		case "enter":
			choices := m.remoteChoices.Values()
			if m.remoteCursor >= 0 && m.remoteCursor < len(choices) {
				return m.beginPlan(choices[m.remoteCursor])
			}
		}
		return m, nil

	case overlayPlanLoading:
		switch message.String() {
		case "q", "ctrl+c":
			m.cancelPlanRead()
			return m.quit()
		case "esc":
			m.cancelPlanRead()
			m.overlay = overlayNone
			m.status = ""
		}
		return m, nil

	case overlayPlan:
		if message.String() == "ctrl+c" {
			return m.quit()
		}
		if message.String() == "esc" {
			m.confirm.Blur()
			m.overlay = overlayNone
			m.status = ""
			return m, nil
		}
		if !m.planCanApply() {
			if message.String() == "q" {
				return m.quit()
			}
			return m, nil
		}
		if m.plan.Confirmation.Kind == taskflow.ConfirmationTyped {
			if message.String() == "enter" {
				token := m.confirm.Value()
				if token != m.plan.Confirmation.Token {
					m.status = "confirmation token does not match exactly"
					return m, nil
				}
				return m.startApply(taskflow.ApproveWithToken(m.plan.PlanID, token))
			}
			var command tea.Cmd
			m.confirm, command = m.confirm.Update(message)
			return m, command
		}
		switch message.String() {
		case "q":
			return m.quit()
		case "y", "Y":
			return m.startApply(taskflow.Approve(m.plan.PlanID))
		}
		return m, nil

	case overlayResult:
		switch message.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "esc":
			m.overlay = overlayNone
			m.status = ""
			return m, nil
		case "r":
			return m.beginRepositoryLoad(false)
		case "?":
			m.overlayReturn = overlayResult
			m.overlay = overlayHelp
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

func (m Model) planCanApply() bool {
	return m.hasPlan && m.planErr == nil && m.plan.Availability == taskflow.AvailabilityReady
}

func (m Model) startApply(approval taskflow.Approval) (tea.Model, tea.Cmd) {
	if !m.planCanApply() {
		return m, nil
	}
	if err := m.plan.ValidateApproval(approval); err != nil {
		m.status = fmt.Sprintf("approval rejected: %v", err)
		return m, nil
	}
	m.applyGeneration++
	m.applyRunning = true
	m.overlay = overlayApplying
	m.status = "Apply in progress; quit and refresh will be queued"
	m.hasHandoff = false
	m.finalHandoff = taskflow.Handoff{}
	m.hasLastResult = false
	m.lastResult = taskflow.Result{}
	m.lastResultErr = nil
	return m, m.applyCmd(m.applyGeneration, m.planTarget, m.plan, approval)
}
