package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
	// ViewRemote lists repositories visible through gh and glab.
	ViewRemote
)

// Views is the cycle order.
var Views = []View{ViewTasks, ViewRepos, ViewRemote}

func (v View) String() string {
	switch v {
	case ViewRepos:
		return "repos"
	case ViewRemote:
		return "remote"
	default:
		return "tasks"
	}
}

// Actions is everything the dashboard can do. The TUI owns no domain logic:
// it calls back into the same code paths the non-interactive commands use, so
// behaviour cannot diverge between them.
type Actions struct {
	// Reload re-reads the task inventory.
	Reload func(ctx context.Context) ([]inventory.Row, error)
	// ReloadRepos re-reads the repository list.
	ReloadRepos func(ctx context.Context) ([]RepoRow, error)
	// ReloadRemote queries gh and glab. It is lazy: the network is untouched
	// until the REMOTE view is opened.
	ReloadRemote func(ctx context.Context) ([]RemoteRow, error)
	// Open makes a task live.
	Open func(ctx context.Context, t *task.Task) (string, error)
	// OpenRepo makes a repository live.
	OpenRepo func(ctx context.Context, r RepoRow) (string, error)
	// OpenRemote opens a remote's existing local checkout.
	OpenRemote func(ctx context.Context, r RemoteRow) (string, error)
	// CloneRemote clones a remote that has no local checkout and returns the
	// new path, so the row can be updated without querying the network again.
	CloneRemote func(ctx context.Context, r RemoteRow) (status, localPath string, err error)
	// Park closes a task's session and records its next action.
	Park func(ctx context.Context, t *task.Task, next string) (string, error)
	// SetNext records a task's next action.
	SetNext func(ctx context.Context, t *task.Task, next string) error
	// Start creates an isolated worktree task in a repository.
	Start func(ctx context.Context, r RepoRow, name string) (string, error)
	// StartDirect tracks the repository's currently checked-out branch without
	// creating a branch or worktree.
	StartDirect func(ctx context.Context, r RepoRow, name string) (string, error)
	// LoadStats builds the selected repository's activity heatmap.
	LoadStats func(ctx context.Context, repo string) (StatsPanel, error)
	// BackfillStats derives this repository's history into the activity store.
	BackfillStats func(ctx context.Context, repo string) error
	// EditConfig returns the editor process; tea suspends around it.
	EditConfig func() (*exec.Cmd, error)
	// ReloadConfig reparses config and returns live-updatable preferences.
	// Runtime backend changes need a TUI restart, which status explains.
	ReloadConfig func(ctx context.Context) (ConfigUpdate, string, error)
	// RepoColumns and sorting are live-updatable display policy.
	RepoColumns []string
	RepoSort    string
	RepoReverse bool
	// Runtime names the active backend.
	Runtime runtime.Runtime
	// Tools are external programs the dashboard hands the terminal to.
	Tools []Tool
}

// ConfigUpdate is the subset of config a running TUI can safely apply without
// rebuilding its runtime backend.
type ConfigUpdate struct {
	Tools       []Tool
	RepoColumns []string
	RepoSort    string
	RepoReverse bool
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
	modeStartDirect
	modeConfirmClone
	modeStats
)

// Model is the dashboard state.
type Model struct {
	actions Actions

	view    View
	rows    []inventory.Row
	repos   []RepoRow
	remotes []RemoteRow
	// Remote loading is lazy so opening the dashboard never waits on the
	// network. The first switch to REMOTE triggers it.
	remotesLoaded  bool
	remotesLoading bool
	initialLoad    bool
	loadingLocal   bool

	// Cursors are plain fields, one per view, rather than a map: bubbletea
	// passes the model by value and expects each returned copy to be
	// independent. A map would be shared between every copy, so a keypress
	// that produced two candidate models would have them silently agree.
	taskCursor   int
	repoCursor   int
	remoteCursor int
	// filter is the live text query; states narrows the task view.
	filter string
	states []task.State
	// showDone includes finished tasks, which are hidden by default because a
	// finished task is not work in progress.
	showDone bool

	mode  mode
	input textinput.Model
	stats *StatsPanel

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

// WithRemotes seeds the lazy forge view from a fresh on-disk cache. The first
// switch is then instant; r still refreshes explicitly.
func (m Model) WithRemotes(rows []RemoteRow) Model {
	m.remotes, m.remotesLoaded = rows, true
	return m
}

// BeginLoading makes Init load task and repo data asynchronously. This lets
// the alternate screen appear immediately instead of blocking on dozens of Git
// probes before Bubble Tea starts.
func (m Model) BeginLoading() Model {
	m.initialLoad, m.loadingLocal = true, true
	return m
}

// Chosen reports a directory the caller should cd into after the program
// exits, or "" when there is none.
func (m Model) Chosen() string { return m.chosen }

// CurrentView reports which list is showing.
func (m Model) CurrentView() View { return m.view }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	if m.initialLoad {
		return tea.Batch(textinput.Blink, m.reload())
	}
	return textinput.Blink
}

type reloadMsg struct {
	rows      []inventory.Row
	repos     []RepoRow
	remotes   []RemoteRow
	remoteSet bool
	err       error
}

type remoteMsg struct {
	rows []RemoteRow
	err  error
}

type statsMsg struct {
	panel StatsPanel
	err   error
}

type statsBackfilledMsg struct {
	repo string
	err  error
}

type configEditedMsg struct{ err error }

type configMsg struct {
	update        ConfigUpdate
	status        string
	refreshRemote bool
	err           error
}

type actionMsg struct {
	status     string
	cd         string
	remoteName string
	localPath  string
	err        error
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

func (m Model) reloadRemote() tea.Cmd {
	return func() tea.Msg {
		if m.actions.ReloadRemote == nil {
			return remoteMsg{}
		}
		rows, err := m.actions.ReloadRemote(context.Background())
		return remoteMsg{rows: rows, err: err}
	}
}

func (m Model) reloadConfig(refreshRemote bool) tea.Cmd {
	return func() tea.Msg {
		if m.actions.ReloadConfig == nil {
			return configMsg{refreshRemote: refreshRemote}
		}
		update, status, err := m.actions.ReloadConfig(context.Background())
		return configMsg{update: update, status: status, refreshRemote: refreshRemote, err: err}
	}
}

func (m Model) reloadAfterConfig(refreshRemote bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		out := reloadMsg{}
		if m.actions.Reload != nil {
			out.rows, out.err = m.actions.Reload(ctx)
		}
		if m.actions.ReloadRepos != nil {
			var err error
			out.repos, err = m.actions.ReloadRepos(ctx)
			if out.err == nil {
				out.err = err
			}
		}
		if refreshRemote && m.actions.ReloadRemote != nil {
			var err error
			out.remotes, err = m.actions.ReloadRemote(ctx)
			out.remoteSet = true
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
	sortBy := m.actions.RepoSort
	if sortBy == "" {
		sortBy = "activity"
	}
	sort.SliceStable(out, func(i, j int) bool {
		cmp := compareRepos(out[i], out[j], sortBy)
		if m.actions.RepoReverse {
			return cmp > 0
		}
		return cmp < 0
	})
	return out
}

// visibleRemotes filters the combined gh/glab inventory. Local clones sort
// first, then recently updated repositories.
func (m Model) visibleRemotes() []RemoteRow {
	var out []RemoteRow
	for _, r := range m.remotes {
		if matches(r.searchText(), m.filter) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cloned() != out[j].Cloned() {
			return out[i].Cloned()
		}
		if !out[i].Repo.UpdatedAt.Equal(out[j].Repo.UpdatedAt) {
			return out[i].Repo.UpdatedAt.After(out[j].Repo.UpdatedAt)
		}
		return out[i].Repo.Label() < out[j].Repo.Label()
	})
	return out
}

// compareRepos returns negative when a belongs before b.
func compareRepos(a, b RepoRow, sortBy string) int {
	nameCmp := strings.Compare(strings.ToLower(a.Repo.Display()), strings.ToLower(b.Repo.Display()))
	descInt := func(x, y int) int {
		switch {
		case x > y:
			return -1
		case x < y:
			return 1
		default:
			return 0
		}
	}
	descBool := func(x, y bool) int {
		if x == y {
			return 0
		}
		if x {
			return -1
		}
		return 1
	}
	latest := func() int {
		if a.LastActivity.Equal(b.LastActivity) {
			return 0
		}
		if a.LastActivity.After(b.LastActivity) {
			return -1
		}
		return 1
	}

	switch sortBy {
	case "name":
		return nameCmp
	case "latest":
		if c := latest(); c != 0 {
			return c
		}
	case "git":
		if c := descBool(a.Status.Dirty(), b.Status.Dirty()); c != 0 {
			return c
		}
		if c := descInt(a.Status.Changed, b.Status.Changed); c != 0 {
			return c
		}
	case "tasks":
		if c := descInt(a.HotTasks(), b.HotTasks()); c != 0 {
			return c
		}
		if c := descInt(len(a.Tasks), len(b.Tasks)); c != 0 {
			return c
		}
	default: // activity
		if c := descInt(a.HotTasks(), b.HotTasks()); c != 0 {
			return c
		}
		if c := descBool(a.Live, b.Live); c != 0 {
			return c
		}
		if c := descBool(a.Status.Dirty(), b.Status.Dirty()); c != 0 {
			return c
		}
		if c := descInt(len(a.Tasks), len(b.Tasks)); c != 0 {
			return c
		}
		if c := latest(); c != 0 {
			return c
		}
	}
	return nameCmp
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
	switch m.view {
	case ViewRepos:
		return len(m.visibleRepos())
	case ViewRemote:
		return len(m.visibleRemotes())
	default:
		return len(m.visibleTasks())
	}
}

// at is the cursor position in the current view.
func (m Model) at() int {
	switch m.view {
	case ViewRepos:
		return m.repoCursor
	case ViewRemote:
		return m.remoteCursor
	default:
		return m.taskCursor
	}
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
	switch m.view {
	case ViewRepos:
		m.repoCursor = i
	case ViewRemote:
		m.remoteCursor = i
	default:
		m.taskCursor = i
	}
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

// currentRemote returns the selected forge repository.
func (m Model) currentRemote() (RemoteRow, bool) {
	if m.view != ViewRemote {
		return RemoteRow{}, false
	}
	rows := m.visibleRemotes()
	if m.at() >= len(rows) {
		return RemoteRow{}, false
	}
	return rows[m.at()], true
}

// matchRemoteLocals fills cached remote rows from the freshly loaded local
// inventory without another scan.
func (m *Model) matchRemoteLocals() {
	byRemote := map[string]RepoRow{}
	for _, r := range m.repos {
		if r.RemoteName != "" {
			byRemote[string(r.RemoteForge)+"/"+strings.ToLower(r.RemoteName)] = r
		}
	}
	for i := range m.remotes {
		key := string(m.remotes[i].Repo.Forge) + "/" + strings.ToLower(m.remotes[i].Repo.FullName)
		if local, ok := byRemote[key]; ok {
			m.remotes[i].LocalPath = local.Repo.Path
			m.remotes[i].LocalName = local.Repo.Display()
		} else {
			m.remotes[i].LocalPath, m.remotes[i].LocalName = "", ""
		}
	}
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
	if r, ok := m.currentRemote(); ok {
		return r.LocalPath
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
		m.loadingLocal = false
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
			m.matchRemoteLocals()
		}
		if msg.remoteSet {
			m.remotes, m.remotesLoaded, m.remotesLoading = msg.remotes, true, false
		}
		m.setAt(m.at())
		return m, nil

	case remoteMsg:
		m.remotes, m.remotesLoaded, m.remotesLoading = msg.rows, true, false
		m.err = msg.err
		m.status = ""
		m.setAt(m.at())
		return m, nil

	case statsBackfilledMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			return m, nil
		}
		m.status = "backfill complete; loading heatmap…"
		return m, m.loadStats(msg.repo)

	case configEditedMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			return m, nil
		}
		m.status = "reloading config…"
		return m, m.reloadConfig(m.view == ViewRemote)

	case configMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			return m, nil
		}
		m.actions.Tools = msg.update.Tools
		m.actions.RepoColumns = msg.update.RepoColumns
		m.actions.RepoSort = msg.update.RepoSort
		m.actions.RepoReverse = msg.update.RepoReverse
		m.err, m.status = nil, msg.status
		return m, m.reloadAfterConfig(msg.refreshRemote)

	case statsMsg:
		if msg.err != nil {
			m.err = msg.err
			m.stats = nil
		} else {
			m.err = nil
			m.stats = &msg.panel
		}
		m.status = ""
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.status = nil, msg.status
		if msg.remoteName != "" && msg.localPath != "" {
			for i := range m.remotes {
				if m.remotes[i].Repo.FullName == msg.remoteName {
					m.remotes[i].LocalPath = msg.localPath
					break
				}
			}
		}
		if msg.cd != "" {
			// Under a runtime with no sessions the only useful outcome is
			// putting the user in the directory, which needs the alternate
			// screen torn down first.
			m.chosen, m.quitting = msg.cd, true
			return m, tea.Quit
		}
		if m.view == ViewRemote {
			// Clone already updated the selected row; only local repo/task
			// state needs a refresh. A network round-trip here would make a
			// successful clone feel hung for several seconds.
			return m, m.reload()
		}
		return m, m.reload()

	case tea.KeyMsg:
		if m.mode == modeStats {
			return m.updateStats(msg)
		}
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeEditNext, modeConfirmPark, modeStartTask, modeStartDirect, modeConfirmClone:
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
		return m.afterViewSwitch()
	case "shift+tab", "h", "left":
		m.view = Views[(int(m.view)+len(Views)-1)%len(Views)]
		return m.afterViewSwitch()

	case "/":
		m.mode = modeFilter
		m.input.SetValue(m.filter)
		m.input.Placeholder = "filter"
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink

	case "r":
		m.status = "reloading config + data…"
		if m.view == ViewRemote {
			m.remotesLoading = true
		}
		return m, m.reloadConfig(m.view == ViewRemote)

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
		if row, ok := m.currentTask(); ok {
			return m.prompt(modeEditNext, row.Task.Next, "next action")
		}
		if row, ok := m.currentRemote(); ok && !row.Cloned() {
			return m.prompt(modeConfirmClone, row.Repo.FullName,
				"enter to clone; esc to cancel")
		}
		return m, nil

	case "s":
		if _, ok := m.currentRepo(); !ok {
			return m, nil
		}
		return m.prompt(modeStartTask, "", "name for the new worktree task")

	case "d":
		if _, ok := m.currentRepo(); !ok {
			return m, nil
		}
		return m.prompt(modeStartDirect, "", "name for direct work on current branch")

	case "e":
		if m.actions.EditConfig == nil {
			return m, nil
		}
		proc, err := m.actions.EditConfig()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.status = "editing config…"
		return m, tea.ExecProcess(proc, func(err error) tea.Msg {
			return configEditedMsg{err: err}
		})

	case "O":
		if m.view != ViewRepos {
			return m, nil
		}
		orders := []string{"activity", "latest", "name", "git", "tasks"}
		current := 0
		for i, order := range orders {
			if order == m.actions.RepoSort {
				current = i
				break
			}
		}
		m.actions.RepoSort = orders[(current+1)%len(orders)]
		m.status = "repo sort: " + m.actions.RepoSort
		m.setAt(0)
		return m, nil

	case "R":
		if m.view != ViewRepos {
			return m, nil
		}
		m.actions.RepoReverse = !m.actions.RepoReverse
		m.status = fmt.Sprintf("repo sort reversed: %v", m.actions.RepoReverse)
		m.setAt(0)
		return m, nil

	case "H":
		repo := m.selectedRepoName()
		if repo == "" || m.actions.LoadStats == nil {
			return m, nil
		}
		m.mode, m.stats, m.status = modeStats, nil, "loading activity…"
		return m, m.loadStats(repo)

	default:
		if cmd := m.launchTool(msg.String()); cmd != nil {
			return m, cmd
		}
	}
	m.setAt(m.at())
	return m, nil
}

func (m Model) selectedRepoName() string {
	if r, ok := m.currentTask(); ok {
		return r.Task.Repo
	}
	if r, ok := m.currentRepo(); ok {
		return r.Repo.Name
	}
	if r, ok := m.currentRemote(); ok && r.Cloned() {
		if r.LocalName != "" {
			return filepath.Base(r.LocalName)
		}
		return r.Repo.Name
	}
	return ""
}

func (m Model) loadStats(repo string) tea.Cmd {
	return func() tea.Msg {
		panel, err := m.actions.LoadStats(context.Background(), repo)
		return statsMsg{panel: panel, err: err}
	}
}

func (m Model) updateStats(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc", "H":
		m.mode, m.stats, m.err, m.status = modeList, nil, nil, ""
		return m, nil
	case "r":
		repo := ""
		if m.stats != nil {
			repo = m.stats.Repo
		}
		if repo == "" {
			repo = m.selectedRepoName()
		}
		m.stats, m.status = nil, "loading activity…"
		return m, m.loadStats(repo)
	case "b":
		repo := ""
		if m.stats != nil {
			repo = m.stats.Repo
		}
		if repo == "" {
			repo = m.selectedRepoName()
		}
		if repo == "" || m.actions.BackfillStats == nil {
			return m, nil
		}
		m.status = "backfilling this repository from Git history…"
		return m, func() tea.Msg {
			err := m.actions.BackfillStats(context.Background(), repo)
			return statsBackfilledMsg{repo: repo, err: err}
		}
	}
	return m, nil
}

// afterViewSwitch lazily loads forge data only when REMOTE is first opened,
// so starting the dashboard never waits on a network request.
func (m Model) afterViewSwitch() (tea.Model, tea.Cmd) {
	m.setAt(m.at())
	if m.view == ViewRemote && !m.remotesLoaded && !m.remotesLoading {
		m.remotesLoading = true
		m.status = "loading GitHub and GitLab repositories…"
		return m, m.reloadRemote()
	}
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

	case modeStartDirect:
		r, ok := m.currentRepo()
		if !ok {
			return nil
		}
		if value == "" {
			return func() tea.Msg { return actionMsg{err: fmt.Errorf("a task needs a name")} }
		}
		return func() tea.Msg {
			status, err := m.actions.StartDirect(context.Background(), r, value)
			return actionMsg{status: status, err: err}
		}

	case modeConfirmClone:
		r, ok := m.currentRemote()
		if !ok {
			return nil
		}
		return func() tea.Msg {
			status, path, err := m.actions.CloneRemote(context.Background(), r)
			return actionMsg{
				status: status, remoteName: r.Repo.FullName, localPath: path, err: err,
			}
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
	if r, ok := m.currentRemote(); ok {
		if !r.Cloned() {
			return nil // c is the explicit clone action
		}
		dir := r.LocalPath
		return func() tea.Msg {
			status, err := m.actions.OpenRemote(context.Background(), r)
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
	if m.remotesLoaded {
		parts = append(parts, fmt.Sprintf("%d remote", len(m.remotes)))
	}
	if len(parts) == 0 {
		return "no tasks"
	}
	return strings.Join(parts, "   ")
}

func contract(p string) string { return config.Contract(p) }
