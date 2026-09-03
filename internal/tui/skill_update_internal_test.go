package tui

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
)

func TestSkillUpdateConfirmationPinsSelectedRepositoryRow(t *testing.T) {
	firstLock := &agentskill.LockMetadata{Name: "shared", Source: "owner/repo", SourceType: "github", SkillPath: "skills/shared/SKILL.md"}
	secondLock := &agentskill.LockMetadata{Name: "shared", Source: "owner/repo", SourceType: "github", SkillPath: "skills/shared/SKILL.md"}
	first := agentskill.Skill{Name: "shared", Scope: agentskill.ScopeProject, Checkout: "/repo/a", ManagedBy: agentskill.ManagedBySkills, Lock: firstLock}
	second := agentskill.Skill{Name: "shared", Scope: agentskill.ScopeProject, Checkout: "/repo/b", ManagedBy: agentskill.ManagedBySkills, Lock: secondLock}
	var selected agentskill.Skill
	actions := Actions{UpdateSkill: func(row agentskill.Skill) (*agentskill.MutationCommand, error) {
		selected = row
		return &agentskill.MutationCommand{Command: exec.Command("true")}, nil
	}}
	model := New(actions, nil, nil)
	model.view = ViewSkills
	model.skills = []agentskill.Skill{first, second}
	model.skillCursor = 1
	model.seedViewSnapshot(ViewSkills, perftrace.SourceLive, perftrace.FreshnessFresh, true)

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	model = next.(Model)
	// A concurrent refresh changes the cursor's row after confirmation opened.
	model.skills = []agentskill.Skill{first}
	if command := model.submit(modeConfirmSkillUpdate, ""); command == nil {
		t.Fatal("confirmation produced no update command")
	}
	if selected.Checkout != second.Checkout {
		t.Fatalf("confirmation updated checkout %q, want pinned %q", selected.Checkout, second.Checkout)
	}
}

func TestSkillUpdateWaitsForInFlightReload(t *testing.T) {
	lock := &agentskill.LockMetadata{Name: "shared", Source: "owner/repo", SourceType: "github", SkillPath: "skills/shared/SKILL.md"}
	model := New(Actions{}, nil, nil)
	model.view = ViewSkills
	model.skills = []agentskill.Skill{{
		Name: "shared", Scope: agentskill.ScopeProject, Checkout: "/repo/a",
		ManagedBy: agentskill.ManagedBySkills, Lock: lock,
	}}
	model.seedViewSnapshot(ViewSkills, perftrace.SourceLive, perftrace.FreshnessFresh, true)
	model.beginViewLoad(ViewSkills, loadRefresh)
	next, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	model = next.(Model)
	if command != nil || model.mode == modeConfirmSkillUpdate || !strings.Contains(model.viewStatus(ViewSkills), "wait") {
		t.Fatalf("update did not wait for in-flight reload: mode=%v status=%q", model.mode, model.viewStatus(ViewSkills))
	}
}

func TestReloadUpdatedSkillChecksExactCheckout(t *testing.T) {
	rows := []agentskill.Skill{
		{Name: "shared", Scope: agentskill.ScopeProject, Checkout: "/repo/a", Lock: &agentskill.LockMetadata{Name: "shared-lock"}},
		{Name: "restored-name", Scope: agentskill.ScopeProject, Checkout: "/repo/b", Lock: &agentskill.LockMetadata{Name: "shared-lock"}},
	}
	checked := ""
	actions := Actions{
		ReloadSkillsWithRepos: func(context.Context, []RepoRow) ([]agentskill.Skill, error) {
			return append([]agentskill.Skill(nil), rows...), nil
		},
		CheckSkills: func(_ context.Context, rows []agentskill.Skill) []agentskill.Skill {
			checked = rows[0].Checkout
			return rows
		},
	}
	trace := perftrace.New(64)
	model := New(actions, nil, []RepoRow{}).WithTrace(trace)
	model.beginViewLoad(ViewSkills, loadAction)
	message := model.reloadUpdatedSkill("old-local-name", "shared-lock", agentskill.ScopeProject, "/repo/b")()
	if checked != "/repo/b" {
		t.Fatalf("post-update check used %q, want /repo/b", checked)
	}
	result := message.(skillsMsg)
	if !result.valid || !result.checked || len(result.rows) != 2 {
		t.Fatalf("reload result = %+v", result)
	}
	producerEvents := 0
	for _, event := range trace.Freeze().Events {
		if event.Name == perftrace.TUIProducerSkills {
			producerEvents++
		}
	}
	if producerEvents != 1 {
		t.Fatalf("post-update producer events = %d", producerEvents)
	}
	model.beginViewLoad(ViewSkills, loadAction)
	missing := model.reloadUpdatedSkill("old-local-name", "missing-lock", agentskill.ScopeProject, "/repo/b")().(skillsMsg)
	if missing.checked || !strings.Contains(missing.status, "not found") {
		t.Fatalf("missing post-update identity was reported verified: %+v", missing)
	}
}
