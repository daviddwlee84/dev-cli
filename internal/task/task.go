// Package task stores the small amount of state git cannot answer: which
// change streams exist, what state each is in, which machine owns it, and what
// the next action is. Everything else — branch, dirty, ahead/behind, whether a
// worktree exists — is derived live and deliberately not stored here.
package task

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

// CheckoutMode is how a task occupies the repository filesystem.
type CheckoutMode string

const (
	// ModeWorktree owns a linked worktree and branch.
	ModeWorktree CheckoutMode = "worktree"
	// ModeBranch uses a short-lived branch in the canonical checkout.
	ModeBranch CheckoutMode = "branch"
	// ModeDirect tracks ad-hoc work on the branch already checked out in the
	// canonical repo — usually main — without creating a branch or worktree.
	ModeDirect CheckoutMode = "direct"
)

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
	// Mode says whether this task uses a worktree, a branch in the canonical
	// checkout, or the current branch directly. Older entries infer it.
	Mode CheckoutMode `toml:"mode"`

	State State `toml:"state"`
	// Owner is the hostname that last held this task hot. One writer per
	// branch at a time avoids two machines diverging the same history.
	Owner string `toml:"owner"`
	// Next is the single most valuable field in the file: what to do when you
	// come back. Without it, parking a task still loses the thread.
	Next string   `toml:"next"`
	Note string   `toml:"note"`
	Tags []string `toml:"tags"`

	// AgentSession reserves a resumable agent context ("claude:<uuid>"). The
	// production start/park/resume flow does not yet capture or attach it.
	AgentSession string `toml:"agent_session"`
	// RuntimeHandle is the last known runtime session id (e.g. a Herdr
	// workspace id). RuntimeName records which backend owns that opaque handle.
	// Both are advisory and must be revalidated against live checkout coverage.
	RuntimeHandle string `toml:"runtime_handle"`
	RuntimeName   string `toml:"runtime_name"`

	Created time.Time `toml:"created"`
	Updated time.Time `toml:"updated"`

	// storedRevision is populated only by Store after a successful read/write.
	// It never enters TOML or Revision and lets legacy Save callers participate
	// in compare-and-swap without adding a persisted field older binaries erase.
	storedRevision string
}

// Revision returns a deterministic fingerprint of the task identity and every
// field represented by its TOML record. It is deliberately computed rather
// than persisted: older dev binaries can continue to read and rewrite task
// files without erasing a revision field they do not know about.
func (t Task) Revision() string {
	hash := sha256.New()
	writeString := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	writeTime := func(value time.Time) {
		writeString(value.UTC().Format(time.RFC3339Nano))
	}

	writeString("dev-task-revision-v1")
	writeString(t.ID)
	writeString(t.Name)
	writeString(t.Repo)
	writeString(t.RepoPath)
	writeString(t.Branch)
	writeString(t.Base)
	writeString(t.WorktreePath)
	writeString(string(t.Mode))
	writeString(string(t.State))
	writeString(t.Owner)
	writeString(t.Next)
	writeString(t.Note)
	writeString(fmt.Sprint(len(t.Tags)))
	for _, tag := range t.Tags {
		writeString(tag)
	}
	writeString(t.AgentSession)
	writeString(t.RuntimeHandle)
	writeString(t.RuntimeName)
	writeTime(t.Created)
	writeTime(t.Updated)

	return hex.EncodeToString(hash.Sum(nil))
}

// EffectiveMode returns the explicit mode, or infers a legacy task. Before
// modes were recorded, a path meant worktree and no path meant a canonical
// branch task; direct mode is never guessed because it changes integration
// semantics and must be explicit.
func (t Task) EffectiveMode() CheckoutMode {
	if t.Mode != "" {
		return t.Mode
	}
	if t.WorktreePath != "" {
		return ModeWorktree
	}
	return ModeBranch
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
	if t.ID != "" {
		if err := ValidateID(t.ID); err != nil {
			return fmt.Errorf("task ID: %w", err)
		}
	}
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
	switch t.Mode {
	case "", ModeWorktree, ModeBranch, ModeDirect:
	default:
		return fmt.Errorf("task %s: unknown mode %q", t.ID, t.Mode)
	}
	return nil
}
