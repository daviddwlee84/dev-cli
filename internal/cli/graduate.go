package cli

import (
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/spf13/cobra"
)

func newGraduateCmd(app *App) *cobra.Command {
	return newGraduateCmdWithUse(app, "graduate [try]")
}

func newGraduateCmdWithUse(app *App, use string) *cobra.Command {
	var (
		category string
		name     string
		private  bool
		remote   bool
		push     bool
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   use,
		Short: "Promote an experiment into a real project",
		Long: `Move a try out of the scratch directory and into your projects tree.

An experiment that turns out to matter should stop being an experiment, and
that transition is where things usually get lost — the directory keeps its date
prefix forever, or it gets copied by hand and the history is left behind.

graduate moves the directory to <project_root>/<category>/<name>, drops the
date prefix, makes sure it has a git repo and a first commit, and optionally
creates the remote with gh or glab.

With no argument, the try containing the current directory is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			request := experiment.GraduateRequest{
				Ref: ref, Category: category, Name: name, DryRun: dryRun,
			}
			plan, err := service.PlanGraduate(ctx, request)
			warnExperimentDiagnostics(app, plan.Diagnostics)
			if err != nil {
				return err
			}

			fmt.Fprintf(app.Out, "graduate  %s\n", config.Contract(plan.Source))
			fmt.Fprintf(app.Out, "       →  %s\n", config.Contract(plan.Destination))
			if dryRun {
				fmt.Fprintln(app.Out, "\n(dry run — nothing moved)")
				return nil
			}

			result, err := service.Graduate(ctx, request)
			if err != nil {
				return err
			}
			if result.GitInitialized {
				fmt.Fprintln(app.Out, "   git init")
			}
			if result.InitialCommitMade {
				fmt.Fprintln(app.Out, "   committed the existing work")
			}

			if remote {
				adapter, err := forge.Preferred()
				if err != nil {
					app.warnf("%v — the project is in place; add a remote by hand", err)
				} else {
					remoteURL, createErr := adapter.CreateRepo(ctx, result.Plan.Destination, forge.RepoRequest{
						Name: result.Plan.Name, Private: private, Push: push,
					})
					if createErr != nil {
						app.warnf("could not create the remote: %v", createErr)
					} else {
						fmt.Fprintf(app.Out, "   remote    %s\n", remoteURL)
					}
					// A forge command can add origin successfully and then fail while
					// pushing. Refresh after every attempt so that partial success does not
					// leave catalog provenance stale; a wholly failed create needs no
					// second warning about its expected missing origin.
					if refreshed, refreshErr := service.RefreshOrigin(ctx, result.Item.ID); refreshErr == nil {
						result.Item = refreshed
					} else if createErr == nil {
						app.warnf("remote was created, but catalog origin metadata could not be refreshed: %v", refreshErr)
					}
				}
			}

			fmt.Fprintf(app.Out, "\n%s is now a project. Start work on it with:\n  dev start %s --task <name>\n",
				result.Plan.Name, result.Plan.Name)
			target := result.Item.OpenTarget()
			if err := openOrCD(app, ctx, target.Path, result.Plan.Name); err != nil {
				return err
			}
			_, err = service.Touch(ctx, result.Item.ID)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&category, "category", "c", "", "category subdirectory under project_root")
	flags.StringVar(&name, "name", "", "project name (default: the try name without its date prefix)")
	flags.BoolVar(&private, "private", true, "create the remote as private")
	flags.BoolVar(&remote, "remote", false, "create a remote repository with gh or glab")
	flags.BoolVar(&push, "push", true, "push after creating the remote")
	flags.BoolVar(&dryRun, "dry-run", false, "show what would happen without moving anything")
	return cmd
}
