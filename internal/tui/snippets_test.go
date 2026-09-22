package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/forge"
)

func snippetTestRow(id, title string) SnippetRow {
	return SnippetRow{Key: "github:test:" + id, ID: id, Provider: "github", Title: title,
		Owner: "tester", URL: "https://gist.github.com/tester/" + id, Visibility: "secret", Files: []string{"example.py"}}
}

func snippetTestModel(load func(context.Context, SnippetQuery) (SnippetResult, error)) Model {
	m := New(Actions{Snippets: SnippetActions{Load: load}}, nil, nil).WithRemotes([]RemoteRow{
		{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/first"}},
		{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/second"}},
	})
	m.view = ViewRemote
	return m
}

func TestSnippetsAreLazyAndRestoreRepositoryState(t *testing.T) {
	loads := 0
	m := snippetTestModel(func(context.Context, SnippetQuery) (SnippetResult, error) {
		loads++
		return SnippetResult{Rows: []SnippetRow{snippetTestRow("1", "first gist"), snippetTestRow("2", "second gist")}, Complete: true}, nil
	})
	m.filter, m.remoteCursor = "owner", 1
	m.tableSorts[int(ViewRemote)] = tableSort{column: "repository"}
	m.View()
	next, command := m.afterViewSwitch()
	m = next.(Model)
	if loads != 0 || command != nil || m.snippets.enabled {
		t.Fatal("ordinary REMOTE visit loaded snippets")
	}
	next, command = m.runListAction(listActionRemoteContent)
	m = next.(Model)
	if loads != 0 || command == nil {
		t.Fatal("toggle did not schedule lazy load")
	}
	next, _ = m.Update(command())
	m = next.(Model)
	if loads != 1 || m.count() != 2 || m.currentDir() != "" {
		t.Fatalf("snippet state %+v", m.snippets)
	}
	m.snippets.filter, m.snippets.cursor = "gist", 1
	next, _ = m.cycleTableSort("snippet")
	m = next.(Model)
	next, command = m.runListAction(listActionRemoteContent)
	m = next.(Model)
	if command != nil || m.remoteCursor != 1 || m.filter != "owner" || m.activeTableSort().column != "repository" {
		t.Fatal("repository state not restored")
	}
	next, command = m.runListAction(listActionRemoteContent)
	m = next.(Model)
	if command != nil || m.snippets.cursor != 1 || m.snippets.filter != "gist" || m.activeTableSort().column != "snippet" {
		t.Fatal("snippet state not retained")
	}
}

func TestSnippetScopeCancelsAndDiscardsStaleResponses(t *testing.T) {
	contexts := []context.Context{}
	m := snippetTestModel(func(ctx context.Context, query SnippetQuery) (SnippetResult, error) {
		contexts = append(contexts, ctx)
		return SnippetResult{Rows: []SnippetRow{snippetTestRow(query.Provider, query.Provider)}, Complete: true}, nil
	})
	next, first := m.runListAction(listActionRemoteContent)
	m = next.(Model)
	firstResult := first()
	next, second := m.runListAction(listActionSnippetGitLab)
	m = next.(Model)
	if contexts[0].Err() == nil {
		t.Fatal("superseded scope was not canceled")
	}
	next, _ = m.Update(firstResult)
	m = next.(Model)
	if m.snippets.hasSnapshot || !m.snippets.loading {
		t.Fatal("stale response became current")
	}
	next, _ = m.Update(second())
	m = next.(Model)
	if got, ok := m.currentSnippet(); !ok || got.Title != "gitlab" {
		t.Fatalf("new scope lost: %+v", got)
	}
	next, late := m.loadSnippets()
	m = next.(Model)
	lateResult := late()
	next, _ = m.runListAction(listActionRemoteContent)
	m = next.(Model)
	next, _ = m.Update(lateResult)
	m = next.(Model)
	if m.snippets.enabled || len(m.remotes) != 2 || !m.snippets.stale {
		t.Fatal("late snippet result changed repository mode")
	}
}

func TestSnippetMetadataFilterAndExplicitContentSearch(t *testing.T) {
	queries := []SnippetQuery{}
	m := snippetTestModel(func(_ context.Context, query SnippetQuery) (SnippetResult, error) {
		queries = append(queries, query)
		return SnippetResult{Rows: []SnippetRow{snippetTestRow("1", "Python notes")}, Complete: true}, nil
	})
	next, command := m.runListAction(listActionRemoteContent)
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	next, _ = m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(Model)
	next, _ = m.updateFilter(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("example.py")})
	m = next.(Model)
	if len(queries) != 1 || m.count() != 1 || m.filter != "" {
		t.Fatal("metadata filter issued network request or changed repository filter")
	}
	m.mode = modeList
	next, _ = m.runListAction(listActionSnippetSearch)
	m = next.(Model)
	m.input.SetValue("def example")
	next, command = m.updateSnippetPrompt(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if command == nil || !m.snippets.query.Content || m.snippets.query.Text != "def example" {
		t.Fatal("content query not explicit")
	}
	_ = command()
	if len(queries) != 2 || !queries[1].Content {
		t.Fatal("content request missing")
	}
	next, _ = m.runListAction(listActionSnippetProject)
	m = next.(Model)
	m.input.SetValue("group/project")
	next, command = m.updateSnippetPrompt(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if command != nil || m.snippets.query.Project != "" {
		t.Fatal("canceled scope prompt changed request")
	}
}

func TestSnippetActionsCannotCloneOrOpenLocalCheckout(t *testing.T) {
	opened, copied := "", ""
	m := snippetTestModel(nil)
	m.snippets.enabled, m.snippets.hasSnapshot = true, true
	m.snippets.result = SnippetResult{Rows: []SnippetRow{snippetTestRow("1", "gist")}, Complete: true}
	m.actions.Snippets.Open = func(_ context.Context, row SnippetRow) error { opened = row.URL; return nil }
	m.actions.Copy = func(value string) error { copied = value; return nil }
	m.actions.CloneRemote = func(context.Context, RemoteRow) (string, error) { t.Fatal("snippet clone attempted"); return "", nil }
	m.actions.OpenRemote = func(context.Context, RemoteRow) (OpenResult, error) {
		t.Fatal("snippet local open attempted")
		return OpenResult{}, nil
	}
	menu := m.openActionMenu()
	for index := 0; index < menu.overlay.optionCount; index++ {
		id := menu.overlay.options[index].action
		if id == listActionRemoteClone || id == listActionCopyCloneURL || id == listActionBrowse || id == listActionStartWorktree || id == listActionStartDirect {
			t.Fatalf("repository action %d offered for snippet", id)
		}
	}
	_, command := m.runListAction(listActionOpen)
	if command == nil {
		t.Fatal("missing browser action")
	}
	_ = command()
	_, command = m.runListAction(listActionCopy)
	if command == nil {
		t.Fatal("missing URL copy")
	}
	_ = command()
	if opened == "" || opened != copied {
		t.Fatalf("open=%q copy=%q", opened, copied)
	}
}

func TestSnippetReadinessPartialErrorsAndCancellation(t *testing.T) {
	m := snippetTestModel(nil)
	next, command := m.runListAction(listActionRemoteContent)
	m = next.(Model)
	if command != nil || m.snippets.err == nil {
		t.Fatal("missing loader appeared ready")
	}
	menu := m.openActionMenu()
	menu = selectMenuAction(t, menu, listActionSnippetCreate)
	if menu.overlay.options[menu.overlay.optionIndex].disabled == "" {
		t.Fatal("missing wizard appeared ready")
	}
	m.actions.Snippets.Load = func(context.Context, SnippetQuery) (SnippetResult, error) {
		return SnippetResult{Rows: []SnippetRow{snippetTestRow("1", "one")}}, errors.New("gitlab authentication unavailable")
	}
	next, command = m.loadSnippets()
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	if !m.snippets.hasSnapshot || !m.snippets.stale || m.count() != 1 || !strings.Contains(m.View(), "authentication unavailable") {
		t.Fatal("partial provider failure hid rows or error")
	}
	next, command = m.loadSnippets()
	m = next.(Model)
	late := command()
	next, _ = m.runListAction(listActionSnippetCancel)
	m = next.(Model)
	next, _ = m.Update(late)
	m = next.(Model)
	if m.snippets.loading || m.count() != 1 || !strings.Contains(m.snippets.status, "canceled") {
		t.Fatal("cancellation lost snapshot or accepted late response")
	}
}

func TestSnippetWizardKeepsProviderChoiceExplicit(t *testing.T) {
	m := snippetTestModel(nil)
	m.snippets.enabled = true
	m.snippets.query.Provider = "all"
	called := false
	m.actions.Snippets.Create = func(query SnippetQuery) (*exec.Cmd, error) {
		called = true
		if query.Provider != "all" {
			t.Fatalf("provider silently changed to %q", query.Provider)
		}
		return exec.Command("dev", "snippet", "create"), nil
	}
	_, command := m.runListAction(listActionSnippetCreate)
	if !called || command == nil {
		t.Fatal("creation did not hand off to shared wizard")
	}
}

func TestSnippetMetadataCannotInjectTerminalControls(t *testing.T) {
	m := snippetTestModel(func(context.Context, SnippetQuery) (SnippetResult, error) {
		return SnippetResult{Rows: []SnippetRow{snippetTestRow("1", "safe\x1b[2J\u202etitle\nnext")}, Complete: true}, nil
	})
	next, command := m.runListAction(listActionRemoteContent)
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	row, _ := m.currentSnippet()
	if strings.ContainsAny(row.Title, "\x1b\u202e\n") || !strings.Contains(row.Title, "next") {
		t.Fatalf("unsafe title %q", row.Title)
	}
}

func TestSnippetResponsePreservesSelectionWhileAnotherViewIsActive(t *testing.T) {
	m := snippetTestModel(func(context.Context, SnippetQuery) (SnippetResult, error) {
		return SnippetResult{Rows: []SnippetRow{snippetTestRow("2", "two"), snippetTestRow("1", "one")}, Complete: true}, nil
	})
	m.snippets.enabled, m.snippets.hasSnapshot = true, true
	m.snippets.result = SnippetResult{Rows: []SnippetRow{snippetTestRow("1", "one"), snippetTestRow("2", "two")}, Complete: true}
	m.snippets.cursor = 1
	next, command := m.loadSnippets()
	m = next.(Model)
	m.view = ViewRepos
	next, _ = m.Update(command())
	m = next.(Model)
	if m.view != ViewRepos {
		t.Fatal("response navigated away from local repositories")
	}
	m.view = ViewRemote
	row, ok := m.currentSnippet()
	if !ok || row.ID != "2" {
		t.Fatalf("selection changed during background result: %+v", row)
	}
}

func TestSnippetRenderingFitsAndProviderMenuDoesNotLoad(t *testing.T) {
	loads := 0
	m := snippetTestModel(func(context.Context, SnippetQuery) (SnippetResult, error) { loads++; return SnippetResult{}, nil })
	m.snippets.enabled, m.snippets.hasSnapshot = true, true
	m.snippets.result = SnippetResult{Rows: []SnippetRow{snippetTestRow("1", strings.Repeat("長", 80))}, Complete: true}
	for _, width := range []int{40, 60, 80, 100, 160} {
		m.width = width
		for _, line := range strings.Split(m.renderSnippets(), "\n") {
			if lipgloss.Width(line) > width {
				t.Errorf("width %d overflow: %q", width, line)
			}
		}
	}
	next, command := m.runListAction(listActionSnippetProvider)
	m = next.(Model)
	if command != nil || loads != 0 {
		t.Fatal("opening provider selector queried a provider")
	}
	next, command = m.updateOverlay(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if command != nil || loads != 0 || m.overlay.kind != overlayNone {
		t.Fatal("canceling provider selector made a request")
	}
}
