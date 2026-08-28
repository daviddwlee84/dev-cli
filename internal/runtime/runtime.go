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
	"os"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Pane is one terminal surface inside a runtime session.
type Pane struct {
	ID           string
	CWD          string
	ShellCWD     string
	Agent        string
	AgentStatus  string
	AgentSession string
}

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
	// Panes retain the exact runtime IDs and cwd values used for caller and
	// mixed-workspace retirement checks.
	Panes   []Pane
	Focused bool
}

// Covers reports whether the session has a pane rooted at or under dir.
func (s Session) Covers(dir string) bool {
	for _, d := range s.Dirs {
		// A pane inside the repository makes it live. The inverse is not
		// true: a generic pane at $HOME does not make every repository below
		// $HOME active.
		if inside, err := pathx.Contains(dir, d); err == nil && inside {
			return true
		}
	}
	return false
}

// OpenResult is the transient result of making a checkout live. Handle may be
// persisted by callers, but the pane belongs to this one creation response and
// must never be treated as durable state.
type OpenResult struct {
	Handle     string
	Surface    string
	Opened     bool
	Created    bool
	RootPaneID string
}

// AgentActivity is one recognized live coding agent occupying a runtime pane.
// Every activity returned by a backend is occupied regardless of Status; idle,
// done and unknown describe lifecycle state, not permission to share a checkout.
type AgentActivity struct {
	PaneID      string
	WorkspaceID string
	Agent       string
	Name        string
	Status      string
	CWD         string
}

// AgentActivityLister is an optional runtime capability. Core runtimes need not
// know about agents; Herdr implements it because its pane model can identify the
// exact process and checkout involved.
type AgentActivityLister interface {
	AgentActivities(ctx context.Context) ([]AgentActivity, error)
}

// CurrentPaneResolver is an optional runtime capability for resolving the live
// pane behind inherited caller context. Herdr pane IDs can change after a move,
// so collision exclusions must not trust HERDR_PANE_ID alone.
type CurrentPaneResolver interface {
	CurrentPaneID(ctx context.Context) (string, error)
}

// Activator is the optional interactive half of opening a runtime surface.
// Open deliberately remains detached so creation commands can run in the
// background. Navigation commands call Activate after their own terminal UI
// has been torn down: inside a multiplexer this switches the current client;
// outside it attaches a new client and blocks until the user detaches.
type Activator interface {
	Activate(ctx context.Context, handle string) error
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
	// Open makes dir live. Its result distinguishes a newly created surface from
	// a reused one and may carry a root pane proven by that same create response.
	Open(ctx context.Context, dir, label string) (OpenResult, error)
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
	OpenWorktree(ctx context.Context, path, label string) (OpenResult, error)
}

// ErrUnavailable reports a backend that is not usable on this machine.
type ErrUnavailable struct{ Backend, Reason string }

func (e *ErrUnavailable) Error() string {
	return fmt.Sprintf("runtime %s unavailable: %s", e.Backend, e.Reason)
}

// Select resolves a configured backend name to a Runtime.
//
// "auto" prefers the richest backend that is actually available: herdr (which
// models workspaces, worktrees and agent sessions), then tmux, then zellij,
// then None.
// None is always available so dev never hard-fails for lack of a multiplexer.
func Select(backend string) Runtime {
	herdr, tmux, zellij, none := NewHerdr(), NewTmux(), NewZellij(), None{}
	switch backend {
	case "herdr":
		return herdr
	case "tmux":
		return tmux
	case "zellij":
		return zellij
	case "none":
		return none
	case "", "auto":
		if herdr.Available() {
			return herdr
		}
		if tmux.Available() {
			return tmux
		}
		if zellij.Available() {
			return zellij
		}
		return none
	}
	return none
}

// All returns every backend, for `dev doctor` to report on.
func All() []Runtime { return []Runtime{NewHerdr(), NewTmux(), NewZellij(), None{}} }

// haveBinary reports whether name is on PATH.
func haveBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runInteractive connects a multiplexer client directly to dev's terminal.
// Runtime protocol calls use captured stdout/stderr; attach clients must not,
// because they own the terminal until the user detaches.
func runInteractive(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return nil
}
