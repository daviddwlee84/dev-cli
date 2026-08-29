//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"syscall"
)

// replaceProcessWithShell replaces the current process image with an interactive
// login shell in the current working directory. On POSIX systems this is a true
// exec(2), so the shell inherits the controlling terminal directly and dev's own
// process is gone.
func replaceProcessWithShell() error {
	shell := shellPath()
	return syscall.Exec(shell, []string{"-" + filepath.Base(shell)}, os.Environ())
}
