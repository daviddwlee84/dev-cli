package tui_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
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

func tryRow(id, name string, phase catalog.ExperimentPhase, state catalog.LocationState, tags ...string) tui.TryRow {
	path := "/src/tries/" + name
	location := catalog.Location{State: state, CurrentPath: path}
	if state == catalog.LocationArchived {
		location.RestorePath = path
		location.CurrentPath = "/src/tries/.dev/archive/" + id + "/" + name
	}
	entry := &catalog.Entry{
		ID: id, Kind: catalog.KindTry, Name: name, Tags: tags,
		Experiment: &catalog.Experiment{Phase: phase, Slug: name, OriginalPath: path},
		Locations:  map[string]catalog.Location{"test": location},
	}
	item := experiment.Item{
		Entry: entry, ID: id, Kind: catalog.KindTry, Name: name, Basename: name,
		Slug: name, Phase: phase, Tags: append([]string(nil), tags...),
		Live: experiment.LiveFacts{
			Present: state == catalog.LocationPresent, CurrentPath: location.CurrentPath,
			RealPath: location.CurrentPath,
		},
	}
	copy := location
	return tui.TryRow{Item: item, Location: &copy}
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
	if m.CurrentView() != tui.ViewTries {
		t.Error("the third view should be Try")
	}
	m = send(m, key("tab"))
	if m.CurrentView() != tui.ViewRemote {
		t.Error("the fourth view should be remote")
	}
	if send(m, key("tab")).CurrentView() != tui.ViewTasks {
		t.Error("a fourth tab should cycle back round")
	}
}

func TestTryViewRendersFiltersAndOpensThroughActions(t *testing.T) {
	important := tryRow("try-a", "redis-streams", catalog.PhaseActive, catalog.LocationPresent, "important", "go")
	important.Item.Note = "compare consumer groups"
	other := tryRow("try-b", "queue-bench", catalog.PhaseActive, catalog.LocationPresent, "rust")
	rows := []tui.TryRow{other, important}
	var requests []tui.TryRequest
	actions := newActions(&recorder{}, nil)
	actions.Tries = tui.TryActions{
		Reload: func(context.Context, bool) ([]tui.TryRow, error) { return rows, nil },
		Apply: func(_ context.Context, request tui.TryRequest) (tui.TryActionResult, error) {
			requests = append(requests, request)
			return tui.TryActionResult{Status: "opened"}, nil
		},
	}
	m := tui.New(actions, nil, nil).WithTries(rows)
	m = send(m, key("tab"), key("tab"))
	out := m.View()
	for _, want := range []string{"redis-streams", "queue-bench", "PHASE", "WHERE", "important", "compare consumer groups"} {
		if !strings.Contains(out, want) {
			t.Errorf("TRY view missing %q:\n%s", want, out)
		}
	}

	m = send(m, key("/"))
	for _, typed := range typeText("tag:important") {
		m = send(m, typed)
	}
	if out := m.View(); !strings.Contains(out, "redis-streams") || strings.Contains(out, "queue-bench") {
		t.Fatalf("structured Try filter did not narrow rows:\n%s", out)
	}
	m = send(m, key("enter"), key("enter"))
	if len(requests) != 1 || requests[0].Action != tui.TryOpen || requests[0].ID != "try-a" {
		t.Fatalf("Try open requests = %+v", requests)
	}
}

func TestTryHistoryToggleReloadsOnlyTryInventory(t *testing.T) {
	active := tryRow("active", "active", catalog.PhaseActive, catalog.LocationPresent)
	archived := tryRow("archived", "archived", catalog.PhaseDeprecated, catalog.LocationArchived)
	loads := 0
	var requestedAll bool
	actions := newActions(&recorder{}, nil)
	actions.Tries.Reload = func(_ context.Context, all bool) ([]tui.TryRow, error) {
		loads++
		requestedAll = all
		if all {
			return []tui.TryRow{active, archived}, nil
		}
		return []tui.TryRow{active}, nil
	}
	m := tui.New(actions, nil, nil).WithTries([]tui.TryRow{active})
	m = send(m, key("tab"), key("tab"), key("a"))
	if loads != 1 || !requestedAll || !strings.Contains(m.View(), "archived") {
		t.Fatalf("history toggle loads=%d all=%v:\n%s", loads, requestedAll, m.View())
	}
}

func TestTryCreateFormSubmitsNormalizedRequest(t *testing.T) {
	var request tui.TryRequest
	actions := newActions(&recorder{}, nil)
	actions.Tries = tui.TryActions{
		Reload: func(context.Context, bool) ([]tui.TryRow, error) { return nil, nil },
		Apply: func(_ context.Context, got tui.TryRequest) (tui.TryActionResult, error) {
			request = got
			return tui.TryActionResult{Status: "created"}, nil
		},
	}
	m := tui.New(actions, nil, nil).WithTries(nil)
	m = send(m, key("tab"), key("tab"), key("n"))
	if !strings.Contains(m.View(), "NEW TRY") {
		t.Fatalf("n did not open the create form:\n%s", m.View())
	}
	for _, typed := range typeText("cache probe") {
		m = send(m, typed)
	}
	m = send(m, key("tab"), key("tab"))
	// Replace the seeded yes with no using textinput's standard ctrl+w behavior.
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlW})
	for _, typed := range typeText("no") {
		m = send(m, typed)
	}
	m = send(m, key("enter"))
	if request.Action != tui.TryCreate || request.Name != "cache probe" || !request.NoGit {
		t.Fatalf("create request = %+v", request)
	}
}

func TestRepoMarkOverlayUsesSharedCatalogAction(t *testing.T) {
	row := repoRow("api")
	row.Asset = &catalog.Entry{Kind: catalog.KindRepository, Tags: []string{"keep"}, Note: "old"}
	var gotTags []string
	var gotNote string
	actions := newActions(&recorder{}, nil)
	actions.Repos.Patch = func(_ context.Context, got tui.RepoRow, tags []string, note string) (string, error) {
		if got.Repo.Name != "api" {
			t.Errorf("patched repo = %s", got.Repo.Name)
		}
		gotTags, gotNote = append([]string(nil), tags...), note
		return "updated", nil
	}
	m := tui.New(actions, nil, []tui.RepoRow{row})
	m = send(m, key("tab"), key(" "))
	if out := m.View(); !strings.Contains(out, "MARK API") || !strings.Contains(out, row.Repo.Path) {
		t.Fatalf("repo mark form missing target:\n%s", out)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlW})
	for _, typed := range typeText("important") {
		m = send(m, typed)
	}
	m = send(m, key("tab"), tea.KeyMsg{Type: tea.KeyCtrlW})
	for _, typed := range typeText("primary") {
		m = send(m, typed)
	}
	m = send(m, key("enter"))
	if strings.Join(gotTags, ",") != "important" || gotNote != "primary" {
		t.Fatalf("repo patch tags=%v note=%q", gotTags, gotNote)
	}
}

func TestTryMutationErrorCanRefreshWithoutHidingTheFailure(t *testing.T) {
	row := tryRow("partial", "partial", catalog.PhaseActive, catalog.LocationPresent)
	loads := 0
	actions := newActions(&recorder{}, nil)
	actions.Tries = tui.TryActions{
		Reload: func(context.Context, bool) ([]tui.TryRow, error) {
			loads++
			return []tui.TryRow{row}, nil
		},
		Apply: func(context.Context, tui.TryRequest) (tui.TryActionResult, error) {
			return tui.TryActionResult{RefreshRepos: true}, fmt.Errorf("created but runtime failed")
		},
	}
	m := tui.New(actions, nil, nil).WithTries([]tui.TryRow{row})
	m = send(m, key("tab"), key("tab"), key("enter"))
	if loads != 1 || !strings.Contains(m.View(), "created but runtime failed") {
		t.Fatalf("partial mutation refresh loads=%d:\n%s", loads, m.View())
	}
}

func TestTryArchiveActionRequiresExactYES(t *testing.T) {
	row := tryRow("archive-me", "archive-me", catalog.PhaseActive, catalog.LocationPresent)
	var requests []tui.TryRequest
	actions := newActions(&recorder{}, nil)
	actions.Tries = tui.TryActions{
		Reload: func(context.Context, bool) ([]tui.TryRow, error) { return []tui.TryRow{row}, nil },
		Apply: func(_ context.Context, request tui.TryRequest) (tui.TryActionResult, error) {
			requests = append(requests, request)
			return tui.TryActionResult{Status: "archived"}, nil
		},
	}
	m := tui.New(actions, nil, nil).WithTries([]tui.TryRow{row})
	m = send(m, key("tab"), key("tab"), key(" "), key("down"), key("down"), key("enter"))
	if !strings.Contains(m.View(), "CONFIRM ARCHIVE") || !strings.Contains(m.View(), row.Item.Live.CurrentPath) {
		t.Fatalf("archive review did not show its target:\n%s", m.View())
	}
	for _, typed := range typeText("yes") {
		m = send(m, typed)
	}
	m = send(m, key("enter"))
	if len(requests) != 0 || !strings.Contains(m.View(), "exactly YES") {
		t.Fatalf("lowercase confirmation applied archive: %+v\n%s", requests, m.View())
	}
	m = send(m, key("esc"), key(" "), key("down"), key("down"), key("enter"))
	for _, typed := range typeText("YES") {
		m = send(m, typed)
	}
	m = send(m, key("enter"))
	if len(requests) != 1 || requests[0].Action != tui.TryArchive || requests[0].ID != row.Item.ID {
		t.Fatalf("archive requests = %+v", requests)
	}
}

func TestHelpOverlayIsDiscoverableAndClosesWithoutQuitting(t *testing.T) {
	m := tui.New(newActions(&recorder{}, nil), nil, nil)
	m = send(m, key("?"))
	if out := m.View(); !strings.Contains(out, "KEYBOARD HELP") || !strings.Contains(out, "TRY") {
		t.Fatalf("help overlay missing Try bindings:\n%s", out)
	}
	m = send(m, key("q"))
	if out := m.View(); out == "" || strings.Contains(out, "KEYBOARD HELP") {
		t.Fatalf("q should close help without quitting:\n%s", out)
	}
}

func TestTrySortCyclesIndependently(t *testing.T) {
	old := tryRow("old", "aaa-old", catalog.PhaseActive, catalog.LocationPresent)
	old.Item.LastOpened = time.Now().Add(-24 * time.Hour)
	newest := tryRow("new", "zzz-new", catalog.PhaseActive, catalog.LocationPresent)
	newest.Item.LastOpened = time.Now()
	m := tui.New(newActions(&recorder{}, nil), nil, nil).WithTries([]tui.TryRow{old, newest})
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); strings.Index(out, "zzz-new") > strings.Index(out, "aaa-old") {
		t.Fatalf("activity sort did not put newest first:\n%s", out)
	}
	m = send(m, key("O"))
	if out := m.View(); strings.Index(out, "aaa-old") > strings.Index(out, "zzz-new") || !strings.Contains(out, "O sort:name") {
		t.Fatalf("name sort did not apply:\n%s", out)
	}
}

func TestSizeStreamUpdatesRepoAndTryRowsWithoutBlockingInitialRender(t *testing.T) {
	repository := repoRow("api")
	repository.SizeTarget = diskusage.Plain(repository.Repo.Path)
	try := tryRow("sized-try", "sized-try", catalog.PhaseActive, catalog.LocationPresent)
	try.SizeTarget = diskusage.Plain(try.Item.Live.CurrentPath)
	starts := 0
	forceSeen := false
	actions := newActions(&recorder{}, nil)
	actions.ReloadRepos = func(context.Context) ([]tui.RepoRow, error) { return []tui.RepoRow{repository}, nil }
	actions.Tries.Reload = func(context.Context, bool) ([]tui.TryRow, error) { return []tui.TryRow{try}, nil }
	actions.Sizes.Start = func(_ context.Context, targets []diskusage.Target, force bool) diskusage.Load {
		starts++
		forceSeen = force
		results := make(chan diskusage.Result, len(targets))
		for index, target := range targets {
			owned := int64((index + 1) * 1024)
			total := owned
			usage := diskusage.Usage{
				CheckoutBytes: owned, OwnedBytes: owned, TotalBytes: &total,
				Complete: true, MeasuredAt: time.Now().UTC(),
			}
			results <- diskusage.Result{LoadID: 7, Key: target.Key, Usage: usage}
		}
		close(results)
		return diskusage.Load{ID: 7, Results: results}
	}
	m := tui.New(actions, nil, []tui.RepoRow{repository}).WithTries([]tui.TryRow{try})
	// Before a reload completes, rows render immediately with a non-blocking marker.
	m = send(m, key("tab"))
	if out := m.View(); !strings.Contains(out, "SIZE") || !strings.Contains(out, "…") {
		t.Fatalf("unmeasured repo row did not render immediately:\n%s", out)
	}
	m = send(m, key("r"))
	if starts != 1 || !forceSeen || !strings.Contains(m.View(), "KiB") {
		t.Fatalf("size stream starts=%d force=%v:\n%s", starts, forceSeen, m.View())
	}
	m = send(m, key("tab"))
	if out := m.View(); !strings.Contains(out, "KiB") || !strings.Contains(out, "SIZE") {
		t.Fatalf("Try size result missing:\n%s", out)
	}
}

func TestTrySizeFilterSortAndSharedDetail(t *testing.T) {
	small := tryRow("small", "small", catalog.PhaseActive, catalog.LocationPresent)
	large := tryRow("large", "large", catalog.PhaseActive, catalog.LocationPresent)
	for row, bytes := range map[*tui.TryRow]int64{&small: 512, &large: 4096} {
		total := bytes
		row.SizeTarget = diskusage.Plain(row.Item.Live.CurrentPath)
		row.Usage = &diskusage.Usage{
			CheckoutBytes: bytes, OwnedBytes: bytes, TotalBytes: &total,
			Complete: true, MeasuredAt: time.Now().UTC(),
		}
	}
	shared := int64(8192)
	large.Usage.TotalBytes = nil
	large.Usage.SharedGitBytes = &shared
	m := tui.New(newActions(&recorder{}, nil), nil, nil).WithTries([]tui.TryRow{small, large})
	m = send(m, key("tab"), key("tab"), key("/"))
	for _, typed := range typeText("size:>1KiB") {
		m = send(m, typed)
	}
	if out := m.View(); !strings.Contains(out, "large") || strings.Contains(out, "small") {
		t.Fatalf("size filter failed:\n%s", out)
	}
	m = send(m, key("enter"))
	if out := m.View(); !strings.Contains(out, "not reclaimable") {
		t.Fatalf("shared size detail failed:\n%s", out)
	}
	m = send(m, key("O"), key("O"), key("O"))
	if out := m.View(); !strings.Contains(out, "O sort:size") {
		t.Fatalf("Try size sort did not cycle:\n%s", out)
	}
}

func TestTryTableAdaptsWithoutScrollingTabsOffNarrowTerminals(t *testing.T) {
	var rows []tui.TryRow
	for index := 0; index < 30; index++ {
		rows = append(rows, tryRow(fmt.Sprintf("try-%02d", index), fmt.Sprintf("實驗-%02d", index),
			catalog.PhaseActive, catalog.LocationPresent, "important", "prototype"))
	}
	for _, width := range []int{60, 80, 120} {
		m := tui.New(newActions(&recorder{}, nil), nil, nil).WithTries(rows)
		m = send(m, tea.WindowSizeMsg{Width: width, Height: 24}, key("tab"), key("tab"))
		output := m.View()
		lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
		if len(lines) > 24 || !strings.Contains(lines[0], "TASKS") || !strings.Contains(lines[0], "TRY") {
			t.Fatalf("width %d lost top tabs or overflowed height (%d lines):\n%s", width, len(lines), output)
		}
		for _, line := range lines {
			if strings.Contains(line, "WHERE") && lipgloss.Width(line) > width {
				t.Fatalf("width %d Try header is %d cells:\n%s", width, lipgloss.Width(line), line)
			}
		}
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

func TestRepoViewHidesTriesButRemoteMatchingUsesAndClearsThem(t *testing.T) {
	ordinary := repoRow("api")
	tryRepo := repoRow("scratch")
	tryRepo.Asset = &catalog.Entry{
		Kind: catalog.KindTry,
		Experiment: &catalog.Experiment{
			Phase: catalog.PhaseActive,
		},
	}
	repos := []tui.RepoRow{ordinary, tryRepo}
	remote := remoteRow(forge.GitHub, "owner/scratch", tryRepo.Repo.Path)
	remote.LocalKind = catalog.KindTry

	actions := newActions(&recorder{}, nil)
	actions.ReloadRepos = func(context.Context) ([]tui.RepoRow, error) { return repos, nil }
	m := tui.New(actions, nil, repos).WithRemotes([]tui.RemoteRow{remote})
	m = send(m, key("tab"))
	if out := m.View(); strings.Contains(out, "scratch") || !strings.Contains(out, "api") {
		t.Fatalf("REPOS should hide Try assets while retaining ordinary repos:\n%s", out)
	}
	if summary := m.Summary(); !strings.Contains(summary, "1 repos") || strings.Contains(summary, "2 repos") {
		t.Errorf("summary counted hidden Try: %q", summary)
	}

	m = send(m, key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "try") {
		t.Fatalf("REMOTE should retain the Try local kind:\n%s", out)
	}

	// Once the Try disappears from the fresh local snapshot, an ordinary local
	// reload must clear the cached marker rather than preserving stale state.
	repos = []tui.RepoRow{ordinary}
	m = send(m, key("tab"), key("tab"), key("r"), key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "not cloned") {
		t.Fatalf("stale remote Try marker survived local reload:\n%s", out)
	}
}

func TestRemoteMatchingDoesNotChooseBetweenDuplicateLocalClones(t *testing.T) {
	first := repoRow("first")
	second := repoRow("second")
	first.RemoteForge, first.RemoteName = forge.GitHub, "owner/shared"
	second.RemoteForge, second.RemoteName = forge.GitHub, "owner/shared"
	repos := []tui.RepoRow{first, second}
	actions := newActions(&recorder{}, nil)
	actions.ReloadRepos = func(context.Context) ([]tui.RepoRow, error) { return repos, nil }
	remote := remoteRow(forge.GitHub, "owner/shared", "")
	m := tui.New(actions, nil, repos).WithRemotes([]tui.RemoteRow{remote})
	m = send(m, key("r"), key("tab"), key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "not cloned") {
		t.Fatalf("ambiguous local clones produced an arbitrary remote path:\n%s", out)
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
	m = send(m, key("tab")) // Try
	if loads != 0 {
		t.Fatal("the Try view must not touch the forge")
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("/"))
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
	m = send(m, key("tab"), key("tab"), key("tab"))

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
	m = send(m, key("tab"), key("tab"), key("tab"), key("enter"))

	if len(rec.opened) != 1 || rec.opened[0] != "remote:owner/api" {
		t.Errorf("enter should open the existing local clone, got %v", rec.opened)
	}
}

func TestRemoteViewLabelsLocalTryKind(t *testing.T) {
	row := remoteRow(forge.GitHub, "owner/experiment", "/src/tries/experiment")
	row.LocalKind = catalog.KindTry
	m := tui.New(newActions(&recorder{}, nil), nil, nil).WithRemotes([]tui.RemoteRow{row})
	m = send(m, key("tab"), key("tab"), key("tab"))
	out := m.View()
	if !strings.Contains(out, "try") || !strings.Contains(out, "asset") {
		t.Errorf("REMOTE view did not identify the local Try:\n%s", out)
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
	m = send(m, key("tab"), key("tab"), key("tab"))
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
	m = send(m, key("tab"), key("tab"), key("tab"))

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

func TestRepoAndTryDetailsExposeRecoveryTopology(t *testing.T) {
	topology := gitx.RecoveryTopology{
		Branches:          []gitx.BranchUpstream{{Branch: "main", OID: "abc"}},
		LocalOnlyBranches: []string{"main"},
	}
	repository := repoRow("local-only")
	repository.Topology = topology
	actions := newActions(&recorder{}, nil)
	actions.RepoColumns = []string{"repo", "remote"}
	m := tui.New(actions, nil, []tui.RepoRow{repository})
	m = send(m, key("tab"))
	if out := m.View(); !strings.Contains(out, "none · local:1") ||
		!strings.Contains(out, "local Git has no remote backup") || !strings.Contains(out, "local refs") {
		t.Fatalf("REPOS topology detail missing:\n%s", out)
	}

	try := tryRow("try-local", "try-local", catalog.PhaseActive, catalog.LocationPresent)
	try.Item.Live.Repo = &gitx.Repo{Name: "try-local", Root: try.Item.Live.CurrentPath}
	try.Topology = topology
	m = tui.New(newActions(&recorder{}, nil), nil, nil).WithTries([]tui.TryRow{try})
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "local Git has no remote backup") || !strings.Contains(out, "main") {
		t.Fatalf("TRY topology detail missing:\n%s", out)
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
	actions.ReloadConfig = func(context.Context) (tui.ConfigUpdate, string, error) {
		configReloads++
		return tui.ConfigUpdate{RepoColumns: []string{"repo", "latest"}, RepoSort: "latest"}, "reloaded", nil
	}
	actions.Reload = func(context.Context) ([]inventory.Row, error) {
		dataReloads++
		return rows, nil
	}
	m := tui.New(actions, rows, []tui.RepoRow{repoRow("api")})
	m = send(m, key("r"))
	if configReloads != 1 || dataReloads != 1 {
		t.Errorf("r should reload both: config=%d data=%d", configReloads, dataReloads)
	}
	m = send(m, key("tab"))
	if !strings.Contains(m.View(), "LATEST") || strings.Contains(m.View(), "BRANCH") {
		t.Errorf("live-reloaded columns should apply immediately:\n%s", m.View())
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

func TestRepoColumnsAreConfigurable(t *testing.T) {
	r := repoRow("api")
	r.LastActivity = time.Now().Add(-2 * time.Hour)
	actions := newActions(&recorder{}, nil)
	actions.RepoColumns = []string{"repo", "latest"}
	m := tui.New(actions, nil, []tui.RepoRow{r})
	m = send(m, key("tab"))
	out := m.View()
	if !strings.Contains(out, "REPO") || !strings.Contains(out, "LATEST") || !strings.Contains(out, "2h") {
		t.Errorf("configured columns missing:\n%s", out)
	}
	if strings.Contains(out, "BRANCH") || strings.Contains(out, "GIT") {
		t.Errorf("omitted columns should not render:\n%s", out)
	}
}

func TestRepoLatestSortAndReverse(t *testing.T) {
	old := repoRow("aaa-old")
	old.LastActivity = time.Now().Add(-7 * 24 * time.Hour)
	newest := repoRow("zzz-new")
	newest.LastActivity = time.Now().Add(-time.Hour)
	actions := newActions(&recorder{}, nil)
	actions.RepoSort = "latest"
	m := tui.New(actions, nil, []tui.RepoRow{old, newest})
	m = send(m, key("tab"))

	out := m.View()
	if strings.Index(out, "zzz-new") > strings.Index(out, "aaa-old") {
		t.Errorf("latest sort should put newest first:\n%s", out)
	}
	m = send(m, key("R"))
	out = m.View()
	if strings.Index(out, "aaa-old") > strings.Index(out, "zzz-new") {
		t.Errorf("reverse should invert the order:\n%s", out)
	}
}

func TestOCyclesRepoSort(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	actions.RepoSort = "activity"
	m := tui.New(actions, nil, []tui.RepoRow{repoRow("api")})
	m = send(m, key("tab"), key("O"))
	if !strings.Contains(m.View(), "O sort:latest") {
		t.Errorf("O should cycle activity → latest:\n%s", m.View())
	}
}

func TestEmptyHeatmapCanBackfillSelectedRepo(t *testing.T) {
	rows := []inventory.Row{row("a", "token refresh", task.Hot, "")}
	backfilled, loads := false, 0
	actions := newActions(&recorder{}, rows)
	actions.LoadStats = func(context.Context, string) (tui.StatsPanel, error) {
		loads++
		panel := tui.StatsPanel{Repo: "demo", Since: time.Now().AddDate(-1, 0, 0), Until: time.Now()}
		if backfilled {
			panel.Seconds, panel.ActiveDays, panel.Heatmap = 3600, 2, "BACKFILLED-GRID\n"
		}
		return panel, nil
	}
	actions.BackfillStats = func(_ context.Context, repo string) error {
		if repo != "demo" {
			t.Errorf("backfilled repo = %q", repo)
		}
		backfilled = true
		return nil
	}
	m := tui.New(actions, rows, nil)
	m = send(m, key("H"))
	if !strings.Contains(m.View(), "Press b to backfill only this repo") {
		t.Fatalf("empty panel should expose the action:\n%s", m.View())
	}
	m = send(m, key("b"))
	if !backfilled || loads != 2 {
		t.Errorf("backfilled=%v loads=%d, want true/2", backfilled, loads)
	}
	if !strings.Contains(m.View(), "BACKFILLED-GRID") {
		t.Errorf("panel should refresh after backfill:\n%s", m.View())
	}
}

func TestBeginLoadingShowsBeforeInventoryFinishes(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	m := tui.New(actions, nil, nil).BeginLoading()
	if !strings.Contains(m.View(), "Loading tasks, repositories, and experiments") {
		t.Errorf("startup should render a loading screen immediately:\n%s", m.View())
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("BeginLoading Init should schedule the inventory load")
	}
	if _, ok := cmd().(tea.BatchMsg); !ok {
		t.Error("initial command should batch cursor blink + background inventory")
	}
}

func TestRepoViewNeverScrollsTopBarOffTerminal(t *testing.T) {
	var repos []tui.RepoRow
	for i := 0; i < 30; i++ {
		r := repoRow(fmt.Sprintf("repo-%02d", i), &task.Task{
			ID: fmt.Sprintf("t-%02d", i), Repo: fmt.Sprintf("repo-%02d", i),
			Branch: "main", State: task.Hot,
		})
		r.Live = i == 0
		r.Runtime, r.RuntimeHandle, r.RuntimeStatus = "herdr", "w7", "working"
		r.Status = gitx.Status{Changed: 3, Staged: 1, Unstaged: 1, Untracked: 1}
		repos = append(repos, r)
	}
	actions := newActions(&recorder{}, nil)
	// Enough tools to force the footer to wrap, reproducing the original
	// REPOS-only overflow.
	for i := 0; i < 5; i++ {
		actions.Tools = append(actions.Tools, tui.Tool{
			Key: fmt.Sprintf("X%d", i), Name: "long-tool-name",
			Available: func() bool { return true },
		})
	}
	m := tui.New(actions, nil, repos)
	m = send(m, tea.WindowSizeMsg{Width: 86, Height: 24}, key("tab"))
	out := m.View()
	if !strings.HasPrefix(out, "dev") {
		t.Errorf("top bar must remain the first line:\n%s", out)
	}
	lines := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1
	if lines > 24 {
		t.Errorf("view has %d lines in a 24-line terminal; top bar will scroll off:\n%s", lines, out)
	}
}
