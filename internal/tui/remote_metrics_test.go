package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
)

func metricRepo(name string, stars *int64) RemoteRow {
	row := RemoteRow{Repo: forge.RemoteRepo{Forge: forge.GitHub, Name: name, FullName: "owner/" + name, URL: "https://github.com/owner/" + name}}
	if stars != nil {
		row.Repo.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(*stars, time.Now())}
	}
	return row
}

func consumeMetrics(t *testing.T, m Model, command tea.Cmd) Model {
	t.Helper()
	for count := 0; command != nil; count++ {
		if count > 10 {
			t.Fatal("metrics producer did not finish")
		}
		next, following := m.Update(command())
		m, command = next.(Model), following
	}
	return m
}

func TestRemoteMetricsLoadAfterInventoryAndKeepSelection(t *testing.T) {
	calls := 0
	first, second := metricRepo("a", nil), metricRepo("b", nil)
	second.LocalPath = "/retained/local"
	m := New(Actions{LoadRemoteMetrics: func(_ context.Context, rows []RemoteRow, emit func([]RemoteRow)) error {
		calls++
		for i := range rows {
			rows[i].Repo.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(int64(i*10), time.Now())}
			emit(rows[i : i+1])
		}
		return nil
	}}, nil, nil).WithRemotes([]RemoteRow{first, second})
	m.View()
	if calls != 0 {
		t.Fatal("render queried metrics")
	}
	m.view = ViewRemote
	m.tableSorts[int(ViewRemote)] = tableSort{column: "stars", descending: true}
	m.selectRemoteKey(remoteRowKey(first))
	next, command := m.afterViewSwitch()
	m = next.(Model)
	if command == nil || !m.remoteMetrics.loading || calls != 0 {
		t.Fatal("visit did not schedule independent metrics")
	}
	m = consumeMetrics(t, m, command)
	if calls != 1 || m.selectedRemoteKey() != remoteRowKey(first) || m.remotes[1].LocalPath != second.LocalPath || m.remoteMetrics.loading {
		t.Fatal("patch lost focus/local data or did not finish")
	}
	if m.visibleRemotes()[0].Repo.Name != "b" {
		t.Fatal("numeric ordering not updated")
	}
	_, command = m.afterViewSwitch()
	if command != nil {
		t.Fatal("ordinary revisit retried metrics")
	}
}

func TestRemoteMetricsRejectSupersededAndForeignPatches(t *testing.T) {
	var readContext context.Context
	row := metricRepo("a", nil)
	m := New(Actions{LoadRemoteMetrics: func(ctx context.Context, rows []RemoteRow, emit func([]RemoteRow)) error {
		readContext = ctx
		rows[0].Repo.URL = "https://other.example/owner/a"
		rows[0].Repo.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(99, time.Now())}
		emit(rows)
		return nil
	}}, nil, nil).WithRemotes([]RemoteRow{row})
	m.view = ViewRemote
	command := m.startRemoteMetrics(false)
	message := command()
	next, remaining := m.Update(message)
	m = next.(Model)
	if m.remotes[0].Repo.Metrics != nil {
		t.Fatal("foreign host attached metrics")
	}
	m.beginViewLoad(ViewRemote, loadRefresh)
	if readContext.Err() == nil {
		t.Fatal("inventory refresh did not cancel metrics")
	}
	if remaining != nil {
		next, _ = m.Update(remaining())
		m = next.(Model)
	}
	if m.remotes[0].Repo.Metrics != nil || !m.viewLoad(ViewRemote).loading {
		t.Fatal("late result changed new inventory request")
	}
}

func TestMetricSortingUsesNumbersAndUnknownLast(t *testing.T) {
	zero, two, ten := int64(0), int64(2), int64(10)
	m := New(Actions{}, nil, nil).WithRemotes([]RemoteRow{metricRepo("ten", &ten), metricRepo("unknown", nil), metricRepo("two", &two), metricRepo("zero", &zero)})
	m.view = ViewRemote
	for _, descending := range []bool{false, true} {
		m.tableSorts[int(ViewRemote)] = tableSort{column: "stars", descending: descending}
		rows := m.visibleRemotes()
		want := "zero"
		if descending {
			want = "ten"
		}
		if rows[0].Repo.Name != want || rows[3].Repo.Name != "unknown" {
			t.Fatal(rows)
		}
	}
	m.snippets.enabled = true
	a, b := snippetTestRow("a", "a"), snippetTestRow("b", "b")
	a.FilesComplete = true
	b.Files = []string{"one", "two"}
	m.snippets.result.Rows = []SnippetRow{b, a}
	m.snippets.order = tableSort{column: "files", descending: true}
	if m.visibleSnippets()[1].ID != "b" {
		t.Fatal("incomplete file list treated as exact total")
	}
}

func TestMetricLayoutsAndHiddenColumnSorting(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 120, 160} {
		m := New(Actions{}, nil, nil).WithRemotes([]RemoteRow{metricRepo("long-repository", nil)})
		m.width, m.height, m.view = width, 32, ViewRemote
		for _, snippets := range []bool{false, true} {
			m.snippets.enabled = snippets
			m.snippets.hasSnapshot = true
			m.snippets.result.Rows = []SnippetRow{snippetTestRow("one", "some gist")}
			for _, line := range strings.Split(ansi.Strip(m.renderRawList()), "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("width=%d line=%q", width, line)
				}
			}
			next, _ := m.runListAction(listActionSortMenu)
			menu := next.(Model).overlay
			found := false
			for i := 0; i < menu.optionCount; i++ {
				found = found || menu.options[i].column == "stars"
			}
			if !found {
				t.Fatal("hidden metric absent from keyboard sort menu")
			}
		}
	}
}

func TestSnippetMetricsAreIndependentAndCancelable(t *testing.T) {
	var readContext context.Context
	row := snippetTestRow("one", "one")
	row.NodeID = "node-one"
	m := snippetTestModel(nil)
	m.snippets.enabled, m.snippets.hasSnapshot = true, true
	m.snippets.result = SnippetResult{Rows: []SnippetRow{row}, Complete: true}
	m.actions.Snippets.LoadMetrics = func(ctx context.Context, rows []SnippetRow, emit func([]SnippetRow)) error {
		readContext = ctx
		rows[0].Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(0, time.Now())}
		emit(rows)
		return errors.New("statistics request failed")
	}
	command := m.startSnippetMetrics(false)
	m = consumeMetrics(t, m, command)
	if readContext.Err() == nil || !m.snippets.result.Complete || m.snippets.err != nil || m.snippets.result.Rows[0].Metrics.Stars.Value == nil {
		t.Fatal("metrics failure changed inventory or zero was lost")
	}
	if m.snippets.metrics.warning == "" {
		t.Fatal("missing metrics warning")
	}
	m.snippets.metrics.attempted = false
	command = m.startSnippetMetrics(false)
	message := command()
	m.cancelSnippetLoad()
	next, _ := m.Update(message)
	if next.(Model).snippets.metrics.loading {
		t.Fatal("canceled metrics revived")
	}
}

func TestMetricFreshCacheAndStaleRendering(t *testing.T) {
	m := New(Actions{MetricsTTL: time.Hour}, nil, nil)
	known := forgemetrics.Known(0, time.Now())
	stale := forgemetrics.Known(2, time.Now().Add(-2*time.Hour))
	if m.metricText(known) != "0" || m.metricText(stale) != "2~" || m.metricText(nil) != "?" || m.metricText(forgemetrics.Unsupported()) != "—" {
		t.Fatal("observation states collapsed")
	}
	metrics := &forgemetrics.Metrics{Stars: known, Forks: known, OpenIssues: known, OpenPRs: known}
	row := metricRepo("a", nil)
	row.Repo.Metrics = metrics
	m = m.WithRemotes([]RemoteRow{row})
	m.actions.LoadRemoteMetrics = func(context.Context, []RemoteRow, func([]RemoteRow)) error {
		t.Fatal("fresh cache queried network")
		return nil
	}
	if m.startRemoteMetrics(false) != nil {
		t.Fatal("fresh cache scheduled work")
	}
}

func TestMetricDashboardHeightAndHeaderHit(t *testing.T) {
	for _, width := range []int{40, 80, 120, 160} {
		var rows []RemoteRow
		for i := 0; i < 30; i++ {
			rows = append(rows, metricRepo(strings.Repeat("a", i+1), nil))
		}
		m := New(Actions{}, nil, nil).WithRemotes(rows).WithVersion("v0.2.43-dirty", false)
		m.width, m.height, m.view = width, 24, ViewRemote
		if lines := lineCount(m.View()); lines > m.height {
			t.Fatalf("width=%d: dashboard height %d > %d", width, lines, m.height)
		}
		if width < 120 {
			continue
		}
		found := false
		for _, column := range m.tableHeader().columns {
			if column.key != "stars" {
				continue
			}
			found = true
			next, _ := m.updateMouse(tea.MouseMsg{X: column.from, Y: 2 + m.tableHeader().line, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			m = next.(Model)
			if m.activeTableSort().column != "stars" {
				t.Fatal("stats column hit sorted wrong field")
			}
		}
		if !found {
			t.Fatal("wide layout omitted stars header")
		}
	}
}
