package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
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
	for {
		condition, blocked := doneCondition(current, flow.ConditionIntegrationOccupancy)
		if !blocked || condition.Verdict == flow.VerdictMet {
			break
		}
		fmt.Fprintf(app.Out, "\nParent checkout is preserved: %s\n  %s\n", config.Contract(current.Locator.RepoPath), condition.Evidence)
		fmt.Fprintln(app.Out, "  dev done will not close parent agents or tabs. Handle them independently through Herdr.")
		if occupancy, err := doneOccupancy(ctx, app, selected, current.Locator.RepoPath); err == nil {
			renderForegroundOccupancy(app, occupancy, "KEEP")
		}
		choice, err := p.choice("Parent occupied (r=recheck, p=PR, q=cancel)", "cancel", "recheck (r), pull request (p), cancel (q)", map[string]string{"r": "recheck", "recheck": "recheck", "p": "pr", "pr": "pr", "q": "cancel", "cancel": "cancel"})
		if canceledDone(app, err, choice == "cancel") {
			return current, selection, true, nil
		}
		if err != nil {
			return current, selection, false, err
		}
		if choice == "pr" {
			selection.Integration = doneIntegrationPR
			selection.IntegrationTargetPolicy = flow.IntegrationTargetFail
			selection.Runtime = flow.CompletionRuntimeOptions{}
		}
		fresh, err := session.plan(ctx, doneActionOptions(selected, selection, opts))
		if err != nil {
			return current, selection, false, err
		}
		if changed := changedDoneNonRuntimeAuthority(current, fresh); changed != "" {
			return current, selection, false, presentDoneApplyError(flow.Result{}, &flow.StalePlanError{Reason: changed + " changed while refreshing parent occupancy"})
		}
		current = fresh
	}
	if condition, blocked := doneCondition(current, flow.ConditionAgentOccupancy); blocked && condition.Verdict != flow.VerdictMet {
		panes, err := offerIdleAgentPaneClosure(ctx, app, p, selected, current.Locator.CheckoutPath, "task checkout")
		if err != nil {
			return current, selection, false, err
		}
		if len(panes) == 0 {
			return current, selection, false, nil
		}
		selection.Runtime.CloseTaskPanes = flow.NewFields(panes)
		fresh, err := replanDone(ctx, session, current, doneActionOptions(selected, selection, opts))
		if err != nil {
			return current, selection, false, err
		}
		current = fresh
	}
	if condition, blocked := doneCondition(current, flow.ConditionForegroundPrograms); blocked && condition.Verdict != flow.VerdictMet {
		consent, canceled, err := confirmDoneForegroundPrograms(ctx, app, p, selected, current)
		if err != nil || canceled {
			return current, selection, canceled, err
		}
		selection.Runtime.ProgramConsent = flow.NewFields(consent)
		fresh, err := replanDone(ctx, session, current, doneActionOptions(selected, selection, opts))
		if err != nil {
			return current, selection, false, err
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
		fmt.Fprintln(app.Out, "Integration canceled; no panes were closed and nothing was changed.")
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

func offerIdleAgentPaneClosure(ctx context.Context, app *App, p *prompter, selected task.Task, checkout, label string) (map[string]string, error) {
	occupancy, err := doneOccupancy(ctx, app, selected, checkout)
	if err != nil {
		return nil, err
	}
	panes := flow.CloseableTaskPanes(selected.EffectiveMode(), occupancy)
	for _, agent := range occupancy.Agents {
		if agent.Blocking && panes[agent.Activity.PaneID] == "" {
			return nil, nil
		}
	}
	if len(panes) == 0 {
		return nil, nil
	}
	fmt.Fprintf(app.Out, "\nIdle agent blocker · %s\n", label)
	renderForegroundOccupancy(app, occupancy, "CANDIDATE")
	confirmed, err := p.confirm(fmt.Sprintf("Include closure of these %d exact idle/done TASK agent pane(s) in the final plan? Their sessions will end only after final approval", len(panes)), false)
	if errors.Is(err, errPromptCanceled) || !confirmed {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return panes, nil
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
