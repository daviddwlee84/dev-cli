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

type Result struct {
	Stdout       []byte
	Stderr       []byte
	ExitCode     int
	TimedOut     bool
	UsedPassword bool
}

type Transport struct {
	Err io.Writer
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
	if password != "" {
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		_, _ = writer.WriteString(password + "\n")
		_ = writer.Close()
		defer reader.Close()
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		cmd.ExtraFiles = []*os.File{reader}
		cmd.Env = append(cmd.Env,
			askpassMarker+"=1", askpassFD+"=3", "SSH_ASKPASS="+executable,
			"SSH_ASKPASS_REQUIRE=force", "DISPLAY=dev-fleet")
	}
	return cmd.Run()
}

func (t Transport) Run(ctx context.Context, host Host, remoteArgs []string, stdin []byte, pty bool) Result {
	timeout := host.CommandTimeout.Duration
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	first := t.runAttempt(commandCtx, host, remoteArgs, stdin, pty, "")
	if first.ExitCode != 255 || host.PasswordKind() == "none" || !permissionDenied(first.Stderr) {
		first.TimedOut = errors.Is(commandCtx.Err(), context.DeadlineExceeded)
		return first
	}
	password, err := t.resolvePassword(host)
	if err != nil {
		first.Stderr = append(first.Stderr, []byte("\n"+err.Error())...)
		return first
	}
	second := t.runAttempt(commandCtx, host, remoteArgs, stdin, pty, password)
	second.UsedPassword = true
	second.TimedOut = errors.Is(commandCtx.Err(), context.DeadlineExceeded)
	return second
}

func (t Transport) runAttempt(ctx context.Context, host Host, remoteArgs []string, stdin []byte, pty bool, password string) Result {
	args := sshArgs(host, pty, password != "")
	args = append(args, host.Destination(), remoteCommand(host, remoteArgs))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if password != "" {
		reader, pipeWriter, err := os.Pipe()
		if err != nil {
			return Result{ExitCode: 255, Stderr: []byte(err.Error())}
		}
		_, _ = pipeWriter.WriteString(password + "\n")
		_ = pipeWriter.Close()
		executable, err := os.Executable()
		if err != nil {
			_ = reader.Close()
			return Result{ExitCode: 255, Stderr: []byte(err.Error())}
		}
		cmd.ExtraFiles = []*os.File{reader}
		cmd.Env = append(cmd.Env,
			askpassMarker+"=1", askpassFD+"=3", "SSH_ASKPASS="+executable,
			"SSH_ASKPASS_REQUIRE=force", "DISPLAY=dev-fleet")
		defer reader.Close()
	}
	err := cmd.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.ExitCode, result.TimedOut = 124, true
	} else {
		result.ExitCode = 255
		if len(result.Stderr) == 0 {
			result.Stderr = []byte(err.Error())
		}
	}
	return result
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
