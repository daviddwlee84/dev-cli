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
    __complete|__completeNoDesc|--help|-h|--version|--skill|completion|config|doctor|retire|prepare|artifact|git|edit|gitignore|ignore|help|list|ls|park|skill|shell-init|cache|stats|status|sweep|adopt|bootstrap)
      command %[1]s "$@" || return $?
      return 0
      ;;
  esac

  local __dev_cd_file __dev_action_file __dev_dir __dev_status
  local __dev_action __dev_task __dev_delete __dev_unknown
  if ! __dev_cd_file="$(mktemp "${TMPDIR:-/tmp}/dev-cd.XXXXXX" 2>/dev/null)"; then
    __dev_cd_file="$(mktemp "/tmp/dev-cd.XXXXXX" 2>/dev/null)" || {
      printf 'dev: cannot create the shell directory side channel\n' >&2
      return 1
    }
  fi

  if ! __dev_action_file="$(mktemp "${TMPDIR:-/tmp}/dev-action.XXXXXX" 2>/dev/null)"; then
    __dev_action_file="$(mktemp "/tmp/dev-action.XXXXXX" 2>/dev/null)" || {
      rm -f -- "$__dev_cd_file" || :
      printf 'dev: cannot create the shell action side channel\n' >&2
      return 1
    }
  fi

  __dev_status=0
  DEV_SHELL_CD_FD=3 DEV_SHELL_ACTION_FD=4 command %[1]s "$@" 3>"$__dev_cd_file" 4>"$__dev_action_file" || __dev_status=$?
  if [ $__dev_status -eq 0 ] && [ -s "$__dev_cd_file" ]; then
    if IFS= read -r -d '' __dev_dir < "$__dev_cd_file"; then
      if [ -n "$__dev_dir" ]; then
        builtin cd -- "$__dev_dir" || __dev_status=$?
      fi
    else
      __dev_status=1
    fi
  fi
  if [ $__dev_status -eq 0 ] && [ -s "$__dev_action_file" ]; then
    exec 4<"$__dev_action_file"
    if IFS= read -r -d '' __dev_action <&4 &&
       IFS= read -r -d '' __dev_task <&4 &&
       IFS= read -r -d '' __dev_delete <&4 &&
       IFS= read -r -d '' __dev_unknown <&4 &&
       [ "$__dev_action" = retire ]; then
      case "$__dev_delete:$__dev_unknown" in
        false:false) command %[1]s retire -- "$__dev_task" || __dev_status=$? ;;
        true:false)  command %[1]s retire --delete-branch -- "$__dev_task" || __dev_status=$? ;;
        false:true)  command %[1]s retire --close-unknown -- "$__dev_task" || __dev_status=$? ;;
        true:true)   command %[1]s retire --delete-branch --close-unknown -- "$__dev_task" || __dev_status=$? ;;
        *) __dev_status=1 ;;
      esac
    else
      __dev_status=1
    fi
    exec 4<&-
  fi
  rm -f -- "$__dev_cd_file" "$__dev_action_file" || :
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
    if contains -- "$__dev_command" __complete __completeNoDesc --help -h --version --skill completion config doctor retire prepare artifact git edit gitignore ignore help list ls park skill shell-init cache stats status sweep adopt bootstrap
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

    set -l __dev_action_file (mktemp "$__dev_tmpdir/dev-action.XXXXXX" 2>/dev/null)
    if test $status -ne 0
        set __dev_action_file (mktemp "/tmp/dev-action.XXXXXX" 2>/dev/null)
    end
    if test -z "$__dev_action_file"
        rm -f -- "$__dev_cd_file"
        printf 'dev: cannot create the shell action side channel\n' >&2
        return 1
    end

    set -lx DEV_SHELL_CD_FD 3
    set -lx DEV_SHELL_ACTION_FD 4
    command %[1]s $argv 3>"$__dev_cd_file" 4>"$__dev_action_file"
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
    if test $__dev_status -eq 0; and test -s "$__dev_action_file"
        begin
            read -z -l __dev_action
            read -z -l __dev_task
            read -z -l __dev_delete
            read -z -l __dev_unknown
            if test "$__dev_action" = retire
                switch "$__dev_delete:$__dev_unknown"
                    case false:false
                        command %[1]s retire -- "$__dev_task"
                    case true:false
                        command %[1]s retire --delete-branch -- "$__dev_task"
                    case false:true
                        command %[1]s retire --close-unknown -- "$__dev_task"
                    case true:true
                        command %[1]s retire --delete-branch --close-unknown -- "$__dev_task"
                    case '*'
                        false
                end
                set __dev_status $status
            else
                set __dev_status 1
            end
        end < "$__dev_action_file"
    end
    rm -f -- "$__dev_cd_file" "$__dev_action_file"
    return $__dev_status
end
`

// powershellInit is the Windows counterpart to posixInit. PowerShell does not
// inherit an extra file descriptor from its parent, so the wrapper names a temp
// file in DEV_SHELL_CD_FILE and reads the NUL-terminated path back after dev
// exits, rather than reading fd 3.
const powershellInit = `# dev shell integration — add to your PowerShell profile:
#   Invoke-Expression (& dev shell-init powershell | Out-String)
$env:DEV_SHELL_INIT = "1"
function dev {
    $__dev_exe = %[1]s
    $__dev_direct = @('__complete','__completeNoDesc','--help','-h','--version','--skill','completion','config','doctor','retire','prepare','artifact','git','edit','gitignore','ignore','help','list','ls','park','skill','shell-init','cache','stats','status','sweep','adopt','bootstrap')
    if ($args.Count -gt 0 -and $__dev_direct -contains $args[0]) {
        & $__dev_exe @args
        return
    }
    $__dev_cd_file = [System.IO.Path]::GetTempFileName()
    $__dev_action_file = [System.IO.Path]::GetTempFileName()
    try {
        $env:DEV_SHELL_CD_FILE = $__dev_cd_file
        $env:DEV_SHELL_ACTION_FILE = $__dev_action_file
        & $__dev_exe @args
        $__dev_status = $LASTEXITCODE
        if ($__dev_status -eq 0 -and (Get-Item -LiteralPath $__dev_cd_file).Length -gt 0) {
            $__dev_dir = [System.IO.File]::ReadAllText($__dev_cd_file).TrimEnd([char]0)
            if ($__dev_dir) { Set-Location -LiteralPath $__dev_dir }
        }
        if ($__dev_status -eq 0 -and (Get-Item -LiteralPath $__dev_action_file).Length -gt 0) {
            $__dev_fields = ([System.Text.Encoding]::UTF8.GetString([System.IO.File]::ReadAllBytes($__dev_action_file))).Split([char]0)
            if ($__dev_fields.Count -lt 5 -or $__dev_fields[0] -ne 'retire') {
                $__dev_status = 1
            } else {
                $__dev_retire_args = @('retire')
                if ($__dev_fields[2] -eq 'true') { $__dev_retire_args += '--delete-branch' }
                if ($__dev_fields[3] -eq 'true') { $__dev_retire_args += '--close-unknown' }
                $__dev_retire_args += @('--', $__dev_fields[1])
                & $__dev_exe @__dev_retire_args
                $__dev_status = $LASTEXITCODE
            }
        }
        $global:LASTEXITCODE = $__dev_status
    } finally {
        Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath $__dev_cd_file
        Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath $__dev_action_file
        Remove-Item -ErrorAction SilentlyContinue Env:\DEV_SHELL_CD_FILE
        Remove-Item -ErrorAction SilentlyContinue Env:\DEV_SHELL_ACTION_FILE
    }
}
`

func newShellInitCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell-init <bash|zsh|fish|powershell>",
		Short: "Print the shell wrapper that lets dev change your directory",
		Long: `Print a shell function that wraps the dev binary.

dev commands that move you into or safely retire a checkout pass navigation
and the narrow post-exit retire action through private side channels, because
a child process cannot change its parent's working directory. The wrapper
never evaluates normal command output or arbitrary shell code. POSIX shells
receive the channels on child-only file descriptors; PowerShell reads private
temp files, since Windows shells do not inherit the descriptors.

Add to your shell rc file:

    eval "$(dev shell-init zsh)"                              # bash and zsh
    dev shell-init fish | source                              # fish
    Invoke-Expression (& dev shell-init powershell | Out-String)  # PowerShell`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell", "pwsh"},
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
			case "powershell", "pwsh":
				fmt.Fprintf(app.Out, powershellInit, powershellQuote(self))
			default:
				return fmt.Errorf("unsupported shell %q: want bash, zsh, fish or powershell", args[0])
			}
			return nil
		},
	}
	return cmd
}
