//go:build !windows

package fleet

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransportPassesSyncStdinAlongsideWindowsWrapper(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(root, "command")
	stdinPath := filepath.Join(root, "stdin")
	ssh := filepath.Join(bin, "ssh")
	script := `#!/bin/sh
last=""
for argument do
    last="$argument"
done
printf '%s' "$last" > "$CAPTURE_COMMAND"
IFS= read -r input
printf '%s' "$input" > "$CAPTURE_STDIN"
exit 0
`
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CAPTURE_COMMAND", commandPath)
	t.Setenv("CAPTURE_STDIN", stdinPath)

	stdin := []byte(`{"remote_identity":"github.com/example/api","branch":"main","expected_oid":"abc123"}`)
	host := Host{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSWindows, DevPath: "auto"}
	host.ConnectTimeout.Duration = time.Second
	host.CommandTimeout.Duration = time.Minute
	result := (Transport{}).Run(context.Background(), host, []string{"fleet", "_sync"}, stdin, false)
	if result.ExitCode != 0 {
		t.Fatalf("Transport.Run = exit %d stderr %s", result.ExitCode, result.Stderr)
	}
	capturedStdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(capturedStdin, stdin) {
		t.Fatalf("captured stdin = %q, want %q", capturedStdin, stdin)
	}
	capturedCommand, err := os.ReadFile(commandPath)
	if err != nil {
		t.Fatal(err)
	}
	scriptBody := decodePowerShellRemoteCommand(t, string(capturedCommand))
	if got := decodePowerShellArgumentVector(t, scriptBody); len(got) != 2 || got[0] != "fleet" || got[1] != "_sync" {
		t.Fatalf("captured Windows helper argv = %#v", got)
	}
}
