package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/skill"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

const rootLong = `dev is a thin glue layer over git, worktrees, forges and agent runtimes.

It exists to keep three things separate that otherwise collapse into one:

  git remote      durable code state, the source of truth
  git worktree    a disposable local checkout
  herdr / tmux    a per-host live runtime
  dev             human intent: what am I working on, and what is next

Everything derivable from git or the runtime is derived live. dev persists only
what git cannot answer — a task's state, its owner and its next action — so
closing a runtime session never means losing the thread.`

// NewRootCommand builds the whole command tree writing to the process streams.
func NewRootCommand() *cobra.Command {
	return NewRootCommandWithIO(os.Stdout, os.Stderr)
}

// NewRootCommandWithIO builds the command tree writing to the given streams.
// Commands write through App rather than through cobra, so this is the seam
// tests use to capture output without redirecting the process's file
// descriptors.
func NewRootCommandWithIO(out, errOut io.Writer) *cobra.Command {
	app := &App{Out: out, Err: errOut}

	root := &cobra.Command{
		Use:           "dev",
		Short:         "Manage repos, worktrees and work-in-progress across agent runtimes",
		Long:          rootLong,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bare `dev` shows the inventory, which is the question the tool
		// exists to answer.
		RunE: func(cmd *cobra.Command, args []string) error {
			if printSkill, _ := cmd.Flags().GetBool("skill"); printSkill {
				out, err := skill.Render()
				if err != nil {
					return err
				}
				fmt.Fprint(app.Out, out)
				return nil
			}
			// A terminal gets the dashboard; a pipe gets the listing, so
			// `dev | grep` and `dev > file` behave as expected.
			if interactive() {
				return runTUI(app)
			}
			return runList(app, listOptions{})
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return app.Load()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&app.configPath, "config", "", "path to config.toml (default: $XDG_CONFIG_HOME/dev/config.toml)")
	pf.StringVar(&app.runtimeOverride, "runtime", "", "override runtime backend: herdr, tmux or none")
	pf.BoolVar(&app.noRuntime, "no-runtime", false, "do not touch any terminal multiplexer")

	// Mirrors `herdr --skill`: the binary is the authority for its own agent
	// skill, so a dotfiles installer can sync it without vendoring a copy.
	root.Flags().Bool("skill", false, "print the bundled agent skill and exit")

	root.AddCommand(
		newListCmd(app),
		newTUICmd(app),
		newStatusCmd(app),
		newStartCmd(app),
		newParkCmd(app),
		newResumeCmd(app),
		newDoneCmd(app),
		newSweepCmd(app),
		newWorktreeCmd(app),
		newRepoCmd(app),
		newTryCmd(app),
		newGraduateCmd(app),
		newStatsCmd(app),
		newHelpTopicCmd(app),
		newSkillCmd(app),
		newDoctorCmd(app),
		newShellInitCmd(app),
		newConfigCmd(app),
	)
	root.SetHelpCommand(&cobra.Command{Hidden: true, Use: "no-help"})
	root.SetOut(out)
	root.SetErr(errOut)
	return root
}

// Execute runs the CLI and maps errors to exit codes.
func Execute() int {
	root := NewRootCommand()
	if err := root.Execute(); err != nil {
		msg := err.Error()
		// cobra already printed usage errors in a readable form.
		if !strings.HasPrefix(msg, "unknown command") {
			fmt.Fprintln(os.Stderr, "dev: "+msg)
		}
		return 1
	}
	return 0
}
