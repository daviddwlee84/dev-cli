package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
)

func TestMissingLocklessSkillOffersOnlyAvailableFileActions(t *testing.T) {
	model := New(Actions{
		Copy:     func(string) error { return nil },
		ReadFile: func(context.Context, string) (string, error) { return "", nil },
	}, nil, nil)
	model.view = ViewSkills
	model.skills = []agentskill.Skill{{
		Name: "dev-cli", Scope: agentskill.ScopeProject, Presence: agentskill.PresenceMissing,
	}}
	model.seedViewSnapshot(ViewSkills, "live", "fresh", true)
	model = model.openActionMenu()
	if model.overlay.kind != overlayActionMenu {
		t.Fatal("missing skill should still offer its safe summary")
	}
	for i := 0; i < model.overlay.optionCount; i++ {
		switch model.overlay.options[i].action {
		case listActionOpenCapabilityFile, listActionCopyCapabilityPath, listActionCopyCapabilityRaw:
			t.Fatalf("missing lockless skill offered unavailable file action %q", model.overlay.options[i].label)
		}
	}
	model.overlay = overlayState{}
	next, _ := model.runListAction(listActionCopy)
	model = next.(Model)
	if help := model.copyBindingHelp(); help != "s safe summary" {
		t.Fatalf("missing skill copy help = %q", help)
	}
	if footer := model.renderFooter(); strings.Contains(footer, "e file") {
		t.Fatalf("missing skill footer offered editor:\n%s", footer)
	}
}

func TestExecCompletionReenablesMouseTracking(t *testing.T) {
	model := New(Actions{}, nil, nil)
	next, command := model.Update(afterExec(copyMsg{status: "copied"}))
	model = next.(Model)
	if model.status != "copied" || command == nil {
		t.Fatalf("exec completion status=%q command=%v", model.status, command)
	}
	if message := command(); !strings.Contains(fmt.Sprintf("%T", message), "enableMouseCellMotionMsg") {
		t.Fatalf("exec completion command = %T, want mouse enable", message)
	}
}

func TestCapabilityFileEditReloadsOnlyOriginatingView(t *testing.T) {
	skillLoads, mcpLoads := 0, 0
	model := New(Actions{
		ReloadSkills: func(_ context.Context, scope CapabilityScope) ([]agentskill.Skill, error) {
			skillLoads++
			if scope != CapabilityAllRepositories {
				t.Fatalf("skill reload scope = %s", scope)
			}
			return []agentskill.Skill{{Name: "edited"}}, nil
		},
		ReloadMCP: func(context.Context, CapabilityScope) ([]agentmcp.Declaration, error) {
			mcpLoads++
			return nil, nil
		},
	}, nil, nil)
	model.view = ViewSkills
	model.capabilityScope = CapabilityAllRepositories
	model.seedViewSnapshot(ViewSkills, "live", "fresh", true)
	model.seedViewSnapshot(ViewMCP, "live", "fresh", true)

	next, command := model.Update(capabilityFileEditedMsg{view: ViewSkills})
	model = next.(Model)
	if command == nil {
		t.Fatal("successful editor return did not start a capability reload")
	}
	next, _ = model.Update(command())
	model = next.(Model)
	if skillLoads != 1 || mcpLoads != 0 {
		t.Fatalf("capability reloads skills=%d MCP=%d", skillLoads, mcpLoads)
	}
	if len(model.skills) != 1 || model.skills[0].Name != "edited" {
		t.Fatalf("edited skill snapshot = %+v", model.skills)
	}
}
