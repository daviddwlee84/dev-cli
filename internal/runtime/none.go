package runtime

import "context"

// None is the no-multiplexer backend. It is always available, which is what
// guarantees dev's core inventory and worktree commands work on a bare machine
// with nothing but git installed.
//
// Open returns the directory itself as the handle: the shell wrapper installed
// by `dev shell-init` turns that into a cd in the calling shell, since a child
// process can never change its parent's working directory.
type None struct{}

// Name implements Runtime.
func (None) Name() string { return "none" }

// Available implements Runtime; the null backend always is.
func (None) Available() bool { return true }

// Open implements Runtime by reporting the directory to change into. None does
// not open a runtime surface, so the result is never launchable.
func (None) Open(_ context.Context, dir, _ string) (OpenResult, error) {
	return OpenResult{Handle: dir}, nil
}

// Close implements Runtime as a no-op: there is no session to end.
func (None) Close(context.Context, string) error { return nil }

// List implements Runtime; without a multiplexer nothing is tracked as live.
func (None) List(context.Context) ([]Session, error) { return nil, nil }

// Annotate implements Runtime as a no-op.
func (None) Annotate(context.Context, string, map[string]string) error { return nil }
