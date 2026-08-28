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
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/repo"
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
	// ViewFleet lists repository observations across configured machines.
	ViewFleet
	// ViewTries lists durable scratch experiments and retained lifecycle history.
	ViewTries
	// ViewRemote lists repositories visible through configured forge CLIs.
	ViewRemote
	// ViewSkills lists project and global agent skills.
	ViewSkills
)

// Views is the cycle order.
var Views = []View{ViewTasks, ViewRepos, ViewFleet, ViewTries, ViewRemote, ViewSkills}

func (v View) String() string {
	switch v {
	case ViewRepos:
		return "repos"
	case ViewFleet:
		return "fleet"
	case ViewTries:
		return "try"
	case ViewRemote:
		return "remote"
	case ViewSkills:
		return "skills"
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
	// ReloadFleet fans out to configured dev hosts. It is lazy like REMOTE.
	ReloadFleet func(ctx context.Context) ([]FleetRow, error)
	// ReloadRemote queries configured forge CLIs. It is lazy: the network is untouched
	// until the REMOTE view is opened.
	ReloadRemote func(ctx context.Context) ([]RemoteRow, error)
	// ReloadSkills reads local project/global skill state without contacting sources.
	ReloadSkills func(ctx context.Context) ([]agentskill.Skill, error)
	// CheckSkills performs the explicitly requested read-only network comparison.
	CheckSkills func(ctx context.Context, rows []agentskill.Skill) []agentskill.Skill
	// AddSkill and UpdateSkill return interactive processes for tea to suspend around.
	AddSkill    func() (*exec.Cmd, error)
	UpdateSkill func(row agentskill.Skill) (*exec.Cmd, error)
	// Repos and Tries group asset-specific metadata and lifecycle actions.
	Repos RepoActions
	Tries TryActions
	Notes NoteActions
	Sizes SizeActions
	// Open makes a task live.
	Open func(ctx context.Context, t *task.Task) (OpenResult, error)
	// OpenRepo makes a repository live.
	OpenRepo func(ctx context.Context, r RepoRow) (OpenResult, error)
	// OpenCheckout makes one linked worktree live.
	OpenCheckout func(ctx context.Context, r RepoRow, checkout inventory.RepoCheckout) (OpenResult, error)
	// OpenRemote opens a remote's existing local checkout.
	OpenRemote func(ctx context.Context, r RemoteRow) (OpenResult, error)
	// OpenFleet returns an interactive process for a local or remote fleet row.
	OpenFleet func(ctx context.Context, r FleetRow) (*exec.Cmd, error)
	// CloneRemote clones a remote that has no local checkout and returns the
	// new path, so the row can be updated without querying the network again.
	CloneRemote func(ctx context.Context, r RemoteRow) (OpenResult, string, error)
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
	// Copy writes one of the repo-context payloads to the system clipboard.
	Copy func(text string) error
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

// OpenResult separates the status text rendered by the dashboard from the
// opaque runtime handle activated only after Bubble Tea leaves its alternate
// screen.
type OpenResult struct {
	Status        string
	RuntimeHandle string
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
	modeConfirmSkillUpdate
	modeStats
	modeCopy
	modeNoteBrowse
	modeNoteAdd
	modeNoteSearch
	modeNoteConfirmDelete
)

// Model is the dashboard state.
type Model struct {
	actions Actions

	view    View
	rows    []inventory.Row
	repos   []RepoRow
	tries   []TryRow
	remotes []RemoteRow
	fleet   []FleetRow
	skills  []agentskill.Skill
	// Remote loading is lazy so opening the dashboard never waits on the
	// network. The first switch to REMOTE triggers it.
	remotesLoaded   bool
	remotesLoading  bool
	fleetLoaded     bool
	fleetLoading    bool
	skillsLoaded    bool
	skillsLoading   bool
	skillsChecking  bool
	remotesStale    bool
	initialLoad     bool
	loadingLocal    bool
	sizeLoad        diskusage.Load
	forceSizeReload bool

	// Cursors are plain fields, one per view, rather than a map: bubbletea
	// passes the model by value and expects each returned copy to be
	// independent. A map would be shared between every copy, so a keypress
	// that produced two candidate models would have them silently agree.
	taskCursor   int
	repoCursor   int
	tryCursor    int
	remoteCursor int
	fleetCursor  int
	skillCursor  int
	// expandedRepos is a slice rather than a map because Bubble Tea copies the
	// model by value; a map would make candidate models share mutation.
	expandedRepos []string
	// filter is the live text query; states narrows the task view.
	filter string
	states []task.State
	// showDone includes finished tasks, which are hidden by default because a
	// finished task is not work in progress.
	showDone bool
	// showAllTries includes deprecated, archived, evicted, and graduated history.
	showAllTries bool
	trySort      string
	tryReverse   bool

	mode    mode
	input   textinput.Model
	stats   *StatsPanel
	overlay overlayState

	notes              []*note.Note
	noteTarget         NoteTarget
	noteCursor         int
	noteQuery          string
	noteExpanded       bool
	noteLoading        bool
	noteReturnToBrowse bool
	noteRequest        uint64

	status string
	err    error

	width, height int
	quitting      bool
	chosen        string
	activate      string
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

// WithFleet seeds cached fleet rows while the first live refresh remains lazy.
func (m Model) WithFleet(rows []FleetRow) Model {
	m.fleet = rows
	return m
}

// WithRemotesStale marks seeded rows for background refresh on first visit.
func (m Model) WithRemotesStale(stale bool) Model {
	m.remotesStale = stale
	return m
}

// WithTries seeds the Try view, primarily for tests and embedded callers. The
// production dashboard loads it with the other local inventories in Init.
func (m Model) WithTries(rows []TryRow) Model {
	m.tries = rows
	return m
}

// BeginLoading makes Init load task, repository, and Try data asynchronously. This lets
// the alternate screen appear immediately instead of blocking on dozens of Git
// probes before Bubble Tea starts.
func (m Model) BeginLoading() Model {
	m.initialLoad, m.loadingLocal = true, true
	return m
}

// Chosen reports a directory the caller should cd into after the program
// exits, or "" when there is none.
func (m Model) Chosen() string { return m.chosen }

// Activation reports the runtime handle to switch/attach after the dashboard
// has restored the terminal, or "" when the user quit normally.
func (m Model) Activation() string { return m.activate }

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
	rows       []inventory.Row
	repos      []RepoRow
	tries      []TryRow
	rowsSet    bool
	reposSet   bool
	triesSet   bool
	remotes    []RemoteRow
	remoteSet  bool
	forceSizes bool
	err        error
}

type remoteMsg struct {
	rows []RemoteRow
	err  error
}

type fleetMsg struct {
	rows []FleetRow
	err  error
}
type skillsMsg struct {
	rows    []agentskill.Skill
	loaded  bool
	checked bool
	status  string
	err     error
}

type skillProcessMsg struct {
	action string
	name   string
	scope  agentskill.Scope
	err    error
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

type noteListMsg struct {
	notes     []*note.Note
	repos     []RepoRow
	targetKey string
	request   uint64
	err       error
}

type noteActionMsg struct {
	status       string
	returnBrowse bool
	err          error
}

type actionMsg struct {
	status     string
	cd         string
	activate   string
	remoteName string
	localPath  string
	forceSizes bool
	err        error
}

type triesMsg struct {
	tries    []TryRow
	triesSet bool
	repos    []RepoRow
	reposSet bool
	err      error
}

type tryActionMsg struct {
	result TryActionResult
	err    error
}

type copyMsg struct {
	status string
	err    error
}

func (m Model) reload() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		out := reloadMsg{forceSizes: m.forceSizeReload}
		if m.actions.Reload != nil {
			rows, err := m.actions.Reload(ctx)
			out.rows, out.rowsSet, out.err = rows, true, err
		}
		if m.actions.ReloadRepos != nil {
			repos, err := m.actions.ReloadRepos(ctx)
			out.repos, out.reposSet = repos, true
			if out.err == nil {
				out.err = err
			}
		}
		if m.actions.Tries.Reload != nil {
			tries, err := m.actions.Tries.Reload(ctx, m.showAllTries)
			out.tries, out.triesSet = tries, true
			if out.err == nil {
				out.err = err
			}
		}
		return out
	}
}

func (m Model) reloadTries(includeRepos bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		out := triesMsg{}
		if m.actions.Tries.Reload != nil {
			out.tries, out.err = m.actions.Tries.Reload(ctx, m.showAllTries)
			out.triesSet = true
		}
		if includeRepos && m.actions.ReloadRepos != nil {
			var err error
			out.repos, err = m.actions.ReloadRepos(ctx)
			out.reposSet = true
			if out.err == nil {
				out.err = err
			}
		}
		return out
	}
}

func (m Model) applyRepoPatch(row RepoRow, tags []string, note string) tea.Cmd {
	if m.actions.Repos.Patch == nil {
		return nil
	}
	return func() tea.Msg {
		status, err := m.actions.Repos.Patch(context.Background(), row, tags, note)
		return actionMsg{status: status, err: err}
	}
}

func (m Model) applyTry(request TryRequest) tea.Cmd {
	if m.actions.Tries.Apply == nil {
		return nil
	}
	return func() tea.Msg {
		result, err := m.actions.Tries.Apply(context.Background(), request)
		return tryActionMsg{result: result, err: err}
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

func (m Model) reloadFleet() tea.Cmd {
	return func() tea.Msg {
		if m.actions.ReloadFleet == nil {
			return fleetMsg{}
		}
		rows, err := m.actions.ReloadFleet(context.Background())
		return fleetMsg{rows: rows, err: err}
	}
}
func (m Model) reloadSkills() tea.Cmd {
	return func() tea.Msg {
		if m.actions.ReloadSkills == nil {
			return skillsMsg{loaded: true}
		}
		rows, err := m.actions.ReloadSkills(context.Background())
		return skillsMsg{rows: rows, loaded: true, err: err}
	}
}

func (m Model) checkSkills(rows []agentskill.Skill) tea.Cmd {
	return func() tea.Msg {
		if m.actions.CheckSkills == nil {
			return skillsMsg{rows: rows, loaded: true, checked: true}
		}
		return skillsMsg{
			rows: m.actions.CheckSkills(context.Background(), rows), loaded: true, checked: true,
		}
	}
}

func (m Model) reloadUpdatedSkill(name string, scope agentskill.Scope) tea.Cmd {
	return func() tea.Msg {
		if m.actions.ReloadSkills == nil {
			return skillsMsg{loaded: true, status: "updated " + name}
		}
		rows, err := m.actions.ReloadSkills(context.Background())
		if err != nil {
			return skillsMsg{loaded: true, err: err}
		}
		if m.actions.CheckSkills != nil {
			for i := range rows {
				if rows[i].Name == name && rows[i].Scope == scope {
					checked := m.actions.CheckSkills(context.Background(), []agentskill.Skill{rows[i]})
					if len(checked) == 1 {
						rows[i] = checked[0]
					}
					break
				}
			}
		}
		return skillsMsg{rows: rows, loaded: true, checked: true, status: "updated " + name}
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
		out := reloadMsg{forceSizes: m.forceSizeReload}
		if m.actions.Reload != nil {
			out.rows, out.err = m.actions.Reload(ctx)
			out.rowsSet = true
		}
		if m.actions.ReloadRepos != nil {
			var err error
			out.repos, err = m.actions.ReloadRepos(ctx)
			out.reposSet = true
			if out.err == nil {
				out.err = err
			}
		}
		if m.actions.Tries.Reload != nil {
			var err error
			out.tries, err = m.actions.Tries.Reload(ctx, m.showAllTries)
			out.triesSet = true
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
		if r.IsTry() || !r.matches(m.filter) {
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

// visibleTries applies structured experiment filters and keeps sorting local to
// the view so repository ordering remains independent.
func (m Model) visibleTries() []TryRow {
	out := make([]TryRow, 0, len(m.tries))
	for _, row := range m.tries {
		if row.matches(m.filter) {
			out = append(out, row)
		}
	}
	sortBy := m.trySort
	if sortBy == "" {
		sortBy = "activity"
	}
	sortTryRows(out, sortBy, m.tryReverse)
	return out
}

type repoItem struct {
	Repo          RepoRow
	CheckoutIndex int // -1 is the repository parent; 1+ is a linked worktree
}

func (i repoItem) child() bool { return i.CheckoutIndex > 0 }

func (i repoItem) checkout() (inventory.RepoCheckout, bool) {
	if i.CheckoutIndex < 0 || i.CheckoutIndex >= len(i.Repo.Context.Checkouts) {
		return inventory.RepoCheckout{}, false
	}
	return i.Repo.Context.Checkouts[i.CheckoutIndex], true
}

// visibleRepoItems flattens expanded worktrees under their sorted parent.
// Children never participate in repo sorting and therefore cannot drift away
// from the repository they belong to.
func (m Model) visibleRepoItems() []repoItem {
	repos := m.visibleRepos()
	out := make([]repoItem, 0, len(repos))
	for _, r := range repos {
		out = append(out, repoItem{Repo: r, CheckoutIndex: -1})
		if !m.repoExpanded(r) {
			continue
		}
		for i := 1; i < len(r.Context.Checkouts); i++ {
			out = append(out, repoItem{Repo: r, CheckoutIndex: i})
		}
	}
	return out
}

func repoKey(r RepoRow) string {
	if r.Repo.CommonDir != "" {
		return r.Repo.CommonDir
	}
	return r.Repo.Path
}

func (m Model) repoExpanded(r RepoRow) bool {
	key := repoKey(r)
	for _, expanded := range m.expandedRepos {
		if expanded == key {
			return true
		}
	}
	return false
}

func (m *Model) toggleRepo(r RepoRow) {
	key := repoKey(r)
	for i, expanded := range m.expandedRepos {
		if expanded == key {
			m.expandedRepos = append(append([]string(nil), m.expandedRepos[:i]...), m.expandedRepos[i+1:]...)
			return
		}
	}
	m.expandedRepos = append(append([]string(nil), m.expandedRepos...), key)
}

// visibleRemotes filters the combined forge inventory. Local clones sort
// first, then recently updated repositories.
func (m Model) visibleRemotes() []RemoteRow {
	var out []RemoteRow
	for _, r := range m.remotes {
		if r.matches(m.filter) {
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

func (m Model) visibleFleet() []FleetRow {
	var out []FleetRow
	for _, row := range m.fleet {
		if matches(row.searchText(), m.filter) {
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftName, rightName := "", ""
		if out[i].Repository != nil {
			leftName = strings.ToLower(out[i].Repository.Display)
		}
		if out[j].Repository != nil {
			rightName = strings.ToLower(out[j].Repository.Display)
		}
		if leftName != rightName {
			return leftName < rightName
		}
		return out[i].Host < out[j].Host
	})
	return out
}

func (m Model) visibleSkills() []agentskill.Skill {
	var out []agentskill.Skill
	for _, row := range m.skills {
		if skillMatches(row, m.filter) {
			out = append(out, row)
		}
	}
	return out
}

func skillMatches(row agentskill.Skill, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, term := range strings.Fields(query) {
		key, value, structured := strings.Cut(term, ":")
		if structured {
			switch key {
			case "scope":
				if !strings.Contains(strings.ToLower(string(row.Scope)), value) {
					return false
				}
				continue
			case "agent":
				if !strings.Contains(strings.ToLower(strings.Join(row.Agents, " ")), value) {
					return false
				}
				continue
			case "update":
				if !strings.Contains(strings.ToLower(string(row.UpdateStatus)), value) {
					return false
				}
				continue
			}
		}
		search := strings.ToLower(strings.Join([]string{
			row.Name, string(row.Scope), row.Source, row.SourceURL,
			string(row.ManagedBy), string(row.UpdateStatus), strings.Join(row.Agents, " "),
		}, " "))
		if !strings.Contains(search, term) {
			return false
		}
	}
	return true
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
	case "size":
		switch {
		case a.Usage != nil && b.Usage == nil:
			return -1
		case a.Usage == nil && b.Usage != nil:
			return 1
		case a.Usage != nil && b.Usage != nil && a.Usage.OwnedBytes > b.Usage.OwnedBytes:
			return -1
		case a.Usage != nil && b.Usage != nil && a.Usage.OwnedBytes < b.Usage.OwnedBytes:
			return 1
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
		return len(m.visibleRepoItems())
	case ViewFleet:
		return len(m.visibleFleet())
	case ViewTries:
		return len(m.visibleTries())
	case ViewRemote:
		return len(m.visibleRemotes())
	case ViewSkills:
		return len(m.visibleSkills())
	default:
		return len(m.visibleTasks())
	}
}

// at is the cursor position in the current view.
func (m Model) at() int {
	switch m.view {
	case ViewRepos:
		return m.repoCursor
	case ViewFleet:
		return m.fleetCursor
	case ViewTries:
		return m.tryCursor
	case ViewRemote:
		return m.remoteCursor
	case ViewSkills:
		return m.skillCursor
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
	case ViewFleet:
		m.fleetCursor = i
	case ViewTries:
		m.tryCursor = i
	case ViewRemote:
		m.remoteCursor = i
	case ViewSkills:
		m.skillCursor = i
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
	items := m.visibleRepoItems()
	if m.at() >= len(items) || items[m.at()].child() {
		return RepoRow{}, false
	}
	return items[m.at()].Repo, true
}

func (m Model) currentRepoItem() (repoItem, bool) {
	if m.view != ViewRepos {
		return repoItem{}, false
	}
	items := m.visibleRepoItems()
	if m.at() >= len(items) {
		return repoItem{}, false
	}
	return items[m.at()], true
}

// currentTry returns the selected experiment row.
func (m Model) currentTry() (TryRow, bool) {
	if m.view != ViewTries {
		return TryRow{}, false
	}
	rows := m.visibleTries()
	if m.at() >= len(rows) {
		return TryRow{}, false
	}
	return rows[m.at()], true
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

func (m Model) currentFleet() (FleetRow, bool) {
	if m.view != ViewFleet {
		return FleetRow{}, false
	}
	rows := m.visibleFleet()
	if m.at() >= len(rows) {
		return FleetRow{}, false
	}
	return rows[m.at()], true
}
func (m Model) currentSkill() (agentskill.Skill, bool) {
	if m.view != ViewSkills {
		return agentskill.Skill{}, false
	}
	rows := m.visibleSkills()
	if m.at() >= len(rows) {
		return agentskill.Skill{}, false
	}
	return rows[m.at()], true
}

// matchRemoteLocals fills cached remote rows from the freshly loaded local
// inventory without another scan.
func (m *Model) matchRemoteLocals() {
	byRemote := map[string]RepoRow{}
	ambiguous := map[string]bool{}
	for _, r := range m.repos {
		if r.RemoteName == "" {
			continue
		}
		key := string(r.RemoteForge) + "/" + strings.ToLower(r.RemoteName)
		if ambiguous[key] {
			continue
		}
		if _, exists := byRemote[key]; exists {
			delete(byRemote, key)
			ambiguous[key] = true
			continue
		}
		byRemote[key] = r
	}
	for i := range m.remotes {
		key := string(m.remotes[i].Repo.Forge) + "/" + strings.ToLower(m.remotes[i].Repo.FullName)
		if local, ok := byRemote[key]; ok {
			m.remotes[i].LocalPath = local.Repo.Path
			m.remotes[i].LocalName = local.Repo.Display()
			m.remotes[i].LocalKind = catalog.KindRepository
			if local.Asset != nil {
				m.remotes[i].LocalKind = local.Asset.Kind
			}
		} else {
			m.remotes[i].LocalPath, m.remotes[i].LocalName, m.remotes[i].LocalKind = "", "", ""
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
	if item, ok := m.currentRepoItem(); ok {
		if checkout, child := item.checkout(); child {
			if checkout.Exists && !checkout.Worktree.Prunable {
				return checkout.Worktree.Path
			}
			return ""
		}
		return item.Repo.Repo.Path
	}
	if r, ok := m.currentTry(); ok && r.Present() {
		return r.Item.Live.CurrentPath
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
		if msg.rowsSet && msg.rows != nil {
			m.rows = msg.rows
		}
		if msg.reposSet && msg.repos != nil {
			m.repos = msg.repos
			m.matchRemoteLocals()
		}
		if msg.triesSet {
			m.tries = msg.tries
		}
		if msg.remoteSet {
			m.remotes, m.remotesLoaded, m.remotesLoading = msg.remotes, true, false
			m.remotesStale = msg.err != nil
		}
		m.err = msg.err
		m.forceSizeReload = false
		m.setAt(m.at())
		return m.beginSizeLoad(msg.forceSizes)

	case sizeMsg:
		if msg.loadID == 0 || msg.loadID != m.sizeLoad.ID {
			return m, nil
		}
		if msg.done {
			m.sizeLoad = diskusage.Load{}
			return m, nil
		}
		m.applySizeResult(msg.result)
		return m, waitForSize(m.sizeLoad)

	case remoteMsg:
		m.remotes, m.remotesLoaded, m.remotesLoading = msg.rows, true, false
		m.remotesStale = msg.err != nil
		m.err = msg.err
		m.status = ""
		m.setAt(m.at())
		return m, nil

	case fleetMsg:
		m.fleet, m.fleetLoaded, m.fleetLoading = msg.rows, true, false
		m.err = msg.err
		m.status = ""
		m.setAt(m.at())
		return m, nil

	case skillsMsg:
		m.skillsLoading, m.skillsChecking = false, false
		if msg.loaded {
			m.skillsLoaded = true
		}
		if msg.rows != nil {
			m.skills = msg.rows
		}
		m.err = msg.err
		switch {
		case msg.err != nil:
			m.status = ""
		case msg.status != "":
			m.status = msg.status
		case msg.checked:
			m.status = "skill update check complete"
		default:
			m.status = ""
		}
		m.setAt(m.at())
		return m, nil

	case skillProcessMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			return m, nil
		}
		m.err, m.skillsLoading = nil, true
		if msg.action == "update" {
			m.status = "reloading and verifying " + msg.name + "…"
			return m, m.reloadUpdatedSkill(msg.name, msg.scope)
		}
		m.status = "reloading agent skills…"
		return m, m.reloadSkills()

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

	case triesMsg:
		if msg.triesSet {
			m.tries = msg.tries
		}
		if msg.reposSet && msg.repos != nil {
			m.repos = msg.repos
			m.matchRemoteLocals()
		}
		if msg.err != nil {
			m.err = msg.err
		}
		m.setAt(m.at())
		return m.beginSizeLoad(false)

	case tryActionMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			if msg.result.RefreshRepos {
				return m, m.reloadTries(true)
			}
			return m, nil
		}
		m.err, m.status = nil, msg.result.Status
		if msg.result.CD != "" {
			m.chosen, m.quitting = msg.result.CD, true
			return m, tea.Quit
		}
		if msg.result.RuntimeHandle != "" {
			m.activate, m.quitting = msg.result.RuntimeHandle, true
			return m, tea.Quit
		}
		return m, m.reloadTries(msg.result.RefreshRepos)

	case noteListMsg:
		if msg.request != m.noteRequest || msg.targetKey != m.noteTarget.Key() || !m.noteMode() {
			return m, nil // stale result for a repository/overlay already left
		}
		m.noteLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.notes, m.err = msg.notes, nil
		if msg.repos != nil {
			m.repos = msg.repos
			m.matchRemoteLocals()
		}
		m.clampNoteCursor()
		return m, nil

	case noteActionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.status = nil, msg.status
		if msg.returnBrowse {
			m.mode, m.noteLoading = modeNoteBrowse, true
			return m.beginNoteLoad(true)
		}
		m.mode = modeList
		return m, m.reload()

	case actionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.status = nil, msg.status
		m.forceSizeReload = msg.forceSizes
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
		if msg.activate != "" {
			m.activate, m.quitting = msg.activate, true
			return m, tea.Quit
		}
		if m.view == ViewRemote {
			// Clone already updated the selected row; only local repo/task
			// state needs a refresh. A network round-trip here would make a
			// successful clone feel hung for several seconds.
			return m, m.reload()
		}
		return m, m.reload()

	case copyMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
		} else {
			m.err, m.status = nil, msg.status
		}
		return m, nil

	case tea.KeyMsg:
		if m.mode == modeNoteBrowse || m.mode == modeNoteAdd ||
			m.mode == modeNoteSearch || m.mode == modeNoteConfirmDelete {
			return m.updateNotes(msg)
		}
		if m.overlay.kind != overlayNone {
			return m.updateOverlay(msg)
		}
		if m.mode == modeStats {
			return m.updateStats(msg)
		}
		if m.mode == modeCopy {
			return m.updateCopy(msg)
		}
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeEditNext, modeConfirmPark, modeStartTask, modeStartDirect, modeConfirmClone, modeConfirmSkillUpdate:
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

	case "?":
		return m.openHelpOverlay(), nil

	case "n":
		if m.view == ViewTries {
			return m.openTryForm(TryCreate, TryRow{})
		}
		if target, ok := m.selectedNoteTarget(); ok {
			return m.openNoteAdd(target, false)
		}
		return m, nil

	case "N":
		if target, ok := m.selectedNoteTarget(); ok {
			return m.openNotes(target)
		}
		return m, nil

	case "r":
		if m.view == ViewFleet {
			m.fleetLoading = true
			m.status = "refreshing fleet…"
			return m, m.reloadFleet()
		}
		if m.view == ViewSkills {
			m.status, m.err = "reloading local agent skills…", nil
			m.skillsLoading = true
			return m, m.reloadSkills()
		}
		m.status = "reloading config + data…"
		m.forceSizeReload = true
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
		if m.view == ViewSkills {
			if m.actions.AddSkill == nil {
				return m, nil
			}
			proc, err := m.actions.AddSkill()
			if err != nil {
				m.err = err
				return m, nil
			}
			m.status = "opening interactive skill installer…"
			return m, tea.ExecProcess(proc, func(err error) tea.Msg {
				return skillProcessMsg{action: "add", err: err}
			})
		}
		if m.view == ViewTries {
			m.showAllTries = !m.showAllTries
			m.status = fmt.Sprintf("Try history visible: %v", m.showAllTries)
			m.setAt(0)
			return m, m.reloadTries(false)
		}
		m.showDone, m.states = !m.showDone, nil
		m.setAt(0)

	case "enter", "o":
		return m, m.openSelected()

	case " ":
		if m.view == ViewRepos {
			item, ok := m.currentRepoItem()
			if !ok || item.Repo.Worktrees == 0 {
				return m, nil
			}
			if item.Repo.Context.WorktreeErr != nil {
				m.err = item.Repo.Context.WorktreeErr
				return m, nil
			}
			if item.child() {
				// The parent is the nearest preceding non-child item.
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
		if row, ok := m.currentTry(); ok {
			return m.openTryMenu(row), nil
		}
		return m, nil

	case "m":
		if row, ok := m.currentRepo(); ok {
			return m.openRepoForm(row)
		}
		return m, nil

	case "y":
		if _, ok := m.currentRepoItem(); !ok {
			return m, nil
		}
		m.mode, m.err, m.status = modeCopy, nil, ""
		return m, nil

	case "p":
		if _, ok := m.currentTask(); !ok {
			return m, nil
		}
		return m.prompt(modeConfirmPark, "", "what to do when you come back")

	case "c":
		if m.view == ViewSkills {
			m.status, m.err = "checking skill sources…", nil
			m.skillsChecking = true
			return m, m.checkSkills(append([]agentskill.Skill(nil), m.skills...))
		}
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

	case "u":
		row, ok := m.currentSkill()
		if !ok {
			return m, nil
		}
		if row.ManagedBy != agentskill.ManagedBySkills {
			m.err = fmt.Errorf("%s is not managed by the skills CLI", row.Name)
			return m, nil
		}
		m.mode = modeConfirmSkillUpdate
		return m, nil

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
		switch m.view {
		case ViewRepos:
			orders := []string{"activity", "latest", "name", "git", "size", "tasks"}
			m.actions.RepoSort = nextSort(m.actions.RepoSort, orders)
			m.status = "repo sort: " + m.actions.RepoSort
		case ViewTries:
			m.trySort = nextSort(m.trySort, []string{"activity", "name", "phase", "size"})
			m.status = "Try sort: " + m.trySort
		default:
			return m, nil
		}
		m.setAt(0)
		return m, nil

	case "R":
		switch m.view {
		case ViewRepos:
			m.actions.RepoReverse = !m.actions.RepoReverse
			m.status = fmt.Sprintf("repo sort reversed: %v", m.actions.RepoReverse)
		case ViewTries:
			m.tryReverse = !m.tryReverse
			m.status = fmt.Sprintf("Try sort reversed: %v", m.tryReverse)
		default:
			return m, nil
		}
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

func (m Model) selectedNoteTarget() (NoteTarget, bool) {
	if row, ok := m.currentTask(); ok {
		return NoteTarget{Repo: repo.Repo{
			Name: row.Task.Repo, Path: row.Task.RepoPath,
			RealPath: row.Task.RepoPath, HasGit: true,
		}}, true
	}
	if item, ok := m.currentRepoItem(); ok {
		id := ""
		if item.Repo.Asset != nil {
			id = item.Repo.Asset.ID
		}
		return NoteTarget{CatalogID: id, Repo: item.Repo.Repo}, true
	}
	if row, ok := m.currentRemote(); ok && row.Cloned() && row.LocalKind != catalog.KindTry {
		return NoteTarget{Repo: repo.Repo{
			Name: row.Repo.Name, Path: row.LocalPath,
			RealPath: row.LocalPath, HasGit: true,
		}}, true
	}
	return NoteTarget{}, false
}

func (m Model) openNoteAdd(target NoteTarget, returnToBrowse bool) (tea.Model, tea.Cmd) {
	m.noteTarget, m.noteReturnToBrowse = target, returnToBrowse
	m.mode, m.err = modeNoteAdd, nil
	m.input.SetValue("")
	m.input.Placeholder = "quick thought"
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func (m Model) openNotes(target NoteTarget) (tea.Model, tea.Cmd) {
	m.noteTarget, m.noteQuery = target, ""
	m.noteCursor, m.noteExpanded = 0, false
	m.mode, m.noteLoading, m.err = modeNoteBrowse, true, nil
	return m.beginNoteLoad(false)
}

func (m Model) beginNoteLoad(refreshRepos bool) (Model, tea.Cmd) {
	m.noteRequest++
	request, targetKey, target := m.noteRequest, m.noteTarget.Key(), m.noteTarget
	query := m.noteQuery
	m.noteLoading = true
	return m, func() tea.Msg {
		if m.actions.Notes.List == nil {
			return noteListMsg{request: request, targetKey: targetKey}
		}
		var (
			notes []*note.Note
			err   error
		)
		if query != "" && m.actions.Notes.Search != nil {
			notes, err = m.actions.Notes.Search(context.Background(), target, query)
		} else {
			notes, err = m.actions.Notes.List(context.Background(), target)
		}
		msg := noteListMsg{notes: notes, request: request, targetKey: targetKey, err: err}
		if refreshRepos && m.actions.ReloadRepos != nil {
			msg.repos, err = m.actions.ReloadRepos(context.Background())
			if msg.err == nil {
				msg.err = err
			}
		}
		return msg
	}
}

func (m Model) currentNote() (*note.Note, bool) {
	if m.noteCursor < 0 || m.noteCursor >= len(m.notes) {
		return nil, false
	}
	return m.notes[m.noteCursor], true
}

func (m *Model) clampNoteCursor() {
	switch {
	case len(m.notes) == 0:
		m.noteCursor = 0
	case m.noteCursor >= len(m.notes):
		m.noteCursor = len(m.notes) - 1
	case m.noteCursor < 0:
		m.noteCursor = 0
	}
}

func (m Model) updateNotes(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeNoteBrowse:
		switch msg.String() {
		case "esc", "N", "q":
			m.mode, m.notes, m.noteQuery, m.err = modeList, nil, "", nil
			m.noteRequest++
			return m, nil
		case "j", "down":
			m.noteCursor++
			m.noteExpanded = false
			m.clampNoteCursor()
		case "k", "up":
			m.noteCursor--
			m.noteExpanded = false
			m.clampNoteCursor()
		case "g", "home":
			m.noteCursor, m.noteExpanded = 0, false
		case "G", "end":
			m.noteCursor, m.noteExpanded = len(m.notes)-1, false
			m.clampNoteCursor()
		case "enter":
			if _, ok := m.currentNote(); ok {
				m.noteExpanded = !m.noteExpanded
			}
		case "a", "n":
			return m.openNoteAdd(m.noteTarget, true)
		case "/":
			m.mode = modeNoteSearch
			m.input.SetValue(m.noteQuery)
			m.input.Placeholder = "search body, tags, repo"
			m.input.CursorEnd()
			return m, m.input.Focus()
		case "d":
			if _, ok := m.currentNote(); ok {
				m.mode = modeNoteConfirmDelete
			}
		case "e":
			n, ok := m.currentNote()
			if !ok || m.actions.Notes.Edit == nil {
				return m, nil
			}
			edit, err := m.actions.Notes.Edit(n)
			if err != nil {
				m.err = err
				return m, nil
			}
			return m, tea.ExecProcess(edit.Command, func(runErr error) tea.Msg {
				if edit.Complete != nil {
					runErr = edit.Complete(runErr)
				}
				return noteActionMsg{status: "updated note " + n.ID[:8], returnBrowse: true, err: runErr}
			})
		}
		return m, nil

	case modeNoteAdd:
		switch msg.String() {
		case "esc":
			m.input.Blur()
			if m.noteReturnToBrowse {
				m.mode = modeNoteBrowse
			} else {
				m.mode = modeList
			}
			return m, nil
		case "enter":
			body := strings.TrimSpace(m.input.Value())
			if body == "" {
				m.err = fmt.Errorf("note body is empty")
				return m, nil
			}
			m.input.Blur()
			returnBrowse := m.noteReturnToBrowse
			return m, func() tea.Msg {
				status, err := m.actions.Notes.Add(context.Background(), m.noteTarget, body)
				return noteActionMsg{status: status, returnBrowse: returnBrowse, err: err}
			}
		}
		input, cmd := m.input.Update(msg)
		m.input = input
		return m, cmd

	case modeNoteSearch:
		switch msg.String() {
		case "esc":
			m.input.Blur()
			m.noteQuery, m.mode, m.noteLoading = "", modeNoteBrowse, true
			return m.beginNoteLoad(false)
		case "enter":
			m.noteQuery = strings.TrimSpace(m.input.Value())
			m.input.Blur()
			m.mode, m.noteLoading, m.noteCursor = modeNoteBrowse, true, 0
			return m.beginNoteLoad(false)
		}
		input, cmd := m.input.Update(msg)
		m.input = input
		return m, cmd

	case modeNoteConfirmDelete:
		switch msg.String() {
		case "esc", "n":
			m.mode = modeNoteBrowse
		case "y", "Y":
			n, ok := m.currentNote()
			if !ok || m.actions.Notes.Delete == nil {
				m.mode = modeNoteBrowse
				return m, nil
			}
			return m, func() tea.Msg {
				status, err := m.actions.Notes.Delete(context.Background(), n)
				return noteActionMsg{status: status, returnBrowse: true, err: err}
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateCopy(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		m.mode = modeList
		return m, nil
	}
	item, ok := m.currentRepoItem()
	if !ok {
		m.mode = modeList
		return m, nil
	}
	checkoutIndex := -1
	if item.child() {
		checkoutIndex = item.CheckoutIndex
	}

	var payload, label string
	switch msg.String() {
	case "y":
		payload = inventory.FormatRepoContext(item.Repo.Context, checkoutIndex)
		if item.child() {
			label = "worktree context"
		} else {
			label = "repo context"
		}
	case "p":
		label = "checkout path"
		if checkoutIndex >= 0 {
			payload = item.Repo.Context.Checkouts[checkoutIndex].Worktree.Path
		} else {
			payload = item.Repo.Repo.Path
		}
	case "b":
		label = "branch"
		if checkoutIndex >= 0 {
			payload = item.Repo.Context.Checkouts[checkoutIndex].Branch()
		} else if main, found := item.Repo.Context.Main(); found {
			payload = main.Branch()
		} else {
			payload = item.Repo.Status.Branch
		}
		if payload == "" {
			m.mode, m.err = modeList, fmt.Errorf("selected checkout has detached HEAD")
			return m, nil
		}
	case "s":
		label = "runtime sessions"
		payload = inventory.FormatSessions(item.Repo.Context, checkoutIndex)
		if payload == "" {
			m.mode, m.err = modeList, fmt.Errorf("no live runtime sessions to copy")
			return m, nil
		}
	case "w":
		label = "worktree paths"
		payload = inventory.LinkedWorktreePaths(item.Repo.Context)
		if payload == "" {
			m.mode, m.err = modeList, fmt.Errorf("no linked worktrees to copy")
			return m, nil
		}
	default:
		m.mode, m.err = modeList, fmt.Errorf("unknown copy key %q", msg.String())
		return m, nil
	}

	m.mode = modeList
	if m.actions.Copy == nil {
		m.err = fmt.Errorf("clipboard integration is unavailable; use `dev repo context`")
		return m, nil
	}
	return m, func() tea.Msg {
		err := m.actions.Copy(payload)
		if err != nil {
			return copyMsg{err: fmt.Errorf("copy %s: %w; use `dev repo context` as a fallback", label, err)}
		}
		return copyMsg{status: "copied " + label}
	}
}

func (m Model) selectedRepoName() string {
	if r, ok := m.currentTask(); ok {
		return r.Task.Repo
	}
	if item, ok := m.currentRepoItem(); ok {
		return item.Repo.Repo.Name
	}
	if r, ok := m.currentTry(); ok && r.Item.Live.Repo != nil {
		return r.Item.Live.Repo.Name
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

// afterViewSwitch lazily loads optional inventories only when their view is
// first opened, so starting the dashboard never waits on the network, a forge
// CLI, or Node tooling.
func (m Model) afterViewSwitch() (tea.Model, tea.Cmd) {
	m.setAt(m.at())
	if m.view == ViewFleet && !m.fleetLoaded && !m.fleetLoading {
		m.fleetLoading = true
		m.status = "loading configured dev hosts…"
		return m, m.reloadFleet()
	}
	if m.view == ViewRemote && (!m.remotesLoaded || m.remotesStale) && !m.remotesLoading {
		m.remotesLoading = true
		m.status = "refreshing remote repositories…"
		return m, m.reloadRemote()
	}
	if m.view == ViewSkills && !m.skillsLoaded && !m.skillsLoading {
		m.skillsLoading = true
		m.status = "loading local agent skills…"
		return m, m.reloadSkills()
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
			opened, path, err := m.actions.CloneRemote(context.Background(), r)
			return actionMsg{
				status: opened.Status, activate: opened.RuntimeHandle,
				remoteName: r.Repo.FullName, localPath: path, err: err,
			}
		}

	case modeConfirmSkillUpdate:
		row, ok := m.currentSkill()
		if !ok || m.actions.UpdateSkill == nil {
			return nil
		}
		proc, err := m.actions.UpdateSkill(row)
		if err != nil {
			return func() tea.Msg { return skillProcessMsg{action: "update", err: err} }
		}
		return tea.ExecProcess(proc, func(err error) tea.Msg {
			return skillProcessMsg{action: "update", name: row.Name, scope: row.Scope, err: err}
		})
	}
	return nil
}

func (m Model) openSelected() tea.Cmd {
	cdWanted := m.actions.Runtime != nil && m.actions.Runtime.Name() == "none"

	if row, ok := m.currentTask(); ok {
		t := row.Task
		dir := row.Checkout
		return func() tea.Msg {
			opened, err := m.actions.Open(context.Background(), t)
			cd := ""
			if err == nil && cdWanted {
				cd = dir
			}
			return actionMsg{status: opened.Status, cd: cd, activate: opened.RuntimeHandle, err: err}
		}
	}
	if r, ok := m.currentRepo(); ok {
		dir := r.Repo.Path
		return func() tea.Msg {
			opened, err := m.actions.OpenRepo(context.Background(), r)
			cd := ""
			if err == nil && cdWanted {
				cd = dir
			}
			return actionMsg{status: opened.Status, cd: cd, activate: opened.RuntimeHandle, err: err}
		}
	}
	if item, ok := m.currentRepoItem(); ok && item.child() {
		checkout, _ := item.checkout()
		dir := checkout.Worktree.Path
		return func() tea.Msg {
			if !checkout.Exists || checkout.Worktree.Prunable {
				return actionMsg{err: fmt.Errorf("worktree checkout is missing or prunable: %s", dir)}
			}
			if m.actions.OpenCheckout == nil {
				return actionMsg{err: fmt.Errorf("opening linked worktrees is unavailable")}
			}
			opened, err := m.actions.OpenCheckout(context.Background(), item.Repo, checkout)
			cd := ""
			if err == nil && cdWanted {
				cd = dir
			}
			return actionMsg{status: opened.Status, cd: cd, activate: opened.RuntimeHandle, err: err}
		}
	}
	if r, ok := m.currentTry(); ok {
		if !r.Present() {
			return func() tea.Msg {
				return tryActionMsg{err: fmt.Errorf("Try %s is %s on this host", r.Item.DisplayName(), r.Where())}
			}
		}
		return m.applyTry(TryRequest{Action: TryOpen, ID: r.Item.ID})
	}
	if r, ok := m.currentRemote(); ok {
		if !r.Cloned() {
			return nil // c is the explicit clone action
		}
		dir := r.LocalPath
		return func() tea.Msg {
			opened, err := m.actions.OpenRemote(context.Background(), r)
			cd := ""
			if err == nil && cdWanted {
				cd = dir
			}
			return actionMsg{status: opened.Status, cd: cd, activate: opened.RuntimeHandle, err: err}
		}
	}
	if row, ok := m.currentFleet(); ok && row.Repository != nil && m.actions.OpenFleet != nil {
		process, err := m.actions.OpenFleet(context.Background(), row)
		if err != nil {
			return func() tea.Msg { return actionMsg{err: err} }
		}
		return tea.ExecProcess(process, func(err error) tea.Msg {
			if err != nil {
				return actionMsg{err: fmt.Errorf("fleet open: %w", err)}
			}
			return actionMsg{status: "returned from " + row.Host}
		})
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
			return actionMsg{status: "back from " + t.Name, forceSizes: true}
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
	repoCount := 0
	for _, row := range m.repos {
		if !row.IsTry() {
			repoCount++
		}
	}
	if repoCount > 0 {
		parts = append(parts, fmt.Sprintf("%d repos", repoCount))
	}
	if len(m.tries) > 0 {
		parts = append(parts, fmt.Sprintf("%d tries", len(m.tries)))
	}
	if m.remotesLoaded {
		parts = append(parts, fmt.Sprintf("%d remote", len(m.remotes)))
	}
	if len(m.fleet) > 0 {
		parts = append(parts, fmt.Sprintf("%d fleet", len(m.fleet)))
	}
	if m.skillsLoaded {
		project, global := 0, 0
		for _, row := range m.skills {
			if row.Scope == agentskill.ScopeProject {
				project++
			} else if row.Scope == agentskill.ScopeGlobal {
				global++
			}
		}
		parts = append(parts, fmt.Sprintf("%dP/%dG skills", project, global))
	}
	if len(parts) == 0 {
		return "no tasks"
	}
	return strings.Join(parts, "   ")
}

func contract(p string) string { return config.Contract(p) }

func (m Model) noteSummary(repoPath, repoName string) (int, *note.Note) {
	for _, row := range m.repos {
		if row.Repo.Path == repoPath || row.Repo.RealPath == repoPath ||
			(row.Repo.Name == repoName && repoName != "") {
			return row.NoteCount, row.LatestNote
		}
	}
	return 0, nil
}
