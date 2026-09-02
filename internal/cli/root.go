package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/skill"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags. VersionFromBuild also recovers a
// module version for `go install ...@version` builds.
var Version = "dev"

func versionFromBuild() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}

const workflowTLDR = `TL;DR: default managed-task loop

  dev start --> HOT: work / commit / test
                  ^               |
                  |  dev resume   |  dev park --next "..."
                  +------ WARM <--+
                  |
                  +-- direct:          dev done      --> DONE
                  +-- branch/worktree: dev done --ff --> DONE
                  +-- branch/worktree: dev done --pr --> push / review handoff
                                                        |
                          feedback --> resume if parked --> work

  DONE --> dev sweep (report) --> dev sweep --apply (reap) --> next task
  Remote merge detection and cleanup are not automatic; verify integration first.`

const rootLong = `dev is a thin glue layer over git, worktrees, forges and agent runtimes.

It exists to keep four things separate that otherwise collapse into one:

  git remote      durable code state, the source of truth
  git worktree    a disposable local checkout
  herdr / tmux / zellij
                  a per-host live runtime
  dev             human intent: what am I working on, and what is next

Everything derivable from git or the runtime is derived live. dev persists only
human intent Git cannot answer — task state/owner/next action and asset identity,
tags, notes and experiment lifecycle — so closing a runtime session or moving a
Try never means losing the thread. Transient pane data is not persisted.

` + workflowTLDR

// NewRootCommand builds the whole command tree writing to the process streams.
func NewRootCommand() *cobra.Command {
	return NewRootCommandWithIO(os.Stdout, os.Stderr)
}

// NewRootCommandWithIO builds the command tree writing to the given streams.
// Commands write through App rather than through cobra, so this is the seam
// tests use to capture output without redirecting the process's file
// descriptors.
func NewRootCommandWithIO(out, errOut io.Writer) *cobra.Command {
	return newRootCommand(&App{In: os.Stdin, Out: out, Err: errOut})
}

func newRootCommand(app *App) *cobra.Command {
	out, errOut := app.Out, app.Err
	root := &cobra.Command{
		Use:           "dev",
		Short:         "Manage repos, worktrees and work-in-progress across agent runtimes",
		Long:          rootLong,
		Version:       versionFromBuild(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          rejectRootArgs,
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
			if err := validateColorMode(app.colorMode); err != nil {
				return err
			}
			if completionInvocation(cmd) {
				return nil
			}
			finish := app.trace.Start(perftrace.AppLoad, perftrace.Fields{})
			if err := app.Load(); err != nil {
				finish(perftrace.OutcomeFailed)
				return err
			}
			finish(perftrace.OutcomeSuccess)
			deferRefresh := cmd.Name() == "tui" || (cmd.Parent() == nil && app.interactive())
			app.maybeNoteNewerRelease(cmd, deferRefresh)
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&app.configPath, "config", "", "path to config.toml (default: $XDG_CONFIG_HOME/dev/config.toml)")
	pf.StringVar(&app.remotesPath, "remotes", "", "path to remotes.toml (default: $XDG_CONFIG_HOME/dev/remotes.toml)")
	pf.StringVar(&app.scaffoldsPath, "scaffolds", "", "path to scaffolds.toml (default: $XDG_CONFIG_HOME/dev/scaffolds.toml)")
	pf.StringVar(&app.runtimeOverride, "runtime", "", "override runtime backend: herdr, tmux, zellij or none")
	pf.BoolVar(&app.noRuntime, "no-runtime", false, "do not touch any terminal multiplexer")
	pf.BoolVar(&app.allowSharedCheckout, "allow-shared-checkout", false,
		"allow a writer claim in a checkout occupied by another live agent")
	pf.StringVar(&app.colorMode, "color", colorAuto, "colorize human output: auto, always or never")
	registerFlagCompletion(root, "runtime", runtimeCompletions())
	registerFlagCompletion(root, "color", fixedCompletions(colorAuto, colorAlways, colorNever))

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
		newPrepareCmd(app),
		newDoneCmd(app),
		newRetireCmd(app),
		newArtifactCmd(app),
		newSweepCmd(app),
		newAdoptCmd(app),
		newBootstrapCmd(app),
		newWorktreeCmd(app),
		newRepoCmd(app),
		newFleetCmd(app),
		newSSHCmd(app),
		newGitCmd(app),
		newGitignoreCmd(app),
		newTryCmd(app),
		newTriesCmd(app),
		newGraduateCmd(app),
		newJournalCmd(app),
		newSummaryCmd(app),
		newStatsCmd(app),
		newHelpTopicCmd(app),
		newSkillCmd(app),
		newDoctorCmd(app),
		newVersionCmd(app),
		newUpgradeCmd(app),
		newShellInitCmd(app),
		newConfigCmd(app),
		newCacheCmd(app),
		newNoteCmd(app),
		newEditCmd(app),
	)
	root.SetHelpCommand(&cobra.Command{Hidden: true, Use: "no-help"})
	// Inherited by every subcommand, so a bad flag anywhere in the tree is
	// reported as a usage mistake rather than a bare error line.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return asUsageError(err)
	})
	root.SetOut(out)
	root.SetErr(errOut)
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		var buf bytes.Buffer
		previous := cmd.OutOrStdout()
		cmd.SetOut(&buf)
		defaultHelp(cmd, args)
		cmd.SetOut(previous)
		fmt.Fprint(previous, renderCobraHelp(buf.String(), app.outStyle()))
	})
	enforceUsageErrors(root)
	annotateHelp(root)
	return root
}

// Execute runs the CLI and maps errors to exit codes. SilenceErrors and
// SilenceUsage are set on the root, so nothing reaches the terminal unless it
// is printed here; ExecuteC hands back the command that failed so a usage
// mistake can be answered with that command's usage rather than the root's.
func Execute() int {
	trace, tracePath, traceErr := traceFromEnvironment()
	app := &App{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		trace: trace, tracePath: tracePath, traceOnce: &sync.Once{},
	}
	if traceErr != nil {
		fmt.Fprintf(os.Stderr, "dev: warning: performance trace disabled: %v\n", traceErr)
	}
	app.trace.Mark(perftrace.CLIExecuteBegin, perftrace.Fields{})
	defer app.finishTrace()

	sweepStaleUpgradeArtifacts()
	finishRoot := app.trace.Start(perftrace.CLIRootBuild, perftrace.Fields{})
	root := newRootCommand(app)
	finishRoot(perftrace.OutcomeSuccess)
	cmd, err := root.ExecuteC()
	if err == nil {
		return 0
	}
	style := styleForWriter(os.Stderr, colorModeFromArgs(os.Args[1:]))
	fmt.Fprintln(os.Stderr, style.danger("dev:")+" "+err.Error())
	if cmd != nil && wantsUsage(err) {
		fmt.Fprint(os.Stderr, renderCobraHelp(cmd.UsageString(), style))
	}
	return 1
}
