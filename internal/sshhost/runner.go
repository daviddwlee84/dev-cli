package sshhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

const (
	maxCapturedOutputBytes = 1 << 20
	outputTruncationMarker = "\n[dev-cli-output-truncated]\n"
)

// RunRequest is an argv-only subprocess request. Display is a redacted label
// for diagnostics; runners must not reconstruct diagnostics from sensitive
// argv or stdin. Interactive attaches native stdin (when Stdin is empty) and
// output to the controller terminal so ssh/ssh-keygen can own hidden prompts.
// CaptureStdout is reserved for bounded machine-readable output from an
// otherwise interactive command, such as ssh-keygen -y.
type RunRequest struct {
	Name          string   `json:"-"`
	Args          []string `json:"-"`
	Dir           string   `json:"-"`
	Env           []string `json:"-"`
	Stdin         []byte   `json:"-"`
	Display       string   `json:"display,omitempty"`
	Interactive   bool     `json:"interactive,omitempty"`
	CaptureStdout bool     `json:"-"`
}

// RunResult separates process exit from launcher failure. Captured output is
// bounded; the byte slices end with outputTruncationMarker when the matching
// truncation flag is true.
type RunResult struct {
	Stdout          []byte `json:"-"`
	Stderr          []byte `json:"-"`
	StdoutTruncated bool   `json:"-"`
	StderrTruncated bool   `json:"-"`
	ExitCode        int    `json:"exit_code"`
}

// Runner is injected so effective-config and later bootstrap operations are
// testable without touching the developer's SSH agent or network.
type Runner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

// ExecRunner executes requests directly, without a shell.
type ExecRunner struct{}

// Run implements Runner. Noninteractive cancellation terminates the complete
// process tree through a Unix process group or Windows Job Object. Interactive
// children remain in the controller's foreground process group so terminal job
// control and Ctrl-C retain native behavior; context cancellation kills only
// the direct interactive child.
func (ExecRunner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if request.Name == "" {
		return RunResult{}, errors.New("runner command is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	cmd := exec.Command(request.Name, request.Args...)
	cmd.Dir = request.Dir
	if request.Interactive && len(request.Stdin) == 0 {
		cmd.Stdin = os.Stdin
	} else {
		cmd.Stdin = bytes.NewReader(request.Stdin)
	}
	cmd.Env = append(os.Environ(), request.Env...)

	var stdout, stderr boundedCapture
	captureStdout := !request.Interactive || request.CaptureStdout
	if captureStdout {
		cmd.Stdout = &stdout
	} else {
		cmd.Stdout = os.Stdout
	}
	if request.Interactive {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = &stderr
	}

	platformPrepareCommand(cmd, request.Interactive)
	if err := cmd.Start(); err != nil {
		return RunResult{}, err
	}
	tree, err := platformAttachProcess(cmd, request.Interactive)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return capturedRunResult(&stdout, &stderr, captureStdout, !request.Interactive), err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-wait:
		closeErr := tree.close()
		if waitErr == nil && closeErr != nil {
			return capturedRunResult(&stdout, &stderr, captureStdout, !request.Interactive), closeErr
		}
	case <-ctx.Done():
		terminateErr := tree.terminate()
		var fallbackErr error
		if terminateErr != nil {
			fallbackErr = cmd.Process.Kill()
			if errors.Is(fallbackErr, os.ErrProcessDone) {
				fallbackErr = nil
			}
		}
		waitErr = <-wait
		closeErr := tree.close()
		return capturedRunResult(&stdout, &stderr, captureStdout, !request.Interactive), errors.Join(ctx.Err(), terminateErr, fallbackErr, closeErr)
	}

	result := capturedRunResult(&stdout, &stderr, captureStdout, !request.Interactive)
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, waitErr
}

type boundedCapture struct {
	data      []byte
	truncated bool
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	length := len(data)
	remaining := maxCapturedOutputBytes - len(capture.data)
	if remaining > 0 {
		if remaining > length {
			remaining = length
		}
		capture.data = append(capture.data, data[:remaining]...)
	}
	if remaining < length {
		capture.truncated = true
	}
	return length, nil
}

func (capture *boundedCapture) takeBytes() []byte {
	if capture == nil || len(capture.data) == 0 && !capture.truncated {
		return nil
	}
	result := capture.data
	capture.data = nil
	if capture.truncated {
		result = append(result, outputTruncationMarker...)
	}
	return result
}

func capturedRunResult(stdout, stderr *boundedCapture, includeStdout, includeStderr bool) RunResult {
	var result RunResult
	if includeStdout {
		result.Stdout = stdout.takeBytes()
		result.StdoutTruncated = stdout.truncated
	}
	if includeStderr {
		result.Stderr = stderr.takeBytes()
		result.StderrTruncated = stderr.truncated
	}
	return result
}

var _ io.Writer = (*boundedCapture)(nil)
