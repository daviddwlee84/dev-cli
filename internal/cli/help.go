package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/help"
	"github.com/spf13/cobra"
)

func newHelpTopicCmd(app *App) *cobra.Command {
	var tree, aliases bool
	var depth int
	cmd := &cobra.Command{
		Use:   "help [topic|command path]",
		Short: "Guides to repository workflows, policies, and tradeoffs",
		Long: `Practical guides to repository work: when to branch, what a commit should
contain, where agent history belongs, who owns each worktree, and how to hand
work to another machine.

Run without an argument to see the guide index. --tree shows the current
command tree, optionally rooted at a command path. --depth is relative to that
root and defaults to two levels; zero prints every level. --aliases lists the
supported shortcuts and their canonical destinations.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if depth < 0 {
				return fmt.Errorf("--depth must be zero or positive")
			}
			if !tree && cmd.Flags().Changed("depth") {
				return fmt.Errorf("--depth requires --tree")
			}
			if !tree && !aliases {
				return cobra.MaximumNArgs(1)(cmd, args)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if tree || aliases {
				selected, err := resolveHelpCommand(cmd.Root(), args)
				if err != nil {
					return err
				}
				if tree {
					renderCommandTree(app, selected, depth)
				}
				if aliases {
					if tree {
						fmt.Fprintln(app.Out)
					}
					renderCommandAliases(app, cmd.Root(), selected)
				}
				return nil
			}
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
			} else if selected, findErr := resolveHelpCommand(cmd.Root(), strings.Fields(name)); findErr == nil {
				if mapped, ok := helpTopics[canonicalCommandPath(selected)]; ok {
					name = mapped
				} else {
					return selected.Help()
				}
			}
			topic, err := help.Get(name)
			if err != nil {
				return err
			}
			fmt.Fprint(app.Out, renderMarkdown(topic.Body, app.outStyle()))
			return nil
		},
	}
	cmd.Flags().BoolVar(&tree, "tree", false, "show the canonical command tree")
	cmd.Flags().IntVar(&depth, "depth", 2, "tree levels below the selected command; 0 means unlimited")
	cmd.Flags().BoolVar(&aliases, "aliases", false, "show supported shortcuts and their canonical destinations")
	cmd.ValidArgsFunction = completeHelp
	return cmd
}

// Resolve command tokens directly rather than letting Find interpret flags or
// silently return an unmatched tail. Internal helpers are never documentation.
func resolveHelpCommand(root *cobra.Command, args []string) (*cobra.Command, error) {
	selected := root
	if len(args) > 0 && args[0] == root.Name() {
		args = args[1:]
	}
	for _, name := range args {
		var next *cobra.Command
		for _, child := range selected.Commands() {
			if child.Hidden && child.Annotations[shortcutTargetAnnotation] == "" {
				continue
			}
			if child.Name() == name || child.HasAlias(name) {
				next = child
				break
			}
		}
		if next == nil {
			return nil, unknownSubcommand(selected, name)
		}
		selected = next
	}
	if canonical := canonicalCommandPath(selected); canonical != selected.CommandPath() {
		return resolveHelpCommand(root, strings.Fields(canonical))
	}
	return selected, nil
}

func renderCommandTree(app *App, root *cobra.Command, depth int) {
	fmt.Fprintf(app.Out, "%s  %s\n", root.CommandPath(), root.Short)
	var visit func(*cobra.Command, int)
	visit = func(parent *cobra.Command, level int) {
		if depth != 0 && level > depth {
			return
		}
		for _, child := range parent.Commands() {
			if child.Hidden {
				continue
			}
			fmt.Fprintf(app.Out, "%s%s  %s\n", strings.Repeat("  ", level), child.Name(), child.Short)
			visit(child, level+1)
		}
	}
	visit(root, 1)
}

func renderCommandAliases(app *App, root, selected *cobra.Command) {
	fmt.Fprintln(app.Out, "Shortcuts (remain supported):")
	prefix := selected.CommandPath()
	for _, alias := range commandAliases(root) {
		if selected != root && alias.to != prefix && !strings.HasPrefix(alias.to, prefix+" ") {
			continue
		}
		fmt.Fprintf(app.Out, "  %s -> %s", alias.from, alias.to)
		if alias.detail != "" {
			fmt.Fprintf(app.Out, " (%s)", alias.detail)
		}
		fmt.Fprintln(app.Out)
	}
}
