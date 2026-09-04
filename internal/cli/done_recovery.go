package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

func recoverDoneBlockers(
	ctx context.Context,
	app *App,
	p *prompter,
	session lifecycleSession,
	selected task.Task,
	plan flow.Plan,
	selection doneSelection,
	opts doneOptions,
) (flow.Plan, doneSelection, bool, error) {
	current := plan
	for _, target := range []struct {
		condition flow.ConditionCode
		path      string
		label     string
	}{
		{flow.ConditionAgentOccupancy, current.Locator.CheckoutPath, "task checkout"},
		{flow.ConditionIntegrationOccupancy, current.Locator.RepoPath, "canonical integration checkout"},
	} {
		condition, ok := doneCondition(current, target.condition)
		if !ok || condition.Verdict == flow.VerdictMet {
			continue
		}
		changed, err := offerIdleAgentPaneClosure(ctx, app, p, selected, target.path, target.label)
		if err != nil {
			return current, selection, false, err
		}
		if !changed {
			fresh, planErr := session.plan(ctx, doneActionOptions(selected, selection, opts))
			if planErr != nil {
				return current, selection, false, planErr
			}
			if changed := changedDoneNonRuntimeAuthority(current, fresh); changed != "" {
				return current, selection, false, presentDoneApplyError(flow.Result{}, &flow.StalePlanError{
					ExpectedPlanID: current.PlanID, ActualPlanID: fresh.PlanID,
					Reason: changed + " changed while refreshing agent occupancy",
				})
			}
			current = fresh
			condition, stillBlocked := doneCondition(current, target.condition)
			if stillBlocked && condition.Verdict != flow.VerdictMet {
				return current, selection, false, nil
			}
			continue
		}
		fresh, err := session.plan(ctx, doneActionOptions(selected, selection, opts))
		if err != nil {
			return current, selection, false, err
		}
		if changed := changedDoneNonRuntimeAuthority(current, fresh); changed != "" {
			return current, selection, false, presentDoneApplyError(flow.Result{}, &flow.StalePlanError{
				ExpectedPlanID: current.PlanID, ActualPlanID: fresh.PlanID,
				Reason: changed + " changed while closing an approved idle agent pane",
			})
		}
		current = fresh
	}

	condition, ok := doneCondition(current, flow.ConditionIntegrationTarget)
	if current.Action != flow.CompleteFF || !ok || condition.Verdict == flow.VerdictMet ||
		!strings.Contains(condition.Evidence, "canonical checkout is dirty") {
		return current, selection, false, nil
	}
	status, err := gitx.StatusOf(ctx, current.Locator.RepoPath)
	if err != nil {
		return current, selection, false, err
	}
	paths, err := gitx.ChangedPaths(ctx, current.Locator.RepoPath)
	if err != nil {
		return current, selection, false, err
	}
	sort.Strings(paths)
	s := app.outStyle()
	fmt.Fprintf(app.Out, "\n%s\n", s.title("Canonical checkout blocker"))
	fmt.Fprintf(app.Out, "  %s  %s\n", s.label("checkout"), config.Contract(current.Locator.RepoPath))
	fmt.Fprintf(app.Out, "  %s    %s\n", s.label("status"), s.warning(status.Breakdown()))
	for _, path := range paths {
		label := path
		if gitx.IsAgentArtifact(path) {
			label += " (agent artifact)"
		}
		fmt.Fprintf(app.Out, "    %s\n", label)
	}
	stashSafety, stashErr := gitx.InspectStashSafety(ctx, current.Locator.RepoPath)
	stashAvailable := stashErr == nil && stashSafety.Safe()
	prompt := "Resolve canonical changes (p=PR, d=discard, q=cancel)"
	description := "pull request (p), discard canonical changes (d), cancel (q)"
	choices := map[string]string{
		"p": "pr", "pr": "pr", "pull-request": "pr",
		"d": "discard", "discard": "discard", "drop": "discard",
		"q": "cancel", "cancel": "cancel",
	}
	if stashAvailable {
		prompt = "Resolve canonical changes (p=PR, s=stash+restore, d=discard, q=cancel)"
		description = "pull request (p), stash and restore canonical changes (s), discard canonical changes (d), cancel (q)"
		choices["s"] = "stash-restore"
		choices["stash"] = "stash-restore"
		choices["stash-restore"] = "stash-restore"
		fmt.Fprintln(app.Out, "  stash       preserves staged, unstaged, untracked, and agent-artifact changes")
	} else {
		reason := "stash safety could not be observed"
		switch {
		case stashErr != nil:
			reason += ": " + stashErr.Error()
		case stashSafety.DirtySubmodules > 0:
			reason = fmt.Sprintf("stash unavailable: %d dirty or unavailable submodule checkout(s)", stashSafety.DirtySubmodules)
		case len(stashSafety.NestedRepositories) > 0:
			reason = "stash unavailable: nested repositories at " + strings.Join(stashSafety.NestedRepositories, ", ")
		}
		fmt.Fprintf(app.Out, "  %s\n", s.warning(reason))
	}
	choice, promptErr := p.choice(prompt, "cancel", description, choices)
	if errors.Is(promptErr, errPromptCanceled) || choice == "cancel" {
		fmt.Fprintln(app.Out, "Integration canceled; any approved pane closures remain complete.")
		return current, selection, true, nil
	}
	if promptErr != nil {
		return current, selection, false, promptErr
	}
	selection.IntegrationTargetPolicy = flow.IntegrationTargetFail
	if choice == "discard" {
		selection.IntegrationTargetPolicy = flow.IntegrationTargetDiscard
	}
	if choice == "stash-restore" {
		selection.IntegrationTargetPolicy = flow.IntegrationTargetStashRestore
	}
	if choice == "pr" {
		selection.Integration = doneIntegrationPR
		selection.IntegrationTargetPolicy = flow.IntegrationTargetFail
	}
	fresh, err := replanDone(ctx, session, current, doneActionOptions(selected, selection, opts))
	if err != nil {
		return current, selection, false, err
	}
	return fresh, selection, false, nil
}

func offerIdleAgentPaneClosure(
	ctx context.Context,
	app *App,
	p *prompter,
	selected task.Task,
	checkout, label string,
) (bool, error) {
	rt := runtimeForTask(app, &selected)
	if rt.Name() != "herdr" {
		return false, nil
	}
	closer, ok := rt.(runtime.PaneCloser)
	if !ok {
		return false, nil
	}
	caller, err := callerPaneID(ctx, rt)
	if err != nil {
		return false, err
	}
	agents, err := checkoutAgentActivities(ctx, rt, checkout, caller)
	if err != nil || len(agents) == 0 {
		return false, err
	}
	for _, agent := range agents {
		if agent.Status != "idle" && agent.Status != "done" {
			return false, nil
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].PaneID < agents[j].PaneID })
	fmt.Fprintf(app.Out, "\n%s\n", app.outStyle().title("Idle agent blocker · "+label))
	for _, agent := range agents {
		name := agent.Agent
		if agent.Name != "" {
			name = agent.Name
		}
		fmt.Fprintf(app.Out, "  %s  %s · %s · %s\n", agent.PaneID, name, agent.Status, config.Contract(agent.CWD))
	}
	confirmed, promptErr := p.confirm(fmt.Sprintf("Close %d exact idle/done Herdr pane(s) and recheck?", len(agents)), false)
	if errors.Is(promptErr, errPromptCanceled) || !confirmed {
		return false, nil
	}
	if promptErr != nil {
		return false, promptErr
	}
	for _, expected := range agents {
		fresh, err := checkoutAgentActivities(ctx, rt, checkout, caller)
		if err != nil {
			return false, err
		}
		matched := false
		for _, candidate := range fresh {
			if candidate.PaneID != expected.PaneID {
				continue
			}
			matched = true
			if candidate.Agent != expected.Agent || candidate.Name != expected.Name || candidate.Status != expected.Status ||
				candidate.CWD != expected.CWD || candidate.Status != "idle" && candidate.Status != "done" {
				return false, fmt.Errorf("agent pane %s changed while its close prompt was open", expected.PaneID)
			}
			break
		}
		if !matched {
			return false, fmt.Errorf("agent pane %s disappeared while its close prompt was open", expected.PaneID)
		}
		if err := closer.ClosePane(ctx, expected.PaneID); err != nil {
			return false, fmt.Errorf("close idle Herdr pane %s: %w", expected.PaneID, err)
		}
		fmt.Fprintf(app.Out, "   closed     Herdr pane %s\n", expected.PaneID)
	}
	return true, nil
}

func changedDoneNonRuntimeAuthority(previous, fresh flow.Plan) string {
	before, after := previous.AuthorityFields(), fresh.AuthorityFields()
	keys := []string{
		"task.revision", "task.mode", "task.state", "task.repo-path", "task.branch", "task.base", "task.worktree-path",
		"repo.git-common-dir", "worktree.fingerprint",
		"git.status-error", "git.branch", "git.detached", "git.upstream", "git.ahead", "git.behind",
		"git.changed", "git.staged", "git.unstaged", "git.untracked", "git.conflicted", "git.head",
		"git.base-oid", "git.upstream-oid", "git.operation", "git.operation-active", "git.operation-error",
		"artifact.fingerprint", "finish.authority", "completion.base-ref", "completion.base-oid",
		"completion.base-oid-error", "completion.expected-branch", "completion.branch-oid",
		"review.kind", "review.remote-url", "review.repository", "review.provider-bin", "review.provider-error",
	}
	for _, key := range keys {
		if before[key] != after[key] {
			return key
		}
	}
	return ""
}
