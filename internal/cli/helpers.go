package cli

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

// openCheckout makes a directory live, asking the backend to treat it as a git
// worktree when it knows how — that is what groups the checkout under its
// parent repo in a sidebar instead of listing it as an unrelated directory.
func openCheckout(ctx context.Context, rt runtime.Runtime, dir, label string) (runtime.OpenResult, error) {
	if wo, ok := rt.(runtime.WorktreeOpener); ok {
		return wo.OpenWorktree(ctx, dir, label)
	}
	return rt.Open(ctx, dir, label)
}

// activateRuntime performs the interactive half of navigation. Runtime Open
// calls are detached by design; explicit open/resume actions opt into either
// switching the current multiplexer client or attaching from a plain shell.
func activateRuntime(ctx context.Context, rt runtime.Runtime, handle string) error {
	if rt == nil || rt.Name() == "none" || handle == "" {
		return nil
	}
	activator, ok := rt.(runtime.Activator)
	if !ok {
		return errors.New("runtime " + rt.Name() + " cannot activate sessions")
	}
	return activator.Activate(ctx, handle)
}

func persistedRuntimeHandle(rt runtime.Runtime, opened runtime.OpenResult) string {
	if rt == nil || rt.Name() == "none" {
		return ""
	}
	return opened.Handle
}

func worktreeRuntimeLabel(repo, branch string) string { return repo + "/" + branch }

// asError is errors.As with a typed target, kept here so call sites read as
// one line.
func asError[T error](err error, target *T) bool { return errors.As(err, target) }
