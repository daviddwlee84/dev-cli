//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
)

// replaceProcessWithShell starts an interactive shell in the current working
// directory. Windows has no exec(2), so dev runs the shell as a child that
// inherits the console and then exits with the shell's status when it returns.
func replaceProcessWithShell() error {
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
