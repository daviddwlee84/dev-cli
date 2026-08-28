package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func guardSharedCheckout(ctx context.Context, app *App, rt runtime.Runtime, checkout string) error {
	if app.allowSharedCheckout {
		return nil
	}
	excludePane, err := callerPaneID(ctx, rt)
	if err != nil {
		return fmt.Errorf("resolve the current runtime pane before claiming %s: %w", config.Contract(checkout), err)
	}
	activities, err := checkoutAgentActivities(ctx, rt, checkout, excludePane)
	if err != nil {
		return fmt.Errorf("check live agents before claiming %s: %w", config.Contract(checkout), err)
	}
	if len(activities) == 0 {
		return nil
	}

	var occupied []string
	for _, activity := range activities {
		label := activity.Agent
		if activity.Name != "" {
			label = activity.Name
		}
		if label == "" {
			label = "agent"
		}
		state := activity.Status
		if state == "" {
			state = "unknown"
		}
		occupied = append(occupied, fmt.Sprintf("%s (%s, pane %s)", label, state, activity.PaneID))
	}
	return fmt.Errorf("%s is already occupied by %s; use a separate worktree, or pass --allow-shared-checkout only after coordinating disjoint file ownership",
		config.Contract(checkout), strings.Join(occupied, ", "))
}

func callerPaneID(ctx context.Context, rt runtime.Runtime) (string, error) {
	inherited := os.Getenv("HERDR_PANE_ID")
	if inherited == "" {
		return "", nil
	}
	if resolver, ok := rt.(runtime.CurrentPaneResolver); ok {
		return resolver.CurrentPaneID(ctx)
	}
	return inherited, nil
}

// checkoutAgentActivities returns recognized agents whose reported cwd resolves
// to the same canonical Git worktree root as checkout. A pane is excluded only
// when its exact ID matches excludePane; lifecycle state never makes it free.
func checkoutAgentActivities(ctx context.Context, rt runtime.Runtime, checkout, excludePane string) ([]runtime.AgentActivity, error) {
	lister, ok := rt.(runtime.AgentActivityLister)
	if !ok {
		return nil, nil
	}
	target, ok := canonicalWorktreeRoot(ctx, checkout)
	if !ok {
		return nil, nil
	}
	activities, err := lister.AgentActivities(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runtime.AgentActivity, 0, len(activities))
	for _, activity := range activities {
		if excludePane != "" && activity.PaneID == excludePane {
			continue
		}
		root, ok := canonicalWorktreeRoot(ctx, activity.CWD)
		if ok && root == target {
			out = append(out, activity)
		}
	}
	return out, nil
}

func canonicalWorktreeRoot(ctx context.Context, dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	repo, err := gitx.Discover(ctx, dir)
	if err != nil || repo.Root == "" {
		return "", false
	}
	root := filepath.Clean(repo.Root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(resolved)
	}
	return root, true
}
