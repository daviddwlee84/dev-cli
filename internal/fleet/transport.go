package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
)

const (
	askpassMarker = "DEV_FLEET_SSH_ASKPASS"
	askpassFD     = "DEV_FLEET_SSH_ASKPASS_FD"

	DefaultStdinLimit  int64 = 32 << 20
	DefaultStdoutLimit int64 = 8 << 20
	DefaultStderrLimit int64 = 1 << 20
)

var promptPasswordMu sync.Mutex

// MaybeServeAskpass turns the dev executable into a one-shot SSH_ASKPASS
// helper. The password arrives through an inherited descriptor, never argv.
func MaybeServeAskpass() (bool, int) {
	if os.Getenv(askpassMarker) != "1" {
		return false, 0
	}
	fd, err := strconv.Atoi(os.Getenv(askpassFD))
	if err != nil || fd < 3 {
		return true, 2
	}
	file := os.NewFile(uintptr(fd), "dev-fleet-askpass")
	if file == nil {
		return true, 2
	}
	defer file.Close()
	password, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return true, 2
	}
	password = bytes.TrimSuffix(password, []byte{'\n'})
	_, err = os.Stdout.Write(append(password, '\n'))
	if err != nil {
		return true, 2
	}
	return true, 0
}

type RetryPolicy string

const (
	RetryNever          RetryPolicy = "never"
	RetryAuthentication RetryPolicy = "authentication"
)

type RunOptions struct {
	Retry RetryPolicy
}

type Result struct {
	Stdout         []byte
	Stderr         []byte
	ExitCode       int
	TimedOut       bool
	UsedPassword   bool
	Attempts       int
	CaptureError   string
	TransportError string
}

type Transport struct {
	Err         io.Writer
	StdinLimit  int64
	StdoutLimit int64
	StderrLimit int64
}

// Interactive replaces the caller's terminal with an SSH PTY for a fixed dev
// helper command. Password sources use the same descriptor-backed askpass path
// as non-interactive probes.
func (t Transport) Interactive(ctx context.Context, host Host, remoteArgs []string, usePassword bool) error {
	password := ""
	if usePassword && host.PasswordKind() != "none" {
		resolved, err := t.resolvePassword(host)
		if err != nil {
			return err
		}
		password = resolved
	}
	args := sshArgs(host, true, password != "")
	args = append(args, host.Destination(), remoteCommand(host, remoteArgs))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return runSSHCommand(cmd, password)

}

// Run preserves the v0.2 fleet behavior: retry once with the configured
// password only when SSH exits 255 with an authentication-denial diagnostic.
// New mutating protocols should call RunWithOptions and choose a retry policy
// explicitly.
func (t Transport) Run(ctx context.Context, host Host, remoteArgs []string, stdin []byte, pty bool) Result {
	return t.run(ctx, host, remoteArgs, stdin, pty, RunOptions{Retry: RetryAuthentication})
}

// RunWithOptions executes a fixed, noninteractive protocol command with an
// explicit retry policy. It always requests no PTY and disables forwarding.
func (t Transport) RunWithOptions(ctx context.Context, host Host, remoteArgs []string, stdin []byte, options RunOptions) Result {
	return t.run(ctx, host, remoteArgs, stdin, false, options)
}

func (t Transport) run(ctx context.Context, host Host, remoteArgs []string, stdin []byte, pty bool, options RunOptions) Result {
	if options.Retry == "" {
		options.Retry = RetryNever
	}
	if options.Retry != RetryNever && options.Retry != RetryAuthentication {
		return localTransportFailure(fmt.Sprintf("fleet transport: unknown retry policy %q", options.Retry))
	}
	stdinLimit := effectiveLimit(t.StdinLimit, DefaultStdinLimit)
	if int64(len(stdin)) > stdinLimit {
		message := fmt.Sprintf("fleet transport: stdin exceeds %d-byte limit", stdinLimit)
		return Result{ExitCode: 125, Stderr: []byte(message), CaptureError: message, TransportError: message}
	}
	timeout := host.CommandTimeout.Duration
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	first := t.runAttempt(commandCtx, host, remoteArgs, stdin, pty, "")
	if options.Retry != RetryAuthentication || first.CaptureError != "" || first.ExitCode != 255 || host.PasswordKind() == "none" || !permissionDenied(first.Stderr) {
		first.TimedOut = first.TimedOut || errors.Is(commandCtx.Err(), context.DeadlineExceeded)
		return first
	}
	password, err := t.resolvePassword(host)
	if err != nil {
		message := "fleet transport: " + err.Error()
		first.TransportError = message
		first.Stderr = boundedBytes([]byte(message), effectiveLimit(t.StderrLimit, DefaultStderrLimit))
		return first
	}
	second := t.runAttempt(commandCtx, host, remoteArgs, stdin, pty, password)
	second.UsedPassword = true
	second.Attempts = first.Attempts + second.Attempts
	second.TimedOut = second.TimedOut || errors.Is(commandCtx.Err(), context.DeadlineExceeded)
	return second
}

func (t Transport) runAttempt(ctx context.Context, host Host, remoteArgs []string, stdin []byte, pty bool, password string) Result {
	args := sshArgs(host, pty, password != "")
	args = append(args, host.Destination(), remoteCommand(host, remoteArgs))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := newCaptureBuffer(effectiveLimit(t.StdoutLimit, DefaultStdoutLimit))
	stderr := newCaptureBuffer(effectiveLimit(t.StderrLimit, DefaultStderrLimit))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	err := runSSHCommand(cmd, password)
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0, Attempts: 1}
	if stdout.Exceeded() || stderr.Exceeded() {
		message := captureLimitMessage(stdout, stderr)
		result.ExitCode = 125
		result.CaptureError = message
		result.TransportError = message
		result.Stderr = appendBounded(result.Stderr, []byte("\n"+message), effectiveLimit(t.StderrLimit, DefaultStderrLimit))
		return result
	}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.ExitCode, result.TimedOut = 124, true
	} else if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		if result.ExitCode < 0 {
			result.ExitCode = 255
		}
	} else {
		result.ExitCode = 255
		if len(result.Stderr) == 0 {
			result.Stderr = []byte(err.Error())
		}
	}
	return result
}

// runSSHCommand starts SSH before writing an askpass secret. Starting first is
// load-bearing: a secret larger than the pipe capacity must remain cancellable
// through the command context instead of blocking before SSH exists.
func runSSHCommand(cmd *exec.Cmd, password string) error {
	if password == "" {
		return cmd.Run()
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = []*os.File{reader}
	cmd.Env = append(cmd.Env,
		askpassMarker+"=1", askpassFD+"=3", "SSH_ASKPASS="+executable,
		"SSH_ASKPASS_REQUIRE=force", "DISPLAY=dev-fleet")
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return err
	}
	// The child owns its inherited descriptor after Start; closing the parent
	// copy lets an early SSH exit interrupt a blocked write.
	readerCloseErr := reader.Close()
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := io.Copy(writer, strings.NewReader(password+"\n"))
		writeDone <- errors.Join(writeErr, writer.Close())
	}()
	waitErr := cmd.Wait()
	// A descendant could retain the inherited read descriptor after SSH exits.
	// Close our writer so that cannot strand the write goroutine.
	_ = writer.Close()
	writeErr := <-writeDone
	return errors.Join(waitErr, readerCloseErr, writeErr)
}

func sshArgs(host Host, pty, password bool) []string {
	connect := host.ConnectTimeout.Duration
	if connect <= 0 {
		connect = 15 * time.Second
	}
	seconds := int(connect.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	args := []string{"-o", "ConnectTimeout=" + strconv.Itoa(seconds), "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}
	if password {
		args = append(args, "-o", "BatchMode=no", "-o", "PubkeyAuthentication=no", "-o", "PreferredAuthentications=keyboard-interactive,password", "-o", "NumberOfPasswordPrompts=1")
	} else {
		args = append(args, "-o", "BatchMode=yes")
	}
	if pty {
		args = append(args, "-t")
	} else {
		// A fixed protocol command never needs a TTY or client-side forwarding.
		// Do not add host-key overrides here: OpenSSH's configured strict host-key
		// verification remains authoritative.
		args = append(args,
			"-T",
			"-o", "ClearAllForwardings=yes",
			"-o", "ForwardAgent=no",
			"-o", "ForwardX11=no",
			"-o", "PermitLocalCommand=no",
		)
	}
	if host.User != "" && host.SSHAlias != "" {
		args = append(args, "-l", host.User)
	}
	if host.Port > 0 {
		args = append(args, "-p", strconv.Itoa(host.Port))
	}
	if host.IdentityFile != "" && !password {
		args = append(args, "-i", devconfig.Expand(host.IdentityFile))
	}
	return args
}

func remoteCommand(host Host, args []string) string {
	devPath := host.DevPath
	if devPath == "" {
		devPath = "auto"
	}
	quotedArgs := make([]string, len(args))
	for index, arg := range args {
		quotedArgs[index] = shellQuote(arg)
	}
	joined := strings.Join(quotedArgs, " ")
	if devPath != "auto" {
		return "exec " + shellQuote(devconfig.Expand(devPath)) + " " + joined
	}
	path := `PATH="$HOME/.local/bin:$HOME/go/bin:$HOME/.local/share/mise/shims:/opt/homebrew/bin:/home/linuxbrew/.linuxbrew/bin:/usr/local/bin:/snap/bin:$PATH"`
	return path + "; export PATH; command -v dev >/dev/null 2>&1 || exit 127; exec dev " + joined
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func permissionDenied(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "permission denied")
}

func (t Transport) resolvePassword(host Host) (string, error) {
	source := host.SSHLoginPasswordSource
	switch host.PasswordKind() {
	case "plain":
		return source.Value, nil
	case "bitwarden":
		cmd := exec.Command("bw", "get", "password", source.Item)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("resolve Bitwarden SSH password for %s: %w", host.Name, err)
		}
		return strings.TrimSpace(string(out)), nil
	case "prompt":
		promptPasswordMu.Lock()
		defer promptPasswordMu.Unlock()
		writer := t.Err
		if writer == nil {
			writer = os.Stderr
		}
		fmt.Fprintf(writer, "SSH password for %s: ", host.Name)
		terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return "", fmt.Errorf("open terminal for SSH password: %w", err)
		}
		defer terminal.Close()
		password, err := term.ReadPassword(terminal.Fd())
		fmt.Fprintln(writer)
		if err != nil {
			return "", err
		}
		return string(password), nil
	default:
		return "", nil
	}
}

type captureBuffer struct {
	limit    int64
	buffer   bytes.Buffer
	exceeded bool
}

func newCaptureBuffer(limit int64) *captureBuffer {
	return &captureBuffer{limit: limit}
}

func (b *captureBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - int64(b.buffer.Len())
	if remaining > 0 {
		keep := int64(len(value))
		if keep > remaining {
			keep = remaining
		}
		_, _ = b.buffer.Write(value[:int(keep)])
	}
	if int64(len(value)) > remaining {
		b.exceeded = true
	}
	// Consume excess output without retaining it. The command timeout still
	// bounds a peer that writes forever, while the parent process stays bounded.
	return len(value), nil
}

func (b *captureBuffer) Bytes() []byte {
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *captureBuffer) Exceeded() bool { return b.exceeded }

func effectiveLimit(configured, fallback int64) int64 {
	if configured <= 0 {
		return fallback
	}
	return configured
}

func captureLimitMessage(stdout, stderr *captureBuffer) string {
	streams := make([]string, 0, 2)
	if stdout.Exceeded() {
		streams = append(streams, fmt.Sprintf("stdout exceeded %d-byte capture limit", stdout.limit))
	}
	if stderr.Exceeded() {
		streams = append(streams, fmt.Sprintf("stderr exceeded %d-byte capture limit", stderr.limit))
	}
	return "fleet transport: remote " + strings.Join(streams, " and ")
}

func boundedBytes(value []byte, limit int64) []byte {
	if int64(len(value)) > limit {
		value = value[:int(limit)]
	}
	return append([]byte(nil), value...)
}

func appendBounded(current, suffix []byte, limit int64) []byte {
	if int64(len(current)) >= limit {
		return append([]byte(nil), current...)
	}
	available := limit - int64(len(current))
	if int64(len(suffix)) > available {
		suffix = suffix[:int(available)]
	}
	return append(append([]byte(nil), current...), suffix...)
}

func localTransportFailure(message string) Result {
	return Result{ExitCode: 125, Stderr: []byte(message), CaptureError: message, TransportError: message}
}
