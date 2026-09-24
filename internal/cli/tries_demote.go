package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newTriesDemoteCmd(app *App) *cobra.Command {
	var to string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "demote <repo-or-path-or-catalog-id>",
		Short: "Move a previously graduated project back to Tries",
		Long: `Move the current project back to its original Try path, or an explicit --to
destination under tries_root. Only projects that previously graduated are eligible.
Files, Git history, remotes, notes and catalog identity are preserved. This does
not undo commits, Git initialization or remote publication. Leave the checkout
and resolve task, runtime and pending artifact claims before moving it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newDemoteService(app)
			if err != nil {
				return err
			}
			if to != "" {
				to = config.Expand(to)
			}
			plan, err := service.PlanDemote(cmd.Context(), experiment.DemoteRequest{Ref: args[0], To: to, DryRun: dryRun})
			if err != nil {
				return err
			}
			renderDemotePlan(app, plan)
			if dryRun {
				fmt.Fprintln(app.Out, "\n(dry run - nothing moved)")
				return nil
			}
			return applyDemote(cmd.Context(), app, service, plan)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "explicit unoccupied destination directly under tries_root")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the guarded move without changing files or catalog metadata")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func newDemoteService(app *App) (*experiment.Service, error) {
	guard := taskflow.MoveClaims{
		Tasks: app.Tasks, Artifacts: artifactStore(app), Runtimes: func() []runtime.Runtime { return demoteRuntimes(app) },
		ProtectedPaths: []string{app.Cfg.StateDir()},
	}
	return experiment.NewService(experiment.ServiceConfig{
		Registry: app.Registry, Store: app.Catalog,
		TriesRoot: config.Expand(app.Cfg.Paths.TriesRoot), ProjectRoot: config.Expand(app.Cfg.Paths.ProjectRoot),
		Host: config.Hostname(),
		Hooks: experiment.Hooks{
			DemoteGuard: func(ctx context.Context, request experiment.DemoteGuardRequest) (string, error) {
				return guard.Inspect(ctx, request.Source, request.Destination)
			},
			DemoteLock: guard.WithLock,
		},
	})
}

func demoteRuntimes(app *App) []runtime.Runtime {
	if app.noRuntime || app.runtimeOverride == "none" || (app.runtimeOverride == "" && app.Cfg.Runtime.Backend == "none") {
		return nil
	}
	candidates := []runtime.Runtime{app.Runtime()}
	for _, backend := range runtime.All() {
		if backend.Name() != "none" {
			candidates = append(candidates, app.runtimeNamed(backend.Name()))
		}
	}
	seen := map[string]bool{}
	var available []runtime.Runtime
	for _, backend := range candidates {
		if backend != nil && backend.Name() != "none" && !seen[backend.Name()] && backend.Available() {
			seen[backend.Name()] = true
			available = append(available, backend)
		}
	}
	return available
}

func renderDemotePlan(app *App, plan experiment.DemotePlan) {
	fmt.Fprintf(app.Out, "demote %s\n  from  %s\n  to    %s\n", plan.ID, config.Contract(plan.Source), config.Contract(plan.Destination))
	fmt.Fprintln(app.Out, "  Preserve current files, Git history, remotes and catalog identity.")
}

func applyDemote(ctx context.Context, app *App, service *experiment.Service, plan experiment.DemotePlan) error {
	result, err := service.ApplyDemote(ctx, plan)
	if err != nil {
		if result.RolledBack {
			fmt.Fprintln(app.Err, "Demote failed; the directory move was rolled back.")
		} else if result.Moved {
			fmt.Fprintf(app.Err, "The directory moved to %s; catalog recovery is required.\n", config.Contract(plan.Destination))
		}
		return err
	}
	fmt.Fprintf(app.Out, "\n%s is an active Try at %s\n", plan.ID, config.Contract(result.Item.CurrentPath()))
	return nil
}

func runRepoDemoteWorkflow(ctx context.Context, app *App, request tui.WorkflowRequest) error {
	selected := request.RepoAsset
	if selected == nil || selected.Kind != catalog.KindRepository || selected.Experiment == nil || selected.Experiment.Phase != catalog.PhaseGraduated {
		return errors.New("select a previously graduated Try to demote")
	}
	location, ok := selected.LocationFor(config.Hostname())
	if !ok || !sameCleanPath(location.CurrentPath, request.Repo.Path) {
		return errors.New("selected repository no longer matches its catalog location; refresh the dashboard")
	}
	service, err := newDemoteService(app)
	if err != nil {
		return err
	}
	p := newPrompter(app)
	fmt.Fprintf(app.Out, "Demote %s back to Tries\nOriginal Try path: %s\n", selected.Name, config.Contract(selected.Experiment.OriginalPath))
	to, err := p.line("Destination (blank uses the original Try path)", "")
	if err != nil {
		return err
	}
	if to != "" {
		to = config.Expand(to)
	}
	plan, err := service.PlanDemote(ctx, experiment.DemoteRequest{Ref: selected.ID, To: to, Expected: selected})
	if err != nil {
		return err
	}
	renderDemotePlan(app, plan)
	confirmed, err := p.confirm("Move this project back to Tries?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errPromptCanceled
	}
	return applyDemote(ctx, app, service, plan)
}
