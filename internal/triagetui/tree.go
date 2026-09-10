package triagetui

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/triage"
)

type treeRow struct {
	key, parent string
	item        triage.Item
	members     []triage.Item
	child       bool
}

func groupKey(i triage.Item) string {
	if i.Kind == "try" {
		if i.CatalogID != "" {
			return "try:" + i.CatalogID
		}
		return "try:" + i.RepositoryPath
	}
	return "repo:" + i.RepositoryID
}
func (m Model) rowKey() string {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return m.rows[m.cursor].key
	}
	return ""
}
func (m *Model) focusRow(key string) {
	for n, r := range m.rows {
		if r.key == key {
			m.cursor = n
			return
		}
	}
	m.cursor = min(max(0, len(m.rows)-1), m.cursor)
}
func (m *Model) filterRows() {
	m.visible = nil
	m.rows = nil
	groups := map[string][]triage.Item{}
	keys := []string{}
	items := append([]triage.Item{}, m.report.Items...)
	triage.SortItems(items, false)
	for _, i := range items {
		if (!m.deferred && i.Deferred != "") || (m.kind != "all" && i.Kind != m.kind) {
			continue
		}
		text := i.Name + " " + i.Path + " " + i.Note + " " + strings.Join(i.Tags, " ")
		if i.Branch != nil {
			text += " " + i.Branch.Ref
		}
		for _, f := range i.Findings {
			text += " " + f.Code + " " + f.Detail
		}
		if !strings.Contains(strings.ToLower(text), strings.ToLower(m.filter)) {
			continue
		}
		m.visible = append(m.visible, i)
		key := groupKey(i)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], i)
	}
	for _, key := range keys {
		members := groups[key]
		representative := members[0]
		for _, i := range members {
			if i.Scope == "checkout" && i.Path == i.RepositoryPath {
				representative = i
				break
			}
		}
		parent := treeRow{key: key, item: representative, members: members}
		m.rows = append(m.rows, parent)
		if m.expanded[key] {
			for _, i := range members {
				if i.Scope == "repository" {
					continue
				}
				m.rows = append(m.rows, treeRow{key: i.ID, parent: key, item: i, members: []triage.Item{i}, child: true})
			}
		}
	}
	m.cursor = min(max(0, len(m.rows)-1), m.cursor)
}
func (m Model) selectionState(items []triage.Item) string {
	count := 0
	for _, i := range items {
		if m.selected[i.ID] {
			count++
		}
	}
	if count == 0 {
		return "[ ]"
	}
	if count == len(items) {
		return "[x]"
	}
	return "[-]"
}
func (m *Model) toggleRow(n int) {
	if n < 0 || n >= len(m.rows) {
		return
	}
	m.selected = maps.Clone(m.selected)
	items := m.rows[n].members
	selectAll := m.selectionState(items) != "[x]"
	for _, i := range items {
		if selectAll {
			m.selected[i.ID] = true
		} else {
			delete(m.selected, i.ID)
		}
	}
}
func (m *Model) toggleVisible() {
	m.selected = maps.Clone(m.selected)
	selectAll := m.selectionState(m.visible) != "[x]"
	for _, i := range m.visible {
		if selectAll {
			m.selected[i.ID] = true
		} else {
			delete(m.selected, i.ID)
		}
	}
}
func (m *Model) expandCurrent(expand bool) {
	if len(m.rows) == 0 {
		return
	}
	row := m.rows[m.cursor]
	key := row.key
	if row.child {
		key = row.parent
	}
	m.expanded = maps.Clone(m.expanded)
	m.expanded[key] = expand
	m.filterRows()
	m.focusRow(key)
}
func (m Model) selectionCounts() (selected, hidden int) {
	for _, yes := range m.selected {
		if yes {
			selected++
		}
	}
	visible := 0
	for _, i := range m.visible {
		if m.selected[i.ID] {
			visible++
		}
	}
	return selected, selected - visible
}

type actionChoice struct {
	name                string
	candidates, blocked int
}

func (m Model) choices() []actionChoice {
	counts := map[string]actionChoice{}
	for _, item := range m.menuItems {
		seen := map[string]bool{}
		for _, a := range item.Actions {
			if seen[a.Name] {
				continue
			}
			seen[a.Name] = true
			c := counts[a.Name]
			c.name = a.Name
			if a.Availability == "candidate" {
				c.candidates++
			} else {
				c.blocked++
			}
			counts[a.Name] = c
		}
	}
	out := []actionChoice{}
	for _, c := range counts {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
func actionLabel(name string) string {
	labels := map[string]string{"fetch": "Fetch remote refs", "push": "Push commits", "fast-forward": "Update checkout (fast-forward)", "park-warm": "Park tasks warm", "park-cold": "Park tasks cold", "retire": "Retire completed tasks", "remove-checkout": "Remove linked checkout", "trash-try": "Move Try to Trash", "forget-try": "Forget missing Try entry"}
	if s := labels[name]; s != "" {
		return s
	}
	return name
}
func actionHint(name string) string {
	switch name {
	case "fetch":
		return "Network action; review push/update in a separate round after fetching."
	case "push":
		return "Push the reviewed commit/ref only; no force push or automatic rebase."
	case "trash-try":
		return "Preserve the whole folder in system Trash, including ignored files."
	case "forget-try":
		return "Remove unreferenced catalog metadata for a confirmed missing folder."
	default:
		return "Review exact targets, effects and blockers before approval."
	}
}
func rowName(r treeRow) string {
	if r.child {
		if r.item.Branch != nil {
			return strings.TrimPrefix(r.item.Branch.Ref, "refs/heads/") + " · " + r.item.Scope
		}
		return r.item.Scope
	}
	return r.item.Name
}
func facts(items []triage.Item) []string {
	unsaved, sync, occupied, unknown := 0, 0, 0, 0
	missing := false
	for _, i := range items {
		if i.Scope == "repository" {
			continue
		}
		u, s, o, x := false, false, false, !i.Complete
		for _, f := range i.Findings {
			switch f.Code {
			case "uncommitted":
				u = true
			case "ahead", "behind", "diverged", "no-upstream", "upstream-unavailable":
				s = true
			case "agent-occupied", "runtime-unclaimed":
				o = true
			case "missing":
				missing = true
			}
		}
		if u {
			unsaved++
		}
		if s {
			sync++
		}
		if o {
			occupied++
		}
		if x {
			unknown++
		}
	}
	out := []string{}
	if unsaved > 0 {
		out = append(out, fmt.Sprintf("! %d unsaved", unsaved))
	}
	if sync > 0 {
		out = append(out, fmt.Sprintf("↕ %d sync", sync))
	}
	if occupied > 0 {
		out = append(out, fmt.Sprintf("● %d in use", occupied))
	}
	if missing {
		out = append(out, "? missing")
	}
	if unknown > 0 {
		out = append(out, fmt.Sprintf("? %d inspect", unknown))
	}
	if len(out) == 0 {
		out = append(out, "review local work")
	}
	return out
}
func details(items []triage.Item) []string {
	lines := []string{}
	for _, i := range items {
		if i.Scope == "repository" && len(items) > 1 {
			continue
		}
		label := i.Scope + " · " + i.Path
		if i.Branch != nil {
			label = strings.TrimPrefix(i.Branch.Ref, "refs/heads/") + " · " + i.Path
		}
		lines = append(lines, label)
		if i.Note != "" {
			lines = append(lines, "  note: "+i.Note)
		}
		for _, f := range i.Findings {
			lines = append(lines, "  "+f.Code+": "+f.Detail)
		}
		for _, a := range i.Actions {
			line := "  " + actionLabel(a.Name) + " [" + a.Availability + "]"
			if a.Reason != "" {
				line += " " + a.Reason
			}
			lines = append(lines, line)
		}
	}
	return lines
}
func (m *Model) showDetails() { m.overlay = details(m.currentMembers()); m.scroll = 0 }
func (m *Model) showResultDetails() {
	if m.lastLedger == nil || m.resultCursor >= len(m.lastLedger.Outcomes) {
		return
	}
	o := m.lastLedger.Outcomes[m.resultCursor]
	m.overlay = []string{actionLabel(o.Action) + " · " + o.Status, o.Path}
	found := false
	for _, step := range o.Steps {
		if d := step.Diagnostic; d != nil {
			found = true
			m.overlay = append(m.overlay, d.Summary, fmt.Sprintf("Diagnostic: %s · exit %d", d.Code, d.ExitCode), d.Next)
			m.overlay = append(m.overlay, strings.Split(d.Details, "\n")...)
		} else {
			m.overlay = append(m.overlay, step.Effect.Description)
			if step.Detail != "" {
				m.overlay = append(m.overlay, step.Detail)
			}
		}
	}
	if !found && o.Error != "" {
		m.overlay = append(m.overlay, o.Error, "Detailed diagnostic was not recorded for this result.")
	}
	if o.CatalogID != "" {
		m.overlay = append(m.overlay, "Catalog: "+o.CatalogID)
	}
	if o.OperationRecord != "" {
		m.overlay = append(m.overlay, "Operation record: "+o.OperationRecord)
	}
	m.overlay = append(m.overlay, "Receipt: "+m.lastLedger.Path)
	m.scroll = 0
}
func helpLines() []string {
	return []string{"Select items → Choose action → Preview → Results", "Selection: Space / checkbox toggles; Ctrl+A selects or clears visible targets", "Collapsed repo selection includes its branches and registered checkouts", "Expand: left/right arrows or the disclosure marker", "Enter shows details. Tab focuses the Choose action button; Ctrl+O opens actions", "Click a row to focus. Right click opens its explicit action scope", "Filters: / text · g repo/Try · d deferred", "Review intent: L keep local · s snooze · U clear", "R edits disposable-directory policy; it never authorizes undeclared deletion", "Individual tools: o flow · e shell · v runtime", "Results: Enter details · e shell · Ctrl+O new action · r refresh", "Refresh never fetches. New actions and retries always need a new preview", "Esc returns; q quits. During Apply, Esc stops after the current operation"}
}
