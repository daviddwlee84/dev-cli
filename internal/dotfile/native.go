package dotfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

type NativeError struct {
	Action string
	Err    error
}

func (e *NativeError) Error() string { return fmt.Sprintf("chezmoi %s: %v", e.Action, e.Err) }
func (e *NativeError) Unwrap() error { return e.Err }
func (e *NativeError) ExitCode() int {
	var exit *exec.ExitError
	if errors.As(e.Err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	return 1
}

// RunNative preserves native streams, cancellation and failures. It does not
// inject --force, install chezmoi, or interpret native output.
func RunNative(ctx context.Context, configPath, action string, args []string, in io.Reader, out, errOut io.Writer) error {
	path, err := exec.LookPath("chezmoi")
	if err != nil {
		return fmt.Errorf("chezmoi is not installed; see https://www.chezmoi.io/install/: %w", err)
	}
	var argv []string
	if configPath != "" {
		argv = append(argv, "--config", configPath)
	}
	argv = append(argv, action)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, path, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	if err := cmd.Run(); err != nil {
		return &NativeError{Action: action, Err: err}
	}
	return nil
}
