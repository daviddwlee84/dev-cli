package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/desktop"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/repobrowse"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newRepoBrowseCmd(app *App) *cobra.Command {
	var remote string
	var printOnly bool
	cmd := &cobra.Command{Use: "browse [repo-or-path]", Short: "Open the repository homepage, or print its URL without opening", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, path, err := resolveRepoContextTarget(cmd.Context(), app, args)
		if err != nil {
			return err
		}
		return browseRepository(cmd.Context(), app, path, remote, printOnly)
	}}
	cmd.Flags().StringVar(&remote, "remote", "", "select a configured Git remote")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the HTTPS URL without opening a browser")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func browseRepository(ctx context.Context, app *App, path, remote string, printOnly bool) error {
	choices, err := repobrowse.Choices(ctx, path, remote)
	if err != nil {
		return err
	}
	if len(choices) > 1 {
		if !app.interactive() || printOnly {
			return fmt.Errorf("choose --remote from: %s", repobrowse.Names(choices))
		}
		for _, choice := range choices {
			fmt.Fprintf(app.Out, "  %s  %s\n", choice.Remote, choice.URL)
		}
		p := newPrompter(app)
		for len(choices) > 1 {
			name, err := p.line("Remote", "")
			if err != nil {
				return err
			}
			for _, choice := range choices {
				if choice.Remote == name {
					choices = []repobrowse.Choice{choice}
					break
				}
			}
			if len(choices) > 1 {
				fmt.Fprintln(app.Out, "Choose an exact remote name from: "+repobrowse.Names(choices))
			}
		}

	}
	if len(choices) != 1 {
		return fmt.Errorf("no unambiguous repository web URL")
	}
	fmt.Fprintln(app.Out, choices[0].URL)
	if printOnly {
		return nil
	}
	return desktop.OpenURL(ctx, choices[0].URL)
}

func runBrowseWorkflow(ctx context.Context, app *App, request tui.WorkflowRequest) error {
	if request.Remote != nil {
		web, ok := forge.DeriveWebURL(forge.WebURLRequest{Exact: request.Remote})
		if !ok {
			return fmt.Errorf("selected remote has no supported repository web URL")
		}
		return desktop.OpenURL(ctx, web.URL)
	}
	return browseRepository(ctx, app, request.Path, "", false)
}
