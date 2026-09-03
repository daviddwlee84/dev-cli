package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Zellij models a checkout as a named Zellij session. Zellij has no tmux-like
// session user options, so metadata annotation is intentionally a no-op.
type Zellij struct {
	bin        string
	runCommand func(context.Context, ...string) (string, error)
}

// NewZellij returns the zellij adapter.
func NewZellij() *Zellij { return &Zellij{bin: "zellij"} }

// Name implements Runtime.
func (z *Zellij) Name() string { return "zellij" }

// Available reports whether zellij is installed. Like tmux, creating a
// background session starts its server on demand.
func (z *Zellij) Available() bool { return haveBinary(z.bin) }

func (z *Zellij) run(ctx context.Context, args ...string) (string, error) {
	if z.runCommand != nil {
		return z.runCommand(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, z.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("zellij %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

var zellijLayoutCWD = regexp.MustCompile(`(?m)^\s*cwd\s+((?:"(?:\\.|[^"\\])*")|\S+)`)

// listSessionNames splits zellij's native listing into live sessions and
// exited-but-resurrectable ones. Zellij keeps an exited session in its
// namespace: the name still answers `attach` and still blocks a new session,
// but no server backs it, so every action command against it fails. dev must
// see both halves without ever calling a dead session live.
func (z *Zellij) listSessionNames(ctx context.Context) (live, exited []string, err error) {
	out, err := z.run(ctx, "list-sessions", "--no-formatting")
	if err != nil {
		if strings.Contains(err.Error(), "No active zellij sessions found") ||
			strings.Contains(err.Error(), "No active sessions") {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.Contains(line, "(EXITED - attach to resurrect)") {
			exited = append(exited, fields[0])
			continue
		}
		live = append(live, fields[0])
	}
	return live, exited, nil
}

// zellijSessionGone reports whether an action failed because the named session
// has no running server. Zellij's exit marker is the primary signal; this
// tolerates listings whose wording changes between releases.
func zellijSessionGone(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no active session")
}

// describe resolves each named session's root cwd. dump-layout is zellij's
// native read API; a session that dies between the listing and the dump is
// dropped rather than failing the whole listing.
func (z *Zellij) describe(ctx context.Context, names []string) ([]Session, error) {
	var sessions []Session
	for _, name := range names {
		layout, err := z.run(ctx, "--session", name, "action", "dump-layout")
		if err != nil {
			if zellijSessionGone(err) {
				continue
			}
			return nil, fmt.Errorf("inspect zellij session %s: %w", name, err)
		}
		dirs := []string(nil)
		if match := zellijLayoutCWD.FindStringSubmatch(layout); len(match) == 2 {
			cwd := match[1]
			if strings.HasPrefix(cwd, `"`) {
				if decoded, err := strconv.Unquote(cwd); err == nil {
					cwd = decoded
				}
			}
			if cwd != "" {
				dirs = []string{cwd}
			}
		}
		sessions = append(sessions, Session{Handle: name, Label: name, Dirs: dirs})
	}
	return sessions, nil
}

// List implements Runtime. Only live sessions are reported: an exited session
// covers no checkout and must not inflate the runtime view dev joins into its
// inventory.
func (z *Zellij) List(ctx context.Context) ([]Session, error) {
	live, _, err := z.listSessionNames(ctx)
	if err != nil {
		return nil, err
	}
	return z.describe(ctx, live)
}

// Open creates a detached Zellij session rooted at dir, or reuses the named
// session only when its native layout confirms the same directory.
func (z *Zellij) Open(ctx context.Context, dir, label string) (OpenResult, error) {
	name := SessionName(label)
	live, exited, err := z.listSessionNames(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	if slices.Contains(live, name) {
		sessions, err := z.describe(ctx, []string{name})
		if err != nil {
			return OpenResult{}, err
		}
		if len(sessions) == 0 {
			// The live name disappeared while its layout was inspected. Do not
			// fall through to attach --create-background: Zellij may retain the
			// exited layout and resurrect it at its old cwd. A fresh invocation
			// will list the stable live/exited state and choose safely.
			return OpenResult{}, fmt.Errorf("zellij session %s changed state while its cwd was inspected; retry", name)
		}
		session := sessions[0]
		if len(session.Dirs) == 0 {
			return OpenResult{}, fmt.Errorf("zellij session %s exists but its cwd is unavailable", name)
		}
		if !sameDirectory(session.Dirs[0], dir) {
			return OpenResult{}, fmt.Errorf("zellij session %s already exists at %s, not %s", name, session.Dirs[0], dir)
		}
		return OpenResult{Handle: name, Surface: "session", Opened: true}, nil
	}
	// An exited session still owns the name, and `attach --create-background`
	// would resurrect its old layout instead of creating one at dir. Fail
	// closed rather than hand back a session rooted somewhere else.
	if slices.Contains(exited, name) {
		return OpenResult{}, fmt.Errorf("zellij session %s exists but has exited; run `zellij delete-session %s` to reclaim the name or `zellij attach %s` to resurrect it", name, name, name)
	}
	if _, err := z.run(ctx, "attach", "--create-background", name, "options", "--default-cwd", dir); err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Handle: name, Surface: "session", Opened: true, Created: true}, nil
}

// Activate switches an existing Zellij client when supported (Zellij 0.44+),
// or attaches from an ordinary shell. Older Zellij releases have no exact
// inside-session switch command and return a clear upgrade error.
func (z *Zellij) Activate(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	if os.Getenv("ZELLIJ") != "" || os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		if _, err := z.run(ctx, "action", "switch-session", handle); err != nil {
			return fmt.Errorf("switch zellij session to %s (requires zellij 0.44 or newer): %w", handle, err)
		}
		return nil
	}
	return runInteractive(ctx, z.bin, "attach", handle)
}

// Close implements Runtime.
func (z *Zellij) Close(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	live, _, err := z.listSessionNames(ctx)
	if err != nil {
		return err
	}
	// Only a live session has a server to kill. An exited one is already
	// closed as far as dev is concerned, so a stale handle is a no-op.
	if slices.Contains(live, handle) {
		_, err := z.run(ctx, "kill-session", handle)
		return err
	}
	return nil
}

// Annotate implements Runtime. Zellij exposes no session metadata options.
func (z *Zellij) Annotate(context.Context, string, map[string]string) error { return nil }
