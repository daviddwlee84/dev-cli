package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type listAction uint8

const (
	listActionOpen listAction = iota
	listActionAddNote
	listActionBrowseNotes
	listActionPark
	listActionEditNext
	listActionToggleWorktrees
	listActionRepoCreate
	listActionRepoMetadata
	listActionStartWorktree
	listActionStartDirect
	listActionCopy
	listActionStats
	listActionRemoteClone
	listActionSkillUpdate
	listActionOpenCapabilityFile
	listActionCopyCapabilityPath
	listActionCopyCapabilitySummary
	listActionCopySkillSourceURL
	listActionCopyCapabilityRaw
	listActionTryMark
	listActionTryDeprecate
	listActionTryReactivate
	listActionTryArchive
	listActionTryRestore
	listActionTryGraduate
)

type selectionToken struct {
	view View
	key  string
}

func repoItemKey(item repoItem) string {
	key := repoKey(item.Repo) + "\x00"
	if checkout, child := item.checkout(); child {
		return key + checkout.Worktree.Path
	}
	return key
}

func fleetRowKey(row FleetRow) string {
	path := ""
	if row.Repository != nil {
		path = row.Repository.Path
	}
	return row.Host + "\x00" + path
}

func skillRowKey(row agentskill.Skill) string {
	lockFile := ""
	if row.Lock != nil {
		lockFile = row.Lock.File
	}
	return strings.Join([]string{string(row.Scope), row.Checkout, row.Path, lockFile, row.Name}, "\x00")
}

func (m Model) currentSelectionToken() (selectionToken, bool) {
	switch m.view {
	case ViewTasks:
		row, ok := m.currentTask()
		if !ok || row.Task == nil {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: row.Task.ID}, true
	case ViewRepos:
		row, ok := m.currentRepoItem()
		if !ok {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: repoItemKey(row)}, true
	case ViewFleet:
		row, ok := m.currentFleet()
		if !ok {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: fleetRowKey(row)}, true
	case ViewTries:
		row, ok := m.currentTry()
		if !ok {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: row.Item.ID}, true
	case ViewRemote:
		row, ok := m.currentRemote()
		if !ok {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: remoteRowKey(row)}, true
	case ViewSkills:
		row, ok := m.currentSkill()
		if !ok {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: skillRowKey(row)}, true
	case ViewMCP:
		row, ok := m.currentMCP()
		if !ok {
			return selectionToken{}, false
		}
		return selectionToken{view: m.view, key: row.IdentityKey()}, true
	default:
		return selectionToken{}, false
	}
}

func (m *Model) selectToken(token selectionToken) bool {
	if m.view != token.view {
		return false
	}
	switch token.view {
	case ViewTasks:
		for i, row := range m.visibleTasks() {
			if row.Task != nil && row.Task.ID == token.key {
				m.setAt(i)
				return true
			}
		}
	case ViewRepos:
		for i, row := range m.visibleRepoItems() {
			if repoItemKey(row) == token.key {
				m.setAt(i)
				return true
			}
		}
	case ViewFleet:
		for i, row := range m.visibleFleet() {
			if fleetRowKey(row) == token.key {
				m.setAt(i)
				return true
			}
		}
	case ViewTries:
		for i, row := range m.visibleTries() {
			if row.Item.ID == token.key {
				m.setAt(i)
				return true
			}
		}
	case ViewRemote:
		for i, row := range m.visibleRemotes() {
			if remoteRowKey(row) == token.key {
				m.setAt(i)
				return true
			}
		}
	case ViewSkills:
		for i, row := range m.visibleSkills() {
			if skillRowKey(row) == token.key {
				m.setAt(i)
				return true
			}
		}
	case ViewMCP:
		for i, row := range m.visibleMCP() {
			if row.IdentityKey() == token.key {
				m.setAt(i)
				return true
			}
		}
	}
	return false
}

func (m Model) selectionHeading() (string, string) {
	if row, ok := m.currentTask(); ok {
		return row.Task.Title(), contract(row.Checkout)
	}
	if item, ok := m.currentRepoItem(); ok {
		if checkout, child := item.checkout(); child {
			return item.Repo.Repo.Display() + "/" + filepath.Base(checkout.Worktree.Path), contract(checkout.Worktree.Path)
		}
		return item.Repo.Repo.Display(), contract(item.Repo.Repo.Path)
	}
	if row, ok := m.currentFleet(); ok {
		if row.Repository != nil {
			return row.Host + "/" + row.Repository.Display, contract(row.Repository.Path)
		}
		return row.Host, string(row.State)
	}
	if row, ok := m.currentTry(); ok {
		return row.Item.DisplayName(), contract(row.Item.Live.CurrentPath)
	}
	if row, ok := m.currentRemote(); ok {
		detail := row.Repo.URL
		if row.LocalPath != "" {
			detail = contract(row.LocalPath)
		}
		return row.Repo.FullName, detail
	}
	if row, ok := m.currentSkill(); ok {
		path, _ := skillFilePath(row)
		return row.Name, contract(path)
	}
	if row, ok := m.currentMCP(); ok {
		return row.Name, contract(row.ConfigPath)
	}
	return "", ""
}

func (m Model) openActionMenu() Model {
	token, ok := m.currentSelectionToken()
	if !ok {
		return m
	}
	subject, detail := m.selectionHeading()
	overlay := overlayState{
		kind: overlayActionMenu, title: strings.ToUpper(m.view.String()) + " actions",
		subject: subject, detail: detail, selection: token,
	}

	switch m.view {
	case ViewTasks:
		if row, ok := m.currentTask(); ok && m.actions.Open != nil && taskOpenBlocker(row) == nil {
			overlay.addOption(listActionOpen, "open task")
		}
		m.addNoteOptions(&overlay)
		if m.actions.SetNext != nil {
			overlay.addOption(listActionEditNext, "edit next action")
		}
		if row, ok := m.currentTask(); ok && m.actions.Park != nil &&
			(row.Task.State == task.Hot || row.Task.State == task.Warm) {
			overlay.addOption(listActionPark, "park warm")
		}
		m.addStatsOption(&overlay)

	case ViewRepos:
		if m.actions.Repos.Create != nil {
			overlay.addOption(listActionRepoCreate, "new repository…")
		}
		item, _ := m.currentRepoItem()
		if item.child() {
			if checkout, ok := item.checkout(); ok && checkout.Exists && !checkout.Worktree.Prunable && m.actions.OpenCheckout != nil {
				overlay.addOption(listActionOpen, "open worktree")
			}
			overlay.addOption(listActionToggleWorktrees, "collapse worktrees")
		} else {
			if m.actions.OpenRepo != nil {
				overlay.addOption(listActionOpen, "open repository")
			}
			if item.Repo.Worktrees > 0 && item.Repo.Context.WorktreeErr == nil {
				overlay.addOption(listActionToggleWorktrees, "expand or collapse worktrees")
			}
			if m.actions.Repos.Patch != nil {
				overlay.addOption(listActionRepoMetadata, "edit repository metadata")
			}
			if m.actions.Start != nil {
				overlay.addOption(listActionStartWorktree, "start worktree task")
			}
			if m.actions.StartDirect != nil {
				overlay.addOption(listActionStartDirect, "start direct task")
			}
		}
		m.addNoteOptions(&overlay)
		if m.actions.Copy != nil {
			overlay.addOption(listActionCopy, "copy repository data…")
		}
		m.addStatsOption(&overlay)

	case ViewFleet:
		if row, ok := m.currentFleet(); ok && row.Repository != nil && m.actions.OpenFleet != nil {
			overlay.addOption(listActionOpen, "open repository on host")
		}

	case ViewTries:
		row, _ := m.currentTry()
		if m.actions.Tries.Apply != nil {
			overlay.addOption(listActionTryMark, "edit tags and note")
			switch row.Item.Phase {
			case catalog.PhaseActive:
				overlay.addOption(listActionTryDeprecate, "deprecate (metadata only)")
			case catalog.PhaseDeprecated:
				overlay.addOption(listActionTryReactivate, "reactivate")
			}
			if row.Item.Phase != catalog.PhaseGraduated {
				switch row.LocationState() {
				case catalog.LocationPresent:
					overlay.addOption(listActionTryArchive, "archive locally (reversible move)")
					overlay.addOption(listActionTryGraduate, "graduate into a project")
				case catalog.LocationArchived:
					overlay.addOption(listActionTryRestore, "restore from local archive")
					overlay.addOption(listActionTryGraduate, "graduate archived Try")
				}
			}
			if row.Present() {
				overlay.addOption(listActionOpen, "open Try")
			}
		}

	case ViewRemote:
		row, _ := m.currentRemote()
		if row.Cloned() && m.actions.OpenRemote != nil {
			overlay.addOption(listActionOpen, "open local checkout")
			m.addNoteOptions(&overlay)
		} else if !row.Cloned() && m.actions.CloneRemote != nil {
			overlay.addOption(listActionRemoteClone, "clone repository…")
		}

	case ViewSkills:
		row, _ := m.currentSkill()
		_, fileErr := skillFilePath(row)
		if fileErr == nil && m.actions.EditFile != nil {
			overlay.addOption(listActionOpenCapabilityFile, "open primary skill file")
		}
		if m.actions.Copy != nil {
			if fileErr == nil {
				overlay.addOption(listActionCopyCapabilityPath, "copy primary skill file path")
			}
			overlay.addOption(listActionCopyCapabilitySummary, "copy safe skill summary")
			if row.SourceURL != "" {
				overlay.addOption(listActionCopySkillSourceURL, "copy skill source URL")
			}
			if fileErr == nil && m.actions.ReadFile != nil {
				overlay.addOption(listActionCopyCapabilityRaw, "copy raw primary skill file")
			}
		}
		if m.actions.UpdateSkill != nil && agentskill.CanUpdate(row) {
			overlay.addOption(listActionSkillUpdate, "update selected skill…")
		}

	case ViewMCP:
		row, _ := m.currentMCP()
		if row.ConfigPath != "" && m.actions.EditFile != nil {
			overlay.addOption(listActionOpenCapabilityFile, "open MCP config")
		}
		if m.actions.Copy != nil {
			if row.ConfigPath != "" {
				overlay.addOption(listActionCopyCapabilityPath, "copy MCP config path")
			}
			overlay.addOption(listActionCopyCapabilitySummary, "copy safe declaration summary")
			if row.ConfigPath != "" && m.actions.ReadFile != nil {
				overlay.addOption(listActionCopyCapabilityRaw, "copy raw MCP config file")
			}
		}
	}

	if overlay.optionCount == 0 {
		return m
	}
	m.overlay = overlay
	m.err = nil
	return m
}

func (m Model) addNoteOptions(overlay *overlayState) {
	if _, ok := m.selectedNoteTarget(); !ok {
		return
	}
	if m.actions.Notes.Add != nil {
		overlay.addOption(listActionAddNote, "add note")
	}
	if m.actions.Notes.List != nil {
		overlay.addOption(listActionBrowseNotes, "browse notes")
	}
}

func (m Model) addStatsOption(overlay *overlayState) {
	if m.selectedRepoName() != "" && m.actions.LoadStats != nil {
		overlay.addOption(listActionStats, "open activity heatmap")
	}
}

func (m Model) runOverlayAction() (tea.Model, tea.Cmd) {
	if m.overlay.optionCount == 0 || m.overlay.optionIndex >= m.overlay.optionCount {
		return m, nil
	}
	action := m.overlay.options[m.overlay.optionIndex].action
	token := m.overlay.selection
	if !m.selectToken(token) {
		m.overlay = overlayState{}
		m.err = fmt.Errorf("selected row changed while its action menu was open")
		return m, nil
	}
	m.overlay = overlayState{}
	return m.runListAction(action)
}

func (m Model) runListAction(action listAction) (tea.Model, tea.Cmd) {
	switch action {
	case listActionOpen:
		return m, m.openSelected()
	case listActionAddNote:
		if target, ok := m.selectedNoteTarget(); ok {
			return m.openNoteAdd(target, false)
		}
	case listActionBrowseNotes:
		if target, ok := m.selectedNoteTarget(); ok {
			return m.openNotes(target)
		}
	case listActionPark:
		if row, ok := m.currentTask(); ok && (row.Task.State == task.Hot || row.Task.State == task.Warm) {
			target := *row.Task
			m.taskPromptTarget = &target
			return m.prompt(modeConfirmPark, "", "what to do when you come back")
		}
	case listActionEditNext:
		if row, ok := m.currentTask(); ok {
			target := *row.Task
			m.taskPromptTarget = &target
			return m.prompt(modeEditNext, row.Task.Next, "next action")
		}
	case listActionToggleWorktrees:
		return m.toggleSelectedRepo()
	case listActionRepoCreate:
		if m.actions.Repos.Create == nil {
			return m, nil
		}
		process, err := m.actions.Repos.Create()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.err = nil
		m.status = "opening new repository wizard…"
		return m, runExecProcess(process, func(runErr error) tea.Msg {
			return actionMsg{status: "repository wizard completed", forceSizes: true, err: runErr}
		})
	case listActionRepoMetadata:
		if row, ok := m.currentRepo(); ok {
			return m.openRepoForm(row)
		}
	case listActionStartWorktree:
		if row, ok := m.currentRepo(); ok {
			m.repoPromptTarget, m.repoPromptSet = row, true
			return m.prompt(modeStartTask, "", "name for the new worktree task")
		}
	case listActionStartDirect:
		if row, ok := m.currentRepo(); ok {
			m.repoPromptTarget, m.repoPromptSet = row, true
			return m.prompt(modeStartDirect, "", "name for direct work on current branch")
		}
	case listActionCopy:
		token, ok := m.currentSelectionToken()
		if !ok {
			return m, nil
		}
		m.copySelection = token
		m.mode, m.err, m.status = modeCopy, nil, ""
		return m, nil
	case listActionStats:
		repo := m.selectedRepoName()
		if repo != "" && m.actions.LoadStats != nil {
			m.mode, m.stats, m.status = modeStats, nil, "loading activity…"
			return m, m.loadStats(repo)
		}
	case listActionRemoteClone:
		return m.promptSelectedRemoteClone()
	case listActionSkillUpdate:
		return m.promptSelectedSkillUpdate()
	case listActionOpenCapabilityFile:
		return m.openSelectedCapabilityFile()
	case listActionCopyCapabilityPath:
		return m.copyCapabilityValue("p")
	case listActionCopyCapabilitySummary:
		return m.copyCapabilityValue("s")
	case listActionCopySkillSourceURL:
		return m.copyCapabilityValue("u")
	case listActionCopyCapabilityRaw:
		return m.copyCapabilityValue("f")
	case listActionTryMark, listActionTryDeprecate, listActionTryReactivate,
		listActionTryArchive, listActionTryRestore, listActionTryGraduate:
		return m.runTryListAction(action)
	}
	return m, nil
}

func (m Model) toggleSelectedRepo() (tea.Model, tea.Cmd) {
	item, ok := m.currentRepoItem()
	if !ok || item.Repo.Worktrees == 0 {
		return m, nil
	}
	if item.Repo.Context.WorktreeErr != nil {
		m.err = item.Repo.Context.WorktreeErr
		return m, nil
	}
	if item.child() {
		for i := m.repoCursor - 1; i >= 0; i-- {
			if !m.visibleRepoItems()[i].child() {
				m.repoCursor = i
				break
			}
		}
	}
	m.toggleRepo(item.Repo)
	return m, nil
}

func (m Model) promptSelectedRemoteClone() (tea.Model, tea.Cmd) {
	row, ok := m.currentRemote()
	if !ok || row.Cloned() {
		return m, nil
	}
	if row.CloneProblemPath != "" {
		if _, err := os.Lstat(row.CloneProblemPath); err == nil {
			m.err = fmt.Errorf("inspect or move the existing clone destination at %s before retrying", config.Contract(row.CloneProblemPath))
			return m, nil
		}
		m.setRemoteCloneProblem(row, "")
	}
	if !m.reposReadyForClone() {
		m.setViewStatus(ViewRemote, "wait for local repositories to finish loading")
		return m, nil
	}
	m.remoteClonePrompt = row
	return m.prompt(modeConfirmClone, row.Repo.FullName, "enter clone; o clone and open; esc cancel")
}

func (m Model) promptSelectedSkillUpdate() (tea.Model, tea.Cmd) {
	if m.viewLoad(ViewSkills).loading {
		m.setViewStatus(ViewSkills, "wait for the current skill reload/check to finish")
		return m, nil
	}
	row, ok := m.currentSkill()
	if !ok {
		return m, nil
	}
	if !agentskill.CanUpdate(row) {
		m.err = fmt.Errorf("%s has no update-safe provider lock", row.Name)
		return m, nil
	}
	m.skillUpdateTarget = row
	m.mode = modeConfirmSkillUpdate
	return m, nil
}

func (m Model) openSelectedCapabilityFile() (tea.Model, tea.Cmd) {
	path, err := m.capabilityFilePath()
	if err != nil {
		m.err = err
		return m, nil
	}
	if m.actions.EditFile == nil {
		m.err = fmt.Errorf("opening capability files is unavailable")
		return m, nil
	}
	edit, err := m.actions.EditFile(path)
	if err != nil {
		m.err = err
		return m, nil
	}
	if edit.Command == nil {
		m.err = fmt.Errorf("capability editor returned no process")
		return m, nil
	}
	view := m.view
	m.status = "editing " + config.Contract(path) + "…"
	return m, runExecProcess(edit.Command, func(runErr error) tea.Msg {
		if edit.Complete != nil {
			runErr = edit.Complete(runErr)
		}
		return capabilityFileEditedMsg{view: view, err: runErr}
	})
}

func (m Model) runTryListAction(action listAction) (tea.Model, tea.Cmd) {
	row, ok := m.currentTry()
	if !ok {
		return m, nil
	}
	var tryAction TryAction
	switch action {
	case listActionTryMark:
		tryAction = TryMark
	case listActionTryDeprecate:
		tryAction = TryDeprecate
	case listActionTryReactivate:
		tryAction = TryReactivate
	case listActionTryArchive:
		tryAction = TryArchive
	case listActionTryRestore:
		tryAction = TryRestore
	case listActionTryGraduate:
		tryAction = TryGraduate
	}
	switch tryAction {
	case TryMark, TryRestore, TryGraduate:
		return m.openTryForm(tryAction, row)
	case TryArchive:
		return m.openTryConfirmation(tryAction, row)
	case TryDeprecate, TryReactivate:
		m.status = string(tryAction) + " in progress…"
		return m, m.applyTry(TryRequest{Action: tryAction, ID: row.Item.ID})
	default:
		return m, nil
	}
}
