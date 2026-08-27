// Package task stores the small amount of state git cannot answer: which
// change streams exist, what state each is in, which machine owns it, and what
// the next action is. Everything else — branch, dirty, ahead/behind, whether a
// worktree exists — is derived live and deliberately not stored here.
package task

import (
	"fmt"
	"strings"
	"time"
)

// State is where a change stream sits in its lifecycle.
type State string

const (
	// Hot: worktree and branch exist, a runtime session is open, actively worked on.
	Hot State = "hot"
	// Warm: worktree and branch kept, runtime closed. Back within days.
	Warm State = "warm"
	// Cold: committed and pushed, worktree removed. Reconstructible anywhere.
	Cold State = "cold"
	// Done: merged. Kept only until the entry is reaped.
	Done State = "done"
)

// States lists the lifecycle in order, for help text and validation.
var States = []State{Hot, Warm, Cold, Done}

// ParseState validates a user-supplied state name.
func ParseState(s string) (State, error) {
	got := State(strings.ToLower(strings.TrimSpace(s)))
	for _, valid := range States {
		if got == valid {
			return got, nil
		}
	}
	return "", fmt.Errorf("unknown state %q: want one of %s", s, JoinStates(", "))
}

// JoinStates renders the valid state names.
func JoinStates(sep string) string {
	parts := make([]string, len(States))
	for i, s := range States {
		parts[i] = string(s)
	}
	return strings.Join(parts, sep)
}

// Icon is the glyph shown in the inventory.
func (s State) Icon() string {
	switch s {
	case Hot:
		return "🔥"
	case Warm:
		return "🌤"
	case Cold:
		return "❄️"
	case Done:
		return "✅"
	}
	return "?"
}

// Label is the uppercase column form.
func (s State) Label() string { return strings.ToUpper(string(s)) }

// Task is one change stream. Field names are the TOML keys.
type Task struct {
	// ID is derived from repo and branch; it is the filename stem and is not
	// stored inside the file.
	ID string `toml:"-"`

	// Name is the human label ("atp security recovery").
	Name string `toml:"name"`
	// Repo is the repository's display name.
	Repo string `toml:"repo"`
	// RepoPath is the main checkout, the directory git commands run in.
	RepoPath string `toml:"repo_path"`
	// Branch is the change stream's identity — the one durable handle that
	// survives every machine, worktree and runtime.
	Branch string `toml:"branch"`
	// Base is the ref the branch was created from, recorded so `dev done` can
	// integrate without re-guessing.
	Base string `toml:"base"`
	// WorktreePath is the linked checkout, empty when the task works directly
	// in the main checkout or has gone cold.
	WorktreePath string `toml:"worktree_path"`

	State State `toml:"state"`
	// Owner is the hostname that last held this task hot. One writer per
	// branch at a time avoids two machines diverging the same history.
	Owner string `toml:"owner"`
	// Next is the single most valuable field in the file: what to do when you
	// come back. Without it, parking a task still loses the thread.
	Next string   `toml:"next"`
	Note string   `toml:"note"`
	Tags []string `toml:"tags"`

	// AgentSession records a resumable agent context ("claude:<uuid>"), so a
	// parked task can be resumed with its conversation intact.
	AgentSession string `toml:"agent_session"`
	// RuntimeHandle is the last known runtime session id (e.g. a herdr
	// workspace id). Advisory only — always re-resolved against the live
	// runtime before use.
	RuntimeHandle string `toml:"runtime_handle"`

	Created time.Time `toml:"created"`
	Updated time.Time `toml:"updated"`
}

// Title is the display name, falling back to the branch when unnamed.
func (t Task) Title() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Branch
}

// OwnedBy reports whether host is this task's owner. An empty owner means
// unclaimed, which counts as owned by anyone.
func (t Task) OwnedBy(host string) bool { return t.Owner == "" || t.Owner == host }

// Validate rejects a task that could not be acted on later.
func (t Task) Validate() error {
	switch {
	case t.Repo == "":
		return fmt.Errorf("task %s: repo is required", t.ID)
	case t.Branch == "":
		return fmt.Errorf("task %s: branch is required", t.ID)
	case t.RepoPath == "":
		return fmt.Errorf("task %s: repo_path is required", t.ID)
	}
	if _, err := ParseState(string(t.State)); err != nil {
		return fmt.Errorf("task %s: %w", t.ID, err)
	}
	return nil
}
