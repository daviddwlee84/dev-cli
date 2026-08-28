package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

// List implements Runtime. Zellij's short session list contains names only;
// dump-layout is its native read API for each session's root cwd.
func (z *Zellij) List(ctx context.Context) ([]Session, error) {
	out, err := z.run(ctx, "list-sessions", "--short", "--no-formatting")
	if err != nil {
		if strings.Contains(err.Error(), "No active zellij sessions found") ||
			strings.Contains(err.Error(), "No active sessions") {
			return nil, nil
		}
		return nil, err
	}
	var sessions []Session
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		layout, err := z.run(ctx, "--session", name, "action", "dump-layout")
		if err != nil {
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

// Open creates a detached Zellij session rooted at dir, or reuses the named
// session only when its native layout confirms the same directory.
func (z *Zellij) Open(ctx context.Context, dir, label string) (OpenResult, error) {
	name := SessionName(label)
	sessions, err := z.List(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	for _, session := range sessions {
		if session.Handle != name {
			continue
		}
		if len(session.Dirs) == 0 {
			return OpenResult{}, fmt.Errorf("zellij session %s exists but its cwd is unavailable", name)
		}
		if !sameDirectory(session.Dirs[0], dir) {
			return OpenResult{}, fmt.Errorf("zellij session %s already exists at %s, not %s", name, session.Dirs[0], dir)
		}
		return OpenResult{Handle: name, Surface: "session", Opened: true}, nil
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
	sessions, err := z.List(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.Handle == handle {
			_, err := z.run(ctx, "kill-session", handle)
			return err
		}
	}
	return nil
}

// Annotate implements Runtime. Zellij exposes no session metadata options.
func (z *Zellij) Annotate(context.Context, string, map[string]string) error { return nil }
