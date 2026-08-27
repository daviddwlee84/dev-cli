package cli

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

// openCheckout makes a directory live, asking the backend to treat it as a git
// worktree when it knows how — that is what groups the checkout under its
// parent repo in a sidebar instead of listing it as an unrelated directory.
func openCheckout(ctx context.Context, rt runtime.Runtime, dir, label string) (string, error) {
	if wo, ok := rt.(runtime.WorktreeOpener); ok {
		return wo.OpenWorktree(ctx, dir, label)
	}
	return rt.Open(ctx, dir, label)
}

// asError is errors.As with a typed target, kept here so call sites read as
// one line.
func asError[T error](err error, target *T) bool { return errors.As(err, target) }
