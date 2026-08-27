package tui_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func row(id, name string, st task.State, next string) inventory.Row {
	return inventory.Row{
		Task: &task.Task{
			ID: id, Name: name, Repo: "demo", RepoPath: "/src/demo",
			Branch: "feat/" + id, State: st, Next: next,
		},
		CheckoutExists: true,
	}
}

// recorder captures which actions the dashboard triggered.
type recorder struct {
	opened  []string
	parked  []string
	started []string
	cloned  []string
	nexts   map[string]string
}

func newActions(r *recorder, rows []inventory.Row) tui.Actions {
	r.nexts = map[string]string{}
	return tui.Actions{
		Runtime: runtime.None{},
		Reload: func(context.Context) ([]inventory.Row, error) {
			return rows, nil
		},
		ReloadRepos:  func(context.Context) ([]tui.RepoRow, error) { return nil, nil },
		ReloadRemote: func(context.Context) ([]tui.RemoteRow, error) { return nil, nil },
		OpenRepo: func(_ context.Context, rr tui.RepoRow) (string, error) {
			r.opened = append(r.opened, "repo:"+rr.Repo.Name)
			return "opened", nil
		},
		OpenRemote: func(_ context.Context, rr tui.RemoteRow) (string, error) {
			r.opened = append(r.opened, "remote:"+rr.Repo.FullName)
			return "opened", nil
		},
		CloneRemote: func(_ context.Context, rr tui.RemoteRow) (string, string, error) {
			r.cloned = append(r.cloned, rr.Repo.FullName)
			return "cloned", "/src/" + rr.Repo.Name, nil
		},
		Start: func(_ context.Context, rr tui.RepoRow, name string) (string, error) {
			r.started = append(r.started, "worktree:"+rr.Repo.Name+"/"+name)
			return "started", nil
		},
		StartDirect: func(_ context.Context, rr tui.RepoRow, name string) (string, error) {
			r.started = append(r.started, "direct:"+rr.Repo.Name+"/"+name)
			return "started direct", nil
		},
		Open: func(_ context.Context, t *task.Task) (string, error) {
			r.opened = append(r.opened, t.ID)
			return "opened " + t.ID, nil
		},
		Park: func(_ context.Context, t *task.Task, next string) (string, error) {
			r.parked = append(r.parked, t.ID)
			r.nexts[t.ID] = next
			return "parked " + t.ID, nil
		},
		SetNext: func(_ context.Context, t *task.Task, next string) error {
			r.nexts[t.ID] = next
			return nil
		},
	}
}

// send feeds a sequence of key presses through Update, running any command the
// model returns so the action callbacks actually fire — that is what makes
// these tests exercise the real key bindings rather than the model's fields.
func send(m tui.Model, msgs ...tea.Msg) tui.Model {
	var cur tea.Model = m
	for _, msg := range msgs {
		var cmd tea.Cmd
		cur, cmd = cur.Update(msg)
		// Follow the command chain, so an action's resulting message (and the
		// reload it triggers) is applied too. Commands that do not return
		// promptly are the cursor's blink timers; running those would make the
		// suite sleep for half a second per keystroke.
		for i := 0; cmd != nil && i < 8; i++ {
			out, ok := runQuickly(cmd)
			if !ok || out == nil {
				break
			}
			if _, isBatch := out.(tea.BatchMsg); isBatch {
				break
			}
			cur, cmd = cur.Update(out)
		}
	}
	return cur.(tui.Model)
}

// runQuickly executes a command, giving up on anything that blocks — which in
// practice means the cursor blink timers.
func runQuickly(cmd tea.Cmd) (tea.Msg, bool) {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg, true
	case <-time.After(100 * time.Millisecond):
		return nil, false
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(s string) []tea.KeyMsg {
	out := make([]tea.KeyMsg, 0, len(s))
	for _, r := range s {
		out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return out
}

func TestViewRendersTasksAndHelp(t *testing.T) {
	rows := []inventory.Row{
		row("a", "token refresh", task.Hot, "add the regression test"),
		row("b", "orderbook", task.Warm, ""),
	}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 30})

	out := m.View()
	for _, want := range []string{"token refresh", "orderbook", "HOT", "WARM", "add the regression test"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "q quit") {
		t.Error("the footer should show the key bindings")
	}
	if !strings.Contains(out, "TASKS") || !strings.Contains(out, "REPOS") {
		t.Errorf("both views should be visible in the tab strip:\n%s", out)
	}
}

func TestFilterByState(t *testing.T) {
	rows := []inventory.Row{
		row("a", "hot task", task.Hot, ""),
		row("b", "warm task", task.Warm, ""),
		row("c", "done task", task.Done, ""),
	}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)

	// Done tasks are hidden by default — a finished task is not work in progress.
	if strings.Contains(m.View(), "done task") {
		t.Error("done tasks should be hidden by default")
	}

	got := send(m, key("1")).View()
	if !strings.Contains(got, "hot task") || strings.Contains(got, "warm task") {
		t.Errorf("filter 1 should show only hot:\n%s", got)
	}
	got = send(m, key("2")).View()
	if !strings.Contains(got, "warm task") || strings.Contains(got, "hot task") {
		t.Errorf("filter 2 should show only warm:\n%s", got)
	}
	got = send(m, key("a")).View()
	if !strings.Contains(got, "done task") {
		t.Errorf("a should reveal done tasks:\n%s", got)
	}
}

func TestCursorStaysInBounds(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, ""), row("b", "two", task.Hot, "")}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)

	// Past the end and past the start; neither should panic or select nothing.
	m = send(m, key("down"), key("down"), key("down"), key("down"))
	if out := m.View(); !strings.Contains(out, "two") {
		t.Error("cursor overran the list")
	}
	m = send(m, key("up"), key("up"), key("up"))
	if out := m.View(); !strings.Contains(out, "one") {
		t.Error("cursor underran the list")
	}
}

func TestEnterOpensSelectedTask(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, ""), row("b", "second", task.Warm, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows, nil)

	send(m, key("down"), key("enter"))
	if len(rec.opened) != 1 || rec.opened[0] != "b" {
		t.Errorf("enter should open the selected task, opened %v", rec.opened)
	}
}

func TestParkPromptsForNextAction(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows, nil)

	m = send(m, key("p"))
	if !strings.Contains(m.View(), "park first") {
		t.Fatalf("p should open the park prompt:\n%s", m.View())
	}
	for _, k := range typeText("write the test") {
		m = send(m, k)
	}
	send(m, key("enter"))

	if len(rec.parked) != 1 || rec.parked[0] != "a" {
		t.Fatalf("park not triggered: %v", rec.parked)
	}
	if rec.nexts["a"] != "write the test" {
		t.Errorf("the typed next action should be passed through, got %q", rec.nexts["a"])
	}
}

func TestEscCancelsPrompt(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows, nil)

	m = send(m, key("p"))
	m = send(m, key("esc"))
	if strings.Contains(m.View(), "park first") {
		t.Error("esc should close the prompt")
	}
	if len(rec.parked) != 0 {
		t.Error("esc must not park anything")
	}
}

func TestEditNextSeedsCurrentValue(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "existing note")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows, nil)

	m = send(m, key("c"))
	if !strings.Contains(m.View(), "existing note") {
		t.Errorf("editing should start from the current value:\n%s", m.View())
	}
	send(m, key("enter"))
	if rec.nexts["a"] != "existing note" {
		t.Errorf("unchanged value should be saved as-is, got %q", rec.nexts["a"])
	}
}

func TestEmptyInventoryExplainsItself(t *testing.T) {
	m := tui.New(newActions(&recorder{}, nil), nil, nil)
	out := m.View()
	if !strings.Contains(out, "No tasks recorded yet") {
		t.Errorf("an empty dashboard should say what to do:\n%s", out)
	}
	if !strings.Contains(out, "dev adopt") {
		t.Errorf("it should point at the way to import existing work:\n%s", out)
	}
	// Acting on nothing must not panic.
	send(m, key("enter"), key("p"), key("c"))
}

func TestQuitStopsRendering(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)
	updated, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q should return a quit command")
	}
	if updated.View() != "" {
		t.Error("a quitting model should render nothing, so the terminal is left clean")
	}
}

func TestSummaryCountsStates(t *testing.T) {
	rows := []inventory.Row{
		row("a", "one", task.Hot, ""),
		row("b", "two", task.Hot, ""),
		row("c", "three", task.Warm, ""),
	}
	got := tui.New(newActions(&recorder{}, rows), rows, nil).Summary()
	if !strings.Contains(got, "2 hot") || !strings.Contains(got, "1 warm") {
		t.Errorf("Summary = %q", got)
	}
	if got := tui.New(newActions(&recorder{}, nil), nil, nil).Summary(); got != "no tasks" {
		t.Errorf("empty Summary = %q", got)
	}
}

func repoRow(name string, tasks ...*task.Task) tui.RepoRow {
	return tui.RepoRow{
		Repo:  repo.Repo{Name: name, Path: "/src/" + name, HasGit: true},
		Tasks: tasks,
	}
}

func TestTabSwitchesViews(t *testing.T) {
	rows := []inventory.Row{row("a", "token refresh", task.Hot, "")}
	repos := []tui.RepoRow{repoRow("api"), repoRow("web")}
	m := tui.New(newActions(&recorder{}, rows), rows, repos)

	if m.CurrentView() != tui.ViewTasks {
		t.Fatal("the dashboard should open on the task list")
	}
	m = send(m, key("tab"))
	if m.CurrentView() != tui.ViewRepos {
		t.Fatal("tab should switch to the repository list")
	}
	out := m.View()
	if !strings.Contains(out, "api") || !strings.Contains(out, "web") {
		t.Errorf("the repo view should list repositories:\n%s", out)
	}
	// l and h double as right/left, since a list has no horizontal axis.
	if send(m, key("h")).CurrentView() != tui.ViewTasks {
		t.Error("h should move to the previous view")
	}
	m = send(m, key("tab"))
	if m.CurrentView() != tui.ViewRemote {
		t.Error("the third view should be remote")
	}
	if send(m, key("tab")).CurrentView() != tui.ViewTasks {
		t.Error("a third tab should cycle back round")
	}
}

// The commonest first run is a machine full of repositories and no tasks yet.
// An empty dashboard would be the wrong answer to "what do I have here?"
func TestReposViewWorksWithNoTasks(t *testing.T) {
	repos := []tui.RepoRow{repoRow("api"), repoRow("web")}
	m := tui.New(newActions(&recorder{}, nil), nil, repos)

	out := m.View()
	if !strings.Contains(out, "tab for the repository list") {
		t.Errorf("the empty task list should point at the repo list:\n%s", out)
	}
	m = send(m, key("tab"))
	if !strings.Contains(m.View(), "api") {
		t.Errorf("repos should still be browsable:\n%s", m.View())
	}
}

func TestVimNavigation(t *testing.T) {
	var rows []inventory.Row
	for i := 0; i < 20; i++ {
		rows = append(rows, row(fmt.Sprintf("t%02d", i), fmt.Sprintf("task %02d", i), task.Hot, ""))
	}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m = send(m, key("G"))
	if !strings.Contains(m.View(), "▸") {
		t.Fatal("G should leave the cursor somewhere visible")
	}
	m = send(m, key("g"))
	// After g the first row must be selected, so it carries the marker.
	lines := strings.Split(m.View(), "\n")
	var markerLine string
	for _, l := range lines {
		if strings.Contains(l, "▸") {
			markerLine = l
			break
		}
	}
	if !strings.Contains(markerLine, "task 00") {
		t.Errorf("g should jump to the top, cursor is on %q", markerLine)
	}

	// ctrl+d moves further than a single j.
	oneDown := send(m, key("j"))
	halfPage := send(m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if oneDown.View() == halfPage.View() {
		t.Error("ctrl+d should move further than j")
	}
}

func TestFilterNarrowsAsYouType(t *testing.T) {
	rows := []inventory.Row{
		row("a", "token refresh", task.Hot, ""),
		row("b", "orderbook rewrite", task.Hot, ""),
	}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)

	m = send(m, key("/"))
	for _, k := range typeText("token") {
		m = send(m, k)
	}
	out := m.View()
	if !strings.Contains(out, "token refresh") || strings.Contains(out, "orderbook") {
		t.Errorf("the filter should narrow live:\n%s", out)
	}

	// esc clears the filter rather than quitting.
	m = send(m, key("esc"))
	if !strings.Contains(m.View(), "orderbook") {
		t.Errorf("esc should clear the filter:\n%s", m.View())
	}
}

// Terms are matched independently, because the order words come to mind in is
// rarely the order they appear in.
func TestFilterMatchesTermsOutOfOrder(t *testing.T) {
	rows := []inventory.Row{row("a", "api token auth", task.Hot, "")}
	m := tui.New(newActions(&recorder{}, rows), rows, nil)

	m = send(m, key("/"))
	for _, k := range typeText("auth api") {
		m = send(m, k)
	}
	if !strings.Contains(m.View(), "api token auth") {
		t.Errorf("out-of-order terms should still match:\n%s", m.View())
	}
}

func TestStartTaskFromRepoView(t *testing.T) {
	repos := []tui.RepoRow{repoRow("api")}
	rec := &recorder{}
	m := tui.New(newActions(rec, nil), nil, repos)

	m = send(m, key("tab"), key("s"))
	if !strings.Contains(m.View(), "start work in api") {
		t.Fatalf("s should open the start prompt:\n%s", m.View())
	}
	for _, k := range typeText("token refresh") {
		m = send(m, k)
	}
	send(m, key("enter"))

	if len(rec.started) != 1 || rec.started[0] != "worktree:api/token refresh" {
		t.Errorf("s should start a worktree task in the selected repo, got %v", rec.started)
	}
}

func TestEnterOpensRepoInRepoView(t *testing.T) {
	repos := []tui.RepoRow{repoRow("api")}
	rec := &recorder{}
	m := tui.New(newActions(rec, nil), nil, repos)

	send(m, key("tab"), key("enter"))
	if len(rec.opened) != 1 || rec.opened[0] != "repo:api" {
		t.Errorf("enter in the repo view should open the repo, got %v", rec.opened)
	}
}

// Repositories with work in flight belong at the top: that is what someone
// opening the dashboard is looking for, not alphabetical order.
func TestReposSortWorkInFlightFirst(t *testing.T) {
	hot := repoRow("zzz-busy", &task.Task{ID: "x", Repo: "zzz-busy", Branch: "feat/x", State: task.Hot})
	idle := repoRow("aaa-idle")
	m := tui.New(newActions(&recorder{}, nil), nil, []tui.RepoRow{idle, hot})
	m = send(m, key("tab"), tea.WindowSizeMsg{Width: 120, Height: 40})

	out := m.View()
	if strings.Index(out, "zzz-busy") > strings.Index(out, "aaa-idle") {
		t.Errorf("the repo with a hot task should sort first:\n%s", out)
	}
}

func TestToolsOnlyListedWhenAvailable(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, "")}
	actions := newActions(&recorder{}, rows)
	actions.Tools = []tui.Tool{
		{Key: "L", Name: "lazygit", Command: []string{"lazygit"}, Available: func() bool { return true }},
		{Key: "Z", Name: "absent", Command: []string{"absent"}, Available: func() bool { return false }},
	}
	m := tui.New(actions, rows, nil)

	out := m.View()
	if !strings.Contains(out, "L lazygit") {
		t.Errorf("an available tool should be advertised:\n%s", out)
	}
	if strings.Contains(out, "Z absent") {
		t.Error("a tool that is not installed should not be offered")
	}
	if len(m.Tools()) != 1 {
		t.Errorf("Tools() should filter by availability, got %d", len(m.Tools()))
	}
}

func remoteRow(provider forge.Kind, fullName, local string) tui.RemoteRow {
	name := fullName
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return tui.RemoteRow{
		Repo: forge.RemoteRepo{
			Forge: provider, Name: name, FullName: fullName,
			Description:   "description for " + name,
			URL:           "https://example.com/" + fullName,
			CloneURL:      "https://example.com/" + fullName + ".git",
			DefaultBranch: "main", Visibility: "private",
			UpdatedAt: time.Now().Add(-24 * time.Hour),
		},
		LocalPath: local,
	}
}

func TestRemoteViewLoadsLazily(t *testing.T) {
	rows := []tui.RemoteRow{
		remoteRow(forge.GitHub, "owner/api", "/src/api"),
		remoteRow(forge.GitLab, "group/web", ""),
	}
	loads := 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) {
		loads++
		return rows, nil
	}
	m := tui.New(actions, nil, nil)

	if loads != 0 {
		t.Fatal("constructing the dashboard must not touch the network")
	}
	m = send(m, key("tab")) // repos
	if loads != 0 {
		t.Fatal("the local repo view must not touch the forge")
	}
	m = send(m, key("tab")) // remote
	if loads != 1 {
		t.Fatalf("first remote visit should load once, got %d", loads)
	}
	out := m.View()
	if !strings.Contains(out, "owner/api") || !strings.Contains(out, "group/web") {
		t.Errorf("remote rows missing:\n%s", out)
	}
	if !strings.Contains(out, "github") || !strings.Contains(out, "gitlab") {
		t.Errorf("providers should be visible:\n%s", out)
	}
}

func TestRemoteFilterUsesNameDescriptionAndProvider(t *testing.T) {
	rows := []tui.RemoteRow{
		remoteRow(forge.GitHub, "owner/api", ""),
		remoteRow(forge.GitLab, "group/web", ""),
	}
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return rows, nil }
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("/"))
	for _, k := range typeText("gitlab web") {
		m = send(m, k)
	}
	out := m.View()
	if !strings.Contains(out, "group/web") || strings.Contains(out, "owner/api") {
		t.Errorf("remote search should match provider + name terms:\n%s", out)
	}
}

func TestRemoteCloneRequiresConfirmation(t *testing.T) {
	rows := []tui.RemoteRow{remoteRow(forge.GitHub, "owner/api", "")}
	rec := &recorder{}
	actions := newActions(rec, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return rows, nil }
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"))

	// Enter on an uncloned remote does nothing; c is the explicit action.
	m = send(m, key("enter"))
	if len(rec.cloned) != 0 {
		t.Error("enter must not clone without a confirmation")
	}
	m = send(m, key("c"))
	if !strings.Contains(m.View(), "clone owner/api") {
		t.Fatalf("c should open the confirmation:\n%s", m.View())
	}
	send(m, key("enter"))
	if len(rec.cloned) != 1 || rec.cloned[0] != "owner/api" {
		t.Errorf("confirmed clone not triggered: %v", rec.cloned)
	}
}

func TestRemoteLocalCloneOpensWithEnter(t *testing.T) {
	rows := []tui.RemoteRow{remoteRow(forge.GitHub, "owner/api", "/src/api")}
	rec := &recorder{}
	actions := newActions(rec, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return rows, nil }
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("enter"))

	if len(rec.opened) != 1 || rec.opened[0] != "remote:owner/api" {
		t.Errorf("enter should open the existing local clone, got %v", rec.opened)
	}
}

func TestRemoteRefreshQueriesAgain(t *testing.T) {
	loads := 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) {
		loads++
		return nil, nil
	}
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"))
	m = send(m, key("r"))
	if loads != 2 {
		t.Errorf("initial visit + refresh should load twice, got %d", loads)
	}
}

func TestFreshRemoteCacheAvoidsNetworkOnFirstSwitch(t *testing.T) {
	loads := 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) {
		loads++
		return nil, nil
	}
	cached := []tui.RemoteRow{remoteRow(forge.GitHub, "owner/cached", "")}
	m := tui.New(actions, nil, nil).WithRemotes(cached)
	m = send(m, key("tab"), key("tab"))

	if loads != 0 {
		t.Errorf("fresh cache should make the first switch instant, network loads=%d", loads)
	}
	if !strings.Contains(m.View(), "owner/cached") {
		t.Errorf("cached remote missing:\n%s", m.View())
	}
	m = send(m, key("r"))
	if loads != 1 {
		t.Errorf("r should refresh explicitly, network loads=%d", loads)
	}
}

func TestRepoViewShowsLiveRuntimeExplicitly(t *testing.T) {
	r := repoRow("api")
	r.Live = true
	r.Runtime, r.RuntimeHandle, r.RuntimeStatus = "herdr", "w7", "working"
	m := tui.New(newActions(&recorder{}, nil), nil, []tui.RepoRow{r})
	m = send(m, key("tab"))

	out := m.View()
	if !strings.Contains(out, "LIVE") || !strings.Contains(out, "herdr:working") {
		t.Errorf("repo row should make active workspace status explicit:\n%s", out)
	}
	if !strings.Contains(out, "herdr w7 · working") {
		t.Errorf("detail should show the workspace handle:\n%s", out)
	}
}

func TestHeatmapShortcutAndRefresh(t *testing.T) {
	rows := []inventory.Row{row("a", "token refresh", task.Hot, "")}
	loads := 0
	actions := newActions(&recorder{}, rows)
	actions.LoadStats = func(context.Context, string) (tui.StatsPanel, error) {
		loads++
		return tui.StatsPanel{
			Repo: "demo", Heatmap: "HEATMAP-GRID\n", Seconds: 5400,
			ActiveDays: 3, Since: time.Now().AddDate(-1, 0, 0), Until: time.Now(),
		}, nil
	}
	m := tui.New(actions, rows, nil)
	if !strings.Contains(m.View(), "H stats") {
		t.Errorf("footer should expose the shortcut:\n%s", m.View())
	}
	m = send(m, key("H"))
	out := m.View()
	if !strings.Contains(out, "HEATMAP") || !strings.Contains(out, "HEATMAP-GRID") ||
		!strings.Contains(out, "1h 30m") {
		t.Errorf("stats overlay not rendered:\n%s", out)
	}
	m = send(m, key("r"))
	if loads != 2 {
		t.Errorf("H load + r refresh = 2, got %d", loads)
	}
	m = send(m, key("esc"))
	if strings.Contains(m.View(), "HEATMAP-GRID") {
		t.Error("esc should return to the list")
	}
}

func TestConfigEditReturnsSuspendingProcess(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, "")}
	edits := 0
	actions := newActions(&recorder{}, rows)
	actions.EditConfig = func() (*exec.Cmd, error) {
		edits++
		return exec.Command("true"), nil
	}
	m := tui.New(actions, rows, nil)
	_, cmd := m.Update(key("e"))
	if edits != 1 || cmd == nil {
		t.Errorf("e should resolve one editor process for tea.ExecProcess: edits=%d cmd=%v", edits, cmd)
	}
	// tea.ExecProcess invokes its callback through Program's exec machinery,
	// not by returning a normal message from cmd(), so r covers the reload
	// path independently below.
}

func TestRReloadsConfigAsWellAsData(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, "")}
	configReloads, dataReloads := 0, 0
	actions := newActions(&recorder{}, rows)
	actions.ReloadConfig = func(context.Context) ([]tui.Tool, string, error) {
		configReloads++
		return nil, "reloaded", nil
	}
	actions.Reload = func(context.Context) ([]inventory.Row, error) {
		dataReloads++
		return rows, nil
	}
	m := tui.New(actions, rows, nil)
	send(m, key("r"))
	if configReloads != 1 || dataReloads != 1 {
		t.Errorf("r should reload both: config=%d data=%d", configReloads, dataReloads)
	}
}

func TestStartDirectTaskFromRepoView(t *testing.T) {
	repos := []tui.RepoRow{repoRow("api")}
	rec := &recorder{}
	m := tui.New(newActions(rec, nil), nil, repos)
	m = send(m, key("tab"), key("d"))
	if !strings.Contains(m.View(), "track direct work in api") {
		t.Fatalf("d should open the direct-task prompt:\n%s", m.View())
	}
	for _, k := range typeText("quick fix") {
		m = send(m, k)
	}
	send(m, key("enter"))
	if len(rec.started) != 1 || rec.started[0] != "direct:api/quick fix" {
		t.Errorf("d should start direct work, got %v", rec.started)
	}
}
