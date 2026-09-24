package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newGraduateCmd(app *App) *cobra.Command { return newGraduateCmdWithUse(app, "graduate [try]") }

func newGraduateCmdWithUse(app *App, use string) *cobra.Command {
	flags := defaultGraduateFlags()
	cmd := &cobra.Command{
		Use:   use,
		Short: "Promote an experiment into a real project",
		Long: `Move a Try into <project_root>/<category>/<name>, preserving its catalog
identity, tags, notes, provenance and current files. --name sets the project
name; [try] always selects the source. With no argument, use the current Try.

Interactive terminals review the name, category and optional upstream in a
wizard. --yes and non-interactive calls use the supplied flags directly.
--dry-run never prompts, probes authentication, moves files or publishes.

Existing remotes are preserved. For a Try without remotes, --remote creates a
GitHub/GitLab repository; --remote-url attaches an existing URL as origin.
Creation defaults to private with a push; attaching a URL only pushes when
--push is explicitly supplied. A publication failure keeps the graduated local
project and reports a nonzero partial result without retrying the creation.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.pushSet = cmd.Flags().Changed("push")
			flags.privateSet = cmd.Flags().Changed("private")
			flags.urlSet = cmd.Flags().Changed("remote-url")
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			err := runGraduateWorkflow(cmd.Context(), app, ref, nil, flags, false)
			if errors.Is(err, errPromptCanceled) {
				fmt.Fprintln(app.Out, "Canceled; nothing was graduated.")
				return nil
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVarP(&flags.category, "category", "c", "", "category subdirectory under project_root")
	f.StringVar(&flags.name, "name", "", "project name (default: remembered name, otherwise the Try name without its date prefix)")
	f.BoolVar(&flags.upstream.private, "private", true, "create a private upstream (compatibility flag)")
	f.BoolVar(&flags.upstream.remote, "remote", false, "create a GitHub or GitLab upstream")
	f.BoolVar(&flags.upstream.push, "push", true, "push current branch commits (creation: true; existing URL: false unless explicitly set)")
	f.StringVar(&flags.remoteURL, "remote-url", "", "add an existing repository URL as origin without creating a remote repository")
	f.StringVar(&flags.upstream.forge, "forge", "auto", "upstream provider: auto, github, gitlab or none; github/gitlab selects creation")
	f.StringVar(&flags.upstream.namespace, "namespace", "", "GitHub owner/org or GitLab namespace for upstream creation")
	f.StringVar(&flags.upstream.visibility, "visibility", "", "upstream visibility: private, public, or internal (GitLab only)")
	f.BoolVarP(&flags.yes, "yes", "y", false, "use the supplied options without the interactive wizard")
	f.BoolVar(&flags.dryRun, "dry-run", false, "preview without prompting, applying or probing forge authentication")
	registerFlagCompletion(cmd, "forge", fixedCompletions("auto", "github", "gitlab", "none"))
	registerFlagCompletion(cmd, "visibility", fixedCompletions("private", "public", "internal"))
	return cmd
}
