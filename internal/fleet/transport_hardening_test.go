package fleet

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requirePOSIXTransportFixture(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture; Windows askpass has native tests")
	}
}

func TestSSHArgsHardenNoninteractiveFixedCommands(t *testing.T) {
	host := Host{Name: "lab", SSHAlias: "lab"}
	host.ConnectTimeout.Duration = time.Second
	args := sshArgs(host, false, false)
	joined := strings.Join(args, " ")
	for _, required := range []string{"-T", "ClearAllForwardings=yes", "ForwardAgent=no", "ForwardX11=no", "PermitLocalCommand=no"} {
		if !containsArgument(args, required) {
			t.Errorf("noninteractive args lack %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
		if containsArgument(args, forbidden) {
			t.Errorf("noninteractive args weaken host-key verification with %q: %s", forbidden, joined)
		}
	}
	interactive := sshArgs(host, true, false)
	if !containsArgument(interactive, "-t") || containsArgument(interactive, "-T") || containsArgument(interactive, "ClearAllForwardings=yes") {
		t.Fatalf("interactive args unexpectedly hardened as protocol command: %s", strings.Join(interactive, " "))
	}
}

func TestTransportBoundsCapturedStdoutAndStderr(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	script := "#!/bin/sh\nprintf 'abcdefghijklmnop'\nprintf 'ABCDEFGHIJKLMNOP' >&2\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	host := transportTestHost()
	result := (Transport{StdoutLimit: 8, StderrLimit: 9}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, nil, RunOptions{Retry: RetryNever})
	if result.ExitCode != 125 || result.CaptureError == "" {
		t.Fatalf("bounded result = %+v", result)
	}
	if len(result.Stdout) != 8 || string(result.Stdout) != "abcdefgh" {
		t.Fatalf("stdout = %q (%d bytes)", result.Stdout, len(result.Stdout))
	}
	if !strings.Contains(result.CaptureError, "stdout exceeded 8-byte") || !strings.Contains(result.CaptureError, "stderr exceeded 9-byte") {
		t.Fatalf("capture error = %q", result.CaptureError)
	}
	if len(result.Stderr) != 9 || string(result.Stderr) != "ABCDEFGHI" {
		t.Fatalf("stderr = %q (%d bytes), want bounded remote prefix", result.Stderr, len(result.Stderr))
	}
}

func TestTransportRetryPolicyIsExplicit(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	script := `#!/bin/sh
if [ -f "$ATTEMPT_FILE" ]; then
  printf 'success'
  exit 0
fi
: > "$ATTEMPT_FILE"
printf 'Permission denied (publickey).' >&2
exit 255
`
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	host := transportTestHost()
	host.SSHLoginPasswordSource = PasswordSource{Type: "plain", Value: "secret"}

	neverMarker := filepath.Join(t.TempDir(), "never")
	t.Setenv("ATTEMPT_FILE", neverMarker)
	never := (Transport{}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, nil, RunOptions{Retry: RetryNever})
	if never.ExitCode != 255 || never.Attempts != 1 || never.UsedPassword {
		t.Fatalf("RetryNever result = %+v", never)
	}

	authMarker := filepath.Join(t.TempDir(), "auth")
	t.Setenv("ATTEMPT_FILE", authMarker)
	auth := (Transport{}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, nil, RunOptions{Retry: RetryAuthentication})
	if auth.ExitCode != 0 || auth.Attempts != 2 || !auth.UsedPassword || string(auth.Stdout) != "success" || auth.TransportError != "" {
		t.Fatalf("RetryAuthentication result = %+v", auth)
	}

	legacyMarker := filepath.Join(t.TempDir(), "legacy")
	t.Setenv("ATTEMPT_FILE", legacyMarker)
	legacy := (Transport{}).Run(context.Background(), host, []string{"fleet", "_sync"}, nil, false)
	if legacy.ExitCode != 0 || legacy.Attempts != 2 || !legacy.UsedPassword {
		t.Fatalf("legacy Run changed sync retry behavior: %+v", legacy)
	}
}

func TestTransportRejectsOversizedStdinBeforeStartingSSH(t *testing.T) {
	host := transportTestHost()
	result := (Transport{StdinLimit: 3}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, []byte("four"), RunOptions{Retry: RetryNever})
	if result.ExitCode != 125 || result.Attempts != 0 || !strings.Contains(result.CaptureError, "stdin exceeds 3-byte") {
		t.Fatalf("oversized stdin result = %+v", result)
	}
}

func TestTransportReportsContextTimeoutAs124(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	host := transportTestHost()
	host.CommandTimeout.Duration = 25 * time.Millisecond
	result := (Transport{}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, nil, RunOptions{Retry: RetryNever})
	if result.ExitCode != 124 || !result.TimedOut {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestTransportPreservesPasswordResolutionFailureWhenStderrIsFull(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	denial := "Permission denied (publickey)." + strings.Repeat("x", 128)
	script := "#!/bin/sh\nprintf '" + denial + "' >&2\nexit 255\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	host := transportTestHost()
	host.SSHLoginPasswordSource = PasswordSource{Type: "bitwarden", Item: "missing"}
	limit := int64(len(denial))
	result := (Transport{StderrLimit: limit}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, nil, RunOptions{Retry: RetryAuthentication})
	if result.ExitCode != 255 || !strings.Contains(result.TransportError, "Bitwarden") {
		t.Fatalf("password resolution result = %+v", result)
	}
	if len(result.Stderr) > int(limit) || !strings.Contains(string(result.Stderr), "Bitwarden") {
		t.Fatalf("bounded password diagnostic = %q (%d bytes), limit %d", result.Stderr, len(result.Stderr), limit)
	}
}

func TestTransportStartsSSHBeforeWritingLargeAskpassSecret(t *testing.T) {
	requirePOSIXTransportFixture(t)
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	script := `#!/bin/sh
if [ -z "$DEV_FLEET_SSH_ASKPASS" ]; then
  printf 'Permission denied (publickey).' >&2
  exit 255
fi
: > "$STARTED_FILE"
exec /bin/sleep 0.1
`
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("STARTED_FILE", started)
	host := transportTestHost()
	host.CommandTimeout.Duration = 10 * time.Second
	host.SSHLoginPasswordSource = PasswordSource{Type: "plain", Value: strings.Repeat("x", maxAskpassSecretSize-1)}

	done := make(chan Result, 1)
	go func() {
		done <- (Transport{}).RunWithOptions(context.Background(), host, []string{"fleet", "_protocol"}, nil, RunOptions{Retry: RetryAuthentication})
	}()
	select {
	case result := <-done:
		if result.Attempts != 2 || !result.UsedPassword {
			t.Fatalf("large askpass result = %+v", result)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("transport blocked writing askpass secret before SSH could start")
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("password-authenticated SSH attempt did not start: %v", err)
	}
}

func transportTestHost() Host {
	host := Host{Name: "lab", SSHAlias: "lab", DevPath: "auto"}
	host.ConnectTimeout.Duration = time.Second
	host.CommandTimeout.Duration = time.Minute
	return host
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
