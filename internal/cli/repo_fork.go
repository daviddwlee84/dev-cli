package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/spf13/cobra"
)

// Only the fork confirmation before Apply may claim that nothing was cloned.
// Later scaffold prompts can be canceled after acquisition has already succeeded.
var errRepoForkCanceled = errors.New("fork confirmation canceled")

func newRepoForkCmd(app *App) *cobra.Command {
	var sourceRemote string
	var dryRun, jsonOut, yes bool
	cmd := &cobra.Command{
		Use:   "fork [repo-or-path]",
		Short: "Connect an existing checkout to your personal GitHub fork",
		Long: `Create or reuse your personal GitHub fork and configure this checkout:
origin points to your fork, upstream to the original source. Existing branch
pull targets are preserved and default pushes use origin. Conflicting remote
names or unrelated explicit push targets stop the operation.

Source discovery prefers upstream, then origin; use --source-remote when needed.
--dry-run reads local and GitHub state without changes. Outside a terminal,
mutation requires --yes. No branches are pushed and no pull requests are opened.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun && !yes && (jsonOut || !app.interactive()) {
				return errors.New("non-interactive fork requires --yes; use --dry-run to inspect the plan")
			}
			root, err := resolveSetupRepo(app, args)
			if err != nil {
				return err
			}
			provider, err := forge.For(forge.GitHub)
			if err != nil {
				return err
			}
			plan, err := repo.PlanFork(ctxOf(), provider, repo.ForkRequest{
				Path: root, SourceRemote: sourceRemote, Config: app.Cfg,
			})
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOut {
					return json.NewEncoder(app.Out).Encode(map[string]any{"operation": "fork", "dry_run": true, "plan": plan})
				}
				fmt.Fprintln(app.Out, "Dry run — nothing will be changed")
				renderForkPlan(app, plan)
				return nil
			}
			if err := confirmRepoFork(app, plan, yes); err != nil {
				if errors.Is(err, errPromptCanceled) {
					fmt.Fprintln(app.Out, "Canceled; nothing was changed.")
					return nil
				}
				return err
			}
			result, applyErr := repo.ApplyFork(ctxOf(), provider, plan)
			if jsonOut {
				if err := renderRepoForkResult(app, "fork", result, applyErr); err != nil {
					return err
				}
			} else {
				renderForkPlan(app, plan)
				fmt.Fprintf(app.Out, "Completed: %v\n", result.Completed)
			}
			return applyErr
		},
	}
	cmd.Flags().StringVar(&sourceRemote, "source-remote", "", "remote identifying the original GitHub repository")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "inspect the fork and remote plan without changing anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit a machine-readable plan or result")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply the fork and remote plan without prompting")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func planRepoForkClone(app *App, request *repoWorkflowRequest) error {
	provider, err := forge.For(forge.GitHub)
	if err != nil {
		return err
	}
	plan, err := repo.PlanFork(ctxOf(), provider, repo.ForkRequest{
		CloneRef: request.Ref, Destination: request.Destination,
		Config: app.Cfg, Submodules: request.Submodules,
	})
	if err != nil {
		return err
	}
	request.ForkPlan = &plan
	return nil
}

func acquireRepoFork(app *App, request *repoWorkflowRequest) error {
	if request.ForkPlan == nil {
		if err := planRepoForkClone(app, request); err != nil {
			return err
		}
	}
	plan := *request.ForkPlan
	if request.DryRun {
		return renderCloneDryRun(app, *request)
	}
	if err := confirmRepoFork(app, plan, request.ForkYes); err != nil {
		if errors.Is(err, errPromptCanceled) {
			return errRepoForkCanceled
		}
		return err
	}
	provider, err := forge.For(forge.GitHub)
	if err != nil {
		return err
	}
	result, applyErr := repo.ApplyFork(ctxOf(), provider, plan)
	request.ForkResult = &result
	if applyErr != nil {
		if request.JSON {
			if err := renderRepoForkResult(app, "clone", result, applyErr); err != nil {
				return err
			}
		} else {
			renderForkPlan(app, plan)
			fmt.Fprintf(app.Out, "Completed: %v\n", result.Completed)
		}
		return applyErr
	}
	request.Destination = result.Plan.Path
	if result.Acquisition != nil {
		request.Name = result.Acquisition.Name
	}
	return nil
}

func confirmRepoFork(app *App, plan repo.ForkPlan, yes bool) error {
	if yes || plan.NoOp {
		return nil
	}
	if !app.interactive() {
		return errors.New("non-interactive fork requires --yes")
	}
	renderForkPlan(app, plan)
	confirmed, err := newPrompter(app).confirm("Apply this fork and remote configuration?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errPromptCanceled
	}
	return nil
}

func renderForkPlan(app *App, plan repo.ForkPlan) {
	fmt.Fprintf(app.Out, "  checkout    %s\n  origin      %s\n  upstream    %s\n", config.Contract(plan.Path), plan.ForkURL, plan.SourceURL)
	fmt.Fprintln(app.Out, "  push        origin (existing branch pull targets preserved)")
	for _, step := range plan.Steps {
		fmt.Fprintf(app.Out, "  - %s\n", step)
	}
}

func renderRepoForkResult(app *App, operation string, result repo.ForkResult, applyErr error) error {
	output := map[string]any{"operation": operation, "path": result.Plan.Path, "fork": result}
	if applyErr != nil {
		output["error"] = applyErr.Error()
	}
	return json.NewEncoder(app.Out).Encode(output)
}
