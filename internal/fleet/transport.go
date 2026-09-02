package fleet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/charmbracelet/x/term"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
)

const (
	askpassMarker        = "DEV_FLEET_SSH_ASKPASS"
	askpassFD            = "DEV_FLEET_SSH_ASKPASS_FD"
	maxAskpassSecretSize = 64 * 1024
)

var promptPasswordMu sync.Mutex

// MaybeServeAskpass turns the dev executable into a one-shot SSH_ASKPASS
// helper. The password arrives through an inherited descriptor, never argv.
func MaybeServeAskpass() (bool, int) {
	if os.Getenv(askpassMarker) != "1" {
		return false, 0
	}
	file, err := openAskpassSecret(os.Getenv(askpassFD))
	if err != nil || file == nil {
		return true, 2
	}
	defer file.Close()
	password, err := io.ReadAll(io.LimitReader(file, maxAskpassSecretSize+1))
	if err != nil || len(password) > maxAskpassSecretSize {
		return true, 2
	}
	password = bytes.TrimSuffix(password, []byte{'\n'})
	if _, err := os.Stdout.Write(password); err != nil {
		return true, 2
	}
	if _, err := os.Stdout.Write([]byte{'\n'}); err != nil {
		return true, 2
	}
	return true, 0
}

func runCommandWithAskpass(cmd *exec.Cmd, password string) error {
	if password == "" {
		return cmd.Run()
	}
	if len(password)+1 > maxAskpassSecretSize {
		return errors.New("SSH password exceeds the askpass secret carrier limit")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	carrier, identifier, err := newAskpassSecretCarrier(cmd)
	if err != nil {
		return err
	}
	cmd.Env = append(cmd.Env,
		askpassMarker+"=1", askpassFD+"="+identifier, "SSH_ASKPASS="+executable,
		"SSH_ASKPASS_REQUIRE=force", "DISPLAY=dev-fleet")
	if err := cmd.Start(); err != nil {
		return errors.Join(err, carrier.close())
	}
	parentErr := carrier.parentAfterStart()
	// The secret may be as large as a platform pipe buffer. Write concurrently
	// so the SSH child can reach and launch its askpass reader instead of the
	// parent blocking before that reader exists.
	writeDone := make(chan error, 1)
	go func() { writeDone <- carrier.writeSecret([]byte(password + "\n")) }()
	waitErr := cmd.Wait()
	writeErr := <-writeDone
	return errors.Join(waitErr, parentErr, writeErr, carrier.close())
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
	remote, err := checkedRemoteCommand(host, remoteArgs)
	if err != nil {
		return err
	}
	args := sshArgs(host, true, password != "")
	args = append(args, host.Destination(), remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return runCommandWithAskpass(cmd, password)
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
	remote, err := checkedRemoteCommand(host, remoteArgs)
	if err != nil {
		return Result{ExitCode: 255, Stderr: []byte(err.Error())}
	}
	args := sshArgs(host, pty, password != "")
	args = append(args, host.Destination(), remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	err = runCommandWithAskpass(cmd, password)
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
	command, _ := checkedRemoteCommand(host, args)
	return command
}

func checkedRemoteCommand(host Host, args []string) (string, error) {
	switch host.EffectiveRemoteOS() {
	case RemoteOSPOSIX:
		return posixRemoteCommand(host, args), nil
	case RemoteOSWindows:
		return windowsRemoteCommand(host, args)
	default:
		return "", fmt.Errorf("host %q has unsupported remote_os %q", host.Name, host.RemoteOS)
	}
}

func posixRemoteCommand(host Host, args []string) string {
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
		// dev_path belongs to the remote target. Never expand it through the
		// controller's environment or filepath semantics.
		return "exec " + shellQuote(devPath) + " " + joined
	}
	path := `PATH="$HOME/.local/bin:$HOME/go/bin:$HOME/.local/share/mise/shims:/opt/homebrew/bin:/home/linuxbrew/.linuxbrew/bin:/usr/local/bin:/snap/bin:$PATH"`
	return path + "; export PATH; command -v dev >/dev/null 2>&1 || exit 127; exec dev " + joined
}

func windowsRemoteCommand(host Host, args []string) (string, error) {
	if err := validateWindowsFleetHelperArgs(args); err != nil {
		return "", err
	}
	argumentJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode Windows fleet helper arguments: %w", err)
	}
	encodedArguments := base64.StdEncoding.EncodeToString(argumentJSON)

	devPath := host.DevPath
	if devPath == "" {
		devPath = "auto"
	}
	var locator string
	if devPath == "auto" {
		locator = `$devExecutable = $null
$candidates = @()
if (-not [String]::IsNullOrEmpty($env:USERPROFILE)) {
    $candidates += (Join-Path -Path $env:USERPROFILE -ChildPath '.local\bin\dev.exe')
    $candidates += (Join-Path -Path $env:USERPROFILE -ChildPath 'go\bin\dev.exe')
    $candidates += (Join-Path -Path $env:USERPROFILE -ChildPath '.local\share\mise\shims\dev.exe')
    $candidates += (Join-Path -Path $env:USERPROFILE -ChildPath 'scoop\shims\dev.exe')
}
if (-not [String]::IsNullOrEmpty($env:LOCALAPPDATA)) {
    $candidates += (Join-Path -Path $env:LOCALAPPDATA -ChildPath 'Microsoft\WinGet\Links\dev.exe')
}
$resolvedCommand = Get-Command -Name 'dev.exe' -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -ne $resolvedCommand) {
    $candidates += $resolvedCommand.Source
}
foreach ($candidate in $candidates) {
    if (-not [String]::IsNullOrEmpty($candidate) -and [IO.File]::Exists($candidate)) {
        $devExecutable = $candidate
        break
    }
}`
	} else {
		encodedPath := base64.StdEncoding.EncodeToString([]byte(devPath))
		locator = fmt.Sprintf(`$devExecutable = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))`, encodedPath)
	}

	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
%s
if ([String]::IsNullOrEmpty($devExecutable) -or -not [IO.File]::Exists($devExecutable)) {
    exit 127
}
$argumentJSON = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))
$devArguments = @((ConvertFrom-Json -InputObject $argumentJSON))
& $devExecutable @devArguments
$devExitCode = $LASTEXITCODE
if ($null -eq $devExitCode) {
    exit 1
}
exit [int]$devExitCode
`, locator, encodedArguments)
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand " + encodePowerShellCommand(script), nil
}

func validateWindowsFleetHelperArgs(args []string) error {
	if len(args) < 2 || args[0] != "fleet" {
		return errors.New("Windows fleet transport permits only internal fleet helpers")
	}
	switch args[1] {
	case "_snapshot", "_sync":
		if len(args) != 2 {
			return fmt.Errorf("Windows fleet helper %s accepts no command arguments", args[1])
		}
		return nil
	case "_open-herdr", "_shell":
		if len(args) != 4 || args[2] != "--request" {
			return fmt.Errorf("Windows fleet helper %s requires exactly --request <encoded-request>", args[1])
		}
		if err := validateEncodedOpenRequest(args[3]); err != nil {
			return fmt.Errorf("Windows fleet helper %s request: %w", args[1], err)
		}
		return nil
	default:
		return fmt.Errorf("Windows fleet helper %q is not allowlisted", args[1])
	}
}

func validateEncodedOpenRequest(value string) error {
	if value == "" || len(value) > 64<<10 {
		return errors.New("encoded request is empty or too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return errors.New("encoded request is not unpadded base64url")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var request OpenRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode request JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func encodePowerShellCommand(script string) string {
	codeUnits := utf16.Encode([]rune(script))
	encoded := make([]byte, len(codeUnits)*2)
	for index, codeUnit := range codeUnits {
		binary.LittleEndian.PutUint16(encoded[index*2:], codeUnit)
	}
	return base64.StdEncoding.EncodeToString(encoded)
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
