package flowtui

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

const pickerRequestKey = "@repository-picker"

type repositoryListMsg struct {
	generation uint64
	repoKey    string
	rows       []RepositoryRow
	err        error
}

type repositoryLoadMsg struct {
	generation uint64
	repoKey    string
	snapshot   Snapshot
	err        error
}

type planMsg struct {
	generation uint64
	repoKey    string
	rowKey     string
	actionID   string
	plan       taskflow.Plan
	err        error
}

type applyMsg struct {
	generation uint64
	repoKey    string
	rowKey     string
	actionID   string
	result     taskflow.Result
	err        error
}

func (m Model) listRepositoriesCmd(generation uint64, ctx context.Context) tea.Cmd {
	callback := m.actions.ListRepositories
	return func() tea.Msg {
		if callback == nil {
			return repositoryListMsg{
				generation: generation, repoKey: pickerRequestKey,
				err: errors.New("repository listing is unavailable"),
			}
		}
		rows, err := callback(ctx)
		return repositoryListMsg{
			generation: generation, repoKey: pickerRequestKey,
			rows: append([]RepositoryRow(nil), rows...), err: err,
		}
	}
}

func (m Model) loadRepositoryCmd(generation uint64, repoKey string, ctx context.Context) tea.Cmd {
	callback := m.actions.LoadRepository
	return func() tea.Msg {
		if callback == nil {
			return repositoryLoadMsg{
				generation: generation, repoKey: repoKey,
				err: errors.New("repository loading is unavailable"),
			}
		}
		snapshot, err := callback(ctx, repoKey)
		return repositoryLoadMsg{
			generation: generation, repoKey: repoKey,
			snapshot: snapshot.Clone(), err: err,
		}
	}
}

func (m Model) planCmd(generation uint64, target actionTarget, ctx context.Context) tea.Cmd {
	target = target.clone()
	callback := m.actions.Plan
	return func() tea.Msg {
		if callback == nil {
			return planMsg{
				generation: generation, repoKey: target.repoKey, rowKey: target.rowKey, actionID: target.actionID,
				err: errors.New("planning is unavailable"),
			}
		}
		plan, err := callback(
			ctx, target.repoKey, target.rowKey, target.actionID,
			target.locator, target.choice.Options(),
		)
		return planMsg{
			generation: generation, repoKey: target.repoKey, rowKey: target.rowKey, actionID: target.actionID,
			plan: plan.Clone(), err: err,
		}
	}
}

func (m Model) applyCmd(generation uint64, target actionTarget, plan taskflow.Plan, approval taskflow.Approval) tea.Cmd {
	target = target.clone()
	plan = plan.Clone()
	callback := m.actions.Apply
	// Apply is intentionally detached from the run's cancellation signal. The UI
	// queues quit/refresh and waits for this command's ledger instead of claiming a
	// mutation was canceled or discarding its completion message.
	ctx := context.WithoutCancel(m.baseContext())
	return func() tea.Msg {
		if callback == nil {
			return applyMsg{
				generation: generation, repoKey: target.repoKey, rowKey: target.rowKey, actionID: target.actionID,
				err: errors.New("Apply is unavailable"),
			}
		}
		result, err := callback(
			ctx, target.repoKey, target.rowKey, target.actionID,
			target.locator, target.choice.Options(), plan.Clone(), approval,
		)
		return applyMsg{
			generation: generation, repoKey: target.repoKey, rowKey: target.rowKey, actionID: target.actionID,
			result: result.Clone(), err: err,
		}
	}
}

func (m Model) beginRepositoryList() (tea.Model, tea.Cmd) {
	if m.listRequest.cancel != nil {
		m.listRequest.cancel()
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.listRequest.generation++
	m.listRequest.repoKey = pickerRequestKey
	m.listRequest.loading = true
	m.listRequest.cancel = cancel
	m.listRequest.err = nil
	m.status = "loading repositories"
	return m, m.listRepositoriesCmd(m.listRequest.generation, ctx)
}

func (m Model) beginRepositoryLoad(clear bool) (tea.Model, tea.Cmd) {
	if m.loadRequest.cancel != nil {
		m.loadRequest.cancel()
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.loadRequest.generation++
	m.loadRequest.repoKey = m.repository.RepoKey
	m.loadRequest.loading = true
	m.loadRequest.cancel = cancel
	m.loadRequest.err = nil
	if clear {
		m.loadRequest.stale = false
	}
	m.status = "loading local repository facts"
	return m, m.loadRepositoryCmd(m.loadRequest.generation, m.repository.RepoKey, ctx)
}

func (m Model) beginPlan(choice ActionChoice) (tea.Model, tea.Cmd) {
	row, ok := m.currentSurface()
	if !ok || !choice.Valid() {
		return m, nil
	}
	if m.loadRequest.loading {
		m.status = "wait for the current local reload before planning"
		return m, nil
	}
	if m.planRequest.cancel != nil {
		m.planRequest.cancel()
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	m.planRequest.generation++
	m.planRequest.repoKey = m.repository.RepoKey
	m.planRequest.loading = true
	m.planRequest.cancel = cancel
	m.planRequest.err = nil
	m.planTarget = actionTarget{
		repoKey: m.repository.RepoKey, rowKey: row.RowKey,
		actionID: choice.ID, locator: row.Locator, choice: choice.clone(),
	}
	m.plan, m.hasPlan, m.planErr = taskflow.Plan{}, false, nil
	m.confirm.SetValue("")
	m.confirm.Blur()
	m.overlay = overlayPlanLoading
	m.status = "planning " + choice.Label
	return m, m.planCmd(m.planRequest.generation, m.planTarget, ctx)
}

func (m *Model) cancelRepositoryRead() {
	if !m.loadRequest.loading {
		return
	}
	if m.loadRequest.cancel != nil {
		m.loadRequest.cancel()
	}
	m.loadRequest.generation++
	m.loadRequest.loading = false
	m.loadRequest.cancel = nil
	m.loadRequest.err = nil
}

func (m *Model) cancelRepositoryListRead() {
	if !m.listRequest.loading {
		return
	}
	if m.listRequest.cancel != nil {
		m.listRequest.cancel()
	}
	m.listRequest.generation++
	m.listRequest.loading = false
	m.listRequest.cancel = nil
	m.listRequest.err = nil
}

func (m *Model) cancelPlanRead() {
	if m.planRequest.cancel != nil {
		m.planRequest.cancel()
	}
	m.planRequest.generation++
	m.planRequest.loading = false
	m.planRequest.cancel = nil
	m.planRequest.err = nil
}

func (m Model) acceptRepositoryList(message repositoryListMsg) (tea.Model, tea.Cmd) {
	if message.generation != m.listRequest.generation ||
		message.repoKey != pickerRequestKey || m.listRequest.repoKey != pickerRequestKey {
		return m, nil
	}
	if m.listRequest.cancel != nil {
		m.listRequest.cancel()
	}
	m.listRequest.cancel = nil
	m.listRequest.loading = false
	m.listRequest.err = message.err

	selectedKey := ""
	if selected, ok := m.currentRepository(); ok {
		selectedKey = selected.RepoKey
	}
	oldIndex := m.repoCursor
	if message.err != nil {
		m.listRequest.stale = len(m.repositories) > 0
		m.status = ""
		return m, nil
	}

	m.repositories = append([]RepositoryRow(nil), message.rows...)
	m.listRequest.stale = false
	m.status = ""
	m.repoCursor = nearestRepositoryIndex(m.filteredRepositories(), selectedKey, oldIndex)
	return m, nil
}

func nearestRepositoryIndex(rows []RepositoryRow, repoKey string, oldIndex int) int {
	if repoKey != "" {
		for index, row := range rows {
			if row.RepoKey == repoKey {
				return index
			}
		}
	}
	if len(rows) == 0 {
		return 0
	}
	if oldIndex < 0 {
		return 0
	}
	if oldIndex >= len(rows) {
		return len(rows) - 1
	}
	return oldIndex
}

func (m Model) acceptRepositoryLoad(message repositoryLoadMsg) (tea.Model, tea.Cmd) {
	if message.generation != m.loadRequest.generation ||
		message.repoKey != m.loadRequest.repoKey || message.repoKey != m.repository.RepoKey {
		return m, nil
	}
	if m.loadRequest.cancel != nil {
		m.loadRequest.cancel()
	}
	m.loadRequest.cancel = nil
	m.loadRequest.loading = false

	if message.err == nil && message.snapshot.Repository.RepoKey != message.repoKey {
		message.err = fmt.Errorf(
			"loaded snapshot repository key %q does not match requested key %q",
			message.snapshot.Repository.RepoKey, message.repoKey,
		)
	}
	if message.err != nil {
		m.loadRequest.err = message.err
		m.loadRequest.stale = m.hasSnapshot
		if m.hasSnapshot {
			m.snapshot.Freshness = FreshnessStale
		}
		m.status = ""
		return m, nil
	}

	oldRow, hadOldRow := m.currentSurface()
	oldIndex := m.rowCursor
	oldActionID := ""
	if choice, ok := m.currentAction(); ok {
		oldActionID = choice.ID
	}

	accepted := message.snapshot.Clone()
	if accepted.Freshness == "" {
		accepted.Freshness = FreshnessFresh
	}
	m.snapshot = accepted
	m.hasSnapshot = true
	m.loadRequest.err = nil
	m.loadRequest.stale = accepted.Freshness != FreshnessFresh || accepted.Error != ""
	m.repository = accepted.Repository
	if remote, ok := accepted.Remote.RemoteObservation(); ok {
		m.remote = SomeRemoteObservation(remote)
	}

	rows := accepted.Surfaces.Values()
	m.rowCursor = retainedSurfaceIndex(rows, oldRow, hadOldRow, oldIndex, m.desiredFocus)
	m.desiredFocus = ""
	m.actionCursor = retainedActionIndex(rows, m.rowCursor, oldActionID)
	m.clampActionCursor()
	m.status = ""
	return m, nil
}

func retainedSurfaceIndex(rows []SurfaceRow, previous SurfaceRow, hadPrevious bool, oldIndex int, focus string) int {
	if len(rows) == 0 {
		return 0
	}
	if hadPrevious {
		for index, row := range rows {
			if row.RowKey == previous.RowKey {
				return index
			}
		}
		if previous.Locator.TaskID != "" {
			for index, row := range rows {
				if row.Locator.TaskID == previous.Locator.TaskID {
					return index
				}
			}
		}
		for index, row := range rows {
			if locatorsReferToSameSurface(previous.Locator, row.Locator) {
				return index
			}
		}
	}
	if focus != "" {
		for index, row := range rows {
			if row.RowKey == focus || row.Locator.RowKey == focus {
				return index
			}
		}
		for index, row := range rows {
			if row.Locator.TaskID == focus {
				return index
			}
		}
		for index, row := range rows {
			if row.Locator.CheckoutPath == focus {
				return index
			}
		}
	}
	if oldIndex < 0 {
		return 0
	}
	if oldIndex >= len(rows) {
		return len(rows) - 1
	}
	return oldIndex
}

func retainedActionIndex(rows []SurfaceRow, rowIndex int, actionID string) int {
	if actionID == "" || rowIndex < 0 || rowIndex >= len(rows) {
		return 0
	}
	for index, action := range rows[rowIndex].Actions.Values() {
		if action.ID == actionID {
			return index
		}
	}
	return 0
}

func locatorsReferToSameSurface(left, right taskflow.Locator) bool {
	if left.RepositoryID != "" && right.RepositoryID != "" && left.RepositoryID != right.RepositoryID {
		return false
	}
	if left.CheckoutPath != "" && right.CheckoutPath != "" {
		return left.CheckoutPath == right.CheckoutPath
	}
	if left.TaskID != "" && right.TaskID != "" {
		return left.TaskID == right.TaskID
	}
	return left.RepoKey != "" && left.RepoKey == right.RepoKey &&
		left.Branch != "" && left.Branch == right.Branch && left.Base == right.Base
}

func (m Model) acceptPlan(message planMsg) (tea.Model, tea.Cmd) {
	if message.generation != m.planRequest.generation ||
		message.repoKey != m.planRequest.repoKey || message.repoKey != m.planTarget.repoKey ||
		message.rowKey != m.planTarget.rowKey || message.actionID != m.planTarget.actionID {
		return m, nil
	}
	if m.planRequest.cancel != nil {
		m.planRequest.cancel()
	}
	m.planRequest.cancel = nil
	m.planRequest.loading = false
	m.planRequest.err = message.err
	m.overlay = overlayPlan
	m.status = ""

	if message.plan.PlanID != "" {
		m.plan = message.plan.Clone()
		m.hasPlan = true
	}
	if message.err != nil {
		m.planErr = message.err
		return m, nil
	}
	if err := message.plan.Validate(); err != nil {
		m.planErr = combineError("invalid plan returned by callback", err)
		return m, nil
	}
	if message.plan.Locator != m.planTarget.locator {
		m.planErr = errors.New("plan locator does not match the exact selected surface")
		return m, nil
	}
	if message.plan.Action != m.planTarget.choice.Action() {
		m.planErr = errors.New("plan action does not match the exact selected action options")
		return m, nil
	}
	expected, err := taskflow.NewRequest(m.planTarget.locator, m.planTarget.choice.Options())
	if err != nil {
		m.planErr = combineError("selected action options are invalid", err)
		return m, nil
	}
	if !reflect.DeepEqual(message.plan.Request.Options, expected.Options) {
		m.planErr = errors.New("plan was built with different concrete action options than the selected choice")
		return m, nil
	}
	m.plan = message.plan.Clone()
	m.hasPlan = true
	m.planErr = nil
	if m.plan.Availability == taskflow.AvailabilityReady && m.plan.Confirmation.Kind == taskflow.ConfirmationTyped {
		m.confirm.SetValue("")
		m.confirm.CursorEnd()
		return m, m.confirm.Focus()
	}
	return m, nil
}

func (m Model) acceptApply(message applyMsg) (tea.Model, tea.Cmd) {
	if !m.applyRunning || message.generation != m.applyGeneration ||
		message.repoKey != m.planTarget.repoKey || message.rowKey != m.planTarget.rowKey ||
		message.actionID != m.planTarget.actionID {
		return m, nil
	}
	m.applyRunning = false
	m.overlay = overlayResult
	m.lastResult = message.result.Clone()
	m.hasLastResult = true
	m.lastResultErr = message.err
	m.status = ""
	if remote, ok := message.result.RemoteObservation(); ok {
		m.remote = SomeRemoteObservation(remote)
	}
	if message.err == nil {
		if handoff, ok := message.result.Handoff(); ok {
			m.finalHandoff = handoff
			m.hasHandoff = true
		}
	}

	queuedQuit := m.queuedQuit
	m.queuedQuit, m.queuedRefresh = false, false
	updated, command := m.beginRepositoryLoad(false)
	m = updated.(Model)
	if queuedQuit {
		// The fresh generation is established before honoring the queued quit. It
		// cannot authorize anything and the mutation ledger remains in final Model.
		if m.loadRequest.cancel != nil {
			m.loadRequest.cancel()
			m.loadRequest.cancel = nil
		}
		m.quitting = true
		return m, tea.Quit
	}
	return m, command
}
