package tui

import (
	"context"
	"errors"
	"os/exec"
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

func TestEveryDashboardViewBuildsRowActionMenu(t *testing.T) {
	actions := Actions{
		Open:       func(context.Context, *task.Task) (OpenResult, error) { return OpenResult{}, nil },
		OpenRepo:   func(context.Context, RepoRow) (OpenResult, error) { return OpenResult{}, nil },
		OpenFleet:  func(context.Context, FleetRow) (*exec.Cmd, error) { return exec.Command("true"), nil },
		OpenRemote: func(context.Context, RemoteRow) (OpenResult, error) { return OpenResult{}, nil },
		EditFile:   func(string) (CapabilityEdit, error) { return CapabilityEdit{Command: exec.Command("true")}, nil },
		Copy:       func(string) error { return nil },
		ReadFile:   func(context.Context, string) (string, error) { return "", nil },
		Tries: TryActions{
			Apply: func(context.Context, TryRequest) (TryActionResult, error) { return TryActionResult{}, nil },
		},
	}
	model := New(actions, []inventory.Row{{
		Task:           &task.Task{ID: "task-1", Name: "task", Repo: "demo", RepoPath: "/repos/demo"},
		CheckoutExists: true,
	}}, []RepoRow{{Repo: repo.Repo{Name: "demo", Path: "/repos/demo", CommonDir: "/repos/demo/.git"}}})
	model.fleet = []FleetRow{{Host: "builder", Repository: &fleet.RepoSnapshot{Path: "/repos/demo"}}}
	model.ssh = sshTestInventory("builder")
	model.tries = []TryRow{{Item: experiment.Item{
		ID: "try-1", Name: "try", Live: experiment.LiveFacts{Present: true, CurrentPath: "/tries/try"},
	}}}
	model.remotes = []RemoteRow{{
		Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/demo"}, LocalPath: "/repos/demo",
	}}
	model.skills = []agentskill.Skill{{
		Name: "skill", Scope: agentskill.ScopeProject, Path: "/skills/skill", Presence: agentskill.PresencePresent,
	}}
	model.mcp = []agentmcp.Declaration{{
		Name: "server", Agent: agentmcp.AgentClaudeCode, Scope: agentmcp.ScopeProject, ConfigPath: "/repos/demo/.mcp.json",
	}}

	for _, view := range Views {
		candidate := model
		candidate.view = view
		candidate.setAt(0)
		candidate = candidate.openActionMenu()
		if candidate.overlay.kind != overlayActionMenu || candidate.overlay.optionCount == 0 {
			t.Errorf("%s action menu = kind %v, options %d", view, candidate.overlay.kind, candidate.overlay.optionCount)
		}
		if candidate.overlay.selection.view != view || candidate.overlay.selection.key == "" {
			t.Errorf("%s action menu token = %+v", view, candidate.overlay.selection)
		}
		touch := model
		touch.view = view
		touch, command := applyMouse(touch, mouseMessage(3, 2+touch.listPreambleLines(), tea.MouseButtonLeft, tea.MouseActionPress))
		if command != nil || touch.overlay.kind != overlayActionMenu || touch.overlay.selection != candidate.overlay.selection {
			t.Errorf("%s selected-row tap differs from keyboard actions", view)
		}
	}
}

func TestRepoCreateActionMenuDoesNotRequireSelectedRow(t *testing.T) {
	for _, test := range []struct {
		name, pending                   string
		empty, filter, disappear, child bool
	}{
		{name: "empty", empty: true},
		{name: "filtered empty", filter: true},
		{name: "cached", pending: "cached"},
		{name: "loading", pending: "loading"},
		{name: "runtime pending", pending: "runtime pending"},
		{name: "cached child", pending: "cached", child: true},
		{name: "selected row disappeared", disappear: true},
		{name: "selected child disappeared", disappear: true, child: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			creates := 0
			m := New(Actions{Repos: RepoActions{Create: func() (*exec.Cmd, error) {
				creates++
				return exec.Command("true"), nil
			}}}, nil, nil)
			m.view = ViewRepos
			if !test.empty {
				m.repos = []RepoRow{{Repo: repo.Repo{Name: "one", Path: "/one"}, Pending: test.pending}}
			}
			if test.filter {
				m.filter = "no-match"
			}
			if test.child {
				m.repos[0].Context.Checkouts = []inventory.RepoCheckout{
					{Worktree: gitx.Worktree{Path: "/one", Main: true}, Exists: true},
					{Worktree: gitx.Worktree{Path: "/worktrees/one/feature"}, Exists: true},
				}
				m.toggleRepo(m.repos[0])
				m.setAt(1)
			}
			next, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
			m = next.(Model)
			if command != nil || creates != 0 || m.overlay.kind != overlayActionMenu {
				t.Fatal("opening Create menu executed an action")
			}
			found := false
			for i := 0; i < m.overlay.optionCount; i++ {
				if m.overlay.options[i].action == listActionRepoCreate {
					m.overlay.optionIndex, found = i, true
					break
				}
			}
			if !found {
				t.Fatal("new repository missing from action menu")
			}
			if test.disappear {
				m.repos = nil
			}
			next, command = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(Model)
			if creates != 1 || command == nil || m.err != nil || m.overlay.kind != overlayNone {
				t.Fatalf("Create calls=%d command=%v err=%v overlay=%v", creates, command, m.err, m.overlay.kind)
			}
		})
	}
}

func TestRepoCreatePreservesCallbackAvailabilityAndErrors(t *testing.T) {
	failure := errors.New("wizard unavailable")
	for _, test := range []struct {
		name      string
		err       error
		available bool
	}{
		{name: "callback absent"},
		{name: "callback failure", available: true, err: failure},
		{name: "callback success", available: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "one", Path: "/one"}, Pending: "cached"}})
			m.view = ViewRepos
			if test.available {
				m.actions.Repos.Create = func() (*exec.Cmd, error) {
					calls++
					return exec.Command("true"), test.err
				}
			}
			menu := m.openActionMenu()
			listed := false
			for i := 0; i < menu.overlay.optionCount; i++ {
				if menu.overlay.options[i].action == listActionRepoCreate {
					listed = true
					if (menu.overlay.options[i].disabled == "") != test.available {
						t.Fatal("wizard availability reason disagrees with callback")
					}
				}
			}
			if !listed {
				t.Fatalf("Create menu availability=%v", listed)
			}
			if test.available {
				m.err = errors.New("old failure")
			}
			next, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
			m = next.(Model)
			if (calls == 1) != test.available || (command != nil) != (test.available && test.err == nil) || !errors.Is(m.err, test.err) {
				t.Fatalf("calls=%d command=%v err=%v", calls, command, m.err)
			}
		})
	}
}

func TestRepoCreateBlockedDuringActiveClone(t *testing.T) {
	for _, phase := range []remoteClonePhase{remoteCloneRunning, remoteCloneRefreshing, remoteCloneOpening} {
		m := New(Actions{Repos: RepoActions{Create: func() (*exec.Cmd, error) {
			t.Fatal("Create ran during an active clone")
			return nil, nil
		}}}, nil, nil)
		m.view = ViewRepos
		m.remoteClone.phase = phase
		for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("n")}, {Type: tea.KeyCtrlO}} {
			next, command := m.Update(key)
			got := next.(Model)
			if command != nil || got.overlay.kind != overlayNone || !got.remoteClone.active() {
				t.Fatalf("phase=%v key=%s accepted another action", phase, key)
			}
		}
	}
}

func TestPendingRepoStillBlocksRowDependentActions(t *testing.T) {
	for _, pending := range []string{"cached", "loading", "runtime pending"} {
		t.Run(pending, func(t *testing.T) {
			m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "one", Path: "/one", HasGit: true}, Worktrees: 1, Pending: pending}})
			m.view = ViewRepos
			for _, action := range []listAction{
				listActionOpen, listActionStartWorktree, listActionStartDirect, listActionRepoMetadata,
				listActionToggleWorktrees, listActionCopy, listActionCopyCloneURL,
				listActionAddNote, listActionBrowseNotes, listActionStats, listActionBrowse,
				listActionTriage, listActionHygieneRepo, listActionSkillRepo,
			} {
				next, command := m.runListAction(action)
				got := next.(Model)
				if command != nil || got.mode != modeList || got.overlay.kind != overlayNone || got.status != "Waiting for fresh repository observations…" {
					t.Fatalf("pending=%q action=%v escaped freshness guard: mode=%v command=%v status=%q", pending, action, got.mode, command, got.status)
				}
			}
		})
	}
}

func TestRepoActionMenuStillRejectsMissingSelection(t *testing.T) {
	for _, action := range []listAction{listActionOpen, listActionStartWorktree, listActionStartDirect, listActionRepoMetadata, listActionBrowseNotes} {
		m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Name: "one", Path: "/one"}}})
		m.view = ViewRepos
		m.overlay = overlayState{kind: overlayActionMenu, selection: m.currentToken()}
		m.overlay.addOption(action, "selected repository action")
		m.repos = []RepoRow{{Repo: repo.Repo{Name: "two", Path: "/two"}}}
		next, command := m.runOverlayAction()
		got := next.(Model)
		if command != nil || got.err == nil || got.err.Error() != "selected row changed while its action menu was open" || got.overlay.kind != overlayNone {
			t.Fatalf("stale action=%v command=%v err=%v", action, command, got.err)
		}
	}
}

func TestMCPSelectionIdentityIncludesCheckoutAndSource(t *testing.T) {
	model := New(Actions{}, nil, nil)
	model.view = ViewMCP
	model.mcp = []agentmcp.Declaration{
		{Name: "same", Agent: agentmcp.AgentClaudeCode, Scope: agentmcp.ScopeProject, Checkout: "/one", ConfigPath: "/shared.json", Source: agentmcp.SourceDirect},
		{Name: "same", Agent: agentmcp.AgentClaudeCode, Scope: agentmcp.ScopeProject, Checkout: "/two", ConfigPath: "/shared.json", Source: agentmcp.SourcePlugin},
	}
	model.setAt(0)
	first, _ := model.currentSelectionToken()
	model.setAt(1)
	second, _ := model.currentSelectionToken()
	if first.key == second.key {
		t.Fatalf("distinct MCP declarations shared identity %q", first.key)
	}
}

func TestPromptAndCopyModesKeepTheirOriginalSelection(t *testing.T) {
	first := inventory.Row{Task: &task.Task{ID: "first", Name: "first", State: task.Hot}, CheckoutExists: true}
	second := inventory.Row{Task: &task.Task{ID: "second", Name: "second", State: task.Hot}, CheckoutExists: true}
	parked := ""
	model := New(Actions{Park: func(_ context.Context, selected *task.Task, _ string) (string, error) {
		parked = selected.ID
		return "parked", nil
	}}, []inventory.Row{first, second}, nil)
	next, _ := model.runListAction(listActionPark)
	model = next.(Model)
	model.rows = []inventory.Row{second, first}
	model.setAt(0)
	if detail := model.renderDetail(); !strings.Contains(detail, "first") || strings.Contains(detail, "second") {
		t.Fatalf("park prompt changed displayed target:\n%s", detail)
	}
	command := model.submit(modeConfirmPark, "later")
	if command == nil {
		t.Fatal("park prompt lost its target")
	}
	_ = command()
	if parked != "first" {
		t.Fatalf("park prompt changed target to %q", parked)
	}

	repoModel := New(Actions{Start: func(context.Context, RepoRow, string) (string, error) {
		return "", nil
	}}, nil, []RepoRow{
		{Repo: repo.Repo{Name: "repo-one", Path: "/one"}},
		{Repo: repo.Repo{Name: "repo-two", Path: "/two"}},
	})
	repoModel.view = ViewRepos
	next, _ = repoModel.runListAction(listActionStartWorktree)
	repoModel = next.(Model)
	repoModel.repos = []RepoRow{
		{Repo: repo.Repo{Name: "repo-two", Path: "/two"}},
		{Repo: repo.Repo{Name: "repo-one", Path: "/one"}},
	}
	repoModel.setAt(0)
	if detail := repoModel.renderDetail(); !strings.Contains(detail, "repo-one") || strings.Contains(detail, "repo-two") {
		t.Fatalf("start prompt changed displayed target:\n%s", detail)
	}

	copied := ""
	model = New(Actions{Copy: func(value string) error {
		copied = value
		return nil
	}}, nil, nil)
	model.view = ViewMCP
	one := agentmcp.Declaration{Name: "one", Agent: agentmcp.AgentClaudeCode, Scope: agentmcp.ScopeProject, Checkout: "/one", ConfigPath: "/one.json"}
	two := agentmcp.Declaration{Name: "two", Agent: agentmcp.AgentClaudeCode, Scope: agentmcp.ScopeProject, Checkout: "/two", ConfigPath: "/two.json"}
	model.mcp = []agentmcp.Declaration{one, two}
	next, _ = model.runListAction(listActionCopy)
	model = next.(Model)
	model.mcp = []agentmcp.Declaration{two, one}
	model.setAt(0)
	next, command = model.updateCopy(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	model = next.(Model)
	if command == nil {
		t.Fatal("copy mode lost its original declaration")
	}
	_ = command()
	if !strings.Contains(copied, "server: one") || strings.Contains(copied, "server: two") {
		t.Fatalf("copy mode changed target:\n%s", copied)
	}
}

func TestActionMenuRevalidatesStableSelectionBeforeRunning(t *testing.T) {
	opened := 0
	actions := Actions{Open: func(context.Context, *task.Task) (OpenResult, error) {
		opened++
		return OpenResult{}, nil
	}}
	first := inventory.Row{Task: &task.Task{ID: "first", Name: "first"}, CheckoutExists: true}
	second := inventory.Row{Task: &task.Task{ID: "second", Name: "second"}, CheckoutExists: true}
	model := New(actions, []inventory.Row{first, second}, nil)
	model = model.openActionMenu()
	model.rows = []inventory.Row{second}

	next, command := model.runOverlayAction()
	model = next.(Model)
	if command != nil || opened != 0 {
		t.Fatalf("stale menu ran action: command=%v opened=%d", command, opened)
	}
	if model.overlay.kind != overlayNone || model.err == nil {
		t.Fatalf("stale menu did not close with an error: overlay=%v err=%v", model.overlay.kind, model.err)
	}
}

func TestActionMenuAndKeyboardShareOpenDispatcher(t *testing.T) {
	var opened []string
	actions := Actions{Open: func(_ context.Context, selected *task.Task) (OpenResult, error) {
		opened = append(opened, selected.ID)
		return OpenResult{}, nil
	}}
	row := inventory.Row{Task: &task.Task{ID: "same", Name: "same"}, CheckoutExists: true}
	model := New(actions, []inventory.Row{row}, nil)

	_, command := model.runListAction(listActionOpen)
	if command == nil {
		t.Fatal("keyboard dispatcher returned no command")
	}
	_ = command()
	model = model.openActionMenu()
	_, command = model.runOverlayAction()
	if command == nil {
		t.Fatal("action menu returned no command")
	}
	_ = command()
	if len(opened) != 2 || opened[0] != "same" || opened[1] != "same" {
		t.Fatalf("open dispatches = %v", opened)
	}
}
