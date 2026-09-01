package tui_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
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
	opened   []string
	parked   []string
	started  []string
	cloned   []string
	copied   []string
	notes    []*note.Note
	deleted  []string
	searches []string
	edits    []string
	nexts    map[string]string
}

func newActions(r *recorder, rows []inventory.Row) tui.Actions {
	r.nexts = map[string]string{}
	return tui.Actions{
		Reload: func(context.Context) ([]inventory.Row, error) {
			return rows, nil
		},
		ReloadRepos:  func(context.Context) ([]tui.RepoRow, error) { return nil, nil },
		ReloadRemote: func(context.Context) ([]tui.RemoteRow, error) { return nil, nil },
		OpenRepo: func(_ context.Context, rr tui.RepoRow) (tui.OpenResult, error) {
			r.opened = append(r.opened, "repo:"+rr.Repo.Name)
			return tui.OpenResult{Status: "opened"}, nil
		},
		OpenCheckout: func(_ context.Context, rr tui.RepoRow, checkout inventory.RepoCheckout) (tui.OpenResult, error) {
			r.opened = append(r.opened, "worktree:"+checkout.Branch())
			return tui.OpenResult{Status: "opened"}, nil
		},
		OpenRemote: func(_ context.Context, rr tui.RemoteRow) (tui.OpenResult, error) {
			r.opened = append(r.opened, "remote:"+rr.Repo.FullName)
			return tui.OpenResult{Status: "opened"}, nil
		},
		CloneRemote: func(_ context.Context, rr tui.RemoteRow) (tui.OpenResult, string, error) {
			r.cloned = append(r.cloned, rr.Repo.FullName)
			return tui.OpenResult{Status: "cloned"}, "/src/" + rr.Repo.Name, nil
		},
		Start: func(_ context.Context, rr tui.RepoRow, name string) (string, error) {
			r.started = append(r.started, "worktree:"+rr.Repo.Name+"/"+name)
			return "started", nil
		},
		StartDirect: func(_ context.Context, rr tui.RepoRow, name string) (string, error) {
			r.started = append(r.started, "direct:"+rr.Repo.Name+"/"+name)
			return "started direct", nil
		},
		Open: func(_ context.Context, t *task.Task) (tui.OpenResult, error) {
			r.opened = append(r.opened, t.ID)
			return tui.OpenResult{Status: "opened " + t.ID}, nil
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
		Notes: tui.NoteActions{
			List: func(context.Context, tui.NoteTarget) ([]*note.Note, error) {
				return append([]*note.Note(nil), r.notes...), nil
			},
			Search: func(_ context.Context, _ tui.NoteTarget, query string) ([]*note.Note, error) {
				r.searches = append(r.searches, query)
				var out []*note.Note
				for _, n := range r.notes {
					if strings.Contains(strings.ToLower(n.Body), strings.ToLower(query)) {
						out = append(out, n)
					}
				}
				return out, nil
			},
			Add: func(_ context.Context, target tui.NoteTarget, body string) (string, error) {
				n := &note.Note{
					SchemaVersion: note.CurrentSchemaVersion,
					ID:            fmt.Sprintf("%08d-0000-4000-8000-000000000000", len(r.notes)+1),
					RepositoryID:  target.CatalogID, Repository: target.Name(), Body: body + "\n",
					Created: time.Now(), Updated: time.Now(),
				}
				r.notes = append([]*note.Note{n}, r.notes...)
				return "added note " + n.ID[:8], nil
			},
			Delete: func(_ context.Context, n *note.Note) (string, error) {
				r.deleted = append(r.deleted, n.ID)
				var kept []*note.Note
				for _, candidate := range r.notes {
					if candidate.ID != n.ID {
						kept = append(kept, candidate)
					}
				}
				r.notes = kept
				return "deleted note " + n.ID[:8], nil
			},
			Edit: func(n *note.Note) (tui.NoteEdit, error) {
				r.edits = append(r.edits, n.ID)
				return tui.NoteEdit{Command: exec.Command("true"), Complete: func(error) error { return nil }}, nil
			},
		},
		Copy: func(text string) error {
			r.copied = append(r.copied, text)
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

func TestSkillsCheckWaitsForInitialLocalSnapshot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	checks := 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadSkills = func(context.Context) ([]agentskill.Skill, error) {
		close(started)
		<-release
		return []agentskill.Skill{{Name: "loaded", Scope: agentskill.ScopeProject}}, nil
	}
	actions.CheckSkills = func(_ context.Context, rows []agentskill.Skill) []agentskill.Skill {
		checks++
		return rows
	}
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))
	next, loadCommand := m.Update(key("tab"))
	m = next.(tui.Model)
	loadResult := make(chan tea.Msg, 1)
	go func() { loadResult <- loadCommand() }()
	<-started

	next, checkCommand := m.Update(key("c"))
	m = next.(tui.Model)
	if checkCommand != nil || checks != 0 || !strings.Contains(m.View(), "wait for local agent skills") {
		t.Fatalf("check superseded initial skills load (checks=%d cmd=%v):\n%s", checks, checkCommand, m.View())
	}
	close(release)
	m = send(m, <-loadResult)
	m = send(m, key("c"))
	if checks != 1 || !strings.Contains(m.View(), "loaded") {
		t.Fatalf("check after local load failed (checks=%d):\n%s", checks, m.View())
	}
}

func TestSkillsViewLoadsLazilyFiltersAndRunsExplicitActions(t *testing.T) {
	rows := []agentskill.Skill{
		{
			Name: "project-skill", Scope: agentskill.ScopeProject, ScopeRoot: "/src/demo",
			Path: "/src/demo/.agents/skills/project-skill", Agents: []string{"Claude Code", "Codex"},
			Source: "owner/repo", ManagedBy: agentskill.ManagedBySkills,
			UpdateStatus: agentskill.UpdateUnchecked,
		},
		{
			Name: "global-skill", Scope: agentskill.ScopeGlobal, ScopeRoot: "/home/test",
			Path: "/home/test/.agents/skills/global-skill", Agents: []string{"Cursor"},
			ManagedBy: agentskill.ManagedByExternal, UpdateStatus: agentskill.UpdateUnknown,
		},
	}
	loaded, checked, added, updated := 0, 0, 0, 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadSkills = func(context.Context) ([]agentskill.Skill, error) {
		loaded++
		return append([]agentskill.Skill(nil), rows...), nil
	}
	actions.CheckSkills = func(_ context.Context, got []agentskill.Skill) []agentskill.Skill {
		checked++
		out := append([]agentskill.Skill(nil), got...)
		for i := range out {
			if out[i].ManagedBy == agentskill.ManagedBySkills {
				out[i].UpdateStatus = agentskill.UpdateCurrent
			}
		}
		return out
	}
	actions.AddSkill = func() (*exec.Cmd, error) {
		added++
		return exec.Command("true"), nil
	}
	actions.UpdateSkill = func(row agentskill.Skill) (*exec.Cmd, error) {
		updated++
		if row.Name != "project-skill" || row.Scope != agentskill.ScopeProject {
			t.Fatalf("updated wrong row: %+v", row)
		}
		return exec.Command("true"), nil
	}

	m := tui.New(actions, nil, nil)
	if loaded != 0 {
		t.Fatal("skills must not load during dashboard construction")
	}
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"), key("tab"))
	if loaded != 1 || m.CurrentView() != tui.ViewSkills {
		t.Fatalf("skills load/view = %d/%s", loaded, m.CurrentView())
	}
	view := m.View()
	for _, want := range []string{"SKILLS", "project-skill", "global-skill", "project", "global", "a add", "c check", "u update selected"} {
		if !strings.Contains(view, want) {
			t.Errorf("skills view missing %q:\n%s", want, view)
		}
	}
	m = send(m, tea.WindowSizeMsg{Width: 60, Height: 24})
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "SCOPE") && lipgloss.Width(line) > 60 {
			t.Fatalf("narrow skills header is %d cells:\n%s", lipgloss.Width(line), line)
		}
	}

	m = send(m, key("c"))
	if checked != 1 || !strings.Contains(m.View(), "current") {
		t.Fatalf("check action = %d\n%s", checked, m.View())
	}
	m = send(m, key("a"))
	if added != 1 {
		t.Fatalf("add action = %d", added)
	}

	// Reload after the suspended add process is driven by Bubble Tea itself in
	// production; seed the view again here, then verify the guarded update path.
	m = tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"), key("tab"), key("u"))
	if !strings.Contains(m.View(), "update project skill project-skill") {
		t.Fatalf("update confirmation missing:\n%s", m.View())
	}
	m = send(m, key("enter"))
	if updated != 1 {
		t.Fatalf("update action = %d", updated)
	}

	m = tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"), key("tab"), key("/"))
	for _, msg := range typeText("scope:global") {
		m = send(m, msg)
	}
	m = send(m, key("enter"))
	filtered := m.View()
	if strings.Contains(filtered, "project-skill") || !strings.Contains(filtered, "global-skill") {
		t.Fatalf("scope filter failed:\n%s", filtered)
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

func TestEnterDefersRuntimeActivationUntilAfterTUIExit(t *testing.T) {
	rows := []inventory.Row{row("a", "first", task.Hot, "")}
	actions := newActions(&recorder{}, rows)
	actions.Open = func(context.Context, *task.Task) (tui.OpenResult, error) {
		return tui.OpenResult{Status: "opened", RuntimeHandle: "task-a"}, nil
	}
	m := send(tui.New(actions, rows, nil), key("enter"))
	if got := m.Activation(); got != "task-a" {
		t.Fatalf("Activation = %q, want task-a", got)
	}
	if got := m.Chosen(); got != "" {
		t.Fatalf("runtime activation must not become a cd directive: %q", got)
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

func repoRowWithWorktrees() tui.RepoRow {
	r := repoRow("api")
	r.Runtime, r.Worktrees = "herdr", 2
	r.Context = inventory.RepoContext{
		Repo: r.Repo, Runtime: "herdr", WorktreeCount: 2,
		Checkouts: []inventory.RepoCheckout{
			{Worktree: gitx.Worktree{Path: "/src/api", Branch: "main", Main: true}, Exists: true,
				Ownership: inventory.CheckoutCanonical, Sessions: []runtime.Session{{Handle: "w0", AgentStatus: "idle"}}},
			{Worktree: gitx.Worktree{Path: "/wt/api/feat-one", Branch: "feat/one"}, Exists: true,
				Ownership: inventory.CheckoutExternal, Sessions: []runtime.Session{{
					Handle: "w1", AgentStatus: "working", AgentSessions: []string{"codex:one"},
				}}},
			{Worktree: gitx.Worktree{Path: "/src/api/.claude/worktrees/turn-2", Branch: "worktree-turn-2"}, Exists: true,
				Ownership: inventory.CheckoutEphemeral},
		},
	}
	r.Live = true
	return r
}

func TestRepoWorktreeTreeExpandsNavigatesAndCollapses(t *testing.T) {
	rec := &recorder{}
	m := tui.New(newActions(rec, nil), nil, []tui.RepoRow{repoRowWithWorktrees()})
	m = send(m, tea.WindowSizeMsg{Width: 180, Height: 40}, key("tab"))
	if out := m.View(); !strings.Contains(out, "▸ api") || strings.Contains(out, "feat-one") {
		t.Fatalf("collapsed repo tree:\n%s", out)
	}
	m = send(m, key(" "))
	out := m.View()
	for _, want := range []string{"▾ api", "├─ feat-one (external)", "└─ turn-2 (ephemeral)", "herdr:2 live"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded tree missing %q:\n%s", want, out)
		}
	}
	send(m, key("down"), key("enter"))
	if len(rec.opened) != 1 || rec.opened[0] != "worktree:feat/one" {
		t.Errorf("child enter opened %v", rec.opened)
	}
	m = send(m, key("down"), key(" "))
	if out := m.View(); strings.Contains(out, "feat-one") || !strings.Contains(out, "▸ api") {
		t.Errorf("space on child should collapse to parent:\n%s", out)
	}
}

func TestRepoCopyChordsUseContextualScope(t *testing.T) {
	r := repoRowWithWorktrees()
	rec := &recorder{}
	m := tui.New(newActions(rec, nil), nil, []tui.RepoRow{r})
	m = send(m, key("tab"), key("y"))
	if !strings.Contains(m.View(), "y context") {
		t.Fatalf("y should show the copy menu:\n%s", m.View())
	}
	m = send(m, key("y"))
	if len(rec.copied) != 1 || !strings.Contains(rec.copied[0], "# dev repo context") ||
		!strings.Contains(rec.copied[0], "/wt/api/feat-one") {
		t.Fatalf("parent yy payload: %q", rec.copied)
	}

	m = send(m, key(" "), key("down"), key("y"), key("y"))
	if len(rec.copied) != 2 || !strings.Contains(rec.copied[1], "# dev worktree context") ||
		!strings.Contains(rec.copied[1], "/wt/api/feat-one") || strings.Contains(rec.copied[1], "turn-2") {
		t.Fatalf("child yy payload: %q", rec.copied)
	}
	m = send(m, key("y"), key("p"), key("y"), key("b"), key("y"), key("s"), key("y"), key("w"))
	if got := rec.copied[len(rec.copied)-4]; got != "/wt/api/feat-one" {
		t.Errorf("yp = %q", got)
	}
	if got := rec.copied[len(rec.copied)-3]; got != "feat/one" {
		t.Errorf("yb = %q", got)
	}
	if got := rec.copied[len(rec.copied)-2]; !strings.Contains(got, "codex:one") {
		t.Errorf("ys = %q", got)
	}
	if got := rec.copied[len(rec.copied)-1]; got != "/wt/api/feat-one\n/src/api/.claude/worktrees/turn-2" {
		t.Errorf("yw = %q", got)
	}
}

func TestRepoReadinessDetailUsesLoadedFactsWithoutNetwork(t *testing.T) {
	remoteCalls, fleetCalls := 0, 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) {
		remoteCalls++
		return nil, errors.New("network must not run")
	}
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		fleetCalls++
		return nil, errors.New("network must not run")
	}
	m := tui.New(actions, nil, []tui.RepoRow{repoRowWithWorktrees()})
	m = send(m, tea.WindowSizeMsg{Width: 180, Height: 40}, key("tab"))
	if remoteCalls != 0 || fleetCalls != 0 {
		t.Fatalf("repo detail triggered external collection: forge=%d fleet=%d", remoteCalls, fleetCalls)
	}
	out := m.View()
	if !strings.Contains(out, "ready") || !strings.Contains(out, "checkout eligible · task not-applicable · worktree blocked") {
		t.Errorf("repo detail missing scoped local readiness:\n%s", out)
	}
	if remoteCalls != 0 || fleetCalls != 0 {
		t.Fatalf("rendering repo detail triggered external collection: forge=%d fleet=%d", remoteCalls, fleetCalls)
	}
}

func TestRepoDetailsRenderInventoryFailuresAsUnavailable(t *testing.T) {
	row := repoRowWithWorktrees()
	row.Context.TaskErr = errors.New("task inventory failed")
	row.Context.RuntimeErr = errors.New("runtime inventory failed")
	model := tui.New(newActions(&recorder{}, nil), nil, []tui.RepoRow{row})
	model = send(model, tea.WindowSizeMsg{Width: 180, Height: 40}, key("tab"))
	parent := model.View()
	if !strings.Contains(parent, "tasks unavailable") || !strings.Contains(parent, "live  unavailable") || strings.Contains(parent, "none — press s") {
		t.Fatalf("parent inventory errors rendered as known empty:\n%s", parent)
	}
	model = send(model, key(" "), key("down"))
	child := model.View()
	if !strings.Contains(child, "tasks unavailable") || !strings.Contains(child, "live  unavailable") || strings.Contains(child, "untracked") || strings.Contains(child, "live  closed") {
		t.Fatalf("child inventory errors rendered as known empty:\n%s", child)
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
	if m.CurrentView() != tui.ViewFleet {
		t.Error("the third view should be Fleet")
	}
	m = send(m, key("tab"))
	if m.CurrentView() != tui.ViewTries {
		t.Error("the fourth view should be Try")
	}
	m = send(m, key("tab"))
	if m.CurrentView() != tui.ViewRemote {
		t.Error("the fifth view should be remote")
	}
	m = send(m, key("tab"))
	if m.CurrentView() != tui.ViewSkills {
		t.Error("the fifth view should be skills")
	}
	if send(m, key("tab")).CurrentView() != tui.ViewTasks {
		t.Error("a fifth tab should cycle back round")
	}
}

func TestFleetViewLoadsLazilyAndRendersRepositoryState(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	opened := false
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		return []tui.FleetRow{{
			Host: "lab", State: fleet.HostOK,
			Repository: &fleet.RepoSnapshot{
				Name: "api", Display: "api", Path: "/srv/api", Branch: "main",
				Status: gitx.Status{Upstream: "origin/main", Behind: 2},
				Tasks:  fleet.TaskCounts{Hot: 1}, Live: true, Runtime: "herdr", AgentStatus: "working",
			},
		}}, nil
	}
	actions.OpenFleet = func(context.Context, tui.FleetRow) (*exec.Cmd, error) {
		opened = true
		return exec.Command("true"), nil
	}
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"))
	out := m.View()
	for _, want := range []string{"FLEET", "lab", "api", "herdr · working", "H1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("fleet view missing %q:\n%s", want, out)
		}
	}
	_ = send(m, key("enter"))
	if !opened {
		t.Fatal("enter did not route through the fleet open action")
	}
}

func TestFleetHidesLocalRowsByDefaultAndTogglesThemWithA(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		return []tui.FleetRow{
			{
				Host: "laptop", Local: true, State: fleet.HostOK,
				Repository: &fleet.RepoSnapshot{Name: "local-api", Display: "local-api", Path: "/src/local-api", Branch: "main"},
			},
			{
				Host: "lab", State: fleet.HostOK,
				Repository: &fleet.RepoSnapshot{Name: "remote-api", Display: "remote-api", Path: "/srv/remote-api", Branch: "main"},
			},
		}, nil
	}

	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); strings.Contains(out, "local-api") || !strings.Contains(out, "remote-api") ||
		!strings.Contains(out, "1 fleet") || !strings.Contains(out, "a local:hidden") {
		t.Fatalf("FLEET did not default to remote-only rows:\n%s", out)
	}

	m = send(m, key("a"))
	if out := m.View(); !strings.Contains(out, "local-api") || !strings.Contains(out, "remote-api") ||
		!strings.Contains(out, "2 fleet") || !strings.Contains(out, "a local:shown") {
		t.Fatalf("a did not reveal local fleet rows:\n%s", out)
	}
}

func TestFleetOnlyLocalRowsExplainsHowToRevealThem(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		return []tui.FleetRow{{Host: "laptop", Local: true, State: fleet.HostOK}}, nil
	}
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "Press a to include this machine") {
		t.Fatalf("local-only fleet had no reveal hint:\n%s", out)
	}
}

func TestFleetWaitsForAndReusesAcceptedRepositorySnapshot(t *testing.T) {
	localResults := make(chan tui.LocalResult, 3)
	var request tui.LocalLoadRequest
	var gotRepos []tui.RepoRow
	fleetLoads := 0
	actions := newActions(&recorder{}, nil)
	actions.Local.Start = func(_ context.Context, got tui.LocalLoadRequest) tui.LocalLoad {
		request = got
		return tui.LocalLoad{ID: 9, Request: got, Results: localResults}
	}
	actions.ReloadFleetWithRepos = func(_ context.Context, repos []tui.RepoRow) ([]tui.FleetRow, error) {
		fleetLoads++
		gotRepos = append([]tui.RepoRow(nil), repos...)
		return []tui.FleetRow{{Host: "local", State: fleet.HostOK}}, nil
	}
	m := tui.New(actions, nil, nil).BeginLoading()
	initial := m.Init()().(tea.BatchMsg)
	m = send(m, key("tab"), key("tab"))
	if fleetLoads != 0 || !strings.Contains(m.View(), "waiting for local repositories") {
		t.Fatalf("fleet did not wait for REPOS (loads=%d):\n%s", fleetLoads, m.View())
	}

	localResults <- tui.LocalResult{
		View: tui.ViewRepos, Generation: request.ReposGeneration,
		Repos: []tui.RepoRow{repoRow("shared-api")}, Valid: true,
	}
	next, command := m.Update(initial[1]())
	m = next.(tui.Model)
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("repo acceptance command = %T/%d", batch, len(batch))
	}
	m = send(m, batch[1]())
	if fleetLoads != 1 || len(gotRepos) != 1 || gotRepos[0].Repo.Name != "shared-api" {
		t.Fatalf("fleet loads=%d repos=%+v", fleetLoads, gotRepos)
	}
	if out := m.View(); !strings.Contains(out, "local") {
		t.Fatalf("fleet result missing:\n%s", out)
	}
	close(localResults)
}

func TestFleetWaitsForCurrentReposWhenOlderSnapshotExists(t *testing.T) {
	stream := make(chan tui.LocalResult, 3)
	var request tui.LocalLoadRequest
	var gotRepos []tui.RepoRow
	actions := newActions(&recorder{}, nil)
	actions.Local.Start = func(_ context.Context, got tui.LocalLoadRequest) tui.LocalLoad {
		request = got
		return tui.LocalLoad{ID: 1, Request: got, Results: stream}
	}
	actions.ReloadFleetWithRepos = func(_ context.Context, repos []tui.RepoRow) ([]tui.FleetRow, error) {
		gotRepos = append([]tui.RepoRow(nil), repos...)
		return []tui.FleetRow{{Host: "current", State: fleet.HostOK}}, nil
	}
	m := tui.New(actions, nil, []tui.RepoRow{repoRow("old")})
	next, configCommand := m.Update(key("r"))
	m = next.(tui.Model)
	next, localCommand := m.Update(configCommand())
	m = next.(tui.Model)
	m = send(m, key("tab"), key("tab"))
	if len(gotRepos) != 0 || !strings.Contains(m.View(), "waiting for local repositories") {
		t.Fatalf("fleet used old REPOS while refresh was active:\n%s", m.View())
	}

	stream <- tui.LocalResult{
		View: tui.ViewRepos, Generation: request.ReposGeneration,
		Repos: []tui.RepoRow{repoRow("new")}, Valid: true,
	}
	next, fleetBatchCommand := m.Update(localCommand())
	m = next.(tui.Model)
	fleetBatch := fleetBatchCommand().(tea.BatchMsg)
	m = send(m, fleetBatch[1]())
	if len(gotRepos) != 1 || gotRepos[0].Repo.Name != "new" {
		t.Fatalf("fleet repos = %+v", gotRepos)
	}
	close(stream)
}

func TestRejectedStaleReposCannotFailCurrentFleetDependency(t *testing.T) {
	var requests []tui.LocalLoadRequest
	var streams []chan tui.LocalResult
	fleetLoads := 0
	actions := newActions(&recorder{}, nil)
	actions.Local.Start = func(_ context.Context, request tui.LocalLoadRequest) tui.LocalLoad {
		stream := make(chan tui.LocalResult, 3)
		requests = append(requests, request)
		streams = append(streams, stream)
		return tui.LocalLoad{ID: uint64(len(streams)), Request: request, Results: stream}
	}
	actions.ReloadFleetWithRepos = func(_ context.Context, repos []tui.RepoRow) ([]tui.FleetRow, error) {
		fleetLoads++
		return []tui.FleetRow{{Host: "current", State: fleet.HostOK}}, nil
	}
	m := tui.New(actions, nil, nil).BeginLoading()
	initial := m.Init()().(tea.BatchMsg)

	next, configCommand := m.Update(key("r"))
	m = next.(tui.Model)
	next, currentLocalCommand := m.Update(configCommand())
	m = next.(tui.Model)
	if len(requests) != 2 {
		t.Fatalf("local requests = %d, want 2", len(requests))
	}
	m = send(m, key("tab"), key("tab"))

	streams[0] <- tui.LocalResult{
		View: tui.ViewRepos, Generation: requests[0].ReposGeneration,
		Repos: []tui.RepoRow{repoRow("stale")}, Valid: true,
	}
	next, _ = m.Update(initial[1]())
	m = next.(tui.Model)
	if fleetLoads != 0 || !strings.Contains(m.View(), "waiting for local repositories") {
		t.Fatalf("stale REPOS changed current FLEET (loads=%d):\n%s", fleetLoads, m.View())
	}

	streams[1] <- tui.LocalResult{
		View: tui.ViewRepos, Generation: requests[1].ReposGeneration,
		Repos: []tui.RepoRow{repoRow("current")}, Valid: true,
	}
	next, fleetBatchCommand := m.Update(currentLocalCommand())
	m = next.(tui.Model)
	fleetBatch := fleetBatchCommand().(tea.BatchMsg)
	m = send(m, fleetBatch[1]())
	if fleetLoads != 1 || !strings.Contains(m.View(), "current") {
		t.Fatalf("current REPOS did not start FLEET (loads=%d):\n%s", fleetLoads, m.View())
	}
	close(streams[0])
	close(streams[1])
}

func TestFleetOlderGenerationCannotReplaceNewerRefresh(t *testing.T) {
	var calls atomic.Int32
	started := make(chan int, 2)
	responses := [2]chan []tui.FleetRow{make(chan []tui.FleetRow, 1), make(chan []tui.FleetRow, 1)}
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		index := int(calls.Add(1)) - 1
		started <- index
		return <-responses[index], nil
	}
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"))
	next, firstCmd := m.Update(key("tab"))
	m = next.(tui.Model)
	if firstCmd == nil {
		t.Fatal("first fleet command missing")
	}
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstCmd() }()
	if index := <-started; index != 0 {
		t.Fatalf("first load index = %d", index)
	}

	next, secondCmd := m.Update(key("r"))
	m = next.(tui.Model)
	if secondCmd == nil {
		t.Fatal("second fleet command missing")
	}
	secondResult := make(chan tea.Msg, 1)
	go func() { secondResult <- secondCmd() }()
	if index := <-started; index != 1 {
		t.Fatalf("second load index = %d", index)
	}

	responses[1] <- []tui.FleetRow{{Host: "new-host", State: fleet.HostOK}}
	m = send(m, <-secondResult)
	responses[0] <- []tui.FleetRow{{Host: "old-host", State: fleet.HostOK}}
	m = send(m, <-firstResult)
	if out := m.View(); !strings.Contains(out, "new-host") || strings.Contains(out, "old-host") {
		t.Fatalf("older fleet result replaced the current generation:\n%s", out)
	}
}

func TestFleetSupersedingRefreshCancelsOlderRead(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(ctx context.Context) ([]tui.FleetRow, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}
		return []tui.FleetRow{{Host: "current", State: fleet.HostOK}}, nil
	}
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"))
	next, firstCmd := m.Update(key("tab"))
	m = next.(tui.Model)
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstCmd() }()
	<-started

	next, secondCmd := m.Update(key("r"))
	m = next.(tui.Model)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("superseded fleet read was not canceled")
	}
	m = send(m, secondCmd())
	m = send(m, <-firstResult)
	if out := m.View(); !strings.Contains(out, "current") {
		t.Fatalf("current result missing after cancellation:\n%s", out)
	}
}

func TestViewReadinessTransitionsAreTraced(t *testing.T) {
	trace := perftrace.New(32)
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		return []tui.FleetRow{{Host: "lab", State: fleet.HostOK}}, nil
	}
	m := tui.New(actions, nil, nil).WithTrace(trace)
	m = send(m, key("tab"), key("tab"))
	events := trace.Freeze().Events
	var requested, accepted, finished bool
	for _, event := range events {
		if event.View != perftrace.ViewFleet || event.Generation != 1 {
			continue
		}
		switch event.Name {
		case perftrace.TUIViewLoadRequested:
			requested = true
		case perftrace.TUIViewSnapshotAccepted:
			accepted = event.Rows != nil && *event.Rows == 1
		case perftrace.TUIViewLoadFinished:
			finished = event.Outcome == perftrace.OutcomeSuccess
		}
	}
	if !requested || !accepted || !finished {
		t.Fatalf("readiness trace requested=%v accepted=%v finished=%v events=%+v", requested, accepted, finished, events)
	}
}

func TestFleetFailedRefreshRetainsUsableRowsAndScopesError(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		return nil, errors.New("fleet unavailable")
	}
	cached := []tui.FleetRow{{Host: "cached-host", State: fleet.HostStale, FromCache: true}}
	m := tui.New(actions, nil, nil).WithFleet(cached)
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "cached-host") || !strings.Contains(out, "fleet unavailable") {
		t.Fatalf("failed refresh did not retain and label cached rows:\n%s", out)
	}
	m = send(m, key("tab"))
	if out := m.View(); strings.Contains(out, "fleet unavailable") {
		t.Fatalf("fleet error leaked into another view:\n%s", out)
	}
}

func TestFleetSuccessfulEmptyRefreshClearsOlderRows(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) { return nil, nil }
	cached := []tui.FleetRow{{Host: "cached-host", State: fleet.HostStale, FromCache: true}}
	m := tui.New(actions, nil, nil).WithFleet(cached)
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); strings.Contains(out, "cached-host") || !strings.Contains(out, "No fleet row") {
		t.Fatalf("successful empty refresh did not clear old rows:\n%s", out)
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
	m = send(m, key("tab"), key("tab"), key("tab"))
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("a"))
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("n"))
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
	m = send(m, key("tab"), key("m"))
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("enter"))
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
	m = send(m, key("tab"), key("tab"), key("tab"), key(" "), key("down"), key("down"), key("enter"))
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
	if out := m.View(); !strings.Contains(out, "KEYBOARD HELP") || !strings.Contains(out, "TRY") || !strings.Contains(out, "SKILLS") {
		t.Fatalf("help overlay missing view bindings:\n%s", out)
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
	m = send(m, key("tab"), key("tab"), key("tab"))
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
	m = send(m, key("tab"), key("tab"))
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("/"))
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
		m = send(m, tea.WindowSizeMsg{Width: width, Height: 24}, key("tab"), key("tab"), key("tab"))
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
	actions.ReloadRemoteWithRepos = func(context.Context, []tui.RepoRow) ([]tui.RemoteRow, error) {
		return []tui.RemoteRow{remote}, nil
	}
	m := tui.New(actions, nil, repos).WithRemotes([]tui.RemoteRow{remote})
	m = send(m, key("tab"))
	if out := m.View(); strings.Contains(out, "scratch") || !strings.Contains(out, "api") {
		t.Fatalf("REPOS should hide Try assets while retaining ordinary repos:\n%s", out)
	}
	if summary := m.Summary(); !strings.Contains(summary, "1 repos") || strings.Contains(summary, "2 repos") {
		t.Errorf("summary counted hidden Try: %q", summary)
	}

	m = send(m, key("tab"), key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "try") {
		t.Fatalf("REMOTE should retain the Try local kind:\n%s", out)
	}

	// Once the Try disappears from the fresh local snapshot, an ordinary local
	// reload must clear the cached marker rather than preserving stale state.
	repos = []tui.RepoRow{ordinary}
	m = send(m, key("tab"), key("tab"), key("tab"), key("r"), key("tab"), key("tab"), key("tab"))
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
	remote := remoteRow(forge.GitHub, "owner/shared", "")
	actions := newActions(&recorder{}, nil)
	actions.ReloadRepos = func(context.Context) ([]tui.RepoRow, error) { return repos, nil }
	actions.ReloadRemoteWithRepos = func(context.Context, []tui.RepoRow) ([]tui.RemoteRow, error) {
		return []tui.RemoteRow{remote}, nil
	}
	m := tui.New(actions, nil, repos).WithRemotes([]tui.RemoteRow{remote})
	m = send(m, key("r"), key("tab"), key("tab"), key("tab"), key("tab"))
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

func TestViewNeverRunsToolProbe(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, "")}
	var calls atomic.Int32
	actions := newActions(&recorder{}, rows)
	actions.Tools = []tui.Tool{{
		Key: "L", Name: "lazygit", Command: []string{"lazygit"},
		Probe: func(context.Context) bool { calls.Add(1); return true },
	}}
	m := tui.New(actions, rows, nil)
	if out := m.View(); calls.Load() != 0 || strings.Contains(out, "L lazygit") {
		t.Fatalf("View invoked or advertised an unresolved tool (calls=%d):\n%s", calls.Load(), out)
	}
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init did not batch the background tool probe")
	}
	for _, command := range batch {
		if msg, ready := runQuickly(command); ready && msg != nil {
			m = send(m, msg)
		}
	}
	if out := m.View(); calls.Load() != 1 || !strings.Contains(out, "L lazygit") {
		t.Fatalf("background probe was not applied once (calls=%d):\n%s", calls.Load(), out)
	}
}

func TestToolsOnlyListedWhenAvailable(t *testing.T) {
	rows := []inventory.Row{row("a", "one", task.Hot, "")}
	actions := newActions(&recorder{}, rows)
	actions.Tools = []tui.Tool{
		{Key: "L", Name: "lazygit", Command: []string{"lazygit"}, Availability: tui.ToolAvailable},
		{Key: "Z", Name: "absent", Command: []string{"absent"}, Probe: func(context.Context) bool { return false }, Availability: tui.ToolUnavailable},
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
	m = send(m, key("tab")) // Fleet
	if loads != 0 {
		t.Fatal("the Fleet view must not touch the forge")
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

func TestRemoteRefreshBannerDoesNotScrollTabsOffTerminal(t *testing.T) {
	var rows []tui.RemoteRow
	for index := 0; index < 40; index++ {
		rows = append(rows, remoteRow(forge.GitHub, fmt.Sprintf("owner/repo-%02d", index), ""))
	}
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return rows, nil }
	m := tui.New(actions, nil, nil).WithRemotes(rows).WithRemotesStale(true)
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 24}, key("tab"), key("tab"), key("tab"))

	updated, cmd := m.Update(key("tab"))
	if cmd == nil {
		t.Fatal("opening a stale REMOTE cache did not start a refresh")
	}
	m = updated.(tui.Model)
	output := m.View()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > 24 || !strings.Contains(lines[0], "TASKS") || !strings.Contains(lines[0], "REMOTE") {
		t.Fatalf("REMOTE refresh lost top tabs or overflowed height (%d lines):\n%s", len(lines), output)
	}
	if !strings.Contains(output, "Showing cached repositories while refreshing") {
		t.Fatalf("REMOTE refresh lost its cached-row banner:\n%s", output)
	}
}

func TestTaskWithMissingWorktreeRequiresSweepBeforeOpen(t *testing.T) {
	rec := &recorder{}
	missing := row("missing", "missing worktree", task.Warm, "recover it")
	missing.Task.Mode = task.ModeWorktree
	missing.Task.WorktreePath = "/src/orphan-shell"
	missing.Checkout = missing.Task.WorktreePath
	missing.CheckoutExists = true
	missing.WorktreeMissing = true
	m := tui.New(newActions(rec, []inventory.Row{missing}), []inventory.Row{missing}, nil)

	if out := m.View(); !strings.Contains(out, "run `dev sweep`") ||
		!strings.Contains(out, "salvage anything it reports") || strings.Contains(out, "enter open") {
		t.Fatalf("missing-worktree recovery guidance is incomplete:\n%s", out)
	}
	m = send(m, key("enter"))
	if len(rec.opened) != 0 {
		t.Fatalf("missing worktree opened its orphan shell: %v", rec.opened)
	}
	if out := m.View(); !strings.Contains(out, "missing or unregistered worktree") {
		t.Fatalf("missing-worktree open did not explain the blocker:\n%s", out)
	}
}

func TestColdWorktreeTaskRequiresExplicitResume(t *testing.T) {
	rec := &recorder{}
	cold := row("cold-task", "cold task", task.Cold, "continue")
	cold.Task.Mode = task.ModeWorktree
	cold.Task.WorktreePath = ""
	cold.Checkout = cold.Task.RepoPath
	cold.CheckoutExists = true
	m := tui.New(newActions(rec, []inventory.Row{cold}), []inventory.Row{cold}, nil)

	if out := m.View(); !strings.Contains(out, "dev resume "+cold.Task.ID) || strings.Contains(out, "enter open") {
		t.Fatalf("cold task did not require explicit resume:\n%s", out)
	}
	m = send(m, key("enter"))
	if len(rec.opened) != 0 {
		t.Fatalf("cold task opened the canonical checkout: %v", rec.opened)
	}
	if out := m.View(); !strings.Contains(out, "no managed worktree") {
		t.Fatalf("cold-task open did not explain the blocker:\n%s", out)
	}
}

func TestRemoteFilterUsesNameDescriptionAndProvider(t *testing.T) {
	rows := []tui.RemoteRow{
		remoteRow(forge.GitHub, "owner/api", ""),
		remoteRow(forge.GitLab, "group/web", ""),
	}
	rows[1].Repo.Visibility = "public"
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return rows, nil }
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"), key("/"))
	for _, k := range typeText("gitlab web") {
		m = send(m, k)
	}
	out := m.View()
	if !strings.Contains(out, "group/web") || strings.Contains(out, "owner/api") {
		t.Errorf("remote search should match provider + name terms:\n%s", out)
	}
	m = send(m, key("esc"), key("/"))
	for _, k := range typeText("vis:private") {
		m = send(m, k)
	}
	out = m.View()
	if !strings.Contains(out, "owner/api") || strings.Contains(out, "group/web") {
		t.Errorf("structured visibility filter failed:\n%s", out)
	}
}

func TestRemoteCloneRequiresConfirmation(t *testing.T) {
	rows := []tui.RemoteRow{remoteRow(forge.GitHub, "owner/api", "")}
	rec := &recorder{}
	actions := newActions(rec, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return rows, nil }
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))

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
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"), key("enter"))

	if len(rec.opened) != 1 || rec.opened[0] != "remote:owner/api" {
		t.Errorf("enter should open the existing local clone, got %v", rec.opened)
	}
}

func TestRemoteViewLabelsLocalTryKind(t *testing.T) {
	row := remoteRow(forge.GitHub, "owner/experiment", "/src/tries/experiment")
	row.LocalKind = catalog.KindTry
	m := tui.New(newActions(&recorder{}, nil), nil, nil).WithRemotes([]tui.RemoteRow{row})
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))
	out := m.View()
	if !strings.Contains(out, "try") || !strings.Contains(out, "asset") {
		t.Errorf("REMOTE view did not identify the local Try:\n%s", out)
	}
}

func TestLateStartupRemoteCacheCannotOverwriteVisitedView(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	actions := newActions(&recorder{}, nil)
	actions.LoadRemoteCache = func(context.Context) tui.RemoteCacheResult {
		close(started)
		<-release
		return tui.RemoteCacheResult{
			Rows: []tui.RemoteRow{remoteRow(forge.GitHub, "owner/cached", "")}, Found: true,
		}
	}
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) {
		return []tui.RemoteRow{remoteRow(forge.GitHub, "owner/live", "")}, nil
	}
	m := tui.New(actions, nil, nil)
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init commands = %T/%d, want blink + cache", batch, len(batch))
	}
	cacheResult := make(chan tea.Msg, 1)
	go func() { cacheResult <- batch[1]() }()
	<-started
	if out := m.View(); !strings.Contains(out, "TASKS") {
		t.Fatalf("blocked cache prevented initial View:\n%s", out)
	}

	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))
	close(release)
	m = send(m, <-cacheResult)
	if out := m.View(); !strings.Contains(out, "owner/live") || strings.Contains(out, "owner/cached") {
		t.Fatalf("late generation-zero cache replaced live rows:\n%s", out)
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))
	m = send(m, key("r"))
	if loads != 2 {
		t.Errorf("initial visit + refresh should load twice, got %d", loads)
	}
}

func TestOffscreenConfigReloadInvalidatesFreshRemoteSnapshot(t *testing.T) {
	loads := 0
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) {
		loads++
		return []tui.RemoteRow{remoteRow(forge.GitHub, "owner/current", "")}, nil
	}
	cached := []tui.RemoteRow{remoteRow(forge.GitHub, "owner/cached", "")}
	m := tui.New(actions, nil, nil).WithRemotes(cached)
	m = send(m, key("r"))
	if loads != 0 {
		t.Fatalf("off-screen config reload queried REMOTE eagerly: %d", loads)
	}
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))
	if loads != 1 || !strings.Contains(m.View(), "owner/current") {
		t.Fatalf("invalidated REMOTE did not refresh on visit (loads=%d):\n%s", loads, m.View())
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
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"))

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
	m = send(m, key("tab"), key("tab"), key("tab"))
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
	actions.ReloadRepos = func(context.Context) ([]tui.RepoRow, error) {
		return []tui.RepoRow{repoRow("api")}, nil
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

func TestSharedLocalLoadPublishesEachViewWithoutWaitingForOthers(t *testing.T) {
	results := make(chan tui.LocalResult, 3)
	var request tui.LocalLoadRequest
	var loadContext context.Context
	actions := newActions(&recorder{}, nil)
	actions.Local.Start = func(ctx context.Context, got tui.LocalLoadRequest) tui.LocalLoad {
		loadContext, request = ctx, got
		return tui.LocalLoad{ID: 7, Request: got, Results: results}
	}
	m := tui.New(actions, nil, nil).BeginLoading()
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init commands = %T/%d, want blink + local stream", batch, len(batch))
	}
	results <- tui.LocalResult{
		View: tui.ViewTasks, Generation: request.TasksGeneration,
		Tasks: []inventory.Row{row("task-a", "ready first", task.Hot, "")}, Valid: true,
	}
	message := batch[1]()
	next, waitRepos := m.Update(message)
	m = next.(tui.Model)
	if out := m.View(); !strings.Contains(out, "ready first") {
		t.Fatalf("TASKS did not publish independently:\n%s", out)
	}
	if err := loadContext.Err(); err != nil {
		t.Fatalf("TASKS completion canceled the shared local cycle: %v", err)
	}

	results <- tui.LocalResult{
		View: tui.ViewRepos, Generation: request.ReposGeneration,
		Repos: []tui.RepoRow{repoRow("repo-second")}, Valid: true,
	}
	next, waitTries := m.Update(waitRepos())
	m = next.(tui.Model)
	m = send(m, key("tab"))
	if out := m.View(); !strings.Contains(out, "repo-second") {
		t.Fatalf("REPOS did not publish independently:\n%s", out)
	}

	results <- tui.LocalResult{
		View: tui.ViewTries, Generation: request.TriesGeneration,
		Tries: []tui.TryRow{tryRow("try-third", "try-third", catalog.PhaseActive, catalog.LocationPresent)}, Valid: true,
	}
	close(results)
	next, waitDone := m.Update(waitTries())
	m = next.(tui.Model)
	next, _ = m.Update(waitDone())
	m = next.(tui.Model)
	m = send(m, key("tab"), key("tab"))
	if out := m.View(); !strings.Contains(out, "try-third") {
		t.Fatalf("TRY did not publish independently:\n%s", out)
	}
}

func TestAfterFirstViewWorkWaitsForInitialView(t *testing.T) {
	started := make(chan struct{})
	actions := newActions(&recorder{}, nil)
	actions.AfterFirstView = func(context.Context) { close(started) }
	m := tui.New(actions, nil, nil)
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init commands = %T/%d, want blink + post-view work", batch, len(batch))
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[1]() }()
	select {
	case <-started:
		t.Fatal("post-view work started before View returned")
	default:
	}
	_ = m.View()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("post-view work did not start after View returned")
	}
	<-done
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
			Availability: tui.ToolAvailable,
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

func sampleNote(id, body string, age time.Duration, tags ...string) *note.Note {
	return &note.Note{
		SchemaVersion: note.CurrentSchemaVersion,
		ID:            id, RepositoryID: "11111111-1111-4111-8111-111111111111",
		Repository: "demo", Body: body + "\n", Tags: tags,
		Created: time.Now().Add(-age), Updated: time.Now().Add(-age),
	}
}

func TestNQuickAddsNoteFromTask(t *testing.T) {
	rows := []inventory.Row{row("a", "task", task.Hot, "")}
	rec := &recorder{}
	m := tui.New(newActions(rec, rows), rows, nil)
	m = send(m, key("n"))
	if !strings.Contains(m.View(), "quick thought") {
		t.Fatalf("n should open quick note prompt:\n%s", m.View())
	}
	for _, k := range typeText("try event subscription") {
		m = send(m, k)
	}
	m = send(m, key("enter"))
	if len(rec.notes) != 1 || strings.TrimSpace(rec.notes[0].Body) != "try event subscription" {
		t.Errorf("quick note not added: %+v", rec.notes)
	}
	if strings.Contains(m.View(), "quick thought") {
		t.Error("quick add should return to the main list")
	}
}

func TestNBrowsesSearchesAndExpandsNotes(t *testing.T) {
	rows := []inventory.Row{row("a", "task", task.Hot, "")}
	rec := &recorder{notes: []*note.Note{
		sampleNote("22222222-2222-4222-8222-222222222222", "event subscription", time.Minute, "idea"),
		sampleNote("33333333-3333-4333-8333-333333333333", "settings redesign", 24*time.Hour, "ui"),
	}}
	m := tui.New(newActions(rec, rows), rows, nil)
	m = send(m, key("N"))
	out := m.View()
	if !strings.Contains(out, "NOTES") || !strings.Contains(out, "event subscription") || !strings.Contains(out, "settings redesign") {
		t.Fatalf("notes overlay:\n%s", out)
	}
	m = send(m, key("enter"))
	if !strings.Contains(m.View(), "22222222-2222") {
		t.Errorf("enter should expand full note metadata/body:\n%s", m.View())
	}
	m = send(m, key("/"))
	for _, k := range typeText("settings") {
		m = send(m, k)
	}
	m = send(m, key("enter"))
	if len(rec.searches) != 1 || rec.searches[0] != "settings" {
		t.Errorf("searches = %v", rec.searches)
	}
	if strings.Contains(m.View(), "event subscription") || !strings.Contains(m.View(), "settings redesign") {
		t.Errorf("search result scope:\n%s", m.View())
	}
}

func TestNotesDeleteRequiresVisibleConfirmation(t *testing.T) {
	rows := []inventory.Row{row("a", "task", task.Hot, "")}
	id := "22222222-2222-4222-8222-222222222222"
	rec := &recorder{notes: []*note.Note{sampleNote(id, "delete me", time.Minute)}}
	m := tui.New(newActions(rec, rows), rows, nil)
	m = send(m, key("N"), key("d"))
	if !strings.Contains(m.View(), "Delete note 22222222?") {
		t.Fatalf("confirmation missing:\n%s", m.View())
	}
	m = send(m, key("n"))
	if len(rec.deleted) != 0 || !strings.Contains(m.View(), "delete me") {
		t.Error("n should cancel without deleting")
	}
	m = send(m, key("d"), key("y"))
	if len(rec.deleted) != 1 || rec.deleted[0] != id {
		t.Errorf("confirmed delete = %v", rec.deleted)
	}
	if !strings.Contains(m.View(), "No notes") {
		t.Errorf("list should refresh after delete:\n%s", m.View())
	}
}

func TestNotesEditHandsTerminalToEditor(t *testing.T) {
	rows := []inventory.Row{row("a", "task", task.Hot, "")}
	id := "22222222-2222-4222-8222-222222222222"
	rec := &recorder{notes: []*note.Note{sampleNote(id, "edit me", time.Minute)}}
	m := tui.New(newActions(rec, rows), rows, nil)
	m = send(m, key("N"))
	_, cmd := m.Update(key("e"))
	if cmd == nil || len(rec.edits) != 1 || rec.edits[0] != id {
		t.Errorf("editor boundary: cmd=%v edits=%v", cmd, rec.edits)
	}
}

func TestUnclonedRemoteHasNoNoteTarget(t *testing.T) {
	remote := remoteRow(forge.GitHub, "owner/api", "")
	actions := newActions(&recorder{}, nil)
	actions.ReloadRemote = func(context.Context) ([]tui.RemoteRow, error) { return []tui.RemoteRow{remote}, nil }
	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("tab"), key("n"))
	if strings.Contains(m.View(), "quick thought") {
		t.Error("uncloned remote must not accept a local sidecar note")
	}
}

func TestRepoNotesColumnAndDetailPreview(t *testing.T) {
	n := sampleNote("22222222-2222-4222-8222-222222222222", "latest thought", time.Minute)
	r := repoRow("api")
	r.NoteCount, r.LatestNote = 3, n
	actions := newActions(&recorder{}, nil)
	actions.RepoColumns = []string{"repo", "notes"}
	m := tui.New(actions, nil, []tui.RepoRow{r})
	m = send(m, key("tab"))
	out := m.View()
	if !strings.Contains(out, "NOTES") || !strings.Contains(out, "3") || !strings.Contains(out, "latest thought") {
		t.Errorf("note count/preview missing:\n%s", out)
	}
}

func TestTryNStillCreatesTryRatherThanNote(t *testing.T) {
	m := tui.New(newActions(&recorder{}, nil), nil, nil).WithTries(nil)
	m = send(m, key("tab"), key("tab"), key("tab"), key("n"))
	if !strings.Contains(m.View(), "NEW TRY") {
		t.Errorf("n in TRY view must retain its lifecycle meaning:\n%s", m.View())
	}
}

func TestExpandedNoteStaysWithinTerminalHeight(t *testing.T) {
	rows := []inventory.Row{row("a", "task", task.Hot, "")}
	body := strings.Repeat("a long note line that wraps across the viewport\n", 50)
	rec := &recorder{notes: []*note.Note{
		sampleNote("22222222-2222-4222-8222-222222222222", body, time.Minute),
	}}
	m := tui.New(newActions(rec, rows), rows, nil)
	m = send(m, tea.WindowSizeMsg{Width: 72, Height: 24}, key("N"), key("enter"))
	out := m.View()
	lines := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1
	if lines > 24 {
		t.Errorf("expanded note has %d lines in a 24-line terminal:\n%s", lines, out)
	}
	if !strings.Contains(out, "…") {
		t.Error("truncated expanded note should show an ellipsis")
	}
}

func TestStaleNoteLoadCannotPopulateNewRepositoryOverlay(t *testing.T) {
	rows := []inventory.Row{
		{Task: &task.Task{ID: "a", Name: "A task", Repo: "repo-a", RepoPath: "/src/a", Branch: "main", State: task.Hot}, CheckoutExists: true},
		{Task: &task.Task{ID: "b", Name: "B task", Repo: "repo-b", RepoPath: "/src/b", Branch: "main", State: task.Hot}, CheckoutExists: true},
	}
	aNote := sampleNote("22222222-2222-4222-8222-222222222222", "A private thought", time.Minute)
	bNote := sampleNote("33333333-3333-4333-8333-333333333333", "B selected thought", time.Minute)
	actions := newActions(&recorder{}, rows)
	actions.Notes.List = func(_ context.Context, target tui.NoteTarget) ([]*note.Note, error) {
		if target.Name() == "repo-a" {
			return []*note.Note{aNote}, nil
		}
		return []*note.Note{bNote}, nil
	}
	m := tui.New(actions, rows, nil)

	// Start A's async load but do not deliver its result yet.
	next, cmdA := m.Update(key("N"))
	m = next.(tui.Model)
	if cmdA == nil {
		t.Fatal("A load command missing")
	}
	// Leave A, select B, and start B's load.
	m = send(m, key("N"), key("down"))
	next, cmdB := m.Update(key("N"))
	m = next.(tui.Model)
	if cmdB == nil {
		t.Fatal("B load command missing")
	}

	// B arrives first and is accepted; delayed A arrives second and must be ignored.
	m = send(m, cmdB())
	m = send(m, cmdA())
	out := m.View()
	if !strings.Contains(out, "B selected thought") || strings.Contains(out, "A private thought") {
		t.Errorf("stale A result contaminated B overlay:\n%s", out)
	}
}

func TestFleetViewEditsRemotesNotTheMainConfig(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	var editedConfig, editedRemotes, validated, reloaded bool
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		reloaded = true
		return []tui.FleetRow{{Host: "lab", State: fleet.HostOK}}, nil
	}
	actions.EditConfig = func() (*exec.Cmd, error) {
		editedConfig = true
		return exec.Command("true"), nil
	}
	actions.EditFleetConfig = func() (*exec.Cmd, error) {
		editedRemotes = true
		return exec.Command("true"), nil
	}
	actions.ValidateFleetConfig = func() error {
		validated = true
		return nil
	}

	// `e` on the task list still opens dev's own config.
	m := tui.New(actions, nil, nil)
	_ = send(m, key("e"))
	if !editedConfig || editedRemotes {
		t.Fatalf("e outside FLEET edited the wrong file (config=%v remotes=%v)", editedConfig, editedRemotes)
	}

	// In FLEET it opens the file that view is actually about.
	editedConfig = false
	m = send(m, key("tab"), key("tab"))
	if m.CurrentView() != tui.ViewFleet {
		t.Fatalf("expected the fleet view, got %v", m.CurrentView())
	}
	m = send(m, key("e"))
	if !editedRemotes || editedConfig {
		t.Fatalf("e in FLEET edited the wrong file (config=%v remotes=%v)", editedConfig, editedRemotes)
	}

	reloaded = false
	m = send(m, tui.FleetConfigEditedForTest(nil))
	if !validated {
		t.Error("remotes.toml was applied without being reparsed")
	}
	if !reloaded {
		t.Error("the fleet was not refreshed after its configuration changed")
	}
	if out := m.View(); !strings.Contains(out, "e hosts") {
		t.Errorf("the FLEET footer still advertises the wrong file:\n%s", out)
	}
}

func TestFleetKeepsShowingWhatItHadWhenRemotesIsInvalid(t *testing.T) {
	actions := newActions(&recorder{}, nil)
	reloads := 0
	actions.ReloadFleet = func(context.Context) ([]tui.FleetRow, error) {
		reloads++
		return []tui.FleetRow{{Host: "lab", State: fleet.HostOK}}, nil
	}
	actions.EditFleetConfig = func() (*exec.Cmd, error) { return exec.Command("true"), nil }
	actions.ValidateFleetConfig = func() error {
		return errors.New("unknown field(s) in remotes.toml: hosts.alais")
	}

	m := tui.New(actions, nil, nil)
	m = send(m, key("tab"), key("tab"))
	before := reloads

	m = send(m, tui.FleetConfigEditedForTest(nil))
	if reloads != before {
		t.Error("dev fanned out to hosts using a file it had just rejected")
	}
	// A rejected file is still on disk; the reason has to reach the user.
	if out := m.View(); !strings.Contains(out, "unknown field") {
		t.Errorf("the parse error was swallowed:\n%s", out)
	}
}
