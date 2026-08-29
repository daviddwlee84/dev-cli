//go:build unix

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCDDirectiveUsesPrivateDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directive")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// cdDirective owns and closes the inherited descriptor. Give it a duplicate
	// so the original os.File retains independent ownership and its finalizer
	// can never close an unrelated descriptor that the OS reused.
	ownedFD, err := unix.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEV_SHELL_CD_FD", strconv.Itoa(ownedFD))

	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	dir := "/tmp/a\ndirectory"
	if err := app.cdDirective(dir); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != dir+"\x00" {
		t.Errorf("directive file = %q, want NUL-terminated path %q", body, dir)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("descriptor transport should not write streams: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestPosixShellWrapperPreservesStreamsStatusAndCD(t *testing.T) {
	fake, tmp, target := writeFakeDev(t)
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s not installed; skipping", shell)
			continue
		}
		t.Run(shell, func(t *testing.T) {
			wrapper := fmt.Sprintf(posixInit, shellQuote(fake), shell)
			script := wrapper + `
dev stream
stream_status=$?
dev fail
fail_status=$?
set -e
dev cd "$DEV_TEST_TARGET"
cd_status=$?
printf 'stream_status=%s\nfail_status=%s\ncd_status=%s\npwd=%s\n' \
  "$stream_status" "$fail_status" "$cd_status" "$PWD"
dev __complete
if [ -n "$(find "$TMPDIR" -name 'dev-cd.*' -print -quit)" ]; then
  printf 'leaked=yes\n'
else
  printf 'leaked=no\n'
fi
`
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "DEV_TEST_TARGET="+target)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("run wrapper: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			for _, want := range []string{
				"streamed stdout\n", "stream_status=0\n", "fail_status=7\n",
				"cd_status=0\n", "pwd=" + target + "\n", "completion-direct\n", "leaked=no\n",
			} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout missing %q:\n%s", want, stdout.String())
				}
			}
			if !strings.Contains(stderr.String(), "streamed stderr\n") {
				t.Errorf("stderr was not passed through:\n%s", stderr.String())
			}
		})
	}
}

func TestPosixShellWrapperFallsBackFromStaleTMPDIR(t *testing.T) {
	fake, _, target := writeFakeDev(t)
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			wrapper := fmt.Sprintf(posixInit, shellQuote(fake), shell)
			script := wrapper + `
dev status
set -e
dev cd "$DEV_TEST_TARGET"
printf 'pwd=%s\n' "$PWD"
`
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(),
				"TMPDIR="+filepath.Join(t.TempDir(), "missing"),
				"DEV_TEST_TARGET="+target,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run wrapper: %v\n%s", err, out)
			}
			for _, want := range []string{"status-direct\n", "pwd=" + target + "\n"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestFishShellWrapperPreservesStreamsStatusAndCD(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not installed")
	}
	fake, tmp, target := writeFakeDev(t)
	wrapper := fmt.Sprintf(fishInit, shellQuote(fake))
	script := wrapper + `
dev stream
set stream_status $status
dev fail
set fail_status $status
dev cd "$DEV_TEST_TARGET"
set cd_status $status
printf 'stream_status=%s\nfail_status=%s\ncd_status=%s\npwd=%s\n' \
  $stream_status $fail_status $cd_status $PWD
dev __complete
if test -n (find "$TMPDIR" -name 'dev-cd.*' -print -quit)
    printf 'leaked=yes\n'
else
    printf 'leaked=no\n'
end
`
	cmd := exec.Command(fish)
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "DEV_TEST_TARGET="+target)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run wrapper: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"streamed stdout\n", "stream_status=0\n", "fail_status=7\n",
		"cd_status=0\n", "pwd=" + target + "\n", "completion-direct\n", "leaked=no\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if !strings.Contains(stderr.String(), "streamed stderr\n") {
		t.Errorf("stderr was not passed through:\n%s", stderr.String())
	}
}

func writeFakeDev(t *testing.T) (path, tmp, target string) {
	t.Helper()
	root := t.TempDir()
	tmp = filepath.Join(root, "tmp")
	target = filepath.Join(root, "target with spaces\nand a newline")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(root, "fake dev")
	body := `#!/bin/sh
case "${1:-}" in
  stream)
    printf 'streamed stdout\n'
    printf 'streamed stderr\n' >&2
    ;;
  fail)
    exit 7
    ;;
  status)
    printf 'status-direct\n'
    ;;
  cd)
    printf '%s\0' "$2" >&3
    ;;
  __complete|__completeNoDesc)
    if [ -n "${DEV_SHELL_CD_FD:-}" ]; then
      printf 'completion-wrapped\n'
    else
      printf 'completion-direct\n'
    fi
    ;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, tmp, target
}
