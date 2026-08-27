package tui

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// View is which list the dashboard is showing.
type View int

const (
	// ViewTasks lists recorded change streams — what am I working on.
	ViewTasks View = iota
	// ViewRepos lists every repository under the scan roots — what do I have.
	ViewRepos
)

// Views is the cycle order.
var Views = []View{ViewTasks, ViewRepos}

func (v View) String() string {
	if v == ViewRepos {
		return "repos"
	}
	return "tasks"
}

// Actions is everything the dashboard can do. The TUI owns no domain logic:
// it calls back into the same code paths the non-interactive commands use, so
// behaviour cannot diverge between them.
type Actions struct {
	// Reload re-reads the task inventory.
	Reload func(ctx context.Context) ([]inventory.Row, error)
	// ReloadRepos re-reads the repository list.
	ReloadRepos func(ctx context.Context) ([]RepoRow, error)
	// Open makes a task live.
	Open func(ctx context.Context, t *task.Task) (string, error)
	// OpenRepo makes a repository live.
	OpenRepo func(ctx context.Context, r RepoRow) (string, error)
	// Park closes a task's session and records its next action.
	Park func(ctx context.Context, t *task.Task, next string) (string, error)
	// SetNext records a task's next action.
	SetNext func(ctx context.Context, t *task.Task, next string) error
	// Start creates a task in a repository.
	Start func(ctx context.Context, r RepoRow, name string) (string, error)
	// Runtime names the active backend.
	Runtime runtime.Runtime
	// Tools are external programs the dashboard hands the terminal to.
	Tools []Tool
}

// Tool is an external program launched in the selected row's directory.
//
// The dashboard suspends while one runs and redraws afterwards, so lazygit or
// a file manager feels like part of it rather than something you have to quit
// the dashboard to reach.
type Tool struct {
	Key  string
	Name string
	// Command is the argv to run; the first element is looked up on PATH.
	Command []string
	// Available reports whether the program is installed. A tool that is not
	// installed is left out of the footer rather than offered and then failing.
	Available func() bool
	// NeedsRepo restricts the tool to rows that have a checkout on disk.
	NeedsRepo bool
}

type mode int

const (
	modeList mode = iota
	modeEditNext
	modeConfirmPark
	modeFilter
	modeStartTask
)

// Model is the dashboard state.
type Model struct {
	actions Actions

	view  View
	rows  []inventory.Row
	repos []RepoRow

	// Cursors are plain fields, one per view, rather than a map: bubbletea
	// passes the model by value and expects each returned copy to be
	// independent. A map would be shared between every copy, so a keypress
	// that produced two candidate models would have them silently agree.
	taskCursor int
	repoCursor int
	// filter is the live text query; states narrows the task view.
	filter string
	states []task.State
	// showDone includes finished tasks, which are hidden by default because a
	// finished task is not work in progress.
	showDone bool

	mode  mode
	input textinput.Model

	status string
	err    error

	width, height int
	quitting      bool
	chosen        string
}

// New builds the dashboard.
func New(actions Actions, rows []inventory.Row, repos []RepoRow) Model {
	in := textinput.New()
	in.CharLimit = 200

	return Model{
		actions: actions,
		rows:    rows,
		repos:   repos,
		input:   in,
		width:   100,
		height:  30,
	}
}

// Chosen reports a directory the caller should cd into after the program
// exits, or "" when there is none.
func (m Model) Chosen() string { return m.chosen }

// CurrentView reports which list is showing.
func (m Model) CurrentView() View { return m.view }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return textinput.Blink }

type reloadMsg struct {
	rows  []inventory.Row
	repos []RepoRow
	err   error
}

type actionMsg struct {
	status string
	cd     string
	err    error
}

func (m Model) reload() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		out := reloadMsg{}
		if m.actions.Reload != nil {
			rows, err := m.actions.Reload(ctx)
			out.rows, out.err = rows, err
		}
		if m.actions.ReloadRepos != nil {
			repos, err := m.actions.ReloadRepos(ctx)
			out.repos = repos
			if out.err == nil {
				out.err = err
			}
		}
		return out
	}
}

// visibleTasks applies the state filter and the text query.
func (m Model) visibleTasks() []inventory.Row {
	var out []inventory.Row
	for _, r := range m.rows {
		if len(m.states) > 0 {
			if !containsState(m.states, r.Task.State) {
				continue
			}
		} else if r.Task.State == task.Done && !m.showDone {
			continue
		}
		if !matches(taskSearchText(r), m.filter) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// visibleRepos applies the text query, and sorts repositories with work in
// flight to the top — that is what someone opening the dashboard is looking
// for, not alphabetical order.
func (m Model) visibleRepos() []RepoRow {
	var out []RepoRow
	for _, r := range m.repos {
		if !matches(r.searchText(), m.filter) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.HotTasks() != b.HotTasks() {
			return a.HotTasks() > b.HotTasks()
		}
		if a.Live != b.Live {
			return a.Live
		}
		if len(a.Tasks) != len(b.Tasks) {
			return len(a.Tasks) > len(b.Tasks)
		}
		if a.Status.Dirty() != b.Status.Dirty() {
			return a.Status.Dirty()
		}
		return strings.ToLower(a.Repo.Display()) < strings.ToLower(b.Repo.Display())
	})
	return out
}

func containsState(list []task.State, s task.State) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// count is how many rows the current view has.
func (m Model) count() int {
	if m.view == ViewRepos {
		return len(m.visibleRepos())
	}
	return len(m.visibleTasks())
}

// at is the cursor position in the current view.
func (m Model) at() int {
	if m.view == ViewRepos {
		return m.repoCursor
	}
	return m.taskCursor
}

// setAt moves the cursor, clamped to the current view's length.
func (m *Model) setAt(i int) {
	n := m.count()
	switch {
	case n == 0:
		i = 0
	case i >= n:
		i = n - 1
	case i < 0:
		i = 0
	}
	if m.view == ViewRepos {
		m.repoCursor = i
		return
	}
	m.taskCursor = i
}

// currentTask returns the selected task, if the task view is showing one.
func (m Model) currentTask() (inventory.Row, bool) {
	if m.view != ViewTasks {
		return inventory.Row{}, false
	}
	rows := m.visibleTasks()
	if m.at() >= len(rows) {
		return inventory.Row{}, false
	}
	return rows[m.at()], true
}

// currentRepo returns the selected repository, if the repo view is showing one.
func (m Model) currentRepo() (RepoRow, bool) {
	if m.view != ViewRepos {
		return RepoRow{}, false
	}
	repos := m.visibleRepos()
	if m.at() >= len(repos) {
		return RepoRow{}, false
	}
	return repos[m.at()], true
}

// currentDir is the checkout the selected row points at, for the external
// tools and for the cd directive.
func (m Model) currentDir() string {
	if r, ok := m.currentTask(); ok {
		if r.CheckoutExists {
			return r.Checkout
		}
		return ""
	}
	if r, ok := m.currentRepo(); ok {
		return r.Repo.Path
	}
	return ""
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case reloadMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		if msg.rows != nil {
			m.rows = msg.rows
		}
		if msg.repos != nil {
			m.repos = msg.repos
		}
		m.setAt(m.at())
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.status = nil, msg.status
		if msg.cd != "" {
			// Under a runtime with no sessions the only useful outcome is
			// putting the user in the directory, which needs the alternate
			// screen torn down first.
			m.chosen, m.quitting = msg.cd, true
			return m, tea.Quit
		}
		return m, m.reload()

	case tea.KeyMsg:
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeEditNext, modeConfirmPark, modeStartTask:
			return m.updatePrompt(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := m.count()
	page := m.listHeight() / 2
	if page < 1 {
		page = 1
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		// One escape clears whatever narrowing is in effect before quitting
		// becomes the meaning of the key.
		if m.filter != "" || len(m.states) > 0 {
			m.filter, m.states = "", nil
			m.setAt(0)
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	// Vim-style movement, with the arrow equivalents alongside.
	case "j", "down", "ctrl+n":
		m.setAt(m.at() + 1)
	case "k", "up", "ctrl+p":
		m.setAt(m.at() - 1)
	case "ctrl+d", "pgdown":
		m.setAt(m.at() + page)
	case "ctrl+u", "pgup":
		m.setAt(m.at() - page)
	case "g", "home":
		m.setAt(0)
	case "G", "end":
		m.setAt(n - 1)

	// View switching. h/l double as left/right between views, since a list has
	// no horizontal axis of its own.
	case "tab", "l", "right":
		m.view = Views[(int(m.view)+1)%len(Views)]
	case "shift+tab", "h", "left":
		m.view = Views[(int(m.view)+len(Views)-1)%len(Views)]

	case "/":
		m.mode = modeFilter
		m.input.SetValue(m.filter)
		m.input.Placeholder = "filter"
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink

	case "r":
		m.status = "refreshing…"
		return m, m.reload()

	case "1":
		m.states, m.showDone = []task.State{task.Hot}, false
		m.view = ViewTasks
		m.setAt(0)
	case "2":
		m.states, m.showDone = []task.State{task.Warm}, false
		m.view = ViewTasks
		m.setAt(0)
	case "3":
		m.states, m.showDone = []task.State{task.Cold}, false
		m.view = ViewTasks
		m.setAt(0)
	case "0":
		m.states, m.filter = nil, ""
		m.setAt(0)
	case "a":
		m.showDone, m.states = !m.showDone, nil
		m.setAt(0)

	case "enter", "o":
		return m, m.openSelected()

	case "p":
		if _, ok := m.currentTask(); !ok {
			return m, nil
		}
		return m.prompt(modeConfirmPark, "", "what to do when you come back")

	case "c":
		row, ok := m.currentTask()
		if !ok {
			return m, nil
		}
		return m.prompt(modeEditNext, row.Task.Next, "next action")

	case "s":
		if _, ok := m.currentRepo(); !ok {
			return m, nil
		}
		return m.prompt(modeStartTask, "", "name for the new task")

	default:
		if cmd := m.launchTool(msg.String()); cmd != nil {
			return m, cmd
		}
	}
	m.setAt(m.at())
	return m, nil
}

func (m Model) prompt(md mode, value, placeholder string) (tea.Model, tea.Cmd) {
	m.mode = md
	m.input.SetValue(value)
	m.input.Placeholder = placeholder
	m.input.CursorEnd()
	m.input.Focus()
	return m, textinput.Blink
}

func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.filter = ""
		m.setAt(0)
		return m, nil
	case "enter":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Filtering live is the point: seeing the list narrow as you type is what
	// makes it faster than remembering an exact name.
	m.filter = m.input.Value()
	m.setAt(0)
	return m, cmd
}

func (m Model) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		md := m.mode
		value := strings.TrimSpace(m.input.Value())
		m.mode = modeList
		m.input.Blur()
		return m, m.submit(md, value)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) submit(md mode, value string) tea.Cmd {
	switch md {
	case modeEditNext:
		row, ok := m.currentTask()
		if !ok {
			return nil
		}
		t := row.Task
		return func() tea.Msg {
			if err := m.actions.SetNext(context.Background(), t, value); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{status: "next action recorded for " + t.Title()}
		}

	case modeConfirmPark:
		row, ok := m.currentTask()
		if !ok {
			return nil
		}
		t := row.Task
		return func() tea.Msg {
			status, err := m.actions.Park(context.Background(), t, value)
			return actionMsg{status: status, err: err}
		}

	case modeStartTask:
		r, ok := m.currentRepo()
		if !ok {
			return nil
		}
		if value == "" {
			return func() tea.Msg { return actionMsg{err: fmt.Errorf("a task needs a name")} }
		}
		return func() tea.Msg {
			status, err := m.actions.Start(context.Background(), r, value)
			return actionMsg{status: status, err: err}
		}
	}
	return nil
}

func (m Model) openSelected() tea.Cmd {
	cdWanted := m.actions.Runtime != nil && m.actions.Runtime.Name() == "none"

	if row, ok := m.currentTask(); ok {
		t := row.Task
		dir := row.Checkout
		return func() tea.Msg {
			status, err := m.actions.Open(context.Background(), t)
			cd := ""
			if err == nil && cdWanted {
				cd = dir
			}
			return actionMsg{status: status, cd: cd, err: err}
		}
	}
	if r, ok := m.currentRepo(); ok {
		dir := r.Repo.Path
		return func() tea.Msg {
			status, err := m.actions.OpenRepo(context.Background(), r)
			cd := ""
			if err == nil && cdWanted {
				cd = dir
			}
			return actionMsg{status: status, cd: cd, err: err}
		}
	}
	return nil
}

// launchTool hands the terminal to an external program running in the selected
// row's checkout, then reloads — the tool may well have changed the git state
// the dashboard is displaying.
func (m Model) launchTool(key string) tea.Cmd {
	for _, t := range m.actions.Tools {
		if t.Key != key {
			continue
		}
		if t.Available != nil && !t.Available() {
			return func() tea.Msg {
				return actionMsg{err: fmt.Errorf("%s is not installed", t.Command[0])}
			}
		}
		dir := m.currentDir()
		if dir == "" {
			return func() tea.Msg {
				return actionMsg{err: fmt.Errorf("nothing selected with a checkout on disk")}
			}
		}
		c := exec.Command(t.Command[0], t.Command[1:]...)
		c.Dir = dir
		return tea.ExecProcess(c, func(err error) tea.Msg {
			if err != nil {
				return actionMsg{err: fmt.Errorf("%s: %w", t.Name, err)}
			}
			return actionMsg{status: "back from " + t.Name}
		})
	}
	return nil
}

// Tools lists the external programs available on this machine, so the footer
// only advertises bindings that will actually work.
func (m Model) Tools() []Tool {
	var out []Tool
	for _, t := range m.actions.Tools {
		if t.Available == nil || t.Available() {
			out = append(out, t)
		}
	}
	return out
}

// Summary counts the task states, for the header line.
func (m Model) Summary() string {
	counts := map[task.State]int{}
	for _, r := range m.rows {
		counts[r.Task.State]++
	}
	var parts []string
	for _, s := range task.States {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d %s", s.Icon(), counts[s], s))
		}
	}
	if len(m.repos) > 0 {
		parts = append(parts, fmt.Sprintf("%d repos", len(m.repos)))
	}
	if len(parts) == 0 {
		return "no tasks"
	}
	return strings.Join(parts, "   ")
}

func contract(p string) string { return config.Contract(p) }
