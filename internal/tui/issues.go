package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/tuiissue"
)

type IssueActions struct {
	// Inspect performs local dependency/path checks only. It never authenticates,
	// repairs, starts an agent/server, or installs a package.
	Inspect func(context.Context, View) []tuiissue.Issue
	Prepare func(context.Context, tuiissue.Issue, tuiissue.Action) (*IssueExecution, error)
}
type IssueExecution struct {
	Preview  string
	Command  tea.ExecCommand
	Complete func(error) (string, error)
}
type issueUIState struct {
	history    []tuiissue.Issue
	snapshot   []tuiissue.Issue
	all        bool
	page       int
	selected   string
	generation uint64
	execution  *IssueExecution
	origin     View
}
type issuesInspectedMsg struct {
	generation uint64
	issues     []tuiissue.Issue
}
type issuePreparedMsg struct {
	generation uint64
	execution  *IssueExecution
	err        error
}
type issueFinishedMsg struct {
	view   View
	status string
	err    error
}

func (m *Model) rememberIssue(issue tuiissue.Issue) {
	issues := append([]tuiissue.Issue(nil), m.issues.history...)
	for n := range issues {
		if issues[n].ID == issue.ID {
			issues[n] = issue
			m.issues.history = issues
			return
		}
	}
	m.issues.history = append(issues, issue)
}
func (m *Model) captureIssueResult(before Model, msg tea.Msg) {
	source := "operation"
	switch msg.(type) {
	case noteListMsg, noteActionMsg:
		source = "notes"
	case statsHistoryMsg, statsMsg, statsBackfilledMsg:
		source = "stats"
	case copyMsg:
		source = "clipboard"
	case snippetsLoadedMsg, snippetCreatedMsg:
		source = "snippets"
	case configMsg:
		source = "configuration"
	case configEditedMsg, capabilityFileEditedMsg, fleetConfigEditedMsg:
		source = "editor"
	}
	if m.err != nil && (before.err == nil || m.err.Error() != before.err.Error()) {
		m.rememberIssue(tuiissue.FromError(before.view.String(), source, before.currentToken().key, m.err))
	}
	if (m.statusSeverity == "warning" || m.statusSeverity == "error") && m.status != "" && m.status != before.status {
		issue := tuiissue.New(before.view.String(), source, before.currentToken().key, "operation-warning", m.status)
		if result, ok := msg.(workflowMsg); ok && result.result.Ledger != nil {
			issue.Detail += "\n\n" + triageReceipt(*result.result.Ledger)
		}
		m.rememberIssue(issue)
	}
	switch message := msg.(type) {
	case snippetsLoadedMsg:
		if !before.snippets.loading || message.generation != before.snippets.generation || message.query != before.snippets.query {
			break
		}
		if m.snippets.err != nil {
			m.rememberIssue(tuiissue.FromError(ViewRemote.String(), "snippets", "", m.snippets.err))
		} else if m.snippets.status != "" && !m.snippets.result.Complete && !errors.Is(message.err, context.Canceled) {
			m.rememberIssue(tuiissue.New(ViewRemote.String(), "snippets", "", "inventory-warning", m.snippets.status))
		}
	case sshEventMsg:
		if message.generation != before.sshUI.generation {
			break
		}
		source := "SSH " + message.kind
		if message.err != nil && !errors.Is(message.err, context.Canceled) {
			m.rememberIssue(tuiissue.FromError(ViewSSH.String(), source, before.currentToken().key, message.err))
		}
		if message.test.Err != nil && !errors.Is(message.test.Err, context.Canceled) {
			m.rememberIssue(tuiissue.FromError(ViewSSH.String(), "SSH test", message.test.ProfileID, message.test.Err))
		}
		for _, outcome := range message.testResult.Outcomes {
			if outcome.Err != nil && !errors.Is(outcome.Err, context.Canceled) {
				m.rememberIssue(tuiissue.FromError(ViewSSH.String(), "SSH test", outcome.ProfileID, outcome.Err))
			}
		}
		if message.discovery.CacheErr != nil {
			m.rememberIssue(tuiissue.FromError(ViewSSH.String(), "discovery cache", "", message.discovery.CacheErr))
		}
	case skillsMsg:
		if message.generation != m.viewLoad(ViewSkills).generation {
			break
		}
		m.acceptInventoryIssues(ViewSkills, message.valid, message.warning, message.issues)
		if message.warning != "" && len(message.issues) == 0 {
			m.rememberIssue(tuiissue.New(ViewSkills.String(), "inventory", "", "inventory-warning", message.warning))
		}
	case mcpMsg:
		if message.generation != m.viewLoad(ViewMCP).generation {
			break
		}
		m.acceptInventoryIssues(ViewMCP, message.valid, message.warning, message.issues)
		if message.warning != "" && len(message.issues) == 0 {
			m.rememberIssue(tuiissue.New(ViewMCP.String(), "inventory", "", "inventory-warning", message.warning))
		}
	}
}
func (m Model) collectedIssues(all bool) []tuiissue.Issue {
	out := []tuiissue.Issue{}
	seen := map[string]bool{}
	add := func(issue tuiissue.Issue) {
		if (all || issue.View == m.view.String()) && !seen[issue.ID] {
			seen[issue.ID] = true
			out = append(out, m.bindIssueTarget(issue))
		}
	}
	addErr := func(view View, source, row string, err error) {
		if err != nil {
			add(tuiissue.FromError(view.String(), source, row, err))
		}
	}
	for _, view := range Views {
		addErr(view, "inventory", "", m.viewError(view))
	}
	if m.err != nil {
		add(tuiissue.FromError(m.view.String(), "operation", m.currentToken().key, m.err))
	}
	for _, issue := range m.issues.history {
		if issue.Source != "native inventory" && issue.Code != "inventory-warning" {
			issue.Summary = "Previous " + issue.Summary
			issue.Guidance = "Recorded earlier in this dashboard session; it may already be resolved. Recheck current observations before taking another action.\n\n" + issue.Guidance
		}
		add(issue)
	}
	for _, row := range m.rows {
		if row.Task == nil {
			continue
		}
		key := row.Task.ID
		addErr(ViewTasks, "git", key, row.StatusErr)
		if blocker := taskOpenBlocker(row); blocker != nil {
			add(tuiissue.New(ViewTasks.String(), "task lifecycle", key, "checkout-unavailable", blocker.Error()))
		}
	}
	for _, row := range m.repos {
		for _, item := range []struct {
			source string
			err    error
		}{{"identity", row.Context.IdentityErr}, {"git worktrees", row.Context.WorktreeErr}, {"runtime", row.Context.RuntimeErr}, {"tasks", row.Context.TaskErr}, {"remote topology", row.TopologyErr}, {"disk usage", row.SizeError}} {
			addErr(ViewRepos, item.source, row.Repo.Path, item.err)
		}
		for _, checkout := range row.Context.Checkouts {
			addErr(ViewRepos, "checkout git", checkout.Worktree.Path, checkout.StatusErr)
		}
	}
	for _, row := range m.tries {
		addErr(ViewTries, "runtime", row.reference(), row.RuntimeErr)
		addErr(ViewTries, "git", row.reference(), row.TopologyErr)
		addErr(ViewTries, "disk usage", row.reference(), row.SizeError)
		if row.Item.Entry != nil && row.Item.Entry.MoveIntent != nil {
			add(tuiissue.New(ViewTries.String(), "unfinished move", row.reference(), "move-pending", "This Try has an unfinished filesystem transition."))
		} else if row.Item.Live.Presence == "missing" {
			add(tuiissue.New(ViewTries.String(), "Try location", row.reference(), "location-missing", "The recorded Try folder is missing."))
		}
	}
	fleetModel := m
	fleetModel.filter = ""
	for _, row := range fleetModel.visibleFleet() {
		if row.Error != "" {
			add(tuiissue.New(ViewFleet.String(), "host observation", fleetRowKey(row), "host-unavailable", row.Error))
		}
	}
	for _, row := range m.skills {
		if row.IntegrityDetail != "" {
			add(tuiissue.New(ViewSkills.String(), "skill integrity", row.Path, "integrity-"+string(row.Integrity), row.IntegrityDetail))
		}
		if row.UpdateStatus == agentskill.UpdateUnknown && row.UpdateDetail != "" {
			add(tuiissue.New(ViewSkills.String(), "skill update", row.Path, "update-unknown", row.UpdateDetail))
		}
	}
	for _, row := range m.mcp {
		for _, diagnostic := range row.Coverage {
			add(tuiissue.New(ViewMCP.String(), "declaration coverage", row.IdentityKey(), string(diagnostic.Code), diagnostic.Message))
		}
	}
	sourceNames := make([]string, 0, len(m.ssh.Sources))
	for source := range m.ssh.Sources {
		sourceNames = append(sourceNames, source)
	}
	sort.Strings(sourceNames)
	for _, source := range sourceNames {
		state := m.ssh.Sources[source]
		if state == "unavailable" || state == "failed" || state == "unknown" || state == "partial" {
			add(tuiissue.New(ViewSSH.String(), "SSH source "+source, "", "source-"+state, source+" observations are "+state+". Existing candidates remain advisory; open source details or explicit discovery to inspect this source."))
		}
	}
	for _, tool := range m.actions.Tools {
		if tool.Availability == ToolUnavailable && len(tool.Command) > 0 {
			add(tuiissue.MissingDependency(m.view.String(), tool.Command[0]))
		}
	}
	return out
}
func (m Model) addIssueOption(menu *overlayState) {
	menu.addOption(listActionIssues, "issues / suggested actions…")
}
func (m Model) openIssues(all bool) (tea.Model, tea.Cmd) {
	m.issues.all = all
	m.issues.page = 0
	m.issues.generation++
	m.issues.snapshot = m.collectedIssues(all)
	m = m.renderIssuesMenu()
	if m.actions.Issues.Inspect == nil {
		return m, nil
	}
	generation, origin := m.issues.generation, m.view
	return m, func() tea.Msg {
		issues := []tuiissue.Issue{}
		views := []View{origin}
		if all {
			views = Views
		}
		for _, view := range views {
			issues = append(issues, m.actions.Issues.Inspect(m.baseContext(), view)...)
		}
		return issuesInspectedMsg{generation: generation, issues: issues}
	}
}
func (m Model) renderIssuesMenu() Model {
	title := "Issues — " + strings.ToUpper(m.view.String())
	if m.issues.all {
		title = "Issues — all pages"
	}
	menu := overlayState{kind: overlayActionMenu, title: title}
	label := "show all pages"
	if m.issues.all {
		label = "show this page"
	}
	menu.addOption(listActionIssuesScope, label)
	if len(m.issues.snapshot) == 0 {
		menu.body = "No known issues in the current observations. Local dependency checks may still be running."
	}
	start := m.issues.page * 70
	end := min(start+70, len(m.issues.snapshot))
	if start > 0 {
		menu.addOption(listActionIssuesPrevious, "previous issues…")
	}
	for _, issue := range m.issues.snapshot[start:end] {
		menu.addOption(listActionIssueDetails, strings.ToUpper(issue.View)+" · "+issue.Summary)
		menu.options[menu.optionCount-1].issueID = issue.ID
	}
	if end < len(m.issues.snapshot) {
		menu.addOption(listActionIssuesNext, "more issues…")
	}
	m.overlay = menu
	return m
}
func (m Model) selectedIssue() (tuiissue.Issue, bool) {
	for _, issue := range m.issues.snapshot {
		if issue.ID == m.issues.selected {
			return issue, true
		}
	}
	return tuiissue.Issue{}, false
}
func issueBody(issue tuiissue.Issue) string {
	return fmt.Sprintf("%s / %s\nCode: %s\n\n%s\n\n%s", strings.ToUpper(issue.View), issue.Source, issue.Code, issue.Detail, issue.Guidance)
}
func (m Model) openIssueDetails(id string) Model {
	m.issues.selected = id
	issue, ok := m.selectedIssue()
	if !ok {
		return m
	}
	menu := overlayState{kind: overlayActionMenu, title: "Issue and suggested actions", body: issueBody(issue)}
	menu.addOption(listActionIssueFull, "read full diagnostic / guidance…")
	menu.addOption(listActionIssueCopy, "copy diagnostic and guidance")
	for _, action := range issue.Actions {
		menu.addOption(listActionIssueAction, action.Label)
		menu.options[menu.optionCount-1].issueAction = action
	}
	menu.addOption(listActionIssues, "back to issues")
	m.overlay = menu
	return m
}
func issueAction(action listAction) bool {
	return action >= listActionIssues && action <= listActionIssueConfirm
}
func (m Model) runIssueAction(action listAction, option actionOption) (tea.Model, tea.Cmd) {
	switch action {
	case listActionIssues:
		return m.openIssues(false)
	case listActionIssuesScope:
		return m.openIssues(!m.issues.all)
	case listActionIssuesNext:
		m.issues.page++
		return m.renderIssuesMenu(), nil
	case listActionIssuesPrevious:
		m.issues.page = max(0, m.issues.page-1)
		return m.renderIssuesMenu(), nil
	case listActionIssueDetails:
		return m.openIssueDetails(option.issueID), nil
	case listActionIssueFull:
		if issue, ok := m.selectedIssue(); ok {
			m.overlay = overlayState{kind: overlayTriageReceipt, title: "Issue details", body: issueBody(issue)}
		}
		return m, nil
	case listActionIssueCopy:
		if issue, ok := m.selectedIssue(); ok {
			return m.copyText(issueBody(issue), "diagnostic and guidance", false)
		}
		return m, nil
	case listActionIssueAction:
		issue, ok := m.selectedIssue()
		if !ok {
			return m, nil
		}
		selected := option.issueAction
		valid := false
		for _, allowed := range issue.Actions {
			if allowed == selected {
				valid = true
			}
		}
		if !valid {
			return m, nil
		}
		if selected.ID == tuiissue.SourceActions || selected.ID == tuiissue.TaskRecovery || selected.ID == tuiissue.TryRecovery {
			return m.runIssueSourceAction(issue, selected.ID)
		}
		if selected.ID == tuiissue.EditDiagnostic {
			if m.actions.EditFile == nil {
				return m, nil
			}
			edit, err := m.actions.EditFile(issue.Row)
			if err != nil {
				m.err = err
				return m, nil
			}
			if edit.Command == nil {
				m.err = errors.New("editor returned no process")
				return m, nil
			}
			view := ViewSkills
			if issue.View == ViewMCP.String() {
				view = ViewMCP
			}
			m.overlay = overlayState{}
			return m, runExecProcess(edit.Command, func(runErr error) tea.Msg {
				if edit.Complete != nil {
					runErr = edit.Complete(runErr)
				}
				return capabilityFileEditedMsg{view: view, err: runErr}
			})
		}
		if selected.ID == tuiissue.Recheck {
			return m.recheckIssueView(issue.View)
		}
		if m.actions.Issues.Prepare == nil {
			m.err = errors.New("this recovery action is unavailable in this dashboard session")
			return m, nil
		}
		m.issues.generation++
		generation := m.issues.generation
		m.issues.origin = m.view
		m.overlay = overlayState{kind: overlayTriageReceipt, title: "Preparing reviewed action", body: "Inspecting current prerequisites…"}
		return m, func() tea.Msg {
			execution, err := m.actions.Issues.Prepare(m.baseContext(), issue, selected)
			return issuePreparedMsg{generation: generation, execution: execution, err: err}
		}
	case listActionIssueConfirm:
		execution := m.issues.execution
		m.issues.execution = nil
		if execution == nil || execution.Command == nil {
			return m, nil
		}
		view := m.issues.origin
		m.overlay = overlayState{}
		return m, tea.Exec(execution.Command, func(err error) tea.Msg {
			status := "Returned from recovery action"
			if execution.Complete != nil {
				status, err = execution.Complete(err)
			}
			return afterExec(issueFinishedMsg{view: view, status: status, err: err})
		})
	}
	return m, nil
}
func (m Model) updateIssues(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch message := msg.(type) {
	case issuesInspectedMsg:
		if message.generation != m.issues.generation || m.overlay.title != "Issues — "+strings.ToUpper(m.view.String()) && m.overlay.title != "Issues — all pages" {
			return m, nil, true
		}
		seen := map[string]bool{}
		for _, issue := range m.issues.snapshot {
			seen[issue.ID] = true
		}
		m.issues.snapshot = append([]tuiissue.Issue(nil), m.issues.snapshot...)
		for _, issue := range message.issues {
			if !seen[issue.ID] {
				m.issues.snapshot = append(m.issues.snapshot, issue)
				seen[issue.ID] = true
			}
		}
		return m.renderIssuesMenu(), nil, true
	case issuePreparedMsg:
		if message.generation != m.issues.generation || m.overlay.title != "Preparing reviewed action" {
			return m, nil, true
		}
		m.issues.execution = message.execution
		if message.err != nil {
			m.err = message.err
			m.overlay = overlayState{kind: overlayTriageReceipt, title: "Recovery unavailable", body: message.err.Error()}
			return m, nil, true
		}
		if message.execution == nil {
			return m, nil, true
		}
		m.overlay = overlayState{kind: overlayActionMenu, title: "Review recovery action", body: message.execution.Preview}
		if message.execution.Command != nil {
			m.overlay.addOption(listActionIssueConfirm, "run this reviewed action in the foreground")
		}
		m.overlay.addOption(listActionIssues, "back to issues")
		return m, nil, true
	case issueFinishedMsg:
		m.err = message.err
		m.status = message.status
		if message.err != nil {
			m.rememberIssue(tuiissue.FromError(message.view.String(), "operation", "recovery", message.err))
		}
		next, command := m.recheckIssueView(message.view.String())
		model := next.(Model)
		model.status = message.status
		model.err = message.err
		if message.err != nil {
			model.overlay = overlayState{kind: overlayTriageReceipt, title: "Recovery result", body: message.status + "\n\n" + message.err.Error()}
		}
		return model, command, true
	}
	return m, nil, false
}
func (m Model) recheckIssueView(name string) (tea.Model, tea.Cmd) {
	view := m.view
	for _, candidate := range Views {
		if candidate.String() == name {
			view = candidate
		}
	}
	m.overlay = overlayState{}
	m.err = nil
	m.status = "Rechecking local observations; completed operations are not repeated"
	m.issues.generation++
	switch view {
	case ViewSSH:
		m.beginViewLoad(view, loadRefresh)
		return m, m.reloadSSH()
	case ViewFleet:
		if m.actions.LoadFleetCache != nil {
			return m, m.loadFleetCache()
		}
	case ViewRemote:
		if m.actions.LoadRemoteCache != nil {
			return m, m.loadRemoteCache()
		}
	case ViewSkills, ViewMCP:
		m.beginViewLoad(view, loadRefresh)
		return m, m.reloadDependentView(view)
	case ViewRepos:
		m.beginViewLoad(view, loadRefresh)
		return m, m.reloadReposOnly()
	case ViewTries:
		m.beginViewLoad(view, loadRefresh)
		return m, m.reloadTries(false)
	case ViewTasks:
		m.beginViewLoad(view, loadRefresh)
		generation, ctx := m.viewLoad(view).generation, m.viewContext(view)
		return m, func() tea.Msg {
			if m.actions.Reload == nil {
				return reloadMsg{rowsSet: true, rowsGeneration: generation, rowsErr: errors.New("task inventory is unavailable")}
			}
			rows, err := m.actions.Reload(ctx)
			return reloadMsg{rows: rows, rowsSet: true, rowsValid: snapshotValid(rows, err), rowsGeneration: generation, rowsErr: err}
		}
	}
	m.status = "Use the page's explicit refresh action to check this source. No operation was repeated."
	return m, nil
}

func (m Model) bindIssueTarget(issue tuiissue.Issue) tuiissue.Issue {
	issue.Actions = append([]tuiissue.Action(nil), issue.Actions...)
	if issue.Row == "" {
		return issue
	}
	switch issue.View {
	case ViewTasks.String():
		for _, row := range m.rows {
			if row.Task != nil && row.Task.ID == issue.Row {
				issue.TargetKey = row.Task.ID
				issue.Revision = row.Task.Revision()
				if issue.Source == "task lifecycle" && m.actions.Workflow != nil {
					issue.Actions = append(issue.Actions, tuiissue.Action{ID: tuiissue.TaskRecovery, Label: "inspect and recover this exact task…"})
				}
				break
			}
		}
	case ViewRepos.String():
		candidate := m
		candidate.filter = ""
		for _, row := range candidate.visibleRepoItems() {
			checkoutMatch := false
			for _, checkout := range row.Repo.Context.Checkouts {
				if checkout.Worktree.Path == issue.Row {
					checkoutMatch = true
					break
				}
			}
			if row.Repo.Repo.Path == issue.Row || repoItemKey(row) == issue.Row || checkoutMatch {
				issue.TargetKey = repoItemKey(row)
				break
			}
		}
	case ViewTries.String():
		for _, row := range m.tries {
			if row.reference() == issue.Row {
				issue.TargetKey = trySelectionKey(row)
				if m.actions.Workflow != nil && (issue.Code == "move-pending" || issue.Code == "location-missing") {
					issue.Actions = append(issue.Actions, tuiissue.Action{ID: tuiissue.TryRecovery, Label: "review recovery for this exact Try…"})
				}
				break
			}
		}
	case ViewFleet.String():
		issue.TargetKey = issue.Row
	case ViewRemote.String():
		for _, row := range m.remotes {
			if remoteRowKey(row) == issue.Row {
				issue.TargetKey = issue.Row
				break
			}
		}
	case ViewSkills.String():
		for _, row := range m.skills {
			if row.Path == issue.Row {
				issue.TargetKey = skillRowKey(row)
				break
			}
		}
	case ViewMCP.String():
		issue.TargetKey = issue.Row
	}
	if issue.TargetKey != "" {
		issue.Actions = append(issue.Actions, tuiissue.Action{ID: tuiissue.SourceActions, Label: "open this item's actions / source…"})
	}
	return issue
}
func (m Model) runIssueSourceAction(issue tuiissue.Issue, action tuiissue.ActionID) (tea.Model, tea.Cmd) {
	if issue.TargetKey == "" {
		return m, nil
	}
	candidate := m
	for _, view := range Views {
		if view.String() == issue.View {
			candidate.view = view
		}
	}
	candidate.filter = ""
	candidate.states = nil
	candidate.showDone = true
	candidate.showAllTries = true
	if !candidate.selectToken(selectionToken{view: candidate.view, key: issue.TargetKey, revision: issue.Revision}) {
		m.err = errors.New("issue source changed or disappeared; recheck before choosing an action")
		return m, nil
	}
	if candidate.view == ViewTasks {
		row, ok := candidate.currentTask()
		if !ok || issue.Revision == "" || row.Task.Revision() != issue.Revision {
			m.err = errors.New("task changed since this issue was shown; recheck before recovery")
			return m, nil
		}
	}
	candidate.overlay = overlayState{}
	switch action {
	case tuiissue.TaskRecovery:
		return candidate.runListAction(listActionSweep)
	case tuiissue.TryRecovery:
		return candidate.runListAction(listActionTriage)
	default:
		return candidate.openActionMenuCommand()
	}
}

func loadWarningIssues(err error) []tuiissue.Issue {
	var warning LoadWarning
	if errors.As(err, &warning) {
		return append([]tuiissue.Issue(nil), warning.Issues...)
	}
	return nil
}
func (m *Model) acceptInventoryIssues(view View, valid bool, warning string, issues []tuiissue.Issue) {
	if !valid {
		return
	}
	if warning == "" || len(issues) > 0 {
		retained := []tuiissue.Issue{}
		for _, issue := range m.issues.history {
			if issue.View != view.String() || issue.Source != "native inventory" && issue.Code != "inventory-warning" {
				retained = append(retained, issue)
			}
		}
		m.issues.history = retained
	}
	for _, issue := range issues {
		m.rememberIssue(issue)
	}
}
