// Package gitx wraps the git porcelain commands dev depends on. Every helper
// shells out to git rather than linking a Go implementation: git is already a
// hard dependency, its porcelain formats are stable and documented, and this
// keeps dev's behaviour identical to what the user sees when they run the same
// command by hand.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotARepo is returned when a directory is not inside a git working tree.
var ErrNotARepo = errors.New("not a git repository")

// Error carries the failing command and git's own stderr, which is almost
// always more useful than the exit status alone.
type Error struct {
	Args   []string
	Dir    string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// run executes git in dir and returns trimmed stdout.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	// -c core.quotepath=false keeps non-ASCII paths readable rather than
	// octal-escaped, which matters for the Chinese-named files in these repos.
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		gerr := &Error{Args: args, Dir: dir, Stderr: stderr.String(), Err: err}
		if strings.Contains(gerr.Stderr, "not a git repository") {
			return "", fmt.Errorf("%s: %w", dir, ErrNotARepo)
		}
		return "", gerr
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// Run executes an arbitrary git command in dir. Exported for the few callers
// (park's wip commit, done's merge) that need a one-off command.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, dir, args...)
}

// lines splits git output into non-empty lines.
func lines(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, "\n")
	filtered := out[:0]
	for _, l := range out {
		if l != "" {
			filtered = append(filtered, l)
		}
	}
	return filtered
}

// nulLines splits NUL-delimited git output (-z forms).
func nulLines(s string) []string {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}
