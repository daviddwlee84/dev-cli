package sshhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// RunRequest is an argv-only subprocess request. Display is a redacted label
// for diagnostics; runners must not reconstruct diagnostics from sensitive
// argv or stdin.
type RunRequest struct {
	Name    string
	Args    []string
	Dir     string
	Env     []string
	Stdin   []byte
	Display string
}

// RunResult separates process exit from launcher failure.
type RunResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner is injected so effective-config and later bootstrap operations are
// testable without touching the developer's SSH agent or network.
type Runner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

// ExecRunner executes requests directly, without a shell.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if request.Name == "" {
		return RunResult{}, errors.New("runner command is empty")
	}
	cmd := exec.CommandContext(ctx, request.Name, request.Args...)
	cmd.Dir = request.Dir
	cmd.Stdin = bytes.NewReader(request.Stdin)
	cmd.Env = append(os.Environ(), request.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	return result, err
}
