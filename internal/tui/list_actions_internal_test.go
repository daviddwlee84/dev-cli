package tui

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
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
