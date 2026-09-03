//go:build unix

package fleet

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnixAskpassRetryUsesSeparateInheritedFDAndPreservesStdin(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	passwordPath := filepath.Join(root, "password")
	stdinPath := filepath.Join(root, "stdin")
	argumentsPath := filepath.Join(root, "arguments")
	environmentPath := filepath.Join(root, "environment")
	ssh := filepath.Join(bin, "ssh")
	script := `#!/bin/sh
case " $* " in
  *BatchMode=yes*)
    printf '%s\n' 'Permission denied (publickey).' >&2
    exit 255
    ;;
esac
: > "$CAPTURE_ARGUMENTS"
for argument do
    printf '%s\n' "$argument" >> "$CAPTURE_ARGUMENTS"
done
set > "$CAPTURE_ENVIRONMENT"
"$SSH_ASKPASS" 'SSH password:' > "$CAPTURE_PASSWORD"
input=''
IFS= read -r input || true
printf '%s' "$input" > "$CAPTURE_STDIN"
exit 0
`
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CAPTURE_PASSWORD", passwordPath)
	t.Setenv("CAPTURE_STDIN", stdinPath)
	t.Setenv("CAPTURE_ARGUMENTS", argumentsPath)
	t.Setenv("CAPTURE_ENVIRONMENT", environmentPath)

	const password = "fleet secret 9f65"
	stdin := []byte(`{"remote_identity":"github.com/example/api","branch":"main","expected_oid":"abc123"}`)
	host := Host{
		Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX, DevPath: "auto",
		SSHLoginPasswordSource: PasswordSource{Type: "plain", Value: password},
	}
	host.ConnectTimeout.Duration = time.Second
	host.CommandTimeout.Duration = time.Minute
	result := (Transport{}).Run(context.Background(), host, []string{"fleet", "_sync"}, stdin, false)
	if result.ExitCode != 0 || !result.UsedPassword {
		t.Fatalf("Transport.Run = exit %d password=%v stderr=%s", result.ExitCode, result.UsedPassword, result.Stderr)
	}
	capturedPassword, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(capturedPassword) != password+"\n" {
		t.Fatalf("askpass output = %q", capturedPassword)
	}
	capturedStdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(capturedStdin, stdin) {
		t.Fatalf("remote stdin = %q, want %q", capturedStdin, stdin)
	}
	for _, capture := range []string{argumentsPath, environmentPath} {
		content, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), password) {
			t.Fatalf("password leaked into %s", filepath.Base(capture))
		}
	}
}
