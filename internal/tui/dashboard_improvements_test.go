package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func overlayKey(m Model, key tea.KeyMsg) Model { next, _ := m.updateOverlay(key); return next.(Model) }
func TestActionMenuFilterUsesVisibleIndicesAndFits(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.width = 70
	m.height = 13
	m.overlay = overlayState{kind: overlayActionMenu, subject: "repo"}
	for i := 0; i < 20; i++ {
		m.overlay.addOption(listActionOpen, "open repository")
	}
	m.overlay.addOption(listActionStats, "open activity heatmap")
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/STATS")})
	visible := m.visibleActions()
	if len(visible) != 1 || visible[0] != 20 {
		t.Fatal(visible)
	}
	index, ok := m.actionMenuOptionAt(3, m.buildActionMenuLayout().firstOptionY)
	if !ok || index != 20 {
		t.Fatalf("mouse index=%d ok=%v", index, ok)
	}
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !m.overlay.searching || len(m.visibleActions()) != 0 {
		t.Fatal("q should be searchable text")
	}
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay.kind != overlayActionMenu {
		t.Fatal("empty result executed")
	}
	m = overlayKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay.kind != overlayActionMenu || m.overlay.searching {
		t.Fatal("first escape should clear search")
	}
	m.moveActionMenu(1)
	if lineCount(m.renderOverlay()) > m.height {
		t.Fatal(m.renderOverlay())
	}
}

func TestRepoProgressRejectsLateCacheAndKeepsFocus(t *testing.T) {
	m := New(Actions{}, nil, nil)
	m.view = ViewRepos
	m.width = 100
	m.height = 30
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	a := RepoRow{Repo: repo.Repo{Name: "a", Path: "/a"}, Pending: "cached"}
	b := RepoRow{Repo: repo.Repo{Name: "b", Path: "/b"}, Pending: "loading"}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{a}, Valid: true})
	token := m.currentToken()
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "discovery", Repos: []RepoRow{b}, Valid: true})
	if token != m.currentToken() {
		t.Fatal("partial result moved focus")
	}
	var accepted bool
	m, accepted = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{{}}, Valid: true})
	if accepted {
		t.Fatal("late cache accepted")
	}
	a.Pending = ""
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Repos: []RepoRow{a}, Valid: true})
	if len(m.repos) != 1 || m.repos[0].Pending != "" {
		t.Fatal(m.repos)
	}
	m, accepted = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "enrichment", Repos: []RepoRow{b}, Valid: true})
	if accepted {
		t.Fatal("post-completion patch accepted")
	}
}

// Loaded rows and fail-fast callbacks keep filter tests independent of native
// tools. Arrow navigation must not return a command or trigger a source read.
func dashboardFilterModel(t *testing.T, view View) Model {
	t.Helper()
	m := New(Actions{
		Workflow: func(context.Context, WorkflowRequest) (Workflow, error) {
			t.Fatal("filter invoked an action")
			return nil, nil
		},
		ReloadRemote: func(context.Context) ([]RemoteRow, error) {
			t.Fatal("filter queried a forge")
			return nil, nil
		},
		LoadFleetHosts: func(context.Context) (FleetHostsResult, error) {
			t.Fatal("filter reloaded fleet inventory")
			return FleetHostsResult{}, nil
		},
		LoadFleetHost: func(context.Context, FleetHostDescriptor) (fleet.HostResult, error) {
			t.Fatal("filter contacted a fleet host")
			return fleet.HostResult{}, nil
		},
		CheckSkills: func(context.Context, []agentskill.Skill) []agentskill.Skill {
			t.Fatal("filter checked upstream skills")
			return nil
		},
		SSH: SSHActions{
			Load: func(context.Context) (SSHInventory, error) {
				t.Fatal("filter reloaded SSH inventory")
				return SSHInventory{}, nil
			},
			Workflow: func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error) {
				t.Fatal("filter invoked an SSH workflow")
				return nil, nil
			},
		},
	}, nil, nil)
	for _, name := range []string{"match-a", "match-b", "match-c", "other"} {
		path := "/repos/" + name
		m.rows = append(m.rows, inventory.Row{Task: &task.Task{ID: name, Name: name, State: task.Hot}, CheckoutExists: true})
		m.repos = append(m.repos, RepoRow{Repo: repo.Repo{Name: name, Path: path, CommonDir: path + "/.git"}})
		m.fleetTree.hosts = append(m.fleetTree.hosts, fleetHostState{descriptor: FleetHostDescriptor{Key: name, Name: name, EndpointID: name}})
		m.tries = append(m.tries, TryRow{Item: experiment.Item{ID: name, Name: name, Live: experiment.LiveFacts{Present: true, CurrentPath: path}}})
		m.remotes = append(m.remotes, RemoteRow{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/" + name, Name: name}})
		m.skills = append(m.skills, agentskill.Skill{Name: name, Path: path, Scope: agentskill.ScopeProject})
		m.mcp = append(m.mcp, agentmcp.Declaration{Name: name, ConfigPath: path + "/.mcp.json", Scope: agentmcp.ScopeProject})
		m.ssh.Machines = append(m.ssh.Machines, SSHRow{ID: name, Label: name})
	}
	for _, other := range Views {
		m.view = other
		m.setAt(1)
	}
	m.view = view
	m.setAt(0)
	m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("match")})
	if m.count() != 3 || m.mode != modeFilter || !m.input.Focused() {
		t.Fatalf("%s filter fixture: count=%d mode=%v", view, m.count(), m.mode)
	}
	return m
}

func TestDashboardFilterNavigationEveryView(t *testing.T) {
	for _, view := range Views {
		t.Run(view.String(), func(t *testing.T) {
			m := dashboardFilterModel(t, view)
			m.input.SetCursor(2)
			for _, move := range []struct {
				key  tea.KeyType
				want int
			}{{tea.KeyUp, 0}, {tea.KeyDown, 1}, {tea.KeyDown, 2}, {tea.KeyDown, 2}, {tea.KeyUp, 1}} {
				next, command := m.Update(tea.KeyMsg{Type: move.key})
				m = next.(Model)
				if command != nil || m.at() != move.want || m.view != view || m.mode != modeFilter || !m.input.Focused() || m.input.Position() != 2 || m.input.Value() != "match" || m.filter != "match" || m.overlay.kind != overlayNone {
					t.Fatalf("key=%v at=%d want=%d mode=%v cursor=%d query=%q command=%v", move.key, m.at(), move.want, m.mode, m.input.Position(), m.filter, command)
				}
				for _, other := range Views {
					if other != view {
						copy := m
						copy.view = other
						if copy.at() != 1 {
							t.Fatalf("filter navigation changed %s cursor to %d", other, copy.at())
						}
					}
				}
			}
			selected := m.currentToken()
			next, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(Model)
			if command != nil || m.currentToken() != selected || m.mode != modeList || m.input.Focused() || m.filter != "match" {
				t.Fatal("Enter opened or lost the selected result instead of leaving input")
			}
			m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
			next, command = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = next.(Model)
			if command != nil || m.mode != modeList || m.input.Focused() || m.filter != "" || m.at() != 0 || m.count() != 4 || m.quitting {
				t.Fatal("Esc did not clear narrowing and select the first row")
			}
		})
	}
}

func TestDashboardFilterTextEditsEveryView(t *testing.T) {
	for _, view := range Views {
		t.Run(view.String(), func(t *testing.T) {
			for _, edit := range []struct {
				name                   string
				key                    tea.KeyMsg
				position, wantPosition int
				want                   string
			}{
				{"left", tea.KeyMsg{Type: tea.KeyLeft}, 3, 2, "match"},
				{"right", tea.KeyMsg{Type: tea.KeyRight}, 2, 3, "match"},
				{"home", tea.KeyMsg{Type: tea.KeyHome}, 3, 0, "match"},
				{"end", tea.KeyMsg{Type: tea.KeyEnd}, 2, 5, "match"},
				{"ctrl-a", tea.KeyMsg{Type: tea.KeyCtrlA}, 3, 0, "match"},
				{"ctrl-e", tea.KeyMsg{Type: tea.KeyCtrlE}, 2, 5, "match"},
				{"empty backspace", tea.KeyMsg{Type: tea.KeyBackspace}, 0, 0, "match"},
				{"empty delete", tea.KeyMsg{Type: tea.KeyDelete}, 5, 5, "match"},
				{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, 3, 2, "mach"},
				{"delete", tea.KeyMsg{Type: tea.KeyDelete}, 2, 2, "mach"},
				{"insert at cursor", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}, 2, 3, "maxtch"},
				{"same matches changed text", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}, 5, 6, "match "},
				{"j is text", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}, 5, 6, "matchj"},
				{"k is text", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}, 5, 6, "matchk"},
				{"view number is text", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("7")}, 5, 6, "match7"},
				{"refresh is text", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}, 5, 6, "matchr"},
				{"new is text", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}, 5, 6, "matchn"},
				{"clear text", tea.KeyMsg{Type: tea.KeyCtrlU}, 5, 0, ""},
			} {
				t.Run(edit.name, func(t *testing.T) {
					m := dashboardFilterModel(t, view)
					m.setAt(2)
					m.input.SetCursor(edit.position)
					m = startupSend(m, edit.key)
					wantAt := 0
					if edit.want == "match" {
						wantAt = 2
					}
					if m.input.Value() != edit.want || m.filter != edit.want || m.at() != wantAt || m.input.Position() != edit.wantPosition || m.mode != modeFilter || !m.input.Focused() || m.view != view || m.overlay.kind != overlayNone {
						t.Fatalf("query=%q input=%q cursor=%d at=%d mode=%v", m.filter, m.input.Value(), m.input.Position(), m.at(), m.mode)
					}
				})
			}
		})
	}
}

func TestDashboardFilterEmptyResultsAndInputLimit(t *testing.T) {
	for _, view := range Views {
		t.Run(view.String(), func(t *testing.T) {
			for _, emptySource := range []bool{false, true} {
				m := dashboardFilterModel(t, view)
				if emptySource {
					m.rows, m.repos, m.fleetTree.hosts, m.tries, m.remotes, m.skills, m.mcp, m.ssh.Machines = nil, nil, nil, nil, nil, nil, nil, nil
				} else {
					m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-absent")})
				}
				query, cursor := m.filter, m.input.Position()
				for _, key := range []tea.KeyType{tea.KeyDown, tea.KeyUp, tea.KeyDown} {
					next, command := m.Update(tea.KeyMsg{Type: key})
					m = next.(Model)
					if command != nil || m.at() != 0 || m.count() != 0 || m.input.Value() != query || m.input.Position() != cursor || m.mode != modeFilter || !m.input.Focused() {
						t.Fatalf("emptySource=%v key=%v changed empty filter state", emptySource, key)
					}
				}
			}
			m := dashboardFilterModel(t, view)
			m.setAt(2)
			m.input.CharLimit = len("match")
			m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
			if m.at() != 2 || m.filter != "match" {
				t.Fatal("rejected text at input limit reset selection")
			}
		})
	}
}

func TestPendingRepoFilterPromptRemainsVisible(t *testing.T) {
	for _, pending := range []string{"cached", "loading", "runtime pending"} {
		t.Run(pending, func(t *testing.T) {
			m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "match", Path: "/match"}, Pending: pending}})
			m.view = ViewRepos
			if !strings.Contains(m.renderDetail(), "waiting for fresh repository observations") {
				t.Fatal("passive pending detail missing")
			}
			m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
			m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("match")})
			for _, output := range []string{m.renderDetail(), m.View()} {
				if !strings.Contains(output, "filter ") || !strings.Contains(output, "↑/↓ select") || !strings.Contains(output, "enter to keep it") || strings.Contains(output, "waiting for fresh repository observations") {
					t.Fatalf("pending detail hid filter prompt:\n%s", output)
				}
			}
			m = startupSend(m, tea.KeyMsg{Type: tea.KeyEnter})
			if !strings.Contains(m.renderDetail(), "waiting for fresh repository observations") {
				t.Fatal("leaving input lost passive pending detail")
			}
		})
	}
}

func TestRepoFilterPreservesChildFocusAcrossProgress(t *testing.T) {
	m := New(Actions{RepoSort: "name"}, nil, nil)
	m.view = ViewRepos
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	b := RepoRow{Repo: repo.Repo{Name: "match-b", Path: "/b", CommonDir: "/b/.git"}, Pending: "cached", Worktrees: 1}
	b.Context.Checkouts = []inventory.RepoCheckout{
		{Worktree: gitx.Worktree{Path: "/b", Main: true}, Exists: true},
		{Worktree: gitx.Worktree{Path: "/worktrees/b/child", Branch: "feature"}, Exists: true},
	}
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: []RepoRow{b}, Valid: true})
	m.toggleRepo(b)
	m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = startupSend(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("match")})
	m = startupSend(m, tea.KeyMsg{Type: tea.KeyDown})
	m.input.SetCursor(2)
	selected := m.currentToken()
	if item, ok := m.currentRepoItem(); !ok || !item.child() {
		t.Fatal("filter arrow did not select expanded worktree")
	}
	a := RepoRow{Repo: repo.Repo{Name: "match-a", Path: "/a"}, Pending: "loading"}
	for _, phase := range []string{"discovery", "enrichment", ""} {
		if phase == "enrichment" || phase == "" {
			b.Pending = ""
		}
		m = startupSend(m, localMsg{result: LocalResult{View: ViewRepos, Generation: generation, Phase: phase, Repos: []RepoRow{a, b}, Valid: true}})
		if m.currentToken() != selected || m.at() != 2 || m.mode != modeFilter || !m.input.Focused() || m.filter != "match" || m.input.Position() != 2 {
			t.Fatalf("phase=%q lost child selection or active input: at=%d token=%+v", phase, m.at(), m.currentToken())
		}
	}
	// Once that checkout disappears, selection must clamp to a surviving row.
	b.Context.Checkouts = b.Context.Checkouts[:1]
	generation = m.beginViewLoad(ViewRepos, loadRefresh)
	m = startupSend(m, reposMsg{generation: generation, valid: true, rows: []RepoRow{a, b}})
	if m.at() >= m.count() || m.mode != modeFilter || m.input.Position() != 2 || m.currentToken() == selected {
		t.Fatal("removed child left invalid focus or reset input")
	}
}

func TestStatsHistoryReadsFirstCancelsAndPreservesPriorPanelOnError(t *testing.T) {
	refreshCalls := 0
	actions := StatsActions{Read: func(context.Context, repo.Repo) (StatsPanel, error) {
		return StatsPanel{Repo: "a", Seconds: 42, Heatmap: "saved"}, nil
	}, Refresh: func(context.Context, repo.Repo, bool) (StatsPanel, error) {
		refreshCalls++
		return StatsPanel{}, errors.New("history unavailable")
	}}
	m := New(Actions{Stats: actions}, nil, nil)
	next, read := m.startStatsHistory(repo.Repo{Name: "a", Path: "/a"}, false, true)
	m = next.(Model)
	next, refresh := m.Update(read())
	m = next.(Model)
	if m.stats == nil || m.stats.Seconds != 42 || refreshCalls != 0 {
		t.Fatal("cached stats not shown before refresh")
	}
	next, _ = m.Update(refresh())
	m = next.(Model)
	if m.stats == nil || m.stats.Seconds != 42 || !strings.Contains(m.err.Error(), "unavailable") {
		t.Fatal("refresh lost cached panel")
	}
	generation := m.statsGeneration
	next, _ = m.updateStats(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	next, _ = m.Update(statsHistoryMsg{generation: generation, panel: StatsPanel{Repo: "late"}})
	m = next.(Model)
	if m.stats != nil {
		t.Fatal("late history result resurrected closed panel")
	}
}
