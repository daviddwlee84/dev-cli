package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
)

type actionScope uint8

const (
	actionSelection actionScope = iota
	actionView
	actionFiltered
)

type actionMenu uint8

const (
	menuMain actionMenu = iota
	menuHygiene
	menuAdapter
)

// ActionContext is an observation-only projection. Predicates must not perform
// IO or mutate its snapshots. Execution still revalidates in the domain service.
// A parent repository and a linked checkout deliberately retain different tokens.
type ActionContext struct {
	model    Model
	token    selectionToken
	selected bool
	task     *inventory.Row
	repo     *repoItem
	fleet    *FleetRow
	ssh      *sshEntry
	try      *TryRow
	remote   *RemoteRow
	pr       *remoteItem
	skill    *agentskill.Skill
	mcp      *agentmcp.Declaration
}

func (m Model) actionContext() ActionContext {
	c := ActionContext{model: m}
	c.token, c.selected = m.currentSelectionToken()
	if r, ok := m.currentTask(); ok {
		c.task = &r
	}
	if r, ok := m.currentRepoItem(); ok {
		c.repo = &r
	}
	if r, ok := m.currentFleet(); ok {
		c.fleet = &r
	}
	if r, ok := m.currentSSHEntry(); ok {
		c.ssh = &r
	}
	if r, ok := m.currentTry(); ok {
		c.try = &r
	}
	if r, ok := m.currentRemote(); ok {
		c.remote = &r
	}
	if r, ok := m.currentPR(); ok {
		c.pr = &r
	}
	if r, ok := m.currentSkill(); ok {
		c.skill = &r
	}
	if r, ok := m.currentMCP(); ok {
		c.mcp = &r
	}
	return c
}

type ActionAvailability struct {
	Applicable bool
	Reason     string // applicable + nonempty means disabled, never executable
}

// ActionSpec is a built-in registration, not a command string or a plugin API.
// Registration order is menu order. A key binding and Help use this same record.
// Dynamic providers carry their exact identities in actionOption, not labels.
type ActionSpec struct {
	ID          listAction
	Views       []View
	Scope       actionScope
	Menu        actionMenu
	Label       string
	Keys        string // space-separated keys; "space" denotes the space key
	Keywords    string
	Description string
	HelpTitle   string
	Topic       string
	Title       func(ActionContext) string
	Applies     func(ActionContext) bool
	Ready       func(ActionContext) string
	Run         func(Model, actionOption) (tea.Model, tea.Cmd)
}

func (s ActionSpec) inView(view View) bool {
	for _, v := range s.Views {
		if v == view {
			return true
		}
	}
	return false
}

func (s ActionSpec) label(c ActionContext) string {
	if s.Title != nil {
		return s.Title(c)
	}
	return s.Label
}

func (s ActionSpec) availability(c ActionContext) ActionAvailability {
	if !s.inView(c.model.view) || (s.Scope == actionSelection && !c.selected) || (s.Applies != nil && !s.Applies(c)) {
		return ActionAvailability{}
	}
	a := ActionAvailability{Applicable: true}
	if s.Scope == actionFiltered && c.model.view == ViewRepos {
		for _, row := range c.model.visibleRepos() {
			if row.Pending != "" {
				a.Reason = "Waiting for fresh repository observations…"
				return a
			}
		}
	}
	if s.Scope == actionSelection && c.repo != nil && c.repo.Repo.Pending != "" {
		a.Reason = "Waiting for fresh repository observations…"
	} else if s.Ready != nil {
		a.Reason = s.Ready(c)
	}
	return a
}

var dashboardActions []ActionSpec

func init() { dashboardActions = newActionRegistry() }

func findAction(view View, id listAction) (ActionSpec, bool) {
	for _, s := range dashboardActions {
		if s.ID == id && s.inView(view) {
			return s, true
		}
	}
	return ActionSpec{}, false
}

func requires(available bool, name string) string {
	if !available {
		return name + " is unavailable in this dashboard session"
	}
	return ""
}

func (m Model) addRegisteredActions(menu *overlayState, location actionMenu) {
	c := m.actionContext()
	for _, spec := range dashboardActions {
		if spec.Menu != location {
			continue
		}
		a := spec.availability(c)
		if !a.Applicable || menu.optionCount == len(menu.options) {
			continue
		}
		menu.addOption(spec.ID, spec.label(c))
		menu.options[menu.optionCount-1].disabled = a.Reason
	}
	// Disabled actions remain discoverable without becoming the default Enter.
	for i := 0; i < menu.optionCount; i++ {
		if menu.options[i].disabled == "" {
			menu.optionIndex = i
			break
		}
	}
}

func (m Model) actionKey(key string) (tea.Model, tea.Cmd, bool) {
	for _, spec := range dashboardActions {
		if !spec.inView(m.view) {
			continue
		}
		for _, binding := range strings.Fields(spec.Keys) {
			if binding == "space" {
				binding = " "
			}
			if binding == key {
				next, cmd := m.runListAction(spec.ID)
				return next, cmd, true
			}
		}
	}
	return m, nil, false
}

func (m Model) runListAction(action listAction) (tea.Model, tea.Cmd) {
	return m.dispatchAction(actionOption{action: action})
}

func (m Model) dispatchAction(option actionOption) (tea.Model, tea.Cmd) {
	spec, ok := findAction(m.view, option.action)
	if !ok {
		m.err = fmt.Errorf("action %d is not registered for %s", option.action, m.view)
		return m, nil
	}
	a := spec.availability(m.actionContext())
	if !a.Applicable {
		return m, nil
	}
	reason := a.Reason
	if reason == "" {
		reason = option.disabled
	}
	if reason != "" {
		m.status = reason
		if m.viewLoad(m.view).loading {
			m.setViewStatus(m.view, reason)
		}
		if m.overlay.kind == overlayActionMenu {
			m.overlay.body = reason
			m.overlay.scroll = 0
		}
		return m, nil
	}
	return spec.Run(m, option)
}

func actionHelpKey(keys string) string {
	if keys == "" {
		return "Ctrl+O"
	}
	parts := strings.Fields(keys)
	for i, p := range parts {
		switch p {
		case "enter":
			parts[i] = "Enter"
		case "space":
			parts[i] = "Space"
		}
	}
	return strings.Join(parts, " / ")
}

func (m Model) registeredHelp(view View) []helpEntry {
	// Help can inspect another view without changing dashboard selection or IO.
	copy := m
	copy.view = view
	c := copy.actionContext()
	var entries []helpEntry
	for _, s := range dashboardActions {
		if !s.inView(view) || s.Label == "" || (s.Menu == menuAdapter && s.Keys == "") {
			continue
		}
		description := s.Description
		if description == "" {
			description = s.Label + ". Availability depends on the selected item and current observations."
		}
		a := s.availability(c)
		if a.Reason != "" {
			description += " " + a.Reason + "."
		}
		title := s.HelpTitle
		if title == "" {
			title = s.Label
		}
		entries = append(entries, helpEntry{ID: fmt.Sprintf("%s:action-%d", view, s.ID), Group: "This view", Key: actionHelpKey(s.Keys), Title: title, Description: description, Topic: s.Topic, View: view})
	}
	return entries
}
