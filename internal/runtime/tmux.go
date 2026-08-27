package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Tmux is the fallback backend for machines without herdr. It models a task as
// a named tmux session rooted at the checkout.
type Tmux struct{ bin string }

// NewTmux returns the tmux adapter.
func NewTmux() *Tmux { return &Tmux{bin: "tmux"} }

// Name implements Runtime.
func (t *Tmux) Name() string { return "tmux" }

// Available reports whether tmux is installed. Unlike herdr, tmux needs no
// running server for `new-session -d` to work, so presence is enough.
func (t *Tmux) Available() bool { return haveBinary(t.bin) }

func (t *Tmux) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, t.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// tmuxUnsafe covers the characters tmux treats specially in session names:
// a "." or ":" would make the name ambiguous with window/pane addressing.
var tmuxUnsafe = regexp.MustCompile(`[.:\s]+`)

// SessionName converts a task label into a legal tmux session name.
func SessionName(label string) string {
	s := tmuxUnsafe.ReplaceAllString(label, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "dev"
	}
	return s
}

// List implements Runtime. An absent server means "no sessions", not an error.
func (t *Tmux) List(ctx context.Context) ([]Session, error) {
	out, err := t.run(ctx, "list-sessions", "-F", "#{session_name}\t#{session_path}\t#{?session_attached,1,0}")
	if err != nil {
		// "no server running on ..." is the normal idle case.
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		s := Session{Handle: f[0], Label: f[0], Dirs: []string{f[1]}}
		if len(f) > 2 {
			s.Focused = f[2] == "1"
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// Open implements Runtime, reusing a session that already exists under that
// name rather than creating a duplicate.
func (t *Tmux) Open(ctx context.Context, dir, label string) (string, error) {
	name := SessionName(label)
	if _, err := t.run(ctx, "has-session", "-t", "="+name); err == nil {
		return name, nil
	}
	if _, err := t.run(ctx, "new-session", "-d", "-s", name, "-c", dir); err != nil {
		return "", err
	}
	return name, nil
}

// Close implements Runtime: it kills the session only. The checkout is
// untouched.
func (t *Tmux) Close(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	if _, err := t.run(ctx, "has-session", "-t", "="+handle); err != nil {
		return nil // already gone
	}
	_, err := t.run(ctx, "kill-session", "-t", "="+handle)
	return err
}

// Annotate implements Runtime. tmux has no metadata channel, so dev writes the
// task state into the session's user option, where a status-line format can
// pick it up with #{@dev_stage} if the user wants it.
func (t *Tmux) Annotate(ctx context.Context, handle string, kv map[string]string) error {
	if handle == "" {
		return nil
	}
	for k, v := range kv {
		if _, err := t.run(ctx, "set-option", "-t", "="+handle, "-q", "@dev_"+k, v); err != nil {
			return err
		}
	}
	return nil
}
