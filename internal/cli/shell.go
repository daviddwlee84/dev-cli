package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// zshInit and friends define the wrapper that makes `dev resume` able to leave
// the user *inside* the checkout.
//
// A process can never change its parent's working directory, so dev prints a
// `cd <path>` directive on stdout and the wrapper evaluates it. This is the
// same mechanism try-cli uses, and it is why `dev` is a shell function rather
// than only a binary.
const posixInit = `# dev shell integration — add to your rc file:
#   eval "$(dev shell-init %[2]s)"
export DEV_SHELL_INIT=1
dev() {
  local __dev_out __dev_status __dev_last __dev_before
  __dev_out="$(command %[1]s "$@")"
  __dev_status=$?
  __dev_last="${__dev_out##*$'\n'}"
  if [ $__dev_status -eq 0 ] && [ "${__dev_last#cd }" != "$__dev_last" ]; then
    if [ "$__dev_out" != "$__dev_last" ]; then
      __dev_before="${__dev_out%%$'\n'*}"
      [ -n "$__dev_before" ] && printf '%%s\n' "$__dev_before"
    fi
    eval "$__dev_last"
  elif [ -n "$__dev_out" ]; then
    printf '%%s\n' "$__dev_out"
  fi
  return $__dev_status
}
`

const fishInit = `# dev shell integration — add to config.fish:
#   dev shell-init fish | source
set -gx DEV_SHELL_INIT 1
function dev
    set -l __dev_out (command %[1]s $argv)
    set -l __dev_status $status
    set -l __dev_last $__dev_out[-1]
    if test $__dev_status -eq 0; and string match -q 'cd *' -- "$__dev_last"
        if test (count $__dev_out) -gt 1
            printf '%%s\n' $__dev_out[1..-2]
        end
        eval $__dev_last
    else if test (count $__dev_out) -gt 0
        printf '%%s\n' $__dev_out
    end
    return $__dev_status
end
`

func newShellInitCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell-init <bash|zsh|fish>",
		Short: "Print the shell wrapper that lets dev change your directory",
		Long: `Print a shell function that wraps the dev binary.

dev commands that move you into a checkout (resume, try, wt open with the
"none" runtime) print a "cd <path>" directive rather than trying to change
directory themselves, because a child process cannot change its parent's
working directory. The wrapper evaluates that directive.

Add to your shell rc file:

    eval "$(dev shell-init zsh)"          # bash and zsh
    dev shell-init fish | source          # fish`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			self, err := os.Executable()
			if err != nil || self == "" {
				self = "dev"
			}
			switch args[0] {
			case "bash", "zsh":
				fmt.Fprintf(app.Out, posixInit, shellQuote(self), args[0])
			case "fish":
				fmt.Fprintf(app.Out, fishInit, shellQuote(self))
			default:
				return fmt.Errorf("unsupported shell %q: want bash, zsh or fish", args[0])
			}
			return nil
		},
	}
	// Completion is a sibling concern but belongs to the same "wire dev into
	// my shell" moment, so it hangs off this command.
	cmd.AddCommand(&cobra.Command{
		Use:       "completion <bash|zsh|fish>",
		Short:     "Print shell completions",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(app.Out, true)
			case "zsh":
				return root.GenZshCompletion(app.Out)
			case "fish":
				return root.GenFishCompletion(app.Out, true)
			}
			return fmt.Errorf("unsupported shell %q", args[0])
		},
	})
	return cmd
}
