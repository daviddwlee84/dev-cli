package tui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/forge"
)

func remotePRActions() []ActionSpec {
	var specs []ActionSpec
	add := func(id listAction, label, keys string, applies func(ActionContext) bool, ready func(ActionContext) string) {
		specs = append(specs, ActionSpec{ID: id, Views: []View{ViewRemote}, Label: label, Keys: keys, Applies: applies, Ready: ready,
			Topic: "pull-requests", HelpTitle: label, Description: "Explicit pull request action. Provider and checkout identity are checked again by the shared service.",
			Run: func(m Model, option actionOption) (tea.Model, tea.Cmd) { return m.executeListAction(option.action) }})
	}
	repository := func(c ActionContext) bool {
		item, ok := c.model.currentRemoteItem()
		return ok && c.model.remotePRSupported(item.Repository.Repo)
	}
	loadReady := func(c ActionContext) string {
		item, _ := c.model.currentRemoteItem()
		return c.model.remotePRLoadReason(item.Repository)
	}
	add(listActionPRToggle, "expand or collapse pull requests", "space", repository, nil)
	add(listActionPRRefresh, "refresh pull requests", "", repository, loadReady)
	add(listActionPRAll, "show all open pull requests", "", repository, loadReady)
	add(listActionPRRelated, "show my requests and requested reviews", "", repository, loadReady)
	add(listActionPRMore, "load more pull requests", "", func(c ActionContext) bool {
		item, ok := c.model.currentRemoteItem()
		return ok && c.model.remotePRState(item.Repository.Repo).result.HasMore
	}, func(c ActionContext) string {
		item, _ := c.model.currentRemoteItem()
		if reason := loadReady(c); reason != "" {
			return reason
		}
		if c.model.remotePRState(item.Repository.Repo).result.NextCursor == "" {
			return "Provider did not return a continuation; refresh to retry"
		}
		return ""
	})
	pr := func(c ActionContext) bool { return c.pr != nil }
	runReady := func(c ActionContext) string { return requires(c.model.actions.PRs.Run != nil, "Pull request actions") }
	localReady := func(c ActionContext) string {
		if reason := runReady(c); reason != "" {
			return reason
		}
		if c.pr != nil && !c.pr.Repository.Cloned() && c.pr.PR.LocalPath == "" {
			return "No matched local repository; use try pull request or clone the repository"
		}
		return ""
	}
	add(listActionPRView, "view fresh pull request details", "", pr, runReady)
	add(listActionPRDiff, "preview pull request diff", "", pr, runReady)
	add(listActionPRCheckout, "open pull request worktree", "", pr, runReady)
	add(listActionPRTry, "try pull request in a new checkout", "", pr, runReady)
	add(listActionPRProvision, "provision pull request checkout…", "", pr, func(c ActionContext) string {
		if reason := runReady(c); reason != "" {
			return reason
		}
		if c.pr.PR.LocalPath == "" {
			return "Open the pull request checkout first"
		}
		return ""
	})
	open := func(c ActionContext) bool { return c.pr != nil && c.pr.PR.PR.State == forge.PRStateOpen }
	mergeReady := func(c ActionContext) string {
		if reason := runReady(c); reason != "" {
			return reason
		}
		if c.pr.PR.PR.Draft {
			return "Draft requests must be marked ready on the provider first"
		}
		return ""
	}
	add(listActionPRMerge, "review squash merge…", "", open, mergeReady)
	add(listActionPRMergeSync, "review squash merge and sync base…", "", open, func(c ActionContext) string {
		if reason := mergeReady(c); reason != "" {
			return reason
		}
		return localReady(c)
	})
	add(listActionPRSyncBase, "sync local base after merge…", "", func(c ActionContext) bool { return c.pr != nil && c.pr.PR.PR.State == forge.PRStateMerged }, localReady)
	add(listActionPRBrowser, "open pull request in browser", "", pr, runReady)
	return specs
}

func (m Model) remotePRLoadReason(repository RemoteRow) string {
	if m.remotePRState(repository.Repo).loading {
		return "Pull requests are loading"
	}
	return requires(m.actions.PRs.Load != nil, "Pull request listing")
}

func (m Model) runPRListAction(action listAction) (tea.Model, tea.Cmd) {
	if action == listActionPRToggle {
		return m.toggleRemotePRs()
	}
	item, ok := m.currentRemoteItem()
	if !ok {
		return m, nil
	}
	switch action {
	case listActionPRRefresh:
		return m.loadRemotePRs(item.Repository, m.remotePRState(item.Repository.Repo).scope, false)
	case listActionPRAll:
		return m.loadRemotePRs(item.Repository, PRScopeAll, false)
	case listActionPRRelated:
		return m.loadRemotePRs(item.Repository, PRScopeRelated, false)
	case listActionPRMore:
		return m.loadRemotePRs(item.Repository, m.remotePRState(item.Repository.Repo).scope, true)
	}
	operations := map[listAction]PRAction{listActionPRView: PRView, listActionPRDiff: PRDiff, listActionPRCheckout: PRCheckout, listActionPRTry: PRTry,
		listActionPRProvision: PRProvision, listActionPRMerge: PRMerge, listActionPRMergeSync: PRMergeSync, listActionPRSyncBase: PRSyncBase, listActionPRBrowser: PRBrowser}
	if operation, ok := operations[action]; ok {
		return m.runRemotePRAction(operation)
	}
	return m, nil
}

func (m Model) updateRemotePR(message tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := message.(type) {
	case remotePRLoadedMsg:
		next, command := m.acceptRemotePRs(msg)
		return next, command, true
	case remotePRActionMsg:
		if index := m.remotePRIndex(msg.key); index >= 0 {
			state := m.mutableRemotePR(msg.request.Repository)
			state.stale = true
			state.result.Rows = append([]PRRow(nil), state.result.Rows...)
			for i := range state.result.Rows {
				state.result.Rows[i].Stale = true
			}
			if patch := msg.result.PR; patch != nil && patch.PR.Key() == msg.request.Row.PR.Key() && remotePRBelongsTo(*patch, msg.request.Repository) {
				fresh := *patch
				if !fresh.Stale && fresh.ObservedAt.IsZero() {
					fresh.ObservedAt = time.Now()
				}
				for i := range state.result.Rows {
					if state.result.Rows[i].PR.Key() == patch.PR.Key() {
						state.result.Rows[i] = fresh
					}
				}
			}
		}
		next, command := m.update(workflowMsg{result: msg.result, err: msg.err})
		return next, command, true
	case ghDashProbeMsg:
		if msg.generation == m.configGeneration {
			m.ghDashAvailability = msg.availability
		}
		return m, nil, true
	case ghDashFinishedMsg:
		m.cancelRemotePRs(false)
		next, command := m.update(workflowMsg{result: WorkflowResult{Status: "returned from gh-dash"}, err: msg.err})
		return next, command, true
	}
	return m, nil, false
}

// PRScope is an explicit repository-scoped request, separate from local / filtering.
type PRScope string

const (
	PRScopeAuto      PRScope = "auto"
	PRScopeAll       PRScope = "all"
	PRScopeRelated   PRScope = "related"
	remotePRPageSize         = 50
	remotePRCacheTTL         = 5 * time.Minute
)

type PRQuery struct {
	Repository forge.RemoteRepo
	Locals     []RepoRow
	Scope      PRScope
	Cursor     string
	Limit      int
}

// PRRow contains display observations, never authority to mutate a request.
// Run must resolve and revalidate the selected request in the shared service.
type PRRow struct {
	PR                                 forge.PullRequest
	HeadOID                            string
	LocalPath                          string
	Readiness                          string
	ObservedAt                         time.Time
	Stale                              bool
	Additions, Deletions, ChangedFiles *int
}

type PRResult struct {
	Rows            []PRRow
	Scope           PRScope
	Total           *int
	TotalLowerBound bool
	HasMore         bool
	NextCursor      string
	ObservedAt      time.Time
	Warning         string
}

type PRAction string

const (
	PRView      PRAction = "view"
	PRDiff      PRAction = "diff"
	PRCheckout  PRAction = "checkout"
	PRTry       PRAction = "try"
	PRProvision PRAction = "provision"
	PRMerge     PRAction = "merge"
	PRMergeSync PRAction = "merge-sync"
	PRSyncBase  PRAction = "sync-base"
	PRBrowser   PRAction = "browser"
)

type PRActionRequest struct {
	Action     PRAction
	Repository forge.RemoteRepo
	Row        PRRow
}

type PRActions struct {
	Load               func(context.Context, PRQuery) (PRResult, error)
	Run                func(context.Context, PRActionRequest) (Workflow, error)
	PageSize           int
	LargeRepoThreshold int
	TTL                time.Duration
}

type remotePRState struct {
	key                              string
	expanded, loading, loaded, stale bool
	scope                            PRScope
	result                           PRResult
	err                              error
	request                          uint64
	cancel                           context.CancelFunc
}

type remotePRTreeState struct {
	repositories     []remotePRState
	nextRequest      uint64
	searchQuery      string
	searchExpansions []fleetSearchExpansion
}

type remotePRLoadedMsg struct {
	key        string
	request    uint64
	appendPage bool
	result     PRResult
	err        error
}

type remotePRActionMsg struct {
	key     string
	request PRActionRequest
	result  WorkflowResult
	err     error
}

func (m Model) remotePRPageLimit() int {
	if m.actions.PRs.PageSize > 0 {
		return m.actions.PRs.PageSize
	}
	return remotePRPageSize
}

func (m Model) remotePRThreshold() int {
	if m.actions.PRs.LargeRepoThreshold > 0 {
		return m.actions.PRs.LargeRepoThreshold
	}
	return 50
}

type remoteItem struct {
	Repository RemoteRow
	PR         *PRRow
	more       bool
	last       bool
	expanded   bool
}

func remotePRRepositoryKey(repository forge.RemoteRepo) string {
	if repository.Forge != forge.GitHub && repository.Forge != forge.GitLab {
		return ""
	}
	web, ok := forge.DeriveWebURL(forge.WebURLRequest{Exact: &repository})
	if !ok {
		return ""
	}
	parsed, err := url.Parse(web.URL)
	if err != nil {
		return ""
	}
	return string(repository.Forge) + "/" + strings.ToLower(parsed.Host) + "/" + strings.ToLower(repository.FullName)
}

func remoteItemKey(item remoteItem) string {
	if item.PR != nil {
		return remotePRRepositoryKey(item.Repository.Repo) + "\x00pr:" + item.PR.PR.Key()
	}
	if item.more {
		return remotePRRepositoryKey(item.Repository.Repo) + "\x00more"
	}
	return remoteRowKey(item.Repository)
}

func (m Model) remotePRIndex(key string) int {
	for index, state := range m.remotePRs.repositories {
		if state.key == key {
			return index
		}
	}
	return -1
}

func (m Model) remotePRState(repository forge.RemoteRepo) remotePRState {
	if index := m.remotePRIndex(remotePRRepositoryKey(repository)); index >= 0 {
		return m.remotePRs.repositories[index]
	}
	return remotePRState{scope: PRScopeAuto}
}

func (m *Model) mutableRemotePR(repository forge.RemoteRepo) *remotePRState {
	m.remotePRs.repositories = append([]remotePRState(nil), m.remotePRs.repositories...)
	key := remotePRRepositoryKey(repository)
	index := m.remotePRIndex(key)
	if index < 0 {
		m.remotePRs.repositories = append(m.remotePRs.repositories, remotePRState{key: key, scope: PRScopeAuto})
		index = len(m.remotePRs.repositories) - 1
	}
	return &m.remotePRs.repositories[index]
}

func (m Model) remotePRSupported(repository forge.RemoteRepo) bool {
	return m.actions.PRs.Load != nil && remotePRRepositoryKey(repository) != ""
}

func (m Model) visibleRemoteItems() []remoteItem {
	// Parent sorting remains the existing REMOTE contract. A PR never sorts
	// outside its repository, and filtering cannot issue provider requests.
	unfiltered := m
	unfiltered.filter = ""
	unfiltered.view, unfiltered.snippets.enabled = ViewRemote, false
	repositories := unfiltered.visibleRemotes()
	var items []remoteItem
	for _, repository := range repositories {
		state := m.remotePRState(repository.Repo)
		parentMatch := repository.matches(m.filter)
		var children []PRRow
		for _, row := range state.result.Rows {
			if parentMatch || matches(strings.ToLower(strings.Join([]string{row.PR.Title, row.PR.Author, row.PR.HeadBranch, row.PR.BaseBranch, fmt.Sprint(row.PR.Number), row.PR.Checks, row.PR.ReviewDecision}, " ")), m.filter) {
				children = append(children, row)
			}
		}
		if !parentMatch && len(children) == 0 {
			continue
		}
		expanded := state.expanded || (m.filter != "" && len(children) > 0)
		if m.filter != "" && m.remotePRs.searchQuery == m.filter {
			for _, override := range m.remotePRs.searchExpansions {
				if override.key == remotePRRepositoryKey(repository.Repo) {
					expanded = override.expanded
					break
				}
			}
		}
		items = append(items, remoteItem{Repository: repository, expanded: expanded})
		if !expanded {
			continue
		}
		for _, row := range children {
			row := row
			items = append(items, remoteItem{Repository: repository, PR: &row})
		}
		if state.result.HasMore {
			items = append(items, remoteItem{Repository: repository, more: true})
		}
		if len(items) > 0 {
			items[len(items)-1].last = true
		}
	}
	return items
}

func (m Model) currentRemoteItem() (remoteItem, bool) {
	if m.view != ViewRemote || m.snippets.enabled {
		return remoteItem{}, false
	}
	items := m.visibleRemoteItems()
	if m.remoteCursor < 0 || m.remoteCursor >= len(items) {
		return remoteItem{}, false
	}
	return items[m.remoteCursor], true
}

func (m Model) currentPR() (remoteItem, bool) {
	item, ok := m.currentRemoteItem()
	return item, ok && item.PR != nil
}

func (m Model) toggleRemotePRs() (tea.Model, tea.Cmd) {
	item, ok := m.currentRemoteItem()
	if !ok || !m.remotePRSupported(item.Repository.Repo) {
		return m, nil
	}
	state := m.mutableRemotePR(item.Repository.Repo)
	expanded := !item.expanded
	if item.PR != nil || item.more {
		expanded = false
	}
	if m.filter != "" {
		if m.remotePRs.searchQuery != m.filter {
			m.remotePRs.searchExpansions = nil
			m.remotePRs.searchQuery = m.filter
		}
		m.remotePRs.searchExpansions = append([]fleetSearchExpansion(nil), m.remotePRs.searchExpansions...)
		found := false
		for index := range m.remotePRs.searchExpansions {
			if m.remotePRs.searchExpansions[index].key == state.key {
				m.remotePRs.searchExpansions[index].expanded = expanded
				found = true
				break
			}
		}
		if !found {
			m.remotePRs.searchExpansions = append(m.remotePRs.searchExpansions, fleetSearchExpansion{key: state.key, expanded: expanded})
		}
	} else {
		state.expanded = expanded
	}
	if item.PR != nil || item.more {
		m.selectRemoteKey(remoteRowKey(item.Repository))
		return m, nil
	}
	if !expanded || state.loading || state.loaded {
		return m, nil
	}
	return m.loadRemotePRs(item.Repository, state.scope, false)
}

func (m Model) loadRemotePRs(repository RemoteRow, scope PRScope, more bool) (tea.Model, tea.Cmd) {
	if !m.remotePRSupported(repository.Repo) {
		return m, nil
	}
	state := m.mutableRemotePR(repository.Repo)
	if more && (state.loading || !state.result.HasMore || state.result.NextCursor == "") {
		return m, nil
	}
	if state.cancel != nil {
		state.cancel()
	}
	query := PRQuery{Repository: repository.Repo, Scope: scope, Limit: m.remotePRPageLimit(), Locals: append([]RepoRow(nil), m.repos...)}
	if more {
		query.Scope, query.Cursor = state.result.Scope, state.result.NextCursor
	}
	m.remotePRs.nextRequest++
	state.request = m.remotePRs.nextRequest
	state.loading, state.expanded, state.err = true, true, nil
	if !more {
		state.scope = scope
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	state.cancel = cancel
	key, request, load := state.key, state.request, m.actions.PRs.Load
	return m, func() tea.Msg {
		result, err := load(ctx, query)
		return remotePRLoadedMsg{key: key, request: request, appendPage: more, result: result, err: err}
	}
}

func (m Model) acceptRemotePRs(message remotePRLoadedMsg) (tea.Model, tea.Cmd) {
	index := m.remotePRIndex(message.key)
	if index < 0 {
		return m, nil
	}
	state := m.remotePRs.repositories[index]
	if !state.loading || state.request != message.request {
		return m, nil
	}
	var repository RemoteRow
	found := false
	for _, row := range m.remotes {
		if remotePRRepositoryKey(row.Repo) == message.key {
			repository, found = row, true
			break
		}
	}
	if !found {
		return m, nil
	}
	focus := m.selectedRemoteKey()
	updated := m.mutableRemotePR(repository.Repo)
	previousObservedAt, previousStale := updated.result.ObservedAt, updated.stale
	if updated.cancel != nil {
		updated.cancel()
	}
	updated.loading, updated.cancel, updated.err = false, nil, message.err
	if message.err != nil {
		updated.err = fmt.Errorf("%s", snippetDisplayText(message.err.Error()))
		if updated.loaded && !message.appendPage {
			updated.stale = true
			return m, nil
		}
	}
	if message.result.Rows != nil || message.err == nil {
		rows := make([]PRRow, 0, len(message.result.Rows))
		for _, row := range message.result.Rows {
			if !remotePRBelongsTo(row, repository.Repo) {
				updated.err = fmt.Errorf("provider returned a pull request from a different repository")
				updated.stale = true
				return m, nil
			}
			row.PR.Roles = append([]forge.PRRole(nil), row.PR.Roles...)
			rows = append(rows, row)
		}
		if message.appendPage {
			rows = append(append([]PRRow(nil), updated.result.Rows...), rows...)
		}
		seen := map[string]int{}
		unique := make([]PRRow, 0, len(rows))
		for _, row := range rows {
			if index, exists := seen[row.PR.Key()]; exists {
				if message.appendPage {
					row.PR.Roles = forge.MergePR(unique[index].PR, row.PR).Roles
				}
				unique[index] = row
				continue
			}
			seen[row.PR.Key()] = len(unique)
			unique = append(unique, row)
		}
		sort.SliceStable(unique, func(i, j int) bool {
			if !unique[i].PR.UpdatedAt.Equal(unique[j].PR.UpdatedAt) {
				return unique[i].PR.UpdatedAt.After(unique[j].PR.UpdatedAt)
			}
			return unique[i].PR.Number > unique[j].PR.Number
		})
		updated.result = message.result
		updated.result.Rows = unique
		if updated.result.Scope != PRScopeAll && updated.result.Scope != PRScopeRelated {
			updated.result.Scope = PRScopeAll
		}
		if updated.result.ObservedAt.IsZero() {
			updated.result.ObservedAt = time.Now()
		}
		if message.appendPage && !previousObservedAt.IsZero() && previousObservedAt.Before(updated.result.ObservedAt) {
			updated.result.ObservedAt = previousObservedAt
		}
		if updated.result.HasMore && updated.result.NextCursor == "" {
			updated.result.Warning = "More requests exist; refresh or use the CLI to continue."
		}
		updated.loaded = true
	}
	updated.stale = message.err != nil || (message.appendPage && previousStale)
	m.selectRemoteKey(focus)
	m.setAt(m.at())
	return m, nil
}

func remotePRBelongsTo(row PRRow, repository forge.RemoteRepo) bool {
	web, ok := forge.DeriveWebURL(forge.WebURLRequest{Exact: &repository})
	if !ok {
		return false
	}
	parsed, err := url.Parse(web.URL)
	ref, referenceErr := forge.ParsePRReference(row.PR.URL)
	return err == nil && referenceErr == nil && ref.Number == row.PR.Number && ref.Forge == row.PR.Forge && strings.EqualFold(ref.Host, row.PR.Host) && strings.EqualFold(ref.Repo, row.PR.Repo) && row.PR.Number > 0 && row.PR.Forge == repository.Forge &&
		strings.EqualFold(row.PR.Repo, repository.FullName) && strings.EqualFold(row.PR.Host, parsed.Host)
}

func (m *Model) cancelRemotePRs(clear bool) {
	m.remotePRs.repositories = append([]remotePRState(nil), m.remotePRs.repositories...)
	for index := range m.remotePRs.repositories {
		state := &m.remotePRs.repositories[index]
		if state.cancel != nil {
			state.cancel()
		}
		state.loading, state.cancel, state.stale = false, nil, true
		state.result.Rows = append([]PRRow(nil), state.result.Rows...)
		for index := range state.result.Rows {
			state.result.Rows[index].Stale = true
		}
	}
	if clear {
		m.remotePRs.repositories = nil
		m.remotePRs.searchQuery, m.remotePRs.searchExpansions = "", nil
	}
}

func (m Model) runRemotePRAction(action PRAction) (tea.Model, tea.Cmd) {
	item, ok := m.currentPR()
	if !ok || m.actions.PRs.Run == nil {
		return m, nil
	}
	request := PRActionRequest{Action: action, Repository: item.Repository.Repo, Row: *item.PR}
	workflow, err := m.actions.PRs.Run(m.baseContext(), request)
	if err != nil {
		m.err = err
		return m, nil
	}
	if workflow == nil {
		m.err = fmt.Errorf("pull request action is unavailable")
		return m, nil
	}
	state := m.mutableRemotePR(item.Repository.Repo)
	if state.cancel != nil {
		state.cancel()
	}
	state.loading, state.cancel, state.stale = false, nil, true
	key := remotePRRepositoryKey(item.Repository.Repo)
	m.err, m.status = nil, "opening pull request "+string(action)+"…"
	return m, tea.Exec(workflow, func(err error) tea.Msg {
		return afterExec(remotePRActionMsg{key: key, request: request, result: workflow.Result(), err: err})
	})
}

func remotePRScopeLabel(scope PRScope) string {
	if scope == PRScopeRelated {
		return "Related to me (author / reviewer)"
	}
	return "All open"
}

func (m Model) remotePRStale(state remotePRState) bool {
	ttl := m.actions.PRs.TTL
	if ttl <= 0 {
		ttl = remotePRCacheTTL
	}
	return state.stale || (state.loaded && time.Since(state.result.ObservedAt) > ttl)
}

func (m Model) remotePRDetailFresh(row PRRow) bool {
	ttl := m.actions.PRs.TTL
	if ttl <= 0 {
		ttl = remotePRCacheTTL
	}
	return !row.Stale && !row.ObservedAt.IsZero() && time.Since(row.ObservedAt) <= ttl
}

func remotePRStateLabel(row PRRow) string {
	if row.PR.State == forge.PRStateMerged {
		return "merged"
	}
	if row.PR.State == forge.PRStateClosed {
		return "closed"
	}
	if row.PR.Draft {
		return "draft"
	}
	if row.PR.Checks == forge.ChecksFailing {
		return "checks fail"
	}
	if row.PR.Checks == forge.ChecksPending {
		return "checks pending"
	}
	if row.PR.Mergeable == "conflicting" || row.PR.Mergeable == "cannot_be_merged" {
		return "conflicts"
	}
	if row.PR.ReviewDecision == "changes_requested" {
		return "changes requested"
	}
	if row.Readiness != "" {
		return row.Readiness
	}
	if row.PR.Checks == forge.ChecksPassing {
		return "checks pass"
	}
	return "open"
}

func remotePRRelationship(row PRRow) string {
	var labels []string
	for _, role := range row.PR.Roles {
		switch role {
		case forge.RoleAuthor:
			labels = append(labels, "mine")
		case forge.RoleReviewer:
			labels = append(labels, "review requested")
		}
	}
	return strings.Join(labels, " · ")
}

func (m Model) renderRemotePRDetail(item remoteItem) string {
	state := m.remotePRState(item.Repository.Repo)
	if item.more {
		return "  " + styleDim.Render(fmt.Sprintf("Load the next %d requests explicitly. / searches loaded requests only.", m.remotePRPageLimit())) + "\n"
	}
	row := item.PR
	lines := []string{"  " + snippetDisplayText(row.PR.URL), "  " + snippetDisplayText(row.PR.HeadRepo+":"+row.PR.HeadBranch+" → "+row.PR.BaseBranch)}
	status := remotePRStateLabel(*row)
	if row.Readiness != "" && !m.remotePRDetailFresh(*row) {
		status += " · stale observation"
	}
	if related := remotePRRelationship(*row); related != "" {
		status += " · " + related
	}
	if row.ChangedFiles != nil {
		status += fmt.Sprintf(" · %d files", *row.ChangedFiles)
	}
	if row.Additions != nil && row.Deletions != nil {
		status += fmt.Sprintf(" · +%d −%d", *row.Additions, *row.Deletions)
	}
	lines = append(lines, "  "+snippetDisplayText(status))
	if !row.ObservedAt.IsZero() {
		lines = append(lines, "  details observed "+row.ObservedAt.Local().Format("2006-01-02 15:04"))
	}
	if row.LocalPath != "" {
		lines = append(lines, "  local "+contract(row.LocalPath))
	}
	lines = append(lines, "  "+m.remotePRSummary(state))
	for index := range lines {
		lines[index] = fitCell(lines[index], max(1, m.width-2))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) remotePRSummary(state remotePRState) string {
	if state.loading && !state.loaded {
		return "Loading pull requests…"
	}
	if !state.loaded {
		if state.err != nil {
			return "PRs unavailable: " + snippetDisplayText(state.err.Error()) + " · actions → refresh PRs"
		}
		return "Space loads pull requests · / searches loaded rows only"
	}
	text := remotePRScopeLabel(state.result.Scope) + fmt.Sprintf(" · %d loaded", len(state.result.Rows))
	if state.result.Total != nil {
		prefix := ""
		if state.result.TotalLowerBound {
			prefix = "≥"
		}
		text += fmt.Sprintf(" · %s%d open overall", prefix, *state.result.Total)
	}
	if state.scope == PRScopeAuto && state.result.Scope == PRScopeRelated {
		text += fmt.Sprintf(" · auto: >%d open · actions → show all", m.remotePRThreshold())
	}
	if state.result.HasMore {
		text += " · more available"
	}
	if state.loading {
		text += " · loading…"
	} else if m.remotePRStale(state) {
		text += " · stale; actions → refresh PRs"
	}
	if state.err != nil {
		text += " · " + snippetDisplayText(state.err.Error())
	}
	if state.result.Warning != "" {
		text += " · " + snippetDisplayText(state.result.Warning)
	}
	return text
}
