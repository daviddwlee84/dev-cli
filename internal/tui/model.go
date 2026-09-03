package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
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
	// ViewMCP lists static MCP server declarations across agent formats.
	ViewMCP
)

// Views is the cycle order.
var Views = []View{ViewTasks, ViewRepos, ViewFleet, ViewTries, ViewRemote, ViewSkills, ViewMCP}

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
	case ViewMCP:
		return "mcp"
	default:
		return "tasks"
	}
}

// Actions is everything the dashboard can do. The TUI owns no domain logic:
// it calls back into the same code paths the non-interactive commands use, so
// behaviour cannot diverge between them.
// LoadWarning carries nonfatal inventory diagnostics without making an accepted
// snapshot stale or retrying it on every tab visit.
type LoadWarning struct{ Message string }

func (w LoadWarning) Error() string { return w.Message }

func splitLoadWarning(err error) (string, error) {
	var warning LoadWarning
	if errors.As(err, &warning) {
		return warning.Message, nil
	}
	return "", err
}

type Actions struct {
	// Reload re-reads the task inventory.
	Reload func(ctx context.Context) ([]inventory.Row, error)
	// ReloadRepos re-reads the repository list.
	ReloadRepos func(ctx context.Context) ([]RepoRow, error)
	// ReloadFleet fans out to configured dev hosts. It is lazy like REMOTE.
	ReloadFleet          func(ctx context.Context) ([]FleetRow, error)
	ReloadFleetWithRepos func(ctx context.Context, repos []RepoRow) ([]FleetRow, error)
	// ReloadRemote queries configured forge CLIs. It is lazy: the network is untouched
	// until the REMOTE view is opened. Production supplies accepted local rows so
	// matching does not trigger another repository discovery.
	ReloadRemote          func(ctx context.Context) ([]RemoteRow, error)
	ReloadRemoteWithRepos func(ctx context.Context, repos []RepoRow) ([]RemoteRow, error)
	// ReloadSkills reads local project/global skill state without contacting sources.
	ReloadSkills          func(ctx context.Context) ([]agentskill.Skill, error)
	ReloadSkillsWithRepos func(ctx context.Context, repos []RepoRow) ([]agentskill.Skill, error)
	// ReloadMCP reads static declarations only; it never starts or probes servers.
	ReloadMCP          func(ctx context.Context) ([]agentmcp.Declaration, error)
	ReloadMCPWithRepos func(ctx context.Context, repos []RepoRow) ([]agentmcp.Declaration, error)
	// Cache seeds are local-only and asynchronous. Live REMOTE/FLEET work remains
	// lazy until its view is requested.
	LoadRemoteCache func(ctx context.Context) RemoteCacheResult
	LoadFleetCache  func(ctx context.Context) FleetCacheResult
	// AfterFirstView performs optional background work only after the initial
	// application frame has been computed.
	AfterFirstView func(ctx context.Context)
	// CheckSkills performs the explicitly requested read-only network comparison.
	CheckSkills func(ctx context.Context, rows []agentskill.Skill) []agentskill.Skill
	// AddSkill and UpdateSkill return locked interactive processes for tea to suspend around.
	AddSkill    func() (*agentskill.MutationCommand, error)
	UpdateSkill func(row agentskill.Skill) (*agentskill.MutationCommand, error)
	// Repos and Tries group asset-specific metadata and lifecycle actions.
	Repos RepoActions
	Tries TryActions
	Notes NoteActions
	Sizes SizeActions
	Local LocalActions
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
	// EditFleetConfig does the same for remotes.toml, which is a separate file
	// with its own schema — the FLEET view's `e` would otherwise open the
	// config that has nothing to do with what it is showing.
	EditFleetConfig func() (*exec.Cmd, error)
	// ValidateFleetConfig reparses remotes.toml after an edit. It rejects
	// unknown fields, so a typo has to surface rather than silently drop a host.
	ValidateFleetConfig func() error
	// ReloadConfig reparses config and returns live-updatable preferences.
	// Runtime backend changes need a TUI restart, which status explains.
	ReloadConfig func(ctx context.Context) (ConfigUpdate, string, error)
	// RepoColumns and sorting are live-updatable display policy.
	RepoColumns []string
	RepoSort    string
	RepoReverse bool
	// Tools are external programs the dashboard hands the terminal to.
	Tools []Tool
}

// OpenResult separates the status text rendered by the dashboard from the
// opaque runtime handle activated only after Bubble Tea leaves its alternate
// screen.
type OpenResult struct {
	Status        string
	Directory     string
	RuntimeHandle string
}

// ConfigUpdate is the subset of config a running TUI can safely apply without
// rebuilding its runtime backend.
type ConfigUpdate struct {
	// Apply publishes the prepared immutable App snapshot only after this config
	// generation is accepted by Update.
	Apply       func()
	Tools       []Tool
	RepoColumns []string
	RepoSort    string
	RepoReverse bool
}

// RemoteCacheResult and FleetCacheResult carry local startup seeds without
// exposing cache IO to the Bubble Tea model.
type RemoteCacheResult struct {
	Rows  []RemoteRow
	Found bool
	Stale bool
}

type FleetCacheResult struct {
	Rows  []FleetRow
	Found bool
}

// Tool is an external program launched in the selected row's directory.
//
// The dashboard suspends while one runs and redraws afterwards, so lazygit or
// a file manager feels like part of it rather than something you have to quit
// the dashboard to reach.
type ToolAvailability uint8

const (
	ToolUnknown ToolAvailability = iota
	ToolAvailable
	ToolUnavailable
)

type Tool struct {
	Key  string
	Name string
	// Command is the argv to run; the first element is looked up on PATH.
	Command []string
	// Probe is invoked only by an asynchronous command. Rendering reads the
	// resolved Availability value and never launches a process.
	Probe        func(context.Context) bool
	Availability ToolAvailability
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
	// trace is the one intentional shared pointer in the value-copied model. The
	// recorder is append-only, bounded and concurrency-safe; it never controls UI
	// behavior.
	trace *perftrace.Recorder
	// runContext bounds read/probe commands to this Bubble Tea program. Mutating
	// actions deliberately keep their own contexts and completion handling.
	runContext     context.Context
	loadContexts   [viewCount]context.Context
	loadCancels    [viewCount]context.CancelFunc
	configCancel   context.CancelFunc
	configContext  context.Context
	localCancel    context.CancelFunc
	localContext   context.Context
	firstViewReady chan struct{}
	firstViewOnce  *sync.Once

	view    View
	rows    []inventory.Row
	repos   []RepoRow
	tries   []TryRow
	remotes []RemoteRow
	fleet   []FleetRow
	skills  []agentskill.Skill
	mcp     []agentmcp.Declaration
	// Each fixed view owns value-copied request/readiness state. Optional views
	// stay lazy; no synthetic all-tabs-ready state exists.
	loads        [viewCount]viewLoadState
	viewErrors   [viewCount]error
	viewStatuses [viewCount]string
	initialLoad  bool
	// skillsChecking is a separate explicit network operation, not initial tab
	// readiness.
	skillsChecking         bool
	skillsInventoryErr     error
	skillsInventoryWarning string
	sizeLoad               diskusage.Load
	forceSizeReload        bool
	configGeneration       uint64
	localGeneration        uint64
	toolGeneration         uint64

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
	mcpCursor    int
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
	// showLocalFleet includes this machine in FLEET. It is hidden by default
	// because REPOS already provides the richer local inventory.
	showLocalFleet bool
	trySort        string
	tryReverse     bool

	mode              mode
	input             textinput.Model
	skillUpdateTarget agentskill.Skill
	stats             *StatsPanel
	overlay           overlayState

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

	m := Model{
		actions:        actions,
		rows:           rows,
		repos:          repos,
		input:          in,
		width:          100,
		height:         30,
		firstViewReady: make(chan struct{}),
		firstViewOnce:  &sync.Once{},
	}
	if len(actions.Tools) > 0 {
		m.toolGeneration = 1
	}
	if rows != nil {
		m.seedViewSnapshot(ViewTasks, perftrace.SourceLive, perftrace.FreshnessFresh, true)
	}
	if repos != nil {
		m.seedViewSnapshot(ViewRepos, perftrace.SourceLive, perftrace.FreshnessFresh, true)
	}
	return m
}

// WithTrace observes performance boundaries without controlling model state.
func (m Model) WithTrace(trace *perftrace.Recorder) Model {
	m.trace = trace
	return m
}

// WithContext bounds asynchronous read/probe commands to one dashboard run.
func (m Model) WithContext(ctx context.Context) Model {
	m.runContext = ctx
	return m
}

// WithRemotes seeds the lazy forge view from a fresh on-disk cache. The first
// switch is then instant; r still refreshes explicitly.
func (m Model) WithRemotes(rows []RemoteRow) Model {
	m.remotes = rows
	m.seedViewSnapshot(ViewRemote, perftrace.SourceCache, perftrace.FreshnessFresh, true)
	return m
}

// WithFleet seeds cached fleet rows while the first live refresh remains lazy.
func (m Model) WithFleet(rows []FleetRow) Model {
	m.fleet = rows
	m.seedViewSnapshot(ViewFleet, perftrace.SourceCache, perftrace.FreshnessStale, true)
	return m
}

// WithRemotesStale marks seeded rows for background refresh on first visit.
func (m Model) WithRemotesStale(stale bool) Model {
	if stale {
		m.loads[int(ViewRemote)].freshness = perftrace.FreshnessStale
	}
	return m
}

// WithTries seeds the Try view, primarily for tests and embedded callers. The
// production dashboard loads it with the other local inventories in Init.
func (m Model) WithTries(rows []TryRow) Model {
	m.tries = rows
	m.matchRemoteLocals()
	m.seedViewSnapshot(ViewTries, perftrace.SourceLive, perftrace.FreshnessFresh, true)
	return m
}

// BeginLoading makes Init load task, repository, and Try data asynchronously. This lets
// the alternate screen appear immediately instead of blocking on dozens of Git
// probes before Bubble Tea starts.
func (m Model) BeginLoading() Model {
	m.initialLoad = true
	m.beginLocalLoads(loadInitial)
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
	commands := []tea.Cmd{textinput.Blink}
	if m.initialLoad {
		commands = append(commands, m.reload())
	}
	if len(m.actions.Tools) > 0 {
		commands = append(commands, m.probeTools())
	}
	if m.actions.LoadRemoteCache != nil {
		commands = append(commands, m.loadRemoteCache())
	}
	if m.actions.LoadFleetCache != nil {
		commands = append(commands, m.loadFleetCache())
	}
	if m.actions.AfterFirstView != nil {
		commands = append(commands, m.runAfterFirstView())
	}
	return tea.Batch(commands...)
}

type reloadMsg struct {
	rows             []inventory.Row
	repos            []RepoRow
	tries            []TryRow
	remotes          []RemoteRow
	rowsSet          bool
	reposSet         bool
	triesSet         bool
	remoteSet        bool
	rowsValid        bool
	reposValid       bool
	triesValid       bool
	remoteValid      bool
	rowsGeneration   uint64
	reposGeneration  uint64
	triesGeneration  uint64
	remoteGeneration uint64
	rowsErr          error
	reposErr         error
	triesErr         error
	remoteErr        error
	forceSizes       bool
}

type remoteCacheMsg struct {
	generation uint64
	result     RemoteCacheResult
}

type fleetCacheMsg struct {
	generation uint64
	result     FleetCacheResult
}

type remoteMsg struct {
	generation  uint64
	rows        []RemoteRow
	valid       bool
	matchLocals bool
	err         error
}

type fleetMsg struct {
	generation uint64
	rows       []FleetRow
	valid      bool
	err        error
}
type skillsMsg struct {
	generation   uint64
	rows         []agentskill.Skill
	valid        bool
	loaded       bool
	checked      bool
	status       string
	warning      string
	inventoryErr error
	err          error
}

type mcpMsg struct {
	generation uint64
	rows       []agentmcp.Declaration
	valid      bool
	warning    string
	err        error
}

type toolsMsg struct {
	generation uint64
	tools      []Tool
}

type skillProcessMsg struct {
	action   string
	name     string
	lockName string
	scope    agentskill.Scope
	checkout string
	err      error
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

type fleetConfigEditedMsg struct{ err error }

type configMsg struct {
	generation    uint64
	update        ConfigUpdate
	status        string
	refreshRemote bool
	err           error
}

type noteListMsg struct {
	notes           []*note.Note
	repos           []RepoRow
	reposSet        bool
	reposValid      bool
	reposGeneration uint64
	reposErr        error
	targetKey       string
	request         uint64
	err             error
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
	tries           []TryRow
	triesSet        bool
	triesValid      bool
	triesGeneration uint64
	triesErr        error
	repos           []RepoRow
	reposSet        bool
	reposValid      bool
	reposGeneration uint64
	reposErr        error
}

type tryActionMsg struct {
	result TryActionResult
	err    error
}

type copyMsg struct {
	status string
	err    error
}

func traceOutcome(err error) perftrace.Outcome {
	switch {
	case err == nil:
		return perftrace.OutcomeSuccess
	case errors.Is(err, context.Canceled):
		return perftrace.OutcomeCanceled
	default:
		return perftrace.OutcomeFailed
	}
}

func snapshotValid[T any](rows []T, err error) bool { return err == nil || rows != nil }

func batchCommands(commands ...tea.Cmd) tea.Cmd {
	filtered := commands[:0]
	for _, command := range commands {
		if command != nil {
			filtered = append(filtered, command)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Batch(filtered...)
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

func (m Model) loadRemoteCache() tea.Cmd {
	generation := m.viewLoad(ViewRemote).generation
	return func() tea.Msg {
		finish := m.trace.Start(perftrace.TUICacheRemoteRead, perftrace.Fields{
			View: perftrace.ViewRemote, Generation: generation,
		})
		result := m.actions.LoadRemoteCache(m.baseContext())
		finish(perftrace.OutcomeSuccess)
		return remoteCacheMsg{generation: generation, result: result}
	}
}

func (m Model) loadFleetCache() tea.Cmd {
	generation := m.viewLoad(ViewFleet).generation
	return func() tea.Msg {
		finish := m.trace.Start(perftrace.TUICacheFleetRead, perftrace.Fields{
			View: perftrace.ViewFleet, Generation: generation,
		})
		result := m.actions.LoadFleetCache(m.baseContext())
		finish(perftrace.OutcomeSuccess)
		return fleetCacheMsg{generation: generation, result: result}
	}
}

func (m Model) probeTools() tea.Cmd {
	generation := m.toolGeneration
	tools := append([]Tool(nil), m.actions.Tools...)
	ctx := m.baseContext()
	return func() tea.Msg {
		finish := m.trace.Start(perftrace.TUIProducerTools, perftrace.Fields{Generation: generation})
		jobs := make(chan int, len(tools))
		for index := range tools {
			jobs <- index
		}
		close(jobs)
		var workers sync.WaitGroup
		for range min(2, len(tools)) {
			workers.Go(func() {
				for index := range jobs {
					available := true
					if tools[index].Probe != nil {
						available = tools[index].Probe(ctx)
					}
					if ctx.Err() != nil {
						tools[index].Availability = ToolUnknown
					} else if available {
						tools[index].Availability = ToolAvailable
					} else {
						tools[index].Availability = ToolUnavailable
					}
				}
			})
		}
		workers.Wait()
		finish(traceOutcome(ctx.Err()))
		return toolsMsg{generation: generation, tools: tools}
	}
}

func (m Model) reload() tea.Cmd {
	if m.actions.Local.Start != nil {
		return m.startLocalLoad()
	}
	return func() tea.Msg {
		ctx := m.viewContext(ViewTasks)
		out := reloadMsg{
			forceSizes:      m.forceSizeReload,
			rowsGeneration:  m.viewLoad(ViewTasks).generation,
			reposGeneration: m.viewLoad(ViewRepos).generation,
			triesGeneration: m.viewLoad(ViewTries).generation,
		}
		if m.actions.Reload != nil {
			finish := m.trace.Start(perftrace.TUIProducerTasks, perftrace.Fields{View: perftrace.ViewTasks})
			out.rows, out.rowsErr = m.actions.Reload(ctx)
			out.rowsSet, out.rowsValid = true, snapshotValid(out.rows, out.rowsErr)
			finish(traceOutcome(out.rowsErr))
		}
		if m.actions.ReloadRepos != nil {
			finish := m.trace.Start(perftrace.TUIProducerRepos, perftrace.Fields{View: perftrace.ViewRepos})
			out.repos, out.reposErr = m.actions.ReloadRepos(ctx)
			out.reposSet, out.reposValid = true, snapshotValid(out.repos, out.reposErr)
			finish(traceOutcome(out.reposErr))
		}
		if m.actions.Tries.Reload != nil {
			finish := m.trace.Start(perftrace.TUIProducerTries, perftrace.Fields{View: perftrace.ViewTries})
			out.tries, out.triesErr = m.actions.Tries.Reload(ctx, m.showAllTries)
			out.triesSet, out.triesValid = true, snapshotValid(out.tries, out.triesErr)
			finish(traceOutcome(out.triesErr))
		}
		return out
	}
}

func (m Model) reloadTries(includeRepos bool) tea.Cmd {
	return func() tea.Msg {
		tryCtx := m.viewContext(ViewTries)
		repoCtx := m.viewContext(ViewRepos)
		out := triesMsg{
			triesGeneration: m.viewLoad(ViewTries).generation,
			reposGeneration: m.viewLoad(ViewRepos).generation,
		}
		if m.actions.Tries.Reload != nil {
			out.tries, out.triesErr = m.actions.Tries.Reload(tryCtx, m.showAllTries)
			out.triesSet, out.triesValid = true, snapshotValid(out.tries, out.triesErr)
		}
		if includeRepos && m.actions.ReloadRepos != nil {
			out.repos, out.reposErr = m.actions.ReloadRepos(repoCtx)
			out.reposSet, out.reposValid = true, snapshotValid(out.repos, out.reposErr)
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
	generation := m.viewLoad(ViewRemote).generation
	locals := append([]RepoRow(nil), m.repos...)
	return func() tea.Msg {
		if m.actions.ReloadRemoteWithRepos == nil && m.actions.ReloadRemote == nil {
			return remoteMsg{generation: generation, valid: true}
		}
		finish := m.trace.Start(perftrace.TUIProducerRemote, perftrace.Fields{View: perftrace.ViewRemote, Generation: generation})
		var rows []RemoteRow
		var err error
		matchLocals := m.actions.ReloadRemoteWithRepos != nil
		if matchLocals {
			rows, err = m.actions.ReloadRemoteWithRepos(m.viewContext(ViewRemote), locals)
		} else {
			rows, err = m.actions.ReloadRemote(m.viewContext(ViewRemote))
		}
		finish(traceOutcome(err))
		return remoteMsg{
			generation: generation, rows: rows, valid: snapshotValid(rows, err),
			matchLocals: matchLocals, err: err,
		}
	}
}

func (m Model) reloadFleet() tea.Cmd {
	generation := m.viewLoad(ViewFleet).generation
	locals := append([]RepoRow(nil), m.repos...)
	return func() tea.Msg {
		if m.actions.ReloadFleetWithRepos == nil && m.actions.ReloadFleet == nil {
			return fleetMsg{generation: generation, valid: true}
		}
		finish := m.trace.Start(perftrace.TUIProducerFleet, perftrace.Fields{View: perftrace.ViewFleet, Generation: generation})
		var rows []FleetRow
		var err error
		if m.actions.ReloadFleetWithRepos != nil {
			rows, err = m.actions.ReloadFleetWithRepos(m.viewContext(ViewFleet), locals)
		} else {
			rows, err = m.actions.ReloadFleet(m.viewContext(ViewFleet))
		}
		finish(traceOutcome(err))
		return fleetMsg{generation: generation, rows: rows, valid: snapshotValid(rows, err), err: err}
	}
}
func (m Model) reloadSkills() tea.Cmd {
	generation := m.viewLoad(ViewSkills).generation
	locals := append([]RepoRow(nil), m.repos...)
	return func() tea.Msg {
		if m.actions.ReloadSkillsWithRepos == nil && m.actions.ReloadSkills == nil {
			return skillsMsg{generation: generation, valid: true, loaded: true}
		}
		finish := m.trace.Start(perftrace.TUIProducerSkills, perftrace.Fields{View: perftrace.ViewSkills, Generation: generation})
		var rows []agentskill.Skill
		var err error
		if m.actions.ReloadSkillsWithRepos != nil {
			rows, err = m.actions.ReloadSkillsWithRepos(m.viewContext(ViewSkills), locals)
		} else {
			rows, err = m.actions.ReloadSkills(m.viewContext(ViewSkills))
		}
		warning, err := splitLoadWarning(err)
		outcome := traceOutcome(err)
		if warning != "" {
			outcome = perftrace.OutcomePartial
		}
		finish(outcome)
		return skillsMsg{
			generation: generation, rows: rows, valid: snapshotValid(rows, err), loaded: true,
			warning: warning, inventoryErr: err, err: err,
		}
	}
}

func (m Model) reloadMCP() tea.Cmd {
	generation := m.viewLoad(ViewMCP).generation
	locals := append([]RepoRow(nil), m.repos...)
	return func() tea.Msg {
		if m.actions.ReloadMCPWithRepos == nil && m.actions.ReloadMCP == nil {
			return mcpMsg{generation: generation, valid: true}
		}
		finish := m.trace.Start(perftrace.TUIProducerMCP, perftrace.Fields{View: perftrace.ViewMCP, Generation: generation})
		var rows []agentmcp.Declaration
		var err error
		if m.actions.ReloadMCPWithRepos != nil {
			rows, err = m.actions.ReloadMCPWithRepos(m.viewContext(ViewMCP), locals)
		} else {
			rows, err = m.actions.ReloadMCP(m.viewContext(ViewMCP))
		}
		warning, err := splitLoadWarning(err)
		outcome := traceOutcome(err)
		if warning != "" {
			outcome = perftrace.OutcomePartial
		}
		finish(outcome)
		return mcpMsg{generation: generation, rows: rows, valid: snapshotValid(rows, err), warning: warning, err: err}
	}
}

func (m Model) checkSkills(rows []agentskill.Skill) tea.Cmd {
	generation := m.viewLoad(ViewSkills).generation
	return func() tea.Msg {
		if m.actions.CheckSkills == nil {
			return skillsMsg{
				generation: generation, rows: rows, valid: true, loaded: true, checked: true,
				warning: m.skillsInventoryWarning, inventoryErr: m.skillsInventoryErr, err: m.skillsInventoryErr,
			}
		}
		return skillsMsg{
			generation: generation, rows: m.actions.CheckSkills(m.viewContext(ViewSkills), rows),
			valid: true, loaded: true, checked: true,
			warning: m.skillsInventoryWarning, inventoryErr: m.skillsInventoryErr, err: m.skillsInventoryErr,
		}
	}
}

func (m Model) reloadUpdatedSkill(name, lockName string, scope agentskill.Scope, checkout string) tea.Cmd {
	generation := m.viewLoad(ViewSkills).generation
	locals := append([]RepoRow(nil), m.repos...)
	return func() tea.Msg {
		finish := m.trace.Start(perftrace.TUIProducerSkills, perftrace.Fields{View: perftrace.ViewSkills, Generation: generation})
		if m.actions.ReloadSkillsWithRepos == nil && m.actions.ReloadSkills == nil {
			finish(perftrace.OutcomeSuccess)
			return skillsMsg{
				generation: generation, valid: true, loaded: true, status: "updated " + name,
				warning: m.skillsInventoryWarning, inventoryErr: m.skillsInventoryErr, err: m.skillsInventoryErr,
			}
		}
		var rows []agentskill.Skill
		var err error
		if m.actions.ReloadSkillsWithRepos != nil {
			rows, err = m.actions.ReloadSkillsWithRepos(m.viewContext(ViewSkills), locals)
		} else {
			rows, err = m.actions.ReloadSkills(m.viewContext(ViewSkills))
		}
		warning, err := splitLoadWarning(err)
		if err != nil && rows == nil {
			finish(traceOutcome(err))
			return skillsMsg{generation: generation, valid: false, loaded: true, inventoryErr: err, err: err}
		}
		verified := false
		if m.actions.CheckSkills != nil {
			for i := range rows {
				candidateName := rows[i].Name
				if rows[i].Lock != nil {
					candidateName = rows[i].Lock.Name
				}
				if candidateName == lockName && rows[i].Scope == scope && rows[i].Checkout == checkout {
					checked := m.actions.CheckSkills(m.viewContext(ViewSkills), []agentskill.Skill{rows[i]})
					if len(checked) == 1 {
						rows[i] = checked[0]
						verified = true
					}
					break
				}
			}
		}
		status := "updated " + name
		if m.actions.CheckSkills != nil && !verified {
			status += "; matching row was not found for source verification"
		}
		if warning != "" {
			status += "; " + warning
		}
		outcome := traceOutcome(err)
		if warning != "" {
			outcome = perftrace.OutcomePartial
		}
		finish(outcome)
		return skillsMsg{
			generation: generation, rows: rows, valid: snapshotValid(rows, err), loaded: true,
			checked: verified, status: status, warning: warning, inventoryErr: err, err: err,
		}
	}
}

func (m Model) reloadConfig(refreshRemote bool) tea.Cmd {
	generation := m.configGeneration
	return func() tea.Msg {
		if m.actions.ReloadConfig == nil {
			return configMsg{generation: generation, refreshRemote: refreshRemote}
		}
		update, status, err := m.actions.ReloadConfig(m.configReadContext())
		return configMsg{
			generation: generation, update: update, status: status,
			refreshRemote: refreshRemote, err: err,
		}
	}
}

func (m Model) reloadAfterConfig(refreshRemote bool) tea.Cmd {
	if m.actions.Local.Start != nil {
		local := m.startLocalLoad()
		if refreshRemote {
			return tea.Batch(local, m.reloadRemote())
		}
		return local
	}
	return func() tea.Msg {
		ctx := m.viewContext(ViewTasks)
		out := reloadMsg{
			forceSizes:       m.forceSizeReload,
			rowsGeneration:   m.viewLoad(ViewTasks).generation,
			reposGeneration:  m.viewLoad(ViewRepos).generation,
			triesGeneration:  m.viewLoad(ViewTries).generation,
			remoteGeneration: m.viewLoad(ViewRemote).generation,
		}
		if m.actions.Reload != nil {
			finish := m.trace.Start(perftrace.TUIProducerTasks, perftrace.Fields{View: perftrace.ViewTasks})
			out.rows, out.rowsErr = m.actions.Reload(ctx)
			out.rowsSet, out.rowsValid = true, snapshotValid(out.rows, out.rowsErr)
			finish(traceOutcome(out.rowsErr))
		}
		if m.actions.ReloadRepos != nil {
			finish := m.trace.Start(perftrace.TUIProducerRepos, perftrace.Fields{View: perftrace.ViewRepos})
			out.repos, out.reposErr = m.actions.ReloadRepos(ctx)
			out.reposSet, out.reposValid = true, snapshotValid(out.repos, out.reposErr)
			finish(traceOutcome(out.reposErr))
		}
		if m.actions.Tries.Reload != nil {
			finish := m.trace.Start(perftrace.TUIProducerTries, perftrace.Fields{View: perftrace.ViewTries})
			out.tries, out.triesErr = m.actions.Tries.Reload(ctx, m.showAllTries)
			out.triesSet, out.triesValid = true, snapshotValid(out.tries, out.triesErr)
			finish(traceOutcome(out.triesErr))
		}
		if refreshRemote && m.actions.ReloadRemote != nil {
			finish := m.trace.Start(perftrace.TUIProducerRemote, perftrace.Fields{View: perftrace.ViewRemote})
			out.remotes, out.remoteErr = m.actions.ReloadRemote(ctx)
			out.remoteSet, out.remoteValid = true, snapshotValid(out.remotes, out.remoteErr)
			finish(traceOutcome(out.remoteErr))
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
		if row.Local && !m.showLocalFleet {
			continue
		}
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

func (m Model) fleetCount() int {
	count := 0
	for _, row := range m.fleet {
		if !row.Local || m.showLocalFleet {
			count++
		}
	}
	return count
}

// taskRecoveryCommand returns the explicit lifecycle command required before
// a task can be navigated to. Generic TUI open remains navigation-only: it
// must not reconcile task state, claim a writer, or enter an artifact shell
// that Git no longer recognizes as a worktree.
func taskRecoveryCommand(row inventory.Row) string {
	if row.Task == nil {
		return ""
	}
	if row.WorktreeMissing || !row.CheckoutExists {
		return "dev sweep"
	}
	if row.Task.EffectiveMode() == task.ModeWorktree && row.Task.WorktreePath == "" {
		return "dev resume " + row.Task.ID
	}
	return ""
}

func taskOpenBlocker(row inventory.Row) error {
	command := taskRecoveryCommand(row)
	if command == "" {
		return nil
	}
	switch {
	case row.WorktreeMissing:
		return fmt.Errorf("%s has a missing or unregistered worktree — run `%s` first", row.Task.Title(), command)
	case !row.CheckoutExists:
		return fmt.Errorf("%s has no checkout on disk — run `%s` first", row.Task.Title(), command)
	default:
		return fmt.Errorf("%s has no managed worktree — run `%s` to rebuild it", row.Task.Title(), command)
	}
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
				agents := strings.Join(append(append([]string(nil), row.Agents...), row.Attribution.AgentIDs...), " ")
				if !strings.Contains(strings.ToLower(agents), value) {
					return false
				}
				continue
			case "update":
				if !strings.Contains(strings.ToLower(string(row.UpdateStatus)), value) {
					return false
				}
				continue
			case "repo":
				if !strings.Contains(strings.ToLower(row.Repository), value) {
					return false
				}
				continue
			case "presence":
				if !strings.Contains(strings.ToLower(string(row.Presence)), value) {
					return false
				}
				continue
			case "integrity":
				if !strings.Contains(strings.ToLower(string(row.Integrity)), value) {
					return false
				}
				continue
			}
		}
		search := strings.ToLower(strings.Join([]string{
			row.Name, row.Repository, row.RepositoryPath, row.Checkout,
			string(row.Scope), row.Source, row.SourceURL, string(row.Presence), string(row.Integrity),
			string(row.ManagedBy), string(row.UpdateStatus),
			strings.Join(append(append([]string(nil), row.Agents...), row.Attribution.AgentIDs...), " "),
		}, " "))
		if !strings.Contains(search, term) {
			return false
		}
	}
	return true
}

func (m Model) visibleMCP() []agentmcp.Declaration {
	var out []agentmcp.Declaration
	for _, row := range m.mcp {
		if mcpMatches(row, m.filter) {
			out = append(out, row)
		}
	}
	return out
}

func mcpMatches(row agentmcp.Declaration, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	for _, term := range strings.Fields(query) {
		key, value, structured := strings.Cut(term, ":")
		if structured {
			var field string
			switch key {
			case "repo":
				field = row.Repository
			case "agent":
				field = string(row.Agent)
			case "scope":
				field = string(row.Scope)
			case "transport":
				field = string(row.Transport)
			case "managed":
				field = string(row.Source)
			case "state":
				field = mcpDeclarationState(row)
			default:
				field = ""
			}
			if field != "" {
				if !strings.Contains(strings.ToLower(field), value) {
					return false
				}
				continue
			}
		}
		search := strings.ToLower(strings.Join([]string{
			row.Name, string(row.Agent), string(row.Scope), row.Repository,
			row.RepositoryPath, row.Checkout, string(row.Source), row.Plugin,
			string(row.Transport), mcpDeclarationState(row), row.Endpoint, row.Command,
		}, " "))
		if !strings.Contains(search, term) {
			return false
		}
	}
	return true
}

func mcpDeclarationState(row agentmcp.Declaration) string {
	if row.Enabled == nil {
		return "configured"
	}
	if *row.Enabled {
		return "enabled"
	}
	return "disabled"
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
	case ViewMCP:
		return len(m.visibleMCP())
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
	case ViewMCP:
		return m.mcpCursor
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
	case ViewMCP:
		m.mcpCursor = i
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

func (m Model) currentMCP() (agentmcp.Declaration, bool) {
	if m.view != ViewMCP {
		return agentmcp.Declaration{}, false
	}
	rows := m.visibleMCP()
	if m.at() >= len(rows) {
		return agentmcp.Declaration{}, false
	}
	return rows[m.at()], true
}

// matchRemoteLocals fills cached remote rows from the freshly loaded local
// inventory without another scan.
func (m *Model) matchRemoteLocals() {
	m.remotes = append([]RemoteRow(nil), m.remotes...)
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
	triesByIdentity := map[string]TryRow{}
	ambiguousTry := map[string]bool{}
	for _, row := range m.tries {
		identity := row.Item.RemoteIdentity
		if !row.Present() || identity == "" || ambiguousTry[identity] {
			continue
		}
		if _, exists := triesByIdentity[identity]; exists {
			delete(triesByIdentity, identity)
			ambiguousTry[identity] = true
			continue
		}
		triesByIdentity[identity] = row
	}
	for i := range m.remotes {
		key := string(m.remotes[i].Repo.Forge) + "/" + strings.ToLower(m.remotes[i].Repo.FullName)
		identity := ""
		for _, raw := range []string{m.remotes[i].Repo.CloneURL, m.remotes[i].Repo.SSHURL, m.remotes[i].Repo.URL} {
			if identity = catalog.NormalizeRemoteIdentity(raw); identity != "" {
				break
			}
		}
		repository, repoOK := byRemote[key]
		try, tryOK := triesByIdentity[identity]
		sameCheckout := repoOK && tryOK && remotePathsOverlap(repository, try)
		if ambiguous[key] || ambiguousTry[identity] || (repoOK && tryOK && !sameCheckout) {
			m.remotes[i].LocalPath, m.remotes[i].LocalName, m.remotes[i].LocalKind = "", "", ""
			continue
		}
		if repoOK {
			m.remotes[i].LocalPath = repository.Repo.Path
			m.remotes[i].LocalName = repository.Repo.Display()
			m.remotes[i].LocalKind = catalog.KindRepository
			if repository.Asset != nil {
				m.remotes[i].LocalKind = repository.Asset.Kind
			}
			continue
		}
		if tryOK {
			m.remotes[i].LocalPath = try.Item.Live.CurrentPath
			m.remotes[i].LocalName = try.Item.DisplayName()
			m.remotes[i].LocalKind = catalog.KindTry
			continue
		}
		m.remotes[i].LocalPath, m.remotes[i].LocalName, m.remotes[i].LocalKind = "", "", ""
	}
}

func remotePathsOverlap(repository RepoRow, try TryRow) bool {
	for _, left := range []string{repository.Repo.Path, repository.Repo.RealPath} {
		for _, right := range []string{try.Item.Live.CurrentPath, try.Item.Live.RealPath} {
			if left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right) {
				return true
			}
		}
	}
	return false
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

	case localMsg:
		if msg.done {
			request := msg.load.Request
			current := m.localGeneration == request.CycleGeneration
			if !current {
				return m, nil
			}
			m.finishLocalLoad()
			m.forceSizeReload = false
			return m.beginSizeLoad(request.ForceSizes)
		}
		var accepted bool
		m, accepted = m.applyLocalResult(msg.result)
		next := waitForLocal(msg.load)
		if accepted && msg.result.View == ViewRepos {
			if fleet := m.afterReposResult(msg.result.Valid, msg.result.Err); fleet != nil {
				return m, tea.Batch(next, fleet)
			}
		}
		return m, next

	case reloadMsg:
		acceptedLocal := false
		reposAccepted := false
		if msg.rowsSet && m.applyViewResult(
			ViewTasks, msg.rowsGeneration, msg.rowsValid, perftrace.SourceLive,
			resultFreshness(msg.rowsErr), len(msg.rows), msg.rowsErr, msg.rowsValid,
		) {
			if msg.rowsValid {
				m.rows = append([]inventory.Row(nil), msg.rows...)
			}
			acceptedLocal = true
		}
		if msg.reposSet && m.applyViewResult(
			ViewRepos, msg.reposGeneration, msg.reposValid, perftrace.SourceLive,
			resultFreshness(msg.reposErr), len(msg.repos), msg.reposErr, msg.reposValid,
		) {
			if msg.reposValid {
				m.repos = append([]RepoRow(nil), msg.repos...)
				m.matchRemoteLocals()
			}
			acceptedLocal = true
			reposAccepted = true
		}
		if msg.triesSet && m.applyViewResult(
			ViewTries, msg.triesGeneration, msg.triesValid, perftrace.SourceLive,
			resultFreshness(msg.triesErr), len(msg.tries), msg.triesErr, msg.triesValid,
		) {
			if msg.triesValid {
				m.tries = append([]TryRow(nil), msg.tries...)
				m.matchRemoteLocals()
			}
			acceptedLocal = true
		}
		if msg.remoteSet && m.applyViewResult(
			ViewRemote, msg.remoteGeneration, msg.remoteValid, perftrace.SourceLive,
			resultFreshness(msg.remoteErr), len(msg.remotes), msg.remoteErr, msg.remoteValid,
		) && msg.remoteValid {
			m.remotes = append([]RemoteRow(nil), msg.remotes...)
		}
		if acceptedLocal {
			m.forceSizeReload = false
		}
		m.setAt(m.at())
		m, sizeCmd := m.beginSizeLoad(msg.forceSizes)
		if reposAccepted {
			if fleetCmd := m.afterReposResult(msg.reposValid, msg.reposErr); fleetCmd != nil {
				return m, tea.Batch(sizeCmd, fleetCmd)
			}
		}
		return m, sizeCmd

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

	case remoteCacheMsg:
		if !msg.result.Found {
			return m, nil
		}
		freshness := perftrace.FreshnessFresh
		if msg.result.Stale {
			freshness = perftrace.FreshnessStale
		}
		if m.applyCacheSeed(ViewRemote, msg.generation, len(msg.result.Rows), freshness) {
			m.remotes = append([]RemoteRow(nil), msg.result.Rows...)
			m.matchRemoteLocals()
		}
		return m, nil

	case fleetCacheMsg:
		if !msg.result.Found {
			return m, nil
		}
		if m.applyCacheSeed(ViewFleet, msg.generation, len(msg.result.Rows), perftrace.FreshnessStale) {
			m.fleet = append([]FleetRow(nil), msg.result.Rows...)
		}
		return m, nil

	case remoteMsg:
		if !m.applyViewResult(
			ViewRemote, msg.generation, msg.valid, perftrace.SourceLive,
			resultFreshness(msg.err), len(msg.rows), msg.err, msg.valid,
		) {
			return m, nil
		}
		if msg.valid {
			m.remotes = append([]RemoteRow(nil), msg.rows...)
			if msg.matchLocals {
				m.matchRemoteLocals()
			}
		}
		m.setViewStatus(ViewRemote, "")
		m.setAt(m.at())
		return m, nil

	case fleetMsg:
		if !m.applyViewResult(
			ViewFleet, msg.generation, msg.valid, perftrace.SourceLive,
			resultFreshness(msg.err), len(msg.rows), msg.err, msg.valid,
		) {
			return m, nil
		}
		if msg.valid {
			m.fleet = append([]FleetRow(nil), msg.rows...)
		}
		m.setViewStatus(ViewFleet, "")
		m.setAt(m.at())
		return m, nil

	case skillsMsg:
		if !m.applyViewResult(
			ViewSkills, msg.generation, msg.valid, perftrace.SourceLive,
			resultFreshness(msg.err), len(msg.rows), msg.err, msg.valid,
		) {
			return m, nil
		}
		m.skillsChecking = false
		m.skillsInventoryErr = msg.inventoryErr
		m.skillsInventoryWarning = msg.warning
		if msg.valid {
			m.skills = append([]agentskill.Skill(nil), msg.rows...)
		}
		switch {
		case msg.err != nil:
			m.setViewStatus(ViewSkills, "")
		case msg.status != "":
			m.setViewStatus(ViewSkills, msg.status)
		case msg.warning != "":
			m.setViewStatus(ViewSkills, msg.warning)
		case msg.checked:
			m.setViewStatus(ViewSkills, "skill update check complete")
		default:
			m.setViewStatus(ViewSkills, "")
		}
		m.setAt(m.at())
		return m, nil

	case mcpMsg:
		if !m.applyViewResult(
			ViewMCP, msg.generation, msg.valid, perftrace.SourceLive,
			resultFreshness(msg.err), len(msg.rows), msg.err, msg.valid,
		) {
			return m, nil
		}
		if msg.valid {
			m.mcp = append([]agentmcp.Declaration(nil), msg.rows...)
		}
		m.setViewStatus(ViewMCP, msg.warning)
		m.setAt(m.at())
		return m, nil

	case toolsMsg:
		if msg.generation != m.toolGeneration {
			return m, nil
		}
		m.actions.Tools = append([]Tool(nil), msg.tools...)
		return m, nil

	case skillProcessMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			return m, nil
		}
		m.err = nil
		m.beginViewLoad(ViewSkills, loadAction)
		if msg.action == "update" {
			m.setViewStatus(ViewSkills, "reloading and verifying "+msg.name+"…")
			return m, m.reloadUpdatedSkill(msg.name, msg.lockName, msg.scope, msg.checkout)
		}
		m.setViewStatus(ViewSkills, "reloading agent skills…")
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
		m.beginConfigLoad()
		m.status = "reloading config…"
		return m, m.reloadConfig(m.view == ViewRemote)

	case fleetConfigEditedMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			return m, nil
		}
		if m.actions.ValidateFleetConfig != nil {
			if err := m.actions.ValidateFleetConfig(); err != nil {
				// A rejected file is still on disk; showing the parse error is
				// the only way the user learns why the fleet did not change.
				m.err, m.status = err, "remotes.toml was not applied"
				return m, nil
			}
		}
		m.beginViewLoad(ViewFleet, loadConfig)
		m.setViewStatus(ViewFleet, "refreshing fleet…")
		return m, m.reloadFleet()

	case configMsg:
		if msg.generation != m.configGeneration {
			return m, nil
		}
		if m.configCancel != nil {
			m.configCancel()
			m.configCancel, m.configContext = nil, nil
		}
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			m.forceSizeReload = false
			return m, nil
		}
		if msg.update.Apply != nil {
			msg.update.Apply()
		}
		m.beginLocalLoads(loadConfig)
		if msg.refreshRemote {
			m.beginViewLoad(ViewRemote, loadConfig)
		} else {
			m.invalidateView(ViewRemote)
		}
		m.actions.Tools = append([]Tool(nil), msg.update.Tools...)
		m.toolGeneration++
		m.actions.RepoColumns = append([]string(nil), msg.update.RepoColumns...)
		m.actions.RepoSort = msg.update.RepoSort
		m.actions.RepoReverse = msg.update.RepoReverse
		m.err, m.status = nil, msg.status
		reload := m.reloadAfterConfig(msg.refreshRemote)
		if len(m.actions.Tools) == 0 {
			return m, reload
		}
		return m, tea.Batch(reload, m.probeTools())

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
		accepted := false
		reposAccepted := false
		if msg.triesSet && m.applyViewResult(
			ViewTries, msg.triesGeneration, msg.triesValid, perftrace.SourceLive,
			resultFreshness(msg.triesErr), len(msg.tries), msg.triesErr, msg.triesValid,
		) {
			if msg.triesValid {
				m.tries = append([]TryRow(nil), msg.tries...)
				m.matchRemoteLocals()
			}
			accepted = true
		}
		if msg.reposSet && m.applyViewResult(
			ViewRepos, msg.reposGeneration, msg.reposValid, perftrace.SourceLive,
			resultFreshness(msg.reposErr), len(msg.repos), msg.reposErr, msg.reposValid,
		) {
			if msg.reposValid {
				m.repos = append([]RepoRow(nil), msg.repos...)
				m.matchRemoteLocals()
			}
			accepted = true
			reposAccepted = true
		}
		m.setAt(m.at())
		var sizeCmd tea.Cmd
		if accepted {
			m, sizeCmd = m.beginSizeLoad(false)
		}
		if reposAccepted {
			if fleetCmd := m.afterReposResult(msg.reposValid, msg.reposErr); fleetCmd != nil {
				return m, tea.Batch(sizeCmd, fleetCmd)
			}
		}
		return m, sizeCmd

	case tryActionMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
			if msg.result.RefreshRepos {
				m.beginTryLoads(true, loadAction)
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
		m.beginTryLoads(msg.result.RefreshRepos, loadAction)
		return m, m.reloadTries(msg.result.RefreshRepos)

	case noteListMsg:
		var fleetCmd, sizeCmd tea.Cmd
		if msg.reposSet {
			accepted := m.applyViewResult(
				ViewRepos, msg.reposGeneration, msg.reposValid, perftrace.SourceLive,
				resultFreshness(msg.reposErr), len(msg.repos), msg.reposErr, msg.reposValid,
			)
			if accepted && msg.reposValid {
				m.repos = append([]RepoRow(nil), msg.repos...)
				m.matchRemoteLocals()
			}
			if accepted {
				m, sizeCmd = m.beginSizeLoad(false)
				fleetCmd = m.afterReposResult(msg.reposValid, msg.reposErr)
			}
		}
		followup := batchCommands(sizeCmd, fleetCmd)
		if msg.request != m.noteRequest || msg.targetKey != m.noteTarget.Key() || !m.noteMode() {
			return m, followup // note overlay moved; independent REPOS result is still resolved
		}
		m.noteLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, followup
		}
		m.notes, m.err = msg.notes, nil
		m.clampNoteCursor()
		return m, followup

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
		m.beginLocalLoads(loadAction)
		return m, m.reload()

	case actionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.status = nil, msg.status
		m.forceSizeReload = msg.forceSizes
		if msg.remoteName != "" && msg.localPath != "" {
			m.remotes = append([]RemoteRow(nil), m.remotes...)
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
		// Clone already updated the selected REMOTE row; only local data
		// needs a refresh. A network round-trip here would make a successful
		// clone feel hung for several seconds.
		m.beginLocalLoads(loadAction)
		return m, m.reload()

	case copyMsg:
		if msg.err != nil {
			m.err, m.status = msg.err, ""
		} else {
			m.err, m.status = nil, msg.status
		}
		return m, nil

	case tea.KeyMsg:
		fields := perftrace.Fields{View: perftrace.View(m.view.String())}
		m.trace.MarkOnce(perftrace.TUIFirstKeyReceived, fields)
		finish := m.trace.Start(perftrace.TUIKeyUpdate, fields)
		defer finish(perftrace.OutcomeSuccess)
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
			m.beginViewLoad(ViewFleet, loadRefresh)
			if err := m.dependentReposUnavailable(ViewFleet); err != nil {
				fleet := m.viewLoad(ViewFleet)
				m.applyViewResult(ViewFleet, fleet.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(ViewFleet) {
				m.setViewStatus(ViewFleet, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(ViewFleet, "refreshing fleet…")
			return m, m.reloadFleet()
		}
		if m.view == ViewSkills || m.view == ViewMCP {
			view := m.view
			m.err = nil
			m.beginViewLoad(view, loadRefresh)
			if err := m.dependentReposUnavailable(view); err != nil {
				state := m.viewLoad(view)
				m.applyViewResult(view, state.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(view) {
				m.setViewStatus(view, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(view, dependentLoadingStatus(view))
			return m, m.reloadDependentView(view)
		}
		m.beginConfigLoad()
		m.status = "reloading config + data…"
		m.forceSizeReload = true
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
			mutation, err := m.actions.AddSkill()
			if err != nil {
				m.err = err
				return m, nil
			}
			proc, finish, err := mutation.Prepare()
			if err != nil {
				m.err = err
				return m, nil
			}
			m.status = "opening interactive skill installer…"
			return m, tea.ExecProcess(proc, func(err error) tea.Msg {
				return skillProcessMsg{action: "add", err: finish(err)}
			})
		}
		if m.view == ViewTries {
			m.showAllTries = !m.showAllTries
			m.status = fmt.Sprintf("Try history visible: %v", m.showAllTries)
			m.setAt(0)
			m.beginTryLoads(false, loadRefresh)
			return m, m.reloadTries(false)
		}
		if m.view == ViewFleet {
			m.showLocalFleet = !m.showLocalFleet
			m.status = fmt.Sprintf("local fleet rows visible: %v", m.showLocalFleet)
			m.setAt(0)
			return m, nil
		}
		if m.view == ViewTasks {
			m.showDone, m.states = !m.showDone, nil
			m.setAt(0)
		}

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
			state := m.viewLoad(ViewSkills)
			if state.loading || !state.hasSnapshot {
				m.setViewStatus(ViewSkills, "wait for local agent skills to finish loading")
				return m, nil
			}
			m.err = nil
			m.beginViewLoad(ViewSkills, loadRefresh)
			m.setViewStatus(ViewSkills, "checking skill sources…")
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

	case "e":
		if m.view == ViewFleet {
			if m.actions.EditFleetConfig == nil {
				return m, nil
			}
			proc, err := m.actions.EditFleetConfig()
			if err != nil {
				m.err = err
				return m, nil
			}
			m.status = "editing remotes.toml…"
			return m, tea.ExecProcess(proc, func(err error) tea.Msg {
				return fleetConfigEditedMsg{err: err}
			})
		}
		if m.view == ViewMCP {
			return m, nil
		}
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
	reposGeneration := m.viewLoad(ViewRepos).generation
	if refreshRepos {
		reposGeneration = m.beginViewLoad(ViewRepos, loadAction)
	}
	query := m.noteQuery
	m.noteLoading = true
	return m, func() tea.Msg {
		if m.actions.Notes.List == nil {
			return noteListMsg{request: request, targetKey: targetKey, reposGeneration: reposGeneration}
		}
		var (
			notes []*note.Note
			err   error
		)
		if query != "" && m.actions.Notes.Search != nil {
			notes, err = m.actions.Notes.Search(m.baseContext(), target, query)
		} else {
			notes, err = m.actions.Notes.List(m.baseContext(), target)
		}
		msg := noteListMsg{
			notes: notes, request: request, targetKey: targetKey, err: err,
			reposGeneration: reposGeneration,
		}
		if refreshRepos && m.actions.ReloadRepos != nil {
			msg.repos, msg.reposErr = m.actions.ReloadRepos(m.viewContext(ViewRepos))
			msg.reposSet, msg.reposValid = true, snapshotValid(msg.repos, msg.reposErr)
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
		panel, err := m.actions.LoadStats(m.baseContext(), repo)
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

func (m Model) viewUsesRepos(view View) bool {
	switch view {
	case ViewFleet:
		return m.actions.ReloadFleetWithRepos != nil
	case ViewSkills:
		return m.actions.ReloadSkillsWithRepos != nil
	case ViewMCP:
		return m.actions.ReloadMCPWithRepos != nil
	default:
		return false
	}
}

func (m Model) viewWaitsForRepos(view View) bool {
	return m.viewUsesRepos(view) && m.viewLoad(ViewRepos).loading
}

func (m Model) dependentReposUnavailable(view View) error {
	if !m.viewUsesRepos(view) {
		return nil
	}
	repos := m.viewLoad(ViewRepos)
	if repos.loading || (repos.hasSnapshot && repos.freshness == perftrace.FreshnessFresh) {
		return nil
	}
	if err := m.viewError(ViewRepos); err != nil {
		return fmt.Errorf("local repository inventory: %w", err)
	}
	return errors.New("local repository inventory is unavailable")
}

func dependentLoadingStatus(view View) string {
	switch view {
	case ViewFleet:
		return "loading configured dev hosts…"
	case ViewSkills:
		return "loading agent skills across repositories…"
	case ViewMCP:
		return "loading MCP declarations across repositories…"
	default:
		return "loading…"
	}
}

func (m Model) reloadDependentView(view View) tea.Cmd {
	switch view {
	case ViewFleet:
		return m.reloadFleet()
	case ViewSkills:
		return m.reloadSkills()
	case ViewMCP:
		return m.reloadMCP()
	default:
		return nil
	}
}

func (m *Model) afterReposResult(valid bool, err error) tea.Cmd {
	fresh := valid && err == nil
	var commands []tea.Cmd
	for _, view := range []View{ViewFleet, ViewSkills, ViewMCP} {
		if !m.viewUsesRepos(view) {
			continue
		}
		state := m.viewLoad(view)
		if state.loading || m.view == view {
			m.beginViewLoad(view, loadRefresh)
			if fresh {
				m.setViewStatus(view, dependentLoadingStatus(view))
				commands = append(commands, m.reloadDependentView(view))
				continue
			}
			dependencyErr := err
			if dependencyErr == nil {
				dependencyErr = errors.New("local repository inventory is unavailable")
			}
			current := m.viewLoad(view)
			m.applyViewResult(view, current.generation, false, "", "", 0, dependencyErr, false)
			continue
		}
		if state.hasSnapshot {
			m.invalidateView(view)
		}
	}
	return batchCommands(commands...)
}

// afterViewSwitch lazily loads optional inventories only when their view is
// first opened, so starting the dashboard never waits on the network, a forge
// CLI, or Node tooling.
func (m Model) afterViewSwitch() (tea.Model, tea.Cmd) {
	m.setAt(m.at())
	switch m.view {
	case ViewFleet:
		if m.viewNeedsLoad(ViewFleet) {
			m.beginViewLoad(ViewFleet, loadVisit)
			if err := m.dependentReposUnavailable(ViewFleet); err != nil {
				fleet := m.viewLoad(ViewFleet)
				m.applyViewResult(ViewFleet, fleet.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(ViewFleet) {
				m.setViewStatus(ViewFleet, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(ViewFleet, "loading configured dev hosts…")
			return m, m.reloadFleet()
		}
	case ViewRemote:
		if m.viewNeedsLoad(ViewRemote) {
			m.beginViewLoad(ViewRemote, loadVisit)
			m.setViewStatus(ViewRemote, "refreshing remote repositories…")
			return m, m.reloadRemote()
		}
	case ViewSkills:
		if m.viewNeedsLoad(ViewSkills) {
			m.beginViewLoad(ViewSkills, loadVisit)
			if err := m.dependentReposUnavailable(ViewSkills); err != nil {
				state := m.viewLoad(ViewSkills)
				m.applyViewResult(ViewSkills, state.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(ViewSkills) {
				m.setViewStatus(ViewSkills, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(ViewSkills, dependentLoadingStatus(ViewSkills))
			return m, m.reloadSkills()
		}
	case ViewMCP:
		if m.viewNeedsLoad(ViewMCP) {
			m.beginViewLoad(ViewMCP, loadVisit)
			if err := m.dependentReposUnavailable(ViewMCP); err != nil {
				state := m.viewLoad(ViewMCP)
				m.applyViewResult(ViewMCP, state.generation, false, "", "", 0, err, false)
				return m, nil
			}
			if m.viewWaitsForRepos(ViewMCP) {
				m.setViewStatus(ViewMCP, "waiting for local repositories…")
				return m, nil
			}
			m.setViewStatus(ViewMCP, dependentLoadingStatus(ViewMCP))
			return m, m.reloadMCP()
		}
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
				status: opened.Status, cd: opened.Directory, activate: opened.RuntimeHandle,
				remoteName: r.Repo.FullName, localPath: path, err: err,
			}
		}

	case modeConfirmSkillUpdate:
		row := m.skillUpdateTarget
		if row.Name == "" || m.actions.UpdateSkill == nil {
			return nil
		}
		mutation, err := m.actions.UpdateSkill(row)
		if err != nil {
			return func() tea.Msg { return skillProcessMsg{action: "update", err: err} }
		}
		proc, finish, err := mutation.Prepare()
		if err != nil {
			return func() tea.Msg { return skillProcessMsg{action: "update", err: err} }
		}
		lockName := row.Name
		if row.Lock != nil {
			lockName = row.Lock.Name
		}
		return tea.ExecProcess(proc, func(err error) tea.Msg {
			return skillProcessMsg{
				action: "update", name: row.Name, lockName: lockName,
				scope: row.Scope, checkout: row.Checkout, err: finish(err),
			}
		})
	}
	return nil
}

func (m Model) openSelected() tea.Cmd {
	if row, ok := m.currentTask(); ok {
		if err := taskOpenBlocker(row); err != nil {
			return func() tea.Msg { return actionMsg{err: err} }
		}
		t := row.Task
		return func() tea.Msg {
			opened, err := m.actions.Open(context.Background(), t)
			return actionMsg{status: opened.Status, cd: opened.Directory, activate: opened.RuntimeHandle, err: err}
		}
	}
	if r, ok := m.currentRepo(); ok {
		return func() tea.Msg {
			opened, err := m.actions.OpenRepo(context.Background(), r)
			return actionMsg{status: opened.Status, cd: opened.Directory, activate: opened.RuntimeHandle, err: err}
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
			return actionMsg{status: opened.Status, cd: opened.Directory, activate: opened.RuntimeHandle, err: err}
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
		return func() tea.Msg {
			opened, err := m.actions.OpenRemote(context.Background(), r)
			return actionMsg{status: opened.Status, cd: opened.Directory, activate: opened.RuntimeHandle, err: err}
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
		if t.Probe != nil && t.Availability != ToolAvailable {
			detail := "availability is still being checked"
			if t.Availability == ToolUnavailable {
				detail = "is not installed"
			}
			return func() tea.Msg {
				return actionMsg{err: fmt.Errorf("%s %s", t.Command[0], detail)}
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
		if t.Probe == nil || t.Availability == ToolAvailable {
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
	if m.viewLoad(ViewRemote).hasSnapshot {
		parts = append(parts, fmt.Sprintf("%d remote", len(m.remotes)))
	}
	if count := m.fleetCount(); count > 0 {
		parts = append(parts, fmt.Sprintf("%d fleet", count))
	}
	if m.viewLoad(ViewSkills).hasSnapshot {
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
	if m.viewLoad(ViewMCP).hasSnapshot {
		parts = append(parts, fmt.Sprintf("%d mcp", len(m.mcp)))
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
