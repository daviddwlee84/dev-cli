// Package runtime abstracts the terminal multiplexer that hosts a checkout.
//
// dev deliberately owns no terminal state of its own. A runtime is where a
// task is *live*; the durable facts live in git and in the task registry. That
// separation is the whole point: closing a runtime session must never feel
// like abandoning a task, so every adapter here is expected to be lossy and
// re-derivable.
package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Session is one live workspace/session of a runtime backend.
type Session struct {
	// Handle is the backend's own identifier (a herdr workspace id, a tmux
	// session name). Opaque to the rest of dev.
	Handle string
	Label  string
	// Dirs lists the working directories observed in this session. A session
	// can span several (multiple panes), which is why this is not a single
	// value.
	Dirs []string
	// AgentStatus is the backend's view of agent activity, when it has one:
	// "working", "idle", "done", "waiting". Empty when unsupported.
	AgentStatus string
	// AgentSessions lists resumable agent context ids, e.g. "claude:<uuid>".
	AgentSessions []string
	Focused       bool
}

// Covers reports whether the session has a pane rooted at or under dir.
func (s Session) Covers(dir string) bool {
	for _, d := range s.Dirs {
		if d == dir || strings.HasPrefix(d, dir+"/") || strings.HasPrefix(dir, d+"/") {
			return true
		}
	}
	return false
}

// Runtime is the contract every backend satisfies. Adapters must degrade
// gracefully: an unavailable backend returns errors rather than panicking, and
// List on an idle backend returns an empty slice, not an error.
type Runtime interface {
	// Name is the backend identifier used in config and in `dev doctor`.
	Name() string
	// Available reports whether this backend can be used on this machine right
	// now (binary installed and, where relevant, a server reachable).
	Available() bool
	// Open makes dir live and returns a handle. Opening something already open
	// focuses it rather than duplicating it.
	Open(ctx context.Context, dir, label string) (string, error)
	// Close ends the session without touching the checkout, the branch or the
	// task entry.
	Close(ctx context.Context, handle string) error
	// List enumerates live sessions.
	List(ctx context.Context) ([]Session, error)
	// Annotate attaches display-only metadata to a session (state, next
	// action). Backends without a metadata channel implement this as a no-op.
	Annotate(ctx context.Context, handle string, kv map[string]string) error
}

// WorktreeOpener is implemented by backends that understand git worktrees and
// can present one as a first-class session grouped under its parent repo.
// dev always creates the worktree itself — this only surfaces it.
type WorktreeOpener interface {
	OpenWorktree(ctx context.Context, path, label string) (string, error)
}

// ErrUnavailable reports a backend that is not usable on this machine.
type ErrUnavailable struct{ Backend, Reason string }

func (e *ErrUnavailable) Error() string {
	return fmt.Sprintf("runtime %s unavailable: %s", e.Backend, e.Reason)
}

// Select resolves a configured backend name to a Runtime.
//
// "auto" prefers the richest backend that is actually available: herdr (which
// models workspaces, worktrees and agent sessions), then tmux, then None.
// None is always available so dev never hard-fails for lack of a multiplexer.
func Select(backend string) Runtime {
	herdr, tmux, none := NewHerdr(), NewTmux(), None{}
	switch backend {
	case "herdr":
		return herdr
	case "tmux":
		return tmux
	case "none":
		return none
	case "", "auto":
		if herdr.Available() {
			return herdr
		}
		if tmux.Available() {
			return tmux
		}
		return none
	}
	return none
}

// All returns every backend, for `dev doctor` to report on.
func All() []Runtime { return []Runtime{NewHerdr(), NewTmux(), None{}} }

// haveBinary reports whether name is on PATH.
func haveBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
