package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// posixInit and fishInit define the wrapper that makes `dev resume` able to
// leave the user *inside* the checkout. The binary keeps its real stdout so a
// terminal still opens the TUI and a pipe still receives plain output; a
// private file carries only the requested parent-directory change.
const posixInit = `# dev shell integration — add to your rc file:
#   eval "$(dev shell-init %[2]s)"
export DEV_SHELL_INIT=1
dev() {
  # Commands outside the navigation surface need no side channel. Besides
  # avoiding two subprocesses, this keeps them working when TMPDIR is stale.
  case "${1:-}" in
    __complete|__completeNoDesc|--help|-h|--version|--skill|completion|config|doctor|done|edit|gitignore|ignore|help|list|ls|park|skill|shell-init|cache|stats|status|sweep|adopt|bootstrap)
      command %[1]s "$@" || return $?
      return 0
      ;;
  esac

  local __dev_cd_file __dev_dir __dev_status
  if ! __dev_cd_file="$(mktemp "${TMPDIR:-/tmp}/dev-cd.XXXXXX" 2>/dev/null)"; then
    __dev_cd_file="$(mktemp "/tmp/dev-cd.XXXXXX" 2>/dev/null)" || {
      printf 'dev: cannot create the shell directory side channel\n' >&2
      return 1
    }
  fi

  __dev_status=0
  DEV_SHELL_CD_FD=3 command %[1]s "$@" 3>"$__dev_cd_file" || __dev_status=$?
  if [ $__dev_status -eq 0 ] && [ -s "$__dev_cd_file" ]; then
    if IFS= read -r -d '' __dev_dir < "$__dev_cd_file"; then
      if [ -n "$__dev_dir" ]; then
        builtin cd -- "$__dev_dir" || __dev_status=$?
      fi
    else
      __dev_status=1
    fi
  fi
  rm -f -- "$__dev_cd_file" || :
  return $__dev_status
}
`

const fishInit = `# dev shell integration — add to config.fish:
#   dev shell-init fish | source
set -gx DEV_SHELL_INIT 1
function dev
    set -l __dev_command ""
    if test (count $argv) -gt 0
        set __dev_command $argv[1]
    end
    if contains -- "$__dev_command" __complete __completeNoDesc --help -h --version --skill completion config doctor done edit gitignore ignore help list ls park skill stats status sweep adopt bootstrap
        command %[1]s $argv
        return $status
    end

    set -l __dev_tmpdir /tmp
    set -q TMPDIR; and set __dev_tmpdir $TMPDIR
    set -l __dev_cd_file (mktemp "$__dev_tmpdir/dev-cd.XXXXXX" 2>/dev/null)
    if test $status -ne 0
        set __dev_cd_file (mktemp "/tmp/dev-cd.XXXXXX" 2>/dev/null)
    end
    if test -z "$__dev_cd_file"
        printf 'dev: cannot create the shell directory side channel\n' >&2
        return 1
    end

    set -lx DEV_SHELL_CD_FD 3
    command %[1]s $argv 3>"$__dev_cd_file"
    set -l __dev_status $status
    if test $__dev_status -eq 0; and test -s "$__dev_cd_file"
        if read -z -l __dev_dir < "$__dev_cd_file"
            if test -n "$__dev_dir"
                builtin cd -- "$__dev_dir"
                set __dev_status $status
            end
        else
            set __dev_status 1
        end
    end
    rm -f -- "$__dev_cd_file"
    return $__dev_status
end
`

func newShellInitCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell-init <bash|zsh|fish>",
		Short: "Print the shell wrapper that lets dev change your directory",
		Long: `Print a shell function that wraps the dev binary.

dev commands that move you into a checkout (resume, try, wt open with the
"none" runtime) pass the destination through a child-only file descriptor,
because a child process cannot change its parent's working directory. The
wrapper reads that path and changes directory without evaluating shell code.

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
	return cmd
}
