package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

func captureRetirementAuthority(ctx context.Context, app *App, target retireCommandTarget, options flow.RetireOptions) (flow.Fields, error) {
	if target.Task != nil {
		session, err := newLifecycleSession(ctx, app, func() (*task.Task, error) { return target.Task, nil })
		if err != nil {
			return flow.Fields{}, err
		}
		plan, err := session.plan(ctx, options)
		if err != nil {
			return flow.Fields{}, err
		}
		return flow.RetirementPreviewAuthority(plan), nil
	}
	repo, err := gitx.Discover(ctx, target.Path)
	if err != nil {
		return flow.Fields{}, err
	}
	if !repo.IsLinkedWorktree {
		return flow.Fields{}, fmt.Errorf("retire requires an exact linked worktree")
	}
	locator, err := exactUnmanagedWorktreeLocator(ctx, repo.MainRoot, repo.Root)
	if err != nil {
		return flow.Fields{}, err
	}
	service, err := newCLILifecycleService(app)
	if err != nil {
		return flow.Fields{}, err
	}
	request, err := flow.NewRequest(locator, flow.RemoveCheckoutOptions{RequireContained: true, ContainmentBase: gitx.DefaultBranch(ctx, repo.MainRoot), DeleteContainedBranch: options.DeleteBranch, CloseUnknown: options.CloseUnknown, AssumeNoRuntime: options.AssumeNoRuntime, Timeout: options.Timeout})
	if err != nil {
		return flow.Fields{}, err
	}
	plan, err := service.Plan(ctx, request)
	if err != nil {
		return flow.Fields{}, err
	}
	return flow.RetirementPreviewAuthority(plan), nil
}

func validateRetirementAuthority(ctx context.Context, app *App, selected task.Task, expected flow.Fields) error {
	fresh, err := captureRetirementAuthority(ctx, app, retireCommandTarget{Task: &selected}, flow.RetireOptions{})
	if err != nil {
		return err
	}
	actual := fresh.Map()
	for _, field := range expected.Entries() {
		if actual[field.Key] != field.Value {
			return fmt.Errorf("retirement preview is stale: %s changed; no coordinator was launched", field.Key)
		}
	}
	return nil
}
