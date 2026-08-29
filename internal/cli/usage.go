package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// usageError marks a failure the user can fix by reading the command's usage:
// an unknown flag, the wrong number of arguments, a mistyped subcommand. It is
// a type rather than a string prefix so Execute never has to guess from the
// message text which errors deserve a usage block.
//
// showUsage is false for a mistyped command name. Its usage block is the whole
// command list, which buries the one line that actually helps — the suggestion
// already carried in the message — so those get a pointer to --help instead.
type usageError struct {
	err       error
	showUsage bool
}

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func asUsageError(err error) error {
	if err == nil {
		return nil
	}
	var already *usageError
	if errors.As(err, &already) {
		return err
	}
	return &usageError{err: err, showUsage: true}
}

// wantsUsage reports whether the error should be answered with the failing
// command's usage block.
func wantsUsage(err error) bool {
	var target *usageError
	return errors.As(err, &target) && target.showUsage
}

// unknownSubcommand reproduces cobra's own wording, including its suggestions.
// Cobra computes them for the root command and then throws them away when
// SilenceErrors is set, and never computes them at all for a command family,
// so dev raises the error itself and keeps the help.
func unknownSubcommand(cmd *cobra.Command, name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown command %q for %q", name, cmd.CommandPath())
	if suggestions := cmd.SuggestionsFor(name); len(suggestions) > 0 {
		b.WriteString("\n\nDid you mean this?\n")
		for _, s := range suggestions {
			fmt.Fprintf(&b, "\t%s\n", s)
		}
	}
	fmt.Fprintf(&b, "\nRun %q for the command list.", cmd.CommandPath()+" --help")
	return &usageError{err: errors.New(b.String())}
}

// groupRunE gives a command family a real Run. Without one, cobra returns
// flag.ErrHelp for `dev wt anything`, ExecuteC turns that into a successful
// help render, and the stray argument disappears with exit code 0.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	return unknownSubcommand(cmd, args[0])
}

// enforceUsageErrors walks the assembled tree once so every node reports a
// mistake the same way. It must run after the last AddCommand.
func enforceUsageErrors(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		enforceUsageErrors(child)
	}

	// A family node with no Run of its own silently swallows a stray argument.
	if cmd.HasSubCommands() && !cmd.Runnable() {
		cmd.RunE = groupRunE
	}

	// Argument validators report real usage mistakes; tag them so the caller
	// can show usage without matching on cobra's message wording.
	if inner := cmd.Args; inner != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			return asUsageError(inner(c, args))
		}
	}
}

// rejectRootArgs replaces the argument validation cobra applies to a root
// command that has subcommands. The behaviour is the same — a positional
// argument is an unknown command — but the error carries suggestions and is
// tagged for the usage block.
func rejectRootArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return unknownSubcommand(cmd, args[0])
}
