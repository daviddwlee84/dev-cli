package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type doneIntegration string

const (
	doneIntegrationNone   doneIntegration = ""
	doneIntegrationFF     doneIntegration = "ff"
	doneIntegrationPR     doneIntegration = "pr"
	doneIntegrationMerged doneIntegration = "merged"
)

type doneDirtyPolicy string

const (
	doneDirtyAuto    doneDirtyPolicy = "auto"
	doneDirtyFail    doneDirtyPolicy = "fail"
	doneDirtyCommit  doneDirtyPolicy = "commit"
	doneDirtyDiscard doneDirtyPolicy = "discard"
)

type doneOptions struct {
	Integration   doneIntegration
	DirtyPolicy   doneDirtyPolicy
	Message       string
	Yes           bool
	Push          bool
	BaseRef       string
	ConfirmSquash string
}

type doneSelection struct {
	Integration              doneIntegration
	Dirty                    flow.DirtyPolicy
	Message                  string
	DiscardIntegrationTarget bool
}

type doneChangeView struct {
	Path           string
	BaseEquivalent bool
}

type donePlanView struct {
	Base       string
	BaseOnly   int
	BranchOnly int
	Equivalent int
	Unique     int
	Status     gitx.Status
	Changes    []doneChangeView
}

func (v donePlanView) contained() bool { return v.BranchOnly == 0 }

func runDone(ctx context.Context, app *App, args []string, opts doneOptions) error {
	if opts.DirtyPolicy == "" {
		opts.DirtyPolicy = doneDirtyAuto
	}
	switch opts.DirtyPolicy {
	case doneDirtyAuto, doneDirtyFail, doneDirtyCommit, doneDirtyDiscard:
	default:
		return fmt.Errorf("--dirty %q: want auto, fail, commit or discard", opts.DirtyPolicy)
	}
	if opts.Message != "" && opts.DirtyPolicy != doneDirtyCommit && opts.DirtyPolicy != doneDirtyAuto {
		return errors.New("--message is only used with --dirty=commit")
	}

	resolved, err := resolveTask(app, args)
	if err != nil {
		return err
	}
	selected := *resolved
	mode := selected.EffectiveMode()
	if mode == task.ModeDirect && opts.Integration != doneIntegrationNone {
		return fmt.Errorf("direct task %s is already on %s; it has no branch/worktree to integrate. "+
			"Run `dev done` without --ff/--pr/--merged", selected.Title(), selected.Branch)
	}
	session, err := newLifecycleSession(ctx, app, func() (*task.Task, error) { return resolved, nil })
	if err != nil {
		return err
	}

	selection := doneSelection{
		Integration: opts.Integration,
		Dirty:       flow.DirtyPolicy(opts.DirtyPolicy),
		Message:     strings.TrimSpace(opts.Message),
	}
	plan, err := session.plan(ctx, doneActionOptions(selected, selection, opts))
	if err != nil {
		return err
	}
	if err := doneFatalPreflightError(selected, plan); err != nil {
		return err
	}
	initial := plan
	view := doneView(plan)
	interactive := app.interactive()
	var p *prompter
	if interactive {
		p = newPrompter(app)
		if opts.Integration == doneIntegrationNone || view.Status.Dirty() {
			renderDonePreflight(app, selected, plan)
		}
	}
	if mode != task.ModeDirect && opts.Integration == doneIntegrationNone && !interactive {
		renderDonePreflight(app, selected, plan)
		fmt.Fprintln(app.Out, "Nothing done. Choose an integration mode:")
		fmt.Fprintln(app.Out, "  dev done --ff      rebase when needed, then fast-forward "+view.Base)
		fmt.Fprintln(app.Out, "  dev done --pr      push and open a pull request")
		fmt.Fprintln(app.Out, "  dev done --merged  verify a branch that was merged outside dev")
		return nil
	}

	prompted := false
	if view.Status.Dirty() {
		switch selection.Dirty {
		case flow.DirtyFail:
			return dirtyFinishError(selected, plan)
		case flow.DirtyAuto:
			if !interactive {
				return dirtyFinishError(selected, plan)
			}
			choice, promptErr := p.choice("Dirty changes (c=commit, d=discard, q=cancel)", "cancel",
				"commit (c), discard (d), cancel (q)", map[string]string{
					"c": "commit", "commit": "commit",
					"d": "discard", "discard": "discard", "drop": "discard",
					"q": "cancel", "cancel": "cancel",
				})
			if canceledDone(app, promptErr, choice == "cancel") {
				return nil
			}
			if promptErr != nil {
				return promptErr
			}
			selection.Dirty = flow.DirtyPolicy(choice)
			prompted = true
			plan, err = replanDone(ctx, session, plan, doneActionOptions(selected, selection, opts))
			if err != nil {
				return err
			}
			if err := doneFatalPreflightError(selected, plan); err != nil {
				return err
			}
		}

		if selection.Dirty == flow.DirtyCommit && strings.TrimSpace(selection.Message) == "" {
			if !interactive {
				return errors.New("--message is required with --dirty=commit outside an interactive terminal")
			}
			message, promptErr := p.line("Commit message", "chore: finalize "+selected.Title())
			if canceledDone(app, promptErr, false) {
				return nil
			}
			if promptErr != nil {
				return promptErr
			}
			selection.Message = strings.TrimSpace(message)
			if selection.Message == "" {
				return errors.New("commit message must not be empty")
			}
			prompted = true
			plan, err = replanDone(ctx, session, plan, doneActionOptions(selected, selection, opts))
			if err != nil {
				return err
			}
			if err := doneFatalPreflightError(selected, plan); err != nil {
				return err
			}
		}
		if selection.Dirty == flow.DirtyDiscard {
			if !interactive && !opts.Yes {
				return errors.New("--dirty=discard outside an interactive terminal requires --yes")
			}
			if interactive && !opts.Yes {
				prompted = true
			}
		}
	}

	view = doneView(plan)
	if mode != task.ModeDirect && selection.Integration == doneIntegrationNone {
		if view.contained() && !(view.Status.Dirty() && selection.Dirty == flow.DirtyCommit) {
			selection.Integration = doneIntegrationFF
			prompted = true
		} else {
			choice, promptErr := p.choice("Integration (f=fast-forward, p=PR, q=cancel)", "ff",
				"fast-forward (f), pull request (p), cancel (q)", map[string]string{
					"f": "ff", "ff": "ff", "fast-forward": "ff",
					"p": "pr", "pr": "pr", "pull-request": "pr",
					"q": "cancel", "cancel": "cancel",
				})
			if canceledDone(app, promptErr, choice == "cancel") {
				return nil
			}
			if promptErr != nil {
				return promptErr
			}
			selection.Integration = doneIntegration(choice)
			prompted = true
			plan, err = replanDone(ctx, session, plan, doneActionOptions(selected, selection, opts))
			if err != nil {
				return err
			}
			if err := doneFatalPreflightError(selected, plan); err != nil {
				return err
			}
		}
	}

	if err := donePlanReadinessError(selected, plan, selection); err != nil {
		if interactive && !opts.Yes {
			var canceled bool
			plan, selection, canceled, err = recoverDoneBlockers(ctx, app, p, session, selected, plan, selection, opts)
			if err != nil {
				return err
			}
			if canceled {
				return nil
			}
			prompted = true
		}
		if err := donePlanReadinessError(selected, plan, selection); err != nil {
			return err
		}
	}

	approval := flow.Approve(plan.PlanID)
	if plan.Confirmation.Kind == flow.ConfirmationTyped && opts.Yes {
		approval = flow.ApproveWithToken(plan.PlanID, plan.Confirmation.Token)
	}
	if prompted && !opts.Yes {
		confirmed, token, promptErr := confirmDonePlan(app, p, selected, plan)
		if canceledDone(app, promptErr, !confirmed) {
			return nil
		}
		if promptErr != nil {
			return promptErr
		}
		if plan.Confirmation.Kind == flow.ConfirmationTyped {
			approval = flow.ApproveWithToken(plan.PlanID, token)
		}
	}
	if plan.Confirmation.Kind == flow.ConfirmationTyped && approval.Token == "" {
		return errors.New("destructive finish plan requires its typed confirmation")
	}

	if opts.ConfirmSquash != "" {
		app.warnf("accepting operator attestation that squash commit %s represents branch %s",
			opts.ConfirmSquash, selected.Branch)
	}
	result, applyErr := session.apply(ctx, plan, approval)
	renderLifecycleResult(app, plan, result)
	if applyErr != nil {
		return presentDoneApplyError(result, applyErr)
	}
	final, err := session.finalTask(app)
	if err != nil {
		return err
	}
	offerCleanup := shouldOfferDoneCleanup(selected, opts, interactive, plan.Action)
	if err := renderDoneSuccess(app, selected, final, plan, result, doneView(initial), offerCleanup); err != nil {
		return err
	}
	if offerCleanup {
		return runDoneCleanupWizard(ctx, app, p, final)
	}
	return nil
}

func doneActionOptions(selected task.Task, selection doneSelection, opts doneOptions) flow.ActionOptions {
	if selected.EffectiveMode() == task.ModeDirect {
		return flow.CompleteDirectOptions{
			Dirty: selection.Dirty, CommitMessage: selection.Message, Push: opts.Push,
		}
	}
	switch selection.Integration {
	case doneIntegrationPR:
		return flow.ReviewHandoffOptions{Dirty: selection.Dirty, CommitMessage: selection.Message}
	case doneIntegrationMerged:
		return flow.VerifyMergedOptions{
			Dirty: selection.Dirty, CommitMessage: selection.Message,
			BaseRef: opts.BaseRef, SquashCommit: opts.ConfirmSquash, PushBase: opts.Push,
		}
	default:
		return flow.CompleteFFOptions{
			Dirty: selection.Dirty, CommitMessage: selection.Message, PushBase: opts.Push,
			DiscardIntegrationTarget: selection.DiscardIntegrationTarget,
		}
	}
}

func replanDone(
	ctx context.Context,
	session lifecycleSession,
	previous flow.Plan,
	options flow.ActionOptions,
) (flow.Plan, error) {
	fresh, err := session.plan(ctx, options)
	if err != nil {
		if errors.Is(err, flow.ErrStalePlan) {
			return flow.Plan{}, presentDoneApplyError(flow.Result{}, err)
		}
		return flow.Plan{}, err
	}
	if changed := changedDoneAuthority(previous, fresh); changed != "" {
		stale := &flow.StalePlanError{
			ExpectedPlanID: previous.PlanID,
			ActualPlanID:   fresh.PlanID,
			Reason:         changed + " changed while the finish plan was open",
		}
		return flow.Plan{}, presentDoneApplyError(flow.Result{}, stale)
	}
	return fresh, nil
}

func changedDoneAuthority(previous, fresh flow.Plan) string {
	before := previous.AuthorityFields()
	after := fresh.AuthorityFields()
	keys := []string{
		"task.revision", "task.mode", "task.state", "task.repo-path", "task.branch", "task.base", "task.worktree-path",
		"repo.git-common-dir", "worktree.fingerprint",
		"git.status-error", "git.branch", "git.detached", "git.upstream", "git.ahead", "git.behind",
		"git.changed", "git.staged", "git.unstaged", "git.untracked", "git.conflicted", "git.head",
		"git.base-oid", "git.upstream-oid", "git.operation", "git.operation-active", "git.operation-error",
		"artifact.fingerprint", "finish.authority", "completion.base-ref", "completion.base-oid",
		"completion.base-oid-error", "completion.expected-branch", "completion.branch-oid",
		"runtime.backend", "runtime.available", "runtime.occupancy-fingerprint", "runtime.occupancy-error",
	}
	if previous.Action == fresh.Action {
		keys = append(keys,
			"completion.proof-ref", "completion.proof-oid", "completion.proof-oid-error",
			"completion.proof-contained", "completion.proof-error", "completion.integration",
			"review.kind", "review.remote-url", "review.repository", "review.provider-bin",
			"review.provider-error", "review.provider-available",
		)
	}
	for _, key := range keys {
		if before[key] != after[key] {
			return key
		}
	}
	return ""
}

func canceledDone(app *App, err error, canceled bool) bool {
	if errors.Is(err, errPromptCanceled) || canceled {
		fmt.Fprintln(app.Out, "Canceled; nothing was changed.")
		return true
	}
	return false
}

func doneFatalPreflightError(selected task.Task, plan flow.Plan) error {
	view := doneView(plan)
	if view.Status.Conflicted > 0 {
		return fmt.Errorf("%s has %d conflicted path(s); resolve or abort the merge/rebase before finishing",
			config.Contract(plan.Locator.CheckoutPath), view.Status.Conflicted)
	}
	if condition, ok := doneCondition(plan, flow.ConditionCheckoutPresent); ok && condition.Verdict != flow.VerdictMet {
		return fmt.Errorf("%s no longer exists — resume the task first", config.Contract(plan.Locator.CheckoutPath))
	}
	if condition, ok := doneCondition(plan, flow.ConditionExplicitBase); ok &&
		condition.Requirement == flow.RequirementRequired && condition.Verdict != flow.VerdictMet {
		return fmt.Errorf("cannot determine the base branch for %s — pass --base when starting a task", selected.Repo)
	}
	if condition, ok := doneCondition(plan, flow.ConditionFinishAnalysis); ok && condition.Verdict == flow.VerdictError {
		return errors.New(condition.Evidence)
	}
	if condition, ok := doneCondition(plan, flow.ConditionArtifactReady); ok &&
		(condition.Verdict == flow.VerdictBlocked || condition.Verdict == flow.VerdictError) {
		return fmt.Errorf("artifact readiness for %s is blocked: %s; finalize or discard the intent before integration",
			config.Contract(plan.Locator.CheckoutPath), condition.Evidence)
	}
	return nil
}

func donePlanReadinessError(selected task.Task, plan flow.Plan, selection doneSelection) error {
	if plan.Availability == flow.AvailabilityReady {
		return nil
	}
	if condition, ok := doneCondition(plan, flow.ConditionCheckoutClean); ok &&
		condition.Requirement == flow.RequirementRequired && condition.Verdict != flow.VerdictMet {
		switch selection.Dirty {
		case flow.DirtyAuto, flow.DirtyFail:
			return dirtyFinishError(selected, plan)
		case flow.DirtyCommit:
			if strings.TrimSpace(selection.Message) == "" {
				return errors.New("--message is required with --dirty=commit outside an interactive terminal")
			}
		}
	}
	if plan.Action == flow.ReviewHandoff {
		if condition, ok := doneCondition(plan, flow.ConditionBranchRelation); ok && condition.Verdict != flow.VerdictMet {
			base := doneView(plan).Base
			return fmt.Errorf("%s is already contained in %s; use --ff to finalize cleanup instead of opening a PR",
				selected.Branch, base)
		}
	}
	if plan.Action == flow.VerifyMerged {
		if condition, ok := doneCondition(plan, flow.ConditionMergeProof); ok && condition.Verdict != flow.VerdictMet {
			fields := plan.AuthorityFields()
			return fmt.Errorf("cannot verify %s is contained in %s", fields["completion.proof-ref"], fields["completion.base-ref"])
		}
	}
	blocking := append(plan.InputConditions(), plan.BlockingConditions()...)
	parts := make([]string, 0, len(blocking))
	for _, condition := range blocking {
		message := string(condition.Code) + ": " + condition.Evidence
		if condition.Remediation != "" {
			message += " (" + condition.Remediation + ")"
		}
		parts = append(parts, message)
	}
	if len(parts) == 0 {
		return fmt.Errorf("finish plan is %s: %w", plan.Availability, flow.ErrPlanNotReady)
	}
	return fmt.Errorf("%s is %s: %s: %w", plan.Summary, plan.Availability,
		strings.Join(parts, "; "), flow.ErrPlanNotReady)
}

func dirtyFinishError(t task.Task, plan flow.Plan) error {
	view := doneView(plan)
	return fmt.Errorf("%s has uncommitted changes: %s; branch relation to %s is behind %d, ahead %d; "+
		"%d dirty path(s) match %s and %d contain unique content",
		config.Contract(plan.Locator.CheckoutPath), view.Status.Breakdown(), view.Base,
		view.BaseOnly, view.BranchOnly, view.Equivalent, view.Base, view.Unique)
}

func renderDonePreflight(app *App, t task.Task, plan flow.Plan) {
	view := doneView(plan)
	s := app.outStyle()
	fmt.Fprintf(app.Out, "%s %s\n", s.title("Finish"), t.Title())
	fmt.Fprintf(app.Out, "  %s      %s\n", s.label("branch"), t.Branch)
	fmt.Fprintf(app.Out, "  %s        %s\n", s.label("base"), view.Base)
	switch {
	case view.BaseOnly == 0 && view.BranchOnly == 0:
		fmt.Fprintf(app.Out, "  %s     %s\n", s.label("commits"), s.success(fmt.Sprintf("already equal to %s (behind 0, ahead 0)", view.Base)))
	case view.contained():
		fmt.Fprintf(app.Out, "  %s     %s\n", s.label("commits"), s.success(fmt.Sprintf("already contained in %s (behind %d, ahead 0)", view.Base, view.BaseOnly)))
	default:
		fmt.Fprintf(app.Out, "  %s     %s\n", s.label("commits"), s.warning(fmt.Sprintf("behind %d, ahead %d relative to %s",
			view.BaseOnly, view.BranchOnly, view.Base)))
	}
	if !view.Status.Dirty() {
		fmt.Fprintf(app.Out, "  %s    %s\n", s.label("checkout"), s.success("clean"))
		return
	}
	fmt.Fprintf(app.Out, "  %s    %s\n", s.label("checkout"), s.warning(view.Status.Breakdown()))
	fmt.Fprintf(app.Out, "  %s    %s, %s\n", s.label("contents"),
		s.success(fmt.Sprintf("%d match %s", view.Equivalent, view.Base)),
		s.warning(fmt.Sprintf("%d unique", view.Unique)))
	for _, change := range view.Changes {
		marker := s.warning("unique")
		if change.BaseEquivalent {
			marker = s.success("matches " + view.Base)
		}
		fmt.Fprintf(app.Out, "    %s%s %s\n", marker,
			strings.Repeat(" ", max(0, 12-width(marker))), change.Path)
	}
}

func confirmDonePlan(app *App, p *prompter, t task.Task, plan flow.Plan) (bool, string, error) {
	view := doneView(plan)
	s := app.outStyle()
	fmt.Fprintln(app.Out, "\n"+s.title("Summary"))
	fmt.Fprintf(app.Out, "  %s        %s\n", s.label("task"), t.Title())
	if donePlanHasEffect(plan, flow.EffectDiscardTarget) {
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("canonical"),
			s.danger("discard all staged, unstaged and untracked integration-target changes"))
	}
	switch {
	case donePlanHasEffect(plan, flow.EffectCommitAll):
		message := doneEffectDetails(plan, flow.EffectCommitAll)["message"]
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("dirty"), s.warning(fmt.Sprintf("commit all as %q", message)))
	case donePlanHasEffect(plan, flow.EffectDiscardAll):
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("dirty"), s.danger("discard all staged, unstaged and untracked changes"))
	default:
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("dirty"), s.success("none"))
	}
	switch {
	case plan.Action == flow.ReviewHandoff:
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.review("open a PR into "+view.Base))
	case plan.Action == flow.VerifyMerged:
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.success("verify merge into "+view.Base))
	case !donePlanHasEffect(plan, flow.EffectMergeFF) && !donePlanHasEffect(plan, flow.EffectRebaseBranch):
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.success("already contained in "+view.Base+"; cleanup only"))
	default:
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.success("fast-forward into "+view.Base))
	}
	if plan.Confirmation.Kind == flow.ConfirmationTyped {
		label := plan.Confirmation.Prompt
		if view.Unique > 0 {
			label = fmt.Sprintf("Type %s to discard %d unique path(s)", plan.Confirmation.Token, view.Unique)
		}
		value, err := p.dangerLine(label)
		if err != nil {
			return false, "", err
		}
		return value == plan.Confirmation.Token, value, nil
	}
	confirmed, err := p.confirm("Proceed with this finish plan?", false)
	return confirmed, "", err
}

func presentDoneApplyError(result flow.Result, err error) error {
	for _, step := range result.FailedSteps() {
		switch step.Effect.Code {
		case flow.EffectCommitAll:
			return fmt.Errorf("commit dirty checkout: %w", err)
		case flow.EffectDiscardAll:
			return fmt.Errorf("discard dirty checkout: %w", err)
		}
	}
	if errors.Is(err, flow.ErrStalePlan) {
		for _, step := range result.CompletedSteps() {
			if step.Effect.Code == flow.EffectCommitAll || step.Effect.Code == flow.EffectDiscardAll {
				return fmt.Errorf("checkout changed again during finalization; stop the active writer and rerun dev done: %w", err)
			}
		}
		return fmt.Errorf("checkout or branch changed while the finish plan was open; review the new state and rerun dev done: %w", err)
	}
	if strings.Contains(err.Error(), "changed again after") {
		return fmt.Errorf("checkout changed again during finalization; stop the active writer and rerun dev done: %w", err)
	}
	return err
}

func renderDoneSuccess(app *App, selected, final task.Task, plan flow.Plan, result flow.Result, initial donePlanView, offerCleanup bool) error {
	switch plan.Action {
	case flow.ReviewHandoff:
		if result.Milestone != flow.MilestoneReviewReady {
			return fmt.Errorf("review handoff completed without the review-ready milestone")
		}
		if final.State != selected.State {
			return fmt.Errorf("review handoff changed task state from %s to %s", selected.State, final.State)
		}
		fmt.Fprintln(app.Out, "\nREADY FOR REVIEW · runtime and worktree kept")
		fmt.Fprintln(app.Out, "After merge: dev done --merged --base-ref origin/"+doneView(plan).Base)
		return nil
	case flow.CompleteDirect, flow.CompleteFF, flow.VerifyMerged:
		if result.Milestone != flow.MilestoneMerged || final.State != task.Done {
			return fmt.Errorf("completion returned milestone %s and task state %s; want merged and DONE",
				result.Milestone, final.State)
		}
	default:
		return fmt.Errorf("unsupported completion result action %s", plan.Action)
	}

	if plan.Action == flow.CompleteDirect {
		fmt.Fprintf(app.Out, "%s %s completed directly on %s\n", task.Done.Icon(), final.Title(), final.Branch)
		fmt.Fprintln(app.Out, "   no branch or worktree was created or removed")
		fmt.Fprintln(app.Out, "   MERGED · cleanup pending: run dev retire from outside this runtime")
		return nil
	}
	view := doneView(plan)
	if plan.Action == flow.CompleteFF && initial.contained() &&
		!donePlanHasEffect(plan, flow.EffectRebaseBranch) && !donePlanHasEffect(plan, flow.EffectMergeFF) {
		fmt.Fprintf(app.Out, "   already merged  %s is contained in %s\n", final.Branch, view.Base)
	}
	fmt.Fprintf(app.Out, "%s %s merged into %s\n", task.Done.Icon(), final.Title(), view.Base)
	fmt.Fprintln(app.Out, "   MERGED · runtime and worktree kept")
	if offerCleanup {
		fmt.Fprintln(app.Out, "   cleanup choice follows after a fresh runtime and agent preview")
	} else {
		fmt.Fprintf(app.Out, "   cleanup pending · run `dev retire %s` from outside its workspace\n", final.ID)
	}
	return nil
}

func doneView(plan flow.Plan) donePlanView {
	fields := plan.AuthorityFields()
	view := donePlanView{
		Base:       fields["completion.base-ref"],
		BaseOnly:   authorityInt(fields, "finish.base-only"),
		BranchOnly: authorityInt(fields, "finish.branch-only"),
		Equivalent: authorityInt(fields, "finish.equivalent-dirty"),
		Unique:     authorityInt(fields, "finish.unique-dirty"),
		Status: gitx.Status{
			Branch: fields["git.branch"], Upstream: fields["git.upstream"],
			Ahead: authorityInt(fields, "git.ahead"), Behind: authorityInt(fields, "git.behind"),
			Changed: authorityIntFirst(fields, "finish.status-changed", "git.changed"),
			Staged:  authorityIntFirst(fields, "finish.status-staged", "git.staged"),
			Unstaged: authorityIntFirst(fields,
				"finish.status-unstaged", "git.unstaged"),
			Untracked: authorityIntFirst(fields,
				"finish.status-untracked", "git.untracked"),
			Conflicted: authorityIntFirst(fields,
				"finish.status-conflicted", "git.conflicted"),
		},
	}
	for index := 0; index < authorityInt(fields, "finish.change-count"); index++ {
		prefix := fmt.Sprintf("finish.change.%d.", index)
		view.Changes = append(view.Changes, doneChangeView{
			Path: fields[prefix+"path"], BaseEquivalent: fields[prefix+"base-equivalent"] == "true",
		})
	}
	return view
}

func authorityInt(fields map[string]string, key string) int {
	value, _ := strconv.Atoi(fields[key])
	return value
}

func authorityIntFirst(fields map[string]string, keys ...string) int {
	for _, key := range keys {
		if _, ok := fields[key]; ok {
			return authorityInt(fields, key)
		}
	}
	return 0
}

func doneCondition(plan flow.Plan, code flow.ConditionCode) (flow.Condition, bool) {
	for _, condition := range plan.Conditions() {
		if condition.Code == code {
			return condition, true
		}
	}
	return flow.Condition{}, false
}

func donePlanHasEffect(plan flow.Plan, code flow.EffectCode) bool {
	for _, effect := range plan.Effects() {
		if effect.Code == code {
			return true
		}
	}
	return false
}

func doneEffectDetails(plan flow.Plan, code flow.EffectCode) map[string]string {
	for _, effect := range plan.Effects() {
		if effect.Code == code {
			return effect.Details.Map()
		}
	}
	return nil
}
