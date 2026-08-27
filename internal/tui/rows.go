package tui

import (
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// RepoRow is one repository as the dashboard shows it: what it is, plus how
// much of it is currently in flight.
//
// The repository view exists because the task list alone answers only half the
// question. On a machine with forty repositories and no tasks recorded yet,
// a task-only dashboard is empty — and the honest answer to "what do I have
// here?" has to come before "what am I working on?"
type RepoRow struct {
	Repo   repo.Repo
	Status gitx.Status
	// Worktrees is the number of linked worktrees, excluding the main checkout.
	Worktrees int
	// Tasks are the recorded tasks belonging to this repository.
	Tasks []*task.Task
	// Live reports a runtime session sitting in this repository.
	Live bool
}

// HotTasks counts the repository's tasks that are currently hot.
func (r RepoRow) HotTasks() int {
	n := 0
	for _, t := range r.Tasks {
		if t.State == task.Hot {
			n++
		}
	}
	return n
}

// StateSummary renders the repository's tasks as a compact per-state tally,
// which is what makes the list scannable at forty repositories.
func (r RepoRow) StateSummary() string {
	if len(r.Tasks) == 0 {
		return ""
	}
	counts := map[task.State]int{}
	for _, t := range r.Tasks {
		counts[t.State]++
	}
	var parts []string
	for _, s := range task.States {
		if counts[s] > 0 {
			parts = append(parts, s.Icon()+strconv.Itoa(counts[s]))
		}
	}
	return strings.Join(parts, " ")
}

// searchText is what a filter query matches against.
func (r RepoRow) searchText() string {
	var b strings.Builder
	b.WriteString(r.Repo.Display())
	b.WriteString(" ")
	b.WriteString(r.Status.Branch)
	for _, t := range r.Tasks {
		b.WriteString(" ")
		b.WriteString(t.Title())
		b.WriteString(" ")
		b.WriteString(t.Branch)
	}
	return strings.ToLower(b.String())
}

// taskSearchText is what a filter query matches a task row against.
func taskSearchText(r inventory.Row) string {
	t := r.Task
	return strings.ToLower(strings.Join([]string{
		t.Title(), t.Repo, t.Branch, t.Next, t.Note, string(t.State),
	}, " "))
}

// matches reports whether every whitespace-separated term of the query appears
// in the haystack.
//
// Term-wise rather than substring so "auth api" finds a task named "api token
// auth" — the order words come to mind in is rarely the order they appear in.
func matches(haystack, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, term := range strings.Fields(query) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}
