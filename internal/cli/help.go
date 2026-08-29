package cli

import (
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/help"
	"github.com/spf13/cobra"
)

func newHelpTopicCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "help [topic]",
		Short: "Quick-reference pages for the git workflow dev assumes",
		Long: `Short answers to the questions this workflow keeps raising: when to branch,
what a commit should contain, who owns which worktree, how to hand work to
another machine.

Run without an argument to see the index.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprintln(app.Out, workflowTLDR)
				fmt.Fprintln(app.Out)
				all, err := help.List()
				if err != nil {
					return err
				}
				t := app.newTable("TOPIC", "ABOUT")
				for _, topic := range all {
					t.Add(topic.Name, truncate(topic.Summary, 68))
				}
				t.Render(app.Out)
				fmt.Fprintln(app.Err, "\nRead one with: dev help <topic>")
				return nil
			}
			name := args[0]
			// A user who just read `dev wt --help` reaches for `dev help wt`.
			// Resolve command names and aliases before falling back to the
			// filename match, which would only fail on them.
			if mapped, ok := topicForCommand(name); ok {
				name = mapped
			}
			topic, err := help.Get(name)
			if err != nil {
				return err
			}
			fmt.Fprint(app.Out, renderMarkdown(topic.Body, app.outStyle()))
			return nil
		},
	}
	cmd.ValidArgsFunction = completeHelpTopics
	return cmd
}
