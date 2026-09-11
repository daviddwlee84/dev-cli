package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// StartupRepository is a local observation, independent of list ordering.
type StartupRepository struct {
	Path, CommonDir string
	Covered         bool
	CoverageErr     error
}

type DiscoveryActions struct {
	Read  func(context.Context) (StartupRepository, error)
	Plan  func(context.Context, StartupRepository, repo.DiscoveryScope) (repo.DiscoveryRegistrationPlan, error)
	Apply func(context.Context, repo.DiscoveryRegistrationPlan) (string, error)
}

type startupRepoState struct {
	savedReposGeneration               uint64
	repository                         StartupRepository
	loaded, focusDone, applying, saved bool
	refocusAfterSave                   bool
	err                                error
	sequence                           uint64
	plan                               repo.DiscoveryRegistrationPlan
}

type startupRepoMsg struct {
	generation uint64
	repository StartupRepository
	err        error
}
type registrationPlanMsg struct {
	sequence uint64
	plan     repo.DiscoveryRegistrationPlan
	err      error
}
type registrationAppliedMsg struct {
	status string
	err    error
}

func (m Model) readStartupRepo() tea.Cmd {
	if m.actions.Discovery.Read == nil {
		return nil
	}
	return func() tea.Msg {
		r, err := m.actions.Discovery.Read(m.baseContext())
		return startupRepoMsg{generation: m.configGeneration, repository: r, err: err}
	}
}

func (m *Model) stopStartupFocus() {
	if m.view == ViewRepos {
		m.startupRepo.focusDone = true
		m.startupRepo.refocusAfterSave = false
	}
}

func (m *Model) focusStartupRepo() {
	s := &m.startupRepo
	if m.view != ViewRepos || s.focusDone || !s.loaded || m.mode != modeList || m.overlay.kind != overlayNone {
		return
	}
	if s.err != nil || s.repository.CommonDir == "" {
		s.focusDone = true
		return
	}
	for i, item := range m.visibleRepoItems() {
		if !item.child() && item.Repo.Repo.CommonDir == s.repository.CommonDir {
			m.repoCursor = i
			s.focusDone = true
			return
		}
	}
	if state := m.viewLoad(ViewRepos); state.hasSnapshot && !state.loading {
		s.focusDone = true
	}
}

func (m Model) startupOutside() bool {
	s := m.startupRepo
	state := m.viewLoad(ViewRepos)
	if !s.loaded || s.err != nil || s.repository.Path == "" || s.repository.CommonDir == "" ||
		s.repository.Covered || s.repository.CoverageErr != nil || s.saved ||
		!state.hasSnapshot || state.loading || m.viewError(ViewRepos) != nil {
		return false
	}
	// TRY rows share local inventory but must not be re-enrolled as REPOS.
	for _, row := range m.repos {
		if row.Repo.CommonDir == s.repository.CommonDir {
			return false
		}
	}
	return true
}

type discoveryButton struct {
	line, from, to int
	action         listAction
}
type discoveryBanner struct {
	text    string
	lines   int
	buttons []discoveryButton
}

func (m Model) discoveryBanner() discoveryBanner {
	var b discoveryBanner
	if m.view != ViewRepos || !m.startupOutside() {
		return b
	}
	lines := []string{
		fmt.Sprintf("Outside discovery: %q", config.Contract(m.startupRepo.repository.Path)),
		"[ Add this repo… ]",
		"[ Scan parent directory… ]",
	}
	for i, line := range lines {
		rendered := fitCell(line, max(1, m.width-2))
		b.text += "  " + rendered + "\n"
		if i > 0 {
			action := listActionRegisterRepo
			if i == 2 {
				action = listActionRegisterParent
			}
			b.buttons = append(b.buttons, discoveryButton{line: i, from: 2, to: min(m.width, 2+lipgloss.Width(line)), action: action})
		}
	}
	b.lines = len(lines)
	return b
}

func (m *Model) addDiscoveryOptions(o *overlayState) {
	if m.view == ViewRepos && m.startupOutside() && m.actions.Discovery.Plan != nil {
		o.addOption(listActionRegisterRepo, "add startup repository to repo_paths…")
		o.addOption(listActionRegisterParent, "add startup repository's parent to scan_roots…")
	}
}

func discoveryAction(action listAction) bool {
	return action >= listActionRegisterRepo && action <= listActionRegistrationEdit
}

func (m Model) beginRegistration(scope repo.DiscoveryScope) (tea.Model, tea.Cmd) {
	if !m.startupOutside() || m.actions.Discovery.Plan == nil || m.remoteClone.active() {
		return m, nil
	}
	m.stopStartupFocus()
	m.startupRepo.sequence++
	id := m.startupRepo.sequence
	m.startupRepo.plan = repo.DiscoveryRegistrationPlan{}
	m.overlay = overlayState{kind: overlayActionMenu, title: "discovery configuration", subject: "Preparing preview…", registration: id}
	m.overlay.addOption(listActionRegistrationCancel, "cancel")
	return m, func() tea.Msg {
		p, err := m.actions.Discovery.Plan(m.baseContext(), m.startupRepo.repository, scope)
		return registrationPlanMsg{sequence: id, plan: p, err: err}
	}
}

func (m Model) acceptRegistrationPlan(msg registrationPlanMsg) (tea.Model, tea.Cmd) {
	if m.overlay.registration != msg.sequence || m.overlay.kind != overlayActionMenu {
		return m, nil
	}
	if msg.err != nil {
		m.overlay = overlayState{}
		m.err = msg.err
		return m, nil
	}
	m.startupRepo.plan = msg.plan
	p := msg.plan
	body := fmt.Sprintf("Config: %q\nRepository: %q\n", p.File, p.RepoPath)
	if p.Before != "" {
		body += "Before:\n" + p.Before + "\n"
	}
	body += "After:\n" + p.After
	if p.Field == string(repo.DiscoveryParent) {
		body += "\nScans the parent and sibling projects using the normal depth-3 discovery rules."
	} else {
		body += "\nAdds only this repository to discovery."
	}
	o := overlayState{kind: overlayActionMenu, title: "review discovery configuration", subject: "Review the path before saving", body: body, registration: msg.sequence}
	if p.Blocked != "" {
		o.body += "\nManual edit required: " + p.Blocked + "\nPreserve existing entries when adding the path."
		if m.actions.EditConfig != nil {
			o.addOption(listActionRegistrationEdit, "open configuration in editor")
		}
	} else if m.actions.Discovery.Apply != nil {
		o.addOption(listActionRegistrationConfirm, "confirm and save")
	}
	o.addOption(listActionRegistrationCancel, "cancel")
	m.overlay = o
	return m, nil
}

func (m Model) confirmRegistration() (tea.Model, tea.Cmd) {
	if m.startupRepo.applying || m.actions.Discovery.Apply == nil {
		return m, nil
	}
	p := m.startupRepo.plan
	m.startupRepo.applying = true
	m.overlay = overlayState{kind: overlayActionMenu, title: "discovery configuration", subject: "Saving configuration…"}
	return m, func() tea.Msg {
		status, err := m.actions.Discovery.Apply(m.baseContext(), p)
		return registrationAppliedMsg{status: status, err: err}
	}
}

func (m Model) registrationApplied(msg registrationAppliedMsg) (tea.Model, tea.Cmd) {
	m.startupRepo.applying = false
	m.overlay = overlayState{}
	if msg.err != nil {
		m.err = fmt.Errorf("configuration update: %w", msg.err)
		m.status = msg.status
		return m, nil
	}
	m.startupRepo.saved = true
	m.startupRepo.refocusAfterSave = true
	m.status = strings.TrimSpace(msg.status + " — reloading local repositories…")
	m.beginConfigLoad()
	return m, m.reloadConfig(false)
}

func (m *Model) finishRegistrationReload() {
	s := &m.startupRepo
	state := m.viewLoad(ViewRepos)
	if s.savedReposGeneration == 0 || state.generation != s.savedReposGeneration || state.loading {
		return
	}
	s.savedReposGeneration = 0
	if state.outcome == perftrace.OutcomeSuccess && state.hasSnapshot {
		s.saved = false
		m.status = "Configuration saved; local repositories refreshed"
		return
	}
	reason := m.viewError(ViewRepos)
	if reason == nil {
		reason = fmt.Errorf("repository inventory is incomplete")
	}
	m.err = fmt.Errorf("configuration saved, but repository refresh failed (press r to retry): %w", reason)
}
