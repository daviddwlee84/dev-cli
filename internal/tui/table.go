package tui

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

type tableSort struct {
	column     string
	descending bool
}
type columnHit struct {
	key      string
	from, to int
}
type tableHeader struct {
	line    int
	text    string
	columns []columnHit
}

var headerWords = regexp.MustCompile(`[A-Z][A-Z0-9_]*`)

// Derive hit cells from the exact undecorated header emitted by each renderer,
// including its responsive padding and loading preamble. This shares geometry
// without maintaining a second set of width calculations for mouse handling.
func (m Model) headerFrom(raw string) tableHeader {
	h := tableHeader{line: m.listPreambleLines() - 1}
	lines := strings.Split(raw, "\n")
	if m.count() == 0 || h.line >= len(lines) {
		return h
	}
	h.text = ansi.Strip(lines[h.line])
	spans := headerWords.FindAllStringIndex(h.text, -1)
	for n, span := range spans {
		end := len(h.text)
		if n+1 < len(spans) {
			end = spans[n+1][0] - 2
		}
		h.columns = append(h.columns, columnHit{strings.ToLower(h.text[span[0]:span[1]]), span[0], max(span[1], end)})
	}
	return h
}
func (m Model) tableHeader() tableHeader { return m.headerFrom(m.renderRawList()) }
func (m Model) decorateTable(raw string) string {
	h := m.headerFrom(raw)
	order := m.tableSorts[int(m.view)]
	if order.column == "" || len(h.columns) == 0 {
		return raw
	}
	line := h.text
	for _, c := range h.columns {
		if c.key == order.column {
			arrow := "↑"
			if order.descending {
				arrow = "↓"
			}
			label := fitCell(strings.ToUpper(c.key)+" "+arrow, c.to-c.from)
			line = line[:c.from] + label + line[c.to:]
			break
		}
	}
	lines := strings.Split(raw, "\n")
	lines[h.line] = styleHeader.Render(line)
	return strings.Join(lines, "\n")
}
func (m Model) currentToken() selectionToken { t, _ := m.currentSelectionToken(); return t }
func (m Model) cycleTableSort(column string) (tea.Model, tea.Cmd) {
	m.stopStartupFocus()
	focus, ok := m.currentSelectionToken()
	s := m.tableSorts[int(m.view)]
	switch {
	case s.column != column:
		s = tableSort{column: column}
	case !s.descending:
		s.descending = true
	default:
		s = tableSort{}
	}
	m.tableSorts[int(m.view)] = s
	m.statusSeverity = "info"
	m.status = "Default ordering"
	if s.column != "" {
		direction := "ascending"
		if s.descending {
			direction = "descending"
		}
		m.status = fmt.Sprintf("Sort %s: %s", strings.ToUpper(column), direction)
	}
	if ok {
		m.selectToken(focus)
	}
	return m, nil
}

type sortCell struct {
	known bool
	text  string
	nums  []int64
}

func textCell(s string) sortCell {
	return sortCell{known: s != "" && s != "—", text: strings.ToLower(s)}
}
func numberCell(n ...int64) sortCell { return sortCell{known: true, nums: n} }
func timeCell(t time.Time) sortCell {
	if t.IsZero() {
		return sortCell{}
	}
	return numberCell(t.UnixNano())
}
func gitSortCell(s gitx.Status) sortCell {
	return numberCell(int64(s.Conflicted), int64(s.Changed), int64(s.Ahead+s.Behind), int64(s.Ahead), int64(s.Behind))
}
func boolNumber(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
func applyColumnSort[T any](m Model, rows []T, project func(T, string) sortCell) []T {
	s := m.tableSorts[int(m.view)]
	if s.column == "" {
		return rows
	}
	slices.SortStableFunc(rows, func(a, b T) int {
		x, y := project(a, s.column), project(b, s.column)
		if x.known != y.known {
			if x.known {
				return -1
			}
			return 1
		}
		if !x.known {
			return 0
		}
		c := 0
		if x.nums != nil || y.nums != nil {
			c = slices.Compare(x.nums, y.nums)
		} else {
			c = cmp.Compare(x.text, y.text)
		}
		if s.descending {
			return -c
		}
		return c
	})
	return rows
}
func taskCell(r inventory.Row, key string) sortCell {
	if r.Task == nil {
		return sortCell{}
	}
	switch key {
	case "task":
		return textCell(r.Task.Title())
	case "state":
		return textCell(string(r.Task.State))
	case "repo":
		return textCell(r.Task.Repo)
	case "branch":
		return textCell(r.Task.Branch)
	case "age":
		return timeCell(r.Task.Updated)
	case "next":
		return textCell(r.Task.Next)
	case "git":
		if r.StatusErr != nil {
			return sortCell{}
		}
		return gitSortCell(r.Status)
	}
	return sortCell{}
}
func repoCell(r RepoRow, key string) sortCell {
	switch key {
	case "repo":
		return textCell(r.Repo.Display())
	case "branch":
		return textCell(r.Status.Branch)
	case "git":
		if r.Context.IdentityErr != nil || r.Context.WorktreeErr != nil {
			return sortCell{}
		}
		return gitSortCell(r.Status)
	case "size":
		if r.Usage != nil && r.Usage.Complete {
			return numberCell(r.Usage.OwnedBytes)
		}
	case "live":
		if r.Context.RuntimeErr != nil {
			return sortCell{}
		}
		return textCell(parentLiveColumn(r))
	case "latest":
		return timeCell(r.LastActivity)
	case "wt":
		if r.Context.WorktreeErr == nil {
			return numberCell(int64(r.Worktrees))
		}
	case "tasks":
		return numberCell(int64(len(r.Tasks)))
	case "notes":
		return numberCell(int64(r.NoteCount))
	case "category":
		return textCell(r.Repo.Category)
	case "path":
		return textCell(r.Repo.Path)
	case "remote":
		if r.TopologyErr == nil {
			return textCell(r.Topology.Summary())
		}
	}
	return sortCell{}
}
func tryCell(r TryRow, key string) sortCell {
	switch key {
	case "try":
		return textCell(r.Item.DisplayName())
	case "phase":
		return textCell(string(r.Item.Phase))
	case "where":
		return textCell(r.Where())
	case "git":
		if r.Item.Live.Status != nil && r.Item.Live.StatusError == nil {
			return gitSortCell(*r.Item.Live.Status)
		}
	case "last":
		return timeCell(r.Item.Activity())
	case "size":
		if r.Usage != nil && r.Usage.Complete {
			return numberCell(r.Usage.OwnedBytes)
		}
	case "tags":
		return textCell(strings.Join(r.Item.Tags, ","))
	}
	return sortCell{}
}
func fleetCell(r FleetRow, key string) sortCell {
	if key == "host" {
		return textCell(r.Host)
	}
	if key == "state" {
		if r.FromCache {
			return textCell("stale")
		}
		return textCell(string(r.State))
	}
	if r.Repository == nil {
		return sortCell{}
	}
	v := r.Repository
	switch key {
	case "repo":
		return textCell(v.Display)
	case "branch":
		return textCell(v.Branch)
	case "git":
		return gitSortCell(v.Status)
	case "live":
		return textCell(v.Runtime + " " + v.AgentStatus)
	case "tasks":
		return numberCell(int64(v.Tasks.Hot + v.Tasks.Warm + v.Tasks.Cold + v.Tasks.Done))
	case "path":
		return textCell(v.Path)
	}
	return sortCell{}
}
func remoteCell(r RemoteRow, key string) sortCell {
	switch key {
	case "forge":
		return textCell(string(r.Repo.Forge))
	case "repository":
		return textCell(r.Repo.FullName)
	case "vis":
		return textCell(string(r.Repo.Visibility))
	case "updated":
		return timeCell(r.Repo.UpdatedAt)
	case "local":
		return numberCell(boolNumber(r.Cloned()))
	case "description":
		return textCell(r.Repo.Description)
	}
	return sortCell{}
}
func skillCell(r agentskill.Skill, key string) sortCell {
	switch key {
	case "repo":
		return textCell(r.Repository)
	case "scope":
		return textCell(string(r.Scope))
	case "skill":
		return textCell(r.Name)
	case "install":
		return textCell(skillInstallState(r))
	case "update":
		return textCell(skillUpdateLabel(r.UpdateStatus))
	case "agents":
		return textCell(skillAgentSummary(r.Agents))
	case "source":
		return textCell(skillSource(r))
	}
	return sortCell{}
}
func mcpCell(r agentmcp.Declaration, key string) sortCell {
	switch key {
	case "repo":
		return textCell(r.Repository)
	case "scope":
		return textCell(string(r.Scope))
	case "agent":
		return textCell(string(r.Agent))
	case "server":
		return textCell(r.Name)
	case "transport":
		return textCell(string(r.Transport))
	case "state":
		return textCell(mcpDeclarationState(r))
	case "source":
		return textCell(mcpSource(r))
	}
	return sortCell{}
}

func triageReceipt(l triage.Ledger) string {
	summary, _ := triage.SummarizeLedger(l)
	lines := []string{summary}
	for _, o := range l.Outcomes {
		lines = append(lines, fmt.Sprintf("[%s] %s · %s", o.Status, o.Action, o.Path))
		if o.Error != "" {
			lines = append(lines, "  "+o.Error)
		}
	}
	lines = append(lines, "Receipt: "+l.Path)
	return strings.Join(lines, "\n")
}

func (m Model) currentStatusText() string {
	if m.err != nil {
		return m.err.Error()
	}
	if err := m.viewError(m.view); err != nil {
		return err.Error()
	}
	if m.status != "" {
		return m.status
	}
	return m.viewStatus(m.view)
}
