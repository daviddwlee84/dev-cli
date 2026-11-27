package tui_test

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func openTargetActions(rec *recorder, targets []tui.OpenTarget) tui.Actions {
	actions := newActions(rec, nil)
	actions.OpenTargets = func() []tui.OpenTarget { return targets }
	actions.OpenRepo = func(ctx context.Context, rr tui.RepoRow) (tui.OpenResult, error) {
		target := tui.OpenTargetFrom(ctx)
		rec.opened = append(rec.opened, "repo:"+rr.Repo.Name+"@"+target)
		if target == tui.OpenTargetCD {
			return tui.OpenResult{Directory: "/src/" + rr.Repo.Name}, nil
		}
		return tui.OpenResult{Status: "opened", RuntimeHandle: "sp1", Runtime: "herdr-core"}, nil
	}
	return actions
}

var pickerTargets = []tui.OpenTarget{
	{ID: "herdr-desktop", Label: "Herdr Desktop space", Default: true},
	{ID: "herdr", Label: "Herdr workspace"},
	{ID: tui.OpenTargetCD, Label: "cd here"},
}

func TestEnterAsksWhereToOpenWhenSeveralTargetsExist(t *testing.T) {
	rec := &recorder{}
	m := tui.New(openTargetActions(rec, pickerTargets), nil, []tui.RepoRow{repoRow("api")})
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40}, key("tab"), key("enter"))
	if len(rec.opened) != 0 {
		t.Fatalf("Enter opened %v before a target was chosen", rec.opened)
	}
	if out := m.View(); !strings.Contains(out, "open in") || !strings.Contains(out, "Herdr Desktop space") {
		t.Fatalf("picker is not shown:\n%s", out)
	}
	m = send(m, key("2"))
	if len(rec.opened) != 1 || rec.opened[0] != "repo:api@herdr" {
		t.Fatalf("opened = %v, want the second target", rec.opened)
	}
}

func TestPickerEnterUsesDefaultAndRecordsActivationRuntime(t *testing.T) {
	rec := &recorder{}
	m := tui.New(openTargetActions(rec, pickerTargets), nil, []tui.RepoRow{repoRow("api")})
	m = send(m, key("tab"), key("enter"), key("enter"))
	if len(rec.opened) != 1 || rec.opened[0] != "repo:api@herdr-desktop" {
		t.Fatalf("opened = %v, want the default target", rec.opened)
	}
	if m.Activation() != "sp1" || m.ActivationRuntime() != "herdr-core" {
		t.Fatalf("activation = %q on %q", m.Activation(), m.ActivationRuntime())
	}
}

func TestPickerEscCancelsWithoutOpening(t *testing.T) {
	rec := &recorder{}
	m := tui.New(openTargetActions(rec, pickerTargets), nil, []tui.RepoRow{repoRow("api")})
	m = send(m, key("tab"), key("enter"), key("esc"))
	if len(rec.opened) != 0 {
		t.Fatalf("esc opened %v", rec.opened)
	}
	if strings.Contains(m.View(), "open in") {
		t.Fatal("picker stayed open after esc")
	}
}

func TestSingleOpenTargetOpensWithoutAsking(t *testing.T) {
	rec := &recorder{}
	m := tui.New(openTargetActions(rec, []tui.OpenTarget{{ID: "herdr", Label: "Herdr workspace", Default: true}}), nil, []tui.RepoRow{repoRow("api")})
	send(m, key("tab"), key("enter"))
	if len(rec.opened) != 1 || rec.opened[0] != "repo:api@herdr" {
		t.Fatalf("opened = %v, want the only target at once", rec.opened)
	}
}

func TestCdKeyGoesToFolderWithoutPicker(t *testing.T) {
	rec := &recorder{}
	m := tui.New(openTargetActions(rec, pickerTargets), nil, []tui.RepoRow{repoRow("api")})
	m = send(m, key("tab"), key("C"))
	if len(rec.opened) != 1 || rec.opened[0] != "repo:api@cd" {
		t.Fatalf("opened = %v, want a cd", rec.opened)
	}
	if m.Chosen() != "/src/api" {
		t.Fatalf("Chosen = %q, want the folder for the shell wrapper", m.Chosen())
	}
}
