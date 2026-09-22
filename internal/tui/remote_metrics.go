package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
)

// Metric producers emit immutable patches after inventory has been accepted.
// They never replace inventory or supply lifecycle authority.
type metricsUIState struct {
	generation uint64
	attempted  bool
	loading    bool
	cancel     context.CancelFunc
	events     <-chan metricsEvent
	warning    string
}

type metricsEvent struct {
	remotes  []RemoteRow
	snippets []SnippetRow
	done     bool
	err      error
}

type metricsMsg struct {
	generation uint64
	inventory  uint64
	snippets   bool
	query      SnippetQuery
	event      metricsEvent
}

func (s *metricsUIState) cancelLoad() {
	if s.cancel != nil {
		s.cancel()
	}
	s.generation++
	s.cancel, s.events, s.loading = nil, nil, false
	s.attempted = false
	s.warning = ""
}

func (m Model) metricsFresh(metrics *forgemetrics.Metrics, snippets bool) bool {
	if metrics == nil {
		return false
	}
	counts := []*forgemetrics.Count{metrics.Stars, metrics.Forks}
	if snippets {
		counts = append(counts, metrics.Comments)
	} else {
		counts = append(counts, metrics.OpenIssues, metrics.OpenPRs)
	}
	for _, count := range counts {
		if !forgemetrics.Fresh(count, m.actions.MetricsTTL, time.Now()) {
			return false
		}
	}
	return true
}

func (m *Model) startRemoteMetrics(force bool) tea.Cmd {
	if m.actions.LoadRemoteMetrics == nil || m.remoteMetrics.loading || m.remoteMetrics.attempted || !m.viewLoad(ViewRemote).hasSnapshot {
		return nil
	}
	var rows []RemoteRow
	for _, row := range m.remotes {
		if force || !m.metricsFresh(row.Repo.Metrics, false) {
			rows = append(rows, row)
		}
	}
	m.remoteMetrics.attempted = true
	if len(rows) == 0 {
		return nil
	}
	load := m.actions.LoadRemoteMetrics
	return m.startMetrics(false, func(ctx context.Context, emit func(metricsEvent)) error {
		return load(ctx, rows, func(patch []RemoteRow) { emit(metricsEvent{remotes: patch}) })
	})
}

func (m *Model) startSnippetMetrics(force bool) tea.Cmd {
	if m.actions.Snippets.LoadMetrics == nil || m.snippets.metrics.loading || m.snippets.metrics.attempted || !m.snippets.hasSnapshot {
		return nil
	}
	var rows []SnippetRow
	for _, row := range m.snippets.result.Rows {
		// GitLab's comment connection is deliberately not traversed in v1.
		if row.Provider == "github" && (force || !m.metricsFresh(row.Metrics, true)) {
			rows = append(rows, row)
		}
	}
	m.snippets.metrics.attempted = true
	if len(rows) == 0 {
		return nil
	}
	load := m.actions.Snippets.LoadMetrics
	return m.startMetrics(true, func(ctx context.Context, emit func(metricsEvent)) error {
		return load(ctx, rows, func(patch []SnippetRow) { emit(metricsEvent{snippets: patch}) })
	})
}

func (m *Model) startMetrics(snippets bool, work func(context.Context, func(metricsEvent)) error) tea.Cmd {
	state := &m.remoteMetrics
	inventory := m.viewLoad(ViewRemote).generation
	if snippets {
		state, inventory = &m.snippets.metrics, m.snippets.generation
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	events := make(chan metricsEvent, 2)
	state.generation++
	state.loading, state.cancel, state.events = true, cancel, events
	envelope := metricsMsg{generation: state.generation, inventory: inventory, snippets: snippets, query: m.snippets.query}
	return func() tea.Msg {
		go func() {
			defer close(events)
			emit := func(event metricsEvent) {
				select {
				case events <- event:
				case <-ctx.Done():
				}
			}
			err := work(ctx, emit)
			emit(metricsEvent{done: true, err: err})
		}()
		return receiveMetrics(ctx, events, envelope)
	}
}

func receiveMetrics(ctx context.Context, events <-chan metricsEvent, envelope metricsMsg) tea.Msg {
	select {
	case event, ok := <-events:
		if !ok {
			event.done = true
		}
		envelope.event = event
	case <-ctx.Done():
		envelope.event = metricsEvent{done: true, err: ctx.Err()}
	}
	return envelope
}

func (m Model) acceptMetrics(msg metricsMsg) (tea.Model, tea.Cmd) {
	state := &m.remoteMetrics
	inventory := m.viewLoad(ViewRemote).generation
	if msg.snippets {
		state, inventory = &m.snippets.metrics, m.snippets.generation
		if msg.query != m.snippets.query {
			return m, nil
		}
	}
	if !state.loading || msg.generation != state.generation || msg.inventory != inventory {
		return m, nil
	}
	if msg.snippets {
		focusModel := m
		focusModel.view, focusModel.snippets.enabled = ViewRemote, true
		focus, selected := focusModel.currentSnippet()
		patches := map[string]SnippetRow{}
		for _, row := range msg.event.snippets {
			patches[row.Key] = row
		}
		m.snippets.result.Rows = append([]SnippetRow(nil), m.snippets.result.Rows...)
		for i := range m.snippets.result.Rows {
			row := &m.snippets.result.Rows[i]
			if patch, ok := patches[row.Key]; ok && patch.URL == row.URL && patch.NodeID == row.NodeID && forgemetrics.Valid(patch.Metrics, time.Now()) {
				row.Metrics = forgemetrics.Merge(row.Metrics, patch.Metrics)
			}
		}
		if selected {
			for i, row := range m.visibleSnippets() {
				if row.Key == focus.Key {
					m.snippets.cursor = i
				}
			}
		}
	} else {
		focus := m.selectedRemoteKey()
		patches := map[string]*forgemetrics.Metrics{}
		for _, row := range msg.event.remotes {
			if key := forge.RepoMetricsKey(row.Repo); key != "" && forgemetrics.Valid(row.Repo.Metrics, time.Now()) {
				patches[key] = row.Repo.Metrics
			}
		}
		m.remotes = append([]RemoteRow(nil), m.remotes...)
		for i := range m.remotes {
			row := &m.remotes[i]
			if patch, ok := patches[forge.RepoMetricsKey(row.Repo)]; ok {
				row.Repo.Metrics = forgemetrics.Merge(row.Repo.Metrics, patch)
			}
		}
		m.selectRemoteKey(focus)
	}
	if msg.event.done {
		if state.cancel != nil {
			state.cancel()
		}
		state.loading, state.cancel, state.events = false, nil, nil
		if msg.event.err != nil && msg.event.err != context.Canceled {
			state.warning = "Some statistics unavailable; refresh to retry."
		}
		return m, nil
	}
	ctx, events := m.baseContext(), state.events
	return m, func() tea.Msg { return receiveMetrics(ctx, events, msg) }
}

func metricCell(count *forgemetrics.Count) sortCell {
	if count == nil || count.Value == nil || *count.Value < 0 {
		return sortCell{}
	}
	return numberCell(*count.Value)
}

func (m Model) metricText(count *forgemetrics.Count) string {
	if count == nil {
		return "?"
	}
	if count.State == forgemetrics.StateUnsupported {
		return "—"
	}
	if count.Value == nil || *count.Value < 0 {
		return "?"
	}
	text := fmt.Sprint(*count.Value)
	if !forgemetrics.Fresh(count, m.actions.MetricsTTL, time.Now()) {
		text += "~"
	}
	return text
}

func repoMetric(metrics *forgemetrics.Metrics, key string) *forgemetrics.Count {
	if metrics == nil {
		return nil
	}
	switch key {
	case "stars":
		return metrics.Stars
	case "forks":
		return metrics.Forks
	case "issues":
		return metrics.OpenIssues
	case "prs":
		return metrics.OpenPRs
	case "comments":
		return metrics.Comments
	}
	return nil
}

func (m Model) metricDetail(metrics *forgemetrics.Metrics, snippets bool) []string {
	keys := []string{"stars", "forks", "issues", "prs"}
	state := m.remoteMetrics
	if snippets {
		keys, state = []string{"stars", "forks", "comments"}, m.snippets.metrics
	}
	var parts []string
	var oldest time.Time
	failed := false
	for _, key := range keys {
		count := repoMetric(metrics, key)
		label := key
		if key == "prs" {
			label = "open PR/MR"
		} else if key == "issues" {
			label = "open issues"
		}
		parts = append(parts, label+" "+m.metricText(count))
		if count != nil {
			failed = failed || count.State == forgemetrics.StateError
			if count.Value != nil && !count.ObservedAt.IsZero() && (oldest.IsZero() || count.ObservedAt.Before(oldest)) {
				oldest = count.ObservedAt
			}
		}
	}
	width := max(1, m.width-4)
	lines := []string{"  " + fitCell(strings.Join(parts, " · "), width)}
	note := "? unknown · — unavailable · ~ stale"
	if !oldest.IsZero() {
		note = "observed " + oldest.Local().Format("2006-01-02 15:04") + " · " + note
	}
	if state.loading {
		note = "Loading statistics… " + note
	} else if failed || state.warning != "" {
		note = "Some statistics unavailable; r retries. " + note
	}
	return append(lines, "  "+fitCell(styleDim.Render(note), width))
}

func (m Model) sortableColumns() []string {
	if m.view == ViewRemote {
		if m.snippetsActive() {
			return []string{"forge", "snippet", "owner", "vis", "updated", "files", "stars", "forks", "comments"}
		}
		return []string{"forge", "repository", "vis", "updated", "local", "description", "stars", "forks", "issues", "prs"}
	}
	var keys []string
	for _, column := range m.tableHeader().columns {
		keys = append(keys, column.key)
	}
	return keys
}

type metricColumn struct {
	key   string
	width int
}

// The sort menu has all fields even when a terminal cannot fit their columns.
func (m Model) remoteColumns(snippets bool) []metricColumn {
	name := "repository"
	if snippets {
		name = "snippet"
	}
	columns := []metricColumn{{"forge", 7}, {name, 0}}
	if snippets {
		if m.width >= 64 {
			columns = append(columns, metricColumn{"owner", 14})
		}
		if m.width >= 48 {
			columns = append(columns, metricColumn{"vis", 9})
		}
		if m.width >= 80 {
			columns = append(columns, metricColumn{"files", 5})
		}
		if m.width >= 110 {
			columns = append(columns, metricColumn{"updated", 10}, metricColumn{"stars", 7}, metricColumn{"forks", 7}, metricColumn{"comments", 8})
		}
	} else {
		if m.width >= 48 {
			columns = append(columns, metricColumn{"vis", 9})
		}
		if m.width >= 64 {
			columns = append(columns, metricColumn{"updated", 10})
		}
		columns = append(columns, metricColumn{"local", 7})
		if m.width >= 100 {
			columns = append(columns, metricColumn{"stars", 7}, metricColumn{"issues", 7}, metricColumn{"prs", 7})
		}
		if m.width >= 120 {
			columns = append(columns, metricColumn{"forks", 7})
		}
		if m.width >= 160 {
			columns = append(columns, metricColumn{"description", 26})
		}
	}
	fixed := 2 + 2*(len(columns)-1)
	for _, column := range columns {
		fixed += column.width
	}
	columns[1].width = max(4, m.width-fixed-1)
	return columns
}

func renderMetricHeader(columns []metricColumn) string {
	var labels []string
	for _, column := range columns {
		labels = append(labels, fitCell(strings.ToUpper(column.key), column.width))
	}
	return styleHeader.Render("  "+strings.Join(labels, "  ")) + "\n"
}

func snippetFileCount(row SnippetRow) string {
	text := fmt.Sprint(len(row.Files))
	if !row.FilesComplete {
		text += "+"
	}
	return text
}

func (m Model) renderSnippetRows(rows []SnippetRow) string {
	columns := m.remoteColumns(true)
	var b strings.Builder
	b.WriteString(renderMetricHeader(columns))
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		row := rows[i]
		var values []string
		for _, column := range columns {
			value := ""
			switch column.key {
			case "forge":
				value = row.Provider
			case "snippet":
				value = row.Title
			case "owner":
				value = dashCell(row.Owner)
			case "vis":
				value = dashCell(row.Visibility)
			case "files":
				value = snippetFileCount(row)
			case "updated":
				value = "—"
				if !row.UpdatedAt.IsZero() {
					value = row.UpdatedAt.Format("2006-01-02")
				}
			default:
				value = m.metricText(repoMetric(row.Metrics, column.key))
			}
			values = append(values, fitCell(snippetDisplayText(value), column.width))
		}
		line := strings.Join(values, "  ")
		b.WriteString(m.renderLine(i, line, line))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}
