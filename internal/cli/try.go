package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/spf13/cobra"
)

func newTryCmd(app *App) *cobra.Command {
	var (
		list  bool
		clone string
		noGit bool
	)
	cmd := &cobra.Command{
		Use:   "try [name]",
		Short: "Make a dated scratch directory for an experiment",
		Long: `Create (or jump to) a dated experiment directory under paths.tries_root.

Experiments need a home that is neither /tmp (where they vanish) nor your
projects directory (where they accumulate as clutter you are afraid to delete).
A try is date-prefixed, disposable by default, and promotable with
"dev graduate" when it turns out to be real.

  dev try                    list what is there
  dev try redis-streams      jump to a matching try, or create 2026-08-27-redis-streams
  dev try --clone <url>      clone a repo into a dated try directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			service, err := newExperimentService(app)
			if err != nil {
				return err
			}
			root := config.Expand(app.Cfg.Paths.TriesRoot)
			if list || (len(args) == 0 && clone == "") {
				return listTries(app, ctx, service, root)
			}

			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			result, err := service.ResolveOrCreate(ctx, experiment.CreateRequest{
				Name: name, Clone: clone, NoGit: noGit,
			})
			warnExperimentDiagnostics(app, result.Diagnostics)
			if result.InitWarning != nil {
				app.warnf("could not git init: %v", result.InitWarning)
			}
			if err != nil {
				return err
			}

			target := result.OpenTarget()
			fmt.Fprintf(app.Out, "%s\n", config.Contract(target.Path))
			if err := openOrCD(app, ctx, target.Path, target.Label); err != nil {
				return err
			}
			_, err = service.Touch(ctx, target.CatalogID)
			return err
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&list, "list", "l", false, "list existing tries")
	flags.StringVar(&clone, "clone", "", "clone a repository into the new try")
	flags.BoolVar(&noGit, "no-git", false, "do not git init the new directory")
	return cmd
}

func newExperimentService(app *App) (*experiment.Service, error) {
	return experiment.NewService(experiment.ServiceConfig{
		Registry:    app.Registry,
		Store:       app.Catalog,
		TriesRoot:   config.Expand(app.Cfg.Paths.TriesRoot),
		ProjectRoot: config.Expand(app.Cfg.Paths.ProjectRoot),
		Host:        config.Hostname(),
		Clock:       time.Now,
	})
}

func listTries(app *App, ctx context.Context, service *experiment.Service, root string) error {
	items, diagnostics, err := service.List(ctx, experiment.ListOptions{})
	warnExperimentDiagnostics(app, diagnostics)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(app.Out, "No tries yet under %s\n", config.Contract(root))
		return nil
	}

	table := NewTable("TRY", "AGE", "GIT")
	for _, item := range items {
		gitColumn := "—"
		switch {
		case item.Live.Repo != nil && item.Live.Status != nil && item.Live.Status.Dirty():
			gitColumn = "repo ●"
		case item.Live.Repo != nil && item.Live.StatusError != nil:
			gitColumn = "repo ?"
		case item.Live.Repo != nil:
			gitColumn = "repo"
		case item.Live.DiscoverError != nil:
			gitColumn = "error"
		}
		age := "unknown"
		if activity := item.Activity(); !activity.IsZero() {
			age = humanAge(time.Since(activity))
		}
		table.Add(truncate(item.DisplayName(), 44), age, gitColumn)
	}
	table.Render(app.Out)
	fmt.Fprintf(app.Err, "\n%d tries under %s — promote one with `dev graduate <try>`\n",
		len(items), config.Contract(root))
	return nil
}

func warnExperimentDiagnostics(app *App, diagnostics []experiment.Diagnostic) {
	for _, diagnostic := range diagnostics {
		app.warnf("%s", diagnostic.Error())
	}
}

func openOrCD(app *App, ctx context.Context, dir, label string) error {
	runtime := app.Runtime()
	if runtime.Name() == "none" {
		return app.cdDirective(dir)
	}
	opened, err := openCheckout(ctx, runtime, dir, label)
	if err != nil {
		return fmt.Errorf("open runtime session: %w", err)
	}
	return activateRuntime(ctx, runtime, opened.Handle)
}
