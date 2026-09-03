package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func guardSharedCheckout(ctx context.Context, app *App, rt runtime.Runtime, checkout string) error {
	return guardCheckoutClaim(ctx, app, rt, checkout, true)
}

// guardNewAgentCheckout treats even the caller pane as occupied. The ordinary
// writer guard excludes it because the existing agent is continuing its own
// work; a prompt handoff starts a second agent and therefore needs a new writer
// claim unless the user explicitly coordinates sharing.
func guardNewAgentCheckout(ctx context.Context, app *App, rt runtime.Runtime, checkout string) error {
	return guardCheckoutClaim(ctx, app, rt, checkout, false)
}

func guardCheckoutClaim(ctx context.Context, app *App, rt runtime.Runtime, checkout string, excludeCaller bool) error {
	if app.allowSharedCheckout {
		return nil
	}
	reportedPane := ""
	if excludeCaller && rt != nil {
		switch rt.Name() {
		case "herdr":
			reportedPane = os.Getenv("HERDR_PANE_ID")
		case "tmux":
			reportedPane = os.Getenv("TMUX_PANE")
		}
	}
	evidence, err := runtime.InspectOccupancy(ctx, rt, checkout, runtime.OccupancyOptions{
		Profile:      runtime.OccupancyStrict,
		CallerPaneID: reportedPane,
	})
	if err != nil {
		return fmt.Errorf("check live occupancy before claiming %s: %w", config.Contract(checkout), err)
	}
	if evidence.CurrentPane.Err != nil {
		return fmt.Errorf("resolve the current runtime pane before claiming %s: %w", config.Contract(checkout), evidence.CurrentPane.Err)
	}
	if evidence.SessionCoverageErr != nil {
		return fmt.Errorf("classify live runtime coverage before claiming %s: %w", config.Contract(checkout), evidence.SessionCoverageErr)
	}
	if evidence.SessionList.Err != nil {
		return fmt.Errorf("list live runtime sessions before claiming %s: %w", config.Contract(checkout), evidence.SessionList.Err)
	}
	if evidence.AgentActivityList.Err != nil {
		return fmt.Errorf("check live agents before claiming %s: %w", config.Contract(checkout), evidence.AgentActivityList.Err)
	}

	var occupied []string
	for _, agent := range evidence.Agents {
		if !agent.Blocking {
			continue
		}
		activity := agent.Activity
		label := activity.Agent
		if activity.Name != "" {
			label = activity.Name
		}
		if label == "" {
			label = "agent"
		}
		occupied = append(occupied, fmt.Sprintf("%s (%s, pane %s)", label, agent.Status, activity.PaneID))
	}
	if len(occupied) == 0 {
		return nil
	}
	return fmt.Errorf("%s is already occupied by %s; use a separate worktree, or pass --allow-shared-checkout only after coordinating disjoint file ownership",
		config.Contract(checkout), strings.Join(occupied, ", "))
}

func callerPaneID(ctx context.Context, rt runtime.Runtime) (string, error) {
	if rt == nil {
		return "", nil
	}
	switch rt.Name() {
	case "herdr":
		if resolver, ok := rt.(runtime.CurrentPaneResolver); ok {
			return resolver.CurrentPaneID(ctx)
		}
		return os.Getenv("HERDR_PANE_ID"), nil
	case "tmux":
		return os.Getenv("TMUX_PANE"), nil
	default:
		if resolver, ok := rt.(runtime.CurrentPaneResolver); ok {
			return resolver.CurrentPaneID(ctx)
		}
		return "", nil
	}
}

// checkoutAgentActivities returns recognized agents whose reported cwd resolves
// to the same canonical Git worktree root as checkout. A pane is excluded only
// when its exact ID matches excludePane; lifecycle state never makes it free.
func checkoutAgentActivities(ctx context.Context, rt runtime.Runtime, checkout, excludePane string) ([]runtime.AgentActivity, error) {
	evidence, err := runtime.InspectOccupancy(ctx, rt, checkout, runtime.OccupancyOptions{Profile: runtime.OccupancyStrict})
	if err != nil {
		return nil, err
	}
	if evidence.AgentActivityList.Err != nil {
		return nil, evidence.AgentActivityList.Err
	}
	out := make([]runtime.AgentActivity, 0, len(evidence.Agents))
	for _, agent := range evidence.Agents {
		if excludePane == "" || agent.Activity.PaneID != excludePane {
			out = append(out, agent.Activity)
		}
	}
	return out, nil
}
