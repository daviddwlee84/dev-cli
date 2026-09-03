package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type lifecycleExecution struct {
	Selected task.Task
	Task     task.Task
	Plan     flow.Plan
	Result   flow.Result
}

type nonTaskLifecycleExecution struct {
	Plan   flow.Plan
	Result flow.Result
}

type lifecycleTaskResolver func() (*task.Task, error)

type lifecycleSession struct {
	Selected task.Task
	Locator  flow.Locator
	service  *flow.Service
}

func newLifecycleSession(ctx context.Context, app *App, resolve lifecycleTaskResolver) (lifecycleSession, error) {
	var session lifecycleSession
	selected, err := resolve()
	if err != nil {
		return session, err
	}
	if selected == nil {
		return session, errors.New("task resolver returned no task")
	}
	session.Selected = *selected

	service, err := newCLILifecycleService(app)
	if err != nil {
		return session, err
	}
	session.service = service
	session.Locator, err = service.LocateTask(ctx, selected.ID)
	if err != nil {
		return session, err
	}
	return session, nil
}

func (s lifecycleSession) plan(ctx context.Context, options flow.ActionOptions) (flow.Plan, error) {
	request, err := flow.NewRequest(s.Locator, options)
	if err != nil {
		return flow.Plan{}, presentLifecycleRequestError(s.Selected, options.Action(), err)
	}
	return s.service.Plan(ctx, request)
}

func (s lifecycleSession) apply(ctx context.Context, plan flow.Plan, approval flow.Approval) (flow.Result, error) {
	return s.service.Apply(ctx, plan, approval)
}

func (s lifecycleSession) finalTask(app *App) (task.Task, error) {
	final, err := app.Tasks.GetRecord(s.Locator.TaskID)
	if err != nil {
		return task.Task{}, fmt.Errorf("load final exact task %s: %w", s.Locator.TaskID, err)
	}
	return final.Task, nil
}

func (s lifecycleSession) requireRetired(app *App, result flow.Result) error {
	if result.Milestone != flow.MilestoneRetired {
		return fmt.Errorf("task %s retirement returned milestone %s, want %s",
			s.Locator.TaskID, result.Milestone, flow.MilestoneRetired)
	}
	if _, err := app.Tasks.GetRecord(s.Locator.TaskID); !errors.Is(err, task.ErrNotFound) {
		if err == nil {
			return fmt.Errorf("task %s retirement completed but the task record still exists", s.Locator.TaskID)
		}
		return fmt.Errorf("verify retired task %s is absent: %w", s.Locator.TaskID, err)
	}
	return nil
}

func executeTaskLifecycle(
	ctx context.Context,
	app *App,
	resolve lifecycleTaskResolver,
	options flow.ActionOptions,
) (lifecycleExecution, error) {
	var execution lifecycleExecution
	session, err := newLifecycleSession(ctx, app, resolve)
	if err != nil {
		return execution, err
	}
	execution.Selected = session.Selected
	execution.Plan, err = session.plan(ctx, options)
	if err != nil {
		return execution, err
	}
	execution.Result, err = session.apply(ctx, execution.Plan, flow.Approve(execution.Plan.PlanID))
	renderLifecycleResult(app, execution.Plan, execution.Result)
	if err != nil {
		return execution, presentLifecycleApplyError(execution.Plan, err)
	}

	execution.Task, err = session.finalTask(app)
	if err != nil {
		return execution, err
	}
	return execution, nil
}

// exactUnmanagedWorktreeLocator converts a branch-selected checkout into the
// canonical identity taskflow requires. Branch lookup is only selection; fresh
// Git registry evidence supplies every authority field used by the plan.
func exactUnmanagedWorktreeLocator(ctx context.Context, repoPath, checkoutPath string) (flow.Locator, error) {
	registered, err := gitx.ResolveRegisteredWorktree(ctx, repoPath, checkoutPath)
	if err != nil {
		return flow.Locator{}, fmt.Errorf("resolve exact registered worktree %s: %w", config.Contract(checkoutPath), err)
	}
	return flow.Locator{
		RepoKey:      registered.GitCommonDir,
		RowKey:       registered.Path,
		RowKind:      "unmanaged",
		RepositoryID: registered.GitCommonDir,
		GitCommonDir: registered.GitCommonDir,
		RepoPath:     registered.RepositoryPath,
		CheckoutPath: registered.Path,
		Branch:       registered.Worktree.Branch,
		HeadOID:      registered.Worktree.Head,
	}, nil
}

// executeNonTaskLifecycle applies an exact non-task plan as an explicitly
// invoked CLI action. Destructive compatibility flags authorize only the typed
// token sealed into that same plan; they do not change readiness or blockers.
func executeNonTaskLifecycle(
	ctx context.Context,
	app *App,
	locator flow.Locator,
	options flow.ActionOptions,
	authorizeTyped bool,
) (nonTaskLifecycleExecution, error) {
	var execution nonTaskLifecycleExecution
	service, err := newCLILifecycleService(app)
	if err != nil {
		return execution, err
	}
	request, err := flow.NewRequest(locator, options)
	if err != nil {
		return execution, err
	}
	execution.Plan, err = service.Plan(ctx, request)
	if err != nil {
		return execution, err
	}
	approval := flow.Approve(execution.Plan.PlanID)
	if execution.Plan.Confirmation.Kind == flow.ConfirmationTyped {
		if !authorizeTyped {
			return execution, fmt.Errorf("%s requires its exact typed confirmation", execution.Plan.Summary)
		}
		approval = flow.ApproveWithToken(execution.Plan.PlanID, execution.Plan.Confirmation.Token)
	}
	execution.Result, err = service.Apply(ctx, execution.Plan, approval)
	renderLifecycleResult(app, execution.Plan, execution.Result)
	if err != nil {
		return execution, presentLifecycleApplyError(execution.Plan, err)
	}
	return execution, nil
}

func newCLILifecycleService(app *App) (*flow.Service, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve lifecycle caller directory: %w", err)
	}
	return flow.NewLifecycleService(flow.LifecycleConfig{
		Config:              app.Cfg,
		Tasks:               app.Tasks,
		Artifacts:           artifactStore(app),
		DefaultRuntime:      app.Runtime,
		NamedRuntime:        app.runtimeNamed,
		Host:                config.Hostname(),
		CWD:                 cwd,
		AllowSharedCheckout: app.allowSharedCheckout,
		Logf: func(format string, args ...any) {
			if app.Err != nil {
				fmt.Fprintf(app.Err, format+"\n", args...)
			}
		},
	})
}

func renderLifecycleResult(app *App, plan flow.Plan, result flow.Result) {
	style := app.outStyle()
	for _, step := range result.AttemptedSteps() {
		if step.Status == flow.StepFailed {
			label := step.Effect.Description
			if label == "" {
				label = string(step.Effect.Code)
			}
			failure := step.Failure
			if failure == "" {
				failure = "operation failed"
			}
			app.warnf("%s failed: %s", label, failure)
			continue
		}
		if step.Status != flow.StepCompleted {
			continue
		}

		details := step.Effect.Details.Map()
		switch step.Effect.Code {
		case flow.EffectCommitWIP, flow.EffectCommitAll:
			message := details["message"]
			if message == "" {
				message = step.Detail
			}
			fmt.Fprintf(app.Out, "   %s  %s\n", style.label("committed"), message)
		case flow.EffectDiscardAll:
			fmt.Fprintf(app.Out, "   %s  checkout changes\n", style.label("discarded"))
		case flow.EffectPushBranch, flow.EffectPushBase:
			pushed := strings.TrimPrefix(step.Detail, "pushed ")
			pushed = strings.TrimPrefix(pushed, "published ")
			if pushed == step.Detail || pushed == "" {
				pushed = "origin/" + step.Effect.Target
			}
			fmt.Fprintf(app.Out, "   %s     %s\n", style.label("pushed"), pushed)
		case flow.EffectCloseRuntime:
			closed := strings.TrimPrefix(step.Detail, "closed ")
			fmt.Fprintf(app.Out, "   %s     %s\n", style.label("closed"), closed)
		case flow.EffectRemoveWorktree:
			fmt.Fprintf(app.Out, "   %s removed: %s\n", style.label("worktree"), config.Contract(step.Effect.Target))
		case flow.EffectDeleteBranch:
			fmt.Fprintf(app.Out, "   %s   %s deleted\n", style.label("branch"), step.Effect.Target)
		case flow.EffectDeleteTask:
			fmt.Fprintf(app.Out, "   %s     %s reaped\n", style.label("task"), step.Effect.Target)
		case flow.EffectRebaseBranch:
			fmt.Fprintf(app.Out, "   %s    %s\n", style.label("rebased"), strings.TrimPrefix(step.Detail, "rebased "))
		case flow.EffectSwitchBase, flow.EffectSwitchBranch:
			switched := strings.TrimPrefix(step.Detail, "switched ")
			fmt.Fprintf(app.Out, "   %s   %s\n", style.label("switched"), switched)
		case flow.EffectMergeFF:
			fmt.Fprintf(app.Out, "   %s  %s\n", style.label("integrated"), step.Detail)
		case flow.EffectCreateReview:
			if handoff, ok := result.Handoff(); ok && handoff.Kind == flow.HandoffURL && handoff.URL != "" {
				fmt.Fprintf(app.Out, "   %s     %s\n", style.label("opened"), handoff.URL)
			}
		case flow.EffectVerifyAncestry:
			fmt.Fprintf(app.Out, "   %s   %s\n", style.label("verified"), step.Detail)
		case flow.EffectUpdateTask:
			if isCompletionAction(plan.Action) {
				state := details["state"]
				if state == "" {
					state = "lifecycle state"
				}
				fmt.Fprintf(app.Out, "   %s   %s\n", style.label("recorded"), strings.ToUpper(state))
			}
		case flow.EffectCreateWorktree:
			fmt.Fprintf(app.Out, "   %s    %s\n", style.label("rebuilt"), config.Contract(step.Effect.Target))
		}
	}
	for _, warning := range result.Warnings() {
		switch warning {
		case "no next action is recorded":
			app.warnf("no next action recorded — `dev park --next \"...\"` is what makes resuming cheap")
		default:
			app.warnf("%s", warning)
		}
	}
	for _, recovery := range result.Recovery() {
		app.warnf("recovery: %s", recovery)
	}
}

func isCompletionAction(action flow.Action) bool {
	switch action {
	case flow.CompleteDirect, flow.CompleteFF, flow.ReviewHandoff, flow.VerifyMerged:
		return true
	default:
		return false
	}
}

func presentLifecycleRequestError(selected task.Task, action flow.Action, err error) error {
	var transition *flow.InvalidTransitionError
	if !errors.As(err, &transition) {
		return err
	}
	if action == flow.ParkCold && selected.EffectiveMode() == task.ModeDirect {
		return fmt.Errorf("direct task %s uses the canonical checkout on %s, so it cannot go cold; park it warm or finish it: %w",
			selected.Title(), selected.Branch, err)
	}
	if action == flow.Retire {
		return fmt.Errorf("task %s is %s, not merged; run dev done first: %w", selected.ID, selected.State, err)
	}
	return err
}

func presentLifecycleApplyError(plan flow.Plan, err error) error {
	if !errors.Is(err, flow.ErrPlanNotReady) {
		return err
	}
	blocking := append(plan.InputConditions(), plan.BlockingConditions()...)
	for _, condition := range blocking {
		switch condition.Code {
		case flow.ConditionCheckoutClean:
			if plan.Action == flow.RemoveCheckout {
				return fmt.Errorf("%s has uncommitted changes (%s).\nCommit them, or re-run with --force to discard them: %w",
					config.Contract(plan.Locator.CheckoutPath), condition.Evidence, err)
			}
			if plan.Action == flow.ParkCold {
				return fmt.Errorf("%s has uncommitted changes; going cold removes the worktree.\nCommit them, or re-run with --wip to checkpoint automatically: %w",
					config.Contract(plan.Locator.CheckoutPath), err)
			}
		case flow.ConditionBranchPublished, flow.ConditionBranchPushed:
			if plan.Action == flow.ParkCold {
				return fmt.Errorf("%s is not fully pushed (%s); a cold task must be reconstructible from the remote.\nRun `dev park --cold --push`: %w",
					plan.Locator.Branch, condition.Evidence, err)
			}
		case flow.ConditionTaskClaims:
			if plan.Action == flow.RemoveCheckout {
				return fmt.Errorf("%s is claimed by task metadata (%s).\nUse managed dev park/dev retire, or reconcile the task claim before removing this checkout: %w",
					config.Contract(plan.Locator.CheckoutPath), condition.Evidence, err)
			}
		case flow.ConditionTaskInventory:
			if plan.Action == flow.RemoveCheckout {
				return fmt.Errorf("cannot prove %s is unmanaged because task inventory is incomplete (%s).\nRepair or reconcile task metadata before using dev park/dev retire or removing this checkout: %w",
					config.Contract(plan.Locator.CheckoutPath), condition.Evidence, err)
			}
		case flow.ConditionOwner:
			if plan.Action == flow.Resume {
				owner := plan.AuthorityFields()["task.owner"]
				return fmt.Errorf("task %s is owned by %s.\nMake sure that machine has pushed its work, then re-run with --force to take ownership: %w",
					plan.Locator.TaskID, owner, err)
			}
		case flow.ConditionAgentOccupancy:
			if plan.Action == flow.Resume && strings.HasPrefix(condition.Evidence, "checkout is occupied by ") {
				occupant := strings.TrimPrefix(condition.Evidence, "checkout is occupied by ")
				return fmt.Errorf("%s is already occupied by %s; use a separate worktree, or pass --allow-shared-checkout only after coordinating disjoint file ownership: %w",
					config.Contract(plan.Locator.CheckoutPath), occupant, err)
			}
		}
	}

	parts := make([]string, 0, len(blocking))
	for _, condition := range blocking {
		message := string(condition.Code) + ": " + condition.Evidence
		if condition.Remediation != "" {
			message += " (" + condition.Remediation + ")"
		}
		parts = append(parts, message)
	}
	if len(parts) == 0 {
		return err
	}
	return fmt.Errorf("%s is %s: %s: %w", plan.Summary, plan.Availability, strings.Join(parts, "; "), err)
}
