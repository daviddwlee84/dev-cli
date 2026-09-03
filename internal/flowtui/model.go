package flowtui

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type screenKind uint8

const (
	screenPicker screenKind = iota
	screenRepository
)

type overlayKind uint8

const (
	overlayNone overlayKind = iota
	overlayHelp
	overlayRemoteMenu
	overlayPlanLoading
	overlayPlan
	overlayApplying
	overlayResult
)

// FocusPane identifies the highlighted repository panel.
type FocusPane uint8

const (
	FocusSurfaces FocusPane = iota
	FocusLifecycle
	FocusActions
)

func (p FocusPane) String() string {
	switch p {
	case FocusLifecycle:
		return "lifecycle/evidence"
	case FocusActions:
		return "actions/condition"
	default:
		return "surfaces"
	}
}

type requestState struct {
	generation uint64
	repoKey    string
	loading    bool
	cancel     context.CancelFunc
	err        error
	stale      bool
}

type actionTarget struct {
	repoKey  string
	rowKey   string
	actionID string
	locator  taskflow.Locator
	choice   ActionChoice
}

func (t actionTarget) clone() actionTarget {
	t.choice = t.choice.clone()
	return t
}

// Model is an independent Bubble Tea model for one repository flow. It has no
// dependency on cli.App or the dashboard model.
type Model struct {
	actions    Actions
	runContext context.Context

	firstViewReady chan struct{}
	firstViewOnce  *sync.Once

	width  int
	height int

	screen        screenKind
	overlay       overlayKind
	overlayReturn overlayKind
	focus         FocusPane

	repositories []RepositoryRow
	repoCursor   int
	filter       textinput.Model
	filterActive bool

	repository   RepositoryRow
	desiredFocus string
	snapshot     Snapshot
	hasSnapshot  bool
	rowCursor    int
	actionCursor int

	remote OptionalRemoteObservation

	listRequest requestState
	loadRequest requestState
	planRequest requestState

	remoteChoices ActionList
	remoteCursor  int

	planTarget actionTarget
	plan       taskflow.Plan
	hasPlan    bool
	planErr    error
	confirm    textinput.Model

	applyGeneration uint64
	applyRunning    bool
	queuedRefresh   bool
	queuedQuit      bool

	lastResult    taskflow.Result
	hasLastResult bool
	lastResultErr error

	finalHandoff taskflow.Handoff
	hasHandoff   bool

	status   string
	quitting bool
}

// New constructs either an asynchronous repository picker (preselected=nil) or
// an asynchronous preselected-repository view. No callback runs until Init's
// command is executed, so View can return the initial frame immediately.
func New(actions Actions, preselected *RepositoryRow) Model {
	filter := textinput.New()
	filter.CharLimit = 240
	filter.Placeholder = "repository filter"
	confirm := textinput.New()
	confirm.CharLimit = 300
	confirm.Placeholder = "exact confirmation token"

	model := Model{
		actions:        actions,
		firstViewReady: make(chan struct{}),
		firstViewOnce:  &sync.Once{},
		width:          120, height: 34,
		filter: filter, confirm: confirm,
		focus: FocusSurfaces,
	}
	if preselected == nil {
		model.screen = screenPicker
		model.listRequest = requestState{generation: 1, repoKey: pickerRequestKey, loading: true}
		return model
	}
	model.screen = screenRepository
	model.repository = *preselected
	model.desiredFocus = preselected.FocusTarget
	model.loadRequest = requestState{generation: 1, repoKey: preselected.RepoKey, loading: true}
	return model
}

// NewPicker constructs a model which discovers repositories asynchronously.
func NewPicker(actions Actions) Model { return New(actions, nil) }

// NewRepository constructs a model focused on one exact repository key.
func NewRepository(actions Actions, repository RepositoryRow) Model {
	return New(actions, &repository)
}

// WithContext bounds cancelable list/load/plan callbacks to one TUI run. Apply
// deliberately uses a cancellation-detached child so queued quit cannot abort a
// mutation whose result ledger must be retained.
func (m Model) WithContext(ctx context.Context) Model {
	m.runContext = ctx
	return m
}

func (m Model) baseContext() context.Context {
	if m.runContext != nil {
		return m.runContext
	}
	return context.Background()
}

// Init implements tea.Model without doing synchronous repository work.
func (m Model) Init() tea.Cmd {
	var commands []tea.Cmd
	switch m.screen {
	case screenRepository:
		if m.loadRequest.loading {
			commands = append(commands, m.loadRepositoryCmd(m.loadRequest.generation, m.loadRequest.repoKey, m.baseContext()))
		}
	case screenPicker:
		if m.listRequest.loading {
			commands = append(commands, m.listRepositoriesCmd(m.listRequest.generation, m.baseContext()))
		}
	}
	if m.actions.AfterFirstView != nil {
		commands = append(commands, m.runAfterFirstView())
	}
	switch len(commands) {
	case 0:
		return nil
	case 1:
		return commands[0]
	default:
		return tea.Batch(commands...)
	}
}

func (m Model) signalFirstView() {
	if m.firstViewOnce != nil && m.firstViewReady != nil {
		m.firstViewOnce.Do(func() { close(m.firstViewReady) })
	}
}

func (m Model) runAfterFirstView() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.baseContext().Done():
			return nil
		case <-m.firstViewReady:
			m.actions.AfterFirstView(m.baseContext())
			return nil
		}
	}
}

// Handoff returns the most recent successful Apply handoff. Failed Apply calls,
// plans, and exit status alone can never populate it.
func (m Model) Handoff() (taskflow.Handoff, bool) { return m.finalHandoff, m.hasHandoff }

// LastResult returns the full retained ledger and Apply error.
func (m Model) LastResult() (taskflow.Result, error, bool) {
	return m.lastResult.Clone(), m.lastResultErr, m.hasLastResult
}

// CurrentSnapshot returns an independent accepted repository snapshot.
func (m Model) CurrentSnapshot() (Snapshot, bool) { return m.snapshot.Clone(), m.hasSnapshot }

// RemoteObservation returns the run-local remote evidence retained across local
// reloads for the current repository.
func (m Model) RemoteObservation() (taskflow.RemoteObservation, bool) {
	return m.remote.RemoteObservation()
}

// RepositoryKey returns the exact repository currently open.
func (m Model) RepositoryKey() string { return m.repository.RepoKey }

// FocusedPane reports the currently highlighted responsive panel.
func (m Model) FocusedPane() FocusPane { return m.focus }

// SelectedRowKey returns the current stable surface identity.
func (m Model) SelectedRowKey() string {
	row, ok := m.currentSurface()
	if !ok {
		return ""
	}
	return row.RowKey
}

// SelectedActionID returns the current stable action-choice identity.
func (m Model) SelectedActionID() string {
	choice, ok := m.currentAction()
	if !ok {
		return ""
	}
	return choice.ID
}

// Update implements tea.Model.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		return m, nil
	case repositoryListMsg:
		return m.acceptRepositoryList(message)
	case repositoryLoadMsg:
		return m.acceptRepositoryLoad(message)
	case planMsg:
		return m.acceptPlan(message)
	case applyMsg:
		return m.acceptApply(message)
	case tea.KeyMsg:
		if m.applyRunning {
			return m.updateApplying(message)
		}
		if m.filterActive {
			return m.updateFilter(message)
		}
		if m.overlay != overlayNone {
			return m.updateOverlay(message)
		}
		if m.screen == screenPicker {
			return m.updatePicker(message)
		}
		return m.updateRepository(message)
	}
	return m, nil
}

func (m Model) updatePicker(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.repoCursor = 0
			return m, nil
		}
		return m.quit()
	case "j", "down":
		m.setRepoCursor(m.repoCursor + 1)
	case "k", "up":
		m.setRepoCursor(m.repoCursor - 1)
	case "/":
		m.filterActive = true
		m.filter.CursorEnd()
		return m, m.filter.Focus()
	case "?":
		m.overlayReturn = overlayNone
		m.overlay = overlayHelp
		return m, nil
	case "r":
		return m.beginRepositoryList()
	case "enter":
		row, ok := m.currentRepository()
		if !ok {
			return m, nil
		}
		return m.selectRepository(row)
	}
	return m, nil
}

func (m Model) updateFilter(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.filterActive = false
		m.filter.Blur()
		m.filter.SetValue("")
		m.repoCursor = 0
		return m, nil
	case "enter":
		m.filterActive = false
		m.filter.Blur()
		m.setRepoCursor(m.repoCursor)
		return m, nil
	}
	var command tea.Cmd
	m.filter, command = m.filter.Update(message)
	m.repoCursor = 0
	return m, command
}

func (m Model) updateRepository(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "esc":
		m.cancelRepositoryRead()
		m.screen = screenPicker
		m.overlay = overlayNone
		return m.beginRepositoryList()
	case "j", "down":
		m.setRowCursor(m.rowCursor + 1)
	case "k", "up":
		m.setRowCursor(m.rowCursor - 1)
	case "l", "right":
		m.moveAction(1)
	case "h", "left":
		m.moveAction(-1)
	case "tab":
		m.focus = FocusPane((int(m.focus) + 1) % 3)
	case "shift+tab":
		m.focus = FocusPane((int(m.focus) + 2) % 3)
	case "?":
		m.overlayReturn = overlayNone
		m.overlay = overlayHelp
	case "r":
		return m.beginRepositoryLoad(false)
	case "R":
		return m.openRemoteMenu()
	case "enter":
		choice, ok := m.currentAction()
		if !ok {
			return m, nil
		}
		return m.beginPlan(choice)
	}
	return m, nil
}

func (m Model) updateApplying(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "q", "ctrl+c":
		m.queuedQuit = true
		m.status = "quit queued; waiting for Apply to finish"
	case "r", "R":
		m.queuedRefresh = true
		m.status = "refresh queued; waiting for Apply to finish"
	default:
		m.status = "Apply is running; navigation is disabled"
	}
	return m, nil
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	if m.listRequest.cancel != nil {
		m.listRequest.cancel()
	}
	if m.loadRequest.cancel != nil {
		m.loadRequest.cancel()
	}
	if m.planRequest.cancel != nil {
		m.planRequest.cancel()
	}
	return m, tea.Quit
}

func (m Model) filteredRepositories() []RepositoryRow {
	query := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	terms := strings.Fields(query)
	out := make([]RepositoryRow, 0, len(m.repositories))
	for _, row := range m.repositories {
		haystack := strings.ToLower(strings.Join([]string{row.RepoKey, row.Name, row.Path, row.Error}, " "))
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, row)
		}
	}
	return out
}

func (m Model) currentRepository() (RepositoryRow, bool) {
	rows := m.filteredRepositories()
	if m.repoCursor < 0 || m.repoCursor >= len(rows) {
		return RepositoryRow{}, false
	}
	return rows[m.repoCursor], true
}

func (m *Model) setRepoCursor(index int) {
	count := len(m.filteredRepositories())
	switch {
	case count == 0:
		index = 0
	case index < 0:
		index = 0
	case index >= count:
		index = count - 1
	}
	m.repoCursor = index
}

func (m Model) currentSurface() (SurfaceRow, bool) {
	if !m.hasSnapshot {
		return SurfaceRow{}, false
	}
	rows := m.snapshot.Surfaces.Values()
	if m.rowCursor < 0 || m.rowCursor >= len(rows) {
		return SurfaceRow{}, false
	}
	return rows[m.rowCursor], true
}

func (m Model) currentAction() (ActionChoice, bool) {
	row, ok := m.currentSurface()
	if !ok {
		return ActionChoice{}, false
	}
	actions := row.Actions.Values()
	if m.actionCursor < 0 || m.actionCursor >= len(actions) {
		return ActionChoice{}, false
	}
	if !actions[m.actionCursor].Valid() {
		return ActionChoice{}, false
	}
	return actions[m.actionCursor], true
}

func (m *Model) setRowCursor(index int) {
	count := 0
	if m.hasSnapshot {
		count = m.snapshot.Surfaces.Len()
	}
	switch {
	case count == 0:
		index = 0
	case index < 0:
		index = 0
	case index >= count:
		index = count - 1
	}
	if index != m.rowCursor {
		m.rowCursor = index
		m.actionCursor = 0
	} else {
		m.rowCursor = index
	}
	m.clampActionCursor()
}

func (m *Model) clampActionCursor() {
	row, ok := m.currentSurface()
	if !ok || row.Actions.Len() == 0 {
		m.actionCursor = 0
		return
	}
	if m.actionCursor < 0 {
		m.actionCursor = 0
	}
	if m.actionCursor >= row.Actions.Len() {
		m.actionCursor = row.Actions.Len() - 1
	}
}

func (m *Model) moveAction(delta int) {
	row, ok := m.currentSurface()
	if !ok || row.Actions.Len() == 0 {
		m.actionCursor = 0
		return
	}
	m.actionCursor += delta
	if m.actionCursor < 0 {
		m.actionCursor = 0
	}
	if m.actionCursor >= row.Actions.Len() {
		m.actionCursor = row.Actions.Len() - 1
	}
	m.focus = FocusActions
}

func (m Model) selectRepository(row RepositoryRow) (tea.Model, tea.Cmd) {
	m.cancelRepositoryListRead()
	m.screen = screenRepository
	m.repository = row
	m.desiredFocus = row.FocusTarget
	m.hasSnapshot = false
	m.snapshot = Snapshot{}
	m.remote = OptionalRemoteObservation{}
	m.rowCursor, m.actionCursor = 0, 0
	m.overlay = overlayNone
	m.hasPlan, m.planErr = false, nil
	m.hasLastResult, m.lastResultErr = false, nil
	m.hasHandoff = false
	return m.beginRepositoryLoad(true)
}

func combineError(label string, err error) error {
	if err == nil {
		return nil
	}
	if label == "" {
		return err
	}
	return errors.New(label + ": " + err.Error())
}
